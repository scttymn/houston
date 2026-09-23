// Package runner is houston runner: a houston-runner-N container's loop.
// It claims a queued deploy from Mission Control, fetches exactly that
// commit with the project's deploy key, and hands the claimed deploy to
// houston deploy, which runs step 00 (the tests) and the rest.
package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sevenmoons/houston/internal/deploy"
	"github.com/sevenmoons/houston/internal/mission"
)

// ClaimWait is how long one claim waits for work (Mission Control's cap).
const ClaimWait = 25

type Mission interface {
	Claim(ctx context.Context, runner string, wait int) (mission.Job, bool, error)
	Report(ctx context.Context, d mission.Deploy, p mission.Progress) error
}

// Git runs git in dir with env and returns its combined output.
type Git interface {
	Run(ctx context.Context, dir string, env []string, args ...string) (string, error)
}

type Runner struct {
	Name      string // houston-runner-N
	Workspace string // a checkout per project, and their keys
	Mission   Mission
	Git       Git
	Deploy    func(ctx context.Context, o deploy.Options) int
	Sleep     func(ctx context.Context, d time.Duration)
	// Base is what every deploy gets (arch, SSH dir, environment, output,
	// the houston binary); File, Ref, Claimed and RunTests come per job.
	Base deploy.Options
}

// Run polls until ctx ends. Mission Control being away (a restart, a token
// being fixed) is retried with backoff, never fatal.
func (r *Runner) Run(ctx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		if _, err := r.RunOnce(ctx); err != nil {
			r.logf("%s: can't claim work: %v; trying again in %s\n", r.Name, err, backoff)
			r.Sleep(ctx, backoff)
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
	}
	return ctx.Err()
}

// RunOnce claims and runs at most one deploy. It reports whether there was one.
func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	job, ok, err := r.Mission.Claim(ctx, r.Name, ClaimWait)
	if err != nil || !ok {
		return false, err
	}
	r.logf("%s: deploy #%d of %s at %s\n", r.Name, job.Deploy.Number, job.Project.Name, job.SHA[:7])
	dir, msg := r.fetch(ctx, job)
	if msg != "" {
		r.Mission.Report(ctx, job.Deploy, mission.Progress{Log: "NO-GO: " + msg + "\n", Status: "no_go", Error: msg})
		return true, nil
	}
	o := r.Base
	o.File = filepath.Join(dir, filepath.FromSlash(job.Project.ComposePath))
	o.Ref = job.Ref
	claimed := job.Deploy
	o.Claimed = &claimed
	o.RunTests = true
	r.Deploy(ctx, o)
	return true, nil
}

// fetch puts exactly the claimed commit in <workspace>/<project>, the ref
// pointing at it. ssh only trusts the host keys Mission Control recorded.
func (r *Runner) fetch(ctx context.Context, job mission.Job) (string, string) {
	dir := filepath.Join(r.Workspace, job.Project.Name)
	keys := filepath.Join(r.Workspace, ".keys")
	key := filepath.Join(keys, job.Project.Name)
	knownHosts := key + ".known_hosts"
	if err := os.MkdirAll(keys, 0o700); err != nil {
		return "", err.Error()
	}
	deployKey := job.Project.DeployKey
	if !strings.HasSuffix(deployKey, "\n") {
		deployKey += "\n"
	}
	for path, data := range map[string]string{key: deployKey, knownHosts: strings.Join(job.KnownHosts, "\n") + "\n"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			return "", err.Error()
		}
		os.Chmod(path, 0o600)
	}
	env := append(append([]string{}, r.Base.Environ...),
		"GIT_SSH_COMMAND=ssh -i "+key+" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=yes -o UserKnownHostsFile="+knownHosts,
		"GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=ssh:https")

	steps := [][]string{
		{"fetch", "--depth", "1", "--no-tags", "--", job.Project.RepoURL, job.SHA},
		{"checkout", "--force", "--detach", job.SHA},
		{"clean", "-ffdxq"},
		{"update-ref", job.Ref, job.SHA},
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err.Error()
		}
		steps = append([][]string{{"init", "-q"}}, steps...)
	}
	for _, args := range steps {
		if out, err := r.Git.Run(ctx, dir, env, args...); err != nil {
			return "", fmt.Sprintf("couldn't fetch %s (git %s): %s", job.SHA[:7], args[0], firstLine(out, err))
		}
	}
	return dir, ""
}

func firstLine(out string, err error) string {
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return err.Error()
}

func (r *Runner) logf(format string, args ...any) {
	if w := r.Base.Stderr; w != nil {
		fmt.Fprintf(w, format, args...)
	}
}

// Sleep waits d or until ctx ends.
func Sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

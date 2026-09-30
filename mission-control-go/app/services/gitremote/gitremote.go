// Package gitremote reads a project's repo with git over ssh (the Rails
// app's GitRemote): its deploy key in a temporary file, only host keys
// Mission Control recorded (a new host's is recorded on first contact, a
// changed one refused), 60 s at most a command, and git's errors in one
// line with what to do about the usual ones.
package gitremote

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/scttymn/houston/internal/mission"
	"github.com/scttymn/houston/internal/project"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
)

// Timeout is how long a git command may take.
const Timeout = 60 * time.Second

// Git runs git commands for Mission Control.
type Git struct {
	// Run runs argv (a real one: Exec; a test's fake).
	Run func(ctx context.Context, argv []string, env map[string]string) dockercmd.Result
	// KnownHosts is Mission Control's file of git hosts' keys.
	KnownHosts string
	// TempDir is where keys are written while a command runs ("": the
	// system's).
	TempDir string
}

// Exec runs argv as a process, stopped after Timeout.
func Exec(ctx context.Context, argv []string, env map[string]string) dockercmd.Result {
	return dockercmd.CLI{Bin: argv[0]}.Run(ctx, argv[1:], dockercmd.Opts{Env: env, Timeout: Timeout})
}

// Link is what reading a repo needs: its address, the deploy key, and the
// branch and compose file where they matter.
type Link struct {
	RepoURL, DeployKey, Branch, ComposePath string
}

// withKey runs fn with the deploy key written to a file of its own, and
// the environment git needs to use it; the file goes after.
func (g Git) withKey(link Link, fn func(env map[string]string, key, dir string) error) error {
	dir, err := os.MkdirTemp(g.TempDir, "houston-git")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	key := filepath.Join(dir, "deploy_key")
	content := link.DeployKey
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(key, []byte(content), 0o600); err != nil {
		return err
	}
	env := map[string]string{
		"GIT_SSH_COMMAND": "ssh -i " + key + " -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=10 " +
			"-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + g.KnownHosts,
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_ALLOW_PROTOCOL":  "ssh:https",
	}
	return fn(env, key, dir)
}

var trouble = regexp.MustCompile(`(?i)denied|fatal|error|could not|not found|timed out`)

// explain is git's answer in one line, with what to do about the usual
// ones; the key file's path is never shown.
func (g Git) explain(output, key string) string {
	text := strings.ReplaceAll(output, key, "<deploy key>")
	if strings.Contains(text, "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		return "the git host's host key changed since Houston first saw it. If that's expected, remove its line from " + g.KnownHosts + " on the server, then check again."
	}
	lines := strings.Split(text, "\n")
	line := strings.TrimSpace(lines[0])
	for _, l := range lines {
		if trouble.MatchString(l) {
			line = strings.TrimSpace(l)
			break
		}
	}
	if strings.Contains(line, "Permission denied") {
		line += ". Add the deploy key above to the repo as a read-only deploy key, then check again."
	}
	if line == "" {
		return "git failed without saying why"
	}
	return line
}

// Check is whether Houston can read the repo and it has the branch: "" when
// it can, else why not.
func (g Git) Check(ctx context.Context, link Link) (ok bool, message string) {
	g.withKey(link, func(env map[string]string, key, _ string) error {
		r := g.Run(ctx, []string{"git", "ls-remote", "--heads", "--", link.RepoURL}, env)
		if !r.OK {
			message = g.explain(r.Output, key)
			return nil
		}
		for _, line := range strings.Split(r.Output, "\n") {
			if _, ref, found := strings.Cut(strings.TrimSpace(line), "\t"); found && ref == "refs/heads/"+link.Branch {
				ok, message = true, "Houston can read the repo"
				return nil
			}
		}
		message = "Houston can read the repo, but it has no branch " + link.Branch
		return nil
	})
	return ok, message
}

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Refs are every branch and tag, ref → commit (with peeled ^{} entries
// for annotated tags), or why they couldn't be read.
func (g Git) Refs(ctx context.Context, link Link) (map[string]string, string) {
	refs := map[string]string{}
	var problem string
	err := g.withKey(link, func(env map[string]string, key, _ string) error {
		r := g.Run(ctx, []string{"git", "ls-remote", "--heads", "--tags", "--", link.RepoURL}, env)
		if !r.OK {
			problem = g.explain(r.Output, key)
			return nil
		}
		for _, line := range strings.Split(r.Output, "\n") {
			sha, ref, found := strings.Cut(strings.TrimSpace(line), "\t")
			if found && fullSHA.MatchString(strings.ToLower(sha)) {
				refs[ref] = sha
			}
		}
		return nil
	})
	if err != nil {
		problem = err.Error()
	}
	return refs, problem
}

// Commit is whether the repo still has commit sha ("" when it has, else
// why not): a blobless shallow fetch of exactly that commit. A SHA names
// its contents, so if it's there, it's that code.
func (g Git) Commit(ctx context.Context, link Link, sha string) string {
	var problem string
	err := g.withKey(link, func(env map[string]string, key, dir string) error {
		checkout := filepath.Join(dir, "repo")
		g.Run(ctx, []string{"git", "init", "-q", checkout}, env)
		fetched := g.Run(ctx, []string{"git", "-C", checkout, "fetch", "--depth", "1", "--no-tags", "--filter=blob:none", "--", link.RepoURL, sha}, env)
		if !fetched.OK {
			problem = g.explain(fetched.Output, key)
			return nil
		}
		head := strings.TrimSpace(g.Run(ctx, []string{"git", "-C", checkout, "rev-parse", "FETCH_HEAD"}, env).Output)
		if head != sha {
			problem = "the repo answered with " + head[:min(7, len(head))] + ", not " + sha[:min(7, len(sha))]
		}
		return nil
	})
	if err != nil {
		return err.Error()
	}
	return problem
}

// Read fetches only the compose file at the branch's head and inspects it
// with houston's own parser (mission.InspectionFor), so Mission Control
// never interprets compose.yml differently from the CLI. It's the head's
// commit and what was read, or the problems in the file or in reaching it.
func (g Git) Read(ctx context.Context, link Link) (sha string, in *mission.Inspection, problems string) {
	err := g.withKey(link, func(env map[string]string, key, dir string) error {
		checkout := filepath.Join(dir, "repo")
		for _, argv := range [][]string{
			{"git", "clone", "--depth", "1", "--single-branch", "--branch", link.Branch, "--no-tags", "--filter=blob:none", "--no-checkout", "--", link.RepoURL, checkout},
			{"git", "-C", checkout, "checkout", "HEAD", "--", link.ComposePath},
		} {
			if r := g.Run(ctx, argv, env); !r.OK {
				problems = g.explain(r.Output, key)
				return nil
			}
		}
		sha = strings.TrimSpace(g.Run(ctx, []string{"git", "-C", checkout, "rev-parse", "HEAD"}, env).Output)
		p, err := project.Load(filepath.Join(checkout, link.ComposePath))
		if err != nil {
			problems = strings.TrimSpace(strings.ReplaceAll(err.Error(), checkout+"/", ""))
			return nil
		}
		inspected := mission.InspectionFor(p)
		in = &inspected
		return nil
	})
	if err != nil {
		problems = err.Error()
	}
	return sha, in, problems
}

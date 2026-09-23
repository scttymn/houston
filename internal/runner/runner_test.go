package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sevenmoons/houston/internal/deploy"
	"github.com/sevenmoons/houston/internal/mission"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func job() mission.Job {
	return mission.Job{
		Deploy: mission.Deploy{ID: 7, Number: 3, Token: "tok"}, SHA: sha, Ref: "refs/heads/main",
		Project:    mission.JobProject{Name: "garage", RepoURL: "git@forgejo:houston/garage.git", Branch: "main", ComposePath: "deploy/compose.yml", DeployKey: "-----BEGIN KEY-----\nk\n-----END KEY-----\n"},
		KnownHosts: []string{"forgejo ssh-ed25519 AAAAhost"},
	}
}

type fakeMission struct {
	jobs     []mission.Job
	claimErr error
	claims   atomic.Int32
	reports  []mission.Progress
}

func (m *fakeMission) Claim(ctx context.Context, runner string, wait int) (mission.Job, bool, error) {
	m.claims.Add(1)
	if m.claimErr != nil {
		return mission.Job{}, false, m.claimErr
	}
	if len(m.jobs) == 0 {
		return mission.Job{}, false, nil
	}
	j := m.jobs[0]
	m.jobs = m.jobs[1:]
	return j, true, nil
}

func (m *fakeMission) Report(ctx context.Context, d mission.Deploy, p mission.Progress) error {
	m.reports = append(m.reports, p)
	return nil
}

type gitCall struct {
	dir  string
	env  []string
	args []string
}

type fakeGit struct {
	calls  []gitCall
	failOn string
	onCall func(gitCall)
}

func (g *fakeGit) Run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	c := gitCall{dir, env, args}
	g.calls = append(g.calls, c)
	if g.onCall != nil {
		g.onCall(c)
	}
	if g.failOn != "" && args[0] == g.failOn {
		return "fatal: couldn't find remote ref " + sha + "\n", errors.New("exit status 128")
	}
	if args[0] == "init" {
		os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	}
	return "", nil
}

func setup(t *testing.T) (*Runner, *fakeMission, *fakeGit, *[]deploy.Options) {
	t.Helper()
	m, g := &fakeMission{}, &fakeGit{}
	var deploys []deploy.Options
	r := &Runner{
		Name: "houston-runner-1", Workspace: t.TempDir(), Mission: m, Git: g,
		Deploy: func(ctx context.Context, o deploy.Options) int { deploys = append(deploys, o); return 0 },
		Sleep:  func(ctx context.Context, d time.Duration) {},
	}
	return r, m, g, &deploys
}

func TestRunnerFetchesTheClaimedCommit(t *testing.T) {
	r, m, g, _ := setup(t)
	m.jobs = []mission.Job{job(), job()}
	var keyMode os.FileMode
	var knownHosts string
	g.onCall = func(c gitCall) {
		if c.args[0] == "fetch" {
			for _, kv := range c.env {
				if v, ok := strings.CutPrefix(kv, "GIT_SSH_COMMAND="); ok {
					key := strings.Fields(v)[2]
					info, _ := os.Stat(key)
					keyMode = info.Mode().Perm()
					kh := v[strings.Index(v, "UserKnownHostsFile=")+len("UserKnownHostsFile="):]
					data, _ := os.ReadFile(strings.Fields(kh)[0])
					knownHosts = string(data)
				}
			}
		}
	}

	if handled, err := r.RunOnce(context.Background()); !handled || err != nil {
		t.Fatalf("RunOnce = %v, %v", handled, err)
	}
	dir := filepath.Join(r.Workspace, "garage")
	var got [][]string
	for _, c := range g.calls {
		if c.dir != dir {
			t.Errorf("git ran in %s, want %s", c.dir, dir)
		}
		got = append(got, c.args)
	}
	want := [][]string{
		{"init", "-q"},
		{"fetch", "--depth", "1", "--no-tags", "--", "git@forgejo:houston/garage.git", sha},
		{"checkout", "--force", "--detach", sha},
		{"clean", "-ffdxq"},
		{"update-ref", "refs/heads/main", sha},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("git calls =\n%v\nwant\n%v", got, want)
	}
	env := g.calls[1].env
	ssh := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_SSH_COMMAND="); ok {
			ssh = v
		}
	}
	for _, opt := range []string{"-o StrictHostKeyChecking=yes", "-o IdentitiesOnly=yes", "-o BatchMode=yes"} {
		if !strings.Contains(ssh, opt) {
			t.Errorf("GIT_SSH_COMMAND lacks %q: %s", opt, ssh)
		}
	}
	if !slices.Contains(env, "GIT_ALLOW_PROTOCOL=ssh:https") || !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("git env = %v", env)
	}
	if keyMode != 0o600 || knownHosts != "forgejo ssh-ed25519 AAAAhost\n" {
		t.Errorf("key mode %o, known_hosts %q", keyMode, knownHosts)
	}

	g.calls = nil
	r.RunOnce(context.Background())
	if g.calls[0].args[0] != "fetch" {
		t.Errorf("second job: first git call %v; the checkout is reused", g.calls[0].args)
	}
}

func TestRunnerHandsTheDeployOver(t *testing.T) {
	r, m, _, deploys := setup(t)
	m.jobs = []mission.Job{job()}
	r.RunOnce(context.Background())
	if len(*deploys) != 1 {
		t.Fatalf("deploy called %d times", len(*deploys))
	}
	o := (*deploys)[0]
	if o.Claimed == nil || *o.Claimed != job().Deploy || !o.RunTests || o.Ref != "refs/heads/main" ||
		o.File != filepath.Join(r.Workspace, "garage", "deploy", "compose.yml") {
		t.Errorf("deploy options = %+v", o)
	}
}

func TestRunnerReportsAFailedFetch(t *testing.T) {
	r, m, g, deploys := setup(t)
	m.jobs = []mission.Job{job()}
	g.failOn = "fetch"
	r.RunOnce(context.Background())
	if len(*deploys) != 0 {
		t.Error("deployed after a failed fetch")
	}
	last := m.reports[len(m.reports)-1]
	if last.Status != "no_go" || !strings.Contains(last.Error, "couldn't find remote ref") {
		t.Errorf("report = %+v", last)
	}
}

func TestRunnerKeepsPolling(t *testing.T) {
	r, m, _, _ := setup(t)
	m.claimErr = errors.New("can't reach Mission Control: connection refused")
	var waits []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	r.Sleep = func(_ context.Context, d time.Duration) {
		waits = append(waits, d)
		if len(waits) == 7 {
			cancel()
		}
	}
	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	if !reflect.DeepEqual(waits, want) {
		t.Errorf("backoff = %v, want %v", waits, want)
	}
	if m.claims.Load() != 7 {
		t.Errorf("%d claims", m.claims.Load())
	}
}

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
	head   string // what rev-parse HEAD answers; default: the job's SHA
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
	if args[0] == "rev-parse" {
		if g.head != "" {
			return g.head + "\n", nil
		}
		return sha + "\n", nil
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
		{"rev-parse", "HEAD"},
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
	if o.Claimed == nil || !reflect.DeepEqual(*o.Claimed, job().Deploy) || !o.RunTests || o.Ref != "refs/heads/main" ||
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

// A restore's data only fits its snapshot's code: the checkout must be the
// job's exact commit, for every deploy, or nothing runs.
func TestCheckoutIsTheJobsCommit(t *testing.T) {
	r, m, g, deploys := setup(t)
	m.jobs = []mission.Job{job()}
	g.head = "fedcba9876543210fedcba9876543210fedcba98"
	r.RunOnce(context.Background())
	if len(*deploys) != 0 {
		t.Error("deployed a checkout that isn't the claimed commit")
	}
	last := m.reports[len(m.reports)-1]
	if last.Status != "no_go" || !strings.Contains(last.Error, "not the claimed commit "+sha) {
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

type fakeDocker struct {
	projects string            // docker compose ls --all --format json
	created  map[string]string // project → its containers' creation times, one per line
	downs    [][]string
	lsErr    error
}

func (d *fakeDocker) Output(args ...string) ([]byte, error) {
	switch {
	case args[0] == "compose" && args[1] == "ls":
		return []byte(d.projects), d.lsErr
	case args[0] == "ps":
		project := strings.TrimPrefix(args[slices.Index(args, "--filter")+1], "label=com.docker.compose.project=")
		return []byte(d.created[project]), nil
	case args[0] == "compose" && slices.Contains(args, "down"):
		d.downs = append(d.downs, args)
		return nil, nil
	}
	return nil, errors.New("unexpected docker " + strings.Join(args, " "))
}

func TestSweepRemovesOnlyStaleTestProjects(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	old, young := now.Add(-3*time.Hour).Format("2006-01-02 15:04:05 -0700 MST"), now.Add(-10*time.Minute).Format("2006-01-02 15:04:05 -0700 MST")
	d := &fakeDocker{
		projects: `[{"Name":"garage-test-0a1b2c3d"},{"Name":"spike-test-ffffffff"},{"Name":"garage-test-dev"},{"Name":"garage"},{"Name":"mixed-test-12345678"}]`,
		created: map[string]string{
			"garage-test-0a1b2c3d": old + "\n" + old + "\n",
			"spike-test-ffffffff":  young + "\n",
			"garage-test-dev":      old + "\n",
			"garage":               old + "\n",
			"mixed-test-12345678":  old + "\n" + young + "\n",
		},
	}
	var log strings.Builder
	Sweep(d, now, &log)

	want := [][]string{{"compose", "-p", "garage-test-0a1b2c3d", "down", "-v", "--rmi", "local", "--remove-orphans"}}
	if !reflect.DeepEqual(d.downs, want) {
		t.Errorf("downs = %v, want %v", d.downs, want)
	}
	if !strings.Contains(log.String(), "garage-test-0a1b2c3d") {
		t.Errorf("log = %q", log.String())
	}

	d = &fakeDocker{lsErr: errors.New("Cannot connect to the Docker daemon")}
	log.Reset()
	Sweep(d, now, &log)
	if len(d.downs) != 0 || !strings.Contains(log.String(), "Cannot connect") {
		t.Errorf("docker error: downs %v, log %q", d.downs, log.String())
	}
}

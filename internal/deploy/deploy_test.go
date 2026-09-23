package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sevenmoons/houston/internal/kamal"
	"github.com/sevenmoons/houston/internal/mission"
	"github.com/sevenmoons/houston/internal/project"
)

const (
	sha         = "0123456789abcdef0123456789abcdef01234567"
	kamalImage  = "ghcr.io/basecamp/kamal:v2.12.0"
	shopCompose = `name: shop
services:
  app:
    build: { context: ., target: dev }
    ports: ["3000:3000"]
    environment:
      DATABASE_URL: postgres://postgres:${POSTGRES_PASSWORD}@${DB_HOST:-db}/shop
      SECRET_KEY_BASE: ${SECRET_KEY_BASE}
      MODE: production
    volumes:
      - storage:/rails/storage
  db:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  storage:
  pgdata:
x-houston:
  health: /up
  deploy: { on: commit, branch: main }
  hooks:
    release: bin/rails db:migrate
    post_deploy: bin/rails runner 'Cache.warm'
`
)

// fakeDocker records docker calls; exit decides each one's exit code.
type fakeDocker struct {
	mu       sync.Mutex
	calls    []dockerCall
	outputs  [][]string
	labels   map[string]string // container → its houston.config label
	exit     func(what string) int
	onStream func(what string, args []string)
	// block, when it says so, holds a call until its context ends.
	block func(what string) bool
	// hold keeps a call running this long (without watching the context).
	hold func(what string) time.Duration
	// emit is extra output a call writes.
	emit func(what string) string
}

type dockerCall struct {
	what string // build, push, kamal <args>, release, post_deploy
	args []string
	env  []string
}

func (f *fakeDocker) Output(args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outputs = append(f.outputs, args)
	switch args[0] {
	case "inspect":
		return []byte(f.labels[args[len(args)-1]] + "\n"), nil
	case "rm":
		return nil, nil
	}
	return nil, errors.New("unexpected docker " + strings.Join(args, " "))
}

func (f *fakeDocker) Stream(ctx context.Context, dir string, env []string, out io.Writer, args ...string) (int, error) {
	what := describe(args)
	f.mu.Lock()
	f.calls = append(f.calls, dockerCall{what: what, args: args, env: env})
	f.mu.Unlock()
	io.WriteString(out, "output of "+what+"\n")
	if f.emit != nil {
		io.WriteString(out, f.emit(what))
	}
	if f.onStream != nil {
		f.onStream(what, args)
	}
	if f.hold != nil {
		time.Sleep(f.hold(what))
	}
	if f.block != nil && f.block(what) {
		<-ctx.Done()
		return 1, ctx.Err()
	}
	if f.exit != nil {
		return f.exit(what), nil
	}
	return 0, nil
}

func (f *fakeDocker) removed() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, o := range f.outputs {
		if o[0] == "rm" {
			out = append(out, o)
		}
	}
	return out
}

func (f *fakeDocker) whats() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.what)
	}
	return out
}

func (f *fakeDocker) call(what string) dockerCall {
	for _, c := range f.calls {
		if c.what == what {
			return c
		}
	}
	return dockerCall{}
}

func describe(args []string) string {
	switch {
	case args[0] == "build" || args[0] == "push":
		return args[0]
	case args[0] == "exec":
		return "post_deploy"
	case args[0] == "run" && slices.Contains(args, "houston-kamal-shop"):
		return "kamal " + strings.Join(args[slices.Index(args, kamalImage)+1:], " ")
	case args[0] == "run" && slices.Contains(args, "shop-release-0123456"):
		return "release"
	}
	return strings.Join(args, " ")
}

// fakeExec records non-docker commands (houston test, for step 00).
type fakeExec struct {
	calls [][]string
	exit  int
}

func (e *fakeExec) Stream(ctx context.Context, dir string, env []string, out io.Writer, name string, args ...string) (int, error) {
	e.calls = append(e.calls, append([]string{name}, args...))
	io.WriteString(out, "output of the tests\n")
	return e.exit, nil
}

type fakeGit struct {
	head, branch, status string
	refs                 map[string]string
}

func (g *fakeGit) Output(dir string, args ...string) (string, error) {
	switch strings.Join(args, " ") {
	case "rev-parse HEAD":
		return g.head, nil
	case "status --porcelain":
		return g.status, nil
	case "symbolic-ref -q HEAD":
		return g.branch, nil
	}
	if args[0] == "rev-parse" && args[1] == "--verify" {
		ref := strings.TrimSuffix(args[3], "^{commit}")
		if sha, ok := g.refs[ref]; ok {
			return sha, nil
		}
		return "", errors.New("unknown ref")
	}
	return "", errors.New("unexpected git " + strings.Join(args, " "))
}

type fakeMission struct {
	mu    sync.Mutex
	calls []string
	// The pre-deploy snapshot: what the POST answers, then each GET in turn
	// (the last one repeats). Default: nothing deployed yet, skipped.
	snapshot     mission.Snapshot
	snapshotErr  error
	snapshotPoll []mission.Snapshot
	onSnapshot   func() // called when the snapshot is asked for
	generation   int    // what sync reports
	syncErr      error
	secrets      map[string]string
	startErr     error
	deploy       mission.Deploy
	reports      []mission.Progress
	reportErr    func(p mission.Progress) error
	domains      map[string]mission.DomainState
}

func (m *fakeMission) Sync(ctx context.Context, req mission.SyncRequest) (mission.SyncResult, error) {
	m.calls = append(m.calls, "sync")
	if m.syncErr != nil {
		return mission.SyncResult{}, m.syncErr
	}
	return mission.SyncResult{Generation: m.generation, Project: req.Name, Host: req.Name + ".svnmns.com", DNS: "per_host", Domains: m.domains}, nil
}

func (m *fakeMission) Secret(ctx context.Context, project, key string) (string, bool, error) {
	v, ok := m.secrets[key]
	return v, ok, nil
}

func (m *fakeMission) Snapshot(ctx context.Context, d mission.Deploy) (mission.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "snapshot")
	if m.onSnapshot != nil {
		m.onSnapshot()
	}
	if m.snapshotErr != nil {
		return mission.Snapshot{}, m.snapshotErr
	}
	if m.snapshot.Status == "" {
		return mission.Snapshot{Status: "skipped", Error: "nothing deployed yet"}, nil
	}
	return m.snapshot, nil
}

func (m *fakeMission) SnapshotStatus(ctx context.Context, d mission.Deploy) (mission.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "snapshot?")
	s := m.snapshotPoll[0]
	if len(m.snapshotPoll) > 1 {
		m.snapshotPoll = m.snapshotPoll[1:]
	}
	return s, nil
}

func (m *fakeMission) StartDeploy(ctx context.Context, project, sha, ref string) (mission.Deploy, error) {
	m.calls = append(m.calls, "start "+sha+" "+ref)
	if m.startErr != nil {
		return mission.Deploy{}, m.startErr
	}
	return m.deploy, nil
}

func (m *fakeMission) Report(ctx context.Context, d mission.Deploy, p mission.Progress) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reportErr != nil {
		if err := m.reportErr(p); err != nil {
			return err
		}
	}
	m.reports = append(m.reports, p)
	return nil
}

func (m *fakeMission) heartbeats() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.reports {
		if r.Step == "" && r.Status == "" {
			n++
		}
	}
	return n
}

func (m *fakeMission) final() mission.Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.reports) - 1; i >= 0; i-- {
		if m.reports[i].Status != "" {
			return m.reports[i]
		}
	}
	return mission.Progress{}
}

func (m *fakeMission) log() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, r := range m.reports {
		b.WriteString(r.Log)
	}
	return b.String()
}

type harness struct {
	dir     string
	docker  *fakeDocker
	git     *fakeGit
	mission *fakeMission
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	ref     string
	timing  Options // Timeout, HeartbeatEvery, FenceAfter; zero means the defaults
	exec    *fakeExec
	claimed *mission.Deploy
	tests   bool
	// snapshotEvery: how often the pre-deploy snapshot is polled.
	snapshotEvery time.Duration
}

// reported: some progress report named step.
func (h *harness) reported(step string) bool {
	for _, r := range h.mission.reports {
		if r.Step == step {
			return true
		}
	}
	return false
}

// finishedWith: the deploy was finished with status and error.
func (h *harness) finishedWith(status, err string) bool {
	for _, r := range h.mission.reports {
		if r.Status == status {
			return r.Error == err
		}
	}
	return false
}

// indexOf is the first call in whats that starts with prefix, or -1.
func indexOf(whats []string, prefix string) int {
	for i, w := range whats {
		if strings.HasPrefix(w, prefix) {
			return i
		}
	}
	return -1
}

func newHarness(t *testing.T, compose string) *harness {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	// The accessories' running labels match deploy.yml unless a test says not.
	p, err := project.Load(filepath.Join(dir, "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for name, label := range kamal.AccessoryLabels(p, 1) {
		labels[p.Name+"-"+name] = label
	}
	return &harness{
		dir:     dir,
		docker:  &fakeDocker{labels: labels},
		exec:    &fakeExec{},
		git:     &fakeGit{head: sha, branch: "refs/heads/main", refs: map[string]string{"refs/heads/main": sha}},
		mission: &fakeMission{secrets: map[string]string{"POSTGRES_PASSWORD": "pw", "SECRET_KEY_BASE": "skb"}, deploy: mission.Deploy{ID: 9, Number: 4, Token: "t"}},
	}
}

func (h *harness) run() int {
	return Run(context.Background(), Options{
		File:           filepath.Join(h.dir, "compose.yml"),
		Ref:            h.ref,
		Arch:           "arm64",
		SSHDir:         "/home/houston/.ssh",
		Environ:        []string{"PATH=/usr/bin", "HOME=/home/houston"},
		Stdout:         &h.stdout,
		Stderr:         &h.stderr,
		Timeout:        h.timing.Timeout,
		HeartbeatEvery: h.timing.HeartbeatEvery,
		FenceAfter:     h.timing.FenceAfter,
		Claimed:        h.claimed,
		RunTests:       h.tests,
		Houston:        "/usr/local/bin/houston",
		SnapshotEvery:  h.snapshotEvery,
	}, Deps{Docker: h.docker, Git: h.git, Mission: h.mission, Exec: h.exec})
}

func TestDeployHappyPath(t *testing.T) {
	h := newHarness(t, shopCompose)
	var releaseEnv string
	var releaseEnvMode os.FileMode
	h.docker.onStream = func(what string, args []string) {
		if what == "release" {
			path := args[slices.Index(args, "--env-file")+1]
			data, _ := os.ReadFile(path)
			info, _ := os.Stat(path)
			releaseEnv, releaseEnvMode = string(data), info.Mode().Perm()
		}
	}

	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}

	if want := []string{"sync", "start " + sha + " refs/heads/main", "snapshot"}; !reflect.DeepEqual(h.mission.calls, want) {
		t.Errorf("mission calls = %v, want %v", h.mission.calls, want)
	}
	want := []string{"build", "push", "kamal accessory boot all --version " + sha, "release", "kamal deploy --skip-push --version " + sha, "post_deploy"}
	if got := h.docker.whats(); !reflect.DeepEqual(got, want) {
		t.Fatalf("docker calls:\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	image := "127.0.0.1:5000/shop:" + sha
	if got := h.docker.call("build").args; !reflect.DeepEqual(got, []string{"build", "--target", "production", "--label", "service=shop", "-t", image, "-f", filepath.Join(h.dir, "Dockerfile"), h.dir}) {
		t.Errorf("build = %v", got)
	}
	if got := h.docker.call("push").args; !reflect.DeepEqual(got, []string{"push", image}) {
		t.Errorf("push = %v", got)
	}
	kamalCall := h.docker.call("kamal deploy --skip-push --version " + sha).args
	prefix := []string{"run", "--rm", "--name", "houston-kamal-shop", "--network", "host",
		"-v", filepath.Join(h.dir, ".houston", "kamal") + ":/workdir", "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", "/home/houston/.ssh:/ssh:ro"}
	if !reflect.DeepEqual(kamalCall[:len(prefix)], prefix) {
		t.Errorf("kamal container = %v", kamalCall)
	}
	release := h.docker.call("release").args
	if want := []string{"--network", "kamal"}; !slices.Contains(release, "kamal") || release[slices.Index(release, "--network")+1] != want[1] {
		t.Errorf("release network: %v", release)
	}
	if got := release[len(release)-5:]; !reflect.DeepEqual(got, []string{"shop_storage:/rails/storage", image, "sh", "-c", "bin/rails db:migrate"}) {
		t.Errorf("release tail = %v", got)
	}
	if !strings.Contains(releaseEnv, "DATABASE_URL=postgres://postgres:pw@shop-db/shop\n") || !strings.Contains(releaseEnv, "MODE=production\n") || releaseEnvMode != 0o600 {
		t.Errorf("release env file (%o):\n%s", releaseEnvMode, releaseEnv)
	}
	if _, err := os.Stat(release[slices.Index(release, "--env-file")+1]); !os.IsNotExist(err) {
		t.Errorf("release env file left behind: %v", err)
	}
	if got := h.docker.call("post_deploy").args; !reflect.DeepEqual(got, []string{"exec", "shop-web-" + sha, "sh", "-c", "bin/rails runner 'Cache.warm'"}) {
		t.Errorf("post_deploy = %v", got)
	}

	kdir := filepath.Join(h.dir, ".houston", "kamal")
	for path, mode := range map[string]os.FileMode{kdir: 0o700, filepath.Join(kdir, "config", "deploy.yml"): 0o600, filepath.Join(kdir, ".kamal", "secrets"): 0o600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v, mode %o, want %o", path, err, info.Mode().Perm(), mode)
		}
	}
	p, _ := project.Load(filepath.Join(h.dir, "compose.yml"))
	wantConfig, _ := kamal.Config(p, kamal.Target{BaseDomain: "svnmns.com", Arch: "arm64"})
	if got, _ := os.ReadFile(filepath.Join(kdir, "config", "deploy.yml")); !bytes.Equal(got, wantConfig) {
		t.Errorf("deploy.yml isn't kamal.Config's output")
	}

	if final := h.mission.final(); final.Status != "go" {
		t.Errorf("final report = %+v", final)
	}
	if !strings.Contains(h.mission.log(), "output of build") || !strings.Contains(h.stdout.String(), "output of build") {
		t.Error("step output should reach both the terminal and the deploy's log")
	}
}

func TestDeployChecksBeforeAnything(t *testing.T) {
	tagged := strings.Replace(shopCompose, "deploy: { on: commit, branch: main }", `deploy: { on: tag, tags: "v*" }`, 1)
	for _, tc := range []struct {
		name, compose, ref string
		git                func(*fakeGit)
		want               int
	}{
		{name: "dirty worktree", compose: shopCompose, git: func(g *fakeGit) { g.status = " M app/models/user.rb" }, want: 2},
		{name: "branch the rule doesn't deploy", compose: shopCompose, ref: "refs/heads/dev", git: func(g *fakeGit) { g.refs["refs/heads/dev"] = sha }, want: 2},
		{name: "tag outside the glob", compose: tagged, ref: "refs/tags/beta1", git: func(g *fakeGit) { g.refs["refs/tags/beta1"] = sha }, want: 2},
		{name: "ref not at HEAD", compose: shopCompose, git: func(g *fakeGit) { g.refs["refs/heads/main"] = strings.Repeat("f", 40) }, want: 2},
		{name: "detached HEAD with no --ref", compose: shopCompose, git: func(g *fakeGit) { g.branch = "" }, want: 2},
		{name: "a tag the glob allows", compose: tagged, ref: "refs/tags/v1.2", git: func(g *fakeGit) { g.refs["refs/tags/v1.2"] = sha }, want: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.compose)
			h.ref = tc.ref
			tc.git(h.git)
			h.mission.syncErr = errors.New("stop here")
			code := h.run()
			if tc.want == -1 {
				if len(h.mission.calls) == 0 {
					t.Errorf("allowed ref didn't reach sync: exit %d\n%s", code, h.stderr.String())
				}
				return
			}
			if code != tc.want || len(h.mission.calls) != 0 || len(h.docker.calls) != 0 {
				t.Errorf("exit %d (want %d), mission %v, docker %v\n%s", code, tc.want, h.mission.calls, h.docker.whats(), h.stderr.String())
			}
		})
	}
}

func TestDeployHoldsForMissingSecrets(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.mission.syncErr = &mission.HoldError{Missing: []string{"POSTGRES_PASSWORD", "SECRET_KEY_BASE"}, Message: "HOLD: …"}

	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(h.stderr.String(), "POSTGRES_PASSWORD") || !strings.Contains(h.stderr.String(), "SECRET_KEY_BASE") {
		t.Errorf("stderr doesn't name the missing variables:\n%s", h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "houston secrets set POSTGRES_PASSWORD") {
		t.Errorf("stderr doesn't give an agent the CLI path:\n%s", h.stderr.String())
	}
	if !reflect.DeepEqual(h.mission.calls, []string{"sync"}) || len(h.docker.calls) != 0 {
		t.Errorf("mission %v, docker %v", h.mission.calls, h.docker.whats())
	}
}

func TestDeployWhileAnotherIsInFlight(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.mission.startErr = &mission.BusyError{Number: 4, Message: "deploy #4 is in flight (last heard from 2026-09-23T07:00:00Z)"}

	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(h.stderr.String(), "#4 is in flight") || len(h.docker.calls) != 0 {
		t.Errorf("stderr %q, docker %v", h.stderr.String(), h.docker.whats())
	}
}

func TestDeployAfterTakeoverReleasesKamalsLock(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.mission.deploy.TookOver = 3

	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	var kamalCalls []string
	for _, w := range h.docker.whats() {
		if strings.HasPrefix(w, "kamal ") {
			kamalCalls = append(kamalCalls, w)
		}
	}
	if len(kamalCalls) == 0 || kamalCalls[0] != "kamal lock release --version "+sha {
		t.Errorf("kamal calls = %v; want the lock released first", kamalCalls)
	}
	if !strings.Contains(h.mission.log(), "#3") {
		t.Error("the log should say which deploy was taken over")
	}
}

func TestDeploySecretsTravelAsEnvironmentOnly(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.mission.secrets = map[string]string{"POSTGRES_PASSWORD": "pw $HOME", "SECRET_KEY_BASE": "skb-secret-value"}
	var releaseEnv string
	h.docker.onStream = func(what string, args []string) {
		if what == "release" {
			data, _ := os.ReadFile(args[slices.Index(args, "--env-file")+1])
			releaseEnv = string(data)
		}
	}

	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	for _, c := range h.docker.calls {
		for _, a := range c.args {
			if strings.Contains(a, "pw $HOME") || strings.Contains(a, "skb-secret-value") {
				t.Errorf("%s: a secret value in the arguments: %q", c.what, a)
			}
		}
	}
	for _, text := range []string{h.mission.log(), h.stdout.String(), h.stderr.String()} {
		if strings.Contains(text, "skb-secret-value") || strings.Contains(text, "pw $HOME") {
			t.Errorf("a secret value in the output:\n%s", text)
		}
	}

	k := h.docker.call("kamal deploy --skip-push --version " + sha)
	for _, want := range []string{
		`HOUSTON_S_POSTGRES_PASSWORD=pw \$HOME`,
		`HOUSTON_S_APP__DATABASE_URL=postgres://postgres:pw \$HOME@shop-db/shop`,
		"HOUSTON_S_SECRET_KEY_BASE=skb-secret-value",
		"HOUSTON_S_KAMAL_REGISTRY_PASSWORD=houston",
		"PATH=/usr/bin",
	} {
		if !slices.Contains(k.env, want) {
			t.Errorf("kamal's docker env lacks %q: %v", want, k.env)
		}
	}
	if i := slices.Index(k.args, "HOUSTON_S_SECRET_KEY_BASE"); i < 1 || k.args[i-1] != "-e" {
		t.Errorf("kamal container isn't given HOUSTON_S_SECRET_KEY_BASE: %v", k.args)
	}
	if !strings.Contains(releaseEnv, "DATABASE_URL=postgres://postgres:pw $HOME@shop-db/shop\n") {
		t.Errorf("the release hook gets the raw value, not the carrier:\n%s", releaseEnv)
	}

	// A value Kamal can't carry stops the deploy before anything is built.
	h = newHarness(t, shopCompose)
	h.mission.secrets["SECRET_KEY_BASE"] = `back\slash`
	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if final := h.mission.final(); final.Status != "no_go" || !strings.Contains(final.Error, "SECRET_KEY_BASE") || strings.Contains(final.Error, `back\slash`) {
		t.Errorf("final report = %+v", final)
	}
	if len(h.docker.calls) != 0 {
		t.Errorf("docker ran: %v", h.docker.whats())
	}
}

func TestDeployStopsWhenTheReleaseHookFails(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.docker.exit = func(what string) int {
		if what == "release" {
			return 3
		}
		return 0
	}

	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if final := h.mission.final(); final.Status != "no_go" || !strings.Contains(final.Error, "release hook failed (exit 3)") {
		t.Errorf("final report = %+v", final)
	}
	if slices.Contains(h.docker.whats(), "kamal deploy --skip-push --version "+sha) {
		t.Error("kamal deploy ran after a failed release hook")
	}
}

func TestDeployReportsAFailedKamalDeploy(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.docker.exit = func(what string) int {
		if strings.HasPrefix(what, "kamal deploy") {
			return 1
		}
		return 0
	}

	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if final := h.mission.final(); final.Status != "no_go" || !strings.Contains(final.Error, "old version keeps serving") {
		t.Errorf("final report = %+v", final)
	}
	if slices.Contains(h.docker.whats(), "post_deploy") {
		t.Error("post_deploy ran after a failed deploy")
	}
}

func TestDeployPostDeployFailureIsOnlyLogged(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.docker.exit = func(what string) int {
		if what == "post_deploy" {
			return 2
		}
		return 0
	}

	if code := h.run(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if final := h.mission.final(); final.Status != "go" {
		t.Errorf("final report = %+v", final)
	}
	if !strings.Contains(h.mission.log(), "post_deploy hook failed (exit 2)") {
		t.Errorf("log:\n%s", h.mission.log())
	}
}

func TestDeployNeedsTheRunnerToken(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.mission.syncErr = mission.ErrUnauthorized

	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(h.stderr.String(), "refused the runner token") {
		t.Errorf("stderr:\n%s", h.stderr.String())
	}
}

func TestDeployStopsWhenTakenOver(t *testing.T) {
	h := newHarness(t, shopCompose)
	// The deadline only bounds the test if the takeover goes unnoticed.
	h.timing = Options{HeartbeatEvery: 5 * time.Millisecond, Timeout: 2 * time.Second}
	building := make(chan struct{})
	h.docker.onStream = func(what string, args []string) {
		if what == "build" {
			close(building)
		}
	}
	h.docker.block = func(what string) bool { return what == "build" }
	h.mission.reportErr = func(p mission.Progress) error {
		select {
		case <-building:
			return fmt.Errorf("%w: deploy #4 is no longer in flight (abandoned)", mission.ErrTakenOver)
		default:
			return nil
		}
	}

	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(h.stderr.String(), "taken over") {
		t.Errorf("stderr:\n%s", h.stderr.String())
	}
	if got := h.docker.whats(); !reflect.DeepEqual(got, []string{"build"}) {
		t.Errorf("docker calls = %v; nothing may run after the takeover", got)
	}
	if got := h.docker.removed(); len(got) != 1 || !slices.Contains(got[0], "houston-kamal-shop") || !slices.Contains(got[0], "shop-release-0123456") {
		t.Errorf("containers removed = %v", got)
	}
	if final := h.mission.final(); final.Status != "" {
		t.Errorf("reported %q on a deploy that isn't ours anymore", final.Status)
	}
}

func TestDeployFencesItselfWhenMissionControlIsGone(t *testing.T) {
	if FenceAfter >= 120*time.Second {
		t.Fatalf("FenceAfter = %s; it must be shorter than Mission Control's takeover threshold (Deploy::STALE_AFTER, 2 minutes)", FenceAfter)
	}
	h := newHarness(t, shopCompose)
	h.timing = Options{HeartbeatEvery: 5 * time.Millisecond, FenceAfter: 40 * time.Millisecond, Timeout: 2 * time.Second}
	h.docker.block = func(what string) bool { return what == "build" }
	var gone atomic.Bool
	h.docker.onStream = func(what string, args []string) {
		if what == "build" {
			gone.Store(true)
		}
	}
	h.mission.reportErr = func(p mission.Progress) error {
		if gone.Load() {
			return errors.New("can't reach Mission Control: connection refused")
		}
		return nil
	}

	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(h.stderr.String(), "lost touch with Mission Control") {
		t.Errorf("stderr:\n%s", h.stderr.String())
	}
	if got := h.docker.whats(); !reflect.DeepEqual(got, []string{"build", "kamal lock release --version " + sha}) {
		t.Errorf("docker calls = %v", got)
	}
	if len(h.docker.removed()) != 1 {
		t.Errorf("containers removed = %v", h.docker.removed())
	}
}

func TestDeployDeadline(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.timing = Options{Timeout: 60 * time.Millisecond}
	h.docker.block = func(what string) bool { return strings.HasPrefix(what, "kamal deploy") }

	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	whats := h.docker.whats()
	if whats[len(whats)-1] != "kamal lock release --version "+sha {
		t.Errorf("docker calls = %v; want Kamal's lock released last", whats)
	}
	if len(h.docker.removed()) != 1 {
		t.Errorf("containers removed = %v", h.docker.removed())
	}
	if final := h.mission.final(); final.Status != "no_go" || !strings.Contains(final.Error, "timed out after") {
		t.Errorf("final report = %+v", final)
	}
}

func TestDeployHeartbeats(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.timing = Options{HeartbeatEvery: 10 * time.Millisecond}
	h.docker.hold = func(what string) time.Duration {
		if what == "build" {
			return 80 * time.Millisecond
		}
		return 0
	}

	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	if n := h.mission.heartbeats(); n < 3 {
		t.Errorf("%d heartbeats during an 80 ms step at 10 ms; want at least 3", n)
	}
}

func TestDeployLogChunks(t *testing.T) {
	h := newHarness(t, shopCompose)
	big := strings.Repeat("0123456789abcdef", 700<<10/16)
	h.docker.emit = func(what string) string {
		if what == "build" {
			return big
		}
		return ""
	}

	if code := h.run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for i, r := range h.mission.reports {
		if len(r.Log) > 200<<10 {
			t.Errorf("report %d carries %d bytes of log; want ≤ 200 KiB", i, len(r.Log))
		}
	}
	if !strings.Contains(h.mission.log(), "output of build\n"+big) {
		t.Error("the chunks don't add up to the step's output")
	}
}

func TestDeployRebootsAChangedAccessory(t *testing.T) {
	p, err := project.Parse(filepath.Join(t.TempDir(), "compose.yml"), []byte(shopCompose))
	if err != nil {
		t.Fatal(err)
	}
	current := kamal.AccessoryLabels(p, 1)["db"]

	h := newHarness(t, shopCompose)
	h.docker.labels = map[string]string{"shop-db": current}
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	if slices.Contains(h.docker.whats(), "kamal accessory reboot db --version "+sha) {
		t.Error("rebooted an accessory whose config didn't change")
	}

	h = newHarness(t, shopCompose)
	h.docker.labels = map[string]string{"shop-db": "an-older-config"}
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	whats := h.docker.whats()
	boot, reboot := slices.Index(whats, "kamal accessory boot all --version "+sha), slices.Index(whats, "kamal accessory reboot db --version "+sha)
	if boot < 0 || reboot != boot+1 {
		t.Errorf("docker calls = %v; want the reboot right after boot", whats)
	}
	if !strings.Contains(h.mission.log(), "db's config changed") {
		t.Errorf("log:\n%s", h.mission.log())
	}
}

// .houston/ ignores itself, as it does for houston dev; otherwise the first
// deploy's files would make the checkout dirty and refuse the next deploy.
func TestDeployKeepsItsFilesOutOfGit(t *testing.T) {
	h := newHarness(t, shopCompose)
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(h.dir, ".houston", ".gitignore"))
	if err != nil || string(data) != "*\n" {
		t.Errorf(".houston/.gitignore = %q, %v; want \"*\\n\"", data, err)
	}
}

func TestDeployContinuesAClaimedDeploy(t *testing.T) {
	claimed := mission.Deploy{ID: 21, Number: 6, Token: "claimed", TookOver: 5}
	h := newHarness(t, shopCompose)
	h.claimed = &claimed
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	if !reflect.DeepEqual(h.mission.calls, []string{"sync", "snapshot"}) {
		t.Errorf("mission calls = %v; a claimed deploy isn't started again", h.mission.calls)
	}
	if whats := h.docker.whats(); len(whats) == 0 || whats[0] != "kamal lock release --version "+sha {
		t.Errorf("docker calls = %v; want the lock released first (took over #5)", whats)
	}

	for _, tc := range []struct {
		name  string
		setup func(*harness)
		want  string
	}{
		{"ref check", func(h *harness) { h.git.status = " M x" }, "uncommitted changes"},
		{"hold", func(h *harness) {
			h.mission.syncErr = &mission.HoldError{Missing: []string{"SECRET_KEY_BASE"}, Message: "HOLD"}
		}, "SECRET_KEY_BASE"},
		{"compose problem", func(h *harness) {
			os.WriteFile(filepath.Join(h.dir, "compose.yml"), []byte(strings.Replace(shopCompose, "health: /up", "health: up", 1)), 0o644)
		}, "x-houston.health"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, shopCompose)
			h.claimed = &claimed
			tc.setup(h)
			if code := h.run(); code == 0 {
				t.Fatal("exit 0")
			}
			final := h.mission.final()
			if final.Status != "no_go" || !strings.Contains(final.Error, tc.want) {
				t.Errorf("final report = %+v; the claimed deploy must be finished NO-GO saying %q", final, tc.want)
			}
			if len(h.docker.calls) != 0 {
				t.Errorf("docker ran: %v", h.docker.whats())
			}
		})
	}
}

func TestDeployRunsTestsFirst(t *testing.T) {
	withTests := strings.Replace(shopCompose, "  hooks:\n", "  commands: { test: bin/rails test }\n  hooks:\n", 1)
	claimed := mission.Deploy{ID: 21, Number: 6, Token: "claimed"}

	h := newHarness(t, withTests)
	h.claimed, h.tests = &claimed, true
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	if len(h.mission.reports) == 0 || h.mission.reports[0].Step != "Test" {
		t.Errorf("first report = %+v; want step Test", h.mission.reports[0])
	}
	if want := [][]string{{"/usr/local/bin/houston", "-f", filepath.Join(h.dir, "compose.yml"), "test"}}; !reflect.DeepEqual(h.exec.calls, want) {
		t.Errorf("exec calls = %v, want %v", h.exec.calls, want)
	}
	if !strings.Contains(h.mission.log(), "output of the tests") {
		t.Error("the tests' output isn't in the deploy's log")
	}

	h = newHarness(t, withTests)
	h.claimed, h.tests = &claimed, true
	h.exec.exit = 3
	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if final := h.mission.final(); final.Status != "no_go" || !strings.Contains(final.Error, "tests failed (exit 3)") {
		t.Errorf("final report = %+v", final)
	}
	if len(h.docker.calls) != 0 {
		t.Errorf("built after failing tests: %v", h.docker.whats())
	}

	h = newHarness(t, shopCompose)
	h.claimed, h.tests = &claimed, true
	if code := h.run(); code != 0 || len(h.exec.calls) != 0 || h.mission.reports[0].Step == "Test" {
		t.Errorf("no commands.test: exit %d, exec %v, first step %q", code, h.exec.calls, h.mission.reports[0].Step)
	}
}

func TestDeployLogsDomainStates(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.mission.domains = map[string]mission.DomainState{
		"shop.example.com": {State: "DNS OK"},
		"shop.app":         {State: "DNS PENDING"},
		"legacy.shop.com":  {State: "NO-GO", Reason: "legacy.shop.com has a record Houston didn't create"},
	}
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	log := h.mission.log()
	for _, want := range []string{"shop.app: DNS PENDING", "legacy.shop.com: NO-GO (legacy.shop.com has a record Houston didn't create)"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "shop.example.com") {
		t.Error("a domain that's fine isn't worth a log line")
	}
}

// The pre-deploy snapshot runs after the image is pushed and before the
// accessories: after the long build, before anything touches the data.
func TestDeploySnapshot(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.snapshotEvery = time.Millisecond
	h.mission.snapshot = mission.Snapshot{ID: 21, Status: "queued"}
	h.mission.snapshotPoll = []mission.Snapshot{{ID: 21, Status: "running"},
		{ID: 21, Status: "go", SnapshotID: "5c5edd4c" + strings.Repeat("0", 56), SHA: strings.Repeat("b", 40), Bytes: 410000000}}

	var before []string
	h.mission.onSnapshot = func() { before = h.docker.whats() }
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	if indexOf(before, "push") < 0 || indexOf(before, "kamal accessory boot") >= 0 {
		t.Errorf("docker calls before the snapshot: %v; want the push, and no accessory boot yet", before)
	}
	if !strings.Contains(h.stdout.String(), "ok  snapshot 5c5edd4c · kind:deploy sha:bbbbbbb · 391 MB") {
		t.Errorf("log:\n%s", h.stdout.String())
	}
	if !h.reported("Snapshot") {
		t.Error("no Snapshot step reported")
	}

	// A first deploy: nothing deployed yet, skipped, and the deploy goes on.
	h = newHarness(t, shopCompose)
	if code := h.run(); code != 0 || !strings.Contains(h.stdout.String(), "no snapshot: nothing deployed yet") {
		t.Errorf("skipped: exit %d\n%s", code, h.stdout.String())
	}
}

func TestDeploySnapshotStops(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.snapshotEvery = time.Millisecond
	h.mission.snapshot = mission.Snapshot{ID: 21, Status: "queued"}
	h.mission.snapshotPoll = []mission.Snapshot{{ID: 21, Status: "no_go", Error: "restic backup failed (exit 1): Fatal: repository not found"}}
	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !h.finishedWith("no_go", "pre-deploy snapshot failed: restic backup failed (exit 1): Fatal: repository not found; the old version keeps serving") {
		t.Errorf("reports: %+v", h.mission.reports)
	}
	for _, after := range []string{"kamal accessory boot", "kamal deploy"} {
		if indexOf(h.docker.whats(), after) >= 0 {
			t.Errorf("%s ran after a failed snapshot: %v", after, h.docker.whats())
		}
	}

	h = newHarness(t, shopCompose)
	h.mission.snapshotErr = errors.New("no backup storage yet (finish setup's storage step)")
	if code := h.run(); code != 1 || !h.finishedWith("no_go", "pre-deploy snapshot failed: no backup storage yet (finish setup's storage step); the old version keeps serving") {
		t.Errorf("refused: exit %d, reports %+v", code, h.mission.reports)
	}

	// The deploy's deadline passes while it waits.
	h = newHarness(t, shopCompose)
	h.snapshotEvery = time.Millisecond
	h.timing.Timeout = 300 * time.Millisecond
	h.mission.snapshot = mission.Snapshot{ID: 21, Status: "queued"}
	h.mission.snapshotPoll = []mission.Snapshot{{ID: 21, Status: "running"}}
	if code := h.run(); code != 1 || !h.finishedWith("no_go", "the pre-deploy snapshot didn't finish before the deploy's deadline; the old version keeps serving") {
		t.Errorf("deadline: exit %d, reports %+v", code, h.mission.reports)
	}
}

// Generation 2 (a restore's): Kamal's config, the release hook's volumes and
// the changed-accessory check all use generation 2's names.
func TestDeployGeneration2(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.mission.generation = 2
	p, err := project.Load(filepath.Join(h.dir, "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	h.docker.labels = map[string]string{}
	for name, label := range kamal.AccessoryLabels(p, 2) {
		h.docker.labels[p.Name+"-"+name] = label
	}
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	config, err := os.ReadFile(filepath.Join(h.dir, ".houston", "kamal", "config", "deploy.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"shop.g2_pgdata:", "db-g2:"} {
		if !strings.Contains(string(config), want) {
			t.Errorf("deploy.yml lacks %q:\n%s", want, config)
		}
	}
	if release := strings.Join(h.docker.call("release").args, " "); !strings.Contains(release, "-v shop.g2_storage:/rails/storage") {
		t.Errorf("the release hook's volumes: %s", release)
	}
	inspected := 0
	for _, args := range h.docker.outputs {
		if args[0] == "inspect" {
			inspected++
			if args[len(args)-1] != "shop-db-g2" {
				t.Errorf("an accessory checked by another generation's name: %v", args)
			}
		}
	}
	if inspected == 0 {
		t.Error("no accessory checked")
	}
}

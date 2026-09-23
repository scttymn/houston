package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

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
	calls    []dockerCall
	exit     func(what string) int
	onStream func(what string, args []string)
}

type dockerCall struct {
	what string // build, push, kamal <args>, release, post_deploy
	args []string
	env  []string
}

func (f *fakeDocker) Output(args ...string) ([]byte, error) {
	return nil, errors.New("unexpected Output")
}

func (f *fakeDocker) Stream(ctx context.Context, dir string, env []string, out io.Writer, args ...string) (int, error) {
	what := describe(args)
	f.calls = append(f.calls, dockerCall{what: what, args: args, env: env})
	io.WriteString(out, "output of "+what+"\n")
	if f.onStream != nil {
		f.onStream(what, args)
	}
	if f.exit != nil {
		return f.exit(what), nil
	}
	return 0, nil
}

func (f *fakeDocker) whats() []string {
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
	calls    []string
	syncErr  error
	secrets  map[string]string
	startErr error
	deploy   mission.Deploy
	reports  []mission.Progress
}

func (m *fakeMission) Sync(ctx context.Context, req mission.SyncRequest) (mission.SyncResult, error) {
	m.calls = append(m.calls, "sync")
	if m.syncErr != nil {
		return mission.SyncResult{}, m.syncErr
	}
	return mission.SyncResult{Project: req.Name, Host: req.Name + ".svnmns.com", DNS: "per_host"}, nil
}

func (m *fakeMission) Secret(ctx context.Context, project, key string) (string, bool, error) {
	v, ok := m.secrets[key]
	return v, ok, nil
}

func (m *fakeMission) StartDeploy(ctx context.Context, project, sha, ref string) (mission.Deploy, error) {
	m.calls = append(m.calls, "start "+sha+" "+ref)
	if m.startErr != nil {
		return mission.Deploy{}, m.startErr
	}
	return m.deploy, nil
}

func (m *fakeMission) Report(ctx context.Context, d mission.Deploy, p mission.Progress) error {
	m.reports = append(m.reports, p)
	return nil
}

func (m *fakeMission) final() mission.Progress {
	for i := len(m.reports) - 1; i >= 0; i-- {
		if m.reports[i].Status != "" {
			return m.reports[i]
		}
	}
	return mission.Progress{}
}

func (m *fakeMission) log() string {
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
}

func newHarness(t *testing.T, compose string) *harness {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	return &harness{
		dir:     dir,
		docker:  &fakeDocker{},
		git:     &fakeGit{head: sha, branch: "refs/heads/main", refs: map[string]string{"refs/heads/main": sha}},
		mission: &fakeMission{secrets: map[string]string{"POSTGRES_PASSWORD": "pw", "SECRET_KEY_BASE": "skb"}, deploy: mission.Deploy{ID: 9, Number: 4, Token: "t"}},
	}
}

func (h *harness) run() int {
	return Run(context.Background(), Options{
		File:    filepath.Join(h.dir, "compose.yml"),
		Ref:     h.ref,
		Arch:    "arm64",
		SSHDir:  "/home/houston/.ssh",
		Environ: []string{"PATH=/usr/bin", "HOME=/home/houston"},
		Stdout:  &h.stdout,
		Stderr:  &h.stderr,
	}, Deps{Docker: h.docker, Git: h.git, Mission: h.mission})
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

	if want := []string{"sync", "start " + sha + " refs/heads/main"}; !reflect.DeepEqual(h.mission.calls, want) {
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

// Package deploy is houston deploy on a Houston server: from a clean checkout
// of the project to the new version serving at <name>.<base>, with Mission
// Control told every step. See docs/plans/deploy-path.md, Batch 4.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sevenmoons/houston/internal/humanize"
	"github.com/sevenmoons/houston/internal/kamal"
	"github.com/sevenmoons/houston/internal/mission"
	"github.com/sevenmoons/houston/internal/project"
)

// KamalImage is the Kamal houston deploy runs, pinned (spike: Kamal 2.12.0).
const KamalImage = "ghcr.io/basecamp/kamal:v2.12.0"

const (
	DefaultTimeout = 30 * time.Minute
	HeartbeatEvery = 15 * time.Second
	FenceAfter     = 60 * time.Second
)

// Exit codes, as the rest of the CLI uses them.
const (
	exitFailure = 1 // the deploy didn't happen or didn't finish
	exitUsage   = 2 // the checkout or compose file can't be deployed as it is
)

type Docker interface {
	Output(args ...string) ([]byte, error)
	Stream(ctx context.Context, dir string, env []string, out io.Writer, args ...string) (int, error)
}

type Git interface {
	Output(dir string, args ...string) (string, error)
}

// Exec runs a program other than docker (houston test, for step 00).
type Exec interface {
	Stream(ctx context.Context, dir string, env []string, out io.Writer, name string, args ...string) (int, error)
}

type Mission interface {
	Sync(ctx context.Context, req mission.SyncRequest) (mission.SyncResult, error)
	Secret(ctx context.Context, project, key string) (string, bool, error)
	StartDeploy(ctx context.Context, project, sha, ref string) (mission.Deploy, error)
	Report(ctx context.Context, d mission.Deploy, p mission.Progress) error
	Snapshot(ctx context.Context, d mission.Deploy) (mission.Snapshot, error)
	SnapshotStatus(ctx context.Context, d mission.Deploy) (mission.Snapshot, error)
}

type Options struct {
	File    string    // the project's compose file, in a clean checkout
	Ref     string    // what's being deployed; default: the checked-out branch
	Arch    string    // the server's architecture, for Kamal's builder.arch
	SSHDir  string    // the houston user's ~/.ssh, mounted into Kamal's container
	Environ []string  // the environment for docker; secrets are added to it
	Stdout  io.Writer // step output, as it happens
	Stderr  io.Writer

	Timeout        time.Duration // the whole deploy; zero: DefaultTimeout
	HeartbeatEvery time.Duration // zero: HeartbeatEvery
	FenceAfter     time.Duration // zero: FenceAfter

	// Claimed is a deploy houston runner already claimed: it isn't started
	// again, and every failure finishes it NO-GO.
	Claimed *mission.Deploy
	// RunTests runs x-houston.commands.test first (step 00) with Houston.
	RunTests bool
	Houston  string // this binary, for houston test
	// SnapshotEvery: how often the pre-deploy snapshot is checked; zero: 2 s.
	SnapshotEvery time.Duration
}

type Deps struct {
	Docker  Docker
	Git     Git
	Mission Mission
	Exec    Exec
}

// Run deploys and returns the exit code.
func Run(ctx context.Context, o Options, d Deps) int {
	// Before the run proper, a failure is only printed, unless the deploy
	// was claimed: then it's finished NO-GO, so it doesn't sit in flight
	// until it goes stale.
	early := func(code int, printed, msg string) int {
		fmt.Fprint(o.Stderr, printed)
		if o.Claimed != nil {
			r := &reporter{ctx: ctx, mission: d.Mission, deploy: *o.Claimed, out: o.Stdout, cancel: func(error) {}, lastOK: time.Now(), fenceAfter: FenceAfter}
			r.logf("NO-GO: %s\n", msg)
			r.finish("no_go", msg)
		}
		return code
	}
	p, err := project.Load(o.File)
	if err != nil {
		return early(exitUsage, err.Error(), strings.TrimSpace(err.Error()))
	}
	abs, err := filepath.Abs(o.File)
	if err != nil {
		return early(exitUsage, fmt.Sprintf("houston deploy: %v\n", err), err.Error())
	}
	dir := filepath.Dir(abs)
	sha, ref, err := checkRef(d.Git, dir, o.Ref, p.Houston.Deploy)
	if err != nil {
		return early(exitUsage, fmt.Sprintf("houston deploy: %v\n", err), err.Error())
	}

	synced, err := d.Mission.Sync(ctx, mission.RequestFor(p))
	if err != nil {
		var hold *mission.HoldError
		var msg string
		switch {
		case errors.As(err, &hold):
			msg = fmt.Sprintf("HOLD: %s %s no value; set it on the project's page in Mission Control (or houston secrets set %s --project %s), then deploy again",
				strings.Join(hold.Missing, ", "), map[bool]string{true: "has", false: "have"}[len(hold.Missing) == 1], hold.Missing[0], p.Name)
		case errors.Is(err, mission.ErrUnauthorized):
			msg = "Mission Control refused the runner token (HOUSTON_TOKEN, or ~/.config/houston/runner-token)"
		default:
			msg = err.Error()
		}
		return early(exitFailure, "houston deploy: "+msg+"\n", msg)
	}
	target := kamal.Target{BaseDomain: strings.TrimPrefix(synced.Host, p.Name+"."), Arch: o.Arch, Generation: synced.Generation}
	config, err := kamal.Config(p, target)
	if err != nil {
		return early(exitUsage, err.Error(), strings.TrimSpace(err.Error()))
	}
	secretsFile, err := kamal.SecretsFile(p)
	if err != nil {
		return early(exitUsage, err.Error(), strings.TrimSpace(err.Error()))
	}

	var dep mission.Deploy
	if o.Claimed != nil {
		dep = *o.Claimed
	} else if dep, err = d.Mission.StartDeploy(ctx, p.Name, sha, ref); err != nil {
		fmt.Fprintf(o.Stderr, "houston deploy: %v\n", err)
		return exitFailure
	}
	// Every docker command runs under runCtx. It ends when the deadline
	// passes, when the deploy is taken over, or when Mission Control has been
	// unreachable for FenceAfter (see stop). Reports use ctx, so the final one
	// still goes out after a timeout.
	timeout, every, fence := orDefault(o.Timeout, DefaultTimeout), orDefault(o.HeartbeatEvery, HeartbeatEvery), orDefault(o.FenceAfter, FenceAfter)
	deadlineCtx, cancelDeadline := context.WithTimeout(ctx, timeout)
	defer cancelDeadline()
	runCtx, cancel := context.WithCancelCause(deadlineCtx)
	defer cancel(nil)

	r := &run{ctx: runCtx, o: o, d: d, p: p, generation: target.Generation, dir: dir, file: abs, sha: sha, config: config, secretsFile: secretsFile, timeout: timeout,
		image:    "127.0.0.1:5000/" + p.Name + ":" + sha,
		kamalDir: filepath.Join(dir, ".houston", "kamal"),
		report:   &reporter{ctx: ctx, mission: d.Mission, deploy: dep, out: o.Stdout, fenceAfter: fence, cancel: cancel, lastOK: time.Now()}}
	stopHeartbeat := r.report.heartbeat(every)
	defer stopHeartbeat()
	r.report.logf("Deploy #%d of %s: %s (%s)\n", dep.Number, p.Name, sha[:7], ref)
	if dep.TookOver > 0 {
		r.report.logf("Took over deploy #%d, which had gone silent.\n", dep.TookOver)
	}
	domains := make([]string, 0, len(synced.Domains))
	for d := range synced.Domains {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	for _, d := range domains {
		if st := synced.Domains[d]; st.State != "DNS OK" && st.State != "WILDCARD" {
			if st.Reason != "" {
				r.report.logf("%s: %s (%s)\n", d, st.State, st.Reason)
			} else {
				r.report.logf("%s: %s\n", d, st.State)
			}
		}
	}
	return r.deploy(dep.TookOver > 0)
}

// checkRef returns the commit and ref to deploy, or why they can't be: the
// worktree must be clean, the ref must be the checked-out commit, and the
// project's deploy rule must allow it.
func checkRef(g Git, dir, ref string, rule project.Deploy) (sha, fullRef string, err error) {
	status, err := g.Output(dir, "status", "--porcelain")
	if err != nil {
		return "", "", fmt.Errorf("can't read the checkout's git status: %v", err)
	}
	if strings.TrimSpace(status) != "" {
		return "", "", errors.New("the checkout has uncommitted changes; houston deploy builds exactly what's committed")
	}
	head, err := g.Output(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("can't read the checked-out commit: %v", err)
	}
	head = strings.TrimSpace(head)
	if ref == "" {
		branch, _ := g.Output(dir, "symbolic-ref", "-q", "HEAD")
		if ref = strings.TrimSpace(branch); ref == "" {
			return "", "", errors.New("HEAD is detached; say what's being deployed with --ref refs/heads/<branch> or refs/tags/<tag>")
		}
	}
	at, err := g.Output(dir, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil || strings.TrimSpace(at) != head {
		return "", "", fmt.Errorf("%s isn't the checked-out commit (%s)", ref, head)
	}

	on, branch, tags := rule.On, rule.Branch, rule.Tags
	if on == "" {
		on = "commit"
	}
	if branch == "" {
		branch = "main"
	}
	if tags == "" {
		tags = "v*"
	}
	switch on {
	case "tag":
		tag, isTag := strings.CutPrefix(ref, "refs/tags/")
		if ok, _ := path.Match(tags, tag); !isTag || !ok {
			return "", "", fmt.Errorf("x-houston.deploy deploys tags matching %s; %s isn't one", tags, ref)
		}
	default:
		if ref != "refs/heads/"+branch {
			return "", "", fmt.Errorf("x-houston.deploy deploys branch %s; %s isn't it", branch, ref)
		}
	}
	return head, ref, nil
}

// run is one started deploy: every failure from here on is reported.
type run struct {
	ctx         context.Context
	o           Options
	d           Deps
	p           *project.Project
	generation  int // the data generation sync reported: which volumes and accessories
	dir         string
	file        string // the compose file, absolute
	sha         string
	image       string
	kamalDir    string
	config      []byte
	secretsFile []byte
	timeout     time.Duration
	kamalEnv    []string // Environ plus HOUSTON_S_<NAME>=<carrier>
	kamalArgs   []string // -e HOUSTON_S_<NAME> for each
	appEnv      map[string]string
	report      *reporter
}

func (r *run) deploy(tookOver bool) int {
	if r.o.RunTests && r.p.Houston.Commands.Test != "" {
		r.report.step("Test")
		code, err := r.d.Exec.Stream(r.ctx, r.dir, nil, r.report, r.o.Houston, "-f", r.file, "test")
		if err != nil || code != 0 {
			return r.fail(fmt.Sprintf("tests failed (exit %d%s); the old version keeps serving", code, errText(err)))
		}
	}

	r.report.step("Secrets")
	if msg := r.secrets(); msg != "" {
		return r.fail(msg)
	}
	if err := r.writeKamalFiles(); err != nil {
		return r.fail(fmt.Sprintf("can't write .houston/kamal: %v", err))
	}
	if tookOver {
		r.report.logf("Releasing Kamal's deploy lock, left by the silent deploy.\n")
		if code, err := r.kamal("lock", "release", "--version", r.sha); err != nil || code != 0 {
			r.report.logf("kamal lock release: exit %d %v\n", code, errText(err))
		}
	}

	r.report.step("Build")
	build := r.p.Compose.Services[r.p.AppService].Build
	context, dockerfile := ".", "Dockerfile"
	if build != nil && build.Context != "" {
		context = build.Context
	}
	if build != nil && build.Dockerfile != "" {
		dockerfile = build.Dockerfile
	}
	if !filepath.IsAbs(context) {
		context = filepath.Join(r.dir, context)
	}
	if !filepath.IsAbs(dockerfile) {
		dockerfile = filepath.Join(context, dockerfile)
	}
	// Kamal only deploys images labelled service=<name> (its own builder
	// adds the label; Houston builds the image itself).
	if msg := r.docker("the image didn't build", "build", "--target", "production", "--label", "service="+r.p.Name, "-t", r.image, "-f", dockerfile, context); msg != "" {
		return r.fail(msg)
	}
	if msg := r.docker("couldn't push the image to Houston's registry", "push", r.image); msg != "" {
		return r.fail(msg)
	}

	// After the long build, before anything touches the data: the accessories
	// may reboot (a new Postgres image) and the release hook migrates.
	r.report.step("Snapshot")
	if code, stopped := r.snapshot(); stopped {
		return code
	}

	if len(r.p.Compose.Services) > 1 {
		r.report.step("Accessories")
		if code, err := r.kamal("accessory", "boot", "all", "--version", r.sha); err != nil || code != 0 {
			return r.fail(fmt.Sprintf("the accessories didn't boot (exit %d%s)", code, errText(err)))
		}
		if msg := r.rebootChangedAccessories(); msg != "" {
			return r.fail(msg)
		}
	}

	if hook := r.p.Houston.Hooks.Release; hook != "" {
		r.report.step("Release")
		if msg := r.release(hook); msg != "" {
			return r.fail(msg)
		}
	}

	r.report.step("Deploy")
	if code, err := r.kamal("deploy", "--skip-push", "--version", r.sha); err != nil || code != 0 {
		return r.fail(fmt.Sprintf("kamal deploy failed (exit %d%s); the old version keeps serving", code, errText(err)))
	}

	if hook := r.p.Houston.Hooks.PostDeploy; hook != "" {
		r.report.step("Post-deploy")
		code, err := r.d.Docker.Stream(r.ctx, r.dir, nil, r.report, "exec", r.p.Name+"-web-"+r.sha, "sh", "-c", hook)
		if err != nil || code != 0 {
			r.report.logf("post_deploy hook failed (exit %d%s); the deploy stands\n", code, errText(err))
		}
	}

	if r.ctx.Err() != nil {
		return r.stop()
	}
	r.report.logf("GO: %s is serving %s\n", r.p.Name, r.sha[:7])
	r.report.finish("go", "")
	return 0
}

// snapshot asks Mission Control for the pre-deploy snapshot of the version
// that's serving, and waits for it. stopped: the deploy ends here, with code.
func (r *run) snapshot() (code int, stopped bool) {
	s, err := r.d.Mission.Snapshot(r.ctx, r.report.deploy)
	for {
		switch {
		case r.ctx.Err() != nil && errors.Is(context.Cause(r.ctx), context.DeadlineExceeded):
			msg := "the pre-deploy snapshot didn't finish before the deploy's deadline; the old version keeps serving"
			r.report.logf("NO-GO: %s\n", msg)
			r.report.finish("no_go", msg)
			fmt.Fprintf(r.o.Stderr, "houston deploy: %s\n", msg)
			return exitFailure, true
		case r.ctx.Err() != nil:
			return r.stop(), true
		case err != nil && s.ID == 0:
			return r.fail(fmt.Sprintf("pre-deploy snapshot failed: %v; the old version keeps serving", err)), true
		case err != nil:
			r.report.logf("checking the snapshot: %v\n", err) // a blip: keep checking until the deadline
		case s.Status == "go":
			r.report.logf("ok  snapshot %s · kind:deploy sha:%s · %s\n", first(s.SnapshotID, 8), first(s.SHA, 7), humanize.Bytes(s.Bytes))
			return 0, false
		case s.Status == "skipped":
			r.report.logf("no snapshot: %s\n", s.Error)
			return 0, false
		case s.Status == "no_go":
			return r.fail(fmt.Sprintf("pre-deploy snapshot failed: %s; the old version keeps serving", s.Error)), true
		}
		select {
		case <-r.ctx.Done():
		case <-time.After(orDefault(r.o.SnapshotEvery, 2*time.Second)):
			id := s.ID
			s, err = r.d.Mission.SnapshotStatus(r.ctx, r.report.deploy)
			if s.ID == 0 {
				s.ID = id
			}
		}
	}
}

func first(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// secrets fetches every value, resolves the composites, and prepares what
// the Kamal container gets. Returns why it can't, without any value in it.
func (r *run) secrets() string {
	var fetchErr error
	lookup := func(name string) (string, bool) {
		v, ok, err := r.d.Mission.Secret(r.ctx, r.p.Name, name)
		if err != nil && fetchErr == nil {
			fetchErr = fmt.Errorf("can't read %s from Mission Control: %w", name, err)
		}
		return v, ok
	}
	values, err := kamal.ResolveSecrets(r.p, r.generation, lookup)
	if fetchErr != nil {
		return fetchErr.Error()
	}
	if err != nil {
		return fmt.Sprintf("a secret can't be resolved: %v", err)
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	r.kamalEnv = append([]string{}, r.o.Environ...)
	for _, name := range names {
		carrier, err := kamal.CarrierValue(values[name])
		if err != nil {
			return fmt.Sprintf("the value of %s can't be deployed: %v", name, err)
		}
		r.kamalEnv = append(r.kamalEnv, "HOUSTON_S_"+name+"="+carrier)
		r.kamalArgs = append(r.kamalArgs, "-e", "HOUSTON_S_"+name)
	}
	if r.appEnv, err = kamal.AppEnv(r.p, r.generation, values); err != nil {
		return err.Error()
	}
	return ""
}

func (r *run) writeKamalFiles() error {
	// .houston/ ignores itself (as houston dev leaves it), so the next deploy
	// doesn't find the checkout dirty.
	if err := os.MkdirAll(filepath.Dir(r.kamalDir), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.kamalDir), ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return err
	}
	for _, d := range []string{r.kamalDir, filepath.Join(r.kamalDir, "config"), filepath.Join(r.kamalDir, ".kamal")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	if err := writePrivate(filepath.Join(r.kamalDir, "config", "deploy.yml"), r.config); err != nil {
		return err
	}
	return writePrivate(filepath.Join(r.kamalDir, ".kamal", "secrets"), r.secretsFile)
}

func writePrivate(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// release runs the release hook in a one-off container of the new image,
// with the app's environment and volumes, before any traffic moves.
func (r *run) release(hook string) string {
	envFile := filepath.Join(r.kamalDir, "release.env")
	var b strings.Builder
	keys := make([]string, 0, len(r.appEnv))
	for k := range r.appEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + "=" + r.appEnv[k] + "\n")
	}
	if err := writePrivate(envFile, []byte(b.String())); err != nil {
		return fmt.Sprintf("can't write the release hook's environment: %v", err)
	}
	defer os.Remove(envFile)

	args := []string{"run", "--rm", "--name", r.p.Name + "-release-" + r.sha[:7], "--network", "kamal", "--env-file", envFile}
	for _, v := range kamal.AppVolumes(r.p, r.generation) {
		args = append(args, "-v", v)
	}
	args = append(args, r.image, "sh", "-c", hook)
	code, err := r.d.Docker.Stream(r.ctx, r.dir, nil, r.report, args...)
	if err != nil || code != 0 {
		return fmt.Sprintf("release hook failed (exit %d%s); the old version keeps serving", code, errText(err))
	}
	return ""
}

// kamal runs Kamal in its container on the host network, with the generated
// files, the Docker socket, the houston user's SSH key, and the secrets in
// its environment (never its arguments).
// rebootChangedAccessories reboots each accessory whose running container
// carries a different config label than deploy.yml's: Kamal's boot skips
// an accessory whose container exists (spike S10). Volumes are kept.
func (r *run) rebootChangedAccessories() string {
	labels := kamal.AccessoryLabels(r.p, r.generation)
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out, err := r.d.Docker.Output("inspect", "-f", `{{index .Config.Labels "`+kamal.ConfigLabel+`"}}`, r.p.Name+"-"+name)
		if err == nil && strings.TrimSpace(string(out)) == labels[name] {
			continue
		}
		r.report.logf("%s's config changed; rebooting it (its volumes are kept).\n", name)
		if code, err := r.kamal("accessory", "reboot", name, "--version", r.sha); err != nil || code != 0 {
			return fmt.Sprintf("the %s accessory didn't reboot (exit %d%s)", name, code, errText(err))
		}
	}
	return ""
}

func (r *run) kamal(args ...string) (int, error) {
	return r.kamalWith(r.ctx, args...)
}

func (r *run) kamalWith(ctx context.Context, args ...string) (int, error) {
	full := []string{"run", "--rm", "--name", "houston-kamal-" + r.p.Name, "--network", "host",
		"-v", r.kamalDir + ":/workdir", "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", r.o.SSHDir + ":/ssh:ro"}
	full = append(full, r.kamalArgs...)
	full = append(full, KamalImage)
	full = append(full, args...)
	return r.d.Docker.Stream(ctx, r.dir, r.kamalEnv, r.report, full...)
}

func (r *run) docker(failure string, args ...string) string {
	code, err := r.d.Docker.Stream(r.ctx, r.dir, nil, r.report, args...)
	if err != nil || code != 0 {
		return fmt.Sprintf("%s (exit %d%s)", failure, code, errText(err))
	}
	return ""
}

func (r *run) fail(msg string) int {
	if r.ctx.Err() != nil {
		return r.stop() // the failure is the run being stopped
	}
	r.report.logf("NO-GO: %s\n", msg)
	r.report.finish("no_go", msg)
	fmt.Fprintf(r.o.Stderr, "houston deploy: %s\n", msg)
	return exitFailure
}

var (
	errTakenOver = errors.New("taken over")
	errLostTouch = errors.New("lost touch with Mission Control")
)

// stop ends a run whose context ended. Cancelling kills the docker CLI, not
// its container, so houston's own containers go first. A taken-over deploy
// isn't ours to report on or unlock; otherwise we held Kamal's lock, so we
// release it and report NO-GO.
func (r *run) stop() int {
	cause := context.Cause(r.ctx)
	r.d.Docker.Output("rm", "-f", "houston-kamal-"+r.p.Name, r.p.Name+"-release-"+r.sha[:7])
	if errors.Is(cause, errTakenOver) {
		fmt.Fprintf(r.o.Stderr, "houston deploy: deploy #%d was taken over by a newer deploy; stopped without touching it further\n", r.report.deploy.Number)
		return exitFailure
	}
	var msg string
	switch {
	case errors.Is(cause, errLostTouch):
		msg = fmt.Sprintf("lost touch with Mission Control for over %s; stopped so another deploy can safely take over", r.report.fenceAfter)
	case errors.Is(cause, context.DeadlineExceeded):
		msg = fmt.Sprintf("timed out after %s; stopped (Kamal only switches traffic to a healthy new version)", r.timeout)
	default:
		msg = fmt.Sprintf("stopped: %v", cause)
	}
	r.report.logf("NO-GO: %s\nReleasing Kamal's deploy lock.\n", msg)
	if code, err := r.kamalWith(context.Background(), "lock", "release", "--version", r.sha); err != nil || code != 0 {
		r.report.logf("kamal lock release: exit %d%s\n", code, errText(err))
	}
	r.report.finish("no_go", msg)
	fmt.Fprintf(r.o.Stderr, "houston deploy: %s\n", msg)
	return exitFailure
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}

// reporter tees step output to the terminal and the deploy's log in Mission
// Control, sent with each step, every heartbeat, and at the end. A 409 means
// the deploy was taken over, and a long stretch of failed reports means
// Mission Control is gone: either cancels the run.
type reporter struct {
	ctx        context.Context
	mission    Mission
	deploy     mission.Deploy
	out        io.Writer
	fenceAfter time.Duration
	cancel     context.CancelCauseFunc

	mu     sync.Mutex // buf, lastOK, closed
	buf    strings.Builder
	lastOK time.Time
	closed bool
	sendMu sync.Mutex // one report at a time, in order
}

// A log chunk may be at most 256 KiB (Deploy::CHUNK_CAP); stay well under.
const chunkSize = 200 << 10

func (r *reporter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out.Write(p)
	r.buf.Write(p)
	return len(p), nil
}

func (r *reporter) logf(format string, args ...any) { fmt.Fprintf(r, format, args...) }

func (r *reporter) step(name string) { r.send(mission.Progress{Step: name}) }

func (r *reporter) finish(status, msg string) {
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	r.send(mission.Progress{Status: status, Error: msg})
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

// heartbeat sends progress every interval (the log so far, or nothing), so
// Mission Control knows this deploy is alive. It returns a stop function.
func (r *reporter) heartbeat(every time.Duration) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				r.send(mission.Progress{})
			}
		}
	}()
	return func() { close(done) }
}

// send attaches the log written since the last report, in chunks. A chunk
// that doesn't get through goes back in the buffer for the next report.
func (r *reporter) send(p mission.Progress) {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	log := r.buf.String()
	r.buf.Reset()
	r.mu.Unlock()

	for len(log) > chunkSize {
		if !r.post(mission.Progress{Log: log[:chunkSize]}) {
			r.requeue(log)
			return
		}
		log = log[chunkSize:]
	}
	p.Log = log
	if !r.post(p) {
		r.requeue(log)
	}
}

func (r *reporter) post(p mission.Progress) bool {
	err := r.mission.Report(r.ctx, r.deploy, p)
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case err == nil:
		r.lastOK = time.Now()
		return true
	case errors.Is(err, mission.ErrTakenOver):
		r.closed = true
		r.cancel(errTakenOver)
	case time.Since(r.lastOK) > r.fenceAfter:
		r.cancel(errLostTouch)
	}
	return false
}

func (r *reporter) requeue(log string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rest := r.buf.String()
	r.buf.Reset()
	r.buf.WriteString(log)
	r.buf.WriteString(rest)
}

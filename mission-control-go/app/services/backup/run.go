package backup

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
)

// Deadline is how long a run may take in all: a command past it stops the
// run.
const Deadline = 3 * time.Hour

// LogCap is how much of a run's log is kept.
const LogCap = 1 << 20

// ResticImage is the restic the backups run, pinned.
const ResticImage = "restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510"

// resticCache survives between runs (each a fresh container), so a run
// doesn't read the whole repository's index again.
const resticCache = "houston-restic-cache:/root/.cache/restic"

// Runner carries out backup runs (the Rails app's DataRun and Backup):
// docker commands under one deadline, a heartbeat while it works, cleanup
// that never fails the run, and a finish written only while the run is
// still this job's.
type Runner struct {
	DB     *db.DB
	Docker dockercmd.Runner
	// Tools is Mission Control's own image, whose binary copies SQLite
	// databases (its backup-sqlite command) and whose sh writes files.
	Tools, ToolsBin string
	Log             *slog.Logger
	// HeartbeatEvery is how often a run says it's alive: 15 s.
	HeartbeatEvery time.Duration
	// Deadline is Deadline, or a test's.
	Deadline time.Duration
	// Refresh is called when a run changes (the flight board's refresh).
	Refresh func()
}

// failed is a run that ends NO-GO, in words for the page.
type failed string

func (f failed) Error() string { return string(f) }

var errTimedOut = errors.New("timed out")

// run is one run in progress.
type run struct {
	Runner
	run      models.BackupRun
	token    string
	project  models.Project
	location models.StorageLocation
	serving  models.Generation
	started  time.Time
	warnings []string
	mu       sync.Mutex
	log      strings.Builder
}

// Do carries out run id: claims it (ErrBusy: the project must wait; a run
// no longer queued is done), then backs up or puts back a restore's data.
// A failure the run records is its NO-GO; the error is for a surprise.
func (r Runner) Do(ctx context.Context, id int64) error {
	q := models.New(r.DB.Read)
	current, err := q.BackupRunByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var token string
	err = r.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		token, err = models.ClaimRun(ctx, tx, current, time.Now())
		return err
	})
	if err != nil || token == "" {
		return err
	}
	r.refresh()
	project, err := q.ProjectByID(ctx, current.ProjectID)
	if err != nil {
		return err
	}
	location, err := q.StorageLocationByID(ctx, current.LocationID)
	if err != nil {
		return err
	}
	if r.HeartbeatEvery == 0 {
		r.HeartbeatEvery = 15 * time.Second
	}
	if r.Deadline == 0 {
		r.Deadline = Deadline
	}
	w := &run{Runner: r, run: current, token: token, project: project, location: location,
		serving: models.Generation{Project: project.Name, Number: project.DataGeneration}}
	if current.Operation == "restore" {
		return fmt.Errorf("putting back a restore's data comes with restores (batch 3b)")
	}
	return w.backUp(ctx)
}

func (r Runner) refresh() {
	if r.Refresh != nil {
		r.Refresh()
	}
}

// backUp dumps each Postgres database and copies each SQLite database into
// a staging volume, writes a manifest, then takes one restic snapshot of
// the app's volumes and the staging volume together.
func (w *run) backUp(ctx context.Context) error {
	if len(w.project.Volumes.V) == 0 && len(w.project.Databases.V) == 0 {
		return w.finish(ctx, "skipped", "nothing to back up (no named volumes, no Postgres)", nil)
	}
	sha := "unknown"
	if d, err := models.New(w.DB.Read).RunningDeploySummary(ctx, w.project.ID); err == nil {
		sha = d.Sha
	}
	if _, err := models.New(w.DB.Write).BeginRun(ctx, models.BeginRunParams{Sha: sha, Now: time.Now(), ID: w.run.ID, TokenDigest: models.Digest(w.token)}); err != nil {
		return err
	}
	return w.steps(ctx, "backup", func(ctx context.Context) (*result, error) {
		if err := w.prepare(ctx); err != nil {
			return nil, err
		}
		var postgres []dumped
		for _, d := range w.project.Databases.V {
			pg, err := w.dumpPostgres(ctx, d.Service, d.Image)
			if err != nil {
				return nil, err
			}
			postgres = append(postgres, pg)
		}
		sqlite := []Copied{}
		if len(w.project.Volumes.V) > 0 {
			var err error
			if sqlite, err = w.copySQLite(ctx); err != nil {
				return nil, err
			}
		}
		if err := w.writeManifest(ctx, sha, postgres, sqlite); err != nil {
			return nil, err
		}
		snapshot, bytes, err := w.resticBackup(ctx, sha)
		if err != nil {
			return nil, err
		}
		w.forget(ctx)
		found := map[string]any{"databases": []map[string]string{}, "sqlite": []map[string]string{}}
		for _, pg := range postgres {
			for _, d := range pg.Databases {
				found["databases"] = append(found["databases"].([]map[string]string), map[string]string{"service": pg.Service, "name": d.Name})
			}
		}
		for _, s := range sqlite {
			found["sqlite"] = append(found["sqlite"].([]map[string]string), map[string]string{"volume": s.Volume, "path": s.Path})
		}
		return &result{snapshotID: snapshot, bytes: bytes, found: found}, nil
	})
}

// result is what a run that went ends with.
type result struct {
	snapshotID string
	bytes      int64
	found      map[string]any
}

// steps runs the work under the deadline and the heartbeat: every path
// finishes the run and removes the staging volume.
func (w *run) steps(ctx context.Context, what string, work func(context.Context) (*result, error)) error {
	w.started = time.Now()
	stop := w.heartbeat()
	defer stop()
	defer w.cleanUp("volume", "rm", "-f", w.staging())
	res, err := work(ctx)
	var f failed
	switch {
	case err == nil:
		return w.finish(ctx, "go", strings.Join(w.warnings, "; "), res)
	case errors.As(err, &f):
		return w.finish(ctx, "no_go", string(f), nil)
	case errors.Is(err, errTimedOut):
		w.cleanUp(append([]string{"rm", "-f"}, w.containers()...)...)
		return w.finish(ctx, "no_go", fmt.Sprintf("took longer than %s; stopped", hours(w.Deadline)), nil)
	default:
		// A bug or a surprise: the run still finishes, and the job fails loudly.
		return errors.Join(w.finish(ctx, "no_go", fmt.Sprintf("Houston failed during the %s: %v", what, err), nil), err)
	}
}

// hours is a deadline in Rails' words: "3 hours".
func hours(d time.Duration) string {
	if d%time.Hour == 0 {
		n := int(d / time.Hour)
		if n == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", n)
	}
	return d.String()
}

// heartbeat beats every HeartbeatEvery until stopped.
func (w *run) heartbeat() (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		tick := time.NewTicker(w.HeartbeatEvery)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				models.New(w.DB.Write).BeatRun(context.Background(), models.BeatRunParams{Now: time.Now(), ID: w.run.ID, TokenDigest: models.Digest(w.token)})
			}
		}
	})
	return func() { close(done); wg.Wait() }
}

// finish records how the run ended, if it's still this job's.
func (w *run) finish(ctx context.Context, status, errText string, res *result) error {
	w.mu.Lock()
	log := w.log.String()
	w.mu.Unlock()
	if len(log) > LogCap {
		log = strings.ToValidUTF8(log[:LogCap], "") + "\n[log truncated at 1 MiB]\n"
	}
	if len(errText) > 4000 {
		errText = errText[:3997] + "..."
	}
	params := models.FinishRunParams{Status: status, Error: errText, Log: log, Now: sql.NullTime{Time: time.Now(), Valid: true},
		ID: w.run.ID, TokenDigest: models.Digest(w.token), Found: "{}"}
	if res != nil {
		found, _ := json.Marshal(res.found)
		params.SnapshotID, params.Bytes, params.Found = res.snapshotID, sql.NullInt64{Int64: res.bytes, Valid: true}, string(found)
	}
	n, err := models.New(w.DB.Write).FinishRun(context.Background(), params)
	if err != nil {
		return err
	}
	if n != 1 {
		w.Log.Warn("a backup run was taken over; not finishing it", "project", w.project.Name, "run", w.run.ID)
		return nil
	}
	w.refresh()
	return nil
}

// docker runs a command with what's left of the deadline; a timeout stops
// the whole run.
func (w *run) docker(ctx context.Context, args []string, env map[string]string, stdin []byte) (dockercmd.Result, error) {
	w.note(args, "")
	left := w.Deadline - time.Since(w.started)
	if left < time.Second {
		return dockercmd.Result{}, errTimedOut
	}
	r := w.Docker.Run(ctx, args, dockercmd.Opts{Env: env, Stdin: stdin, Timeout: left})
	if r.Code == dockercmd.TimedOut {
		return r, errTimedOut
	}
	return r, nil
}

func (w *run) pipe(ctx context.Context, from, to []string) (dockercmd.Result, error) {
	w.note(from, to[len(to)-1])
	left := w.Deadline - time.Since(w.started)
	if left < time.Second {
		return dockercmd.Result{}, errTimedOut
	}
	r := w.Docker.Pipe(ctx, from, to, dockercmd.Opts{Timeout: left})
	if r.Code == dockercmd.TimedOut {
		return r, errTimedOut
	}
	return r, nil
}

// note logs a command, briefly: its first words (never an argument that
// could be a secret: those go through the environment).
func (w *run) note(args []string, into string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	first := args[:min(4, len(args))]
	if into != "" {
		fmt.Fprintf(&w.log, "$ docker %s … | docker … %s\n", strings.Join(args[:min(3, len(args))], " "), into)
		return
	}
	fmt.Fprintf(&w.log, "$ docker %s …\n", strings.Join(first, " "))
}

// cleanUp runs even after the deadline, briefly, and never fails the run.
func (w *run) cleanUp(args ...string) {
	w.Docker.Run(context.Background(), args, dockercmd.Opts{Timeout: time.Minute})
}

func (w *run) prepare(ctx context.Context) error {
	w.docker(ctx, append([]string{"rm", "-f"}, w.containers()...), nil, nil)
	w.docker(ctx, []string{"volume", "rm", "-f", w.staging()}, nil, nil)
	created, err := w.docker(ctx, []string{"volume", "create", w.staging()}, nil, nil)
	if err != nil {
		return err
	}
	if !created.OK {
		return failed("couldn't create the staging volume: " + tail(created.Output))
	}
	return nil
}

// Project names have no dots, so no project's names match another's.
func (w *run) staging() string              { return "houston-backup." + w.project.Name }
func (w *run) container(role string) string { return "houston-backup." + w.project.Name + "." + role }
func (w *run) containers() []string {
	return []string{w.container("sqlite"), w.container("restic"), w.container("write")}
}

const (
	listDatabases = `psql -U "${POSTGRES_USER:-postgres}" -d postgres -Atc "select encode(convert_to(datname, 'UTF8'), 'hex') from pg_database where not datistemplate order by datname"`
	pgDump        = `pg_dump -U "${POSTGRES_USER:-postgres}" -Fc -d "$1"`
	pgGlobals     = `pg_dumpall -U "${POSTGRES_USER:-postgres}" --globals-only`
	writeFile     = `mkdir -p "$(dirname "$1")" && cat > "$1"`
)

// dumped is one Postgres accessory's dump in the staging volume.
type dumped struct {
	Service   string       `json:"service"`
	Image     string       `json:"image"`
	Globals   string       `json:"globals"`
	Databases []dumpedFile `json:"databases"`
}

type dumpedFile struct {
	Name string `json:"name"`
	File string `json:"file"`
}

func (w *run) writeTo(file string) []string {
	return []string{"run", "--rm", "-i", "--name", w.container("write"), "--user", "0", "-v", w.staging() + ":/out",
		"--entrypoint", "sh", w.Tools, "-c", writeFile, "sh", "/out/" + file}
}

func (w *run) dumpPostgres(ctx context.Context, service, image string) (dumped, error) {
	container := w.serving.Container(service)
	listed, err := w.docker(ctx, []string{"exec", container, "sh", "-c", listDatabases}, nil, nil)
	if err != nil {
		return dumped{}, err
	}
	var names [][]byte
	for _, line := range strings.Split(listed.Output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, decodeErr := hex.DecodeString(line)
		if decodeErr != nil {
			listed.OK = false
			break
		}
		names = append(names, name)
	}
	if !listed.OK {
		return dumped{}, failed(fmt.Sprintf("couldn't list %s's databases: %s", service, tail(listed.Output)))
	}
	out := dumped{Service: service, Image: image, Globals: "postgres/" + service + "/globals.sql", Databases: []dumpedFile{}}
	for i, raw := range names {
		name := strings.ToValidUTF8(string(raw), "�")
		file := fmt.Sprintf("postgres/%s/%d.dump", service, i+1)
		ran, err := w.pipe(ctx, []string{"exec", container, "sh", "-c", pgDump, "sh", conninfo(raw)}, w.writeTo(file))
		if err != nil {
			return out, err
		}
		if !ran.OK {
			return out, failed(fmt.Sprintf("pg_dump of %s's database %q failed: %s", service, name, tail(ran.Output)))
		}
		out.Databases = append(out.Databases, dumpedFile{Name: name, File: file})
	}
	ran, err := w.pipe(ctx, []string{"exec", container, "sh", "-c", pgGlobals}, w.writeTo(out.Globals))
	if err != nil {
		return out, err
	}
	if !ran.OK {
		return out, failed(fmt.Sprintf("pg_dumpall --globals-only of %s failed: %s", service, tail(ran.Output)))
	}
	return out, nil
}

// conninfo is a libpq connection string naming the database, so a name
// with "=" or a URI prefix can't be read as connection options.
func conninfo(name []byte) string {
	return "dbname='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(string(name)) + "'"
}

func (w *run) copySQLite(ctx context.Context) ([]Copied, error) {
	args := []string{"run", "--rm", "--name", w.container("sqlite"), "--user", "0"}
	for _, v := range w.project.Volumes.V {
		args = append(args, "-v", w.serving.Volume(v.Name)+":/data/"+v.Name)
	}
	args = append(args, "-v", w.staging()+":/out", "--entrypoint", w.ToolsBin, w.Tools, "backup-sqlite", "/data", "/out")
	ran, err := w.docker(ctx, args, nil, nil)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(ran.Output, "\n"), "\n")
	var report Report
	if json.Unmarshal([]byte(lines[len(lines)-1]), &report) != nil {
		return nil, failed("looking for SQLite databases failed: " + tail(ran.Output))
	}
	if len(report.Errors) > 0 {
		e := report.Errors[0]
		return nil, failed(fmt.Sprintf("couldn't copy the SQLite database %s/%s: %s", e.Volume, e.Path, e.Message))
	}
	if !ran.OK {
		return nil, failed("looking for SQLite databases failed: " + tail(ran.Output))
	}
	w.warnings = append(w.warnings, report.Warnings...)
	return report.SQLite, nil
}

func (w *run) writeManifest(ctx context.Context, sha string, postgres []dumped, sqlite []Copied) error {
	if postgres == nil {
		postgres = []dumped{}
	}
	manifest, _ := json.MarshalIndent(map[string]any{"version": 1, "project": w.project.Name, "sha": sha, "volumes": w.project.Volumes.V,
		"postgres": postgres, "sqlite": sqlite}, "", "  ")
	written, err := w.docker(ctx, w.writeTo("houston.json"), nil, manifest)
	if err != nil {
		return err
	}
	if !written.OK {
		return failed("couldn't write the manifest: " + tail(written.Output))
	}
	return nil
}

// unreadableShown is how many files restic couldn't read a run lists.
const unreadableShown = 20

func (w *run) resticBackup(ctx context.Context, sha string) (string, int64, error) {
	tags := []string{"project:" + w.project.Name, "sha:" + sha, "kind:" + w.run.Kind, "reason:" + w.run.Reason}
	if w.run.DeployNumber.Valid {
		tags = append(tags, "deploy:"+strconv.FormatInt(w.run.DeployNumber.Int64, 10))
	}
	var mounts []string
	for _, v := range w.project.Volumes.V {
		mounts = append(mounts, w.serving.Volume(v.Name)+":/data/"+v.Name+":ro")
	}
	// --retry-lock: a pre-deploy snapshot can meet the daily prune's lock.
	command := []string{"backup", "--retry-lock", "10m", "--host", "houston", "--json"}
	for _, t := range tags {
		command = append(command, "--tag", t)
	}
	if len(mounts) > 0 {
		command = append(command, "--exclude-file", "/out/.houston/exclude", "/data")
	}
	command = append(command, "/out")
	env := ResticEnv(w.location)
	ran, err := w.docker(ctx, ResticArgs(w.location, env, command, w.container("restic"), append(mounts, w.staging()+":/out:ro")), env, nil)
	if err != nil {
		return "", 0, err
	}
	var summary map[string]any
	var unreadable []string
	for _, line := range strings.Split(ran.Output, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		switch m["message_type"] {
		case "summary":
			summary = m
		case "error":
			item, _ := m["item"].(string)
			if item == "" {
				if e, ok := m["error"].(map[string]any); ok {
					item, _ = e["message"].(string)
				}
			}
			unreadable = append(unreadable, item)
		}
	}
	if !(ran.OK || ran.Code == 3) || summary == nil {
		return "", 0, failed(fmt.Sprintf("restic backup failed (exit %d): %s", ran.Code, tail(ran.Output)))
	}
	if ran.Code == 3 {
		noun := "files"
		if len(unreadable) == 1 {
			noun = "file"
		}
		listed := strings.Join(unreadable[:min(len(unreadable), unreadableShown)], ", ")
		if len(unreadable) > unreadableShown {
			listed += ", …"
		}
		w.warnings = append(w.warnings, fmt.Sprintf("%d %s couldn't be read: %s", len(unreadable), noun, listed))
	}
	id, _ := summary["snapshot_id"].(string)
	bytes, _ := summary["total_bytes_processed"].(float64)
	return id, int64(bytes), nil
}

// forget applies this run's kind's retention: the newest snapshot a day
// for auto, the last N for deploy. Grouping by nothing matters: restic's
// default (host and paths) would keep every snapshot whose paths differ. A
// failure is a warning; the new snapshot is safe, and the next run tries
// again.
func (w *run) forget(ctx context.Context) {
	if w.run.Kind == "final" {
		return
	}
	keep := []string{"--keep-daily", strconv.FormatInt(w.project.KeepAuto, 10)}
	if w.run.Kind == "deploy" {
		keep = []string{"--keep-last", strconv.FormatInt(w.project.KeepDeploy, 10)}
	}
	command := append([]string{"forget", "--retry-lock", "10m", "--host", "houston", "--tag", "project:" + w.project.Name + ",kind:" + w.run.Kind,
		"--group-by", "", "--json"}, keep...)
	env := ResticEnv(w.location)
	ran, err := w.docker(ctx, ResticArgs(w.location, env, command, "", nil), env, nil)
	if err == nil && !ran.OK {
		w.warnings = append(w.warnings, "old snapshots weren't forgotten: "+tail(ran.Output))
	}
}

// ResticEnv is a location's secrets for restic, for the docker process's
// environment (never argv).
func ResticEnv(l models.StorageLocation) map[string]string {
	env := map[string]string{"RESTIC_PASSWORD": l.ResticPassword.Reveal(), "RESTIC_REPOSITORY": Repository(l)}
	var creds map[string]string
	json.Unmarshal([]byte(l.Credentials.Reveal()), &creds)
	switch l.Kind {
	case "s3":
		env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"] = creds["access_key_id"], creds["secret_access_key"]
	case "b2":
		env["B2_ACCOUNT_ID"], env["B2_ACCOUNT_KEY"] = creds["key_id"], creds["application_key"]
	}
	return env
}

// envOrder is the order ResticArgs names a location's secrets in.
var envOrder = []string{"RESTIC_PASSWORD", "RESTIC_REPOSITORY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "B2_ACCOUNT_ID", "B2_ACCOUNT_KEY"}

// Repository is a location's restic repository.
func Repository(l models.StorageLocation) string {
	switch l.Kind {
	case "nfs", "local":
		return "/repo"
	}
	return models.WhereItIs(l) // s3's and b2's are their repositories
}

// ResticArgs are docker run's arguments for a restic command against a
// location: its secrets named (-e NAME, their values in env), restic's
// cache, the repository mounted for nfs and local, and mounts (what to
// back up).
func ResticArgs(l models.StorageLocation, env map[string]string, command []string, name string, mounts []string) []string {
	args := []string{"run", "--rm"}
	if name != "" {
		args = append(args, "--name", name)
	}
	for _, k := range envOrder {
		if _, ok := env[k]; ok {
			args = append(args, "-e", k)
		}
	}
	volumes := []string{resticCache}
	switch l.Kind {
	case "nfs":
		volumes = append(volumes, "houston-storage-"+l.Name+":/repo")
	case "local":
		volumes = append(volumes, l.Settings.V["path"]+":/repo")
	}
	for _, m := range append(volumes, mounts...) {
		args = append(args, "-v", m)
	}
	return append(append(args, ResticImage), command...)
}

// tail is the end of a command's output, without restic's progress lines.
func tail(output string) string {
	var kept []string
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, `{"message_type":"status"`) {
			kept = append(kept, line)
		}
	}
	text := strings.TrimSpace(strings.Join(kept[max(0, len(kept)-20):], "\n"))
	if len(text) > 2000 {
		text = text[:1997] + "..."
	}
	return text
}

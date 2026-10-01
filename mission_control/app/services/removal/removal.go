// Package removal carries out a project's deletion, step by step:
//
//	check, snapshot    nothing removed yet: a failure cancels the deletion
//	                   and the project serves as before
//	routes … rows      removal: a failure stops the deletion there, the
//	                   project stays deleting, and asking again resumes
//
// Every removal step is idempotent (what's already gone is done), and names
// only what's the project's own: exact names, the exact service label, the
// exact DNS comment. Project names are DNS labels (no dots, no
// underscores), so <name>_… and <name>.g<n>_… volumes can only be its own.
package removal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/backup"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare"
	"github.com/scttymn/houston/mission_control/app/services/dns"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
	"github.com/scttymn/houston/mission_control/app/services/registry"
)

// Steps are what a deletion does, in order.
var Steps = []string{"check", "snapshot", "routes", "dns", "containers", "volumes", "folders", "images", "registry", "files", "backups", "rows"}

// StepWords are each step in words, for the page.
var StepWords = map[string]string{
	"check": "Check Docker, Cloudflare and storage", "snapshot": "Final snapshot", "routes": "Maintenance routes", "dns": "DNS records",
	"containers": "Containers and kamal-proxy's route", "volumes": "Volumes", "folders": "Volume folders on storage", "images": "Images",
	"registry": "Images in the registry", "files": "Kamal's files and the runners' checkouts", "backups": "Backups", "rows": "Mission Control's records",
}

const (
	// removeFolders removes a project's volume folders on a storage
	// location: volumes/<name> and volumes/<name>.g<n>. The name comes in
	// as $1, never in the script.
	removeFolders = `cd /location/volumes 2>/dev/null || exit 0; for d in "$1" "$1".g[0-9]*; do [ ! -e "$d" ] || rm -rf -- "$d" || exit 1; done`
	// removeFiles removes Kamal's state for the project (its env files
	// hold secret values) and each runner's checkout of it.
	removeFiles = `for p in /kamal/apps/"$1" /kamal/"$1"-audit.log /kamal/lock-"$1" /runners/*/"$1"; do [ ! -e "$p" ] || rm -rf -- "$p" || exit 1; done`
	// RunnersDir is where the runners keep their checkouts, on the host.
	RunnersDir    = "/var/lib/houston/runners"
	dockerTimeout = 5 * time.Minute
	resticTimeout = 3 * time.Hour
	logCap        = 1 << 20
)

// Removal is what a deletion works with.
type Removal struct {
	DB       *db.DB
	Docker   dockercmd.Runner
	Registry registry.Registry
	// Cloudflare is its API's address ("": Cloudflare's own).
	Cloudflare string
	Services   dns.Services
	// Backups takes the final snapshot; Snapshots is the lists' cache.
	Backups   backup.Runner
	Snapshots *backup.Snapshots
	Tools     string
	// KamalHome is where Kamal keeps its files on the host
	// (HOUSTON_KAMAL_HOME); without it nothing is removed.
	KamalHome, RunnersDir string
	Log                   *slog.Logger
	Refresh               func()
	// HeartbeatEvery is how often it says it's alive: 15 s.
	HeartbeatEvery time.Duration
}

// cancel ends a deletion before anything is removed; stop, during removal.
type (
	cancel string
	stop   string
)

func (c cancel) Error() string { return string(c) }
func (s stop) Error() string   { return string(s) }

type deletion struct {
	Removal
	d       models.ProjectDeletion
	project models.Project
	inst    models.Installation
	mu      sync.Mutex
	log     strings.Builder
}

// Do carries out deletion id, if it's still queued: it's claimed first.
// Done, it queues the registry's clean-up (cleanUp).
func (r Removal) Do(ctx context.Context, id int64, cleanUp func(ctx context.Context, deletion int64) error) error {
	now := time.Now()
	n, err := models.New(r.DB.Write).ClaimDeletion(ctx, models.ClaimDeletionParams{Now: sql.NullTime{Time: now, Valid: true}, ID: id})
	if err != nil || n != 1 {
		return err
	}
	q := models.New(r.DB.Read)
	d, err := q.DeletionByID(ctx, id)
	if err != nil {
		return err
	}
	r.refresh()
	if r.HeartbeatEvery == 0 {
		r.HeartbeatEvery = 15 * time.Second
	}
	if r.RunnersDir == "" {
		r.RunnersDir = RunnersDir
	}
	w := &deletion{Removal: r, d: d}
	w.log.WriteString(d.Log)
	w.inst, _ = q.CurrentInstallation(ctx)
	stopBeat := w.heartbeat()
	err = w.run(ctx)
	stopBeat()
	var c cancel
	var s stop
	switch {
	case err == nil:
		w.finish("go", "")
		if cleanUp != nil {
			return cleanUp(ctx, id)
		}
		return nil
	case errors.As(err, &c):
		w.finish("no_go", "cancelled: "+string(c))
		return nil
	case errors.As(err, &s):
		w.finish("no_go", "stopped at "+w.d.Step+": "+string(s))
		return nil
	default:
		w.finish("no_go", fmt.Sprintf("Houston failed while deleting at %s: %v", w.d.Step, err))
		return err
	}
}

func (r Removal) refresh() {
	if r.Refresh != nil {
		r.Refresh()
	}
}

func (w *deletion) run(ctx context.Context) error {
	if !w.d.ProjectID.Valid {
		return nil // its project is gone already: only the finish is left
	}
	p, err := models.New(w.DB.Read).ProjectByID(ctx, w.d.ProjectID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	w.project = p
	if !w.d.RemovingAt.Valid {
		if err := w.prepare(ctx); err != nil {
			return err
		}
	}
	for _, step := range []struct {
		name string
		do   func(context.Context) error
	}{{"routes", w.removeRoutes}, {"dns", w.removeDNS}, {"containers", w.removeContainers}, {"volumes", w.removeVolumes}, {"folders", w.removeFolders},
		{"images", w.removeImages}, {"registry", w.removeRegistry}, {"files", w.removeFiles}, {"backups", w.removeBackups}, {"rows", w.removeRows}} {
		w.step(step.name)
		if err := step.do(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (w *deletion) say(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintf(&w.log, format+"\n", args...)
}

func (w *deletion) logText() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.log.String()
	if len(s) > logCap {
		s = strings.ToValidUTF8(s[len(s)-logCap:], "")
	}
	return s
}

// step starts one, on the log and the page.
func (w *deletion) step(name string) {
	w.say("== %s", name)
	w.d.Step = name
	models.New(w.DB.Write).WriteDeletion(context.Background(), models.WriteDeletionParams{Log: w.logText(), Step: name, Now: time.Now(),
		ID: w.d.ID, StartedAt: w.d.StartedAt})
	w.refresh()
}

func (w *deletion) finish(status, errText string) {
	if errText != "" {
		w.say("%s", errText)
	} else {
		w.say("GO: %s is deleted", w.d.Name)
	}
	if len(errText) > 4000 {
		errText = errText[:3997] + "..."
	}
	models.New(w.DB.Write).FinishDeletion(context.Background(), models.FinishDeletionParams{Status: status, Error: errText, Log: w.logText(),
		Now: sql.NullTime{Time: time.Now(), Valid: true}, ID: w.d.ID, StartedAt: w.d.StartedAt})
	w.refresh()
}

func (w *deletion) heartbeat() (stop func()) {
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
				models.New(w.DB.Write).BeatDeletion(context.Background(), models.BeatDeletionParams{Now: time.Now(), ID: w.d.ID, StartedAt: w.d.StartedAt})
			}
		}
	})
	return func() { close(done); wg.Wait() }
}

func (w *deletion) docker(ctx context.Context, args ...string) dockercmd.Result {
	w.say("$ docker %s", strings.Join(args[:min(5, len(args))], " "))
	return w.Docker.Run(ctx, args, dockercmd.Opts{Timeout: dockerTimeout})
}

// ids is a docker listing's words; a failure stops the deletion.
func (w *deletion) ids(ctx context.Context, args ...string) ([]string, error) {
	r := w.docker(ctx, args...)
	if !r.OK {
		return nil, stop(fmt.Sprintf("docker %s failed: %s", strings.Join(args[:min(2, len(args))], " "), tail(r.Output)))
	}
	return strings.Fields(r.Output), nil
}

func (w *deletion) removeIDs(ctx context.Context, list []string) error {
	if len(list) == 0 {
		return nil
	}
	if r := w.docker(ctx, append([]string{"rm", "-f"}, list...)...); !r.OK {
		return stop(fmt.Sprintf("couldn't remove containers %s: %s", strings.Join(list, ", "), tail(r.Output)))
	}
	return nil
}

func tail(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	s := strings.TrimSpace(strings.Join(lines[max(0, len(lines)-5):], "\n"))
	if len(s) > 1000 {
		s = s[:997] + "..."
	}
	return s
}

func (w *deletion) cloudflare() bool {
	return w.inst.CloudflareConnectedAt.Valid && w.inst.CloudflareApiToken.Reveal() != ""
}

func (w *deletion) client() cloudflare.Client {
	return cloudflare.Client{Token: w.inst.CloudflareApiToken.Reveal(), Base: w.Cloudflare}
}

// ── Before anything is removed ────────────────────────────────────────

func (w *deletion) prepare(ctx context.Context) error {
	w.step("check")
	r := w.docker(ctx, "version", "--format", "{{.Server.Version}}")
	if !r.OK {
		return cancel("Docker didn't answer: " + tail(r.Output))
	}
	w.say("ok  Docker %s", strings.TrimSpace(r.Output))
	if w.KamalHome == "" {
		return cancel("Mission Control doesn't know where Kamal keeps its files: run the installer once more")
	}
	if !w.Registry.DeletesEnabled(ctx) {
		return cancel("the registry doesn't allow deletes yet: run the installer once more")
	}
	if w.cloudflare() {
		var records []cloudflare.Record
		if err := w.client().Get(ctx, "/zones/"+w.inst.CloudflareZoneID+"/dns_records", url.Values{"per_page": {"1"}}, &records); err != nil {
			var cf *cloudflare.Error
			if errors.As(err, &cf) {
				return cancel("Cloudflare said no: " + cf.Msg)
			}
			return err
		}
		w.say("ok  Cloudflare answers")
	}
	q := models.New(w.DB.Read)
	served, err := q.ProjectHasServed(ctx, w.project.ID)
	if err != nil {
		return err
	}
	needsSnapshot := !w.d.DeleteBackups && served && (len(w.project.Volumes.V) > 0 || len(w.project.Databases.V) > 0)
	location, locErr := q.BackupLocationFor(ctx, w.project.BackupLocationID.Int64)
	if needsSnapshot && locErr != nil {
		return cancel("no backup storage to keep a final snapshot in (finish setup's storage step, or delete the backups too)")
	}
	if needsSnapshot {
		w.step("snapshot")
		if err := w.finalSnapshot(ctx, location); err != nil {
			return err
		}
	}
	return models.New(w.DB.Write).MarkRemoving(ctx, models.MarkRemovingParams{Now: sql.NullTime{Time: time.Now(), Valid: true}, ID: w.d.ID, StartedAt: w.d.StartedAt})
}

// finalSnapshot is a backup of kind final: retention forgets by kind, so
// never this one.
func (w *deletion) finalSnapshot(ctx context.Context, location models.StorageLocation) error {
	run, err := models.New(w.DB.Write).CreateBackupRun(ctx, models.CreateBackupRunParams{ProjectID: w.project.ID, LocationID: location.ID,
		Operation: "backup", Kind: "final", Reason: "delete", HeartbeatAt: time.Now()})
	if err != nil {
		return err
	}
	if err := w.Backups.Do(ctx, run.ID); errors.Is(err, models.ErrBusy) {
		return cancel(fmt.Sprintf("a backup of %s is running; delete once it's done", w.project.Name))
	} else if err != nil {
		return err
	}
	run, err = models.New(w.DB.Read).BackupRunByID(ctx, run.ID)
	if err != nil {
		return err
	}
	switch run.Status {
	case "go":
		if err := models.New(w.DB.Write).KeepFinalSnapshot(ctx, models.KeepFinalSnapshotParams{SnapshotID: run.SnapshotID,
			LocationID: sql.NullInt64{Int64: run.LocationID, Valid: true}, Now: time.Now(), ID: w.d.ID, StartedAt: w.d.StartedAt}); err != nil {
			return err
		}
		w.say("ok  final snapshot %s in %s", run.SnapshotID[:min(8, len(run.SnapshotID))], location.Name)
	case "skipped":
	case "queued":
		return cancel("the final snapshot couldn't start")
	default:
		return cancel("the final snapshot failed: " + run.Error)
	}
	return nil
}

// ── Removal ───────────────────────────────────────────────────────────

// removeRoutes: the tunnel's rules leave out projects being removed, so
// their hosts stop reaching Mission Control.
func (w *deletion) removeRoutes(ctx context.Context) error {
	if !w.project.MaintenanceSince.Valid || !w.cloudflare() || w.inst.TunnelID == "" {
		w.say("ok  not in maintenance")
		return nil
	}
	if err := (dns.DNS{Installation: w.inst, API: w.Cloudflare}).PushRoutes(ctx, models.New(w.DB.Read), w.Services); err != nil {
		var cf *cloudflare.Error
		if errors.As(err, &cf) {
			return stop("Cloudflare said no: " + cf.Msg)
		}
		return err
	}
	w.say("ok  %s's hosts no longer reach Mission Control", w.project.Name)
	return nil
}

// removeDNS removes the records commented exactly managed-by:houston
// project:<name>, in the zones the project's hostnames live in, that point
// at this server's tunnel: another server's Houston marks its own records
// the same way.
func (w *deletion) removeDNS(ctx context.Context) error {
	if !w.cloudflare() {
		w.say("ok  Cloudflare isn't connected")
		return nil
	}
	comment := cloudflare.Managed + " project:" + w.project.Name
	zones, err := w.zones(ctx)
	if err == nil {
		for _, zone := range zones {
			var records []cloudflare.Record
			if err = w.client().Get(ctx, "/zones/"+zone+"/dns_records", url.Values{"comment.exact": {comment}, "per_page": {"5000"}}, &records); err != nil {
				break
			}
			for _, rec := range records {
				if rec.Comment != comment {
					continue
				}
				if rec.Content != cloudflare.Target(w.inst.TunnelID) {
					w.say("ok  left %s: it points at another server's tunnel", rec.Name)
					continue
				}
				if err = w.client().Delete(ctx, "/zones/"+zone+"/dns_records/"+rec.ID); err != nil {
					break
				}
				w.say("ok  removed %s", rec.Name)
			}
			if err != nil {
				break
			}
		}
	}
	var cf *cloudflare.Error
	if errors.As(err, &cf) {
		return stop("Cloudflare said no: " + cf.Msg)
	}
	return err
}

func (w *deletion) zones(ctx context.Context) ([]string, error) {
	zones := []string{}
	if w.inst.CloudflareZoneID != "" {
		zones = append(zones, w.inst.CloudflareZoneID)
	}
	domains := slices.Clone(w.project.Domains.V)
	for d := range w.project.DomainStates.V {
		domains = append(domains, d)
	}
	slices.Sort(domains)
	for _, d := range slices.Compact(domains) {
		if strings.HasSuffix(d, "."+w.inst.BaseDomain) {
			continue
		}
		labels := strings.Split(d, ".")
		for i := 0; i < len(labels)-1; i++ {
			zone, err := w.client().FindZone(ctx, strings.Join(labels[i:], "."))
			if err != nil {
				return nil, err
			}
			if zone != nil {
				if !slices.Contains(zones, zone.ID) {
					zones = append(zones, zone.ID)
				}
				break
			}
		}
	}
	return zones, nil
}

func (w *deletion) removeContainers(ctx context.Context) error {
	name := w.project.Name
	for _, filter := range []string{"label=service=" + name, "name=^/" + name + "-release-[0-9a-f]+$"} {
		list, err := w.ids(ctx, "ps", "-aq", "--filter", filter)
		if err != nil {
			return err
		}
		if err := w.removeIDs(ctx, list); err != nil {
			return err
		}
	}
	q := models.New(w.DB.Read)
	own, err := q.ProjectHostNames(ctx, w.project.ID)
	if err != nil {
		return err
	}
	theirs, err := q.OthersHostNames(ctx, w.project.ID)
	if err != nil {
		return err
	}
	var containers []string
	for _, h := range own {
		if h != name {
			containers = append(containers, h)
		}
	}
	slices.Sort(containers)
	containers = append(containers, "houston-kamal-"+name)
	for _, c := range containers {
		if slices.Contains(theirs, c) {
			continue
		}
		if r := w.docker(ctx, "rm", "-f", c); !r.OK && !strings.Contains(r.Output, "No such container") {
			return stop(fmt.Sprintf("couldn't remove %s: %s", c, tail(r.Output)))
		}
	}
	r := w.docker(ctx, "exec", "kamal-proxy", "kamal-proxy", "remove", name+"-web")
	if !r.OK && !strings.Contains(r.Output, "service not found") && !strings.Contains(r.Output, "No such container: kamal-proxy") {
		return stop(fmt.Sprintf("kamal-proxy kept %s-web: %s", name, tail(r.Output)))
	}
	w.say("ok  removed %s's containers and its route in kamal-proxy", name)
	return nil
}

func (w *deletion) removeVolumes(ctx context.Context) error {
	name := regexp.QuoteMeta(w.project.Name)
	ours := regexp.MustCompile(`^(` + name + `(\.g\d+)?_[^\s/]+|houston-(backup|restore)\.` + name + `)$`)
	all, err := w.ids(ctx, "volume", "ls", "-q")
	if err != nil {
		return err
	}
	var volumes []string
	for _, v := range all {
		if ours.MatchString(v) {
			volumes = append(volumes, v)
		}
	}
	for _, v := range volumes {
		using, err := w.ids(ctx, "ps", "-aq", "--filter", "volume="+v)
		if err != nil {
			return err
		}
		if err := w.removeIDs(ctx, using); err != nil {
			return err
		}
	}
	for _, v := range volumes {
		if r := w.docker(ctx, "volume", "rm", v); !r.OK && !strings.Contains(r.Output, "no such volume") {
			return stop(fmt.Sprintf("couldn't remove %s: %s", v, tail(r.Output)))
		}
	}
	w.say("ok  removed %d %s", len(volumes), plural(len(volumes), "volume"))
	return nil
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// removeFolders removes its folders on each storage location that held
// one of its volumes.
func (w *deletion) removeFolders(ctx context.Context) error {
	locations, err := models.New(w.DB.Read).VolumeLocationsOf(ctx, w.project.ID)
	if err != nil {
		return err
	}
	for _, l := range locations {
		root := l.Settings.V["path"]
		if l.Kind == "nfs" {
			root = "houston-storage-" + l.Name
			if r := w.docker(ctx, "volume", "inspect", root); !r.OK {
				s := l.Settings.V
				if r := w.docker(ctx, "volume", "create", "--driver", "local", "--opt", "type=nfs", "--opt", "o=addr="+s["server"]+",rw,nfsvers=4",
					"--opt", "device=:"+s["export"], root); !r.OK {
					return stop(fmt.Sprintf("couldn't reach %s: %s", l.Name, tail(r.Output)))
				}
			}
		}
		if r := w.docker(ctx, "run", "--rm", "--user", "0", "-v", root+":/location", "--entrypoint", "sh", w.Tools, "-c", removeFolders, "sh", w.project.Name); !r.OK {
			return stop(fmt.Sprintf("couldn't remove %s's folders on %s: %s", w.project.Name, l.Name, tail(r.Output)))
		}
		w.say("ok  removed volumes/%s on %s", w.project.Name, l.Name)
	}
	return nil
}

func (w *deletion) removeImages(ctx context.Context) error {
	images, err := w.ids(ctx, "images", "-q", "--filter", "reference=127.0.0.1:5000/"+w.project.Name+":*")
	if err != nil {
		return err
	}
	slices.Sort(images)
	images = slices.Compact(images)
	if len(images) > 0 {
		if r := w.docker(ctx, append([]string{"rmi", "-f"}, images...)...); !r.OK {
			return stop(fmt.Sprintf("couldn't remove %s's images: %s", w.project.Name, tail(r.Output)))
		}
	}
	w.say("ok  removed %d %s from this server", len(images), plural(len(images), "image"))
	return nil
}

// removeRegistry deletes its manifests in Houston's registry; the space is
// freed afterwards, once nothing is being pushed.
func (w *deletion) removeRegistry(ctx context.Context) error {
	n, err := w.Registry.DeleteRepository(ctx, w.project.Name)
	var e registry.Error
	if errors.As(err, &e) {
		return stop(string(e))
	}
	if err != nil {
		return err
	}
	w.say("ok  deleted %d %s from the registry", n, plural(n, "image"))
	return nil
}

func (w *deletion) removeFiles(ctx context.Context) error {
	if r := w.docker(ctx, "run", "--rm", "--user", "0", "-v", w.KamalHome+":/kamal", "-v", w.RunnersDir+":/runners",
		"--entrypoint", "sh", w.Tools, "-c", removeFiles, "sh", w.project.Name); !r.OK {
		return stop("couldn't remove Kamal's files and the runners' checkouts: " + tail(r.Output))
	}
	w.say("ok  removed Kamal's files (its env files held secrets) and the runners' checkouts")
	return nil
}

var snapshotID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (w *deletion) removeBackups(ctx context.Context) error {
	if !w.d.DeleteBackups {
		d, _ := models.New(w.DB.Read).DeletionByID(ctx, w.d.ID)
		if d.SnapshotID != "" {
			w.say("ok  backups kept (final snapshot %s)", d.SnapshotID[:min(8, len(d.SnapshotID))])
		} else {
			w.say("ok  backups kept")
		}
		return nil
	}
	locations, err := backup.LocationsFor(ctx, models.New(w.DB.Read), w.project)
	if err != nil {
		return err
	}
	for _, l := range locations {
		env := backup.ResticEnv(l)
		listed := w.Docker.Run(ctx, backup.ResticArgs(l, env, []string{"snapshots", "--json", "--host", "houston", "--tag", "project:" + w.project.Name}, "", nil),
			dockercmd.Opts{Env: env, Timeout: resticTimeout})
		if !listed.OK {
			return stop(fmt.Sprintf("couldn't list %s's snapshots: %s", l.Name, tail(listed.Output)))
		}
		var entries []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(listed.Output), &entries) != nil {
			return stop("restic's list wasn't JSON")
		}
		var ids []string
		for _, e := range entries {
			if snapshotID.MatchString(e.ID) {
				ids = append(ids, e.ID)
			}
		}
		if len(ids) > 0 {
			forgot := w.Docker.Run(ctx, backup.ResticArgs(l, env, append([]string{"forget", "--retry-lock", "30m", "--prune"}, ids...), "", nil),
				dockercmd.Opts{Env: env, Timeout: resticTimeout})
			if !forgot.OK {
				return stop(fmt.Sprintf("couldn't delete %s's snapshots: %s", l.Name, tail(forgot.Output)))
			}
		}
		w.Snapshots.Forget(w.project.Name, l.ID)
		w.say("ok  deleted %d %s in %s", len(ids), plural(len(ids), "snapshot"), l.Name)
	}
	return nil
}

func (w *deletion) removeRows(ctx context.Context) error {
	err := w.DB.Tx(ctx, func(tx *db.Tx) error {
		q := models.New(tx)
		if err := q.DeleteProjectRows(ctx, w.project.ID); err != nil {
			return err
		}
		if err := q.DeleteProjectVolumes(ctx, w.project.ID); err != nil {
			return err
		}
		return q.DeleteProject(ctx, w.project.ID)
	})
	if err != nil {
		return err
	}
	w.say("ok  removed %s from Mission Control", w.project.Name)
	return nil
}

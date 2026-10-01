package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/jobs"

	"github.com/scttymn/houston/mission_control/app/api"
	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/backup"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
	"github.com/scttymn/houston/mission_control/app/services/gitremote"
	"github.com/scttymn/houston/mission_control/app/services/removal"
)

// DefineJobs defines the app's background jobs on a.Jobs (gantry's jobs
// package): a name, an arguments struct and a function,
// kept on App for the controllers to enqueue. Add one here:
//
//	a.Welcome = jobs.Define(a.Jobs, "welcome", a.welcome, jobs.Opts[WelcomeArgs]{
//		Retry: jobs.Retry{Attempts: 5, Wait: 3 * time.Second}})
//
// with Welcome *jobs.Job[WelcomeArgs] in App, and a controller's
// a.Welcome.Enqueue(ctx, WelcomeArgs{UserID: u.ID}). One that recurs:
// a.Sweep.Every(time.Hour, SweepArgs{}), or a.Report.Cron("0 3 * * *",
// time.UTC, ReportArgs{}). In tests, a.Jobs.Drain(ctx) runs what's due.
func (a *App) DefineJobs() error {
	// A run waits while its project is busy, for as long as one may run.
	busy := jobs.Retry{Attempts: int((backup.Deadline+models.BackupStaleAfter)/backupWait) + 1, Wait: backupWait}
	gaveUp := func(ctx context.Context, args models.BackupArgs, err error) {
		models.New(a.DB.Write).GiveUpRun(ctx, models.GiveUpRunParams{Error: "waited 3 hours and 2 minutes for the project's running backup",
			Now: sql.NullTime{Time: time.Now(), Valid: true}, ID: args.RunID})
	}
	a.Backup = jobs.Define(a.Jobs, "backup", a.backup, jobs.Opts[models.BackupArgs]{Queue: "backups", Retry: busy, OnExhausted: gaveUp})
	// A deploy or restore waits for these (its snapshot, its data): they
	// never queue behind another project's long backup.
	a.Snapshot = jobs.Define(a.Jobs, "snapshot", a.backup, jobs.Opts[models.BackupArgs]{Queue: "snapshots", Retry: busy, OnExhausted: gaveUp})
	// One check at a time a project: a webhook's and the poll's can't
	// interleave (the one holding older refs would queue an older commit).
	a.Check = jobs.Define(a.Jobs, "check_for_changes", a.checkForChanges, jobs.Opts[models.CheckArgs]{
		Limit: &jobs.Limit[models.CheckArgs]{To: 1, Key: func(c models.CheckArgs) string { return strconv.FormatInt(c.ProjectID, 10) }, Duration: 5 * time.Minute}})
	// The webhook is a doorbell; this is the fallback when a ring is missed
	// or refused: every ten minutes, the projects that deploy on push.
	a.Poll = jobs.Define(a.Jobs, "poll_for_changes", a.pollForChanges, jobs.Opts[struct{}]{})
	a.Poll.Every(10*time.Minute, struct{}{})
	// Each project's daily backup, when it's due: looked at every minute.
	a.Schedule = jobs.Define(a.Jobs, "schedule_backups", a.scheduleBackups, jobs.Opts[struct{}]{})
	a.Schedule.Every(time.Minute, struct{}{})
	// restic's prune of every location in use, daily, when little else runs.
	a.Prune = jobs.Define(a.Jobs, "prune_backups", a.prune, jobs.Opts[struct{}]{Queue: "backups"})
	if err := a.Prune.Cron("30 4 * * *", time.UTC, struct{}{}); err != nil {
		return err
	}
	// A project's deletion, then the registry's clean-up once nothing is
	// being pushed (waiting up to 6 hours).
	a.Delete = jobs.Define(a.Jobs, "delete_project", a.deleteProject, jobs.Opts[models.DeletionArgs]{Queue: "deletions"})
	a.CleanRegistry = jobs.Define(a.Jobs, "clean_registry", a.cleanRegistry, jobs.Opts[models.DeletionArgs]{Queue: "deletions",
		Retry: jobs.Retry{Attempts: int(6 * time.Hour / registryWait), Wait: registryWait},
		OnExhausted: func(ctx context.Context, args models.DeletionArgs, err error) {
			a.noteDeletion(ctx, args.ID, "registry space wasn't freed: waited 6 hours ("+err.Error()+")")
		}})
	// A copy that failed or was cancelled before its handover: its new
	// project goes. A delete is refused while its data is still being
	// restored, so it's tried again until that's done.
	a.CopyCleanUp = jobs.Define(a.Jobs, "copy_cleanup", a.copyCleanUp, jobs.Opts[models.CopyArgs]{Queue: "deletions",
		Retry: jobs.Retry{Attempts: 60, Wait: copyWait},
		OnExhausted: func(ctx context.Context, args models.CopyArgs, err error) {
			q := models.New(a.DB.Write)
			if c, qerr := q.CopyByID(context.WithoutCancel(ctx), args.ID); qerr == nil {
				q.SettleCopy(context.WithoutCancel(ctx), models.SettleCopyParams{Status: c.Status, Log: c.Log, Now: time.Now(), ID: c.ID,
					Error: fmt.Sprintf("%s; %s wasn't removed (%v): delete it by hand", c.Error, c.ToName, err)})
			}
		}})
	// The server's update: how it went, once its helper ends, looked at
	// every minute; while one runs, it's also followed every few seconds
	// from its start, so the board can show each step.
	a.ServerUpdate = jobs.Define(a.Jobs, "server_update", a.settleUpdate, jobs.Opts[models.UpdateArgs]{})
	a.ServerUpdate.Every(time.Minute, models.UpdateArgs{})
	a.LatestRelease = jobs.Define(a.Jobs, "check_latest_release", a.checkLatestRelease, jobs.Opts[struct{}]{})
	a.LatestRelease.Every(6*time.Hour, struct{}{})
	return nil
}

// scheduleBackups queues each project's daily backup when it's due: a
// project with something to back up, that has served, with storage to
// back up to.
func (a *App) scheduleBackups(ctx context.Context, _ struct{}) error {
	q := models.New(a.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	projects, err := q.Projects(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, p := range projects {
		if len(p.Volumes.V) == 0 && len(p.Databases.V) == 0 {
			continue
		}
		served, err := q.ProjectHasServed(ctx, p.ID)
		if err != nil || !served {
			continue
		}
		if deleting, err := models.Deleting(ctx, q, p.ID); err != nil || deleting {
			continue
		}
		location, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64)
		if err != nil {
			continue
		}
		schedule, err := models.ParseSchedule(p.BackupSchedule, inst.Zone())
		if err != nil {
			continue
		}
		if due, err := schedule.Due(ctx, q, p.ID, now); err != nil || !due {
			continue
		}
		err = a.DB.Tx(ctx, func(tx *db.Tx) error {
			_, err := models.RequestScheduledBackup(ctx, tx, a.Backup, p, location, schedule.Today(now), now)
			return err
		})
		if err != nil && !db.IsUnique(err) { // another look queued it first
			return err
		}
	}
	return nil
}

// pruneTimeout is how long one location's prune may take.
const pruneTimeout = 3 * time.Hour

// prune runs restic's prune on each location in use; a failure is kept on
// the location for its page, and the next day tries again.
func (a *App) prune(ctx context.Context, _ struct{}) error {
	locations, err := models.New(a.DB.Read).PrunedLocations(ctx)
	if err != nil {
		return err
	}
	for _, l := range locations {
		env := backup.ResticEnv(l)
		ran := a.DockerCLI.Run(ctx, backup.ResticArgs(l, env, []string{"prune", "--retry-lock", "30m"}, "", nil), dockercmd.Opts{Env: env, Timeout: pruneTimeout})
		now := time.Now()
		q := models.New(a.DB.Write)
		if ran.OK {
			err = q.SetPruned(ctx, models.SetPrunedParams{PrunedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now, ID: l.ID})
		} else {
			problem := lastLines(ran.Output, 5)
			if len(problem) > 2000 {
				problem = problem[:1997] + "..."
			}
			a.Log.Error("prune failed", "location", l.Name, "err", problem)
			err = q.SetPruneError(ctx, models.SetPruneErrorParams{PruneError: problem, UpdatedAt: now, ID: l.ID})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(strings.Join(lines[max(0, len(lines)-n):], "\n"))
}

// deleteProject carries out a project's deletion.
func (a *App) deleteProject(ctx context.Context, args models.DeletionArgs) error {
	r := removal.Removal{DB: a.DB, Docker: a.DockerCLI, Registry: a.Registry, Cloudflare: a.Cloudflare, Services: a.Services,
		Backups:   backup.Runner{DB: a.DB, Docker: a.DockerCLI, Tools: a.Tools, ToolsBin: a.ToolsBin, Log: a.Log, Snapshots: a.Snapshots},
		Snapshots: a.Snapshots, Tools: a.Tools, KamalHome: a.KamalHome, Log: a.Log, Refresh: func() { a.Live.Refresh(api.FlightBoard, "") }}
	err := r.Do(ctx, args.ID, func(ctx context.Context, id int64) error {
		_, err := a.CleanRegistry.Enqueue(ctx, models.DeletionArgs{ID: id})
		return err
	})
	if err != nil {
		return jobs.Discard(err) // recorded on the deletion; asking again resumes it
	}
	return nil
}

// settleUpdate records how the server's update went, and follows it.
func (a *App) settleUpdate(ctx context.Context, args models.UpdateArgs) error {
	changed, err := a.Updater.Settle(ctx, time.Now())
	if err != nil {
		return err
	}
	if changed {
		a.Live.Refresh(api.FlightBoard, "")
	}
	if args.Follow {
		if running, err := models.New(a.DB.Read).UpdateRunning(ctx); err != nil || !running {
			return err
		}
		_, err = a.ServerUpdate.EnqueueIn(ctx, args, api.FollowEvery)
	}
	return err
}

// copyWait is how long a copy's clean-up waits before trying again.
const copyWait = 30 * time.Second

// copyCleanUp removes a failed or cancelled copy's new project, with
// nothing kept, since it never served.
func (a *App) copyCleanUp(ctx context.Context, args models.CopyArgs) error {
	q := models.New(a.DB.Read)
	c, err := q.CopyByID(ctx, args.ID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!c.ProjectID.Valid || c.HandedOverAt.Valid) {
		return nil
	}
	if err != nil {
		return err
	}
	p, err := q.ProjectByID(ctx, c.ProjectID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if deleting, err := models.Deleting(ctx, q, p.ID); err != nil || deleting {
		return err
	}
	why := "failed"
	if strings.HasPrefix(c.Error, "cancelled") {
		why = "was cancelled"
	}
	var deletion models.ProjectDeletion
	err = a.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		deletion, err = models.RequestDeletion(ctx, tx, p, p.Name, true, "Houston (the copy of "+c.FromName+" "+why+")", time.Now())
		return err
	})
	var refused models.Refused
	if errors.As(err, &refused) {
		return errors.New(refused.Msg) // tried again: its data may still be being restored
	}
	if err != nil {
		return err
	}
	_, err = a.Delete.Enqueue(ctx, models.DeletionArgs{ID: deletion.ID})
	return err
}

// registryWait is how long the registry's clean-up waits before trying
// again.
const registryWait = 30 * time.Second

// cleanRegistry frees the space of deleted manifests in Houston's
// registry: its garbage collection, which must never run during a push (it
// can delete a layer being uploaded). It holds the lock that keeps deploys
// from starting (the installation's registry_cleanup_since) while it runs.
func (a *App) cleanRegistry(ctx context.Context, args models.DeletionArgs) error {
	now := time.Now()
	var busy error
	err := a.DB.Tx(ctx, func(tx *db.Tx) error {
		q := models.New(tx)
		d, err := q.LiveDeployInFlight(ctx, now.Add(-models.StaleAfter))
		if err == nil {
			what := "deploy"
			if d.Kind == "restore" {
				what = "restore"
			}
			busy = fmt.Errorf("%s #%d of %s is in flight", what, d.Number, d.Project)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		taken, err := q.TakeRegistryLock(ctx, models.TakeRegistryLockParams{Now: sql.NullTime{Time: now, Valid: true},
			Stale: sql.NullTime{Time: now.Add(-models.RegistryCleanupStale), Valid: true}})
		if err == nil && taken == 0 {
			busy = errors.New("the registry is already being cleaned")
		}
		return err
	})
	if err != nil {
		return err
	}
	if busy != nil {
		return busy // wait and try again
	}
	freed, err := a.Registry.Collect(ctx)
	models.New(a.DB.Write).ReleaseRegistryLock(context.Background())
	if err != nil {
		a.noteDeletion(ctx, args.ID, "registry space wasn't freed: "+err.Error())
		return nil
	}
	line := "ok  registry space freed"
	if freed != "" {
		line += " (" + freed + ")"
	}
	a.noteDeletion(ctx, args.ID, line)
	return nil
}

func (a *App) noteDeletion(ctx context.Context, id int64, line string) {
	models.New(a.DB.Write).NoteDeletion(context.WithoutCancel(ctx), models.NoteDeletionParams{Line: line + "\n", Now: time.Now(), ID: id})
}

// checkLatestRelease asks GitHub for Houston's latest release, for the
// flight board's note; a failure is logged, and the next check tries again.
func (a *App) checkLatestRelease(ctx context.Context, _ struct{}) error {
	_, changed, err := a.Release.Check(ctx, a.DB, time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		a.Log.Warn("couldn't check the latest release", "err", err)
		return nil
	}
	if changed {
		a.Live.Refresh(api.FlightBoard, "")
	}
	return nil
}

// Refs reads a project's repo's refs with its deploy key.
func (a *App) Refs(ctx context.Context, p models.Project) (map[string]string, string) {
	return a.Git.Refs(ctx, gitremote.Link{RepoURL: p.RepoUrl, DeployKey: p.DeployKeyPrivate.Reveal()})
}

// checkForChanges reads a linked project's refs and queues a deploy of
// each the deploy rule matches that moved.
func (a *App) checkForChanges(ctx context.Context, args models.CheckArgs) error {
	p, err := models.New(a.DB.Read).ProjectByID(ctx, args.ProjectID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && p.RepoUrl == "" {
		return nil
	}
	if err != nil {
		return err
	}
	queued, err := models.CheckForChanges(ctx, a.DB, a.Refs, p, time.Now())
	if len(queued) > 0 {
		a.Live.Refresh(api.FlightBoard, "")
	}
	return err
}

func (a *App) pollForChanges(ctx context.Context, _ struct{}) error {
	ids, err := models.New(a.DB.Read).PolledProjects(ctx)
	for _, id := range ids {
		if _, err := a.Check.Enqueue(ctx, models.CheckArgs{ProjectID: id}); err != nil {
			return err
		}
	}
	return err
}

// backupWait is how long a busy run waits before it tries again.
const backupWait = 30 * time.Second

// Queues are each queue's workers: backups
// take minutes to hours, one at a time, never in the way of a webhook's
// change check; deletions one at a time; pre-deploy snapshots two, since a
// deploy waits on each.
var Queues = map[string]int{"default": 3, "backups": 1, "snapshots": 2, "deletions": 1}

// backup carries out a backup run: a snapshot, or a restore's data. A
// busy project's run waits and tries again; any other failure is recorded
// on the run, and the job doesn't retry it.
func (a *App) backup(ctx context.Context, args models.BackupArgs) error {
	err := backup.Runner{DB: a.DB, Docker: a.DockerCLI, Tools: a.Tools, ToolsBin: a.ToolsBin, Log: a.Log, Snapshots: a.Snapshots,
		Refresh: func() { a.Live.Refresh(api.FlightBoard, "") }}.Do(ctx, args.RunID)
	if err == nil || errors.Is(err, models.ErrBusy) {
		return err
	}
	return jobs.Discard(err)
}

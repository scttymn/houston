package app

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/scttymn/gantry/jobs"

	"github.com/scttymn/houston/mission-control-go/app/api"
	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
)

// DefineJobs defines the app's background jobs on a.Jobs (gantry's jobs
// package; Rails' Active Job): a name, an arguments struct and a function,
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

// Queues are each queue's workers (the Rails app's queue.yml): backups
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

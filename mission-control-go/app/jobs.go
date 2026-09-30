package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/scttymn/gantry/jobs"

	"github.com/scttymn/houston/mission-control-go/app/api"
	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
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
	a.Check = jobs.Define(a.Jobs, "check_for_changes", a.checkForChanges, jobs.Opts[models.CheckArgs]{})
	return nil
}

// checkForChanges looks at a project's repo for a push to deploy (batch 3
// of docs/plans/mission-control-go.md; until then it says so).
func (a *App) checkForChanges(ctx context.Context, args models.CheckArgs) error {
	return errors.New("change checks don't run in the Go version yet")
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

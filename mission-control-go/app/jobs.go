package app

import (
	"context"
	"errors"

	"github.com/scttymn/gantry/jobs"

	"github.com/scttymn/houston/mission-control-go/app/models"
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
	a.Backup = jobs.Define(a.Jobs, "backup", a.backup, jobs.Opts[models.BackupArgs]{Queue: "backups"})
	a.Check = jobs.Define(a.Jobs, "check_for_changes", a.checkForChanges, jobs.Opts[models.CheckArgs]{})
	return nil
}

// checkForChanges looks at a project's repo for a push to deploy (batch 3
// of docs/plans/mission-control-go.md; until then it says so).
func (a *App) checkForChanges(ctx context.Context, args models.CheckArgs) error {
	return errors.New("change checks don't run in the Go version yet")
}

// backup carries out a backup run: a snapshot, or a restore's data (batch
// 3 of docs/plans/mission-control-go.md; until then it says so).
func (a *App) backup(ctx context.Context, args models.BackupArgs) error {
	return errors.New("backups don't run in the Go version yet")
}

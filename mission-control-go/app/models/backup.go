package models

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/scttymn/gantry/db"
)

// NothingDeployed is a snapshot asked for before anything served: there's
// nothing to hold yet.
const NothingDeployed = "nothing deployed yet"

// BackupStaleAfter is how long a running backup may go without a word (its
// job beats every 15 s) before it's abandoned.
const BackupStaleAfter = 2 * time.Minute

// BackupArgs is the backup job's: the run it carries out.
type BackupArgs struct {
	RunID int64 `json:"run_id"`
}

// Enqueuer queues a job inside a transaction (gantry's jobs.Job).
type Enqueuer[A any] interface {
	EnqueueTx(ctx context.Context, tx *db.Tx, a A) (int64, error)
}

// RequestSnapshot is deploy d's snapshot before it goes on (reason deploy),
// or a restore's safety snapshot (reason restore): one per deploy, so a
// retried request gets the same run; a new one is queued with its job.
func RequestSnapshot(ctx context.Context, tx *db.Tx, jobs Enqueuer[BackupArgs], p Project, d Deploy, now time.Time) (BackupRun, error) {
	q := New(tx)
	reason := "deploy"
	if d.Restore() {
		reason = "restore"
	}
	served, err := q.ProjectHasServed(ctx, p.ID)
	if err != nil {
		return BackupRun{}, err
	}
	if !served {
		return BackupRun{}, Refused{NothingDeployed}
	}
	location, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64) // 0: no row has it
	if errors.Is(err, sql.ErrNoRows) {
		return BackupRun{}, Refused{"no backup storage yet (finish setup's storage step)"}
	}
	if err != nil {
		return BackupRun{}, err
	}
	number := sql.NullInt64{Int64: d.Number, Valid: true}
	run, err := q.DeploySnapshot(ctx, DeploySnapshotParams{ProjectID: p.ID, Reason: reason, DeployNumber: number})
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return run, err
	}
	run, err = q.CreateBackupRun(ctx, CreateBackupRunParams{ProjectID: p.ID, LocationID: location.ID, Operation: "backup", Kind: "deploy",
		Reason: reason, DeployNumber: number, HeartbeatAt: now})
	if err != nil {
		return BackupRun{}, err
	}
	_, err = jobs.EnqueueTx(ctx, tx, BackupArgs{RunID: run.ID})
	return run, err
}

// RequestRestoreData is a restore's data, put back from its snapshot into
// the generation it builds: one per restore.
func RequestRestoreData(ctx context.Context, tx *db.Tx, jobs Enqueuer[BackupArgs], d Deploy, now time.Time) (BackupRun, error) {
	q := New(tx)
	number := sql.NullInt64{Int64: d.Number, Valid: true}
	run, err := q.RestoreRun(ctx, RestoreRunParams{ProjectID: d.ProjectID, DeployNumber: number})
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return run, err
	}
	run, err = q.CreateBackupRun(ctx, CreateBackupRunParams{ProjectID: d.ProjectID, LocationID: d.SourceLocationID.Int64, Operation: "restore",
		Kind: "restore", Reason: "restore", DeployNumber: number, SourceSnapshotID: d.SourceSnapshotID, HeartbeatAt: now})
	if err != nil {
		return BackupRun{}, err
	}
	_, err = jobs.EnqueueTx(ctx, tx, BackupArgs{RunID: run.ID})
	return run, err
}

// Stale is a running run silent for BackupStaleAfter: Mission Control
// stopped during it. The next claim for the project records that; answers
// say so meanwhile.
func (r BackupRun) Stale(now time.Time) bool {
	return r.Status == "running" && r.HeartbeatAt.Before(now.Add(-BackupStaleAfter))
}

// StaleError is what a stale run is finished with.
func (r BackupRun) StaleError() string {
	return "Mission Control stopped during the backup (no word since " + r.HeartbeatAt.UTC().Format(time.RFC3339) + ")"
}

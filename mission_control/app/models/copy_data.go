package models

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/scttymn/gantry/db"
)

// CopyData is where a copy's data stands: a
// snapshot of the old project, taken when the copy's runner asks (as late
// as possible), then restored into the new project's generation. Run is
// the snapshot while it runs, then the restore; without one, Status says
// where it stands (skipped: nothing to copy; queued: not asked for yet;
// no_go). Error, when set, is what the answer says went wrong.
type CopyData struct {
	Run    *BackupRun
	Status string
	Error  string
}

func skippedData(why string) CopyData { return CopyData{Status: "skipped", Error: why} }

// copySource is the old project, or why its data isn't copied.
func copySource(ctx context.Context, q *Queries, c ProjectCopy) (Project, string, error) {
	if !c.FromProjectID.Valid {
		return Project{}, c.FromName + " is gone: its data can't be copied", nil
	}
	old, err := q.ProjectByID(ctx, c.FromProjectID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, c.FromName + " is gone: its data can't be copied", nil
	}
	if err != nil {
		return Project{}, "", err
	}
	if len(old.Volumes.V) == 0 && len(old.Databases.V) == 0 {
		return old, c.FromName + " has no data to copy", nil
	}
	served, err := q.ProjectHasServed(ctx, old.ID)
	if err != nil {
		return old, "", err
	}
	if !served {
		return old, c.FromName + " has never been deployed: no data to copy", nil
	}
	return old, "", nil
}

// StartCopyData asks for the old project's snapshot, once: a retried
// request gets the same run.
func StartCopyData(ctx context.Context, tx *db.Tx, jobs Enqueuer[BackupArgs], d Deploy, now time.Time) (CopyData, error) {
	q := New(tx)
	c, err := q.CopyByDeploy(ctx, sql.NullInt64{Int64: d.ID, Valid: true})
	if err != nil {
		return CopyData{}, err
	}
	old, why, err := copySource(ctx, q, c)
	if err != nil || why != "" {
		return skippedData(why), err
	}
	if !c.SnapshotRunID.Valid {
		location, err := q.BackupLocationFor(ctx, old.BackupLocationID.Int64)
		if errors.Is(err, sql.ErrNoRows) {
			return CopyData{Status: "no_go", Error: "no backup storage for the copy's snapshot"}, nil
		}
		if err != nil {
			return CopyData{}, err
		}
		run, err := q.CreateCopySnapshot(ctx, CreateCopySnapshotParams{ProjectID: old.ID, LocationID: location.ID, HeartbeatAt: now})
		if err != nil {
			return CopyData{}, err
		}
		if err := q.SetCopySnapshot(ctx, SetCopySnapshotParams{SnapshotRunID: sql.NullInt64{Int64: run.ID, Valid: true}, UpdatedAt: now, ID: c.ID}); err != nil {
			return CopyData{}, err
		}
		if _, err := jobs.EnqueueTx(ctx, tx, BackupArgs{RunID: run.ID}); err != nil {
			return CopyData{}, err
		}
	}
	return CopyDataStatus(ctx, tx, jobs, d, now)
}

// CopyDataStatus is where the data stands: the snapshot while it runs,
// then the restore, asked for once the snapshot is GO.
func CopyDataStatus(ctx context.Context, tx *db.Tx, jobs Enqueuer[BackupArgs], d Deploy, now time.Time) (CopyData, error) {
	q := New(tx)
	c, err := q.CopyByDeploy(ctx, sql.NullInt64{Int64: d.ID, Valid: true})
	if err != nil {
		return CopyData{}, err
	}
	if !c.SnapshotRunID.Valid {
		if _, why, err := copySource(ctx, q, c); err != nil || why != "" {
			if why != "" {
				why = c.FromName + " has no data to copy"
			}
			return skippedData(why), err
		}
		return CopyData{Status: "queued"}, nil
	}
	snapshot, err := q.BackupRunByID(ctx, c.SnapshotRunID.Int64)
	if err != nil {
		return CopyData{}, err
	}
	switch snapshot.Status {
	case "queued", "running":
		return CopyData{Run: &snapshot}, nil
	case "skipped":
		return skippedData(snapshot.Error), nil
	case "no_go":
		return CopyData{Run: &snapshot, Error: "the snapshot of " + c.FromName + " failed: " + snapshot.Error}, nil
	}
	if err := q.SetDeploySource(ctx, SetDeploySourceParams{SourceSnapshotID: snapshot.SnapshotID,
		SourceLocationID: sql.NullInt64{Int64: snapshot.LocationID, Valid: true}, UpdatedAt: now, ID: d.ID}); err != nil {
		return CopyData{}, err
	}
	if d, err = q.DeployByID(ctx, d.ID); err != nil {
		return CopyData{}, err
	}
	run, err := RequestRestoreData(ctx, tx, jobs, d, now)
	return CopyData{Run: &run}, err
}

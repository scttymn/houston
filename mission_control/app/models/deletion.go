package models

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/scttymn/gantry/db"
)

// DeletionStaleAfter is how long a running deletion may go without a word
// (it beats every 15 s) before asking again takes it over.
const DeletionStaleAfter = 2 * time.Minute

// Deleting is whether p is being deleted: nothing new starts for it.
func Deleting(ctx context.Context, q *Queries, projectID int64) (bool, error) {
	_, err := q.HoldingDeletion(ctx, sql.NullInt64{Int64: projectID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// refuseWhileDeleting is Refused when p is being deleted.
func refuseWhileDeleting(ctx context.Context, q *Queries, p Project) error {
	deleting, err := Deleting(ctx, q, p.ID)
	if err != nil {
		return err
	}
	if deleting {
		return Refused{p.Name + " is being deleted"}
	}
	return nil
}

// Stale is a running deletion gone silent.
func (d ProjectDeletion) Stale(now time.Time) bool {
	return d.Status == "running" && d.HeartbeatAt.Before(now.Add(-DeletionStaleAfter))
}

// RequestDeletion queues p's deletion, or resumes one that stopped partway
// (or went silent); confirm must be p's name, and nothing else may be
// underway. The caller queues its job after the transaction.
func RequestDeletion(ctx context.Context, tx *db.Tx, p Project, confirm string, deleteBackups bool, by string, now time.Time) (ProjectDeletion, error) {
	refuse := func(format string, args ...any) (ProjectDeletion, error) {
		return ProjectDeletion{}, Refused{fmt.Sprintf(format, args...)}
	}
	if confirm != p.Name {
		return refuse("type %s to confirm", p.Name)
	}
	q := New(tx)
	busy, err := q.BusyDeploy(ctx, p.ID)
	if err == nil {
		what := "deploy"
		if busy.Restore() {
			what = "restore"
		}
		return refuse("%s #%d is %s; wait for #%d", what, busy.Number, busy.StatusWords(), busy.Number)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ProjectDeletion{}, err
	}
	run, err := q.BusyBackupRun(ctx, p.ID)
	if err == nil {
		what := "a backup"
		if run.Operation == "restore" {
			what = "a restore's data"
		}
		return refuse("%s of %s is %s; wait for it", what, p.Name, run.Status)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ProjectDeletion{}, err
	}
	current, err := q.HoldingDeletion(ctx, sql.NullInt64{Int64: p.ID, Valid: true})
	switch {
	case err == nil && (current.Status == "queued" || current.Status == "running") && !current.Stale(now):
		return refuse("%s is already being deleted", p.Name)
	case err == nil:
		return q.RequeueDeletion(ctx, RequeueDeletionParams{ID: current.ID, Now: now})
	case !errors.Is(err, sql.ErrNoRows):
		return ProjectDeletion{}, err
	}
	d, err := q.CreateDeletion(ctx, CreateDeletionParams{ProjectID: sql.NullInt64{Int64: p.ID, Valid: true}, Name: p.Name, RepoUrl: p.RepoUrl,
		RequestedBy: by, DeleteBackups: deleteBackups, HeartbeatAt: now})
	if db.IsUnique(err) {
		return refuse("%s is already being deleted", p.Name)
	}
	return d, err
}

// DeletionArgs is the deletion job's, and the registry clean-up's after it.
type DeletionArgs struct {
	ID int64 `json:"id"`
}

package models

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/scttymn/gantry/db"
)

// ErrBusy is a backup run that must wait: its project has another running,
// or a restore underway that it would catch half done, or the server is
// updating.
var ErrBusy = errors.New("the project is already backing up")

// ClaimRun flips run from queued to running with a new token, in tx, if
// it's still queued and the project has no live running backup (a silent
// one is abandoned first). It's the token, or "" when the run isn't queued
// (a second delivery of its job); ErrBusy when it must wait.
func ClaimRun(ctx context.Context, tx *db.Tx, run BackupRun, now time.Time) (string, error) {
	q := New(tx)
	// The server's update recreates the runners and the helpers' network.
	if updating, err := q.UpdateRunning(ctx); err != nil || updating {
		if err == nil {
			err = ErrBusy
		}
		return "", err
	}
	stale, err := q.StaleRunning(ctx, StaleRunningParams{ProjectID: run.ProjectID, HeartbeatAt: now.Add(-BackupStaleAfter)})
	if err != nil {
		return "", err
	}
	for _, s := range stale {
		if err := q.AbandonRun(ctx, AbandonRunParams{ID: s.ID, Now: sql.NullTime{Time: now, Valid: true}, Error: s.StaleError()}); err != nil {
			return "", err
		}
	}
	other, err := q.OtherRunning(ctx, OtherRunningParams{ProjectID: run.ProjectID, ID: run.ID})
	if err != nil {
		return "", err
	}
	if other {
		return "", ErrBusy
	}
	// While a restore is queued or in flight, only its own runs (its safety
	// snapshot, its data) go ahead: a backup now would catch it half done.
	if run.Reason != "restore" {
		underway, err := q.RestoreUnderway(ctx, run.ProjectID)
		if err != nil {
			return "", err
		}
		if underway {
			return "", ErrBusy
		}
	}
	token := NewToken()
	n, err := q.ClaimRun(ctx, ClaimRunParams{TokenDigest: Digest(token), Now: now, ID: run.ID})
	if db.IsUnique(err) {
		return "", ErrBusy
	}
	if err != nil || n != 1 {
		return "", err
	}
	return token, nil
}

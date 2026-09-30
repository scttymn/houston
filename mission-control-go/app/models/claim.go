package models

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"

	"github.com/scttymn/gantry/db"
)

// RunnerName is a runner's: houston-runner-N.
var RunnerName = regexp.MustCompile(`^houston-runner-\d+$`)

// ClaimNext hands the oldest claimable queued deploy to runner, in tx, or
// nil. Silent in-flight deploys are finished first; a project with a live
// one keeps its queued deploy back. None while the registry is cleaned.
// Without a deploy to hand over, it's only the copies' clean-ups those
// finished need, when there are some.
func ClaimNext(ctx context.Context, tx *db.Tx, runner string, now time.Time) (*Started, error) {
	q := New(tx)
	cleaning, err := q.RegistryCleaning(ctx, sql.NullTime{Time: now.Add(-RegistryCleanupStale), Valid: true})
	if err != nil || cleaning {
		return nil, err
	}
	stale, err := q.StaleInFlight(ctx, now.Add(-StaleAfter))
	if err != nil {
		return nil, err
	}
	tookOver := map[int64]int64{}
	var cleanUp []int64
	for _, d := range stale {
		id, err := abandon(ctx, q, d, now)
		if err != nil {
			return nil, err
		}
		tookOver[d.ProjectID] = d.Number
		if id != 0 {
			cleanUp = append(cleanUp, id)
		}
	}
	candidate, err := q.NextQueued(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		if cleanUp != nil {
			return &Started{CleanUp: cleanUp}, nil
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	project, err := q.ProjectByID(ctx, candidate.ProjectID)
	if err != nil {
		return nil, err
	}
	// A restore builds the next generation; a deploy uses the project's.
	generation := project.DataGeneration
	if candidate.Restore() {
		generation++
	}
	token := NewToken()
	n, err := q.ClaimQueued(ctx, ClaimQueuedParams{Runner: runner, TokenDigest: Digest(token), Generation: generation, Now: now, ID: candidate.ID})
	if err != nil || n != 1 {
		return nil, err
	}
	d, err := q.DeployByID(ctx, candidate.ID)
	if err != nil {
		return nil, err
	}
	if _, err := SettleCopy(ctx, q, d, now); err != nil {
		return nil, err
	}
	return &Started{Deploy: d, Token: token, TookOver: tookOver[d.ProjectID], CleanUp: cleanUp}, nil
}

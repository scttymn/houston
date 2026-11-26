package models

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
)

// RunnerName is a runner's: houston-runner-N.
var RunnerName = regexp.MustCompile(`^houston-runner-\d+$`)

// ClaimNext hands the oldest claimable queued deploy to runner, in tx, or
// nil. Silent in-flight deploys are finished first, except a runner-claimed
// deploy that never started work is put back on the queue once, when
// nothing else is waiting. That next hand-off still sets TookOver, so the
// runner releases Kamal's lock. Restores and copies are still abandoned. A
// project with a live one keeps its queued deploy back. A later claim of a
// waiting requeued row also sets TookOver, from the requeue line in the log.
// None while the registry is cleaned, or the server updates.
// Without a deploy to hand over, it's only the copies' clean-ups those
// finished need, when there are some.
func ClaimNext(ctx context.Context, tx *db.Tx, runner string, now time.Time) (*Started, error) {
	q := New(tx)
	cleaning, err := q.RegistryCleaning(ctx, sql.NullTime{Time: now.Add(-RegistryCleanupStale), Valid: true})
	if err != nil || cleaning {
		return nil, err
	}
	// Nor while the server updates: the installer recreates the runners.
	if updating, err := q.UpdateRunning(ctx); err != nil || updating {
		return nil, err
	}
	stale, err := q.StaleInFlight(ctx, now.Add(-StaleAfter))
	if err != nil {
		return nil, err
	}
	tookOver := map[int64]int64{}
	var cleanUp []int64
	for _, d := range stale {
		n, err := q.ReturnToQueue(ctx, ReturnToQueueParams{Now: now, ID: d.ID})
		if err != nil {
			return nil, err
		}
		if n == 1 {
			tookOver[d.ProjectID] = d.Number
			continue
		}
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
	took := tookOver[d.ProjectID]
	if took == 0 && hasRequeueMarker(d.Log) {
		took = d.Number
	}
	return &Started{Deploy: d, Token: token, TookOver: took, CleanUp: cleanUp}, nil
}

// requeuedLogPrefix is written into a deploy's log when a silent unstarted
// claim is put back on the queue. A later claim that sees it sets TookOver
// so the runner releases Kamal's lock.
const requeuedLogPrefix = "requeued: "

func hasRequeueMarker(log string) bool {
	return strings.Contains(log, requeuedLogPrefix)
}

// withoutRequeueMarker drops requeue lines so a newer push on the same row
// still gets one retry of its own.
func withoutRequeueMarker(log string) string {
	if !hasRequeueMarker(log) {
		return log
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(log, "\n") {
		if strings.HasPrefix(line, requeuedLogPrefix) {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

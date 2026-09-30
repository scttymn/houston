package models

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
)

const (
	// LogCap is how much of a deploy's log Houston keeps: its first 4 MiB.
	LogCap = 4 << 20
	// ChunkCap is the most a report may add to it at once.
	ChunkCap = 256 << 10
	// Switched is the step a restore reports once Kamal has switched traffic
	// to its generation: the runner removes the old one only after Mission
	// Control has taken it.
	Switched = "Clean up"

	truncated = "\n[log truncated: Houston keeps the first 4 MiB of a deploy's log]\n"
)

// Progress is one report of houston deploy's: what it sets (nil: nothing).
type Progress struct {
	Step, Log, Status, Error, ProposedName *string
}

// Reported is what a report did: the deploy as it is now, what was added
// to its log (what's past the cap isn't), and a restore's kept compose.yml
// if its switch applied it.
type Reported struct {
	Deploy   Deploy
	Appended string
	Adopted  *Adopted
	// CleanUp is a copy whose new project goes: its deploy failed before
	// it took any host over.
	CleanUp int64
}

// Report applies p to d, in tx; the caller has checked, in tx, that d is
// the reporter's and still in flight. Past the switch (or GO, should that
// report have been lost), a restore's generation is the one serving: every
// deploy from now uses it, and its compose.yml describes the project.
func Report(ctx context.Context, tx *db.Tx, d Deploy, p Progress, now time.Time) (Reported, error) {
	q := New(tx)
	set := func(field *string, v *string) {
		if v != nil {
			*field = *v
		}
	}
	set(&d.Step, p.Step)
	set(&d.Error, p.Error)
	set(&d.ProposedName, p.ProposedName)
	if p.Status != nil {
		d.Status = *p.Status
		d.FinishedAt = sql.NullTime{Time: now, Valid: true}
	}
	d, err := q.SaveProgress(ctx, SaveProgressParams{Step: d.Step, Error: d.Error, ProposedName: d.ProposedName, Status: d.Status,
		FinishedAt: d.FinishedAt, HeartbeatAt: now, UpdatedAt: now, ID: d.ID})
	if err != nil {
		return Reported{}, err
	}
	out := Reported{Deploy: d}
	if p.Status != nil {
		if out.CleanUp, err = SettleCopy(ctx, New(tx), d, now); err != nil {
			return Reported{}, err
		}
	}
	if d.Restore() && (p.Step != nil && *p.Step == Switched || p.Status != nil && *p.Status == "go") {
		if err := q.MarkSwitched(ctx, MarkSwitchedParams{SwitchedAt: sql.NullTime{Time: now, Valid: true}, ID: d.ID}); err != nil {
			return Reported{}, err
		}
		// Only ever forward, in one statement: no stale read of the project.
		moved, err := q.MoveGenerationForward(ctx, MoveGenerationForwardParams{ID: d.ProjectID, Generation: d.Generation, Now: now})
		if err != nil {
			return Reported{}, err
		}
		if moved == 1 {
			// One that can't be applied is logged by the caller, never
			// failed: it's past the switch, and the next deploy's sync
			// applies its own.
			out.Adopted, _ = Adopt(ctx, tx, d, now)
		}
	}
	if p.Log != nil && *p.Log != "" {
		size, err := q.LogSize(ctx, d.ID)
		if err != nil {
			return Reported{}, err
		}
		if size < LogCap {
			piece := *p.Log
			if room := LogCap - int(size); len(piece) > room {
				piece = strings.ToValidUTF8(piece[:room], "") + truncated
			}
			if err := q.AppendLog(ctx, AppendLogParams{Log: piece, ID: d.ID}); err != nil {
				return Reported{}, err
			}
			out.Appended = piece
		}
	}
	return out, nil
}

package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// CommitCheck is whether a project's repo still has a commit: "" when it
// has, else why not (a gitremote.Git's Commit).
type CommitCheck func(ctx context.Context, p models.Project, sha string) string

// RequestRestore queues a restore of p to a snapshot, code and data
// together, into the next data generation. It's refused, with nothing
// made, unless everything it needs is there, the snapshot's commit in the
// repo included; confirm must be p's name.
func RequestRestore(ctx context.Context, d *db.DB, snapshots *Snapshots, commit CommitCheck, p models.Project, snapshot string,
	location *models.StorageLocation, confirm string, now time.Time) (models.Deploy, error) {
	refuse := func(format string, args ...any) (models.Deploy, error) {
		return models.Deploy{}, models.Refused{Msg: fmt.Sprintf(format, args...)}
	}
	if confirm != p.Name {
		return refuse("type %s to confirm", p.Name)
	}
	q := models.New(d.Read)
	if deleting, err := models.Deleting(ctx, q, p.ID); err != nil {
		return models.Deploy{}, err
	} else if deleting {
		return refuse("%s is being deleted", p.Name)
	}
	if p.RepoUrl == "" {
		return refuse("link the repo first (houston link): a restore fetches the snapshot's commit from it")
	}
	busy, err := q.BusyDeploy(ctx, p.ID)
	if err == nil {
		what := "deploy"
		if busy.Restore() {
			what = "restore"
		}
		return refuse("%s #%d is %s; wait for #%d", what, busy.Number, busy.StatusWords(), busy.Number)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return models.Deploy{}, err
	}
	locations, err := LocationsFor(ctx, q, p)
	if err != nil {
		return models.Deploy{}, err
	}
	known := location != nil
	if known {
		known = false
		for _, l := range locations {
			known = known || l.ID == location.ID
		}
	}
	if !known {
		name := "that location"
		if location != nil {
			name = location.Name
		}
		return refuse("%s never backed up to %s", p.Name, name)
	}
	list, err := snapshots.For(ctx, p.Name, *location)
	var unavailable Unavailable
	if errors.As(err, &unavailable) {
		return refuse("can't read %s's snapshots: %s", location.Name, unavailable)
	}
	if err != nil {
		return models.Deploy{}, err
	}
	var found *Snapshot
	for i := range list {
		if snapshot != "" && (list[i].ID == snapshot || list[i].ShortID == snapshot) {
			found = &list[i]
			break
		}
	}
	if found == nil {
		return refuse("snapshot %s isn't in %s's snapshots of %s", snapshot, location.Name, p.Name)
	}
	if problem := commit(ctx, p, found.Sha); problem != "" {
		return refuse("commit %s isn't in %s any more (was history rewritten, or the repo relinked?): %s", found.Sha[:min(7, len(found.Sha))], p.RepoUrl, problem)
	}
	// The new generation's container names, claimed before any exists (the
	// sync after its switch keeps them and lets the old generation's go).
	next := p.DataGeneration + 1
	names := p.HostNames(next)
	owner, err := q.ProjectHostOwner(ctx, models.ProjectHostOwnerParams{Names: names, Project: p.Name})
	if err == nil {
		return refuse("the container name %s belongs to project %s; rename a service or that project first", owner.Name, owner.Project)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return models.Deploy{}, err
	}
	var restore models.Deploy
	err = d.Tx(ctx, func(tx *db.Tx) error {
		q := models.New(tx)
		have, err := q.ProjectHostNames(ctx, p.ID)
		if err != nil {
			return err
		}
		for _, name := range names {
			if !contains(have, name) {
				if err := q.ClaimProjectHost(ctx, models.ClaimProjectHostParams{ProjectID: p.ID, Name: name}); err != nil {
					return err
				}
			}
		}
		number, err := q.NextDeployNumber(ctx, p.ID)
		if err != nil {
			return err
		}
		restore, err = q.CreateRestore(ctx, models.CreateRestoreParams{ProjectID: p.ID, Number: number, Sha: found.Sha, Ref: "refs/restore/" + found.ShortID,
			Generation: next, HeartbeatAt: now, SourceSnapshotID: found.ID, SourceLocationID: sql.NullInt64{Int64: location.ID, Valid: true}})
		return err
	})
	if db.IsUnique(err) {
		// Two requests at once: the one-queued-a-project lock took the other.
		return refuse("another deploy or restore of %s was just queued; wait for it", p.Name)
	}
	return restore, err
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

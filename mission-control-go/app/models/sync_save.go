package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/scttymn/gantry/db"
)

// Refused is a sync that can't be applied, in words for the person
// deploying (the Rails app's ProjectSync::Refused).
type Refused struct{ Msg string }

func (e Refused) Error() string { return e.Msg }

// Save saves the project and claims the container names it owns, in tx, and
// is the project and the domains the sync dropped. A name another project
// owns is Refused, and tx should be rolled back.
func (s Sync) Save(ctx context.Context, tx *db.Tx, now time.Time) (Project, []string, error) {
	q := New(tx)
	var dropped []string
	existing, err := q.ProjectByName(ctx, s.Name)
	switch {
	case err == nil:
		if err := refuseWhileDeleting(ctx, q, existing); err != nil {
			return Project{}, nil, err
		}
		for _, d := range existing.Domains.V {
			if !contains(s.Domains, d) {
				dropped = append(dropped, d)
			}
		}
	case !errors.Is(err, sql.ErrNoRows):
		return Project{}, nil, err
	}
	p, err := q.SaveSynced(ctx, SaveSyncedParams{Name: s.Name, AppService: s.AppService, Services: Names{V: s.Services},
		Domains: Names{V: s.Domains}, Variables: Variables{V: s.Variables}, Health: s.Health, Port: int64(s.Port),
		DeployRule: Object{V: s.DeployRule}, Volumes: Volumes{V: s.Volumes}, Databases: Databases{V: s.Databases},
		KeepAuto: int64(s.KeepAuto), KeepDeploy: int64(s.KeepDeploy), BackupSchedule: s.BackupSchedule,
		MaintenancePage: s.MaintenancePage, Details: ProjectDetails{V: s.Details}, Now: sql.NullTime{Time: now, Valid: true}})
	if err != nil {
		return Project{}, nil, err
	}
	names := p.HostNames(p.DataGeneration)
	if err := q.ReleaseProjectHosts(ctx, ReleaseProjectHostsParams{ProjectID: p.ID, Keep: names}); err != nil {
		return Project{}, nil, err
	}
	have, err := q.ProjectHostNames(ctx, p.ID)
	if err != nil {
		return Project{}, nil, err
	}
	for _, name := range names {
		if contains(have, name) {
			continue
		}
		err := q.ClaimProjectHost(ctx, ClaimProjectHostParams{ProjectID: p.ID, Name: name})
		if db.IsUnique(err) {
			owner, ownerErr := q.ProjectHostOwner(ctx, ProjectHostOwnerParams{Names: names, Project: s.Name})
			if ownerErr != nil {
				return Project{}, nil, Refused{fmt.Sprintf("another project claimed one of %s's container names; try again", s.Name)}
			}
			return Project{}, nil, Refused{fmt.Sprintf("the container name %s belongs to project %s; rename a service or the project", owner.Name, owner.Project)}
		}
		if err != nil {
			return Project{}, nil, err
		}
	}
	return p, dropped, nil
}

// Adopted is a restore's kept compose.yml, applied: the project as it
// saved it, and the domains it dropped, for DNS after the commit.
type Adopted struct {
	Project Project
	Dropped []string
}

// Adopt applies the compose.yml a restore's check sync kept (the project's
// config and container names), in a savepoint of tx: one that can't be
// applied (another project took one of its names) is its error, and leaves
// tx as it was, for it's applied past the switch and a failed report would
// leave the restore to go stale while it serves. Nil without one.
func Adopt(ctx context.Context, tx *db.Tx, d Deploy, now time.Time) (*Adopted, error) {
	if !d.SyncPayload.Valid {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(d.SyncPayload.String), &fields); err != nil {
		return nil, err
	}
	s, errs := ParseSync(fields)
	if errs != nil {
		return nil, fmt.Errorf("its compose.yml isn't valid: %v", errs)
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT adopt"); err != nil {
		return nil, err
	}
	p, dropped, err := s.Save(ctx, tx, now)
	if err != nil {
		if _, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO adopt"); rollbackErr != nil {
			return nil, rollbackErr
		}
		return nil, err
	}
	_, err = tx.ExecContext(ctx, "RELEASE adopt")
	return &Adopted{Project: p, Dropped: dropped}, err
}

// CatchUp moves p forward to the generation kamal-proxy serves (observed,
// at commit sha, as a runner read it) after a restore that switched but
// never said so: the restore that built it is marked switched, and its
// kept compose.yml applied. It's what it applied, or nil; adoptErr is why
// a kept one wasn't, for the log.
func CatchUp(ctx context.Context, d *db.DB, p Project, observed int, sha string, now time.Time) (adopted *Adopted, adoptErr error, err error) {
	if observed == 0 || sha == "" {
		return nil, nil, nil
	}
	restore, err := New(d.Read).RestoreServing(ctx, RestoreServingParams{ProjectID: p.ID, Generation: int64(observed), Sha: sha})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	err = d.Tx(ctx, func(tx *db.Tx) error {
		q := New(tx)
		moved, err := q.MoveGenerationForward(ctx, MoveGenerationForwardParams{ID: p.ID, Generation: int64(observed), Now: now})
		if err != nil || moved != 1 {
			return err
		}
		if err := q.MarkSwitched(ctx, MarkSwitchedParams{SwitchedAt: sql.NullTime{Time: now, Valid: true}, ID: restore.ID}); err != nil {
			return err
		}
		adopted, adoptErr = Adopt(ctx, tx, restore, now)
		return nil
	})
	return adopted, adoptErr, err
}

// Secrets are p's secrets' values, by key.
func Secrets(ctx context.Context, q *Queries, projectID int64) (map[string]string, error) {
	rows, err := q.ProjectSecrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, r := range rows {
		values[r.Key] = r.Value.Reveal()
	}
	return values, nil
}

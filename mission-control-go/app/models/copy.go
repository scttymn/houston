package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/internal/mission"
)

// CopyArgs is the copy clean-up's: the copy whose new project goes.
type CopyArgs struct {
	ID int64 `json:"id"`
}

// Placeholder is the host a copy's deploy leaves its service on until the
// handover, so it's never kamal-proxy's catch-all.
func Placeholder(name string) string { return name + ".houston-copy.invalid" }

// RepoReader reads a project's repo at its branch: the head's commit and
// what was read, or the problems (a gitremote.Git's Read).
type RepoReader func(ctx context.Context, p Project) (sha string, in *mission.Inspection, problems string)

// RequestCopy copies from to the new name its latest deploy proposes (it
// held because compose.yml names another project): the new project, from
// the same repo and compose file, with the secrets it references and where
// its volumes live, and its first deploy, a copy, queued.
func RequestCopy(ctx context.Context, d *db.DB, read RepoReader, from Project, confirm, by string, now time.Time) (ProjectCopy, error) {
	refuse := func(format string, args ...any) (ProjectCopy, error) {
		return ProjectCopy{}, Refused{fmt.Sprintf(format, args...)}
	}
	if confirm != from.Name {
		return refuse("type %s to confirm", from.Name)
	}
	q := New(d.Read)
	if active, err := q.ActiveCopyFrom(ctx, sql.NullInt64{Int64: from.ID, Valid: true}); err == nil {
		return refuse("%s is already being copied to %s", from.Name, active.ToName)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ProjectCopy{}, err
	}
	if busy, err := q.BusyDeploy(ctx, from.ID); err == nil {
		what := "deploy"
		if busy.Restore() {
			what = "restore"
		}
		return refuse("%s #%d is %s; wait for #%d", what, busy.Number, busy.StatusWords(), busy.Number)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ProjectCopy{}, err
	}
	if run, err := q.BusyBackupRun(ctx, from.ID); err == nil {
		what := "a backup"
		if run.Operation == "restore" {
			what = "a restore's data"
		}
		return refuse("%s of %s is %s; wait for it", what, from.Name, run.Status)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ProjectCopy{}, err
	}
	if err := refuseWhileDeleting(ctx, q, from); err != nil {
		return ProjectCopy{}, err
	}
	latest, err := q.DeploySummaries(ctx, DeploySummariesParams{ProjectID: from.ID, Limit: 1})
	if err != nil {
		return ProjectCopy{}, err
	}
	if len(latest) == 0 || latest[0].Status != "hold" || latest[0].ProposedName == "" {
		return refuse("%s's latest deploy doesn't propose a copy: change name: in compose.yml, push, and its deploy holds with the new name", from.Name)
	}
	to := latest[0].ProposedName
	if !ValidName(to) {
		return refuse("%s can't be a project's name", to)
	}
	if taken, err := q.ProjectExists(ctx, to); err != nil || taken {
		if err != nil {
			return ProjectCopy{}, err
		}
		return refuse("%s is another project", to)
	}
	served, err := q.ProjectHasServed(ctx, from.ID)
	if err != nil {
		return ProjectCopy{}, err
	}
	if (len(from.Volumes.V) > 0 || len(from.Databases.V) > 0) && served {
		if _, err := q.BackupLocationFor(ctx, from.BackupLocationID.Int64); err != nil {
			return refuse("no backup storage for the copy's snapshot of %s's data (finish setup's storage step)", from.Name)
		}
	}
	sha, in, problems := read(ctx, from)
	if in == nil {
		return refuse("couldn't read %s: %s", from.RepoUrl, problems)
	}
	if in.Sync.Name != to {
		return refuse("%s's compose.yml on %s now names %s, not %s: wait for its deploy to hold, then copy", from.Name, from.Branch, in.Sync.Name, to)
	}
	raw, _ := json.Marshal(in.Sync)
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	s, errs := ParseSync(fields)
	if errs != nil {
		keys := make([]string, 0, len(errs))
		for k := range errs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, k+": "+sentence(errs[k]))
		}
		return refuse("%s", sentence(parts))
	}
	var copied ProjectCopy
	err = d.Tx(ctx, func(tx *db.Tx) error {
		q := New(tx)
		p, _, err := s.Save(ctx, tx, now)
		if err != nil {
			return err
		}
		if err := q.SetRepo(ctx, SetRepoParams{RepoUrl: from.RepoUrl, Branch: from.Branch, ComposePath: from.ComposePath, DeployKeyPrivate: from.DeployKeyPrivate,
			DeployKeyPublic: from.DeployKeyPublic, WebhookSecret: from.WebhookSecret, UpdatedAt: now, ID: p.ID}); err != nil {
			return err
		}
		if err := q.CopyProjectSettings(ctx, CopyProjectSettingsParams{WebhookVerifiedAt: from.WebhookVerifiedAt, SeenRefs: from.SeenRefs,
			BackupLocationID: from.BackupLocationID, UpdatedAt: now, ID: p.ID}); err != nil {
			return err
		}
		secrets, err := q.ProjectSecrets(ctx, from.ID)
		if err != nil {
			return err
		}
		for _, secret := range secrets {
			if slicesHasVariable(p.Variables.V, secret.Key) {
				if err := q.CreateSecret(ctx, CreateSecretParams{ProjectID: p.ID, Key: secret.Key, Value: secret.Value}); err != nil {
					return err
				}
			}
		}
		choices, err := q.ProjectVolumeChoices(ctx, from.ID)
		if err != nil {
			return err
		}
		for _, v := range choices {
			if slicesHasVolume(p.Volumes.V, v.Name) {
				if err := q.CreateVolumeChoice(ctx, CreateVolumeChoiceParams{ProjectID: p.ID, Name: v.Name, LocationID: v.LocationID}); err != nil {
					return err
				}
			}
		}
		deploy, err := q.CreateCopyDeploy(ctx, CreateCopyDeployParams{ProjectID: p.ID, Sha: sha, Ref: "refs/heads/" + from.Branch, HeartbeatAt: now,
			SyncPayload: sql.NullString{String: string(raw), Valid: true}})
		if err != nil {
			return err
		}
		copied, err = q.CreateCopy(ctx, CreateCopyParams{ProjectID: sql.NullInt64{Int64: p.ID, Valid: true}, FromProjectID: sql.NullInt64{Int64: from.ID, Valid: true},
			DeployID: sql.NullInt64{Int64: deploy.ID, Valid: true}, FromName: from.Name, ToName: to, Sha: sha, RequestedBy: by})
		return err
	})
	if db.IsUnique(err) {
		return refuse("%s is already being copied", from.Name)
	}
	return copied, err
}

func slicesHasVariable(vars []Variable, name string) bool {
	for _, v := range vars {
		if v.Name == name {
			return true
		}
	}
	return false
}

func slicesHasVolume(vols []Volume, name string) bool {
	for _, v := range vols {
		if v.Name == name {
			return true
		}
	}
	return false
}

// SettleCopy brings a copy's deploy's copy along, in q's transaction: running once
// claimed; GO; or NO-GO, when the new project goes again (it's the
// caller's to queue that clean-up: cleanUp) unless it already took hosts
// over (then it's serving them, and stays).
func SettleCopy(ctx context.Context, q *Queries, d Deploy, now time.Time) (cleanUp int64, err error) {
	if d.Kind != "copy" {
		return 0, nil
	}
	c, err := q.CopyByDeploy(ctx, sql.NullInt64{Int64: d.ID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	switch d.Status {
	case "in_flight":
		return 0, q.SettleCopy(ctx, SettleCopyParams{Status: "running", Error: c.Error, Log: c.Log, Now: now, ID: c.ID})
	case "go":
		return 0, q.SettleCopy(ctx, SettleCopyParams{Status: "go", Error: c.Error, Log: c.Log, Now: now, ID: c.ID})
	case "no_go":
		if c.HandedOverAt.Valid {
			why := d.Error
			if why == "" {
				why = "the copy's deploy failed"
			}
			return 0, q.SettleCopy(ctx, SettleCopyParams{Status: "no_go", Error: fmt.Sprintf("%s; it serves the hosts it took, so %s is kept", why, c.ToName),
				Log: c.Log, Now: now, ID: c.ID})
		}
		// The new project goes, and its deploys with it: the log stays here.
		log, err := q.DeployLog(ctx, d.ID)
		if err != nil {
			return 0, err
		}
		return c.ID, q.SettleCopy(ctx, SettleCopyParams{Status: "no_go", Error: d.Error, Log: log, Now: now, ID: c.ID})
	}
	return 0, nil
}

// CancelCopy ends a copy that hasn't taken hosts over: its deploy NO-GO
// (and so the copy, and its new project goes).
func CancelCopy(ctx context.Context, tx *db.Tx, c ProjectCopy, by string, now time.Time) (cleanUp int64, err error) {
	q := New(tx)
	if c.Status != "queued" && c.Status != "running" {
		return 0, Refused{"the copy to " + c.ToName + " is done"}
	}
	if c.HandedOverAt.Valid {
		return 0, Refused{c.ToName + " has taken over " + c.FromName + "'s hosts; undo the copy instead"}
	}
	if err := q.CancelDeploy(ctx, CancelDeployParams{Error: "cancelled by " + by, Now: sql.NullTime{Time: now, Valid: true}, ID: c.DeployID.Int64}); err != nil {
		return 0, err
	}
	d, err := q.DeployByID(ctx, c.DeployID.Int64)
	if err != nil {
		return 0, err
	}
	return SettleCopy(ctx, q, d, now)
}

// WebhookTarget is the project a webhook at hooks.<base>/<name> rings: the
// one of that name, else the copy of a deleted one, so the git host's
// webhook keeps working after the old project is gone.
func WebhookTarget(ctx context.Context, q *Queries, name string) (Project, error) {
	p, err := q.ProjectByName(ctx, name)
	if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	id, err := q.CopyWebhookTarget(ctx, name)
	if err != nil {
		return Project{}, err
	}
	return q.ProjectByID(ctx, id.Int64)
}

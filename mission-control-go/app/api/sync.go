package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// syncBody is a sync's limit: room for a 512 KB maintenance page after JSON
// escapes its < > & to six bytes each, plus the rest.
const syncBody = 4 << 20

// syncAnswer is what a sync that goes on answers.
type syncAnswer struct {
	Project    string                        `json:"project"`
	Host       string                        `json:"host"`
	DNS        string                        `json:"dns"`
	Domains    map[string]models.DomainState `json:"domains"`
	Generation int64                         `json:"generation"`
}

// Sync is POST /api/projects/sync: houston deploy sends what it read from
// compose.yml. The project is saved first, so the admin can fill in
// missing secrets even when the answer is HOLD; DNS is pointed on every
// sync, so a retry after a Cloudflare failure finishes the job.
//
// A restore's sync (restore_deploy, with its token in
// X-Houston-Deploy-Token) only checks: its snapshot's compose.yml is valid
// and its required secrets have values. It's kept with the restore, which
// applies it at its switch; nothing is stored on the project now. Either
// way, serving_generation catches the project up after a restore that
// switched but never said so.
func (c Controller) Sync(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, syncBody)
	if err != nil {
		return err
	}
	s, errs := models.ParseSync(body)
	if errs != nil {
		return web.JSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "compose.yml doesn't match what Houston expects", "errors": errs})
	}
	ctx, now := r.Context(), time.Now()
	token := r.Header.Get("X-Houston-Deploy-Token")
	if s.RestoreDeploy != nil {
		return c.check(w, r, s, body, token)
	}
	q := models.New(c.DB.Read)
	if s.ClaimedDeploy != nil {
		if err := c.claimMatches(ctx, s, token); err != nil {
			return err
		}
	}
	existing, err := q.ProjectByName(ctx, s.Name)
	switch {
	case err == nil:
		if deleting, err := models.Deleting(ctx, q, existing.ID); err != nil {
			return err
		} else if deleting {
			return web.Status(http.StatusConflict, fmt.Errorf("%s is being deleted", existing.Name))
		}
		// A restore owns the project's config until it's done: its safety
		// snapshot and its cleanup read it. One gone silent doesn't: a hand
		// houston deploy (which syncs first) is how it's taken over.
		restoring, err := q.RestoreHolding(ctx, models.RestoreHoldingParams{ProjectID: existing.ID, Since: now.Add(-models.StaleAfter)})
		if err == nil {
			return web.Status(http.StatusConflict, fmt.Errorf("restore #%d is %s; wait for it", restoring.Number, restoring.StatusWords()))
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, adoptErr, err := models.CatchUp(ctx, c.DB, existing, s.ServingGeneration, s.ServingSHA, now); err != nil {
			return err
		} else if adoptErr != nil {
			c.Log.Warn("a restore's compose.yml wasn't applied as its project caught up", "project", s.Name, "err", adoptErr)
		}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	var project models.Project
	var dropped []string
	err = c.DB.Tx(ctx, func(tx *db.Tx) error {
		project, dropped, err = s.Save(ctx, tx, now)
		return err
	})
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	inst, err := q.CurrentInstallation(ctx)
	if err != nil {
		return err
	}
	dns, err := c.pointDNS(ctx, inst, project)
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if answered, err := answerBadGateway(w, err); answered {
		return err
	}
	if err != nil {
		return err
	}
	domains, err := c.pointDomains(ctx, inst, project, dropped, now)
	if err != nil {
		return err
	}
	c.pushMaintenanceRoutes(ctx, inst, project)
	c.Live.Refresh(FlightBoard, "") // saved, held or not
	if missing, err := c.missingSecrets(ctx, project.ID, s.Variables); err != nil || len(missing) > 0 {
		return hold(w, missing, err)
	}
	// Only a sync that goes on to deploy makes volumes: until then (a HOLD,
	// say) where they live can still be chosen.
	if err := c.placement().Place(ctx, project, project.DataGeneration, project.Volumes.V); errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	} else if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, syncAnswer{Project: project.Name, Host: project.Host(inst.BaseDomain), DNS: dns, Domains: domains, Generation: project.DataGeneration})
}

// claimMatches checks a runner's sync against the deploy it claimed: the
// repo's compose.yml must name that deploy's project, so one repo can't
// sync as another project.
func (c Controller) claimMatches(ctx context.Context, s models.Sync, token string) error {
	q := models.New(c.DB.Read)
	claimed, err := q.DeployByID(ctx, *s.ClaimedDeploy)
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusUnprocessableEntity, errors.New("no such deploy"))
	}
	if err != nil {
		return err
	}
	if !claimed.OwnedBy(token) {
		return web.Status(http.StatusForbidden, errors.New("that token isn't this deploy's"))
	}
	project, err := q.ProjectByID(ctx, claimed.ProjectID)
	if err != nil {
		return err
	}
	if project.Name != s.Name {
		return web.Status(http.StatusUnprocessableEntity, fmt.Errorf("compose.yml names project %q, but deploy #%d is for %s; nothing was synced", s.Name, claimed.Number, project.Name))
	}
	return nil
}

// check is a restore's sync: its compose.yml kept with it, the project
// left as it is.
func (c Controller) check(w http.ResponseWriter, r *http.Request, s models.Sync, body Body, token string) error {
	ctx, now := r.Context(), time.Now()
	q := models.New(c.DB.Read)
	restore, err := q.DeployByID(ctx, *s.RestoreDeploy)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var project models.Project
	if err == nil {
		if project, err = q.ProjectByID(ctx, restore.ProjectID); err != nil {
			return err
		}
	}
	if !restore.Restore() || project.Name != s.Name {
		return web.Status(http.StatusUnprocessableEntity, errors.New("no such restore"))
	}
	if !restore.OwnedBy(token) {
		return web.Status(http.StatusForbidden, errors.New("that token isn't this restore's"))
	}
	inst, err := q.CurrentInstallation(ctx)
	if err != nil {
		return err
	}
	adopted, adoptErr, err := models.CatchUp(ctx, c.DB, project, s.ServingGeneration, s.ServingSHA, now)
	if err != nil {
		return err
	}
	if adoptErr != nil {
		c.Log.Warn("a restore's compose.yml wasn't applied as its project caught up", "project", s.Name, "err", adoptErr)
	}
	c.pointBestEffort(ctx, inst, adopted, now)
	for _, k := range []string{"restore_deploy", "serving_generation", "serving_sha"} {
		delete(body, k)
	}
	kept, err := json.Marshal(body)
	if err != nil {
		return err
	}
	// Ownership was checked above; "still in flight" is checked in the write.
	n, err := models.New(c.DB.Write).KeepRestoreSync(ctx, models.KeepRestoreSyncParams{SyncPayload: sql.NullString{String: string(kept), Valid: true}, ID: restore.ID})
	if err != nil {
		return err
	}
	if n != 1 {
		return web.Status(http.StatusConflict, fmt.Errorf("restore #%d is no longer in flight", restore.Number))
	}
	if missing, err := c.missingSecrets(ctx, project.ID, s.Variables); err != nil || len(missing) > 0 {
		return hold(w, missing, err)
	}
	if project, err = q.ProjectByID(ctx, project.ID); err != nil {
		return err
	}
	dns := "wildcard"
	if inst.DnsMode == "per_host" {
		dns = "per_host"
	}
	return web.JSON(w, http.StatusOK, syncAnswer{Project: project.Name, Host: project.Host(inst.BaseDomain), DNS: dns,
		Domains: states(project.DomainStates.V), Generation: project.DataGeneration})
}

func (c Controller) missingSecrets(ctx context.Context, projectID int64, vars []models.Variable) ([]string, error) {
	values, err := models.Secrets(ctx, models.New(c.DB.Read), projectID)
	if err != nil {
		return nil, err
	}
	return models.MissingSecrets(vars, values), nil
}

// hold answers HOLD: required secrets without a value.
func hold(w http.ResponseWriter, missing []string, err error) error {
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error": fmt.Sprintf("HOLD: set %s in Mission Control first", web.Sentence(missing)), "missing": missing})
}

// states is never nil: {} in JSON.
func states(s map[string]models.DomainState) map[string]models.DomainState {
	if s == nil {
		return map[string]models.DomainState{}
	}
	return s
}

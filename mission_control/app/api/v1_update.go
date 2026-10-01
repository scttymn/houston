package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/port"
	"github.com/scttymn/houston/mission_control/app/services/serverupdate"
)

// GitHubDown is what a release check says when GitHub didn't answer.
const GitHubDown = "Couldn't reach GitHub just now; try again in a minute."

// FollowEvery is how often a running update is followed, so the board can
// show each step.
const FollowEvery = 5 * time.Second

// updateView is the version this runs, a newer release if one is out, and
// the last update started from Mission Control.
func (c V1) updateView(ctx context.Context) (map[string]any, error) {
	q := models.New(c.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil {
		return nil, err
	}
	var latest *string
	if newer := models.NewerRelease(c.Version, inst.LatestRelease); newer != "" {
		latest = &newer
	}
	view := map[string]any{"version": c.Version, "latest": latest, "update": nil}
	last, err := q.LastUpdate(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	view["update"] = map[string]any{"id": last.ID, "to": last.ToVersion, "from": last.FromVersion, "status": last.Status, "step": orNull(last.Step),
		"started_at": last.StartedAt, "finished_at": timeOrNull(last.FinishedAt), "log": orNull(last.Log)}
	return view, nil
}

// Update is GET /api/v1/update (houston update --status).
func (c V1) Update(w http.ResponseWriter, r *http.Request) error {
	view, err := c.updateView(r.Context())
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, view)
}

// StartUpdate is POST /api/v1/update {version}: the server updated to a
// release, the latest known by default (202).
func (c V1) StartUpdate(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	version, isString := field(body, "version")
	if raw := body["version"]; len(raw) > 0 && string(raw) != "null" && !isString {
		return web.Status(http.StatusBadRequest, errors.New("version must be a release tag, like v0.4.3"))
	}
	ctx := r.Context()
	update, err := c.Updater.Start(ctx, version, time.Now())
	var refused serverupdate.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, errors.New("Houston "+string(refused)))
	}
	if err != nil {
		return err
	}
	if _, err := c.FollowUpdate.EnqueueIn(ctx, models.UpdateArgs{Follow: true}, FollowEvery); err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	view, err := c.updateView(ctx)
	if err != nil {
		return err
	}
	view["message"] = "Updating to " + update.ToVersion + ". Mission Control restarts on the way; deploys and backups wait."
	return web.JSON(w, http.StatusAccepted, view)
}

// CheckRelease is POST /api/v1/update/check: GitHub asked for the latest
// release now.
func (c V1) CheckRelease(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	tag, changed, err := c.Release.Check(ctx, c.DB, time.Now())
	if err != nil {
		c.Log.Warn("couldn't check the latest release", "err", err)
		return badGatewayAnswer(w, GitHubDown)
	}
	if changed {
		c.Live.Refresh(FlightBoard, "")
	}
	view, err := c.updateView(ctx)
	if err != nil {
		return err
	}
	view["message"] = tag + " is the latest."
	if newer := models.NewerRelease(c.Version, tag); newer != "" {
		view["message"] = newer + " is out."
	}
	return web.JSON(w, http.StatusOK, view)
}

// portView is what port 3000 is bound to now (null when unknown), and the
// choice the installer keeps.
func (c V1) portView(ctx context.Context) (map[string]any, error) {
	inst, err := models.New(c.DB.Read).CurrentInstallation(ctx)
	if err != nil {
		return nil, err
	}
	saved := "closed"
	if inst.PortOpen {
		saved = "open"
	}
	view := map[string]any{"open": nil, "address": nil, "saved": saved}
	if address := c.Port.Address(ctx); address != "" {
		view["open"], view["address"] = address != "127.0.0.1", address
	}
	return view, nil
}

// ShowPort is GET /api/v1/port (houston port).
func (c V1) ShowPort(w http.ResponseWriter, r *http.Request) error {
	view, err := c.portView(r.Context())
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, view)
}

// SetPort is PATCH or PUT /api/v1/port {open}: port 3000 opened to the
// network or closed (202); Mission Control restarts.
func (c V1) SetPort(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	raw := string(body["open"])
	if raw != "true" && raw != "false" {
		return web.Status(http.StatusBadRequest, errors.New("open must be true or false"))
	}
	open := raw == "true"
	ctx := r.Context()
	err = c.Port.Set(ctx, open)
	var refused port.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, errors.New("Houston "+string(refused)))
	}
	if err != nil {
		return err
	}
	view, err := c.portView(ctx)
	if err != nil {
		return err
	}
	view["message"] = "Port 3000 is closing to the network. Mission Control restarts for a few seconds."
	if open {
		view["message"] = "Port 3000 is opening to the network. Mission Control restarts for a few seconds."
	}
	return web.JSON(w, http.StatusAccepted, view)
}

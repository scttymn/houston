package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission_control/app/deploys"
	"github.com/scttymn/houston/mission_control/app/models"
)

var projectName = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Report is PATCH /api/deploys/{id} with X-Houston-Deploy-Token: progress,
// log, result. Ownership and "still in flight" are checked in the
// transaction that writes.
func (c Controller) Report(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, models.ChunkCap+4<<10)
	if err != nil {
		return err
	}
	var log string
	if json.Unmarshal(body["log"], &log) == nil && len(log) > models.ChunkCap {
		return web.Status(http.StatusRequestEntityTooLarge, fmt.Errorf("a log chunk can be at most %d KiB", models.ChunkCap>>10))
	}
	progress, invalid := parseProgress(body)
	if invalid != "" {
		return web.Status(http.StatusUnprocessableEntity, errors.New(invalid))
	}
	ctx, now := r.Context(), time.Now()
	var reported models.Reported
	err = c.DB.Tx(ctx, func(tx *db.Tx) error {
		d, err := models.New(tx).DeployByID(ctx, web.ID(r, "id"))
		switch {
		case err != nil:
			return web.Status(http.StatusNotFound, errors.New("no such deploy"))
		case !d.OwnedBy(r.Header.Get("X-Houston-Deploy-Token")):
			return web.Status(http.StatusForbidden, errors.New("that token isn't this deploy's"))
		case !d.InFlight():
			why := d.Error
			if why == "" {
				why = d.Status
			}
			return web.Status(http.StatusConflict, fmt.Errorf("deploy #%d is no longer in flight (%s); it was finished or taken over", d.Number, why))
		}
		reported, err = models.Report(ctx, tx, d, progress, now)
		return err
	})
	if err != nil {
		return err
	}
	d := reported.Deploy
	changed := progress.Step != nil || progress.Status != nil || progress.Error != nil || progress.ProposedName != nil
	if err := deploys.Progress(ctx, c.Live, c.DB, d, reported.Appended, changed); err != nil {
		c.Log.Warn("the deploy's page wasn't told", "deploy", d.ID, "err", err)
	}
	if progress.Step != nil || progress.Status != nil {
		c.Live.Refresh(FlightBoard, "")
	}
	c.afterReport(ctx, reported, now)
	if reported.CleanUp != 0 {
		if _, err := c.CopyCleanUp.Enqueue(ctx, models.CopyArgs{ID: reported.CleanUp}); err != nil {
			return err
		}
	}
	return web.JSON(w, http.StatusOK, map[string]any{"number": d.Number, "status": d.Status})
}

// afterReport points names once the report is committed: a restore's
// adopted compose.yml's, and a project's at its first GO (the new version
// passed its health check, so it can take the traffic). Best effort: the
// next deploy's sync points them.
func (c Controller) afterReport(ctx context.Context, reported models.Reported, now time.Time) {
	q := models.New(c.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil {
		c.Log.Warn("no installation to point DNS with", "err", err)
		return
	}
	c.pointBestEffort(ctx, inst, reported.Adopted, now)
	d := reported.Deploy
	if d.Status != "go" {
		return
	}
	first, err := q.FirstGo(ctx, models.FirstGoParams{ProjectID: d.ProjectID, ID: d.ID})
	if err != nil || !first {
		return
	}
	project, err := q.ProjectByID(ctx, d.ProjectID)
	if err == nil {
		c.pointBestEffort(ctx, inst, &models.Adopted{Project: project}, now)
	}
}

// parseProgress is a report's fields, or why they're refused, in words.
func parseProgress(body Body) (models.Progress, string) {
	var p models.Progress
	text := func(key string) (*string, bool, bool) { // value, sent, a string
		raw, sent := body[key]
		if !sent {
			return nil, false, false
		}
		var s string
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
			return nil, true, false
		}
		return &s, true, true
	}
	status, statusSent, _ := text("status")
	if statusSent && (status == nil || *status != "go" && *status != "no_go" && *status != "hold") {
		return p, "status must be go, no_go or hold"
	}
	name, nameSent, nameText := text("proposed_name")
	if statusSent && *status == "hold" || nameSent {
		if !(statusSent && *status == "hold" && nameText) {
			return p, "hold comes with proposed_name, and proposed_name only with hold"
		}
		if !projectName.MatchString(*name) {
			return p, "proposed_name must be a project name (a DNS label)"
		}
	}
	step, stepSent, stepText := text("step")
	if stepSent && !(stepText && utf8.RuneCountInString(*step) <= 100) {
		return p, "step must be at most 100 characters"
	}
	msg, errSent, errText := text("error")
	if errSent && !(errText && utf8.RuneCountInString(*msg) <= 1000) {
		return p, "error must be at most 1000 characters"
	}
	log, logSent, logText := text("log")
	if logSent && !logText {
		return p, "log must be text"
	}
	return models.Progress{Step: step, Log: log, Status: status, Error: msg, ProposedName: name}, ""
}

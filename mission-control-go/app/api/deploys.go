package api

import (
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

// FlightBoard is the flight board's live stream: a refresh when a deploy's
// commit, step or status changes.
const FlightBoard = "flight_board"

// StartDeploy is POST /api/projects/{name}/deploys {sha, ref}: houston
// deploy starts the project's next deploy, and holds its token.
func (c Controller) StartDeploy(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, MaxBody)
	if err != nil {
		return err
	}
	name := r.PathValue("name")
	ctx := r.Context()
	project, err := models.New(c.DB.Read).ProjectByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusNotFound, fmt.Errorf("no project %s; houston deploy syncs it first", name))
	}
	if err != nil {
		return err
	}
	var sha, ref string
	json.Unmarshal(body["sha"], &sha) // not a string: "", which the rules refuse
	json.Unmarshal(body["ref"], &ref)
	var started models.Started
	err = c.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		started, err = models.StartDeploy(ctx, tx, project, sha, ref, time.Now())
		return err
	})
	var busy models.Busy
	var invalid models.Invalid
	switch {
	case errors.As(err, &busy):
		return web.JSON(w, http.StatusConflict, map[string]any{"error": busy.Error(), "number": busy.Deploy.Number})
	case errors.Is(err, models.ErrRegistryBusy):
		return web.Status(http.StatusConflict, err)
	case db.IsUnique(err):
		return web.Status(http.StatusConflict, fmt.Errorf("another deploy of %s just started", project.Name))
	case errors.As(err, &invalid):
		return web.Status(http.StatusUnprocessableEntity, invalid)
	case errors.As(err, new(models.Refused)): // being deleted (Rails lets this through as a 500)
		return web.Status(http.StatusConflict, err)
	case err != nil:
		return err
	}
	c.cleanUpCopies(ctx, started.CleanUp)
	c.Live.Refresh(FlightBoard, "")
	var tookOver *int64
	if started.TookOver > 0 {
		tookOver = &started.TookOver
	}
	return web.JSON(w, http.StatusCreated, map[string]any{"id": started.Deploy.ID, "number": started.Deploy.Number, "token": started.Token, "took_over": tookOver})
}

package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

type deletionView struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	Status           string     `json:"status"`
	Step             *string    `json:"step"`
	Error            *string    `json:"error"`
	DeleteBackups    bool       `json:"delete_backups"`
	SnapshotID       *string    `json:"snapshot_id"`
	SnapshotLocation *string    `json:"snapshot_location"`
	RepoURL          *string    `json:"repo_url"`
	By               string     `json:"by"`
	QueuedAt         time.Time  `json:"queued_at"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	Log              *string    `json:"log,omitempty"`
}

// viewDeletion is a deletion as the API shows it: with its log unless
// it's inside a project's view.
func (c Controller) viewDeletion(ctx context.Context, d models.ProjectDeletion, withLog bool) deletionView {
	v := deletionView{ID: d.ID, Name: d.Name, Status: d.Status, Step: orNull(d.Step), Error: orNull(d.Error), DeleteBackups: d.DeleteBackups,
		SnapshotID: orNull(d.SnapshotID), RepoURL: orNull(d.RepoUrl), By: d.RequestedBy, QueuedAt: d.CreatedAt,
		StartedAt: timeOrNull(d.StartedAt), FinishedAt: timeOrNull(d.FinishedAt)}
	if d.SnapshotLocationID.Valid {
		if l, err := models.New(c.DB.Read).StorageLocationByID(ctx, d.SnapshotLocationID.Int64); err == nil {
			v.SnapshotLocation = &l.Name
		}
	}
	if withLog {
		v.Log = &d.Log
	}
	return v
}

// DeleteProject is DELETE /api/v1/projects/{name} {confirm,
// delete_backups}: the project's deletion queued (202), or one that
// stopped partway resumed.
func (c V1) DeleteProject(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	p, err := c.project(r)
	if err != nil {
		return err
	}
	confirm, _ := field(body, "confirm")
	token, _ := web.Get(r, tokenKey)
	ctx := r.Context()
	var d models.ProjectDeletion
	err = c.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		d, err = models.RequestDeletion(ctx, tx, p, confirm, isTrue(body, "delete_backups"), "token "+token.Name, time.Now())
		return err
	})
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	if _, err := c.Delete.Enqueue(ctx, models.DeletionArgs{ID: d.ID}); err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	return web.JSON(w, http.StatusAccepted, map[string]any{"deletion": c.viewDeletion(ctx, d, true)})
}

// Deletion is GET /api/v1/deletions/{id}: where one stands, with its log.
func (c V1) Deletion(w http.ResponseWriter, r *http.Request) error {
	d, err := models.New(c.DB.Read).DeletionByID(r.Context(), web.ID(r, "id"))
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusNotFound, errors.New("no deletion "+r.PathValue("id")))
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, c.viewDeletion(r.Context(), d, true))
}

package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// backupView is a run as the API shows it (the Rails app's
// RemoteView.backup): a stale one as the NO-GO it will be recorded as.
type backupView struct {
	ID         int64      `json:"id"`
	Status     string     `json:"status"`
	Kind       string     `json:"kind"`
	Reason     string     `json:"reason"`
	Deploy     *int64     `json:"deploy"`
	Sha        *string    `json:"sha"`
	SnapshotID *string    `json:"snapshot_id"`
	Bytes      *int64     `json:"bytes"`
	Error      *string    `json:"error"`
	QueuedAt   time.Time  `json:"queued_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func viewBackup(r models.BackupRun, now time.Time) backupView {
	v := backupView{ID: r.ID, Status: r.Status, Kind: r.Kind, Reason: r.Reason, Sha: orNull(r.Sha), SnapshotID: orNull(r.SnapshotID),
		Error: orNull(r.Error), QueuedAt: r.CreatedAt}
	if r.Stale(now) {
		v.Status, v.Error = "no_go", orNull(r.StaleError())
	}
	if r.DeployNumber.Valid {
		v.Deploy = &r.DeployNumber.Int64
	}
	if r.Bytes.Valid {
		v.Bytes = &r.Bytes.Int64
	}
	if r.StartedAt.Valid {
		v.StartedAt = &r.StartedAt.Time
	}
	if r.FinishedAt.Valid {
		v.FinishedAt = &r.FinishedAt.Time
	}
	return v
}

// ownDeploy is the deploy the request names, if the caller holds its token
// and it's in flight (the Rails app's set_deploy). what names it in the
// 409: "deploy", "restore".
func (c Controller) ownDeploy(r *http.Request, what string, check func(models.Deploy) error) (models.Deploy, error) {
	d, err := models.New(c.DB.Read).DeployByID(r.Context(), web.ID(r, "id"))
	switch {
	case err != nil:
		return d, web.Status(http.StatusNotFound, errors.New("no such deploy"))
	case !d.OwnedBy(r.Header.Get("X-Houston-Deploy-Token")):
		return d, web.Status(http.StatusForbidden, errors.New("that token isn't this deploy's"))
	}
	if check != nil {
		if err := check(d); err != nil {
			return d, err
		}
	}
	if !d.InFlight() {
		return d, web.Status(http.StatusConflict, fmt.Errorf("%s #%d is no longer in flight", what, d.Number))
	}
	return d, nil
}

// RequestSnapshot is POST /api/deploys/{id}/snapshot: the pre-deploy
// snapshot, asked for by the deploy that owns it while it's in flight
// (202). A restore's is its safety snapshot, taken just before the switch.
// With nothing deployed yet there's nothing to hold: 200 skipped.
func (c Controller) RequestSnapshot(w http.ResponseWriter, r *http.Request) error {
	d, err := c.ownDeploy(r, "deploy", nil)
	if err != nil {
		return err
	}
	ctx, now := r.Context(), time.Now()
	project, err := models.New(c.DB.Read).ProjectByID(ctx, d.ProjectID)
	if err != nil {
		return err
	}
	var run models.BackupRun
	err = c.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		run, err = models.RequestSnapshot(ctx, tx, c.Snapshot, project, d, now)
		return err
	})
	var refused models.Refused
	if errors.As(err, &refused) {
		if refused.Msg == models.NothingDeployed {
			return web.JSON(w, http.StatusOK, map[string]string{"status": "skipped", "error": refused.Msg})
		}
		// 409 means "no longer in flight" to the runner (it stops, taken over).
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, viewBackup(run, now))
}

// ShowSnapshot is GET /api/deploys/{id}/snapshot: where it stands.
func (c Controller) ShowSnapshot(w http.ResponseWriter, r *http.Request) error {
	d, err := c.ownDeploy(r, "deploy", nil)
	if err != nil {
		return err
	}
	reason := "deploy"
	if d.Restore() {
		reason = "restore"
	}
	run, err := models.New(c.DB.Read).DeploySnapshot(r.Context(), models.DeploySnapshotParams{ProjectID: d.ProjectID, Reason: reason,
		DeployNumber: sqlNumber(d.Number)})
	if err != nil {
		return web.Status(http.StatusNotFound, fmt.Errorf("deploy #%d has no snapshot", d.Number))
	}
	return web.JSON(w, http.StatusOK, viewBackup(run, time.Now()))
}

// isRestore refuses a deploy that isn't a restore.
func isRestore(d models.Deploy) error {
	if !d.Restore() {
		return web.Status(http.StatusUnprocessableEntity, fmt.Errorf("deploy #%d isn't a restore", d.Number))
	}
	return nil
}

// RequestRestoreData is POST /api/deploys/{id}/restore_data: a restore's
// data, put back into the generation it builds. One per restore: a retried
// POST gets the same run.
func (c Controller) RequestRestoreData(w http.ResponseWriter, r *http.Request) error {
	d, err := c.ownDeploy(r, "restore", isRestore)
	if err != nil {
		return err
	}
	ctx, now := r.Context(), time.Now()
	var run models.BackupRun
	err = c.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		run, err = models.RequestRestoreData(ctx, tx, c.Snapshot, d, now)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, viewBackup(run, now))
}

// ShowRestoreData is GET /api/deploys/{id}/restore_data: where it stands.
func (c Controller) ShowRestoreData(w http.ResponseWriter, r *http.Request) error {
	d, err := c.ownDeploy(r, "restore", isRestore)
	if err != nil {
		return err
	}
	run, err := models.New(c.DB.Read).RestoreRun(r.Context(), models.RestoreRunParams{ProjectID: d.ProjectID, DeployNumber: sqlNumber(d.Number)})
	if err != nil {
		return web.Status(http.StatusNotFound, fmt.Errorf("restore #%d hasn't asked for its data", d.Number))
	}
	return web.JSON(w, http.StatusOK, viewBackup(run, time.Now()))
}

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
)

type snapshotView struct {
	ID      string  `json:"id"`
	ShortID string  `json:"short_id"`
	Time    string  `json:"time"`
	Kind    *string `json:"kind"`
	Reason  *string `json:"reason"`
	Deploy  *int64  `json:"deploy"`
	Sha     *string `json:"sha"`
	Bytes   *int64  `json:"bytes"`
}

func viewSnapshot(s backup.Snapshot) snapshotView {
	return snapshotView{ID: s.ID, ShortID: s.ShortID, Time: s.Time.UTC().Format(time.RFC3339), Kind: orNull(s.Kind), Reason: orNull(s.Reason),
		Deploy: s.Deploy, Sha: orNull(s.Sha), Bytes: s.Bytes}
}

// noStorage is a project with nowhere to back up to yet.
var noStorage = web.Status(http.StatusConflict, errors.New("no backup storage yet (finish setup's storage step)"))

// Snapshots is GET /api/v1/projects/{name}/snapshots: its snapshots in its
// backup location, newest first, from restic.
func (c V1) Snapshots(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	l, err := models.New(c.DB.Read).BackupLocationFor(r.Context(), p.BackupLocationID.Int64)
	if err != nil {
		return noStorage
	}
	list, err := c.SnapshotList.For(r.Context(), p.Name, l)
	var unavailable backup.Unavailable
	if errors.As(err, &unavailable) {
		return badGatewayAnswer(w, "can't read snapshots: "+unavailable.Error())
	}
	if err != nil {
		return err
	}
	views := []snapshotView{}
	for _, s := range list {
		views = append(views, viewSnapshot(s))
	}
	return web.JSON(w, http.StatusOK, map[string]any{"snapshots": views})
}

// DownloadSnapshot is GET .../snapshots/{id}/download?location=: everything
// in one snapshot as a zip, streamed from restic (houston snapshots
// download). The location is the project's backup location unless named.
// Every refusal comes before a byte.
func (c V1) DownloadSnapshot(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	q := models.New(c.DB.Read)
	location := r.URL.Query().Get("location")
	if location == "" {
		l, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64)
		if err != nil {
			return noStorage
		}
		location = l.Name
	}
	token, _ := web.Get(r, tokenKey)
	export, err := backup.OpenExport(ctx, q, c.DockerCLI, c.SnapshotList, c.Log, p, location, r.PathValue("id"), "token "+token.Name)
	var (
		notFound backup.NotFound
		busy     backup.Busy
		failed   backup.Failed
	)
	switch {
	case errors.As(err, &notFound):
		return web.Status(http.StatusNotFound, notFound)
	case errors.As(err, &busy):
		return web.Status(http.StatusConflict, busy)
	case errors.As(err, &failed):
		return badGatewayAnswer(w, failed.Error())
	case err != nil:
		return err
	}
	export.Headers(w.Header())
	w.WriteHeader(http.StatusOK)
	export.Send(ctx, w) // what broke after the first byte is logged: the answer has begun
	return nil
}

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/internal/mission"
	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
	"github.com/scttymn/houston/mission-control-go/app/services/handover"
)

// copyView is a copy as the API shows it (the Rails app's RemoteView.copy).
type copyView struct {
	ID           int64      `json:"id"`
	From         string     `json:"from"`
	To           string     `json:"to"`
	Status       string     `json:"status"`
	Deploy       *int64     `json:"deploy"`
	Sha          string     `json:"sha"`
	By           string     `json:"by"`
	Error        *string    `json:"error"`
	HandedOver   []string   `json:"handed_over"`
	HandedOverAt *time.Time `json:"handed_over_at"`
	UndoneAt     *time.Time `json:"undone_at"`
}

func (c Controller) viewCopy(ctx context.Context, cp models.ProjectCopy) copyView {
	v := copyView{ID: cp.ID, From: cp.FromName, To: cp.ToName, Status: cp.Status, Sha: cp.Sha, By: cp.RequestedBy, Error: orNull(cp.Error),
		HandedOver: cp.HandedOver.V, HandedOverAt: timeOrNull(cp.HandedOverAt), UndoneAt: timeOrNull(cp.UndoneAt)}
	if v.HandedOver == nil {
		v.HandedOver = []string{}
	}
	if cp.DeployID.Valid {
		if d, err := models.New(c.DB.Read).DeployByID(ctx, cp.DeployID.Int64); err == nil {
			v.Deploy = &d.Number
		}
	}
	return v
}

// ownCopy is the copy deploy the request names, and its copy, if the
// caller holds its token and it's in flight.
func (c Controller) ownCopy(r *http.Request) (models.Deploy, models.ProjectCopy, error) {
	var cp models.ProjectCopy
	d, err := c.ownDeploy(r, "copy", func(d models.Deploy) error {
		var err error
		if d.Kind == "copy" {
			cp, err = models.New(c.DB.Read).CopyByDeploy(r.Context(), sqlNumber(d.ID))
		}
		if d.Kind != "copy" || err != nil {
			return web.Status(http.StatusUnprocessableEntity, fmt.Errorf("deploy #%d isn't a copy", d.Number))
		}
		return nil
	})
	return d, cp, err
}

// dataView is where a copy's data stands: its snapshot, then its restore,
// as backups are shown; or without one, what it is.
func (c Controller) dataView(data models.CopyData, now time.Time) any {
	if data.Run == nil {
		return map[string]any{"id": 0, "status": data.Status, "error": orNull(data.Error)}
	}
	v := viewBackup(*data.Run, now)
	if data.Error != "" {
		v.Error = &data.Error
	}
	return v
}

// CreateCopyData is POST /api/deploys/{id}/copy_data: the old project's
// snapshot asked for (202), once; a retried POST gets the same run.
func (c Controller) CreateCopyData(w http.ResponseWriter, r *http.Request) error {
	d, _, err := c.ownCopy(r)
	if err != nil {
		return err
	}
	ctx, now := r.Context(), time.Now()
	var data models.CopyData
	err = c.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		data, err = models.StartCopyData(ctx, tx, c.Snapshot, d, now)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, c.dataView(data, now))
}

// ShowCopyData is GET /api/deploys/{id}/copy_data: where the data stands,
// the snapshot while it runs, then the restore (asked for once the
// snapshot is GO).
func (c Controller) ShowCopyData(w http.ResponseWriter, r *http.Request) error {
	d, _, err := c.ownCopy(r)
	if err != nil {
		return err
	}
	ctx, now := r.Context(), time.Now()
	var data models.CopyData
	err = c.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		data, err = models.CopyDataStatus(ctx, tx, c.Snapshot, d, now)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, c.dataView(data, now))
}

// handover is the hosts' handover for a copy.
func (c Controller) handover(inst models.Installation) handover.Handover {
	return handover.Handover{DB: c.DB, Docker: c.DockerCLI, Installation: inst, Cloudflare: c.Cloudflare}
}

// Handover is POST /api/deploys/{id}/handover: the hosts the old and new
// projects share, handed to the new one, then its domains pointed.
func (c Controller) Handover(w http.ResponseWriter, r *http.Request) error {
	d, cp, err := c.ownCopy(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	q := models.New(c.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil {
		return err
	}
	to, err := q.ProjectByID(ctx, d.ProjectID)
	if err != nil {
		return err
	}
	var moved []string
	err = handover.Locked(func() error {
		// A cancel may have ended the deploy since the check above.
		if d, err := models.New(c.DB.Read).DeployByID(ctx, d.ID); err != nil || !d.InFlight() {
			if err != nil {
				return err
			}
			return web.Status(http.StatusConflict, fmt.Errorf("copy #%d was cancelled", d.Number))
		}
		if cp, err = models.New(c.DB.Read).CopyByDeploy(ctx, sqlNumber(d.ID)); err != nil {
			return err
		}
		moved, err = c.handover(inst).Forward(ctx, cp, to)
		return err
	})
	var failed handover.Failed
	if errors.As(err, &failed) {
		return web.Status(http.StatusUnprocessableEntity, failed)
	}
	if err != nil {
		return err
	}
	c.pointCopyDomains(ctx, inst, to)
	return web.JSON(w, http.StatusOK, map[string][]string{"handed_over": moved})
}

// pointCopyDomains points the new project's domains, now its own. Best
// effort: its first GO points them as well.
func (c Controller) pointCopyDomains(ctx context.Context, inst models.Installation, p models.Project) {
	if !inst.CloudflareConnectedAt.Valid || inst.CloudflareApiToken.Reveal() == "" {
		return
	}
	d := c.dns(inst)
	states := map[string]models.DomainState{}
	for _, domain := range p.Domains.V {
		if domain != p.Host(inst.BaseDomain) {
			states[domain] = d.PointDomain(ctx, p, domain)
		}
	}
	err := models.New(c.DB.Write).SetDomainStates(ctx, models.SetDomainStatesParams{DomainStates: models.DomainStates{V: states}, UpdatedAt: time.Now(), ID: p.ID})
	if err != nil {
		c.Log.Warn("a copy's domains weren't pointed after its handover", "project", p.Name, "err", err)
	}
}

// readRepo reads a project's repo at its branch head.
func (c Controller) readRepo(ctx context.Context, p models.Project) (string, *mission.Inspection, string) {
	return c.Git.Read(ctx, gitremote.Link{RepoURL: p.RepoUrl, Branch: p.Branch, ComposePath: p.ComposePath, DeployKey: p.DeployKeyPrivate.Reveal()})
}

// by is who a personal API request is: "token <name>".
func by(r *http.Request) string {
	token, _ := web.Get(r, tokenKey)
	return "token " + token.Name
}

// RequestCopy is POST /api/v1/projects/{name}/copy {confirm}: the project
// copied to the new name its latest deploy proposes (202).
func (c V1) RequestCopy(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	from, err := c.project(r)
	if err != nil {
		return err
	}
	confirm, _ := field(body, "confirm")
	ctx := r.Context()
	cp, err := models.RequestCopy(ctx, c.DB, c.readRepo, from, confirm, by(r), time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	return web.JSON(w, http.StatusAccepted, map[string]any{"copy": c.viewCopy(ctx, cp)})
}

// copyOf is the copy that made the project the request names.
func (c V1) copyOf(r *http.Request) (models.Project, models.ProjectCopy, error) {
	p, err := c.project(r)
	if err != nil {
		return p, models.ProjectCopy{}, err
	}
	cp, err := models.New(c.DB.Read).CopyOf(r.Context(), sqlNumber(p.ID))
	if err != nil {
		return p, cp, web.Status(http.StatusNotFound, errors.New(p.Name+" isn't a copy"))
	}
	return p, cp, nil
}

// CancelCopy is POST /api/v1/projects/{name}/copy/cancel: a copy that
// hasn't taken hosts over ends, and its new project goes.
func (c V1) CancelCopy(w http.ResponseWriter, r *http.Request) error {
	_, cp, err := c.copyOf(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	cleanUp, err := handover.Cancel(ctx, c.DB, cp.ID, by(r), time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	if cleanUp != 0 {
		if _, err := c.CopyCleanUp.Enqueue(ctx, models.CopyArgs{ID: cleanUp}); err != nil {
			return err
		}
	}
	c.Live.Refresh(FlightBoard, "")
	if cp, err = models.New(c.DB.Read).CopyByID(ctx, cp.ID); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"copy": c.viewCopy(ctx, cp)})
}

// UndoCopy is POST /api/v1/projects/{name}/copy/undo {confirm}: after a
// GO copy, while the old project is there, the shared hosts go back to it,
// then the new project is deleted (its backups kept, as any delete).
func (c V1) UndoCopy(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	p, cp, err := c.copyOf(r)
	if err != nil {
		return err
	}
	confirm, _ := field(body, "confirm")
	ctx := r.Context()
	inst, err := models.New(c.DB.Read).CurrentInstallation(ctx)
	if err != nil {
		return err
	}
	deletion, err := c.handover(inst).Undo(ctx, p, cp, confirm, by(r), time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	if _, err := c.Delete.Enqueue(ctx, models.DeletionArgs{ID: deletion.ID}); err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	if cp, err = models.New(c.DB.Read).CopyByID(ctx, cp.ID); err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, map[string]any{"deletion": c.viewDeletion(ctx, deletion, false), "copy": c.viewCopy(ctx, cp)})
}

// cleanUpCopies queues the clean-up of copies whose deploy was abandoned.
// Best effort: a copy left behind is deleted by hand.
func (c Controller) cleanUpCopies(ctx context.Context, ids []int64) {
	for _, id := range ids {
		if _, err := c.CopyCleanUp.Enqueue(ctx, models.CopyArgs{ID: id}); err != nil {
			c.Log.Error("a copy's clean-up wasn't queued", "copy", id, "err", err)
		}
	}
}

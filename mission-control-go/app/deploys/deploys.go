package deploys

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"net/http"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/live"
	"github.com/scttymn/gantry/turbo"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/shared/layout"
)

// Stream is a deploy's page's live stream.
func Stream(id int64) string { return "deploy:" + strconv.FormatInt(id, 10) }

// Word is what a deploy is: Deploy, Rebuild, Restore, Copy.
func Word(d models.Deploy) string {
	switch {
	case d.Restore():
		return "Restore"
	case d.Kind == "copy":
		return "Copy"
	case d.Fresh:
		return "Rebuild"
	}
	return "Deploy"
}

// view is how times show: now, and when it started, in Houston's zone.
type view struct {
	now     time.Time
	started string
}

// Controller draws deploys' pages.
type Controller struct {
	DB   *db.DB
	Live *live.Hub
}

// Show is GET /projects/{name}/deploys/{number}.
func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := models.New(c.DB.Read)
	p, err := q.ProjectByName(ctx, r.PathValue("name"))
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusNotFound, errors.New("no project "+r.PathValue("name")))
	}
	if err != nil {
		return err
	}
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	d, err := q.DeployByNumber(ctx, models.DeployByNumberParams{ProjectID: p.ID, Number: number})
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusNotFound, errors.New("no deploy #"+r.PathValue("number")))
	}
	if err != nil {
		return err
	}
	s, err := status(ctx, c.DB, p, d, web.Zone(r))
	if err != nil {
		return err
	}
	page := Page{Layout: layout.For(r, p.Name+" · "+Word(d)+" #"+strconv.FormatInt(d.Number, 10)), Live: c.Live.Source(Stream(d.ID)), Status: s}
	return web.Render(w, r, http.StatusOK, ShowPage(page))
}

// status is what the deploy's head shows.
func status(ctx context.Context, d *db.DB, p models.Project, dep models.Deploy, zone *time.Location) (Status, error) {
	q := models.New(d.Read)
	now := time.Now().In(zone)
	s := Status{D: dep, P: p, View: view{now: now, started: dep.CreatedAt.In(zone).Format("15:04:05 MST")}}
	inst, err := q.CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	s.Base = inst.BaseDomain
	if running, err := q.RunningDeploySummary(ctx, p.ID); err == nil {
		r := models.Deploy(running)
		s.Running = &r
	} else if !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	if dep.Kind == "copy" {
		if c, err := q.CopyByDeploy(ctx, sql.NullInt64{Int64: dep.ID, Valid: true}); err == nil {
			s.Copy = &c
		} else if !errors.Is(err, sql.ErrNoRows) {
			return s, err
		}
	}
	return s, nil
}

// Progress sends a deploy's page what changed: the log appended, and when
// its status or step changed, its head and its steps again, in Houston's
// zone as the page is. Best effort: the page can always be reloaded.
func Progress(ctx context.Context, hub *live.Hub, d *db.DB, dep models.Deploy, appended string, changed bool) error {
	var actions []templ.Component
	if appended != "" {
		actions = append(actions, turbo.Append("deploy_log", templ.Raw(html.EscapeString(appended))))
	}
	if changed {
		q := models.New(d.Read)
		inst, err := q.CurrentInstallation(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p, err := q.ProjectByID(ctx, dep.ProjectID)
		if err != nil {
			return err
		}
		s, err := status(ctx, d, p, dep, inst.Zone())
		if err != nil {
			return err
		}
		actions = append(actions, turbo.Replace("deploy_status", StatusPart(s)), turbo.Replace("deploy_steps", Steps(dep)))
	}
	if len(actions) == 0 {
		return nil
	}
	return hub.Broadcast(ctx, Stream(dep.ID), actions...)
}

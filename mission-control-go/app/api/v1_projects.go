package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// project is the project the path names ({name}), or the 404 in its words.
func (c V1) project(r *http.Request) (models.Project, error) {
	name := r.PathValue("name")
	p, err := models.New(c.DB.Read).ProjectByName(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		return p, web.Status(http.StatusNotFound, fmt.Errorf("no project %s", name))
	}
	return p, err
}

// Projects is GET /api/v1/projects: every project, by name.
func (c V1) Projects(w http.ResponseWriter, r *http.Request) error {
	ctx, now := r.Context(), time.Now()
	q := models.New(c.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil {
		return err
	}
	projects, err := q.Projects(ctx)
	if err != nil {
		return err
	}
	c.freshStats(ctx, now)
	views := []projectView{}
	for _, p := range projects {
		v, err := c.viewProject(ctx, p, inst, now)
		if err != nil {
			return err
		}
		views = append(views, v)
	}
	return web.JSON(w, http.StatusOK, map[string]any{"projects": views})
}

// Project is GET /api/v1/projects/{name}: one, in detail.
func (c V1) Project(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil {
		return err
	}
	c.freshStats(r.Context(), time.Now())
	v, err := c.viewProjectDetail(r.Context(), p, inst, time.Now())
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, v)
}

// DeploysPerPage is how many deploys a page of them has.
const DeploysPerPage = 20

// Deploys is GET /api/v1/projects/{name}/deploys?page=N: newest first.
func (c V1) Deploys(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	rows, err := models.New(c.DB.Read).DeploySummaries(r.Context(), models.DeploySummariesParams{ProjectID: p.ID, Limit: DeploysPerPage,
		Offset: int64((page - 1) * DeploysPerPage)})
	if err != nil {
		return err
	}
	now := time.Now()
	views := []deployView{}
	for _, row := range rows {
		views = append(views, viewDeploy(models.Deploy(row), now))
	}
	return web.JSON(w, http.StatusOK, map[string]any{"deploys": views})
}

// Deploy is GET /api/v1/projects/{name}/deploys/{number} (or latest), with
// its steps and its log from ?log_from.
func (c V1) Deploy(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	number := r.PathValue("number")
	q := models.New(c.DB.Read)
	var d models.Deploy
	if number == "latest" {
		d, err = q.LatestDeployByNumber(r.Context(), p.ID)
	} else {
		n, _ := strconv.ParseInt(number, 10, 64)
		d, err = q.DeployByNumber(r.Context(), models.DeployByNumberParams{ProjectID: p.ID, Number: n})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusNotFound, fmt.Errorf("no deploy #%s of %s", number, p.Name))
	}
	if err != nil {
		return err
	}
	from, _ := strconv.Atoi(r.URL.Query().Get("log_from"))
	return web.JSON(w, http.StatusOK, viewDeployWithLog(d, from, time.Now()))
}

// ShowBackup is GET /api/v1/projects/{name}/backups/{id} (or latest).
func (c V1) ShowBackup(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	q := models.New(c.DB.Read)
	var run models.BackupRun
	if id == "latest" {
		run, err = q.LatestBackupRun(r.Context(), p.ID)
	} else {
		n, _ := strconv.ParseInt(id, 10, 64)
		run, err = q.ProjectBackupRun(r.Context(), models.ProjectBackupRunParams{ProjectID: p.ID, ID: n})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusNotFound, fmt.Errorf("no backup %s of %s", id, p.Name))
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, viewBackup(run, time.Now()))
}

// Settings is GET /api/v1/settings.
func (c V1) Settings(w http.ResponseWriter, r *http.Request) error {
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]string{"base_domain": inst.BaseDomain, "time_zone": inst.TimeZone})
}

// Storage is GET /api/v1/storage: the locations that were verified.
func (c V1) Storage(w http.ResponseWriter, r *http.Request) error {
	locations, err := models.New(c.DB.Read).VerifiedLocations(r.Context())
	if err != nil {
		return err
	}
	views := []storageView{}
	for _, l := range locations {
		v, err := c.viewStorage(r.Context(), l)
		if err != nil {
			return err
		}
		views = append(views, v)
	}
	return web.JSON(w, http.StatusOK, map[string]any{"locations": views})
}

type volumeView struct {
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	Location *string `json:"location"`
	Placed   bool    `json:"placed"`
}

// Volumes is GET /api/v1/projects/{name}/volumes: each named volume and
// where it lives.
func (c V1) Volumes(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	rows, err := models.New(c.DB.Read).ProjectVolumeRows(r.Context(), p.ID)
	if err != nil {
		return err
	}
	chosen := map[string]models.ProjectVolumeRowsRow{}
	for _, row := range rows {
		chosen[row.Name] = row
	}
	views := []volumeView{}
	for _, v := range p.Volumes.V {
		view := volumeView{Name: v.Name, Path: v.Path}
		if row, ok := chosen[v.Name]; ok {
			view.Location, view.Placed = orNull(row.Location.String), row.PlacedAt.Valid
		}
		views = append(views, view)
	}
	return web.JSON(w, http.StatusOK, map[string]any{"volumes": views})
}

// linked is p, if it's linked to a repo (a webhook needs one).
func linked(p models.Project) error {
	if p.RepoUrl == "" {
		return web.Status(http.StatusUnprocessableEntity, fmt.Errorf("%s isn't linked to a repo (houston link)", p.Name))
	}
	return nil
}

// Webhook is GET /api/v1/projects/{name}/webhook: where the git host rings,
// and its secret until a push has been verified with it.
func (c V1) Webhook(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err == nil {
		err = linked(p)
	}
	if err != nil {
		return err
	}
	return c.webhookView(w, r, p)
}

func (c V1) webhookView(w http.ResponseWriter, r *http.Request, p models.Project) error {
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil {
		return err
	}
	var secret *string
	if !p.WebhookVerifiedAt.Valid {
		secret = orNull(p.WebhookSecret.Reveal())
	}
	return web.JSON(w, http.StatusOK, map[string]any{"url": "https://hooks." + inst.BaseDomain + "/" + p.Name, "verified": p.WebhookVerifiedAt.Valid, "secret": secret})
}

type secretRow struct {
	Name      string     `json:"name"`
	Required  bool       `json:"required"`
	Set       bool       `json:"set"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// Secrets is GET /api/v1/projects/{name}/secrets: each variable
// compose.yml references, whether it has a value, never the value.
func (c V1) Secrets(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	rows, err := models.New(c.DB.Read).ProjectSecretRows(r.Context(), p.ID)
	if err != nil {
		return err
	}
	have := map[string]models.ProjectSecretRowsRow{}
	for _, row := range rows {
		have[row.Key] = row
	}
	list := []secretRow{}
	for _, v := range p.Variables.V {
		s := secretRow{Name: v.Name, Required: v.Required}
		if row, ok := have[v.Name]; ok {
			s.Set = row.Value.Reveal() != ""
			s.UpdatedAt = &row.UpdatedAt
		}
		list = append(list, s)
	}
	return web.JSON(w, http.StatusOK, map[string]any{"secrets": list})
}

// freshStats reads the apps' stats when the last reading is old: someone's
// looking.
func (c V1) freshStats(ctx context.Context, now time.Time) {
	if c.Stats == nil {
		return
	}
	if projects, err := models.New(c.DB.Read).Projects(ctx); err == nil {
		c.Stats.Fresh(ctx, projects, now)
	}
}

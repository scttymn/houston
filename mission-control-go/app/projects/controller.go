package projects

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/live"
	"github.com/scttymn/gantry/sign"
	"github.com/scttymn/gantry/turbo"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/appstats"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
	"github.com/scttymn/houston/mission-control-go/app/services/maintenance"
	"github.com/scttymn/houston/mission-control-go/app/services/systemstatus"
	"github.com/scttymn/houston/mission-control-go/app/shared/layout"
)

// FlightBoard is the board's live stream: refreshed when anything on it
// changes.
const FlightBoard = "flight_board"

// Controller draws the flight board and projects' pages, and takes their
// forms.
type Controller struct {
	DB      *db.DB
	Live    *live.Hub
	Signer  sign.Signer // the flash's
	Stats   *appstats.Stats
	Status  *systemstatus.Status
	Version string
	// Refs reads a project's repo's refs; Git, anything else of a repo.
	Refs models.RefReader
	Git  gitremote.Git
	// Backup is the backup job; Maintenance, the maintenance switch.
	Backup      models.Enqueuer[models.BackupArgs]
	Maintenance maintenance.Switch
}

// rows are the projects as the board shows them, by name.
func (c Controller) rows(ctx context.Context, inst models.Installation) ([]Row, map[string]string, error) {
	q := models.New(c.DB.Read)
	projects, err := q.Projects(ctx)
	if err != nil {
		return nil, nil, err
	}
	backups, err := q.LastGoBackups(ctx)
	if err != nil {
		return nil, nil, err
	}
	lastGo := map[int64]models.BackupRun{}
	for _, b := range backups {
		lastGo[b.ProjectID] = b
	}
	failed, err := q.FailedBackupProjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	deleting, err := q.DeletingProjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	proposals := map[string]string{}
	var rows []Row
	for _, p := range projects {
		row := Row{Project: p, Status: "standby", Hostnames: p.Hostnames(inst.BaseDomain), Stats: c.Stats.For(p.Name)}
		latest, err := q.DeploySummaries(ctx, models.DeploySummariesParams{ProjectID: p.ID, Limit: 1})
		if err != nil {
			return nil, nil, err
		}
		if len(latest) == 1 {
			d := models.Deploy(latest[0])
			row.Latest, row.Status = &d, d.Status
			if d.Status == "hold" && d.ProposedName != "" {
				proposals[p.Name] = d.ProposedName
			}
		}
		if running, err := q.RunningDeploySummary(ctx, p.ID); err == nil {
			d := models.Deploy(running)
			row.Running = &d
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		if b, ok := lastGo[p.ID]; ok {
			row.Backup = &b
		}
		for _, id := range failed {
			row.FailedBackup = row.FailedBackup || id == p.ID
		}
		for _, id := range deleting {
			row.Deleting = row.Deleting || id.Int64 == p.ID
		}
		rows = append(rows, row)
	}
	return rows, proposals, nil
}

// Index is GET /: the flight board.
func (c Controller) Index(w http.ResponseWriter, r *http.Request) error {
	ctx, now := r.Context(), time.Now()
	q := models.New(c.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, proposals, err := c.rows(ctx, inst)
	if err != nil {
		return err
	}
	zone := web.Zone(r)
	b := Board{Page: layout.For(r, "Projects · Mission Control"), Live: c.Live.Source(FlightBoard), Base: inst.BaseDomain, Version: c.Version,
		Rows: rows, Proposals: proposals, Now: now, Zone: zone, PerHost: inst.DnsMode == "per_host",
		Newer: models.NewerRelease(c.Version, inst.LatestRelease), Tunnel: c.Status.Tunnel(ctx, inst, now)}
	b.Page.Head = layout.MorphRefreshes()
	b.OnLAN = r.Host != "admin."+inst.BaseDomain
	b.AdminRoute = c.Status.Route(ctx, inst, "admin", !b.OnLAN, now)
	b.HooksRoute = c.Status.Route(ctx, inst, "hooks", false, now)
	for _, row := range rows {
		switch row.Status {
		case "in_flight":
			b.InFlight++
		case "no_go":
			b.NoGo++
		}
		next, err := c.nextBackup(ctx, row, inst, now)
		if err != nil {
			return err
		}
		if next != nil && (b.NextBackup == nil || next.Before(*b.NextBackup)) {
			b.NextBackup = next
		}
	}
	if u, err := q.ShownUpdate(ctx, sql.NullTime{Time: now.Add(-models.UpdateShownFor), Valid: true}); err == nil {
		b.Update = &u
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if s, err := q.DefaultStorage(ctx); err == nil {
		b.Storage = &s
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return web.Render(w, r, http.StatusOK, BoardPage(b))
}

// nextBackup is when a project's next scheduled backup is due, in the
// installation's zone: one with data, that has served, with storage.
func (c Controller) nextBackup(ctx context.Context, row Row, inst models.Installation, now time.Time) (*time.Time, error) {
	p := row.Project
	if (len(p.Volumes.V) == 0 && len(p.Databases.V) == 0) || row.Running == nil {
		return nil, nil
	}
	q := models.New(c.DB.Read)
	if _, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	s, err := models.ParseSchedule(p.BackupSchedule, inst.Zone())
	if err != nil {
		return nil, nil
	}
	next, err := s.NextAt(ctx, q, p.ID, now)
	return &next, err
}

// Resources is GET /resources: each app's gauges read now, as Turbo
// Stream updates for its row and its card (an open board asks every 30 s
// while it's in view).
func (c Controller) Resources(w http.ResponseWriter, r *http.Request) error {
	ctx, now := r.Context(), time.Now()
	projects, err := models.New(c.DB.Read).Projects(ctx)
	if err != nil {
		return err
	}
	c.Stats.Fresh(ctx, projects, now)
	var actions []templ.Component
	for _, p := range projects {
		actions = append(actions, turbo.Action{Name: "update", Targets: "[data-resources='" + p.Name + "']", Content: Resources(c.Stats.For(p.Name), now)})
	}
	return turbo.Stream(w, r, actions...)
}

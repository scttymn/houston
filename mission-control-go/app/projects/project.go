package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/shared/layout"
)

// project is the project the request names, or a 404.
func (c Controller) project(r *http.Request) (models.Project, error) {
	p, err := models.New(c.DB.Read).ProjectByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, sql.ErrNoRows) {
		return p, web.Status(http.StatusNotFound, errors.New("no project "+r.PathValue("name")))
	}
	return p, err
}

// ShowProject is GET /projects/{name}: the project page.
func (c Controller) ShowProject(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	s, err := c.load(r, p, nil)
	if err != nil {
		return err
	}
	if kind, msg, ok := c.flash().Take(w, r); ok {
		if kind == "alert" {
			s.Alert = msg
		} else {
			s.Notice = msg
		}
	}
	return web.Render(w, r, http.StatusOK, ShowPage(s))
}

// load is what the project page shows (the Rails app's ProjectPage).
func (c Controller) load(r *http.Request, p models.Project, secretErrors map[string]string) (Show, error) {
	ctx, now := r.Context(), time.Now()
	q := models.New(c.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Show{}, err
	}
	s := Show{Page: layout.For(r, p.Name+" · Project"), P: p, Inst: inst, Status: "standby", SecretErrors: secretErrors, Now: now.In(web.Zone(r)),
		Replace: r.URL.Query().Get("replace"), SnapshotsKind: r.URL.Query().Get("snapshots")}
	s.PageN, _ = strconv.Atoi(r.URL.Query().Get("page"))
	s.PageN = max(s.PageN, 1)
	count, err := q.DeployCount(ctx, p.ID)
	if err != nil {
		return s, err
	}
	s.DeployCount = int(count)
	summaries, err := q.DeploySummaries(ctx, models.DeploySummariesParams{ProjectID: p.ID, Limit: PerPage, Offset: int64((s.PageN - 1) * PerPage)})
	if err != nil {
		return s, err
	}
	for _, d := range summaries {
		s.Deploys = append(s.Deploys, models.Deploy(d))
	}
	latest, err := q.DeploySummaries(ctx, models.DeploySummariesParams{ProjectID: p.ID, Limit: 1})
	if err != nil {
		return s, err
	}
	if len(latest) == 1 {
		d := models.Deploy(latest[0])
		s.Status = d.Status
		if d.Status == "hold" && d.ProposedName != "" {
			s.Proposal = &d
			if s.Refusal, err = models.NameRefusal(ctx, q, d.ProposedName); err != nil {
				return s, err
			}
		}
	}
	if running, err := q.RunningDeploySummary(ctx, p.ID); err == nil {
		d := models.Deploy(running)
		s.Running = &d
	} else if !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}

	secrets, err := q.SavedSecrets(ctx, p.ID)
	if err != nil {
		return s, err
	}
	s.Secrets = map[string]models.Secret{}
	values := map[string]string{}
	for _, secret := range secrets {
		s.Secrets[secret.Key] = secret
		values[secret.Key] = secret.Value.Reveal()
	}
	s.Variables = slices.Clone(p.Variables.V)
	sort.SliceStable(s.Variables, func(i, j int) bool {
		a, b := s.Variables[i], s.Variables[j]
		if a.Required != b.Required {
			return a.Required
		}
		return a.Name < b.Name
	})
	s.Missing = models.MissingSecrets(p.Variables.V, values)
	if p.RepoUrl != "" {
		s.HooksRoute = c.Status.Route(ctx, inst, "hooks", false, now)
	}
	if l, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64); err == nil {
		s.Storage = &l
	} else if !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	if run, err := q.LastBackup(ctx, p.ID); err == nil {
		s.Backup = &run
	} else if !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	if good, err := q.LastGoBackup(ctx, p.ID); err == nil {
		var found struct {
			SQLite []struct{ Volume string } `json:"sqlite"`
		}
		json.Unmarshal([]byte(good.Found), &found)
		for _, f := range found.SQLite {
			s.SQLiteVolumes = append(s.SQLiteVolumes, f.Volume)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	if s.Live, err = q.LiveLocations(ctx); err != nil {
		return s, err
	}
	chosen, err := q.ProjectVolumeChoices(ctx, p.ID)
	if err != nil {
		return s, err
	}
	for _, v := range p.Volumes.V {
		row := VolumeRow{Volume: v, Where: "local disk"}
		for _, ch := range chosen {
			if ch.Name != v.Name {
				continue
			}
			row.Placed = ch.PlacedAt.Valid
			if ch.LocationID.Valid {
				if l, err := q.StorageLocationByID(ctx, ch.LocationID.Int64); err == nil {
					row.Where, row.Chosen, row.NFS = l.Name, l.Name, l.Kind == "nfs"
				}
			}
		}
		s.Volumes = append(s.Volumes, row)
	}
	if err := c.loadCopies(ctx, q, &s, now); err != nil {
		return s, err
	}
	if d, err := q.LatestDeletion(ctx, sql.NullInt64{Int64: p.ID, Valid: true}); err == nil {
		cancelled := d.Status == "no_go" && !d.RemovingAt.Valid
		if !(cancelled && d.FinishedAt.Valid && d.FinishedAt.Time.Before(now.Add(-24*time.Hour))) {
			s.Deletion = &d
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	s.Deleting, err = models.Deleting(ctx, q, p.ID)
	return s, err
}

// loadCopies are the copies to and from a new name the page tells of.
func (c Controller) loadCopies(ctx context.Context, q *models.Queries, s *Show, now time.Time) error {
	id := sql.NullInt64{Int64: s.P.ID, Valid: true}
	one := func(get func() (models.ProjectCopy, error)) (*models.ProjectCopy, error) {
		cp, err := get()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return &cp, err
	}
	var err error
	if s.CopyActive, err = one(func() (models.ProjectCopy, error) { return q.ActiveCopyFrom(ctx, id) }); err != nil {
		return err
	}
	if s.CopyActive != nil && s.CopyActive.DeployID.Valid {
		if d, err := q.DeployByID(ctx, s.CopyActive.DeployID.Int64); err == nil {
			s.CopyActiveTo = d.Number
		}
	}
	if s.CopyActive == nil {
		if s.CopyFailed, err = one(func() (models.ProjectCopy, error) {
			return q.RecentFailedCopyFrom(ctx, models.RecentFailedCopyFromParams{ProjectID: id, Since: now.Add(-24 * time.Hour)})
		}); err != nil {
			return err
		}
	}
	if s.CopyTo, err = one(func() (models.ProjectCopy, error) { return q.CopyWentTo(ctx, id) }); err != nil {
		return err
	}
	s.CopiedFrom, err = one(func() (models.ProjectCopy, error) { return q.CopyCameFrom(ctx, id) })
	return err
}

func (c Controller) flash() web.Flash { return web.Flash{Signer: c.Signer} }

// back redirects to the project page (at anchor, "" for its top), with a
// notice or an alert.
func (c Controller) back(w http.ResponseWriter, r *http.Request, p models.Project, kind, message, anchor string) error {
	to := "/projects/" + p.Name
	if anchor != "" {
		to += anchor
	}
	c.flash().Redirect(w, r, to, kind, message)
	return nil
}

package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/gantry/auth"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/internal/mission"
	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
	"github.com/scttymn/houston/mission-control-go/app/services/handover"
	"github.com/scttymn/houston/mission-control-go/app/shared/layout"
)

// by is who's signed in: their email.
func by(r *http.Request) string {
	u, _ := auth.Current(r)
	return u.EmailAddress
}

// Snapshots is GET /projects/{name}/snapshots?kind=: the Snapshots panel's
// list, a lazy frame (listing a remote repository can take seconds). Every
// tab lists, so the tabs' counts stay put; Settings shows even when the
// listing fails.
func (c Controller) ShowSnapshots(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	q := models.New(c.DB.Read)
	s := Snapshots{P: p, Kind: r.URL.Query().Get("kind"), Counts: map[string]int{}, Zone: web.Zone(r)}
	if s.Kind != "deploy" && s.Kind != "settings" {
		s.Kind = "auto"
	}
	if l, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64); err == nil {
		s.Location = &l
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if schedule, err := models.ParseSchedule(p.BackupSchedule, s.Zone); err == nil {
		s.Schedule = schedule.Words()
	}
	ready, err := q.ReadyLocations(ctx)
	if err != nil {
		return err
	}
	for _, l := range ready {
		switch {
		case l.IsDefault && s.Default == nil:
			s.Default = &l
		default:
			s.Others = append(s.Others, l)
		}
		if p.BackupLocationID.Valid && l.ID == p.BackupLocationID.Int64 {
			s.Chosen = l.Name
		}
	}
	if good, err := q.LastGoBackup(ctx, p.ID); err == nil {
		var found struct {
			SQLite []struct{ Path string } `json:"sqlite"`
		}
		json.Unmarshal([]byte(good.Found), &found)
		for _, f := range found.SQLite {
			s.SQLite = append(s.SQLite, f.Path)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if s.Location != nil {
		all, err := c.Snapshots.For(ctx, p.Name, *s.Location)
		var unavailable backup.Unavailable
		switch {
		case errors.As(err, &unavailable):
			s.Error = unavailable.Error()
		case err != nil:
			return err
		}
		for _, snap := range all {
			s.Counts[snap.Kind]++
			// A deleted project's final snapshot sits with the ones taken before a change.
			if snap.Kind == s.Kind || (s.Kind == "deploy" && snap.Kind == "final") {
				s.List = append(s.List, snap)
			}
		}
	}
	return web.Render(w, r, http.StatusOK, SnapshotsFrame(s))
}

// DownloadSnapshot is GET /projects/{name}/snapshots/{id}/download
// ?location=: everything in one snapshot as a zip, streamed from restic. A
// refusal comes before any byte, so it can still be a redirect.
func (c Controller) DownloadSnapshot(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("id")
	export, err := backup.OpenExport(ctx, models.New(c.DB.Read), c.Docker, c.Snapshots, c.Log, p, r.URL.Query().Get("location"), id, by(r))
	var (
		notFound backup.NotFound
		busy     backup.Busy
		failed   backup.Failed
	)
	switch {
	case errors.As(err, &notFound):
		return web.Status(http.StatusNotFound, notFound)
	case errors.As(err, &busy):
		return c.back(w, r, p, "alert", "Can't download snapshot "+truncate(id, 64)+": "+busy.Error(), "")
	case errors.As(err, &failed):
		return c.back(w, r, p, "alert", "Can't download snapshot "+truncate(id, 64)+": "+failed.Error(), "")
	case err != nil:
		return err
	}
	export.Headers(w.Header())
	w.WriteHeader(http.StatusOK)
	export.Send(ctx, w)
	return nil
}

// restoring is the snapshot and location a restore names, or a redirect
// saying why they can't be read.
func (c Controller) restoring(w http.ResponseWriter, r *http.Request, snapshot, location string) (Restore, bool, error) {
	p, err := c.project(r)
	if err != nil {
		return Restore{}, false, err
	}
	ctx := r.Context()
	q := models.New(c.DB.Read)
	l, err := q.StorageLocationByName(ctx, location)
	if errors.Is(err, sql.ErrNoRows) {
		return Restore{}, false, web.Status(http.StatusNotFound, errors.New("no storage location "+location))
	}
	if err != nil {
		return Restore{}, false, err
	}
	all, err := c.Snapshots.For(ctx, p.Name, l)
	var unavailable backup.Unavailable
	if errors.As(err, &unavailable) {
		return Restore{}, false, c.back(w, r, p, "alert", "Can't read "+l.Name+"'s snapshots: "+unavailable.Error(), "")
	}
	if err != nil {
		return Restore{}, false, err
	}
	i := slices.IndexFunc(all, func(s backup.Snapshot) bool { return s.ID == snapshot || s.ShortID == snapshot })
	if i < 0 {
		return Restore{}, false, web.Status(http.StatusNotFound, errors.New("no snapshot "+snapshot))
	}
	rs := Restore{Page: layout.For(r, "Restore "+p.Name), P: p, Snapshot: all[i], Location: l}
	if running, err := q.RunningDeploySummary(ctx, p.ID); err == nil {
		d := models.Deploy(running)
		rs.Running = &d
	} else if !errors.Is(err, sql.ErrNoRows) {
		return rs, false, err
	}
	return rs, true, nil
}

// NewRestore is GET /projects/{name}/restores/new?snapshot=&location=.
func (c Controller) NewRestore(w http.ResponseWriter, r *http.Request) error {
	rs, ok, err := c.restoring(w, r, r.URL.Query().Get("snapshot"), r.URL.Query().Get("location"))
	if err != nil || !ok {
		return err
	}
	return web.Render(w, r, http.StatusOK, RestorePage(rs))
}

// CreateRestore is POST /projects/{name}/restores: the restore queued once
// the admin typed the project's name, then its page.
func (c Controller) CreateRestore(w http.ResponseWriter, r *http.Request) error {
	rs, ok, err := c.restoring(w, r, r.PostFormValue("snapshot"), r.PostFormValue("location"))
	if err != nil || !ok {
		return err
	}
	commit := func(ctx context.Context, p models.Project, sha string) string {
		return c.Git.Commit(ctx, gitremote.Link{RepoURL: p.RepoUrl, DeployKey: p.DeployKeyPrivate.Reveal()}, sha)
	}
	restore, err := backup.RequestRestore(r.Context(), c.DB, c.Snapshots, commit, rs.P, rs.Snapshot.ID, &rs.Location, r.PostFormValue("confirm"), time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		rs.Error = refused.Msg
		return web.Render(w, r, http.StatusUnprocessableEntity, RestorePage(rs))
	}
	if err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	http.Redirect(w, r, "/projects/"+rs.P.Name+"/deploys/"+strconv.FormatInt(restore.Number, 10), http.StatusSeeOther)
	return nil
}

// copying is the Copy page's.
func (c Controller) copying(r *http.Request) (Copy, error) {
	p, err := c.project(r)
	if err != nil {
		return Copy{}, err
	}
	ctx := r.Context()
	q := models.New(c.DB.Read)
	cp := Copy{Page: layout.For(r, "Copy "+p.Name), P: p, Storage: "none"}
	inst, err := q.CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return cp, err
	}
	latest, err := q.DeploySummaries(ctx, models.DeploySummariesParams{ProjectID: p.ID, Limit: 1})
	if err != nil {
		return cp, err
	}
	if len(latest) == 1 && latest[0].Status == "hold" && latest[0].ProposedName != "" {
		d := models.Deploy(latest[0])
		cp.Proposal, cp.To = &d, d.ProposedName
		if cp.Refusal, err = models.NameRefusal(ctx, q, cp.To); err != nil {
			return cp, err
		}
	} else {
		cp.Refusal = p.Name + "'s latest deploy doesn't propose a copy"
	}
	theirs := append([]string{cp.To + "." + inst.BaseDomain}, p.Domains.V...)
	for _, host := range p.Hostnames(inst.BaseDomain) {
		if slices.Contains(theirs, host) {
			cp.Shared = append(cp.Shared, host)
		}
	}
	volumes, err := c.volumeWords(ctx, q, p)
	if err != nil {
		return cp, err
	}
	cp.Volumes = orWords(strings.Join(volumes, ", "), "none")
	if l, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64); err == nil {
		cp.Storage = l.Name
	} else if !errors.Is(err, sql.ErrNoRows) {
		return cp, err
	}
	return cp, nil
}

// volumeWords are each volume with where it lives: "data (nas)".
func (c Controller) volumeWords(ctx context.Context, q *models.Queries, p models.Project) ([]string, error) {
	chosen, err := q.ProjectVolumeChoices(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, v := range p.Volumes.V {
		where := "local disk"
		for _, ch := range chosen {
			if ch.Name == v.Name {
				if where, err = models.VolumeWhere(ctx, q, ch); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, v.Name+" ("+where+")")
	}
	return out, nil
}

// NewCopy is GET /projects/{name}/copy/new: copying to the new name its
// compose.yml gives.
func (c Controller) NewCopy(w http.ResponseWriter, r *http.Request) error {
	cp, err := c.copying(r)
	if err != nil {
		return err
	}
	return web.Render(w, r, http.StatusOK, CopyPage(cp))
}

// CreateCopy is POST /projects/{name}/copy: asked, then the copy's deploy.
func (c Controller) CreateCopy(w http.ResponseWriter, r *http.Request) error {
	cp, err := c.copying(r)
	if err != nil {
		return err
	}
	read := func(ctx context.Context, p models.Project) (string, *mission.Inspection, string) {
		return c.Git.Read(ctx, gitremote.Link{RepoURL: p.RepoUrl, Branch: p.Branch, ComposePath: p.ComposePath, DeployKey: p.DeployKeyPrivate.Reveal()})
	}
	copied, err := models.RequestCopy(r.Context(), c.DB, read, cp.P, r.PostFormValue("confirm"), by(r), time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		cp.Error = refused.Msg
		return web.Render(w, r, http.StatusUnprocessableEntity, CopyPage(cp))
	}
	if err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	d, err := models.New(c.DB.Read).DeployByID(r.Context(), copied.DeployID.Int64)
	if err != nil {
		return err
	}
	http.Redirect(w, r, "/projects/"+copied.ToName+"/deploys/"+strconv.FormatInt(d.Number, 10), http.StatusSeeOther)
	return nil
}

// copyOf is the project the request names and the copy that made it.
func (c Controller) copyOf(r *http.Request) (models.Project, models.ProjectCopy, error) {
	p, err := c.project(r)
	if err != nil {
		return p, models.ProjectCopy{}, err
	}
	cp, err := models.New(c.DB.Read).CopyOf(r.Context(), sql.NullInt64{Int64: p.ID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return p, cp, web.Status(http.StatusNotFound, errors.New(p.Name+" isn't a copy"))
	}
	return p, cp, err
}

// CancelCopy is POST /projects/{name}/copy/cancel, on the new project.
func (c Controller) CancelCopy(w http.ResponseWriter, r *http.Request) error {
	p, cp, err := c.copyOf(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	d, err := models.New(c.DB.Read).DeployByID(ctx, cp.DeployID.Int64)
	if err != nil {
		return err
	}
	to := "/projects/" + p.Name + "/deploys/" + strconv.FormatInt(d.Number, 10)
	cleanUp, err := handover.Cancel(ctx, c.DB, cp.ID, by(r), time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		c.flash().Redirect(w, r, to, "alert", refused.Msg)
		return nil
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
	http.Redirect(w, r, to, http.StatusSeeOther)
	return nil
}

// UndoCopy is POST /projects/{name}/copy/undo {confirm}, on the new
// project: the hosts back to the old one, then this one deleted.
func (c Controller) UndoCopy(w http.ResponseWriter, r *http.Request) error {
	p, cp, err := c.copyOf(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	inst, err := models.New(c.DB.Read).CurrentInstallation(ctx)
	if err != nil {
		return err
	}
	h := handover.Handover{DB: c.DB, Docker: c.Docker, Installation: inst, Cloudflare: c.Cloudflare}
	deletion, err := h.Undo(ctx, p, cp, r.PostFormValue("confirm"), by(r), time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return c.back(w, r, p, "alert", refused.Msg, "")
	}
	if err != nil {
		return err
	}
	if _, err := c.Delete.Enqueue(ctx, models.DeletionArgs{ID: deletion.ID}); err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	http.Redirect(w, r, "/deletions/"+strconv.FormatInt(deletion.ID, 10), http.StatusSeeOther)
	return nil
}

// deleting is the Delete page's.
func (c Controller) deleting(r *http.Request) (Delete, error) {
	p, err := c.project(r)
	if err != nil {
		return Delete{}, err
	}
	ctx := r.Context()
	q := models.New(c.DB.Read)
	d := Delete{Page: layout.For(r, "Delete "+p.Name), P: p, DeleteBackups: r.PostFormValue("delete_backups") == "1"}
	inst, err := q.CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return d, err
	}
	d.Base = inst.BaseDomain
	g := models.Generation{Project: p.Name, Number: p.DataGeneration}
	d.Containers = []string{p.Name + "-web"}
	for _, s := range p.Accessories() {
		d.Containers = append(d.Containers, g.Container(s))
	}
	if d.Data, err = c.volumeWords(ctx, q, p); err != nil {
		return d, err
	}
	if len(p.Accessories()) > 0 {
		d.Data = append(d.Data, "what "+web.Sentence(p.Accessories())+" hold")
	}
	served, err := q.ProjectHasServed(ctx, p.ID)
	if err != nil {
		return d, err
	}
	d.Kept = served && (len(p.Volumes.V) > 0 || len(p.Databases.V) > 0)
	if l, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64); err == nil {
		d.Storage = &l
	} else if !errors.Is(err, sql.ErrNoRows) {
		return d, err
	}
	used, err := q.UsedLocations(ctx, models.UsedLocationsParams{Project: p.ID, Name: p.Name})
	if err != nil {
		return d, err
	}
	for _, l := range used {
		d.Locations = append(d.Locations, l.Name)
	}
	if d.Storage != nil && !slices.Contains(d.Locations, d.Storage.Name) {
		d.Locations = append(d.Locations, d.Storage.Name)
		slices.Sort(d.Locations)
	}
	return d, nil
}

// NewDeletion is GET /projects/{name}/deletion/new.
func (c Controller) NewDeletion(w http.ResponseWriter, r *http.Request) error {
	d, err := c.deleting(r)
	if err != nil {
		return err
	}
	return web.Render(w, r, http.StatusOK, DeletePage(d))
}

// CreateDeletion is POST /projects/{name}/deletion {confirm,
// delete_backups}: asked, then its page. Asking again for one that stopped
// partway (Finish deleting) resumes it.
func (c Controller) CreateDeletion(w http.ResponseWriter, r *http.Request) error {
	d, err := c.deleting(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	var deletion models.ProjectDeletion
	err = c.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		deletion, err = models.RequestDeletion(ctx, tx, d.P, r.PostFormValue("confirm"), d.DeleteBackups, by(r), time.Now())
		return err
	})
	var refused models.Refused
	if errors.As(err, &refused) {
		d.Error = refused.Msg
		return web.Render(w, r, http.StatusUnprocessableEntity, DeletePage(d))
	}
	if err != nil {
		return err
	}
	if _, err := c.Delete.Enqueue(ctx, models.DeletionArgs{ID: deletion.ID}); err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	http.Redirect(w, r, "/deletions/"+strconv.FormatInt(deletion.ID, 10), http.StatusSeeOther)
	return nil
}

// ShowDeletion is GET /deletions/{id}: its steps and log while it runs
// (the page refreshes itself), then what's left.
func (c Controller) ShowDeletion(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := models.New(c.DB.Read)
	d, err := q.DeletionByID(ctx, web.ID(r, "id"))
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusNotFound, errors.New("no deletion "+r.PathValue("id")))
	}
	if err != nil {
		return err
	}
	page := Deletion{Page: layout.For(r, "Deleting "+d.Name+" · Mission Control"), D: d, Project: d.ProjectID.Valid}
	page.Page.Head = layout.MorphRefreshes()
	if d.SnapshotLocationID.Valid {
		if l, err := q.StorageLocationByID(ctx, d.SnapshotLocationID.Int64); err == nil {
			page.Location = l.Name
		}
	}
	return web.Render(w, r, http.StatusOK, DeletionPage(page))
}

// truncate is Rails' String#truncate.
func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-3]) + "..."
}

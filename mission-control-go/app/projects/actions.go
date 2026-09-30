package projects

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/gantry/auth"
	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/linking"
	"github.com/scttymn/houston/mission-control-go/app/services/maintenance"
)

// Check is POST /projects/{name}/check: a check for changes now (the
// webhook does the same through a job).
func (c Controller) Check(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	queued, err := models.CheckForChanges(r.Context(), c.DB, c.Refs, p, time.Now())
	if err != nil {
		return err
	}
	if p, err = models.New(c.DB.Read).ProjectByID(r.Context(), p.ID); err != nil {
		return err
	}
	var words []string
	for _, d := range queued {
		words = append(words, d.ShortSha()+" ("+d.Ref+")")
	}
	notice := "Nothing new to deploy."
	switch {
	case p.LastCheckError != "":
		notice = "Couldn't read the repo: " + p.LastCheckError
	case len(words) > 0:
		notice = "Queued " + web.Sentence(words) + "."
	}
	return c.back(w, r, p, "notice", notice, "")
}

// RotateWebhook is POST /projects/{name}/rotate_webhook: a new webhook
// secret, shown until its first verified delivery.
func (c Controller) RotateWebhook(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	if _, err := models.New(c.DB.Write).RotateWebhookSecret(r.Context(), models.RotateWebhookSecretParams{WebhookSecret: crypt.Of(models.NewToken()),
		UpdatedAt: time.Now(), ID: p.ID}); err != nil {
		return err
	}
	return c.back(w, r, p, "notice", "New webhook secret. Paste it into the repo's webhook settings.", "")
}

// Deploy is POST /projects/{name}/deploys: the head of what the deploy rule
// matches (fresh=1: rebuilt without Docker's layer cache), then its page.
func (c Controller) Deploy(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	if p.RepoUrl == "" {
		return c.back(w, r, p, "alert", "Link the repo first (Add project or houston link): a deploy fetches its commit from it.", "")
	}
	d, err := models.QueueHead(r.Context(), c.DB, c.Refs, p, r.URL.Query().Get("fresh") != "" || r.PostFormValue("fresh") != "", time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return c.back(w, r, p, "alert", "Can't deploy: "+refused.Msg, "")
	}
	if err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	http.Redirect(w, r, "/projects/"+p.Name+"/deploys/"+strconv.FormatInt(d.Number, 10), http.StatusSeeOther)
	return nil
}

// BackUp is POST /projects/{name}/backups: a snapshot now.
func (c Controller) BackUp(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	err = c.DB.Tx(ctx, func(tx *db.Tx) error {
		_, err := models.RequestManualBackup(ctx, tx, c.Backup, p, time.Now())
		return err
	})
	var refused models.Refused
	if errors.As(err, &refused) {
		return c.back(w, r, p, "alert", "Can't back up "+p.Name+": "+refused.Msg+".", "")
	}
	if err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	return c.back(w, r, p, "notice", "Backing up "+p.Name+".", "")
}

// location is the storage location a form names ("": none), or a 404.
func (c Controller) location(r *http.Request) (*models.StorageLocation, error) {
	name := r.PostFormValue("location")
	if name == "" {
		return nil, nil
	}
	l, err := models.New(c.DB.Read).StorageLocationByName(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, web.Status(http.StatusNotFound, errors.New("no storage location "+name))
	}
	return &l, err
}

// ChooseVolume is PATCH /projects/{name}/volumes/{volume}: where a volume
// will live, until Houston makes it.
func (c Controller) ChooseVolume(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	location, err := c.location(r)
	if err != nil {
		return err
	}
	name := r.PathValue("volume")
	_, err = models.ChooseVolume(r.Context(), c.DB, p, name, location, time.Now())
	var none models.NoVolume
	var refused models.Refused
	var placed models.Placed
	switch {
	case errors.As(err, &none):
		return web.Status(http.StatusNotFound, none)
	case errors.As(err, &refused):
		return c.back(w, r, p, "alert", refused.Msg, "")
	case errors.As(err, &placed):
		return c.back(w, r, p, "alert", placed.Msg, "")
	case err != nil:
		return err
	}
	where := "local disk"
	if location != nil {
		where = location.Name
	}
	return c.back(w, r, p, "notice", name+" will be made on "+where+" at the next deploy.", "")
}

// BackupTarget is PATCH /projects/{name}/backup_target: where the
// project's backups go ("": the default).
func (c Controller) BackupTarget(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	location, err := c.location(r)
	if err != nil {
		return err
	}
	target, err := models.ChooseBackupTarget(r.Context(), c.DB, p, location, time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return c.back(w, r, p, "alert", refused.Msg, "?snapshots=settings#snapshots")
	}
	if err != nil {
		return err
	}
	words := p.Name + " backs up to "
	if target != nil {
		words += target.Name
	}
	if location == nil {
		words += " (the default)"
	}
	return c.back(w, r, p, "notice", words+".", "?snapshots=settings#snapshots")
}

// MoveRepo is PATCH /projects/{name}/repo: the repo moved or renamed on
// its git host (checked first).
func (c Controller) MoveRepo(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	url := r.PostFormValue("repo_url")
	err = linking.Move(r.Context(), c.DB, c.Git, p, url, time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return c.back(w, r, p, "alert", refused.Msg, "#webhook")
	}
	if err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	return c.back(w, r, p, "notice", p.Name+"'s repo is now "+strings.TrimSpace(url)+".", "#webhook")
}

// ToggleMaintenance is PATCH /projects/{name}/maintenance {on, message}: the
// maintenance page on or off.
func (c Controller) ToggleMaintenance(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	ctx, now := r.Context(), time.Now()
	on := r.PostFormValue("on") == "1"
	if on {
		u, _ := auth.Current(r)
		_, err = c.Maintenance.On(ctx, p, u.EmailAddress, strings.TrimSpace(r.PostFormValue("message")), now)
	} else {
		_, err = c.Maintenance.Off(ctx, p, now)
	}
	var failed maintenance.Failed
	var refused models.Refused
	switch {
	case errors.As(err, &failed):
		return c.back(w, r, p, "alert", string(failed), "")
	case errors.As(err, &refused):
		return c.back(w, r, p, "alert", refused.Msg, "")
	case err != nil:
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	if on {
		return c.back(w, r, p, "notice", p.Name+" shows the maintenance page. It stays up until you turn it off.", "")
	}
	return c.back(w, r, p, "notice", p.Name+" is back: its hostnames go to the app again.", "")
}

// secretKey is the project and the secret the request names: only a
// variable compose.yml references.
func (c Controller) secretKey(r *http.Request) (models.Project, string, error) {
	p, err := c.project(r)
	if err != nil {
		return p, "", err
	}
	key := r.PathValue("key")
	if !p.HasVariable(key) {
		return p, key, web.Status(http.StatusNotFound, errors.New(p.Name+" references no "+key))
	}
	return p, key, nil
}

// SetSecret is PUT /projects/{name}/secrets/{key} {value}: write-only; a
// value refused is the page again, with why.
func (c Controller) SetSecret(w http.ResponseWriter, r *http.Request) error {
	p, key, err := c.secretKey(r)
	if err != nil {
		return err
	}
	value := r.PostFormValue("value")
	if problem := models.ValidSecret(key, value); problem != nil {
		s, err := c.load(r, p, map[string]string{key: strings.TrimPrefix(problem.Error(), key+" ")})
		if err != nil {
			return err
		}
		return web.Render(w, r, http.StatusUnprocessableEntity, ShowPage(s))
	}
	if err := c.saveSecret(r, p, key, value); err != nil {
		return err
	}
	return c.back(w, r, p, "notice", key+" saved.", "")
}

// GenerateSecret is POST /projects/{name}/secrets/{key}/generate: a value
// nobody needs to know.
func (c Controller) GenerateSecret(w http.ResponseWriter, r *http.Request) error {
	p, key, err := c.secretKey(r)
	if err != nil {
		return err
	}
	if err := c.saveSecret(r, p, key, models.GeneratedSecret()); err != nil {
		return err
	}
	return c.back(w, r, p, "notice", key+" generated and saved.", "")
}

// RemoveSecret is DELETE /projects/{name}/secrets/{key}.
func (c Controller) RemoveSecret(w http.ResponseWriter, r *http.Request) error {
	p, key, err := c.secretKey(r)
	if err != nil {
		return err
	}
	if err := models.New(c.DB.Write).DeleteSecret(r.Context(), models.DeleteSecretParams{ProjectID: p.ID, Key: key}); err != nil {
		return err
	}
	return c.back(w, r, p, "notice", key+" removed.", "")
}

func (c Controller) saveSecret(r *http.Request, p models.Project, key, value string) error {
	return models.New(c.DB.Write).SaveSecret(r.Context(), models.SaveSecretParams{ProjectID: p.ID, Key: key, Value: crypt.Of(value), Now: time.Now()})
}

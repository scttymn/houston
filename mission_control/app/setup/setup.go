// Package setup is first run: the admin made with the installer's code, then Cloudflare,
// then storage. Open only until each is done.
package setup

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/scttymn/gantry/auth"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/cfsetup"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
	"github.com/scttymn/houston/mission_control/app/services/storage"
	"github.com/scttymn/houston/mission_control/app/settings"
	"github.com/scttymn/houston/mission_control/app/shared/layout"
)

// Controller is setup's pages.
type Controller struct {
	DB     *db.DB
	SignIn *auth.Auth
	Limits *web.Limits
	// Cloudflare is step 2's work; Docker runs restic for step 3's.
	Cloudflare cfsetup.Setup
	Docker     dockercmd.Runner
}

// NextStep is where a signed-in admin goes until setup is done: "" once
// it is.
func NextStep(ctx context.Context, d *db.DB) (string, error) {
	q := models.New(d.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if !inst.CloudflareConnectedAt.Valid {
		return "/setup/cloudflare", nil
	}
	switch ready, err := q.DefaultStorageReady(ctx); {
	case err != nil:
		return "", err
	case !ready:
		return "/setup/storage", nil
	}
	return "", nil
}

// closed is whether the admin exists: then setup's first step is closed,
// and the request is sent on, to sign in or to the flight board.
func (c Controller) closed(w http.ResponseWriter, r *http.Request) (bool, error) {
	set, err := models.New(c.DB.Read).UserExists(r.Context())
	if err != nil || !set {
		return false, err
	}
	to := "/sign-in"
	if c.SignIn.SignedIn(r) {
		to = "/"
	}
	http.Redirect(w, r, to, http.StatusFound)
	return true, nil
}

// Show is GET /setup: step 1, the admin.
func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	if closed, err := c.closed(w, r); closed || err != nil {
		return err
	}
	return web.Render(w, r, http.StatusOK, AdminPage(page(r), r, models.Admin{}, nil))
}

// Create is POST /setup: the admin made, and signed in.
func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	if closed, err := c.closed(w, r); closed || err != nil {
		return err
	}
	if !c.Limits.Allow("setup:"+web.ClientIP(r), 10, 3*time.Minute, time.Now()) {
		w.WriteHeader(http.StatusTooManyRequests)
		return nil
	}
	form := models.Admin{Code: r.PostFormValue("setup[code]"), EmailAddress: r.PostFormValue("setup[email_address]"),
		Password: r.PostFormValue("setup[password]"), PasswordConfirmation: r.PostFormValue("setup[password_confirmation]")}
	id, err := form.Save(r.Context(), c.DB, time.Now())
	var invalid web.Invalid
	if errors.As(err, &invalid) {
		return web.Render(w, r, http.StatusUnprocessableEntity, AdminPage(page(r), r, form, invalid))
	}
	if err != nil {
		return err
	}
	u, err := c.SignIn.Find(r.Context(), id)
	if err != nil {
		return err
	}
	if _, err := c.SignIn.StartSession(w, r, u); err != nil {
		return err
	}
	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

func page(r *http.Request) layout.Page {
	return layout.SetupStep(r, "First-run setup · Mission Control", 1)
}

// connected is whether Cloudflare is: then step 2 is closed.
func (c Controller) connected(w http.ResponseWriter, r *http.Request) (models.Installation, bool, error) {
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return inst, false, err
	}
	if inst.CloudflareConnectedAt.Valid {
		http.Redirect(w, r, "/", http.StatusFound)
		return inst, true, nil
	}
	return inst, false, nil
}

// ShowCloudflare is GET /setup/cloudflare: step 2.
func (c Controller) ShowCloudflare(w http.ResponseWriter, r *http.Request) error {
	inst, closed, err := c.connected(w, r)
	if closed || err != nil {
		return err
	}
	return web.Render(w, r, http.StatusOK, CloudflarePage(cloudflarePage(r), cfsetup.Form{BaseDomain: inst.BaseDomain}, nil, nil))
}

// ConnectCloudflare is POST /setup/cloudflare: the token checked, the
// tunnel made, and on to storage.
func (c Controller) ConnectCloudflare(w http.ResponseWriter, r *http.Request) error {
	if _, closed, err := c.connected(w, r); closed || err != nil {
		return err
	}
	form := cfsetup.Form{BaseDomain: r.PostFormValue("cloudflare[base_domain]"), APIToken: r.PostFormValue("cloudflare[api_token]")}
	ok, checks, err := c.Cloudflare.Save(r.Context(), form, time.Now())
	var invalid web.Invalid
	switch {
	case errors.As(err, &invalid), err == nil && !ok:
		return web.Render(w, r, http.StatusUnprocessableEntity, CloudflarePage(cloudflarePage(r), form.Normal(), checks, invalid))
	case err != nil:
		return err
	}
	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

func cloudflarePage(r *http.Request) layout.Page {
	return layout.SetupStep(r, "Connect Cloudflare · Mission Control", 2)
}

// StorageStep is step 3's filter: Cloudflare first, closed once a default
// storage is ready, and never kept by the browser (it shows the restic
// password).
func (c Controller) StorageStep(w http.ResponseWriter, r *http.Request) error {
	q := models.New(c.DB.Read)
	switch connected, err := q.InstallationConnected(r.Context()); {
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	case !connected:
		http.Redirect(w, r, "/setup/cloudflare", http.StatusFound)
		return nil
	}
	switch ready, err := q.DefaultStorageReady(r.Context()); {
	case err != nil:
		return err
	case ready:
		http.Redirect(w, r, "/", http.StatusFound)
		return nil
	}
	w.Header().Set("Cache-Control", "no-store")
	return nil
}

// candidate is the location step 3 made, nil before it's made.
func (c Controller) candidate(r *http.Request) (*models.StorageLocation, error) {
	l, err := models.New(c.DB.Read).SetupCandidate(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &l, err
}

// ShowStorage is GET /setup/storage: step 3, the form, then the location
// made with its password to save.
func (c Controller) ShowStorage(w http.ResponseWriter, r *http.Request) error {
	l, err := c.candidate(r)
	if err != nil {
		return err
	}
	return web.Render(w, r, http.StatusOK, StoragePage(storagePage(r), l, storage.Setup{Kind: "nfs"}, storage.Saved{}, false))
}

// CreateStorage is POST /setup/storage: the location tested (restic
// writes there) and saved.
func (c Controller) CreateStorage(w http.ResponseWriter, r *http.Request) error {
	form := settings.StorageForm(r)
	saved, err := form.Save(r.Context(), c.DB, c.Docker, time.Now())
	if err != nil {
		return err
	}
	if !saved.OK() {
		return web.Render(w, r, http.StatusUnprocessableEntity, StoragePage(storagePage(r), nil, form, saved, false))
	}
	http.Redirect(w, r, "/setup/storage", http.StatusFound)
	return nil
}

// FinishStorage is POST /setup/storage/finish {saved}: the password saved,
// the location the default, and setup done.
func (c Controller) FinishStorage(w http.ResponseWriter, r *http.Request) error {
	l, err := c.candidate(r)
	if err != nil {
		return err
	}
	if l == nil {
		w.WriteHeader(http.StatusNotFound)
		return nil
	}
	if r.PostFormValue("saved") != "1" {
		return web.Render(w, r, http.StatusUnprocessableEntity, StoragePage(storagePage(r), l, storage.Setup{Kind: l.Kind}, storage.Saved{}, true))
	}
	err = models.New(c.DB.Write).FinishStorageSetup(r.Context(), models.FinishStorageSetupParams{ID: l.ID, Now: sql.NullTime{Time: time.Now(), Valid: true}})
	if err != nil {
		return err
	}
	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

// StoragePassword is GET /setup/storage/password.txt: the location's
// restic password, to keep.
func (c Controller) StoragePassword(w http.ResponseWriter, r *http.Request) error {
	l, err := c.candidate(r)
	if err != nil {
		return err
	}
	if l == nil {
		w.WriteHeader(http.StatusNotFound)
		return nil
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", `attachment; filename="houston-`+l.Name+`-restic-password.txt"`)
	_, err = w.Write([]byte(l.ResticPassword.Reveal() + "\n"))
	return err
}

func storagePage(r *http.Request) layout.Page {
	return layout.SetupStep(r, "Default backup storage · Mission Control", 3)
}

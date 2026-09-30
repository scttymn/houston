// Package setup is first run (the Rails app's SetupController and
// Setup::*): the admin made with the installer's code, then Cloudflare,
// then storage. Open only until each is done.
package setup

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/scttymn/gantry/auth"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/shared/layout"
)

// Controller is setup's pages.
type Controller struct {
	DB     *db.DB
	SignIn *auth.Auth
	Limits *web.Limits
}

// NextStep is where a signed-in admin goes until setup is done: "" once
// it is.
func NextStep(r *http.Request, d *db.DB) (string, error) {
	q := models.New(d.Read)
	inst, err := q.CurrentInstallation(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if !inst.CloudflareConnectedAt.Valid {
		return "/setup/cloudflare", nil
	}
	switch ready, err := q.DefaultStorageReady(r.Context()); {
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

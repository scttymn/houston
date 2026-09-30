package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/scttymn/gantry/auth"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/sessions"
	"github.com/scttymn/houston/mission-control-go/app/setup"
	"github.com/scttymn/houston/mission-control-go/app/shared/layout"
)

// Sessions end after 2 weeks unused, or 30 days after signing in.
const (
	SessionIdle     = 14 * 24 * time.Hour
	SessionLifetime = 30 * 24 * time.Hour
)

// Sign-in failures from every address together through the tunnel, a
// window: so many addresses can't guess without limit. The server itself
// (ssh -L to the port) is never capped this way, so the admin can't be
// locked out.
const (
	failuresEveryone = 50
	failuresWindow   = 3 * time.Minute
)

// throughTunnel is a request that came through Cloudflare, which adds
// Cf-Ray.
func throughTunnel(r *http.Request) bool { return r.Header.Get("Cf-Ray") != "" }

// signIn is sign-in (Rails' Authentication concern and SessionsController):
// gantry's auth, its sessions bound to how they were made, so a cookie
// from plain HTTP on the network can't be replayed at admin.<base>.
func (a *App) signIn() *auth.Auth {
	return &auth.Auth{DB: a.DB, Signer: a.Signer, Log: a.Log, Limits: &a.Limits, Cookie: "session_id",
		Paths: auth.Paths{Login: "/sign-in", Logout: "/session", AfterLogin: "/"},
		Views: auth.Views{Login: func(w http.ResponseWriter, r *http.Request, p auth.Page) error {
			return web.Render(w, r, http.StatusOK, sessions.New(p, ""))
		}},
		Idle: SessionIdle, Lifetime: SessionLifetime,
		Bind: func(r *http.Request) string {
			if throughTunnel(r) {
				return "tunnel"
			}
			return "direct"
		},
		Attempt: func(r *http.Request) bool {
			return !throughTunnel(r) || a.Limits.Count("sign-in:failed", time.Now()) < failuresEveryone
		},
		Failed: func(r *http.Request, _ string) {
			if throughTunnel(r) {
				a.Limits.Allow("sign-in:failed", failuresEveryone, failuresWindow, time.Now())
			}
		},
	}
}

// firstRun sends every page, sign-in's too, to first-run setup until the
// admin exists (the Rails app's require_setup).
func (a *App) firstRun(w http.ResponseWriter, r *http.Request) error {
	set, err := models.New(a.DB.Read).UserExists(r.Context())
	if err != nil || set {
		return err
	}
	http.Redirect(w, r, "/setup", http.StatusFound)
	return nil
}

// nextStep takes the signed-in admin to setup's next unfinished step.
func (a *App) nextStep(w http.ResponseWriter, r *http.Request) error {
	step, err := setup.NextStep(r, a.DB)
	if err != nil || step == "" {
		return err
	}
	http.Redirect(w, r, step, http.StatusFound)
	return nil
}

// page is every page's filter after sign-in: times in the installation's
// zone, and the top bar's contents.
func (a *App) page(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := models.New(a.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) { // none before setup
		return err
	}
	web.SetZone(r, inst.Zone())
	web.Set(r, layout.ChromeKey, a.chrome(ctx, r, inst))
	return nil
}

// chrome is the top bar: the systems' status once setup is done.
func (a *App) chrome(ctx context.Context, r *http.Request, inst models.Installation) layout.Chrome {
	c := layout.Chrome{SignedIn: true, Version: a.Version}
	q := models.New(a.DB.Read)
	ready, err := q.DefaultStorageReady(ctx)
	if err != nil || !inst.CloudflareConnectedAt.Valid || !ready {
		return c
	}
	now := time.Now()
	s := &layout.Status{Tunnel: a.SystemStatus.Tunnel(ctx, inst, now).Go(), Registry: a.SystemStatus.RegistryUp(ctx, now),
		Clock: web.Local(r, now).Format("15:04 MST")}
	if a.Runners > 0 {
		live, _ := q.LiveRunners(ctx, now.Add(-time.Minute))
		s.Runners, s.Expected = int(live), a.Runners
	}
	c.Status = s
	return c
}

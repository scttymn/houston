package app

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/netip"
	"strings"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/projects"
)

// proxies is who may say where a request comes from: cloudflared alone
// (TunnelHost, its container's name), whose Cf-Connecting-Ip is the visitor
// and whose X-Forwarded-Proto the scheme. Anyone else's forwarding headers
// are removed, and so is a forwarded host even from the tunnel: Cloudflare
// passes a visitor's own on. (The Rails app's ForwardedHeaders.)
func (a *App) proxies() web.Proxies {
	p := web.Proxies{Trusted: []netip.Prefix{}, ClientIP: "Cf-Connecting-Ip"}
	if a.TunnelHost != "" {
		p.Names = []string{a.TunnelHost}
	}
	return p
}

// hooksHost is a request for hooks.<base>, once setup has set the base
// domain: the tunnel sends it any one-segment path, and only the webhook and
// the ping answer there (the Rails app's HooksHost).
func (a *App) hooksHost(r *http.Request) bool {
	base, err := models.New(a.DB.Read).InstallationBaseDomain(r.Context())
	return err == nil && web.RequestHost(r) == "hooks."+strings.ToLower(base)
}

// appHost is a request for one of a project's hostnames (the Rails app's
// AppHost).
func (a *App) appHost(r *http.Request) bool {
	p, err := projects.ForHost(r.Context(), models.New(a.DB.Read), strings.ToLower(web.RequestHost(r)))
	return err == nil && p != nil
}

// Identity names this installation on /ping, so Mission Control can tell
// its own answer from another server's: derived one way from
// SECRET_KEY_BASE as the Rails app derives it
// (Rails.application.key_generator.generate_key("houston/identity", 16)),
// so it's the same after the switch.
func Identity(secretKeyBase string) string {
	key, err := pbkdf2.Key(sha256.New, secretKeyBase, []byte("houston/identity"), 1000, 16)
	if err != nil {
		panic(err) // only for a length FIPS mode refuses; 16 isn't one
	}
	return hex.EncodeToString(key)
}

// ping answers GET /ping (admin.<base>/ping tells Mission Control whether
// Cloudflare routes that name here yet): the identity, as plain text.
func (a *App) ping(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, err := io.WriteString(w, a.Identity)
	return err
}

// notFound is an empty 404: on hooks.<base>, for anything that isn't a
// verified webhook, the same whether the project exists or not.
func notFound(w http.ResponseWriter, r *http.Request) error {
	w.WriteHeader(http.StatusNotFound)
	return nil
}

// Package app is the app: every route, and the controllers behind them,
// built from what they depend on.
package app

import (
	"log/slog"
	"net/http"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/jobs"
	"github.com/scttymn/gantry/live"
	"github.com/scttymn/gantry/sign"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/internal/docker"
	"github.com/scttymn/houston/mission-control-go/app/api"
	"github.com/scttymn/houston/mission-control-go/app/home"
	"github.com/scttymn/houston/mission-control-go/app/services/dns"
	"github.com/scttymn/houston/mission-control-go/assets"
)

// App is what the controllers share.
type App struct {
	DB     *db.DB
	Log    *slog.Logger
	Signer sign.Signer
	Jobs   *jobs.Queue // the background jobs, defined in app/jobs.go
	// Live sends pages their changes as they happen: a page draws
	// @a.Live.Source("post:" + id) to listen, and a controller or job calls
	// a.Live.Broadcast(ctx, "post:"+id, turbo.Replace(...)), or
	// a.Live.Refresh("post:"+id, turbo.RequestID(r)).
	Live *live.Hub
	// Identity names this installation on /ping (Identity(SECRET_KEY_BASE)).
	Identity string
	// RunnerToken is HOUSTON_RUNNER_TOKEN, the runner API's bearer token.
	RunnerToken string
	// Cloudflare is its API's address: "" is Cloudflare's own (a test's fake).
	Cloudflare string
	// Services are where the tunnel sends what it routes: Mission Control,
	// and kamal-proxy.
	Services dns.Services
	// Docker is the docker CLI; Tools, the image that makes volumes'
	// directories (HOUSTON_TOOLS_IMAGE).
	Docker docker.Runner
	Tools  string
	// TunnelHost is cloudflared's name on the Docker network, the one proxy
	// trusted (proxies); "" trusts none.
	TunnelHost string
}

// Handler is every route, in gantry's middleware.
func (a *App) Handler() http.Handler { return a.Router().Handler() }

// Router is every route, before Handler puts gantry's middleware around
// them (a test may add one of its own).
func (a *App) Router() *web.Router {
	rt := web.NewRouter(a.Log, nil)
	rt.Proxies = a.proxies()
	// Errors are assets/public's pages (404.html, 500.html ...), plain
	// files, so they show even when the app can't draw a page.
	rt.Public = assets.All

	// hooks.<base>: the webhook and the ping, else an empty 404.
	rt.Constraint(a.hooksHost, func(s *web.Scope) {
		s.Handle("GET /ping", a.ping)
		s.Handle("POST /{name}", notFound) // the webhook comes with the runner API (batch 1)
		s.Handle("/", notFound)
	})

	rt.Handle("GET /ping", a.ping)

	// The runner API: houston deploy on the server, and the runners.
	runner := api.Controller{DB: a.DB, Log: a.Log, Cloudflare: a.Cloudflare, Services: a.Services, Docker: a.Docker, Tools: a.Tools}
	rt.Scope("/api", api.Door{DB: a.DB, RunnerToken: a.RunnerToken}.Pipeline(), func(s *web.Scope) {
		s.Handle("POST /projects/sync", runner.Sync)
		s.Handle("GET /projects/{name}/secrets/{key}", runner.Secret)
	})

	homePage := home.Controller{DB: a.DB}
	rt.Handle("GET /{$}", homePage.Show)

	// The pages' live streams: put it behind the filters that decide who
	// may listen, as the pages it serves are.
	rt.Handle("GET /live", a.Live.Serve)

	assets.Routes(rt.Mount)
	return rt
}

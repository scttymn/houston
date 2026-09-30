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

	"github.com/scttymn/houston/mission-control-go/app/api"
	"github.com/scttymn/houston/mission-control-go/app/home"
	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
	"github.com/scttymn/houston/mission-control-go/app/services/dns"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
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
	// Version is which Houston this is (HoustonVersion).
	Version string
	// Cloudflare is its API's address: "" is Cloudflare's own (a test's fake).
	Cloudflare string
	// Services are where the tunnel sends what it routes: Mission Control,
	// and kamal-proxy.
	Services dns.Services
	// DockerCLI is the docker CLI; Tools, Mission Control's own image for
	// helper containers (HOUSTON_TOOLS_IMAGE), its binary at ToolsBin.
	DockerCLI dockercmd.Downloader
	// Snapshots lists projects' snapshots from restic, cached a while.
	Snapshots *backup.Snapshots
	Tools     string
	ToolsBin  string
	// KnownHosts is the file of git hosts' keys Mission Control recorded.
	KnownHosts string
	// Backup runs a backup or a restore's data; Check looks at a project's
	// repo for changes (app/jobs.go).
	Backup   *jobs.Job[models.BackupArgs]
	Snapshot *jobs.Job[models.BackupArgs] // a backup a deploy waits on
	Check    *jobs.Job[models.CheckArgs]
	// Limits counts requests for rate limits, in this process.
	Limits web.Limits
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

	// The runner API: houston deploy on the server, and the runners.
	runner := api.Controller{DB: a.DB, Log: a.Log, Cloudflare: a.Cloudflare, Services: a.Services, Tools: a.Tools, Live: a.Live, KnownHosts: a.KnownHosts, Backup: a.Backup,
		Snapshot: a.Snapshot, Check: a.Check, Limits: &a.Limits, DockerCLI: a.DockerCLI, SnapshotList: a.Snapshots}

	// hooks.<base>: the webhook and the ping, else an empty 404.
	rt.Constraint(a.hooksHost, func(s *web.Scope) {
		s.Handle("GET /ping", a.ping)
		s.Handle("POST /{name}", runner.Webhook)
		s.Handle("/", notFound)
	})

	rt.Handle("GET /ping", a.ping)

	rt.Scope("/api", api.Door{DB: a.DB, RunnerToken: a.RunnerToken}.Pipeline(), func(s *web.Scope) {
		s.Handle("POST /projects/sync", runner.Sync)
		s.Handle("GET /projects/{name}/secrets/{key}", runner.Secret)
		s.Handle("POST /projects/{name}/deploys", runner.StartDeploy)
		s.Handle("PATCH /deploys/{id}", runner.Report)
		s.Handle("POST /runner/jobs/claim", runner.Claim)
		s.Handle("POST /deploys/{id}/snapshot", runner.RequestSnapshot)
		s.Handle("GET /deploys/{id}/snapshot", runner.ShowSnapshot)
		s.Handle("POST /deploys/{id}/restore_data", runner.RequestRestoreData)
		s.Handle("GET /deploys/{id}/restore_data", runner.ShowRestoreData)
	})

	// The personal API: the houston CLI with --server, and agents.
	remote := api.V1{Controller: runner, Version: a.Version}
	rt.Scope("/api/v1", api.V1Door{DB: a.DB}.Pipeline(), func(s *web.Scope) {
		s.Handle("GET /me", remote.Me)
		s.Handle("GET /settings", remote.Settings)
		s.Handle("GET /storage", remote.Storage)
		s.Handle("GET /projects", remote.Projects)
		s.Handle("GET /projects/{name}", remote.Project)
		s.Handle("GET /projects/{name}/deploys", remote.Deploys)
		s.Handle("GET /projects/{name}/deploys/{number}", remote.Deploy)
		s.Handle("GET /projects/{name}/backups/{id}", remote.ShowBackup)
		s.Handle("GET /projects/{name}/volumes", remote.Volumes)
		s.Handle("GET /projects/{name}/webhook", remote.Webhook)
		s.Handle("GET /projects/{name}/secrets", remote.Secrets)
		for _, method := range []string{"PATCH", "PUT"} { // Rails' update is both
			s.Handle(method+" /settings", remote.UpdateSettings)
			s.Handle(method+" /storage/{location}", remote.DefaultStorage)
			s.Handle(method+" /projects/{name}/secrets/{key}", remote.SetSecret)
			s.Handle(method+" /projects/{name}/maintenance", remote.Maintenance)
			s.Handle(method+" /projects/{name}/volumes/{volume}", remote.ChooseVolume)
			s.Handle(method+" /projects/{name}/backup_target", remote.BackupTarget)
		}
		s.Handle("POST /projects/{name}/secrets/{key}/generate", remote.GenerateSecret)
		s.Handle("DELETE /projects/{name}/secrets/{key}", remote.RemoveSecret)
		s.Handle("POST /projects/{name}/webhook/rotate", remote.RotateWebhook)
		s.Handle("POST /projects/{name}/backups", remote.BackupNow)
		s.Handle("GET /projects/{name}/snapshots", remote.Snapshots)
		s.Handle("GET /projects/{name}/snapshots/{id}/download", remote.DownloadSnapshot)
	})

	homePage := home.Controller{DB: a.DB}
	rt.Handle("GET /{$}", homePage.Show)

	// The pages' live streams: put it behind the filters that decide who
	// may listen, as the pages it serves are.
	rt.Handle("GET /live", a.Live.Serve)

	assets.Routes(rt.Mount)
	return rt
}

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
	"github.com/scttymn/houston/mission-control-go/app/deploys"
	"github.com/scttymn/houston/mission-control-go/app/links"
	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/projects"
	"github.com/scttymn/houston/mission-control-go/app/services/appstats"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
	"github.com/scttymn/houston/mission-control-go/app/services/cfsettings"
	"github.com/scttymn/houston/mission-control-go/app/services/dns"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
	"github.com/scttymn/houston/mission-control-go/app/services/maintenance"
	"github.com/scttymn/houston/mission-control-go/app/services/port"
	"github.com/scttymn/houston/mission-control-go/app/services/registry"
	"github.com/scttymn/houston/mission-control-go/app/services/release"
	"github.com/scttymn/houston/mission-control-go/app/services/serverupdate"
	"github.com/scttymn/houston/mission-control-go/app/services/systemstatus"
	"github.com/scttymn/houston/mission-control-go/app/settings"
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
	Poll     *jobs.Job[struct{}]
	// The recurring upkeep: the backup schedule, prune, the latest release.
	Schedule, Prune, LatestRelease *jobs.Job[struct{}]
	// Release asks GitHub for Houston's latest release.
	Release release.Checker
	// Delete carries out a project's deletion; CleanRegistry frees the
	// registry's space after one.
	Delete, CleanRegistry *jobs.Job[models.DeletionArgs]
	// CopyCleanUp removes a failed copy's new project.
	CopyCleanUp *jobs.Job[models.CopyArgs]
	// Updater updates the server to a release, and ServerUpdate settles
	// and follows it; Port opens port 3000 or closes it.
	Updater      serverupdate.Updater
	ServerUpdate *jobs.Job[models.UpdateArgs]
	Port         *port.Port
	// Stats are the apps' CPU, memory and disk.
	Stats *appstats.Stats
	// CloudflareSettings is Cloudflare in Settings: its view, token, repair.
	CloudflareSettings *cfsettings.Settings
	// SystemStatus is the top bar's: the tunnel and the registry.
	SystemStatus *systemstatus.Status
	// Runners is how many runners the installer keeps (HOUSTON_RUNNERS).
	Runners int
	// Registry is Houston's image registry; KamalHome, where Kamal keeps
	// its files on the host (HOUSTON_KAMAL_HOME).
	Registry  registry.Registry
	KamalHome string
	// Git reads projects' repos.
	Git gitremote.Git
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
		Snapshot: a.Snapshot, Check: a.Check, Limits: &a.Limits, DockerCLI: a.DockerCLI, SnapshotList: a.Snapshots, Refs: a.Refs, Git: a.Git,
		Delete: a.Delete, CopyCleanUp: a.CopyCleanUp, Updater: a.Updater, FollowUpdate: a.ServerUpdate, Port: a.Port, Release: a.Release, Stats: a.Stats,
		CloudflareSettings: a.CloudflareSettings, SystemStatus: a.SystemStatus}

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
		s.Handle("POST /deploys/{id}/copy_data", runner.CreateCopyData)
		s.Handle("GET /deploys/{id}/copy_data", runner.ShowCopyData)
		s.Handle("POST /deploys/{id}/handover", runner.Handover)
	})

	// The personal API: the houston CLI with --server, and agents.
	remote := api.V1{Controller: runner, Version: a.Version}
	rt.Scope("/api/v1", api.V1Door{DB: a.DB}.Pipeline(), func(s *web.Scope) {
		s.Handle("GET /me", remote.Me)
		s.Handle("GET /settings", remote.Settings)
		s.Handle("GET /update", remote.Update)
		s.Handle("POST /update", remote.StartUpdate)
		s.Handle("POST /update/check", remote.CheckRelease)
		s.Handle("GET /port", remote.ShowPort)
		s.Handle("GET /cloudflare", remote.CloudflareView)
		s.Handle("PUT /cloudflare/token", remote.CloudflareToken)
		s.Handle("POST /cloudflare/repair", remote.RepairCloudflare)
		s.Handle("GET /storage", remote.Storage)
		s.Handle("GET /projects", remote.Projects)
		s.Handle("GET /projects/{name}", remote.Project)
		s.Handle("DELETE /projects/{name}", remote.DeleteProject)
		s.Handle("GET /deletions/{id}", remote.Deletion)
		s.Handle("GET /projects/{name}/deploys", remote.Deploys)
		s.Handle("POST /projects/{name}/deploys", remote.DeployNow)
		s.Handle("POST /projects/{name}/restores", remote.RequestRestore)
		s.Handle("POST /links", remote.StartLink)
		s.Handle("POST /links/{id}/access", remote.LinkAccess)
		s.Handle("POST /links/{id}/read", remote.ReadLink)
		s.Handle("POST /links/{id}/save", remote.SaveLink)
		s.Handle("GET /projects/{name}/deploys/{number}", remote.Deploy)
		s.Handle("GET /projects/{name}/backups/{id}", remote.ShowBackup)
		s.Handle("GET /projects/{name}/volumes", remote.Volumes)
		s.Handle("GET /projects/{name}/webhook", remote.Webhook)
		s.Handle("GET /projects/{name}/secrets", remote.Secrets)
		for _, method := range []string{"PATCH", "PUT"} { // Rails' update is both
			s.Handle(method+" /settings", remote.UpdateSettings)
			s.Handle(method+" /port", remote.SetPort)
			s.Handle(method+" /storage/{location}", remote.DefaultStorage)
			s.Handle(method+" /projects/{name}/secrets/{key}", remote.SetSecret)
			s.Handle(method+" /projects/{name}/maintenance", remote.Maintenance)
			s.Handle(method+" /projects/{name}/volumes/{volume}", remote.ChooseVolume)
			s.Handle(method+" /projects/{name}/backup_target", remote.BackupTarget)
			s.Handle(method+" /projects/{name}/repo", remote.MoveRepo)
		}
		s.Handle("POST /projects/{name}/secrets/{key}/generate", remote.GenerateSecret)
		s.Handle("DELETE /projects/{name}/secrets/{key}", remote.RemoveSecret)
		s.Handle("POST /projects/{name}/webhook/rotate", remote.RotateWebhook)
		s.Handle("POST /projects/{name}/backups", remote.BackupNow)
		s.Handle("POST /projects/{name}/copy", remote.RequestCopy)
		s.Handle("POST /projects/{name}/copy/cancel", remote.CancelCopy)
		s.Handle("POST /projects/{name}/copy/undo", remote.UndoCopy)
		s.Handle("GET /projects/{name}/logs", remote.Logs)
		s.Handle("GET /projects/{name}/snapshots", remote.Snapshots)
		s.Handle("GET /projects/{name}/snapshots/{id}/download", remote.DownloadSnapshot)
	})

	// Sign-in, then every page behind it.
	signIn := a.signIn()
	signIn.Routes(rt)
	rt.Handle("GET /session/new", func(w http.ResponseWriter, r *http.Request) error {
		http.Redirect(w, r, "/sign-in", http.StatusMovedPermanently)
		return nil
	})
	pages := projects.Controller{DB: a.DB, Live: a.Live, Signer: a.Signer, Stats: a.Stats, Status: a.SystemStatus, Version: a.Version,
		Refs: a.Refs, Git: a.Git, Backup: a.Backup,
		Maintenance: maintenance.Switch{DB: a.DB, Services: a.Services, Cloudflare: a.Cloudflare},
		Snapshots:   a.Snapshots, Docker: a.DockerCLI, Cloudflare: a.Cloudflare, Delete: a.Delete, CopyCleanUp: a.CopyCleanUp, Log: a.Log}
	settingsPages := settings.Controller{DB: a.DB, Signer: a.Signer, Live: a.Live, Board: projects.FlightBoard, Version: a.Version, Docker: a.DockerCLI,
		Status: a.SystemStatus, Cloudflare: a.CloudflareSettings, Port: a.Port, Release: a.Release, Updater: a.Updater, Follow: a.ServerUpdate,
		MissionControl: a.Services.MissionControl}
	addProject := links.Controller{DB: a.DB, Signer: a.Signer, Git: a.Git, Live: a.Live, Refs: a.Refs, Board: projects.FlightBoard}
	deployPages := deploys.Controller{DB: a.DB, Live: a.Live, Signer: a.Signer}
	rt.Scope("", web.Pipeline{signIn.Required, a.page}, func(s *web.Scope) {
		s.Handle("GET /{$}", pages.Index)
		s.Handle("GET /resources", pages.Resources)
		s.Handle("GET /projects/{name}", pages.ShowProject)
		s.Handle("GET /projects/{name}/deploys/{number}", deployPages.Show)
		s.Handle("POST /projects/{name}/check", pages.Check)
		s.Handle("POST /projects/{name}/rotate_webhook", pages.RotateWebhook)
		s.Handle("POST /projects/{name}/deploys", pages.Deploy)
		s.Handle("POST /projects/{name}/backups", pages.BackUp)
		s.Handle("PATCH /projects/{name}/volumes/{volume}", pages.ChooseVolume)
		s.Handle("PATCH /projects/{name}/backup_target", pages.BackupTarget)
		s.Handle("PATCH /projects/{name}/repo", pages.MoveRepo)
		s.Handle("PATCH /projects/{name}/maintenance", pages.ToggleMaintenance)
		s.Handle("PUT /projects/{name}/secrets/{key}", pages.SetSecret)
		s.Handle("PATCH /projects/{name}/secrets/{key}", pages.SetSecret)
		s.Handle("DELETE /projects/{name}/secrets/{key}", pages.RemoveSecret)
		s.Handle("POST /projects/{name}/secrets/{key}/generate", pages.GenerateSecret)
		s.Handle("GET /projects/{name}/snapshots", pages.ShowSnapshots)
		s.Handle("GET /projects/{name}/snapshots/{id}/download", pages.DownloadSnapshot)
		s.Handle("GET /projects/{name}/restores/new", pages.NewRestore)
		s.Handle("POST /projects/{name}/restores", pages.CreateRestore)
		s.Handle("GET /projects/{name}/copy/new", pages.NewCopy)
		s.Handle("POST /projects/{name}/copy", pages.CreateCopy)
		s.Handle("POST /projects/{name}/copy/cancel", pages.CancelCopy)
		s.Handle("POST /projects/{name}/copy/undo", pages.UndoCopy)
		s.Handle("GET /projects/{name}/deletion/new", pages.NewDeletion)
		s.Handle("POST /projects/{name}/deletion", pages.CreateDeletion)
		s.Handle("GET /deletions/{id}", pages.ShowDeletion)
		s.Handle("GET /settings", settingsPages.Show)
		s.Handle("GET /settings/general", settings.General)
		s.Handle("PATCH /settings/general", settingsPages.SetTimeZone)
		s.Handle("GET /settings/tokens", settings.Tokens)
		s.Handle("POST /settings/tokens", settingsPages.NewToken)
		s.Handle("DELETE /settings/tokens/{id}", settingsPages.RevokeToken)
		s.Handle("GET /settings/cloudflare", settingsPages.CloudflareLive)
		s.Handle("PATCH /settings/cloudflare/token", settingsPages.CloudflareToken)
		s.Handle("POST /settings/cloudflare/repair", settingsPages.RepairCloudflare)
		s.Handle("PATCH /settings/port", settingsPages.SetPort)
		s.Handle("POST /settings/updates/check", settingsPages.CheckRelease)
		s.Handle("POST /settings/updates", settingsPages.StartUpdate)
		s.Handle("GET /settings/updates/{id}", settingsPages.ShowUpdate)
		s.Handle("GET /settings/storage", settings.Storage)
		s.Handle("GET /settings/storage/new", settingsPages.NewStorage)
		s.Handle("POST /settings/storage", settingsPages.CreateStorage)
		s.Handle("GET /settings/storage/{name}", settingsPages.ShowStorage)
		s.Handle("GET /settings/storage/{name}/password.txt", settingsPages.StoragePassword)
		s.Handle("POST /settings/storage/{name}/acknowledge", settingsPages.AcknowledgeStorage)
		s.Handle("POST /settings/storage/{name}/default", settingsPages.MakeDefault)
		s.Handle("GET /link", addProject.New)
		s.Handle("POST /link/access", addProject.Access)
		s.Handle("POST /link/read", addProject.Read)
		s.Handle("POST /link", addProject.Create)
		s.Handle("DELETE /link", addProject.Cancel)
	})

	// The pages' live streams, for whoever may see the pages.
	rt.Scope("", web.Pipeline{signIn.Required}, func(s *web.Scope) {
		s.Handle("GET /live", a.Live.Serve)
	})

	assets.Routes(rt.Mount)
	return rt
}

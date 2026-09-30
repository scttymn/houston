package api

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/live"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
	"github.com/scttymn/houston/mission-control-go/app/services/dns"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
	"github.com/scttymn/houston/mission-control-go/app/services/volumes"
)

// Controller is the runner API's endpoints.
type Controller struct {
	DB  *db.DB
	Log *slog.Logger
	// Cloudflare is its API's address: "" is Cloudflare's own.
	Cloudflare string
	// Services are where the tunnel sends what it routes.
	Services dns.Services
	// Tools is HOUSTON_TOOLS_IMAGE, for making volumes' directories.
	Tools string
	// Live tells open pages what changed.
	Live *live.Hub
	// KnownHosts is the file of git hosts' keys (HOUSTON_KNOWN_HOSTS).
	KnownHosts string
	// Backup and Snapshot are the backup job on its queues: backups, and
	// snapshots for the runs a deploy waits on.
	Backup, Snapshot models.Enqueuer[models.BackupArgs]
	// Check is the change check, which a verified webhook queues.
	Check interface {
		Enqueue(ctx context.Context, a models.CheckArgs) (int64, error)
	}
	// Limits counts requests for rate limits.
	Limits *web.Limits
	// DockerCLI runs docker for the work that needs its environment,
	// input, deadlines and downloads; SnapshotList lists (and caches)
	// projects' snapshots.
	DockerCLI    dockercmd.Downloader
	SnapshotList *backup.Snapshots
	// Refs reads a project's repo's refs; Git, anything else of a repo.
	Refs models.RefReader
	Git  gitremote.Git
}

func (c Controller) placement() volumes.Placement {
	return volumes.Placement{DB: c.DB, Docker: c.DockerCLI, Tools: c.Tools}
}

func sqlNumber(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }

func sqlTime(t time.Time) sql.NullTime { return sql.NullTime{Time: t, Valid: true} }

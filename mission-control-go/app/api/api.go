package api

import (
	"log/slog"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/live"

	"github.com/scttymn/houston/internal/docker"
	"github.com/scttymn/houston/mission-control-go/app/services/dns"
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
	Docker   docker.Runner
	// Tools is HOUSTON_TOOLS_IMAGE, for making volumes' directories.
	Tools string
	// Live tells open pages what changed.
	Live *live.Hub
}

func (c Controller) placement() volumes.Placement {
	return volumes.Placement{DB: c.DB, Docker: c.Docker, Tools: c.Tools}
}

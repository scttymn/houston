// Package maintenance is Houston's maintenance page switch for a project
// (the Rails app's Maintenance): on routes the project's hostnames to
// Mission Control at the tunnel, which serves the page; off routes them
// back. The database and the tunnel always agree: a toggle Cloudflare
// refuses is rolled back. Toggles are serialized, and each push is built
// from the database after its own change, so the last push holds them all.
package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare"
	"github.com/scttymn/houston/mission-control-go/app/services/dns"
)

// Failed is a toggle that didn't happen: no tunnel, or Cloudflare said no.
type Failed string

func (f Failed) Error() string { return string(f) }

// MessageMax is how long the page's message may be.
const MessageMax = 500

// toggles serializes toggles (the Rails app's file lock; one Mission
// Control process serves them all).
var toggles sync.Mutex

// Switch turns projects' maintenance pages on and off.
type Switch struct {
	DB       *db.DB
	Services dns.Services
	// Cloudflare is its API's address ("": Cloudflare's own).
	Cloudflare string
}

// On shows p's maintenance page, by whom, with an optional message; one on
// already keeps when it began.
func (s Switch) On(ctx context.Context, p models.Project, by, message string, now time.Time) (models.Project, error) {
	if len([]rune(message)) > MessageMax {
		return p, models.Refused{Msg: "Maintenance message is too long (maximum is 500 characters)"}
	}
	return s.toggle(ctx, p, models.SetMaintenanceParams{MaintenanceSince: sql.NullTime{Time: now, Valid: true}, MaintenanceBy: by,
		MaintenanceMessage: message, UpdatedAt: now, ID: p.ID})
}

// Off sends p's hostnames to the app again.
func (s Switch) Off(ctx context.Context, p models.Project, now time.Time) (models.Project, error) {
	return s.toggle(ctx, p, models.SetMaintenanceParams{UpdatedAt: now, ID: p.ID})
}

func (s Switch) toggle(ctx context.Context, p models.Project, change models.SetMaintenanceParams) (models.Project, error) {
	q := models.New(s.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	if !inst.CloudflareConnectedAt.Valid || inst.TunnelID == "" {
		return p, Failed("Cloudflare isn't connected, so there's no tunnel to route through")
	}
	toggles.Lock()
	defer toggles.Unlock()
	if p, err = q.ProjectByID(ctx, p.ID); err != nil {
		return p, err
	}
	if change.MaintenanceSince.Valid && p.MaintenanceSince.Valid {
		change.MaintenanceSince = p.MaintenanceSince // on since it was first switched on
	}
	before := models.SetMaintenanceParams{MaintenanceSince: p.MaintenanceSince, MaintenanceBy: p.MaintenanceBy, MaintenanceMessage: p.MaintenanceMessage,
		UpdatedAt: p.UpdatedAt, ID: p.ID}
	write := models.New(s.DB.Write)
	if err := write.SetMaintenance(ctx, change); err != nil {
		return p, err
	}
	if err := (dns.DNS{Installation: inst, API: s.Cloudflare}).PushRoutes(ctx, q, s.Services); err != nil {
		if rollback := write.SetMaintenance(ctx, before); rollback != nil {
			return p, rollback
		}
		var cf *cloudflare.Error
		if errors.As(err, &cf) {
			return p, Failed("Cloudflare said no: " + cf.Msg)
		}
		return p, err
	}
	return q.ProjectByID(ctx, p.ID)
}

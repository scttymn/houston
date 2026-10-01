package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare"
	"github.com/scttymn/houston/mission_control/app/services/dns"
)

func (c Controller) dns(inst models.Installation) dns.DNS {
	return dns.DNS{Installation: inst, API: c.Cloudflare}
}

// pointDNS points <name>.<base> at the tunnel when DNS is host by host, and
// is the DNS mode. A project that has never served keeps its names where
// they are (another host may still answer them: equip's first deploy,
// NO-GO, left its name at 502 while the old copy ran): its first GO points
// them. Cloudflare saying no is a 502.
func (c Controller) pointDNS(ctx context.Context, inst models.Installation, p models.Project) (string, error) {
	if inst.DnsMode != "per_host" {
		return "wildcard", nil
	}
	served, err := models.New(c.DB.Read).ProjectHasServed(ctx, p.ID)
	if err != nil {
		return "", err
	}
	if !served {
		return "after_first_go", nil
	}
	err = c.dns(inst).PointHost(ctx, p)
	var cf *cloudflare.Error
	if errors.As(err, &cf) {
		return "", badGateway(fmt.Sprintf("Cloudflare said no while pointing %s at the tunnel: %s", p.Host(inst.BaseDomain), cf.Msg))
	}
	return "per_host", err
}

// badGateway is Cloudflare's no, answered 502 with its words (gantry keeps
// a 5xx's own words to the log; these are for the person deploying).
type badGateway string

func (e badGateway) Error() string { return string(e) }

// answerBadGateway answers err when it's a badGateway, and reports whether
// it did.
func answerBadGateway(w http.ResponseWriter, err error) (bool, error) {
	var bg badGateway
	if !errors.As(err, &bg) {
		return false, nil
	}
	return true, web.JSON(w, http.StatusBadGateway, map[string]string{"error": string(bg)})
}

// pointDomains points each custom domain, removes the records of dropped
// ones, and stores and is their states. Before its first GO, each waits.
func (c Controller) pointDomains(ctx context.Context, inst models.Installation, p models.Project, dropped []string, now time.Time) (map[string]models.DomainState, error) {
	served, err := models.New(c.DB.Read).ProjectHasServed(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	d := c.dns(inst)
	if served {
		for _, domain := range dropped {
			if err := d.RemoveDomain(ctx, p, domain); err != nil {
				c.Log.Warn("a dropped domain's record wasn't removed", "domain", domain, "err", err)
			}
		}
	}
	out := map[string]models.DomainState{}
	for _, domain := range p.Domains.V {
		if domain == p.Host(inst.BaseDomain) {
			continue
		}
		if served {
			out[domain] = d.PointDomain(ctx, p, domain)
		} else {
			out[domain] = models.DomainState{State: "AFTER FIRST GO", Reason: "pointed here once a deploy of " + p.Name + " is GO"}
		}
	}
	err = models.New(c.DB.Write).SetDomainStates(ctx, models.SetDomainStatesParams{DomainStates: models.DomainStates{V: out}, UpdatedAt: now, ID: p.ID})
	return out, err
}

// pushMaintenanceRoutes keeps every hostname of a project in maintenance
// on the page, custom domains a sync added or dropped included. Best
// effort: the deploy goes on, and the admin's next toggle pushes again.
func (c Controller) pushMaintenanceRoutes(ctx context.Context, inst models.Installation, p models.Project) {
	if !p.MaintenanceSince.Valid {
		return
	}
	if err := c.dns(inst).PushRoutes(ctx, models.New(c.DB.Read), c.Services); err != nil {
		c.Log.Warn("maintenance routes weren't updated", "project", p.Name, "err", err)
	}
}

// pointBestEffort points what a restore's kept compose.yml names, once
// it's applied: the next deploy points them again should this fail.
func (c Controller) pointBestEffort(ctx context.Context, inst models.Installation, a *models.Adopted, now time.Time) {
	if a == nil {
		return
	}
	_, err := c.pointDNS(ctx, inst, a.Project)
	if err == nil {
		_, err = c.pointDomains(ctx, inst, a.Project, a.Dropped, now)
	}
	if err != nil {
		c.Log.Warn("DNS for a restored compose.yml wasn't pointed; the next deploy points it", "project", a.Project.Name, "err", err)
	}
}

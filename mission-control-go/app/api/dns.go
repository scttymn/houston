package api

import (
	"context"
	"errors"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// errNoCloudflareYet stands where Cloudflare's calls go, until slice 1c.
var errNoCloudflareYet = errors.New("pointing names through Cloudflare isn't in the Go version yet")

// pointDNS points <name>.<base> at the tunnel when DNS is host by host, and
// is the DNS mode. A project that has never served keeps its names where
// they are (another host may still answer them): its first GO points them.
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
	return "", errNoCloudflareYet
}

// pointDomains points each custom domain, removes dropped ones' records,
// and stores and is their states.
func (c Controller) pointDomains(ctx context.Context, inst models.Installation, p models.Project, dropped []string, now time.Time) (map[string]models.DomainState, error) {
	var custom []string
	for _, d := range p.Domains.V {
		if d != p.Host(inst.BaseDomain) {
			custom = append(custom, d)
		}
	}
	served, err := models.New(c.DB.Read).ProjectHasServed(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if served && len(custom)+len(dropped) > 0 {
		return nil, errNoCloudflareYet
	}
	out := map[string]models.DomainState{}
	for _, d := range custom {
		out[d] = models.DomainState{State: "AFTER FIRST GO", Reason: "pointed here once a deploy of " + p.Name + " is GO"}
	}
	err = models.New(c.DB.Write).SetDomainStates(ctx, models.SetDomainStatesParams{DomainStates: models.DomainStates{V: out}, UpdatedAt: now, ID: p.ID})
	return out, err
}

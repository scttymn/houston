// Package dns points a project's names at the tunnel through Cloudflare:
// its <name>.<base> when DNS is host by host, its custom domains, and the
// tunnel's routes. Houston never changes a record it didn't make for that
// project.
package dns

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare"
)

// DNS is Cloudflare, as the installation connected it.
type DNS struct {
	Installation models.Installation
	// API is Cloudflare's address: cloudflare.API, or a test's fake.
	API string
}

func (d DNS) client() cloudflare.Client {
	return cloudflare.Client{Token: d.Installation.CloudflareApiToken.Reveal(), Base: d.API}
}

// comment is what marks a record as project p's.
func comment(p models.Project) string { return cloudflare.Managed + " project:" + p.Name }

// PointHost points <name>.<base> at the tunnel, host by host. A record
// there Houston didn't make is Refused: it's someone else's.
func (d DNS) PointHost(ctx context.Context, p models.Project) error {
	records := cloudflare.Records{Client: d.client(), Zone: d.Installation.CloudflareZoneID}
	name := p.Host(d.Installation.BaseDomain)
	existing, err := records.Find(ctx, name)
	if err != nil {
		return err
	}
	if existing != nil && !existing.IsManaged() {
		return models.Refused{Msg: fmt.Sprintf("NO-GO: %s already exists and Houston didn't create it (no %s comment). Remove it in Cloudflare, then deploy again.", name, cloudflare.Managed)}
	}
	return records.Point(ctx, name, d.Installation.TunnelID, existing, comment(p))
}

var owner = regexp.MustCompile(`^` + regexp.QuoteMeta(cloudflare.Managed) + ` project:(\S+)$`)

// PointDomain points one of p's custom domains at the tunnel, and is where
// it stands. A domain it can't or mustn't point only gets a state (the app
// still deploys at <name>.<base>); Cloudflare's errors are one too.
func (d DNS) PointDomain(ctx context.Context, p models.Project, domain string) models.DomainState {
	state, err := d.pointDomain(ctx, p, domain)
	var cf *cloudflare.Error
	if errors.As(err, &cf) {
		return models.DomainState{State: "CAN'T CHECK", Reason: "Cloudflare: " + cf.Msg}
	}
	if err != nil {
		return models.DomainState{State: "CAN'T CHECK", Reason: err.Error()}
	}
	return state
}

func (d DNS) pointDomain(ctx context.Context, p models.Project, domain string) (models.DomainState, error) {
	zone := &cloudflare.Zone{ID: d.Installation.CloudflareZoneID, Status: "active"}
	if d.underBase(domain) {
		if d.Installation.DnsMode != "per_host" {
			return models.DomainState{State: "WILDCARD"}, nil
		}
	} else {
		var err error
		if zone, err = d.client().ZoneOf(ctx, domain); err != nil {
			return models.DomainState{}, err
		}
		if zone == nil {
			labels := strings.Split(domain, ".")
			apex := strings.Join(labels[max(len(labels)-2, 0):], ".")
			return models.DomainState{State: "ZONE NOT IN CLOUDFLARE YET", Reason: "Add " + apex + " to Cloudflare (adding a zone is manual), then deploy again"}, nil
		}
	}
	records := cloudflare.Records{Client: d.client(), Zone: zone.ID}
	existing, err := records.Find(ctx, domain)
	if err != nil {
		return models.DomainState{}, err
	}
	if existing != nil && existing.Comment != comment(p) {
		reason := fmt.Sprintf("%s has a record Houston didn't create for %s; remove it in Cloudflare to move the domain here", domain, p.Name)
		if m := owner.FindStringSubmatch(existing.Comment); m != nil {
			reason = fmt.Sprintf("%s belongs to project %s", domain, m[1])
		}
		return models.DomainState{State: "NO-GO", Reason: reason}, nil
	}
	if err := records.Point(ctx, domain, d.Installation.TunnelID, existing, comment(p)); err != nil {
		return models.DomainState{}, err
	}
	if zone.Status != "active" {
		return models.DomainState{State: "DNS PENDING", Reason: "the zone's nameservers aren't switched to Cloudflare yet"}, nil
	}
	return models.DomainState{State: "DNS OK"}, nil
}

// RemoveDomain removes the record of a domain p no longer has, only if
// it's p's. Its error is for the log: the next sync finds what's left.
func (d DNS) RemoveDomain(ctx context.Context, p models.Project, domain string) error {
	zone := &cloudflare.Zone{ID: d.Installation.CloudflareZoneID}
	if !d.underBase(domain) {
		var err error
		if zone, err = d.client().ZoneOf(ctx, domain); err != nil || zone == nil {
			return err
		}
	}
	records := cloudflare.Records{Client: d.client(), Zone: zone.ID}
	existing, err := records.Find(ctx, domain)
	// Only its own: a record another server's Houston points at its tunnel
	// stays.
	if err != nil || existing == nil || existing.Comment != comment(p) || existing.Content != cloudflare.Target(d.Installation.TunnelID) {
		return err
	}
	return records.Delete(ctx, *existing)
}

func (d DNS) underBase(domain string) bool {
	return strings.HasSuffix(domain, "."+d.Installation.BaseDomain)
}

// Rule is one of the tunnel's ingress rules.
type Rule struct {
	Hostname string `json:"hostname,omitempty"`
	Path     string `json:"path,omitempty"`
	Service  string `json:"service"`
}

// Services are where the tunnel sends what it routes.
type Services struct {
	MissionControl string // HOUSTON_MISSION_CONTROL_URL
	Apps           string // HOUSTON_APPS_URL: kamal-proxy
}

// Rules are the tunnel's ingress, from the database: Mission Control
// answers admin.<base>, hooks.<base>'s webhook paths, and the hostnames of
// projects in maintenance (Houston's maintenance page); everything else
// goes to kamal-proxy.
func Rules(ctx context.Context, q *models.Queries, base string, to Services) ([]Rule, error) {
	projects, err := q.ProjectsInMaintenance(ctx)
	if err != nil {
		return nil, err
	}
	rules := []Rule{
		{Hostname: "admin." + base, Service: to.MissionControl},
		{Hostname: "hooks." + base, Path: "^/[a-z0-9-]+$", Service: to.MissionControl},
		{Hostname: "hooks." + base, Service: "http_status:404"},
	}
	seen := map[string]bool{}
	for _, p := range projects {
		for _, host := range p.Hostnames(base) {
			if !seen[host] {
				seen[host] = true
				rules = append(rules, Rule{Hostname: host, Service: to.MissionControl})
			}
		}
	}
	return append(rules, Rule{Service: to.Apps}), nil
}

// PushRoutes replaces the tunnel's configuration with the rules as the
// database has them now.
func (d DNS) PushRoutes(ctx context.Context, q *models.Queries, to Services) error {
	rules, err := Rules(ctx, q, d.Installation.BaseDomain, to)
	if err != nil {
		return err
	}
	path := "/accounts/" + d.Installation.CloudflareAccountID + "/cfd_tunnel/" + d.Installation.TunnelID + "/configurations"
	return d.client().Put(ctx, path, map[string]any{"config": map[string]any{"ingress": rules}}, nil)
}

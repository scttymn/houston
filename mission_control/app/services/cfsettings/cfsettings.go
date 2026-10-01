// Package cfsettings is Cloudflare in Settings: what Cloudflare has for
// this Houston, replacing its API token, and repair.
package cfsettings

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare"
	"github.com/scttymn/houston/mission_control/app/services/dns"
)

const (
	// StaleAfter is when Settings asks again, showing the last answer
	// meanwhile.
	StaleAfter = 5 * time.Minute
	timeout    = 5 * time.Second
	page       = 100
)

// Settings reads and repairs Cloudflare, keeping the last complete view
// per tunnel.
type Settings struct {
	DB *db.DB
	// API is Cloudflare's address ("": Cloudflare's own).
	API      string
	Services dns.Services

	mu   sync.Mutex
	kept map[string]View // by tunnel id
}

// View is what Cloudflare has: the tunnel and its connections, its live
// routes against the database's, and Houston's DNS records in every zone
// the token sees. Each part that fails is a problem, not an error.
type View struct {
	Tunnel        *Tunnel      `json:"tunnel"`
	Connections   []Connection `json:"connections"`
	Routes        []Route      `json:"routes"`
	MissingRoutes []Route      `json:"missing_routes"`
	Drift         bool         `json:"drift"`
	Records       []Record     `json:"records"`
	Problems      []string     `json:"problems"`
	CheckedAt     time.Time    `json:"-"`
}

type Tunnel struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	CreatedAt *time.Time `json:"created_at"`
}

type Connection struct {
	Colo    string     `json:"colo"`
	Version string     `json:"version"`
	Origin  string     `json:"origin"`
	Since   *time.Time `json:"since"`
}

// Route is a tunnel rule: live, and drift when the database hasn't it; or
// missing, the database's and not Cloudflare's.
type Route struct {
	Hostname *string `json:"hostname"`
	Path     *string `json:"path"`
	Service  string  `json:"service"`
	Drift    bool    `json:"drift"`
}

// Record is one of Houston's: its project's, or Houston's own names; here
// when it points at this server's tunnel.
type Record struct {
	Zone    string `json:"zone"`
	Name    string `json:"name"`
	Project string `json:"project"`
	Proxied bool   `json:"proxied"`
	Here    bool   `json:"here"`
}

// Stale is a view to ask again.
func (v View) Stale(now time.Time) bool {
	return v.CheckedAt.IsZero() || v.CheckedAt.Before(now.Add(-StaleAfter))
}

func (s *Settings) client(token string) cloudflare.Client {
	return cloudflare.Client{Token: token, Base: s.API, HTTP: &http.Client{Timeout: timeout}}
}

// Fetch asks Cloudflare now. A complete answer is kept (Last); one with
// problems is shown over the last good one, which stays kept.
func (s *Settings) Fetch(ctx context.Context, inst models.Installation, now time.Time) View {
	v := View{Connections: []Connection{}, Routes: []Route{}, MissingRoutes: []Route{}, Records: []Record{}, Problems: []string{}}
	c := s.client(inst.CloudflareApiToken.Reveal())
	part := func(name string, load func() error) {
		var cf *cloudflare.Error
		if err := load(); errors.As(err, &cf) {
			v.Problems = append(v.Problems, name+": "+cf.Msg)
		} else if err != nil {
			v.Problems = append(v.Problems, name+": "+err.Error())
		}
	}
	tunnelPath := "/accounts/" + inst.CloudflareAccountID + "/cfd_tunnel/" + inst.TunnelID
	part("tunnel", func() error { return s.loadTunnel(ctx, c, tunnelPath, &v) })
	part("routes", func() error { return s.loadRoutes(ctx, c, tunnelPath, inst, &v) })
	part("records", func() error { return loadRecords(ctx, c, inst, &v) })
	v.CheckedAt = now
	for _, r := range v.Routes {
		v.Drift = v.Drift || r.Drift
	}
	v.Drift = v.Drift || len(v.MissingRoutes) > 0

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kept == nil {
		s.kept = map[string]View{}
	}
	if len(v.Problems) == 0 {
		s.kept[inst.TunnelID] = v
	} else if kept, ok := s.kept[inst.TunnelID]; ok {
		// A failed check shows the parts that failed from the last good one.
		if v.Tunnel == nil {
			v.Tunnel = kept.Tunnel
		}
		if len(v.Connections) == 0 {
			v.Connections = kept.Connections
		}
		if len(v.Routes) == 0 && len(v.MissingRoutes) == 0 {
			v.Routes, v.MissingRoutes, v.Drift = kept.Routes, kept.MissingRoutes, kept.Drift
		}
		if len(v.Records) == 0 {
			v.Records = kept.Records
		}
		v.CheckedAt = kept.CheckedAt
	}
	return v
}

// Last is the last complete view of the installation's tunnel, asking
// Cloudflare nothing.
func (s *Settings) Last(inst models.Installation) (View, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.kept[inst.TunnelID]
	return v, ok
}

// Forget has the next look ask Cloudflare: the token or the routes changed.
func (s *Settings) Forget(inst models.Installation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.kept, inst.TunnelID)
}

func parseTime(value string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &t
}

func (s *Settings) loadTunnel(ctx context.Context, c cloudflare.Client, path string, v *View) error {
	var t struct {
		ID, Name, Status string
		CreatedAt        string `json:"created_at"`
		Connections      []struct {
			Colo     string `json:"colo_name"`
			Version  string `json:"client_version"`
			Origin   string `json:"origin_ip"`
			OpenedAt string `json:"opened_at"`
		}
	}
	if err := c.Get(ctx, path, nil, &t); err != nil {
		return err
	}
	v.Tunnel = &Tunnel{ID: t.ID, Name: t.Name, Status: t.Status, CreatedAt: parseTime(t.CreatedAt)}
	for _, conn := range t.Connections {
		v.Connections = append(v.Connections, Connection{Colo: strings.ToUpper(conn.Colo), Version: conn.Version, Origin: conn.Origin, Since: parseTime(conn.OpenedAt)})
	}
	return nil
}

type routeKey struct{ hostname, path, service string }

func (s *Settings) loadRoutes(ctx context.Context, c cloudflare.Client, path string, inst models.Installation, v *View) error {
	var config struct {
		Config struct {
			Ingress []dns.Rule `json:"ingress"`
		} `json:"config"`
	}
	if err := c.Get(ctx, path+"/configurations", nil, &config); err != nil {
		return err
	}
	rules, err := dns.Rules(ctx, models.New(s.DB.Read), inst.BaseDomain, s.Services)
	if err != nil {
		return err
	}
	var live, expected []routeKey
	for _, r := range config.Config.Ingress {
		live = append(live, routeKey{r.Hostname, r.Path, r.Service})
	}
	for _, r := range rules {
		expected = append(expected, routeKey{r.Hostname, r.Path, r.Service})
	}
	route := func(k routeKey, drift bool) Route {
		r := Route{Service: k.service, Drift: drift}
		if k.hostname != "" {
			r.Hostname = &k.hostname
		}
		if k.path != "" {
			r.Path = &k.path
		}
		return r
	}
	for _, k := range live {
		v.Routes = append(v.Routes, route(k, !slices.Contains(expected, k)))
	}
	for _, k := range expected {
		if !slices.Contains(live, k) {
			v.MissingRoutes = append(v.MissingRoutes, route(k, true))
		}
	}
	return nil
}

var (
	projectComment = regexp.MustCompile(`project:(\S+)`)
	houstonName    = regexp.MustCompile(`^(admin|hooks)\.`)
)

// owner is whose a record is: a project's, one of Houston's own names, the
// wildcard.
func owner(r cloudflare.Record) string {
	if m := projectComment.FindStringSubmatch(r.Comment); m != nil {
		return m[1]
	}
	if m := houstonName.FindStringSubmatch(r.Name); m != nil {
		return m[1]
	}
	if strings.HasPrefix(r.Name, "*.") {
		return "wildcard"
	}
	return "houston"
}

func loadRecords(ctx context.Context, c cloudflare.Client, inst models.Installation, v *View) error {
	var zones []cloudflare.Zone
	if err := c.Get(ctx, "/zones", url.Values{"per_page": {"50"}}, &zones); err != nil {
		return err
	}
	here := cloudflare.Target(inst.TunnelID)
	var records []Record
	for _, z := range zones {
		for n := 1; ; n++ {
			var batch []cloudflare.Record
			if err := c.Get(ctx, "/zones/"+z.ID+"/dns_records", url.Values{"per_page": {strconv.Itoa(page)}, "page": {strconv.Itoa(n)}}, &batch); err != nil {
				return err
			}
			for _, r := range batch {
				if r.IsManaged() {
					records = append(records, Record{Zone: z.Name, Name: r.Name, Project: owner(r), Proxied: r.Proxied, Here: r.Content == here})
				}
			}
			if len(batch) < page {
				break
			}
		}
	}
	slices.SortFunc(records, func(a, b Record) int {
		if c := strings.Compare(a.Zone, b.Zone); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	v.Records = append(v.Records, records...)
	return nil
}

// Check is one of a new token's checks, in words.
type Check struct {
	OK    bool   `json:"ok"`
	Label string `json:"label"`
}

// ReplaceToken replaces Houston's Cloudflare API token when the new one
// passes every check (the same account, this server's tunnel, DNS on the
// base zone and every project domain's zone): any failure keeps the old
// one. The checks are reads, so they can't prove edit rights; their labels
// say which permissions to grant.
func (s *Settings) ReplaceToken(ctx context.Context, inst models.Installation, token string, now time.Time) (bool, []Check) {
	token = strings.TrimSpace(token)
	checks := []Check{}
	fail := func(label string) { checks = append(checks, Check{Label: label}) }
	pass := func(label string) { checks = append(checks, Check{OK: true, Label: label}) }
	if token == "" {
		fail("Paste the new token's value")
		return false, checks
	}
	c := cloudflare.Client{Token: token, Base: s.API}
	cfMsg := func(err error) string {
		var cf *cloudflare.Error
		if errors.As(err, &cf) {
			return cf.Msg
		}
		return err.Error()
	}

	var accounts []struct{ ID, Name string }
	if err := c.Get(ctx, "/accounts", url.Values{"per_page": {"50"}}, &accounts); err != nil {
		var cf *cloudflare.Error
		if errors.As(err, &cf) && (cf.Status == 400 || cf.Status == 401 || cf.Status == 403) {
			fail("The token isn't valid (Cloudflare: " + cf.Msg + "). Copy the token's value (not its ID), and check it's active.")
		} else {
			fail("Cloudflare: " + cfMsg(err))
		}
		return false, checks
	}
	i := slices.IndexFunc(accounts, func(a struct{ ID, Name string }) bool { return a.ID == inst.CloudflareAccountID })
	if i < 0 {
		fail("Account: the token can't see the Cloudflare account Houston uses. Make it for that account.")
		return false, checks
	}
	pass("Account · " + accounts[i].Name)

	if err := c.Get(ctx, "/accounts/"+inst.CloudflareAccountID+"/cfd_tunnel/"+inst.TunnelID, nil, nil); err != nil {
		fail("Cloudflare Tunnel: the token needs Account · Cloudflare Tunnel · Edit (Cloudflare: " + cfMsg(err) + ").")
	} else {
		pass("Cloudflare Tunnel · this server's tunnel")
	}

	// The base domain, then the zone of each project domain outside it.
	domains := []string{inst.BaseDomain}
	projects, err := models.New(s.DB.Read).Projects(ctx)
	if err != nil {
		fail("Mission Control: " + err.Error())
		return false, checks
	}
	for _, p := range projects {
		for _, d := range p.Domains.V {
			if d != inst.BaseDomain && !strings.HasSuffix(d, "."+inst.BaseDomain) && !slices.Contains(domains, d) {
				domains = append(domains, d)
			}
		}
	}
	var checked []string
	for _, domain := range domains {
		zone, err := c.ZoneOf(ctx, domain)
		if err != nil {
			fail("Zone · DNS on " + domain + ": the token needs Zone · DNS · Edit (Cloudflare: " + cfMsg(err) + ").")
			continue
		}
		if zone == nil {
			fail("Zone · DNS for " + domain + ": the token can't see its zone. Give it Zone · DNS · Edit on it (the zone must be in this account).")
			continue
		}
		if slices.Contains(checked, zone.ID) {
			continue
		}
		checked = append(checked, zone.ID)
		if err := c.Get(ctx, "/zones/"+zone.ID+"/dns_records", url.Values{"per_page": {"1"}}, nil); err != nil {
			fail("Zone · DNS on " + zone.Name + ": the token needs Zone · DNS · Edit (Cloudflare: " + cfMsg(err) + ").")
			continue
		}
		pass("Zone · DNS on " + zone.Name)
	}
	for _, check := range checks {
		if !check.OK {
			return false, checks
		}
	}
	if err := models.New(s.DB.Write).SetCloudflareToken(ctx, models.SetCloudflareTokenParams{CloudflareApiToken: crypt.Of(token), UpdatedAt: now}); err != nil {
		fail("Mission Control couldn't save it: " + err.Error())
		return false, checks
	}
	s.Forget(inst)
	return true, checks
}

// Result is one thing repair did, and where it stands.
type Result struct {
	Item   string  `json:"item"`
	State  string  `json:"state"`
	Reason *string `json:"reason"`
}

func result(item, state, reason string) Result {
	r := Result{Item: item, State: state}
	if reason != "" {
		r.Reason = &reason
	}
	return r
}

// Repair pushes the tunnel's routes again from the database, and points
// Houston's records again: admin. and hooks. (host by host), and every
// project that has served, its name and domains. Only records Houston made
// are changed; anything else is reported. Idempotent.
func (s *Settings) Repair(ctx context.Context, inst models.Installation, now time.Time) ([]Result, error) {
	q := models.New(s.DB.Read)
	d := dns.DNS{Installation: inst, API: s.API}
	var results []Result
	if err := d.PushRoutes(ctx, q, s.Services); err != nil {
		var cf *cloudflare.Error
		if !errors.As(err, &cf) {
			return nil, err
		}
		results = append(results, result("routes", "NO-GO", cf.Msg))
	} else {
		results = append(results, result("routes", "OK", ""))
	}
	perHost := inst.DnsMode == "per_host"
	if perHost {
		results = append(results, s.houstonNames(ctx, inst)...)
	}
	projects, err := q.Projects(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(projects, func(a, b models.Project) int { return strings.Compare(a.Name, b.Name) })
	for _, p := range projects {
		served, err := q.ProjectHasServed(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if !served { // a project's names are pointed at its first GO
			continue
		}
		host := p.Host(inst.BaseDomain)
		if perHost {
			state := d.PointDomain(ctx, p, host)
			results = append(results, result(host, state.State, state.Reason))
		}
		states := map[string]models.DomainState{}
		for _, domain := range p.Domains.V {
			if domain == host {
				continue
			}
			states[domain] = d.PointDomain(ctx, p, domain)
			results = append(results, result(domain, states[domain].State, states[domain].Reason))
		}
		if err := models.New(s.DB.Write).SetDomainStates(ctx, models.SetDomainStatesParams{DomainStates: models.DomainStates{V: states}, UpdatedAt: now, ID: p.ID}); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (s *Settings) houstonNames(ctx context.Context, inst models.Installation) []Result {
	records := cloudflare.Records{Client: cloudflare.Client{Token: inst.CloudflareApiToken.Reveal(), Base: s.API}, Zone: inst.CloudflareZoneID}
	var results []Result
	for _, label := range []string{"admin", "hooks"} {
		name := label + "." + inst.BaseDomain
		existing, err := records.Find(ctx, name)
		switch {
		case err != nil:
		case existing != nil && !existing.IsManaged():
			results = append(results, result(name, "NO-GO", name+" has a record Houston didn't create; remove it in Cloudflare, then repair again"))
			continue
		default:
			err = records.Point(ctx, name, inst.TunnelID, existing, cloudflare.Managed)
		}
		if err != nil {
			var cf *cloudflare.Error
			msg := err.Error()
			if errors.As(err, &cf) {
				msg = cf.Msg
			}
			results = append(results, result(name, "CAN'T CHECK", "Cloudflare: "+msg))
			continue
		}
		results = append(results, result(name, "DNS OK", ""))
	}
	return results
}

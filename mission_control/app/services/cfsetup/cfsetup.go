// Package cfsetup is setup's Cloudflare step: the API token checked (reads only), then the tunnel,
// its routes and the *.<base> wildcard made. Every step reuses what's
// there, so a rerun finishes whatever an earlier try didn't.
package cfsetup

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare"
	"github.com/scttymn/houston/mission_control/app/services/dns"
)

// Setup connects Cloudflare.
type Setup struct {
	DB *db.DB
	// API is Cloudflare's address ("": Cloudflare's own); HTTP calls it.
	API  string
	HTTP *http.Client
	// Services are where the tunnel sends what it routes.
	Services dns.Services
	// TokenPath is where cloudflared reads the tunnel's token
	// (HOUSTON_TUNNEL_TOKEN_PATH): writing it is all it takes to connect.
	// "": nowhere (development).
	TokenPath string
}

// Form is what the admin typed.
type Form struct {
	BaseDomain, APIToken string
}

// Normal is the form as it's used: the base domain lowercase, trimmed.
func (f Form) Normal() Form {
	f.BaseDomain = strings.ToLower(strings.TrimSpace(f.BaseDomain))
	return f
}

// TunnelName is the tunnel Houston makes for base: houston-<its first label>.
func TunnelName(base string) string {
	first, _, _ := strings.Cut(base, ".")
	if first == "" {
		first = "<base>"
	}
	return "houston-" + first
}

var label = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (f Form) check() web.Invalid {
	errs := web.Invalid{}
	if strings.TrimSpace(f.APIToken) == "" {
		errs["api_token"] = []string{"can't be blank"}
	}
	labels := strings.Split(f.BaseDomain, ".")
	ok := len(labels) >= 2 && len(f.BaseDomain) <= 253
	for _, l := range labels {
		ok = ok && label.MatchString(l)
	}
	if !ok {
		errs["base_domain"] = []string{"must be a domain like example.com, with no scheme, path or wildcard"}
	}
	return errs
}

// Check is one of the token's checks, or a step that failed, in words.
type Check struct {
	OK    bool
	Label string
}

// run is one try: its checks so far and the client.
type run struct {
	c      cloudflare.Client
	base   string
	checks []Check
}

// errStop is a try that stopped: its checks say why.
var errStop = errors.New("stopped")

func (r *run) pass(label string) { r.checks = append(r.checks, Check{OK: true, Label: label}) }
func (r *run) fail(label string) { r.checks = append(r.checks, Check{Label: label}) }

// step does one write; when Cloudflare says no it's a failed check, with
// denied instead when that's for lack of a permission (the read-only
// checks can't prove edit rights).
func (r *run) step(doing, denied string, do func() error) error {
	err := do()
	var cf *cloudflare.Error
	if !errors.As(err, &cf) {
		return err
	}
	if denied != "" && (cf.Status == 401 || cf.Status == 403) {
		r.fail(denied + " (Cloudflare: " + cf.Msg + ")")
	} else {
		r.fail("Cloudflare said no while " + doing + ": " + cf.Msg + ".")
	}
	return errStop
}

// Save checks the token and, when it passes, makes the tunnel and its DNS,
// saves the installation connected, and hands the tunnel's token to
// cloudflared. Its checks say what passed and what didn't; web.Invalid is
// the form's own errors.
func (s Setup) Save(ctx context.Context, f Form, now time.Time) (bool, []Check, error) {
	f = f.Normal()
	if errs := f.check(); len(errs) > 0 {
		return false, nil, errs
	}
	r := &run{c: cloudflare.Client{Token: strings.TrimSpace(f.APIToken), Base: s.API, HTTP: s.HTTP}, base: f.BaseDomain}
	err := s.save(ctx, r, now)
	if errors.Is(err, errStop) {
		return false, r.checks, nil
	}
	return err == nil, r.checks, err
}

func (s Setup) save(ctx context.Context, r *run, now time.Time) error {
	account, rejected := r.checkAccount(ctx)
	if rejected {
		return errStop // every other check would fail the same way
	}
	r.checkTunnels(ctx, account)
	zone := r.checkZone(ctx)
	for _, c := range r.checks {
		if !c.OK {
			return errStop
		}
	}

	tunnel, err := r.findOrMakeTunnel(ctx, account)
	if err != nil {
		return err
	}
	var token string
	err = r.step("getting the tunnel's token", "", func() error {
		return r.c.Get(ctx, "/accounts/"+account+"/cfd_tunnel/"+tunnel+"/token", nil, &token)
	})
	if err != nil {
		return err
	}
	rules, err := dns.Rules(ctx, models.New(s.DB.Read), r.base, s.Services)
	if err != nil {
		return err
	}
	err = r.step("setting the tunnel's routes", "", func() error {
		return r.c.Put(ctx, "/accounts/"+account+"/cfd_tunnel/"+tunnel+"/configurations", map[string]any{"config": map[string]any{"ingress": rules}}, nil)
	})
	if err != nil {
		return err
	}
	mode, err := r.pointDNS(ctx, zone, tunnel)
	if err != nil {
		return err
	}
	err = models.New(s.DB.Write).ConnectCloudflare(ctx, models.ConnectCloudflareParams{BaseDomain: r.base, CloudflareAccountID: account,
		CloudflareZoneID: zone, TunnelID: tunnel, CloudflareApiToken: crypt.Of(r.c.Token), TunnelToken: crypt.Of(token), DnsMode: mode,
		ConnectedAt: sql.NullTime{Time: now, Valid: true}})
	if err != nil {
		return err
	}
	return s.handToCloudflared(token)
}

// checkAccount is the one account the token sees, and whether Cloudflare
// rejected the token itself.
func (r *run) checkAccount(ctx context.Context) (string, bool) {
	var accounts []struct{ ID, Name string }
	err := r.c.Get(ctx, "/accounts", url.Values{"per_page": {"50"}}, &accounts)
	var cf *cloudflare.Error
	switch {
	case errors.As(err, &cf):
		rejected := cf.Status == 400 || cf.Status == 401 || cf.Status == 403
		if rejected {
			r.fail("The token isn't valid (Cloudflare: " + cf.Msg + "). Copy the token's value again (not its ID), or check it's active in Cloudflare.")
		} else {
			r.fail("Cloudflare: " + cf.Msg)
		}
		return "", rejected
	case err != nil:
		r.fail("Cloudflare: " + err.Error())
		return "", false
	case len(accounts) == 0:
		r.fail("The token can't see any Cloudflare account. Give it access to the account that holds " + r.base + ".")
	case len(accounts) > 1:
		r.fail("The token can see more than one account; make one for just the account that holds " + r.base + ".")
	default:
		r.pass("Account · " + accounts[0].Name)
		return accounts[0].ID, false
	}
	return "", false
}

func (r *run) checkTunnels(ctx context.Context, account string) {
	if account == "" {
		return
	}
	if err := r.c.Get(ctx, "/accounts/"+account+"/cfd_tunnel", url.Values{"per_page": {"1"}}, nil); err != nil {
		r.fail("Account · Cloudflare Tunnel: the token needs Cloudflare Tunnel · Edit on the account (Cloudflare: " + message(err) + ").")
		return
	}
	r.pass("Account · Cloudflare Tunnel")
}

// checkZone is the zone holding the base domain (itself, or a parent when
// Houston lives on a subdomain like next.example.com), when the token can
// read its DNS.
func (r *run) checkZone(ctx context.Context) string {
	zone, err := r.c.ZoneOf(ctx, r.base)
	if err == nil && zone == nil {
		r.fail(r.base + " isn't in one of the token's zones. Give it Zone · DNS · Edit on " + r.base + "'s zone (the zone must be in this Cloudflare account).")
		return ""
	}
	name := r.base
	if err == nil {
		name = zone.Name
		err = r.c.Get(ctx, "/zones/"+zone.ID+"/dns_records", url.Values{"per_page": {"1"}}, nil)
	}
	if err != nil {
		r.fail("Zone · DNS on " + name + ": the token needs Zone · DNS · Edit (Cloudflare: " + message(err) + ").")
		return ""
	}
	r.pass("Zone · DNS on " + name)
	return zone.ID
}

func message(err error) string {
	if cf, ok := errors.AsType[*cloudflare.Error](err); ok {
		return cf.Msg
	}
	return err.Error()
}

func (r *run) findOrMakeTunnel(ctx context.Context, account string) (string, error) {
	var id string
	err := r.step("creating the tunnel", "The token can read tunnels but not create them: give it Cloudflare Tunnel · Edit on the account (not only Read), then try again.", func() error {
		var found []struct{ ID string }
		if err := r.c.Get(ctx, "/accounts/"+account+"/cfd_tunnel", url.Values{"name": {TunnelName(r.base)}, "is_deleted": {"false"}}, &found); err != nil {
			return err
		}
		if len(found) > 0 {
			id = found[0].ID
			return nil
		}
		var made struct{ ID string }
		err := r.c.Post(ctx, "/accounts/"+account+"/cfd_tunnel", map[string]string{"name": TunnelName(r.base), "config_src": "cloudflare"}, &made)
		id = made.ID
		return err
	})
	return id, err
}

// pointDNS owns *.<base> when it's free (or Houston's already): "wildcard".
// When another server owns it (a move between servers in progress),
// Houston leaves it and makes admin. and hooks. its own instead:
// "per_host" (each app's name is added when it's first deployed). Every
// name is looked at before anything's written.
func (r *run) pointDNS(ctx context.Context, zone, tunnel string) (string, error) {
	records := cloudflare.Records{Client: r.c, Zone: zone}
	find := func(name string) (*cloudflare.Record, error) {
		var rec *cloudflare.Record
		err := r.step("looking up "+name, "", func() (err error) {
			rec, err = records.Find(ctx, name)
			return err
		})
		return rec, err
	}
	point := func(name string, existing *cloudflare.Record) error {
		return r.step("pointing "+name+" at the tunnel", "The token can read DNS but not change it: give it Zone · DNS · Edit on "+r.base+" (not only Read), then try again.",
			func() error { return records.Point(ctx, name, tunnel, existing, cloudflare.Managed) })
	}

	wildcard := "*." + r.base
	rec, err := find(wildcard)
	if err != nil {
		return "", err
	}
	if rec == nil || rec.IsManaged() {
		return "wildcard", point(wildcard, rec)
	}
	names := []string{"admin." + r.base, "hooks." + r.base}
	existing := map[string]*cloudflare.Record{}
	for _, name := range names {
		if existing[name], err = find(name); err != nil {
			return "", err
		}
	}
	foreign := false
	for _, name := range names {
		if rec := existing[name]; rec != nil && !rec.IsManaged() {
			foreign = true
			r.fail(name + " already exists and Houston didn't create it (no " + cloudflare.Managed + " comment). Another server owns " + wildcard +
				", so Houston needs its own " + name + "; remove that record in Cloudflare, then try again.")
		}
	}
	if foreign {
		return "", errStop
	}
	for _, name := range names {
		if err := point(name, existing[name]); err != nil {
			return "", err
		}
	}
	return "per_host", nil
}

// handToCloudflared writes the tunnel's token where cloudflared reads it
// (`tunnel run --token-file`, on a volume they share), whole or not at all.
func (s Setup) handToCloudflared(token string) error {
	if s.TokenPath == "" {
		return nil
	}
	tmp := s.TokenPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(token), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.TokenPath)
}

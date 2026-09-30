package cfsetup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/cfsetup"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare/cloudflaretest"
	"github.com/scttymn/houston/mission-control-go/app/services/dns"
	"github.com/scttymn/houston/mission-control-go/test"
)

// fresh is a server at setup's Cloudflare step, and a Cloudflare account
// with svnmns.com in it.
func fresh(t *testing.T) (cfsetup.Setup, *cloudflaretest.Fake) {
	t.Helper()
	keys, _ := crypt.ParseKeys(crypt.NewKey())
	crypt.Use(keys...)
	t.Cleanup(func() { crypt.Use() })
	fake := cloudflaretest.New(t)
	fake.Account("acct", "Seven Moons")
	fake.Zone("zbase", "svnmns.com", "active")
	return cfsetup.Setup{DB: test.DB(t), API: fake.URL, TokenPath: filepath.Join(t.TempDir(), "tunnel-token"),
		Services: dns.Services{MissionControl: "http://mission-control:8080", Apps: "http://kamal-proxy:80"}}, fake
}

func labels(checks []cfsetup.Check) []string {
	var out []string
	for _, c := range checks {
		state := "NO-GO "
		if c.OK {
			state = "GO "
		}
		out = append(out, state+c.Label)
	}
	return out
}

func installation(t *testing.T, s cfsetup.Setup) models.Installation {
	t.Helper()
	inst, err := models.New(s.DB.Read).CurrentInstallation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

// The token checked, the tunnel made with its routes, *.<base> pointed at
// it, the installation connected, and cloudflared handed its token.
func TestConnect(t *testing.T) {
	s, fake := fresh(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	ok, checks, err := s.Save(context.Background(), cfsetup.Form{BaseDomain: " SVNMNS.com ", APIToken: " cf-token "}, now)
	if !ok || err != nil {
		t.Fatalf("connect = %v %v %v", ok, labels(checks), err)
	}
	if want := []string{"GO Account · Seven Moons", "GO Account · Cloudflare Tunnel", "GO Zone · DNS on svnmns.com"}; !slices.Equal(labels(checks), want) {
		t.Errorf("checks %q", labels(checks))
	}
	if !slices.Contains(fake.Calls(), "POST /accounts/acct/cfd_tunnel") {
		t.Errorf("no tunnel made: %q", fake.Calls())
	}
	inst := installation(t, s)
	if inst.BaseDomain != "svnmns.com" || inst.CloudflareAccountID != "acct" || inst.CloudflareZoneID != "zbase" || inst.TunnelID != "tun1" ||
		inst.DnsMode != "wildcard" || !inst.CloudflareConnectedAt.Time.Equal(now) || inst.CloudflareApiToken.Reveal() != "cf-token" ||
		inst.TunnelToken.Reveal() != "token-for-tun1" {
		t.Errorf("installation %+v", inst)
	}
	if records := fake.Records("zbase"); len(records) != 1 || records[0] != (cloudflare.Record{ID: records[0].ID, Type: "CNAME", Name: "*.svnmns.com",
		Content: "tun1.cfargotunnel.com", Proxied: true, Comment: "managed-by:houston"}) {
		t.Errorf("records %+v", records)
	}
	for _, rule := range []string{`"hostname":"admin.svnmns.com","service":"http://mission-control:8080"`, `{"service":"http://kamal-proxy:80"}`} {
		if !strings.Contains(fake.Tunnel("acct", "tun1"), rule) {
			t.Errorf("no %s in the tunnel's routes %s", rule, fake.Tunnel("acct", "tun1"))
		}
	}
	info, err := os.Stat(s.TokenPath)
	if b, _ := os.ReadFile(s.TokenPath); err != nil || string(b) != "token-for-tun1" || info.Mode().Perm() != 0o600 {
		t.Errorf("token file %q %v %v", b, info, err)
	}
	if _, err := os.Stat(s.TokenPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("the temporary file left")
	}
}

// A rerun finishes what the last try made: the tunnel found by its name,
// Houston's wildcard updated.
func TestConnectAgain(t *testing.T) {
	s, fake := fresh(t)
	fake.TunnelDetails("acct", "tun9", map[string]any{"id": "tun9", "name": "houston-svnmns"})
	fake.Record("zbase", cloudflare.Record{Type: "CNAME", Name: "*.svnmns.com", Content: "old.cfargotunnel.com", Proxied: true, Comment: "managed-by:houston"})
	if ok, checks, err := s.Save(context.Background(), cfsetup.Form{BaseDomain: "svnmns.com", APIToken: "cf-token"}, time.Now()); !ok || err != nil {
		t.Fatalf("again = %v %v %v", ok, labels(checks), err)
	}
	for _, call := range fake.Calls() {
		if strings.HasPrefix(call, "POST ") {
			t.Errorf("made again: %s", call)
		}
	}
	if records := fake.Records("zbase"); len(records) != 1 || records[0].Content != "tun9.cfargotunnel.com" {
		t.Errorf("records %+v", records)
	}
	if inst := installation(t, s); inst.TunnelID != "tun9" {
		t.Errorf("tunnel %q", inst.TunnelID)
	}
}

// Another server owns *.<base> (a move between servers): admin. and
// hooks. are Houston's own instead; a name of its own there stops it
// before anything's written.
func TestConnectPerHost(t *testing.T) {
	s, fake := fresh(t)
	fake.Record("zbase", cloudflare.Record{Type: "CNAME", Name: "*.svnmns.com", Content: "other.cfargotunnel.com", Proxied: true})
	fake.Record("zbase", cloudflare.Record{Type: "A", Name: "admin.svnmns.com", Content: "10.0.0.1"})
	ok, checks, err := s.Save(context.Background(), cfsetup.Form{BaseDomain: "svnmns.com", APIToken: "cf-token"}, time.Now())
	want := "NO-GO admin.svnmns.com already exists and Houston didn't create it (no managed-by:houston comment). Another server owns *.svnmns.com, so Houston needs its own admin.svnmns.com; remove that record in Cloudflare, then try again."
	if ok || err != nil || labels(checks)[len(checks)-1] != want {
		t.Fatalf("a foreign admin. = %v %q %v", ok, labels(checks), err)
	}
	if len(fake.Records("zbase")) != 2 {
		t.Errorf("records written: %+v", fake.Records("zbase"))
	}
	if _, err := models.New(s.DB.Read).CurrentInstallation(context.Background()); err == nil {
		t.Error("saved")
	}

	s, fake = fresh(t)
	fake.Record("zbase", cloudflare.Record{Type: "CNAME", Name: "*.svnmns.com", Content: "other.cfargotunnel.com", Proxied: true})
	fake.Record("zbase", cloudflare.Record{Type: "CNAME", Name: "admin.svnmns.com", Content: "old.cfargotunnel.com", Proxied: true, Comment: "managed-by:houston"})
	if ok, checks, err := s.Save(context.Background(), cfsetup.Form{BaseDomain: "svnmns.com", APIToken: "cf-token"}, time.Now()); !ok || err != nil {
		t.Fatalf("per host = %v %q %v", ok, labels(checks), err)
	}
	var names []string
	for _, r := range fake.Records("zbase") {
		if r.IsManaged() {
			names = append(names, r.Name)
		}
	}
	// admin. was Houston's already: updated, not made again.
	if !slices.Equal(names, []string{"admin.svnmns.com", "hooks.svnmns.com"}) || installation(t, s).DnsMode != "per_host" ||
		fake.Records("zbase")[1].Content != installation(t, s).TunnelID+".cfargotunnel.com" {
		t.Errorf("Houston's names %q, mode %q", names, installation(t, s).DnsMode)
	}
}

// Each way it can fail says why, and saves nothing.
func TestConnectRefused(t *testing.T) {
	for _, c := range []struct {
		name  string
		set   func(*cloudflaretest.Fake)
		token string
		want  []string
	}{
		{"not a token", nil, "nope", []string{"NO-GO The token isn't valid (Cloudflare: Invalid API Token). Copy the token's value again (not its ID), or check it's active in Cloudflare."}},
		{"two accounts", func(f *cloudflaretest.Fake) { f.Account("acct2", "Other") }, "", []string{
			"NO-GO The token can see more than one account; make one for just the account that holds svnmns.com.", "GO Zone · DNS on svnmns.com"}},
		{"no tunnels", func(f *cloudflaretest.Fake) { f.FailCall("GET /accounts/acct/cfd_tunnel", "Authentication error") }, "", []string{
			"GO Account · Seven Moons", "NO-GO Account · Cloudflare Tunnel: the token needs Cloudflare Tunnel · Edit on the account (Cloudflare: Authentication error).",
			"GO Zone · DNS on svnmns.com"}},
		{"no zone", func(f *cloudflaretest.Fake) { f.FailCall("GET /zones/zbase/dns_records", "Authentication error") }, "", []string{
			"GO Account · Seven Moons", "GO Account · Cloudflare Tunnel", "NO-GO Zone · DNS on svnmns.com: the token needs Zone · DNS · Edit (Cloudflare: Authentication error)."}},
		{"tunnels read only", func(f *cloudflaretest.Fake) { f.FailCall("POST /accounts/acct/cfd_tunnel", "Authentication error") }, "", []string{
			"GO Account · Seven Moons", "GO Account · Cloudflare Tunnel", "GO Zone · DNS on svnmns.com",
			"NO-GO The token can read tunnels but not create them: give it Cloudflare Tunnel · Edit on the account (not only Read), then try again. (Cloudflare: Authentication error)"}},
		{"Cloudflare down", func(f *cloudflaretest.Fake) { f.FailCallWith("POST /accounts/acct/cfd_tunnel", 500, "Internal error") }, "", []string{
			"GO Account · Seven Moons", "GO Account · Cloudflare Tunnel", "GO Zone · DNS on svnmns.com",
			"NO-GO Cloudflare said no while creating the tunnel: Internal error."}},
		{"no token", func(f *cloudflaretest.Fake) { f.FailCall("GET /accounts/acct/cfd_tunnel/tun1/token", "Tunnel is gone") }, "", []string{
			"GO Account · Seven Moons", "GO Account · Cloudflare Tunnel", "GO Zone · DNS on svnmns.com",
			"NO-GO Cloudflare said no while getting the tunnel's token: Tunnel is gone."}},
		{"DNS read only", func(f *cloudflaretest.Fake) { f.FailCall("POST /zones/zbase/dns_records", "Authentication error") }, "", []string{
			"GO Account · Seven Moons", "GO Account · Cloudflare Tunnel", "GO Zone · DNS on svnmns.com",
			"NO-GO The token can read DNS but not change it: give it Zone · DNS · Edit on svnmns.com (not only Read), then try again. (Cloudflare: Authentication error)"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, fake := fresh(t)
			if c.set != nil {
				c.set(fake)
			}
			token := c.token
			if token == "" {
				token = "cf-token"
			}
			ok, checks, err := s.Save(context.Background(), cfsetup.Form{BaseDomain: "svnmns.com", APIToken: token}, time.Now())
			if ok || err != nil || !slices.Equal(labels(checks), c.want) {
				t.Errorf("= %v %v\n%q\nwant %q", ok, err, labels(checks), c.want)
			}
			if _, err := models.New(s.DB.Read).CurrentInstallation(context.Background()); err == nil {
				t.Error("saved")
			}
			if _, err := os.Stat(s.TokenPath); err == nil {
				t.Error("cloudflared handed a token")
			}
		})
	}
	s, _ := fresh(t)
	for base, field := range map[string]string{"https://svnmns.com": "base_domain", "*.svnmns.com": "base_domain", "svnmns": "base_domain", "": "base_domain"} {
		_, _, err := s.Save(context.Background(), cfsetup.Form{BaseDomain: base, APIToken: "cf-token"}, time.Now())
		var invalid web.Invalid
		if !errors.As(err, &invalid) || invalid[field] == nil {
			t.Errorf("%q: %v", base, err)
		}
	}
	_, _, err := s.Save(context.Background(), cfsetup.Form{BaseDomain: "svnmns.com", APIToken: " "}, time.Now())
	if invalid, ok := errors.AsType[web.Invalid](err); !ok || invalid["api_token"] == nil {
		t.Errorf("no token: %v", err)
	}
}

func TestTunnelName(t *testing.T) {
	for base, want := range map[string]string{"svnmns.com": "houston-svnmns", "a.b.co": "houston-a", "": "houston-<base>"} {
		if got := cfsetup.TunnelName(base); got != want {
			t.Errorf("%q: %q", base, got)
		}
	}
}

// Houston on a subdomain of a zone (a second server beside another in the
// same zone): its own tunnel, and its names under its base, nothing else.
func TestConnectSubdomain(t *testing.T) {
	s, fake := fresh(t)
	fake.TunnelDetails("acct", "tun9", map[string]any{"id": "tun9", "name": "houston-svnmns"})
	fake.Record("zbase", cloudflare.Record{Type: "CNAME", Name: "*.svnmns.com", Content: "tun9.cfargotunnel.com", Proxied: true, Comment: "managed-by:houston"})
	ok, checks, err := s.Save(context.Background(), cfsetup.Form{BaseDomain: "next.svnmns.com", APIToken: "cf-token"}, time.Now())
	if !ok || err != nil || !slices.Contains(labels(checks), "GO Zone · DNS on svnmns.com") {
		t.Fatalf("connect = %v %q %v", ok, labels(checks), err)
	}
	inst := installation(t, s)
	if inst.BaseDomain != "next.svnmns.com" || inst.CloudflareZoneID != "zbase" || inst.TunnelID == "tun9" || inst.DnsMode != "wildcard" {
		t.Errorf("installation %+v", inst)
	}
	var names []string
	for _, r := range fake.Records("zbase") {
		names = append(names, r.Name+" "+r.Content)
	}
	if want := []string{"*.svnmns.com tun9.cfargotunnel.com", "*.next.svnmns.com " + inst.TunnelID + ".cfargotunnel.com"}; !slices.Equal(names, want) {
		t.Errorf("records %q", names)
	}
	if !slices.Contains(fake.Calls(), "GET /accounts/acct/cfd_tunnel?is_deleted=false&name=houston-next") {
		t.Errorf("tunnel looked up: %q", fake.Calls())
	}
}

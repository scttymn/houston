package dns_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare/cloudflaretest"
	"github.com/scttymn/houston/mission-control-go/app/services/dns"
	"github.com/scttymn/houston/mission-control-go/test"
)

// cloudflareFor is a fake Cloudflare with the base zone (svnmns.com), and
// DNS through it: host by host when perHost.
func cloudflareFor(t *testing.T, perHost bool) (*cloudflaretest.Fake, dns.DNS) {
	f := cloudflaretest.New(t)
	f.Zone("zbase", "svnmns.com", "active")
	mode := ""
	if perHost {
		mode = "per_host"
	}
	return f, dns.DNS{API: f.URL, Installation: models.Installation{BaseDomain: "svnmns.com", DnsMode: mode, CloudflareZoneID: "zbase",
		CloudflareAccountID: "acct", TunnelID: "tun", CloudflareApiToken: crypt.Of("cf-token")}}
}

var shop = models.Project{Name: "shop"}

func TestPointHost(t *testing.T) {
	ctx := context.Background()
	f, d := cloudflareFor(t, true)
	if err := d.PointHost(ctx, shop); err != nil {
		t.Fatal(err)
	}
	want := cloudflare.Record{Type: "CNAME", Name: "shop.svnmns.com", Content: "tun.cfargotunnel.com", Proxied: true, Comment: "managed-by:houston project:shop"}
	if got := f.Records("zbase"); len(got) != 1 || got[0].Name != want.Name || got[0].Content != want.Content || got[0].Comment != want.Comment || !got[0].Proxied {
		t.Errorf("made %+v", got)
	}

	// One of Houston's (another project's, before a rename): changed.
	f2, d2 := cloudflareFor(t, true)
	f2.Record("zbase", cloudflare.Record{Type: "CNAME", Name: "shop.svnmns.com", Content: "old.cfargotunnel.com", Comment: "managed-by:houston project:old"})
	if err := d2.PointHost(ctx, shop); err != nil || f2.Records("zbase")[0].Content != "tun.cfargotunnel.com" || len(f2.Records("zbase")) != 1 {
		t.Errorf("Houston's: %v %+v", err, f2.Records("zbase"))
	}

	// Someone else's: refused, left alone.
	f3, d3 := cloudflareFor(t, true)
	f3.Record("zbase", cloudflare.Record{Type: "A", Name: "shop.svnmns.com", Content: "1.2.3.4"})
	err := d3.PointHost(ctx, shop)
	var refused models.Refused
	if !errors.As(err, &refused) || refused.Msg != "NO-GO: shop.svnmns.com already exists and Houston didn't create it (no managed-by:houston comment). Remove it in Cloudflare, then deploy again." ||
		f3.Records("zbase")[0].Content != "1.2.3.4" {
		t.Errorf("someone else's: %v %+v", err, f3.Records("zbase"))
	}
}

// A custom domain gets a state, whatever happens: pointed, waiting on its
// zone, refused, or Cloudflare's error.
func TestPointDomain(t *testing.T) {
	ctx := context.Background()
	f, d := cloudflareFor(t, false)
	f.Zone("zcom", "example.com", "active")
	f.Zone("zpend", "pending.dev", "pending")
	f.Record("zcom", cloudflare.Record{Name: "old.example.com", Comment: "managed-by:houston project:blog"})
	f.Record("zcom", cloudflare.Record{Name: "hand.example.com", Comment: "made by hand"})

	for domain, want := range map[string]models.DomainState{
		"shop.svnmns.com":      {State: "WILDCARD"},
		"www.shop.example.com": {State: "DNS OK"},
		"shop.pending.dev":     {State: "DNS PENDING", Reason: "the zone's nameservers aren't switched to Cloudflare yet"},
		"shop.example.org":     {State: "ZONE NOT IN CLOUDFLARE YET", Reason: "Add example.org to Cloudflare (adding a zone is manual), then deploy again"},
		"shop.notsvnmns.com":   {State: "ZONE NOT IN CLOUDFLARE YET", Reason: "Add notsvnmns.com to Cloudflare (adding a zone is manual), then deploy again"},
		"old.example.com":      {State: "NO-GO", Reason: "old.example.com belongs to project blog"},
		"hand.example.com":     {State: "NO-GO", Reason: "hand.example.com has a record Houston didn't create for shop; remove it in Cloudflare to move the domain here"},
	} {
		if got := d.PointDomain(ctx, shop, domain); got != want {
			t.Errorf("%s: %+v, want %+v", domain, got, want)
		}
	}
	var made []string
	for _, r := range f.Records("zcom") {
		if r.Comment == "managed-by:houston project:shop" {
			made = append(made, r.Name)
		}
	}
	if strings.Join(made, " ") != "www.shop.example.com" {
		t.Errorf("made %v", made)
	}
	// The zone is the domain's nearest parent, never a TLD alone.
	for _, call := range f.Calls() {
		if strings.HasSuffix(call, "zones?name=com") || strings.HasSuffix(call, "zones?name=org") {
			t.Errorf("asked for a TLD's zone: %s", call)
		}
	}

	// Host by host, a domain under the base is pointed in the base zone.
	f2, d2 := cloudflareFor(t, true)
	if got := d2.PointDomain(ctx, shop, "api.svnmns.com"); got.State != "DNS OK" || len(f2.Records("zbase")) != 1 {
		t.Errorf("under the base, host by host: %+v %+v", got, f2.Records("zbase"))
	}

	f.Fail("Rate limited")
	if got := d.PointDomain(ctx, shop, "www.shop.example.com"); got != (models.DomainState{State: "CAN'T CHECK", Reason: "Cloudflare: Rate limited"}) {
		t.Errorf("Cloudflare failing: %+v", got)
	}

	// A state without a reason says so in JSON as null, as the Rails app's.
	if b, _ := json.Marshal(models.DomainState{State: "DNS OK"}); string(b) != `{"state":"DNS OK","reason":null}` {
		t.Errorf("json %s", b)
	}
}

// A dropped domain's record goes, only if it's the project's own.
func TestRemoveDomain(t *testing.T) {
	ctx := context.Background()
	f, d := cloudflareFor(t, false)
	f.Zone("zcom", "example.com", "active")
	f.Record("zcom", cloudflare.Record{Name: "shop.example.com", Comment: "managed-by:houston project:shop"})
	f.Record("zcom", cloudflare.Record{Name: "blog.example.com", Comment: "managed-by:houston project:blog"})
	f.Record("zbase", cloudflare.Record{Name: "api.svnmns.com", Comment: "managed-by:houston project:shop"})
	for _, domain := range []string{"shop.example.com", "blog.example.com", "api.svnmns.com", "shop.example.org"} {
		if err := d.RemoveDomain(ctx, shop, domain); err != nil {
			t.Errorf("%s: %v", domain, err)
		}
	}
	if got := f.Records("zcom"); len(got) != 1 || got[0].Name != "blog.example.com" || len(f.Records("zbase")) != 0 {
		t.Errorf("left %+v %+v", got, f.Records("zbase"))
	}
}

// The tunnel's routes: Mission Control's hosts, then every hostname of a
// project in maintenance (each once, though two list it), then kamal-proxy
// for the rest.
func TestRoutes(t *testing.T) {
	ctx := context.Background()
	d := test.DB(t)
	for _, p := range []struct {
		name, domains string
		maintenance   bool
	}{{"shop", `["shop.example.com","shop.svnmns.com"]`, true}, {"blog", `[]`, false}, {"api", `["api.example.com","shop.example.com"]`, true}} {
		var since any
		if p.maintenance {
			since = time.Now()
		}
		if _, err := d.Write.Exec(`INSERT INTO projects (name, app_service, services, domains, health, port, maintenance_since) VALUES (?, 'web', '["web"]', ?, '/', 80, ?)`,
			p.name, p.domains, since); err != nil {
			t.Fatal(err)
		}
	}
	to := dns.Services{MissionControl: "http://mission-control:8080", Apps: "http://kamal-proxy:80"}
	rules, err := dns.Rules(ctx, models.New(d.Read), "svnmns.com", to)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rules)
	want := `[{"hostname":"admin.svnmns.com","service":"http://mission-control:8080"},` +
		`{"hostname":"hooks.svnmns.com","path":"^/[a-z0-9-]+$","service":"http://mission-control:8080"},` +
		`{"hostname":"hooks.svnmns.com","service":"http_status:404"},` +
		`{"hostname":"api.svnmns.com","service":"http://mission-control:8080"},{"hostname":"api.example.com","service":"http://mission-control:8080"},` +
		`{"hostname":"shop.example.com","service":"http://mission-control:8080"},{"hostname":"shop.svnmns.com","service":"http://mission-control:8080"},` +
		`{"service":"http://kamal-proxy:80"}]`
	if string(b) != want {
		t.Errorf("rules\n %s\nwant\n %s", b, want)
	}

	f, cf := cloudflareFor(t, false)
	if err := cf.PushRoutes(ctx, models.New(d.Read), to); err != nil {
		t.Fatal(err)
	}
	if got := f.Tunnel("acct", "tun"); got != `{"config":{"ingress":`+want+`}}` {
		t.Errorf("pushed %s", got)
	}
}

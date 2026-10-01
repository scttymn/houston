package cfsettings_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/cfsettings"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare/cloudflaretest"
	"github.com/scttymn/houston/mission_control/app/services/dns"
	"github.com/scttymn/houston/mission_control/test"
)

var services = dns.Services{MissionControl: "http://mission-control:8080", Apps: "http://kamal-proxy:80"}

// world is Houston host by host on svnmns.com: equip and estherpictures
// (its own domain) deployed, fresh never; Cloudflare's tunnel with its
// connections and the database's routes.
func world(t *testing.T, token string) (*cfsettings.Settings, *cloudflaretest.Fake, func() models.Installation) {
	t.Helper()
	keys, _ := crypt.ParseKeys(crypt.NewKey())
	crypt.Use(keys...)
	t.Cleanup(func() { crypt.Use() })
	d := test.DB(t)
	for _, q := range []string{
		`INSERT INTO projects (id, name, app_service, services, domains, health, port) VALUES
			(1, 'equip', 'web', '["web"]', '[]', '/', 80),
			(2, 'estherpictures', 'web', '["web"]', '["estherpictures.com","www.estherpictures.com"]', '/', 80),
			(3, 'fresh', 'web', '["web"]', '[]', '/', 80)`,
		`INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at) VALUES (1, 1, 'go', 'a', 'main', CURRENT_TIMESTAMP), (2, 1, 'go', 'a', 'main', CURRENT_TIMESTAMP)`,
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := d.Write.Exec(`INSERT INTO installations (id, base_domain, dns_mode, cloudflare_zone_id, cloudflare_account_id, tunnel_id, cloudflare_api_token, cloudflare_connected_at)
		VALUES (1, 'svnmns.com', 'per_host', 'zbase', 'acct', 'tun', ?, CURRENT_TIMESTAMP)`, crypt.Of(token)); err != nil {
		t.Fatal(err)
	}
	fake := cloudflaretest.New(t)
	fake.Zone("zbase", "svnmns.com", "active")
	fake.Zone("zeq", "estherpictures.com", "active")
	fake.Account("acct", "Seven Moons")
	fake.TunnelDetails("acct", "tun", map[string]any{"id": "tun", "name": "houston-svnmns", "status": "healthy", "created_at": "2026-09-01T10:00:00Z",
		"connections": []map[string]any{{"colo_name": "mci01", "client_version": "2026.9.1", "origin_ip": "99.98.226.252", "opened_at": "2026-09-24T20:10:01Z"},
			{"colo_name": "dfw08", "client_version": "2026.9.1", "origin_ip": "99.98.226.252", "opened_at": "2026-09-24T20:10:02Z"}}})
	rules, _ := dns.Rules(context.Background(), models.New(d.Read), "svnmns.com", services)
	config, _ := json.Marshal(map[string]any{"config": map[string]any{"ingress": rules}})
	fake.Configure("acct", "tun", string(config))
	inst := func() models.Installation {
		i, err := models.New(d.Read).CurrentInstallation(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	return &cfsettings.Settings{DB: d, API: fake.URL, Services: services}, fake, inst
}

func TestViewTunnel(t *testing.T) {
	s, _, inst := world(t, "cf-token")
	v := s.Fetch(context.Background(), inst(), time.Now())
	if v.Tunnel == nil || v.Tunnel.Name != "houston-svnmns" || v.Tunnel.ID != "tun" || v.Tunnel.Status != "healthy" || len(v.Problems) != 0 {
		t.Fatalf("view %+v", v)
	}
	c := v.Connections[0]
	if len(v.Connections) != 2 || c.Colo != "MCI01" || c.Version != "2026.9.1" || c.Origin != "99.98.226.252" || !c.Since.Equal(time.Date(2026, 9, 24, 20, 10, 1, 0, time.UTC)) {
		t.Errorf("connections %+v", v.Connections)
	}
}

func TestViewRoutes(t *testing.T) {
	s, fake, inst := world(t, "cf-token")
	v := s.Fetch(context.Background(), inst(), time.Now())
	var hosts []string
	for _, r := range v.Routes {
		if r.Hostname == nil {
			hosts = append(hosts, "-")
		} else {
			hosts = append(hosts, *r.Hostname)
		}
	}
	if strings.Join(hosts, " ") != "admin.svnmns.com hooks.svnmns.com hooks.svnmns.com -" || v.Drift || len(v.MissingRoutes) != 0 {
		t.Errorf("routes %q, drift %v", hosts, v.Drift)
	}
	b, _ := json.Marshal(v.Routes[1])
	if string(b) != `{"hostname":"hooks.svnmns.com","path":"^/[a-z0-9-]+$","service":"http://mission-control:8080","drift":false}` {
		t.Errorf("route %s", b)
	}
	// Edited in Cloudflare: that rule drifts, and the database's is missing.
	fake.Configure("acct", "tun", `{"config":{"ingress":[{"hostname":"admin.svnmns.com","service":"http://elsewhere:80"},
		{"hostname":"hooks.svnmns.com","path":"^/[a-z0-9-]+$","service":"http://mission-control:8080"},
		{"hostname":"hooks.svnmns.com","service":"http_status:404"},{"service":"http://kamal-proxy:80"}]}}`)
	v = s.Fetch(context.Background(), inst(), time.Now())
	if !v.Drift || !v.Routes[0].Drift || v.Routes[1].Drift || len(v.MissingRoutes) != 1 || *v.MissingRoutes[0].Hostname != "admin.svnmns.com" {
		t.Errorf("drift %+v %+v", v.Routes, v.MissingRoutes)
	}
	// A rule removed in Cloudflare: only missing, and that's drift too.
	fake.Configure("acct", "tun", `{"config":{"ingress":[{"hostname":"hooks.svnmns.com","path":"^/[a-z0-9-]+$","service":"http://mission-control:8080"},
		{"hostname":"hooks.svnmns.com","service":"http_status:404"},{"service":"http://kamal-proxy:80"}]}}`)
	if v = s.Fetch(context.Background(), inst(), time.Now()); !v.Drift || len(v.MissingRoutes) != 1 {
		t.Errorf("missing only: %+v", v)
	}
}

func TestViewRecords(t *testing.T) {
	s, fake, inst := world(t, "cf-token")
	here := "tun.cfargotunnel.com"
	fake.Record("zbase", cloudflare.Record{Name: "admin.svnmns.com", Comment: "managed-by:houston", Content: here, Proxied: true})
	fake.Record("zbase", cloudflare.Record{Name: "equip.svnmns.com", Comment: "managed-by:houston project:equip", Content: here, Proxied: true})
	fake.Record("zbase", cloudflare.Record{Name: "other.svnmns.com", Comment: "managed-by:houston project:other", Content: "ffff0000-second-box.cfargotunnel.com"})
	fake.Record("zbase", cloudflare.Record{Name: "git.svnmns.com", Content: "192.0.2.1"})
	fake.Record("zbase", cloudflare.Record{Name: "*.svnmns.com", Comment: "managed-by:houston", Content: here})
	fake.Record("zeq", cloudflare.Record{Name: "estherpictures.com", Comment: "managed-by:houston project:estherpictures", Content: here})
	v := s.Fetch(context.Background(), inst(), time.Now())
	b, _ := json.Marshal(v.Records)
	want := `[{"zone":"estherpictures.com","name":"estherpictures.com","project":"estherpictures","proxied":false,"here":true},` +
		`{"zone":"svnmns.com","name":"*.svnmns.com","project":"wildcard","proxied":false,"here":true},` +
		`{"zone":"svnmns.com","name":"admin.svnmns.com","project":"admin","proxied":true,"here":true},` +
		`{"zone":"svnmns.com","name":"equip.svnmns.com","project":"equip","proxied":true,"here":true},` +
		`{"zone":"svnmns.com","name":"other.svnmns.com","project":"other","proxied":false,"here":false}]`
	if string(b) != want {
		t.Errorf("= %s", b)
	}
}

// A failing Cloudflare is a problem, not an error; the last complete
// answer is kept, and a failed check never replaces it: what failed is
// shown from it.
func TestViewProblems(t *testing.T) {
	s, fake, inst := world(t, "cf-token")
	if _, ok := s.Last(inst()); ok {
		t.Fatal("a view before any")
	}
	fake.Record("zbase", cloudflare.Record{Name: "equip.svnmns.com", Comment: "managed-by:houston project:equip"})
	now := time.Now()
	s.Fetch(context.Background(), inst(), now)
	kept, ok := s.Last(inst())
	if !ok || kept.CheckedAt != now || kept.Stale(now.Add(time.Minute)) || !kept.Stale(now.Add(cfsettings.StaleAfter+time.Second)) {
		t.Errorf("kept %+v", kept)
	}

	fake.FailCall("GET /accounts/acct/cfd_tunnel/tun", "Authentication error")
	fake.FailCall("GET /accounts/acct/cfd_tunnel/tun/configurations", "couldn't reach Cloudflare")
	fake.FailCall("GET /zones", "timed out")
	failed := s.Fetch(context.Background(), inst(), now.Add(time.Minute))
	if strings.Join(failed.Problems, "\n") != "tunnel: Authentication error\nroutes: couldn't reach Cloudflare\nrecords: timed out" {
		t.Errorf("problems %q", failed.Problems)
	}
	if failed.Tunnel == nil || failed.Tunnel.Name != "houston-svnmns" || len(failed.Routes) != 4 || len(failed.Records) != 1 || failed.CheckedAt != now {
		t.Errorf("what failed isn't shown from the last: %+v", failed)
	}
	if kept, _ := s.Last(inst()); kept.CheckedAt != now {
		t.Errorf("replaced: %+v", kept)
	}

	// Before any good answer: the parts that failed are empty.
	s2 := &cfsettings.Settings{DB: s.DB, API: s.API, Services: services}
	if v := s2.Fetch(context.Background(), inst(), now); v.Tunnel != nil || len(v.Routes) != 0 || len(v.Problems) != 3 {
		t.Errorf("= %+v", v)
	}
	// Another tunnel's answer isn't this one's.
	s.DB.Write.Exec(`UPDATE installations SET tunnel_id = 'another'`)
	if _, ok := s.Last(inst()); ok {
		t.Error("another tunnel's view")
	}
}

// A new token that passes every check replaces the old one (stored
// encrypted), and the next look asks Cloudflare with it.
func TestReplaceToken(t *testing.T) {
	s, _, inst := world(t, "cf-token")
	if s.Fetch(context.Background(), inst(), time.Now()); len(s.Fetch(context.Background(), inst(), time.Now()).Problems) != 0 {
		t.Fatal("no view kept")
	}
	s.DB.Write.Exec(`UPDATE installations SET cloudflare_api_token = ?`, crypt.Of("old-token"))
	replaced, checks := s.ReplaceToken(context.Background(), inst(), " cf-token ", time.Now())
	b, _ := json.Marshal(checks)
	if !replaced || string(b) != `[{"ok":true,"label":"Account · Seven Moons"},{"ok":true,"label":"Cloudflare Tunnel · this server's tunnel"},`+
		`{"ok":true,"label":"Zone · DNS on svnmns.com"},{"ok":true,"label":"Zone · DNS on estherpictures.com"}]` {
		t.Errorf("= %v %s", replaced, b)
	}
	if got := inst().CloudflareApiToken.Reveal(); got != "cf-token" {
		t.Errorf("token %q", got)
	}
	var raw string
	s.DB.Read.QueryRow(`SELECT cloudflare_api_token FROM installations`).Scan(&raw)
	if strings.Contains(raw, "cf-token") {
		t.Error("stored in the clear")
	}
	if _, ok := s.Last(inst()); ok {
		t.Error("the old token's view is kept")
	}
}

// Any failed check keeps the old token.
func TestReplaceTokenRefused(t *testing.T) {
	for _, c := range []struct {
		name, token, want string
		arrange           func(s *cfsettings.Settings, fake *cloudflaretest.Fake)
	}{
		{"blank", "  ", "Paste the new token's value", nil},
		{"rejected", "wrong", "The token isn't valid (Cloudflare: Invalid API Token). Copy the token's value (not its ID), and check it's active.", nil},
		{"another account", "cf-token", "Account: the token can't see the Cloudflare account Houston uses. Make it for that account.",
			func(s *cfsettings.Settings, _ *cloudflaretest.Fake) {
				s.DB.Write.Exec(`UPDATE installations SET cloudflare_account_id = 'theirs'`)
			}},
		{"no tunnel access", "cf-token", "Cloudflare Tunnel: the token needs Account · Cloudflare Tunnel · Edit (Cloudflare: Authentication error).",
			func(_ *cfsettings.Settings, fake *cloudflaretest.Fake) {
				fake.FailCall("GET /accounts/acct/cfd_tunnel/tun", "Authentication error")
			}},
		{"no DNS on the base zone", "cf-token", "Zone · DNS on svnmns.com: the token needs Zone · DNS · Edit (Cloudflare: Authentication error).",
			func(_ *cfsettings.Settings, fake *cloudflaretest.Fake) {
				fake.FailCall("GET /zones/zbase/dns_records", "Authentication error")
			}},
		{"no DNS on a project domain's zone", "cf-token", "Zone · DNS on estherpictures.com: the token needs Zone · DNS · Edit (Cloudflare: Authentication error).",
			func(_ *cfsettings.Settings, fake *cloudflaretest.Fake) {
				fake.FailCall("GET /zones/zeq/dns_records", "Authentication error")
			}},
		{"a domain's zone it can't see", "cf-token", "Zone · DNS for shop.example.com: the token can't see its zone. Give it Zone · DNS · Edit on it (the zone must be in this account).",
			func(s *cfsettings.Settings, _ *cloudflaretest.Fake) {
				s.DB.Write.Exec(`UPDATE projects SET domains = '["shop.example.com"]' WHERE id = 3`)
			}},
	} {
		s, fake, inst := world(t, "old-token")
		if c.arrange != nil {
			c.arrange(s, fake)
		}
		replaced, checks := s.ReplaceToken(context.Background(), inst(), c.token, time.Now())
		var failed []string
		for _, check := range checks {
			if !check.OK {
				failed = append(failed, check.Label)
			}
		}
		if replaced || strings.Join(failed, " | ") != c.want {
			t.Errorf("%s: = %v %q", c.name, replaced, failed)
		}
		if got := inst().CloudflareApiToken.Reveal(); got != "old-token" {
			t.Errorf("%s: the token is %q", c.name, got)
		}
	}
}

// Repair puts the routes and Houston's records back: only records Houston
// made; a project's names only once it has served.
func TestRepair(t *testing.T) {
	s, fake, inst := world(t, "cf-token")
	fake.Configure("acct", "tun", `{}`)
	fake.Record("zbase", cloudflare.Record{Name: "admin.svnmns.com", Comment: "managed-by:houston", Content: "old.cfargotunnel.com"})
	fake.Record("zbase", cloudflare.Record{Name: "equip.svnmns.com", Comment: "managed-by:houston project:equip", Content: "old.cfargotunnel.com"})
	fake.Record("zbase", cloudflare.Record{Name: "hooks.svnmns.com", Content: "somewhere-else"})
	results, err := s.Repair(context.Background(), inst(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(results)
	want := `[{"item":"routes","state":"OK","reason":null},{"item":"admin.svnmns.com","state":"DNS OK","reason":null},` +
		`{"item":"hooks.svnmns.com","state":"NO-GO","reason":"hooks.svnmns.com has a record Houston didn't create; remove it in Cloudflare, then repair again"},` +
		`{"item":"equip.svnmns.com","state":"DNS OK","reason":null},{"item":"estherpictures.svnmns.com","state":"DNS OK","reason":null},` +
		`{"item":"estherpictures.com","state":"DNS OK","reason":null},{"item":"www.estherpictures.com","state":"DNS OK","reason":null}]`
	if string(b) != want {
		t.Errorf("= %s", b)
	}
	if !strings.Contains(fake.Tunnel("acct", "tun"), `"hostname":"admin.svnmns.com"`) {
		t.Errorf("routes %s", fake.Tunnel("acct", "tun"))
	}
	for _, r := range fake.Records("zbase") {
		switch r.Name {
		case "hooks.svnmns.com":
			if r.Content != "somewhere-else" {
				t.Errorf("the foreign record was touched: %+v", r)
			}
		case "admin.svnmns.com", "equip.svnmns.com", "estherpictures.svnmns.com":
			if r.Content != "tun.cfargotunnel.com" {
				t.Errorf("not pointed: %+v", r)
			}
		case "fresh.svnmns.com":
			t.Error("never deployed: pointed at its first GO")
		}
	}
	var states string
	s.DB.Read.QueryRow(`SELECT domain_states FROM projects WHERE id = 2`).Scan(&states)
	if states != `{"estherpictures.com":{"state":"DNS OK","reason":null},"www.estherpictures.com":{"state":"DNS OK","reason":null}}` {
		t.Errorf("states %s", states)
	}

	fake.FailCall("PUT /accounts/acct/cfd_tunnel/tun/configurations", "Authentication error")
	results, _ = s.Repair(context.Background(), inst(), time.Now())
	if results[0].State != "NO-GO" || *results[0].Reason != "Authentication error" {
		t.Errorf("routes %+v", results[0])
	}
}

// Records come a page at a time.
func TestViewRecordPages(t *testing.T) {
	s, fake, inst := world(t, "cf-token")
	for i := range 101 {
		fake.Record("zbase", cloudflare.Record{Name: "p" + strconv.Itoa(i) + ".svnmns.com", Comment: "managed-by:houston project:p"})
	}
	if v := s.Fetch(context.Background(), inst(), time.Now()); len(v.Records) != 101 {
		t.Errorf("%d records", len(v.Records))
	}
}

// A project's own name among its domains isn't a domain's state.
func TestRepairOwnName(t *testing.T) {
	s, _, inst := world(t, "cf-token")
	s.DB.Write.Exec(`UPDATE projects SET domains = '["equip.svnmns.com"]' WHERE id = 1`)
	results, err := s.Repair(context.Background(), inst(), time.Now())
	n := 0
	for _, r := range results {
		if r.Item == "equip.svnmns.com" {
			n++
		}
	}
	var states string
	s.DB.Read.QueryRow(`SELECT domain_states FROM projects WHERE id = 1`).Scan(&states)
	if err != nil || n != 1 || states != "{}" {
		t.Errorf("%d results, states %s, %v", n, states, err)
	}
}

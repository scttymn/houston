package app_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission_control/app"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission_control/app/services/systemstatus"
)

func must(t *testing.T, a *app.App, q string, args ...any) {
	t.Helper()
	if _, err := a.DB.Write.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func contains(t *testing.T, page string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(page, want) {
			t.Errorf("no %s in\n%s", want, page)
		}
	}
}

// signedIn is the admin signed in on the network.
func signedIn(t *testing.T) (*app.App, *browser) {
	t.Helper()
	a, b := admin(t)
	b.signIn("one@example.com", password)
	return a, b
}

// reachable has the route probes (admin. and hooks.) answered as this
// install, from a local server.
func reachable(t *testing.T, a *app.App) {
	ping := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, a.Identity) }))
	t.Cleanup(ping.Close)
	a.SystemStatus.PingURL = func(string) string { return ping.URL }
}

// Before any project: the launch pad, and the pre-flight check.
func TestFlightBoardEmpty(t *testing.T) {
	a, b := signedIn(t)
	reachable(t, a)
	must(t, a, `INSERT INTO installations (id, base_domain, dns_mode, time_zone, cloudflare_connected_at) VALUES (1, 'svnmns.com', 'tunnel', 'America/Denver', CURRENT_TIMESTAMP)`)
	must(t, a, `INSERT INTO storage_locations (name, kind, is_default, acknowledged_at) VALUES ('nas', 'nfs', TRUE, CURRENT_TIMESTAMP)`)
	page := b.do("GET", "/", nil).Body.String()
	contains(t, page, "<title>Projects · Mission Control</title>", `<meta name="turbo-refresh-method" content="morph">`,
		`<turbo-stream-source src="/live?s=`, `data-controller="stats" data-stats-url-value="/resources" data-stats-every-value="30000"`,
		"FLIGHT BOARD · SVNMNS.COM · DEV", "Nothing on the pad yet",
		`<span><span>Tunnel houston-svnmns</span><small>Can&#39;t check: not connected to Cloudflare</small></span>`, `<span class="mono state state--unknown">?</span>`,
		`<span>*.svnmns.com</span><small>New apps get a subdomain automatically</small>`,
		`<span>Backup storage nas</span><small>Default · password saved</small>`,
		`<span class="disp stat__value stat__value--none">—</span>`)
	if strings.Contains(page, `class="panel flight"`) {
		t.Errorf("a board:\n%s", page)
	}
}

// The board: each project's state, flags, what's serving, its domains,
// its last deploy and backup, and its resources; the counts, and the next
// backup in Houston's zone.
func TestFlightBoard(t *testing.T) {
	a, b := signedIn(t)
	a.Version = "v0.4.2"
	b.h = a.Handler()
	now := time.Now()
	denver, _ := time.LoadLocation("America/Denver")
	reachable(t, a)
	must(t, a, `INSERT INTO installations (id, base_domain, time_zone, latest_release, cloudflare_connected_at) VALUES (1, 'svnmns.com', 'America/Denver', 'v9.9.9', CURRENT_TIMESTAMP)`)
	must(t, a, `INSERT INTO storage_locations (id, name, kind, is_default, acknowledged_at) VALUES (1, 'nas', 'nfs', TRUE, CURRENT_TIMESTAMP)`)
	must(t, a, `INSERT INTO projects (id, name, app_service, services, domains, domain_states, volumes, health, port, backup_schedule, maintenance_since, details) VALUES
		(1, 'shop', 'web', '["web","db"]', '["shop.example.com"]', '{"shop.example.com":{"state":"DNS PENDING","reason":"nameservers"}}',
			'[{"name":"data","path":"/d"}]', '/', 80, 'daily 03:00', CURRENT_TIMESTAMP, '{"images":{"db":"postgres:17"}}'),
		(2, 'blog', 'web', '["web"]', '[]', '{}', '[]', '/', 80, 'daily 01:00', NULL, '{}'),
		(3, 'wiki', 'web', '["web"]', '[]', '{}', '[]', '/', 80, 'daily 03:00', NULL, '{}'),
		(4, 'cart', 'web', '["web"]', '[]', '{}', '[{"name":"data","path":"/d"}]', '/', 80, 'daily 02:00', NULL, '{}'),
		(5, 'esther', 'web', '["web"]', '[]', '{}', '[]', '/', 80, 'daily 03:00', NULL, '{}')`)
	must(t, a, `INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at, created_at, finished_at) VALUES
		(1, 1, 'go', 'aaaaaaa1', 'refs/heads/main', ?, ?, ?),
		(1, 2, 'in_flight', 'bbbbbbb2', 'refs/heads/main', ?, ?, NULL),
		(2, 1, 'no_go', 'ccccccc3', 'refs/tags/v1', ?, ?, ?)`,
		now, now.Add(-2*time.Hour), now.Add(-2*time.Hour+108*time.Second), now, now.Add(-52*time.Second), now, now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	must(t, a, `UPDATE deploys SET step = 'Build' WHERE number = 2`)
	// blog served before its failure (a backup schedule, but no data); cart
	// serves, and holds for a copy; esther's failure named another project.
	must(t, a, `INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at, finished_at, proposed_name) VALUES
		(2, 0, 'go', 'ddddddd4', 'main', ?, ?, ''), (4, 1, 'go', 'eeeeeee5', 'main', ?, ?, ''), (4, 2, 'hold', 'fffffff6', 'main', ?, ?, 'store'),
		(5, 1, 'no_go', 'aaaaaaa7', 'main', ?, ?, 'other')`, now, now, now, now, now, now, now, now)
	must(t, a, `UPDATE deploys SET error = 'kamal deploy failed' WHERE project_id = 2`)
	must(t, a, `INSERT INTO backup_runs (project_id, location_id, kind, reason, status, bytes, heartbeat_at, finished_at) VALUES
		(1, 1, 'auto', 'schedule', 'go', 1300000, CURRENT_TIMESTAMP, ?), (1, 1, 'auto', 'manual', 'no_go', NULL, CURRENT_TIMESTAMP, NULL)`,
		time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC))
	must(t, a, `INSERT INTO project_deletions (project_id, name, requested_by, heartbeat_at) VALUES (2, 'blog', 'admin', CURRENT_TIMESTAMP), (3, 'wiki', 'admin', CURRENT_TIMESTAMP)`)
	must(t, a, `INSERT INTO backup_runs (project_id, location_id, kind, reason, status, heartbeat_at) VALUES (4, 1, 'auto', 'manual', 'no_go', CURRENT_TIMESTAMP), (4, 1, 'auto', 'manual', 'go', CURRENT_TIMESTAMP)`)
	fake := a.DockerCLI.(*dockercmdtest.Fake)
	fake.On(dockercmdtest.OK(`{"ID":"w1","Name":"shop-web-`+strings.Repeat("a", 12)+`","CPUPerc":"12.50%","MemUsage":"300MiB / 1GiB"}`), "stats")
	fake.On(dockercmdtest.OK("w1 0 0"), "inspect")
	page := b.do("GET", "/", nil).Body.String()

	// cart's, at 02:00, before shop's at 03:00.
	nextBackup := time.Date(now.In(denver).Year(), now.In(denver).Month(), now.In(denver).Day(), 2, 0, 0, 0, denver)
	if !nextBackup.After(now) {
		nextBackup = nextBackup.AddDate(0, 0, 1)
	}
	blogRow := page[strings.Index(page, `data-project="blog"`):strings.Index(page, `data-project="cart"`)]
	cartRow := page[strings.Index(page, `data-project="cart"`):strings.Index(page, `data-project="esther"`)]
	estherRow := page[strings.Index(page, `data-project="esther"`):strings.Index(page, `data-project="shop"`)]
	contains(t, blogRow, `DELETING`)
	contains(t, cartRow, `<small class="mono state state--hold">names store</small>`)
	if strings.Contains(cartRow, "backup NO-GO") || strings.Contains(estherRow, "names other") || strings.Contains(estherRow, "DELETING") {
		t.Errorf("a flag that isn't:\n%s\n%s", cartRow, estherRow)
	}
	contains(t, page,
		`<span class="mono eyebrow">NEXT BACKUP</span> <span class="disp stat__value">`+nextBackup.In(denver).Format("15:04 MST")+`</span>`,
		`<a class="mono board__pill" href="/settings#releases">v9.9.9 available</a>`,
		`<span class="mono eyebrow">IN FLIGHT</span> <span class="disp stat__value">1</span>`,
		`<span class="mono eyebrow">NO-GO</span> <span class="disp stat__value">2</span>`,
		`<div class="flight__row flight__row--in-flight" data-project="shop">`,
		`<span class="mono state state-pill state--in-flight"><span class="state-pill__dot"></span>IN FLIGHT</span>`,
		`<small class="field__error">backup NO-GO</small>`, `<small class="mono state state--hold">MAINTENANCE</small>`,
		`<small class="flight__services">db · postgres:17</small>`,
		`<span class="mono">aaaaaaa</span>`, `<small class="mono flight__next">→ bbbbbbb</small>`,
		`class="flight__host host-chip--dns-pending" href="https://shop.example.com"`,
		`<small class="mono domain-state domain-state--dns-pending" title="nameservers">DNS PENDING</small>`,
		`<a class="flight__deploying" href="/projects/shop/deploys/2">Deploying · T+00:52</a>`, `<small>Build</small>`,
		`<span>30 Sep 03:00</span>`, `<small>auto · 1.24 MB</small>`,
		`<div class="flight__row flight__row--no-go" data-project="blog">`, `<small class="">still serving</small>`,
		`<a class="flight__failed" href="/projects/blog/deploys/1">Failed about 3 hours ago</a>`, `<small>ccccccc · kamal deploy failed</small>`,
		`<div class="flight__row flight__row--standby" data-project="wiki">`, `<small class="mono state state--nogo">DELETING</small>`,
		`<span>Never deployed</span>`, `<small class="">main</small>`,
		`<article class="flight-card flight-card--in-flight" data-card="shop">`)

	// The resources, read now, as streams for each row and card.
	w := b.do("GET", "/resources", nil)
	body := w.Body.String()
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/vnd.turbo-stream.html") || !strings.Contains(body, `<turbo-stream action="update" targets="[data-resources=&#39;shop&#39;]"><template>`) ||
		!strings.Contains(body, `<span class="usage__amount">0.12 cores</span>`) || !strings.Contains(body, `<turbo-stream action="update" targets="[data-resources=&#39;blog&#39;]"><template><span class="muted">—</span></template>`) {
		t.Errorf("resources = %v\n%s", w.Header(), body)
	}
}

// On the LAN address once setup's done: whether admin.<base> reaches here.
func TestFlightBoardRoutes(t *testing.T) {
	a, b := signedIn(t)
	must(t, a, `INSERT INTO installations (id, base_domain, cloudflare_connected_at, cloudflare_api_token) VALUES (1, 'svnmns.com', ?, ?)`, time.Now().Add(-time.Minute), crypt.Of("x"))
	firstRunDone(t, a)
	answer := a.Identity
	ping := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, answer) }))
	defer ping.Close()
	a.SystemStatus.PingURL = func(string) string { return ping.URL }
	b.h = a.Handler()
	contains(t, b.do("GET", "/", nil).Body.String(), `Setup complete. Mission Control now lives at <span class="mono">admin.svnmns.com</span>`,
		`<li data-check="admin-route" data-state="go"><span class="mono state state--go">GO</span> <span><span>Route to admin.svnmns.com</span><small>Reaches this Mission Control through Cloudflare</small></span></li>`)
	b.host = "admin.svnmns.com"
	page := b.do("GET", "/", nil).Body.String()
	contains(t, page, `<small>You&#39;re using it now</small>`)
	if strings.Contains(page, "Setup complete.") {
		t.Error("the LAN notice on admin.")
	}
	b.host, answer = "", "someone else"
	a.SystemStatus = &systemstatus.Status{Identity: a.Identity, PingURL: func(string) string { return ping.URL }}
	b.h = a.Handler()
	contains(t, b.do("GET", "/", nil).Body.String(), `<div class="notice notice--hold" data-controller="refresh" data-refresh-every-value="10000">`)
}

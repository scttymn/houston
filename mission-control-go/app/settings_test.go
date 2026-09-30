package app_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare/cloudflaretest"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission-control-go/app/services/release"
)

// settingsFor is the admin on Settings, with Houston set up on svnmns.com.
func settingsFor(t *testing.T) (*app.App, *browser, *dockercmdtest.Fake) {
	t.Helper()
	a, b := signedIn(t)
	must(t, a, `INSERT INTO installations (id, base_domain, time_zone, latest_release, latest_release_url, latest_release_checked_at)
		VALUES (1, 'svnmns.com', 'America/Denver', 'v0.4.3', 'https://github.com/scttymn/houston/releases/tag/v0.4.3', ?)`, time.Now().Add(-2*time.Hour))
	must(t, a, `INSERT INTO storage_locations (id, name, kind, settings, is_default, acknowledged_at, verified_at, prune_error) VALUES
		(1, 'nas', 'nfs', '{"server":"10.0.1.20","export":"/volume1/houston"}', TRUE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ''),
		(2, 'b2', 'b2', '{"bucket":"houston-b2"}', FALSE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'restic: repository is locked')`)
	must(t, a, `INSERT INTO api_tokens (name, token_digest, last_used_at) VALUES ('laptop', 'x', ?)`, time.Now().Add(-3*time.Hour))
	a.Version = "v0.4.2"
	a.Updater.Version = "v0.4.2"
	b.h = a.Handler()
	return a, b, a.DockerCLI.(*dockercmdtest.Fake)
}

func TestSettingsPage(t *testing.T) {
	_, b, _ := settingsFor(t)
	page := b.do("GET", "/settings", nil).Body.String()
	contains(t, page, "<title>Settings · Mission Control</title>", `<a href="/settings" class="is-current">Settings</a>`,
		`<a data-action="section-nav#choose" href="#tokens">API tokens</a>`,
		`<li data-token="laptop" class="token-row"><span class="mono">laptop</span>`, `last used about 3 hours ago`,
		`<span class="mono facts-strip__label">BASE DOMAIN</span> <span>svnmns.com</span>`, `Wildcard *.svnmns.com`,
		`<span class="mono eyebrow">V0.4.2</span>`, `value="v0.4.3"`, `Update to v0.4.3</button>`,
		`v0.4.3 is out (checked about 2 hours ago). <a target="_blank" rel="noopener noreferrer" href="https://github.com/scttymn/houston/releases/tag/v0.4.3">Release notes ↗</a>`,
		`No updates from Mission Control yet.`, `Houston can't tell what port 3000 is bound to right now (it asks Docker).`,
		`<li data-location="nas" class="storage-row"><span class="mono">nas <small class="mono badge">DEFAULT</small></span>`,
		`<span class="mono" data-kind="nfs">10.0.1.20:/volume1/houston</span>`, `<span data-label="HOLDS">live volumes · backups</span>`,
		`<span data-label="USED BY">not used yet</span>`, `<small class="field__error">Last prune failed: restic: repository is locked</small>`,
		`action="/settings/storage/b2/default"`, `<option value="America/Denver" selected>America/Denver</option>`)
	if w := b.do("GET", "/settings/tokens", nil); w.Code != 302 || location(w) != "/settings#tokens" {
		t.Errorf("tokens = %d %v", w.Code, w.Header())
	}
}

func TestSettingsTimeZoneAndTokens(t *testing.T) {
	a, b, _ := settingsFor(t)
	w := b.do("POST", "/settings/general", url.Values{"_method": {"patch"}, "time_zone": {"Mars/Base"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "Time zone Mars/Base isn&#39;t a time zone (use an IANA name, like Europe/Berlin)") {
		t.Errorf("a bad zone = %d", w.Code)
	}
	w = b.do("POST", "/settings/general", url.Values{"_method": {"patch"}, "time_zone": {"Europe/Berlin"}})
	contains(t, b.do("GET", strings.Split(location(w), "#")[0], nil).Body.String(), "Houston&#39;s time zone is Europe/Berlin.")

	page := b.do("POST", "/settings/tokens", url.Values{"name": {" agent "}}).Body.String()
	contains(t, page, `<h2 class="disp panel__title">Token "agent"</h2>`, `<pre class="mono deploy-key new-token" data-clipboard-target="source">hou_`,
		"houston login https://admin.svnmns.com")
	token := page[strings.Index(page, `new-token" data-clipboard-target="source">`)+len(`new-token" data-clipboard-target="source">`):]
	token = token[:strings.Index(token, "<")]
	tokens := func() int {
		var n int
		a.DB.Read.QueryRow(`SELECT count(*) FROM api_tokens WHERE token_digest = ?`, models.Digest(token)).Scan(&n)
		return n
	}
	if tokens() != 1 {
		t.Errorf("the new token isn't kept")
	}
	w = b.do("POST", "/settings/tokens", url.Values{"name": {"agent"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `<span class="field__error">Name has already been taken</span>`) {
		t.Errorf("a taken name = %d", w.Code)
	}
	w = b.do("POST", "/settings/tokens", url.Values{"name": {""}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "Name can&#39;t be blank") {
		t.Errorf("no name = %d", w.Code)
	}
	var id string
	a.DB.Read.QueryRow(`SELECT id FROM api_tokens WHERE name = 'agent'`).Scan(&id)
	w = b.do("POST", "/settings/tokens/"+id, url.Values{"_method": {"delete"}})
	contains(t, b.do("GET", "/settings", nil).Body.String(), "Revoked. Anything using that token stops working now.")
	if tokens() != 0 {
		t.Error("a revoked token")
	}
}

// Cloudflare: its panel asked after the page, a new token checked, repair.
func TestSettingsCloudflare(t *testing.T) {
	a, b, _ := settingsFor(t)
	fake := cloudflaretest.New(t)
	fake.Zone("zbase", "svnmns.com", "active")
	fake.Account("acct", "Seven Moons")
	fake.TunnelDetails("acct", "tun", map[string]any{"id": "tun", "name": "houston-svnmns", "status": "healthy", "created_at": "2026-09-01T10:00:00Z",
		"connections": []map[string]any{{"colo_name": "mci01", "client_version": "2026.9.1", "origin_ip": "99.98.226.252", "opened_at": time.Now().Add(-time.Hour).Format(time.RFC3339)}}})
	fake.Record("zbase", cloudflare.Record{Name: "admin.svnmns.com", Comment: "managed-by:houston", Content: "tun.cfargotunnel.com"})
	must(t, a, `UPDATE installations SET cloudflare_zone_id = 'zbase', cloudflare_account_id = 'acct', tunnel_id = 'tun', cloudflare_api_token = ?, cloudflare_connected_at = CURRENT_TIMESTAMP`, crypt.Of("old-token"))
	a.CloudflareSettings.API = fake.URL
	b.h = a.Handler()
	page := b.do("GET", "/settings", nil).Body.String()
	contains(t, page, `<turbo-frame id="cloudflare-live" src="/settings/cloudflare" loading="lazy">`, "Check and replace")

	// With the old token Cloudflare says no: what it said.
	contains(t, b.do("GET", "/settings/cloudflare", nil).Body.String(), `Cloudflare didn&#39;t answer everything: tunnel: Invalid API Token`)
	w := b.do("POST", "/settings/cloudflare/token", url.Values{"_method": {"patch"}, "api_token": {"wrong"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `The token isn&#39;t valid (Cloudflare: Invalid API Token)`) || !strings.Contains(w.Body.String(), "The old token is still in use.") {
		t.Fatalf("a bad token = %d", w.Code)
	}
	a.SystemStatus.Cloudflare = fake.URL
	contains(t, b.do("GET", "/settings", nil).Body.String(), `<span class="mono facts-strip__label">TUNNEL</span> <span><span class="status__nogo">NO-GO</span>`)
	w = b.do("POST", "/settings/cloudflare/token", url.Values{"_method": {"patch"}, "api_token": {"cf-token"}})
	contains(t, b.do("GET", strings.Split(location(w), "#")[0], nil).Body.String(), "Cloudflare token replaced: it passed every check.",
		`<span class="status__go">GO</span> <span class="mono muted">· 1 CONN</span>`) // asked again with the new token

	page = b.do("GET", "/settings/cloudflare", nil).Body.String()
	contains(t, page, `<turbo-frame id="cloudflare-live">`, `<span class="mono">houston-svnmns</span> <span class="mono state state--go">healthy</span>`,
		`<td class="mono" data-label="Data center">MCI01</td>`, `<td data-label="Connected">about 1 hour ago</td>`,
		`ROUTES · WHERE THE TUNNEL SENDS EACH NAME · DRIFT: REPAIR PUTS THEM BACK`, `<span class="mono state state--nogo">MISSING</span>`,
		`<td class="mono" data-label="Name">admin.svnmns.com</td><td data-label="For">Mission Control</td>`)
	// Kept: the page shows it at once, without asking again.
	contains(t, b.do("GET", "/settings", nil).Body.String(), `<turbo-frame id="cloudflare-live"><p class="cf-checked">`)

	page = b.do("POST", "/settings/cloudflare/repair", url.Values{}).Body.String()
	contains(t, page, `<td class="mono" data-label="Item">routes</td><td data-label="Result"><span class="mono state state--go">OK</span></td>`)
	if !strings.Contains(fake.Tunnel("acct", "tun"), `"hostname":"admin.svnmns.com"`) {
		t.Errorf("routes not pushed: %s", fake.Tunnel("acct", "tun"))
	}
}

// Releases: a check says what it found; an update starts, and has a page.
func TestSettingsReleases(t *testing.T) {
	a, b, fake := settingsFor(t)
	tag := "v0.4.5"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag == "" {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, `{"tag_name":"`+tag+`","html_url":"https://github.com/scttymn/houston/releases/tag/`+tag+`"}`)
	}))
	defer github.Close()
	a.Release = release.Checker{API: github.URL, Repo: "scttymn/houston", Version: "v0.4.2"}
	b.h = a.Handler()
	w := b.do("POST", "/settings/updates/check", url.Values{})
	contains(t, b.do("GET", strings.Split(location(w), "#")[0], nil).Body.String(), `class="toast toast--go" role="status"`, "v0.4.5 is out.")
	tag = ""
	w = b.do("POST", "/settings/updates/check", url.Values{})
	contains(t, b.do("GET", "/settings", nil).Body.String(), `toast--nogo`, "Couldn&#39;t reach GitHub just now")

	w = b.do("POST", "/settings/updates", url.Values{"version": {"v0.4.5"}})
	contains(t, b.do("GET", "/settings", nil).Body.String(), "No update: Houston can&#39;t update from here: this Mission Control wasn&#39;t started by Houston&#39;s installer.")
	fake.On(dockercmdtest.OK(`{"com.docker.compose.project.config_files":"/opt/houston/compose.yml","com.docker.compose.project.working_dir":"/opt/houston"}`), "inspect", "--format", "{{json .Config.Labels}}")
	a.Updater.RunnerImage = "houston/runner:local"
	b.h = a.Handler()
	w = b.do("POST", "/settings/updates", url.Values{"version": {"v0.4.5"}})
	if location(w) != "/settings/updates/1" {
		t.Fatalf("update = %d %v", w.Code, w.Header())
	}
	page := b.do("GET", "/settings/updates/1", nil).Body.String()
	contains(t, page, "<title>Update #1 · Releases · Mission Control</title>", `UPDATING</span>`, `<span class="mono">v0.4.2 → v0.4.5</span>`,
		`data-controller="refresh" data-refresh-every-value="5000"`, "Starting…", "Updating to v0.4.5.")
	must(t, a, `UPDATE server_updates SET status = 'rolled_back', finished_at = ?, log = ?`, time.Now(),
		"==> houston update: installing v0.4.5\n==> Pulling\n==> houston update: v0.4.5 didn't install; putting v0.4.2 back\n==> Starting Houston\n")
	page = b.do("GET", "/settings/updates/1", nil).Body.String()
	contains(t, page, "The update to v0.4.5 failed, so this server went back to v0.4.2.",
		`<li data-state="failed"><span class="mono">01</span> <span>Pulling</span>`, `<li data-state="done"><span class="mono">03</span> <span>Starting Houston</span>`)
	contains(t, b.do("GET", "/settings", nil).Body.String(), `<small class="history__error">failed; went back to v0.4.2</small>`, `href="/settings/updates/1">Log</a>`)
	if w := b.do("GET", "/settings/updates/9", nil); w.Code != 404 {
		t.Errorf("no update = %d", w.Code)
	}
}

// Port 3000: what it's bound to, and closing it from the port itself goes
// on to admin.<base>.
func TestSettingsPort(t *testing.T) {
	a, b, fake := settingsFor(t)
	fake.On(dockercmdtest.OK(`{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"3000"}]}`), "inspect", "--format", "{{json .HostConfig.PortBindings}}")
	fake.On(dockercmdtest.OK(`{"com.docker.compose.project.config_files":"/opt/houston/compose.yml","com.docker.compose.project.working_dir":"/opt/houston"}`), "inspect", "--format", "{{json .Config.Labels}}")
	a.Port.RunnerImage = "houston/runner:local"
	b.h = a.Handler()
	contains(t, b.do("GET", "/settings", nil).Body.String(), `OPEN</span> to your network, over plain HTTP`, `<button name="button" type="submit" class="button button--small">Close port 3000</button>`)
	w := b.do("POST", "/settings/port", url.Values{"_method": {"patch"}, "open": {"0"}})
	if location(w) != "https://admin.svnmns.com/settings#security" {
		t.Errorf("close = %v", w.Header())
	}
	b.tunnel = true // through the tunnel: this page stays
	b.signIn("one@example.com", password)
	w = b.do("POST", "/settings/port", url.Values{"_method": {"patch"}, "open": {"0"}})
	if location(w) != "/settings#security" {
		t.Errorf("close through the tunnel = %v", w.Header())
	}
	a.Port.RunnerImage = ""
	w = b.do("POST", "/settings/port", url.Values{"_method": {"patch"}, "open": {"1"}})
	contains(t, b.do("GET", "/settings", nil).Body.String(), "Port 3000 is unchanged: Houston can&#39;t change it from here yet")
}

// Adding storage: the form, checked; restic writes there; its password
// shown once, then confirmed; then it can be the default.
func TestSettingsStorage(t *testing.T) {
	a, b, fake := settingsFor(t)
	contains(t, b.do("GET", "/settings/storage/new", nil).Body.String(), `<input type="radio" value="nfs" checked name="storage[kind]"`, "Test and save")
	w := b.do("POST", "/settings/storage", url.Values{"storage[kind]": {"local"}, "storage[name]": {"Bad Name"}, "storage[local_path]": {"srv"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `<span class="field__error">Name must be lowercase letters, digits and dashes.</span>`) ||
		!strings.Contains(w.Body.String(), `<span>Local path must be an absolute path on the server.</span>`) {
		t.Fatalf("bad fields = %d", w.Code)
	}
	fake.On(dockercmdtest.Fail(1, "Fatal: create repository at /repo failed: permission denied"), "run")
	w = b.do("POST", "/settings/storage", url.Values{"storage[kind]": {"local"}, "storage[name]": {"disk2"}, "storage[local_path]": {"/srv/backups"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "restic couldn&#39;t write there: Fatal: create repository at /repo failed: permission denied") {
		t.Fatalf("restic failing = %d", w.Code)
	}
	var password string
	ok := &dockercmdtest.Fake{}
	a.DockerCLI = ok
	b.h = a.Handler()
	w = b.do("POST", "/settings/storage", url.Values{"storage[kind]": {"local"}, "storage[name]": {"disk2"}, "storage[local_path]": {"/srv/backups"}})
	if w.Code != 303 || location(w) != "/settings/storage/disk2" {
		t.Fatalf("save = %d %v\n%s", w.Code, w.Header(), w.Body.String())
	}
	if n := count(t, a, "storage_locations"); n != 3 {
		t.Errorf("%d locations: a retry made another", n)
	}
	w = b.do("GET", "/settings/storage/disk2", nil)
	page := w.Body.String()
	contains(t, page, `<h1 class="disp">disk2</h1>`, `Wrote a test file at <span class="mono">/srv/backups</span>`, `<div id="password" class="mono shown-once__password" data-clipboard-target="source">`)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("the password page may be kept")
	}
	password = page[strings.Index(page, `shown-once__password" data-clipboard-target="source">`)+len(`shown-once__password" data-clipboard-target="source">`):]
	password = password[:strings.Index(password, "<")]
	if len(password) != 40 {
		t.Errorf("password %q", password)
	}
	w = b.do("GET", "/settings/storage/disk2/password.txt", nil)
	if w.Body.String() != password+"\n" || !strings.Contains(w.Header().Get("Content-Disposition"), `filename="houston-disk2-restic-password.txt"`) {
		t.Errorf("download %q %v", w.Body.String(), w.Header())
	}
	w = b.do("POST", "/settings/storage/disk2/acknowledge", url.Values{})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "Tick the box once the password is saved") {
		t.Errorf("unticked = %d", w.Code)
	}
	w = b.do("POST", "/settings/storage/disk2/acknowledge", url.Values{"saved": {"1"}})
	contains(t, b.do("GET", "/settings", nil).Body.String(), "disk2 is ready for backups and live volumes.")
	if w := b.do("GET", "/settings/storage/disk2", nil); w.Code != 302 {
		t.Errorf("the password page after it's confirmed = %d", w.Code)
	}
	w = b.do("POST", "/settings/storage/disk2/default", url.Values{})
	contains(t, b.do("GET", "/settings", nil).Body.String(), "disk2 is the default: projects without their own target back up there.",
		`<span class="mono">disk2 <small class="mono badge">DEFAULT</small></span>`)
	must(t, a, `INSERT INTO storage_locations (name, kind, settings, verified_at) VALUES ('unsaved', 'local', '{"path":"/x"}', CURRENT_TIMESTAMP)`)
	b.do("POST", "/settings/storage/unsaved/default", url.Values{})
	contains(t, b.do("GET", "/settings", nil).Body.String(), "unsaved isn&#39;t set up yet")
	w = b.do("POST", "/settings/storage", url.Values{"storage[kind]": {"local"}, "storage[name]": {"disk2"}, "storage[local_path]": {"/srv/other"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "Name is already used by another storage location.") {
		t.Errorf("a name in use = %d", w.Code)
	}
}

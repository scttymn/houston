package app_test

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare/cloudflaretest"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
)

const sha = "1111111111111111111111111111111111111111"

// shop is a linked project, deployed, with secrets, volumes and storage.
func shop(t *testing.T) (*app.App, *browser) {
	t.Helper()
	a, b := signedIn(t)
	must(t, a, `INSERT INTO installations (id, base_domain, dns_mode, time_zone) VALUES (1, 'svnmns.com', 'tunnel', 'UTC')`)
	must(t, a, `INSERT INTO storage_locations (id, name, kind, settings, is_default, acknowledged_at) VALUES
		(1, 'nas', 'nfs', '{"server":"10.0.1.20","export":"/volume1/houston"}', TRUE, CURRENT_TIMESTAMP),
		(2, 'b2', 's3', '{"bucket":"b"}', FALSE, CURRENT_TIMESTAMP)`)
	must(t, a, `INSERT INTO projects (id, name, app_service, services, domains, domain_states, variables, volumes, health, port, deploy_rule, details,
		repo_url, branch, compose_path, deploy_key_private, webhook_secret) VALUES
		(1, 'shop', 'web', '["web","db"]', '["shop.svnmns.com","shop.example.com"]', '{"shop.example.com":{"state":"DNS OK","reason":null}}',
			'[{"name":"SECRET_KEY_BASE","required":true},{"name":"DB_PASSWORD","required":true},{"name":"SENTRY_DSN","required":false},{"name":"API_TOKEN","required":false}]',
			'[{"name":"data","path":"/rails/storage"},{"name":"cache","path":"/cache"}]', '/up', 3000, '{"on":"commit","branch":"main"}',
			'{"images":{"db":"postgres:17"},"cpus":"2","memory":"2 GB","console":"bin/rails console"}',
			'git@github.com:scttymn/shop.git', 'main', 'compose.yml', ?, ?)`, crypt.Of("KEY"), crypt.Of("whsec-shown"))
	must(t, a, `INSERT INTO secrets (project_id, key, value, updated_at) VALUES (1, 'SECRET_KEY_BASE', ?, ?)`, crypt.Of("skb"), time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC))
	must(t, a, `INSERT INTO project_volumes (project_id, name, location_id, placed_at) VALUES (1, 'data', 1, CURRENT_TIMESTAMP)`)
	for n := 1; n <= 12; n++ {
		must(t, a, `INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at, created_at, finished_at) VALUES (1, ?, 'go', ?, 'refs/heads/main', ?, ?, ?)`,
			n, strings.Repeat(strconv.Itoa(n%10), 40), time.Now(), time.Now().Add(-time.Hour), time.Now().Add(-time.Hour+108*time.Second))
	}
	must(t, a, `INSERT INTO backup_runs (project_id, location_id, kind, reason, status, snapshot_id, bytes, heartbeat_at, finished_at, found) VALUES
		(1, 1, 'auto', 'schedule', 'go', 'abcdef1234', 2048, CURRENT_TIMESTAMP, ?, '{"sqlite":[{"volume":"data","path":"app.db"}]}')`, time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC))
	firstRunDone(t, a)
	b.h = a.Handler()
	return a, b
}

func TestProjectPage(t *testing.T) {
	_, b := shop(t)
	w := b.do("GET", "/projects/shop", nil)
	page := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("= %d\n%s", w.Code, page)
	}
	contains(t, page, "<title>shop · Project</title>",
		`<span class="mono state state-pill state--go"><span class="state-pill__dot"></span>GO</span>`,
		`class="host-chip host-chip--wildcard" href="https://shop.svnmns.com"`, `domain-state--wildcard`, `class="host-chip host-chip--dns-ok" href="https://shop.example.com"`,
		`<span class="mono console-box__label">CONSOLE · RUNS <span>bin/rails console</span> FROM COMPOSE.YML</span>`,
		`<span class="mono facts-strip__sha">2222222</span>`, `<span>Every commit to main</span>`, `<span>2 CPUs · 2 GB</span>`,
		`<span class="mono facts-strip__label">SERVICES · 1</span>`, `<span class="mono service__image">postgres:17</span>`, `<span>GitHub · scttymn/shop</span>`,
		`<a data-action="section-nav#choose" href="#webhook">Connect pushes</a>`, `<a data-action="section-nav#choose" href="#danger">Danger zone</a>`,
		`<dd class="mono">https://hooks.svnmns.com/shop</dd>`, "whsec-shown", `value="git@github.com:scttymn/shop.git"`,
		`<span class="mono eyebrow">12 DEPLOYS</span>`, `data-deploy="12"`, `<span class="mono muted">1m 48s</span>`,
		`<span class="mono muted pager__range">1–10 of 12</span>`, `<span class="mono">Page 1 of 2</span>`, `href="/projects/shop?page=2">Older →</a>`,
		`<iframe class="maintenance__preview" sandbox="" src="/projects/shop/maintenance/preview"`,
		`The next deploy will stop until DB_PASSWORD has a value.`,
		`<span class="muted">•••••••• · set Sep 29</span>`, `<div class="secret secret--missing" id="secret-DB_PASSWORD" data-secret="DB_PASSWORD">`,
		`placeholder="Paste value" aria-label="Value for DB_PASSWORD"`, `<small>optional</small>`,
		`<span class="mono eyebrow">nas</span>`, `<span class="mono">abcdef12</span> · 2 KB`,
		`<turbo-frame id="snapshots-list" src="/projects/shop/snapshots" loading="lazy">`,
		`<span>nas · <small class="muted">moving a volume is a later feature</small></span>`,
		`Live SQLite over a network share risks corruption: this volume holds SQLite databases.`,
		`<option value="nas">nas (NFS, 10.0.1.20:/volume1/houston)</option>`, `href="/projects/shop/deletion/new">Delete this project</a>`)
	if strings.Contains(page, "b2 (S3") {
		t.Error("an object store offered for a live volume")
	}
	// The required first, then by name.
	if order := []int{strings.Index(page, `data-secret="DB_PASSWORD"`), strings.Index(page, `data-secret="SECRET_KEY_BASE"`),
		strings.Index(page, `data-secret="API_TOKEN"`), strings.Index(page, `data-secret="SENTRY_DSN"`)}; !(order[0] < order[1] && order[1] < order[2] && order[2] < order[3]) {
		t.Errorf("secrets in the order %v", order)
	}
	page = b.do("GET", "/projects/shop?page=2", nil).Body.String()
	contains(t, page, `<span class="mono muted pager__range">11–12 of 12</span>`, `href="/projects/shop?page=1">← Newer</a>`, `data-deploy="1"`)
	if strings.Contains(page, `data-deploy="12"`) {
		t.Error("page 2 has page 1's deploys")
	}
	if w := b.do("GET", "/projects/nope", nil); w.Code != 404 {
		t.Errorf("no project = %d", w.Code)
	}
}

// What the page says above everything: maintenance, a deletion, copies to
// and from a new name, a deploy that held for one.
func TestProjectPageNotices(t *testing.T) {
	a, b := shop(t)
	must(t, a, `UPDATE projects SET maintenance_since = ?, maintenance_by = 'one@example.com', maintenance_message = 'Back soon'`, time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC))
	must(t, a, `INSERT INTO project_deletions (project_id, name, requested_by, step, heartbeat_at) VALUES (1, 'shop', 'admin', 'snapshot', CURRENT_TIMESTAMP)`)
	must(t, a, `INSERT INTO projects (id, name, app_service, services, health, port) VALUES (2, 'store', 'web', '["web"]', '/', 80)`)
	must(t, a, `INSERT INTO deploys (project_id, number, kind, status, sha, ref, heartbeat_at) VALUES (2, 1, 'copy', 'in_flight', ?, 'main', CURRENT_TIMESTAMP)`, sha)
	must(t, a, `INSERT INTO project_copies (project_id, from_project_id, deploy_id, from_name, to_name, sha, requested_by, status)
		SELECT 2, 1, id, 'shop', 'store', ?, 'admin', 'running' FROM deploys WHERE project_id = 2`, sha)
	must(t, a, `UPDATE deploys SET status = 'hold', proposed_name = 'hooks' WHERE project_id = 1 AND number = 12`)
	page := b.do("GET", "/projects/shop", nil).Body.String()
	contains(t, page, `shop shows a maintenance page since 30 Sep 09:00 (one@example.com): “Back soon”. It stays up until you turn it off.`,
		`<input type="hidden" name="on" value="0" autocomplete="off">`,
		`shop is being deleted (final snapshot). <a href="/deletions/1">Follow it</a>`,
		`Copying shop to store; shop keeps serving until it&#39;s GO. <a href="/projects/store/deploys/1">Follow it</a>`,
		`compose.yml on main names hooks (2222222), so deploy #12 held.`, `hooks can&#39;t be a project&#39;s name: change <span class="mono">name:</span>`,
		`<span class="muted">Being deleted</span>`)
	if strings.Contains(page, `id="maintenance"`) || strings.Contains(page, "/copy/new") {
		t.Error("the maintenance panel while on, or a copy offered for a refused name")
	}

	// A copy that failed before is said once the one running ends.
	must(t, a, `INSERT INTO project_copies (project_id, from_project_id, from_name, to_name, sha, requested_by, status, error, updated_at)
		VALUES (NULL, 1, 'shop', 'old-store', ?, 'admin', 'no_go', 'it broke', CURRENT_TIMESTAMP)`, sha)
	if page := b.do("GET", "/projects/shop", nil).Body.String(); strings.Contains(page, "it broke") {
		t.Error("a failed copy shown over the running one")
	}
	must(t, a, `DELETE FROM project_copies WHERE to_name = 'old-store'`)

	// The copy went: store tells of it, shop too.
	must(t, a, `UPDATE project_copies SET status = 'go', handed_over = '["shop.example.com"]'`)
	// A deletion stopped before anything went is said for a day.
	must(t, a, `UPDATE project_deletions SET status = 'no_go', error = 'cancelled by admin', finished_at = ?`, time.Now().Add(-time.Hour))
	contains(t, b.do("GET", "/projects/shop", nil).Body.String(), `Deleting shop was cancelled by admin. Nothing was removed.`)
	must(t, a, `UPDATE project_deletions SET finished_at = ?`, time.Now().Add(-25*time.Hour))
	if page := b.do("GET", "/projects/shop", nil).Body.String(); strings.Contains(page, "Nothing was removed") {
		t.Error("a day-old cancelled deletion")
	}
	// A deploy that failed naming another project isn't a proposal.
	must(t, a, `UPDATE deploys SET status = 'no_go' WHERE project_id = 1 AND number = 12`)
	if page := b.do("GET", "/projects/shop", nil).Body.String(); strings.Contains(page, "so deploy #12 held") {
		t.Error("a NO-GO deploy as a proposal")
	}
	must(t, a, `DELETE FROM project_deletions`)
	contains(t, b.do("GET", "/projects/shop", nil).Body.String(), `shop was copied to <a href="/projects/store">store</a>, which serves shop.example.com now.`,
		`href="/projects/shop/deletion/new">Delete shop</a>`)
	contains(t, b.do("GET", "/projects/store", nil).Body.String(), `store was copied from shop, which is still there`,
		`<strong>Undo the copy from shop.</strong>`, `action="/projects/store/copy/undo"`)

	// A failed copy, with its log.
	must(t, a, `UPDATE project_copies SET status = 'no_go', error = 'kamal deploy failed', log = 'Build ok'||char(10)||'Deploy failed'||char(10)`)
	contains(t, b.do("GET", "/projects/shop", nil).Body.String(), `The copy to store failed: kamal deploy failed.`,
		`<details class="copy-log"><summary>Its log</summary><pre class="mono log">Build ok
Deploy failed
</pre></details>`)
}

// The secrets: saved, refused with why, generated, removed; only what
// compose.yml references.
func TestProjectPageSecrets(t *testing.T) {
	a, b := shop(t)
	w := b.do("POST", "/projects/shop/secrets/DB_PASSWORD", url.Values{"_method": {"put"}, "value": {"hunter2"}})
	if w.Code != 303 || location(w) != "/projects/shop" {
		t.Fatalf("save = %d %v", w.Code, w.Header())
	}
	page := b.do("GET", "/projects/shop", nil).Body.String()
	contains(t, page, `<div class="notice notice--go"><span class="mono">OK</span><span class="notice__text">DB_PASSWORD saved.</span></div>`)
	if strings.Contains(page, "hunter2") || strings.Contains(page, "The next deploy will stop until DB_PASSWORD") {
		t.Error("a value shown, or still missing")
	}
	w = b.do("POST", "/projects/shop/secrets/SENTRY_DSN", url.Values{"_method": {"put"}, "value": {"  "}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `<span class="field__error secret__error">SENTRY_DSN needs a value</span>`) {
		t.Errorf("a blank value = %d\n%s", w.Code, w.Body.String())
	}
	b.do("POST", "/projects/shop/secrets/SENTRY_DSN/generate", url.Values{})
	contains(t, b.do("GET", "/projects/shop", nil).Body.String(), "SENTRY_DSN generated and saved.")
	b.do("POST", "/projects/shop/secrets/SECRET_KEY_BASE", url.Values{"_method": {"delete"}})
	contains(t, b.do("GET", "/projects/shop", nil).Body.String(), "SECRET_KEY_BASE removed.", "The next deploy will stop until SECRET_KEY_BASE has a value.")
	if w := b.do("POST", "/projects/shop/secrets/NOPE", url.Values{"_method": {"put"}, "value": {"x"}}); w.Code != 404 {
		t.Errorf("an unreferenced key = %d", w.Code)
	}
	var n int
	a.DB.Read.QueryRow(`SELECT count(*) FROM secrets`).Scan(&n)
	if n != 2 {
		t.Errorf("%d secrets", n)
	}
}

// The page's other forms: a volume's place, the backup target, a backup
// now, the webhook's secret, the repo, the maintenance page.
func TestProjectPageForms(t *testing.T) {
	a, b := shop(t)
	say := func(method, path string, form url.Values) string {
		t.Helper()
		w := b.do(method, path, form)
		if w.Code != 303 && w.Code != 302 {
			t.Fatalf("%s %s = %d\n%s", method, path, w.Code, w.Body.String())
		}
		to, _, _ := strings.Cut(location(w), "#") // a browser keeps the fragment to itself
		return b.do("GET", to, nil).Body.String()
	}
	contains(t, say("POST", "/projects/shop/volumes/cache", url.Values{"_method": {"patch"}, "location": {"nas"}}), "cache will be made on nas at the next deploy.")
	contains(t, say("POST", "/projects/shop/volumes/data", url.Values{"_method": {"patch"}, "location": {""}}), "data is already on nas; moving a volume is a later feature")
	contains(t, say("POST", "/projects/shop/volumes/cache", url.Values{"_method": {"patch"}, "location": {"b2"}}), "b2 can&#39;t hold live volumes (backups only)")
	if w := b.do("POST", "/projects/shop/volumes/nope", url.Values{"_method": {"patch"}}); w.Code != 404 {
		t.Errorf("no such volume = %d", w.Code)
	}
	w := b.do("POST", "/projects/shop/backup_target", url.Values{"_method": {"patch"}, "location": {"b2"}})
	if location(w) != "/projects/shop?snapshots=settings#snapshots" {
		t.Errorf("target = %v", w.Header())
	}
	contains(t, b.do("GET", "/projects/shop", nil).Body.String(), "shop backs up to b2.")
	contains(t, say("POST", "/projects/shop/backup_target", url.Values{"_method": {"patch"}, "location": {""}}), "shop backs up to nas (the default).")
	contains(t, say("POST", "/projects/shop/backups", url.Values{}), "Backing up shop.")
	contains(t, say("POST", "/projects/shop/backups", url.Values{}), "Backing up shop.") // the one queued
	var manual int
	a.DB.Read.QueryRow(`SELECT count(*) FROM backup_runs WHERE reason = 'manual'`).Scan(&manual)
	if manual != 1 {
		t.Errorf("%d manual backups", manual)
	}

	page := say("POST", "/projects/shop/rotate_webhook", url.Values{})
	contains(t, page, "New webhook secret. Paste it into the repo&#39;s webhook settings.")
	if strings.Contains(page, "whsec-shown") {
		t.Error("the old secret")
	}

	contains(t, say("POST", "/projects/shop/repo", url.Values{"_method": {"patch"}, "repo_url": {"/srv/shop.git"}}),
		"the repo URL must be an ssh://, https:// or user@host:path repo URL")

	// Maintenance goes through Cloudflare: refused before it's connected,
	// then on and off.
	contains(t, say("POST", "/projects/shop/maintenance", url.Values{"_method": {"patch"}, "on": {"1"}}), "Cloudflare isn&#39;t connected, so there&#39;s no tunnel to route through")
	fake := cloudflaretest.New(t)
	a.Cloudflare = fake.URL
	b.h = a.Handler()
	must(t, a, `UPDATE installations SET cloudflare_connected_at = CURRENT_TIMESTAMP, cloudflare_account_id = 'acct', tunnel_id = 'tun', cloudflare_api_token = ?`, crypt.Of("cf-token"))
	page = say("POST", "/projects/shop/maintenance", url.Values{"_method": {"patch"}, "on": {"1"}, "message": {"Back soon"}})
	contains(t, page, "shop shows the maintenance page. It stays up until you turn it off.", "(one@example.com): “Back soon”")
	if !strings.Contains(fake.Tunnel("acct", "tun"), `"hostname":"shop.example.com"`) {
		t.Errorf("routes %s", fake.Tunnel("acct", "tun"))
	}
	contains(t, say("POST", "/projects/shop/maintenance", url.Values{"_method": {"patch"}, "on": {"0"}}), "shop is back: its hostnames go to the app again.")
}

// Check for changes, and deploy the head, read from the repo.
func TestProjectPageDeploys(t *testing.T) {
	a, b := shop(t)
	head := strings.Repeat("f", 40)
	a.Git = gitremote.Git{KnownHosts: "/data/known_hosts", TempDir: t.TempDir(), Run: func(ctx context.Context, argv []string, env map[string]string) dockercmd.Result {
		if strings.Contains(strings.Join(argv, " "), "ls-remote") {
			return dockercmd.Result{OK: true, Output: head + "\trefs/heads/main\n"}
		}
		return dockercmd.Result{OK: true}
	}}
	b.h = a.Handler()
	w := b.do("POST", "/projects/shop/check", url.Values{})
	contains(t, b.do("GET", location(w), nil).Body.String(), "Queued fffffff (refs/heads/main).")
	w = b.do("POST", "/projects/shop/check", url.Values{})
	contains(t, b.do("GET", location(w), nil).Body.String(), "Nothing new to deploy.")
	w = b.do("POST", "/projects/shop/deploys?fresh=1", url.Values{})
	if w.Code != 303 || location(w) != "/projects/shop/deploys/13" {
		t.Fatalf("deploy = %d %v", w.Code, w.Header())
	}
	var fresh bool
	a.DB.Read.QueryRow(`SELECT fresh FROM deploys WHERE number = 13`).Scan(&fresh)
	if !fresh {
		t.Error("not a rebuild")
	}
	// A repo that can't be read says so.
	a.Git.Run = func(ctx context.Context, argv []string, env map[string]string) dockercmd.Result {
		return dockercmd.Result{Output: "git@github.com: Permission denied (publickey).\n", Code: 128}
	}
	b.h = a.Handler()
	w = b.do("POST", "/projects/shop/check", url.Values{})
	contains(t, b.do("GET", location(w), nil).Body.String(), "Couldn&#39;t read the repo: ")
	must(t, a, `UPDATE projects SET repo_url = ''`)
	w = b.do("POST", "/projects/shop/deploys", url.Values{})
	contains(t, b.do("GET", location(w), nil).Body.String(), "Link the repo first (Add project or houston link): a deploy fetches its commit from it.")
}

// Host by host, the project's own name has no wildcard to show.
func TestProjectPagePerHost(t *testing.T) {
	a, b := shop(t)
	must(t, a, `UPDATE installations SET dns_mode = 'per_host'`)
	if page := b.do("GET", "/projects/shop", nil).Body.String(); strings.Contains(page, "WILDCARD") || !strings.Contains(page, `class="host-chip host-chip--unknown" href="https://shop.svnmns.com"`) {
		t.Errorf("per host:\n%s", page)
	}
}

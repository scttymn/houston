package app_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
)

const snapshotSha = "5ade5ade5ade5ade5ade5ade5ade5ade5ade5ade"

// withSnapshots is shop with snapshots in nas: a scheduled one, a
// pre-deploy one, and a deleted project's final one.
func withSnapshots(t *testing.T) (*app.App, *browser, *dockercmdtest.Fake) {
	t.Helper()
	a, b := shop(t)
	fake := a.DockerCLI.(*dockercmdtest.Fake)
	fake.On(dockercmdtest.OK(`[{"id":"aaaa1111bbbb","short_id":"aaaa1111","time":"2026-09-29T03:00:12Z","tags":["project:shop","kind:auto","reason:schedule","sha:`+snapshotSha+`"],"summary":{"total_bytes_processed":2048}},
		{"id":"cccc2222dddd","short_id":"cccc2222","time":"2026-09-30T10:00:00Z","tags":["project:shop","kind:deploy","reason:deploy","deploy:7","sha:`+snapshotSha+`"]},
		{"id":"eeee3333ffff","short_id":"eeee3333","time":"2026-08-01T10:00:00Z","tags":["project:shop","kind:final","reason:delete","sha:`+snapshotSha+`"]}]`), "run")
	return a, b, fake
}

// The Snapshots panel's list: each tab, and the backup plan.
func TestSnapshotsFrame(t *testing.T) {
	a, b, _ := withSnapshots(t)
	// Visited on its own, it's a page; the project page's frame gets the list alone.
	contains(t, b.do("GET", "/projects/shop/snapshots", nil).Body.String(), "<title>Mission Control</title>", "Sign out", `<turbo-frame id="snapshots-list">`)
	b.frame = "snapshots-list"
	page := b.do("GET", "/projects/shop/snapshots", nil).Body.String()
	if strings.Contains(page, "Sign out") {
		t.Error("the layout in the frame's answer")
	}
	contains(t, page, `<turbo-frame id="snapshots-list">`, `<a role="tab" aria-selected="true" class="tabs__tab" href="/projects/shop/snapshots">`,
		`href="/projects/shop/snapshots?kind=deploy"><span class="tabs__label" data-label="Pre-deploy">`,
		"Daily at 03:00 (UTC), plus any you create. One per day is kept for 14 days · kept 1 / 14.",
		`<div class="snapshots__row" data-snapshot="aaaa1111">`, `<span>29 Sep 03:00</span>`, `<small>daily</small>`, `<span class="mono">5ade5ad</span>`,
		`<span class="mono muted">2 KB</span>`, `href="/projects/shop/snapshots/aaaa1111/download?location=nas">Download</a>`,
		`data-turbo-frame="_top" href="/projects/shop/restores/new?snapshot=aaaa1111&amp;location=nas">Restore</a>`)
	if strings.Contains(page, "cccc2222") {
		t.Error("a pre-deploy snapshot on the scheduled tab")
	}
	page = b.do("GET", "/projects/shop/snapshots?kind=deploy", nil).Body.String()
	contains(t, page, "The last 10 are kept · kept 1 / 10.", `<small>before deploy #7</small>`, `data-snapshot="eeee3333"`, `<small>before it was deleted</small>`)
	page = b.do("GET", "/projects/shop/snapshots?kind=settings", nil).Body.String()
	contains(t, page, `<dt class="mono">SCHEDULE</dt><dd>Daily at 03:00 (UTC), plus any you create</dd>`, `<option value="" selected>nas (default)</option>`,
		`<option value="b2">b2</option>`, `<span><span class="mono">data</span> → /rails/storage</span>`, `<span><span class="mono">app.db</span> · .backup</span>`,
		`<span class="mono">db</span> · start empty after a restore`)

	contains(t, b.do("GET", "/projects/shop/snapshots?kind=bogus", nil).Body.String(), `data-snapshot="aaaa1111"`) // the scheduled tab
	must(t, a, `UPDATE projects SET backup_location_id = 2`)
	contains(t, b.do("GET", "/projects/shop/snapshots?kind=settings", nil).Body.String(), `<option value="">nas (default)</option>`, `<option value="b2" selected>b2</option>`)

	// restic failing: said in the list; Settings shows anyway.
	a2, b2 := shop(t)
	a2.DockerCLI.(*dockercmdtest.Fake).On(dockercmdtest.Fail(1, "Fatal: unable to open repository"), "run")
	contains(t, b2.do("GET", "/projects/shop/snapshots", nil).Body.String(), `<span class="field__error">Can&#39;t read snapshots: `)
	contains(t, b2.do("GET", "/projects/shop/snapshots?kind=settings", nil).Body.String(), `<dt class="mono">KEEP</dt><dd>14 scheduled · 10 pre-deploy</dd>`)

	// No default storage: setup's storage step first, as the Rails app.
	must(t, a2, `UPDATE storage_locations SET is_default = FALSE`)
	if w := b2.do("GET", "/projects/shop/snapshots", nil); location(w) != "/setup/storage" {
		t.Errorf("no default storage = %d %q", w.Code, location(w))
	}
}

// Restore: the page for a snapshot, then asked once the name's typed.
func TestRestorePage(t *testing.T) {
	a, b, _ := withSnapshots(t)
	page := b.do("GET", "/projects/shop/restores/new?snapshot=aaaa1111&location=nas", nil).Body.String()
	contains(t, page, "<title>Restore shop</title>", `<h1 class="disp" id="restore-title">Roll shop back to 29 Sep 03:00?</h1>`,
		`<span class="mono">2222222</span>`, `<span class="mono restore-compare__after">5ade5ad</span>`, `29 Sep 03:00 · auto · 2 KB <small class="mono">(aaaa1111, nas)</small>`,
		"The maintenance page is off", `CLI: houston restore aaaa1111 --confirm shop`, `value="aaaa1111bbbb"`)
	if w := b.do("GET", "/projects/shop/restores/new?snapshot=nope&location=nas", nil); w.Code != 404 {
		t.Errorf("no snapshot = %d", w.Code)
	}
	w := b.do("POST", "/projects/shop/restores", url.Values{"snapshot": {"aaaa1111bbbb"}, "location": {"nas"}, "confirm": {"nope"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `<span class="field__error">type shop to confirm</span>`) {
		t.Fatalf("unconfirmed = %d\n%s", w.Code, w.Body.String())
	}
	a.Git = gitremote.Git{KnownHosts: "/data/known_hosts", TempDir: t.TempDir(), Run: func(ctx context.Context, argv []string, env map[string]string) dockercmd.Result {
		if strings.Contains(strings.Join(argv, " "), "rev-parse") {
			return dockercmd.Result{OK: true, Output: snapshotSha + "\n"}
		}
		return dockercmd.Result{OK: true}
	}}
	b.h = a.Handler()
	w = b.do("POST", "/projects/shop/restores", url.Values{"snapshot": {"aaaa1111bbbb"}, "location": {"nas"}, "confirm": {"shop"}})
	if w.Code != 303 || location(w) != "/projects/shop/deploys/13" {
		t.Fatalf("restore = %d %v\n%s", w.Code, w.Header(), w.Body.String())
	}
	contains(t, b.do("GET", "/projects/shop/deploys/13", nil).Body.String(), `<h1 class="disp">Restore #13</h1>`)
}

// Copy: nothing to copy without a held deploy; otherwise the plan, asked
// once the name's typed.
func TestCopyPage(t *testing.T) {
	a, b := shop(t)
	page := b.do("GET", "/projects/shop/copy/new", nil).Body.String()
	contains(t, page, `<h1 class="disp" id="copy-title">Nothing to copy</h1>`, "shop&#39;s latest deploy doesn&#39;t propose a copy. Change")
	must(t, a, `UPDATE deploys SET status = 'hold', proposed_name = 'store' WHERE number = 12`)
	page = b.do("GET", "/projects/shop/copy/new", nil).Body.String()
	contains(t, page, `<h1 class="disp" id="copy-title">Copy shop to store?</h1>`, `at <span class="mono">2222222</span>, which names store`,
		"its volumes where they live (data (nas), cache (local disk)), and its backup target (nas)", `<dd class="mono">shop.svnmns.com, shop.example.com</dd>`,
		`value="Copy to store"`)
	w := b.do("POST", "/projects/shop/copy", url.Values{"confirm": {"nope"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `<span class="field__error">type shop to confirm</span>`) {
		t.Errorf("unconfirmed = %d", w.Code)
	}
	if w := b.do("POST", "/projects/shop/copy/cancel", url.Values{}); w.Code != 404 {
		t.Errorf("cancel on a project that isn't a copy = %d", w.Code)
	}
}

// Delete: what goes and what's kept, asked once the name's typed; then the
// deletion's page, in each state.
func TestDeletePage(t *testing.T) {
	a, b := shop(t)
	page := b.do("GET", "/projects/shop/deletion/new", nil).Body.String()
	contains(t, page, `<h1 class="disp" id="delete-title">Delete shop?</h1>`, `<dd class="mono">shop-web, shop-db</dd>`,
		`<dd>data (nas); cache (local disk); what db hold</dd>`, `<dd class="mono">shop.svnmns.com, shop.example.com</dd>`,
		"A final snapshot of shop&#39;s data goes to nas first, and its backups stay (nas).",
		"Houston can&#39;t remove the deploy key and the webhook on the git host (git@github.com:scttymn/shop.git)")
	w := b.do("POST", "/projects/shop/deletion", url.Values{"confirm": {"nope"}, "delete_backups": {"1"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `<span class="field__error">type shop to confirm</span>`) ||
		!strings.Contains(w.Body.String(), `name="delete_backups" id="delete_backups" value="1" checked`) {
		t.Fatalf("unconfirmed = %d", w.Code)
	}
	w = b.do("POST", "/projects/shop/deletion", url.Values{"confirm": {"shop"}})
	if w.Code != 303 || location(w) != "/deletions/1" {
		t.Fatalf("delete = %d %v", w.Code, w.Header())
	}
	if pending, _ := a.Jobs.Pending(t.Context()); len(pending) == 0 || pending[len(pending)-1].Name != "delete_project" {
		t.Errorf("pending %+v", pending)
	}
	page = b.do("GET", "/deletions/1", nil).Body.String()
	contains(t, page, "<title>Deleting shop · Mission Control</title>", `<meta name="turbo-refresh-method" content="morph">`,
		`<h1 class="disp">Deleting shop</h1>`, `DELETING</span>`, `Asked by one@example.com · `,
		`<div class="serving" data-controller="refresh" data-refresh-every-value="5000">`, "shop keeps serving until its final snapshot is taken.",
		`<li data-state="pending"><span class="mono">00</span> <span>Check Docker, Cloudflare and storage</span>`, "Waiting to start…")

	must(t, a, `UPDATE project_deletions SET status = 'running', step = 'dns', removing_at = CURRENT_TIMESTAMP, log = '== check'||char(10)`)
	page = b.do("GET", "/deletions/1", nil).Body.String()
	contains(t, page, "shop is being taken apart; it serves nothing new meanwhile.", `<li data-state="done"><span class="mono">02</span>`,
		`<li data-state="current"><span class="mono">03</span> <span>DNS records</span> <span class="mono step__state">RUNNING</span></li>`)
	must(t, a, `UPDATE project_deletions SET status = 'no_go', error = 'stopped at dns: Cloudflare said no'`)
	page = b.do("GET", "/deletions/1", nil).Body.String()
	contains(t, page, "Deleting shop stopped at dns: Cloudflare said no. It serves nothing new until it&#39;s finished.",
		`<input type="hidden" name="confirm" value="shop" autocomplete="off">`, `<li data-state="failed">`)
	must(t, a, `UPDATE project_deletions SET removing_at = NULL, error = 'cancelled by admin'`)
	contains(t, b.do("GET", "/deletions/1", nil).Body.String(), "Deleting shop was cancelled by admin. Nothing was removed; it serves as before.")
	must(t, a, `UPDATE project_deletions SET status = 'go', project_id = NULL, snapshot_id = 'abcdef1234', snapshot_location_id = 1, repo_url = 'git@github.com:scttymn/shop.git'`)
	page = b.do("GET", "/deletions/1", nil).Body.String()
	contains(t, page, "shop was deleted.", "Its final snapshot abcdef12 in nas is kept, with its other backups: add a project named shop again, then <span class=\"mono\">houston restore abcdef12</span>.",
		`<span class="mono">ON THE GIT HOST</span>`, `<span>shop</span>`)
	if strings.Contains(page, `href="/projects/shop"`) || strings.Contains(page, "LIVE") {
		t.Error("a link to the deleted project, or live")
	}
	if w := b.do("GET", "/deletions/99", nil); w.Code != 404 {
		t.Errorf("no deletion = %d", w.Code)
	}

	// Nothing to keep: a project without data.
	must(t, a, `INSERT INTO projects (id, name, app_service, services, health, port) VALUES (7, 'blog', 'web', '["web"]', '/', 80)`)
	must(t, a, `INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at) VALUES (7, 1, 'go', ?, 'main', CURRENT_TIMESTAMP)`, sha)
	contains(t, b.do("GET", "/projects/blog/deletion/new", nil).Body.String(), "blog has no data to snapshot, and its backups stay (nas).", `<dd>none</dd>`)
}

// Cancel, from a copy's deploy page: its deploy ends, its new project goes.
func TestCancelCopyPage(t *testing.T) {
	a, b := shop(t)
	must(t, a, `INSERT INTO projects (id, name, app_service, services, health, port) VALUES (2, 'store', 'web', '["web"]', '/', 80)`)
	must(t, a, `INSERT INTO deploys (project_id, number, kind, status, sha, ref, heartbeat_at) VALUES (2, 1, 'copy', 'queued', ?, 'main', CURRENT_TIMESTAMP)`, sha)
	must(t, a, `INSERT INTO project_copies (project_id, from_project_id, deploy_id, from_name, to_name, sha, requested_by)
		SELECT 2, 1, id, 'shop', 'store', ?, 'admin' FROM deploys WHERE project_id = 2`, sha)
	w := b.do("POST", "/projects/store/copy/cancel", url.Values{})
	if w.Code != 303 || location(w) != "/projects/store/deploys/1" {
		t.Fatalf("cancel = %d %v", w.Code, w.Header())
	}
	var status, why string
	a.DB.Read.QueryRow(`SELECT status, error FROM deploys WHERE project_id = 2`).Scan(&status, &why)
	if status != "no_go" || why != "cancelled by one@example.com" {
		t.Errorf("deploy %s %q", status, why)
	}
	if pending, _ := a.Jobs.Pending(t.Context()); len(pending) == 0 || pending[len(pending)-1].Name != "copy_cleanup" {
		t.Errorf("pending %+v", pending)
	}
	w = b.do("POST", "/projects/store/copy/cancel", url.Values{})
	contains(t, b.do("GET", location(w), nil).Body.String(), "the copy to store is done")
}

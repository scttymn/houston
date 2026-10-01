package app_test

import (
	"strings"
	"testing"
	"time"
)

// A deploy's page: its head and clock, what's serving meanwhile, its steps
// and log, and its live stream.
func TestDeployPage(t *testing.T) {
	a, b := shop(t)
	must(t, a, `INSERT INTO deploys (project_id, number, status, sha, ref, runner, step, log, heartbeat_at, created_at) VALUES
		(1, 13, 'in_flight', ?, 'refs/heads/main', 'houston-runner-1', 'Build', ?, CURRENT_TIMESTAMP, ?)`, strings.Repeat("d", 40), "Test ok\n<b>built</b>\n", time.Now().Add(-52*time.Second))
	var id int64
	a.DB.Read.QueryRow(`SELECT id FROM deploys WHERE number = 13`).Scan(&id)
	page := b.do("GET", "/projects/shop/deploys/13", nil).Body.String()
	contains(t, page, "<title>shop · Deploy #13</title>", `<turbo-stream-source src="/live?s=`,
		`<nav class="mono crumbs"><a href="/">Projects</a> <span>/</span> <a href="/projects/shop">shop</a> <span>/</span> <span class="crumbs__here">Deploy #13</span></nav>`,
		`<h1 class="disp">Deploy #13</h1>`, `state--in-flight`, `<span class="mono">ddddddd</span> · main · runner <span class="mono">houston-runner-1</span>`,
		`MISSION ELAPSED · STARTED `, `<span class="disp deploy-head__elapsed">T+00:5`,
		`<span class="mono">2222222</span> is still serving shop.svnmns.com.`, `Traffic switches only after <span class="mono">/up</span> returns 200 on the new version.`,
		`<li data-step="Build" data-state="current"><span class="mono">02</span> <span>Build</span> <span class="mono step__state">RUNNING</span></li>`,
		`<span class="mono log-panel__live">LIVE</span>`, `data-follow-target="log" data-clipboard-target="source" data-action="scroll->follow#scrolled">Test ok
&lt;b&gt;built&lt;/b&gt;
</pre>`)

	must(t, a, `UPDATE deploys SET status = 'no_go', error = 'the health check failed', finished_at = ?, fresh = TRUE WHERE id = ?`, time.Now(), id)
	page = b.do("GET", "/projects/shop/deploys/13", nil).Body.String()
	contains(t, page, `<h1 class="disp">Rebuild #13</h1>`, `TOOK · STARTED`, `<span class="disp deploy-head__elapsed">5`,
		`<span class="notice__text">the health check failed</span>`, `data-step="Build" data-state="failed"`)
	if strings.Contains(page, "LIVE") || strings.Contains(page, "still serving") {
		t.Error("a finished deploy as live")
	}

	// A copy's deploy: its copy, and cancel while it hasn't taken hosts.
	must(t, a, `INSERT INTO projects (id, name, app_service, services, health, port) VALUES (2, 'store', 'web', '["web"]', '/', 80)`)
	must(t, a, `INSERT INTO deploys (project_id, number, kind, status, sha, ref, heartbeat_at) VALUES (2, 1, 'copy', 'queued', ?, 'main', CURRENT_TIMESTAMP)`, sha)
	must(t, a, `INSERT INTO project_copies (project_id, from_project_id, deploy_id, from_name, to_name, sha, requested_by)
		SELECT 2, 1, id, 'shop', 'store', ?, 'admin' FROM deploys WHERE project_id = 2`, sha)
	page = b.do("GET", "/projects/store/deploys/1", nil).Body.String()
	contains(t, page, `<h1 class="disp">Copy #1</h1>`, `shop → store. shop keeps serving until store passes its health check`,
		`action="/projects/store/copy/cancel"`, `Nothing is serving store.svnmns.com yet. Waiting for a runner.`)
	must(t, a, `UPDATE deploys SET kind = 'restore' WHERE project_id = 2`)
	contains(t, b.do("GET", "/projects/store/deploys/1", nil).Body.String(), `<h1 class="disp">Restore #1</h1>`)

	for _, path := range []string{"/projects/nope/deploys/1", "/projects/shop/deploys/99", "/projects/shop/deploys/x"} {
		if w := b.do("GET", path, nil); w.Code != 404 {
			t.Errorf("%s = %d", path, w.Code)
		}
	}
}

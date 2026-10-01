package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"
)

// The projects, as the CLI and agents read them: the list, and one in
// detail (never a secret's value, a deploy key or the webhook's secret).
func TestV1Projects(t *testing.T) {
	a, h := remote(t)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, is_default, acknowledged_at) VALUES (1, 'nas', 'nfs', TRUE, ?)`, time.Now())
	exec(t, a, `UPDATE projects SET repo_url = 'git@github.com:scttymn/shop.git', branch = 'main', deploy_rule = '{"on":"push"}',
		domain_states = '{"shop.example.com":{"state":"DNS OK","reason":null}}', deploy_key_private = ?, webhook_secret = ?,
		maintenance_since = '2026-09-30 07:00:00', maintenance_by = 'scotty', maintenance_message = 'Back soon',
		variables = '[{"name":"SECRET_KEY_BASE","required":true}]'`, crypt.Of("deploy-key-value"), crypt.Of("whsec-value"))
	secret(t, a, "shop", "SECRET_KEY_BASE", "s3cret")
	deploy(t, a, "shop", 1, "deploy", "go", 1, sha1, "", 0)
	exec(t, a, `UPDATE deploys SET created_at = '2026-09-30 10:00:00', finished_at = '2026-09-30 10:01:30', step = 'Post-deploy', runner = 'houston-runner-1'`)
	deploy(t, a, "shop", 2, "deploy", "hold", 1, strings.Repeat("2", 40), "", 0)
	exec(t, a, `UPDATE deploys SET proposed_name = 'shop-2', created_at = '2026-09-30 11:00:00', finished_at = '2026-09-30 11:00:05' WHERE number = 2`)
	exec(t, a, `INSERT INTO backup_runs (project_id, location_id, kind, reason, status, heartbeat_at, created_at, snapshot_id, bytes)
		VALUES (1, 1, 'auto', 'schedule', 'go', '2026-09-30 03:00:00', '2026-09-30 03:00:00', 'abc', 42)`)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"name": "blog", "services": []string{"web"}, "variables": []any{}, "domains": []string{}}), nil)

	w := v1(h, "GET", "/projects", personal, "", nil)
	var list struct{ Projects []map[string]any }
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list.Projects) != 2 || list.Projects[0]["name"] != "blog" || list.Projects[1]["name"] != "shop" {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	blog, _ := json.Marshal(list.Projects[0])
	if !jsonEqual(string(blog), `{"name":"blog","status":"standby","running_sha":null,"host":"blog.svnmns.com","domains":{},"last_deploy":null,
		"maintenance":{"on":false},"deleting":null,"copy_proposal":null,"stats":null}`) {
		t.Errorf("blog %s", blog)
	}

	w = v1(h, "GET", "/projects/shop", personal, "", nil)
	want := `{"name":"shop","status":"hold","running_sha":"` + sha1 + `","host":"shop.svnmns.com",
		"domains":{"shop.example.com":{"state":"DNS OK","reason":null}},
		"last_deploy":{"number":2,"kind":"deploy","fresh":false,"status":"hold","sha":"` + strings.Repeat("2", 40) + `","ref":"refs/heads/main","step":null,
			"error":null,"runner":null,"started_at":"2026-09-30T11:00:00Z","finished_at":"2026-09-30T11:00:05Z","duration":5},
		"maintenance":{"on":true,"since":"2026-09-30T07:00:00Z","by":"scotty","message":"Back soon"},"deleting":null,
		"copy_proposal":{"name":"shop-2","sha":"` + strings.Repeat("2", 40) + `","deploy":2,"refusal":null},"stats":null,
		"deploy_rule":{"on":"push"},"services":["web","db"],"repo_url":"git@github.com:scttymn/shop.git","branch":"main","webhook_verified":false,
		"last_backup":{"id":1,"status":"go","kind":"auto","reason":"schedule","deploy":null,"sha":null,"snapshot_id":"abc","bytes":42,"error":null,
			"queued_at":"2026-09-30T03:00:00Z","started_at":null,"finished_at":null},
		"backup_schedule":"daily 03:00","time_zone":"UTC","secrets":[{"name":"SECRET_KEY_BASE","required":true,"set":true}]}`
	if w.Code != 200 || !jsonEqual(w.Body.String(), want) {
		t.Errorf("shop\n %s\nwant\n %s", w.Body.String(), want)
	}
	for _, secret := range []string{"deploy-key-value", "whsec-value", "s3cret"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("the view shows %q", secret)
		}
	}
	is(t, v1(h, "GET", "/projects/nope", personal, "", nil), 404, `{"error":"no project nope"}`)

	// A copy proposal to a name that can't be: its refusal says why.
	for name, refusal := range map[string]string{"blog": "blog is another project", "admin": "admin can't be a project's name", "Bad": "Bad can't be a project's name"} {
		exec(t, a, `UPDATE deploys SET proposed_name = ? WHERE number = 2`, name)
		if got := answer(t, v1(h, "GET", "/projects/shop", personal, "", nil)); fmt.Sprint(got["copy_proposal"].(map[string]any)["refusal"]) != refusal {
			t.Errorf("%s: refusal %v", name, got["copy_proposal"])
		}
	}
	// Only a deploy on HOLD proposes one.
	exec(t, a, `UPDATE deploys SET status = 'no_go' WHERE number = 2`)
	if got := answer(t, v1(h, "GET", "/projects/shop", personal, "", nil)); got["copy_proposal"] != nil {
		t.Errorf("a NO-GO's proposal: %v", got["copy_proposal"])
	}
	// A secret without a value isn't set.
	exec(t, a, `UPDATE secrets SET value = ''`)
	if got := answer(t, v1(h, "GET", "/projects/shop", personal, "", nil)); fmt.Sprint(got["secrets"]) != "[map[name:SECRET_KEY_BASE required:true set:false]]" {
		t.Errorf("secrets %v", got["secrets"])
	}
}

// Deploys: newest first, 20 a page; one with its steps and its log from a
// byte, on whole characters.
func TestV1Deploys(t *testing.T) {
	a, h := remote(t)
	for n := 1; n <= 23; n++ {
		deploy(t, a, "shop", n, "deploy", "go", 1, sha1, "", 0)
	}
	page := func(q string) []map[string]any {
		var got struct{ Deploys []map[string]any }
		json.Unmarshal(v1(h, "GET", "/projects/shop/deploys"+q, personal, "", nil).Body.Bytes(), &got)
		return got.Deploys
	}
	if first := page(""); len(first) != 20 || first[0]["number"] != 23.0 || first[19]["number"] != 4.0 {
		t.Errorf("page 1: %d, from %v", len(first), first[0]["number"])
	}
	if second := page("?page=2"); len(second) != 3 || second[2]["number"] != 1.0 {
		t.Errorf("page 2: %v", second)
	}
	if zero := page("?page=0"); len(zero) != 20 {
		t.Errorf("page 0 is the first: %d", len(zero))
	}

	exec(t, a, `UPDATE deploys SET status = 'in_flight', step = 'Build', runner = 'houston-runner-1', log = ?, finished_at = NULL WHERE number = 23`, "héllo\nworld\n")
	w := v1(h, "GET", "/projects/shop/deploys/latest", personal, "", nil)
	got := answer(t, w)
	steps, _ := json.Marshal(got["steps"])
	if got["number"] != 23.0 || got["log"] != "héllo\nworld\n" || got["log_size"] != 13.0 || got["log_next"] != 13.0 ||
		string(steps) != `[{"name":"Test","state":"done"},{"name":"Secrets","state":"done"},{"name":"Build","state":"current"},{"name":"Snapshot","state":"pending"},`+
			`{"name":"Accessories","state":"pending"},{"name":"Release","state":"pending"},{"name":"Deploy","state":"pending"},{"name":"Post-deploy","state":"pending"}]` {
		t.Errorf("latest: %s", w.Body.String())
	}
	// From inside the é: on to the next whole character.
	if got := answer(t, v1(h, "GET", "/projects/shop/deploys/23?log_from=2", personal, "", nil)); got["log"] != "llo\nworld\n" || got["log_next"] != 13.0 {
		t.Errorf("from byte 2: %v", got["log"])
	}
	if got := answer(t, v1(h, "GET", "/projects/shop/deploys/23?log_from=99", personal, "", nil)); got["log"] != "" || got["log_next"] != 99.0 {
		t.Errorf("past the end: %v %v", got["log"], got["log_next"])
	}
	// A chunk ends on a whole character: the next answer starts the next.
	exec(t, a, `UPDATE deploys SET log = ? WHERE number = 23`, "a"+strings.Repeat("é", 1<<17))
	if got := answer(t, v1(h, "GET", "/projects/shop/deploys/23", personal, "", nil)); got["log_next"] != float64(256<<10-1) || got["log_size"] != float64(1+2<<17) {
		t.Errorf("a chunk: next %v of %v", got["log_next"], got["log_size"])
	}
	is(t, v1(h, "GET", "/projects/shop/deploys/99", personal, "", nil), 404, `{"error":"no deploy #99 of shop"}`)
}

// A deploy's steps: a hand deploy skips Test, a restore has its own, a
// queued one is all pending, a NO-GO fails where it stopped.
func TestV1StepStates(t *testing.T) {
	a, h := remote(t)
	states := func(n int) string {
		var got struct {
			Steps []struct{ Name, State string }
		}
		json.Unmarshal(v1(h, "GET", fmt.Sprintf("/projects/shop/deploys/%d", n), personal, "", nil).Body.Bytes(), &got)
		var out []string
		for _, s := range got.Steps {
			out = append(out, s.Name+":"+s.State)
		}
		return strings.Join(out, " ")
	}
	deploy(t, a, "shop", 1, "deploy", "no_go", 1, sha1, "", 0)
	exec(t, a, `UPDATE deploys SET step = 'Release' WHERE number = 1`)
	if got := states(1); got != "Test:skipped Secrets:done Build:done Snapshot:done Accessories:done Release:failed Deploy:pending Post-deploy:pending" {
		t.Errorf("a hand deploy's NO-GO: %s", got)
	}
	deploy(t, a, "shop", 2, "restore", "go", 2, sha1, "", 0)
	exec(t, a, `UPDATE deploys SET runner = 'houston-runner-1' WHERE number = 2`)
	if got := states(2); got != "Prepare:done Image:done Accessories:done Restore data:done Safety snapshot:done Switch:done Clean up:done" {
		t.Errorf("a restore's GO: %s", got)
	}
	queue(t, a, "shop", 3, "deploy", time.Now())
	if got := states(3); !strings.HasPrefix(got, "Test:pending Secrets:pending") || strings.Contains(got, "done") {
		t.Errorf("queued: %s", got)
	}
}

// A backup run by id, or the latest.
func TestV1Backups(t *testing.T) {
	a, h := remote(t)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind) VALUES (1, 'nas', 'nfs')`)
	for i := 1; i <= 2; i++ {
		exec(t, a, `INSERT INTO backup_runs (project_id, location_id, kind, reason, status, heartbeat_at) VALUES (1, 1, 'auto', 'manual', 'go', ?)`, time.Now())
	}
	if got := answer(t, v1(h, "GET", "/projects/shop/backups/latest", personal, "", nil)); got["id"] != 2.0 {
		t.Errorf("latest %v", got)
	}
	if got := answer(t, v1(h, "GET", "/projects/shop/backups/1", personal, "", nil)); got["id"] != 1.0 || got["reason"] != "manual" {
		t.Errorf("1 %v", got)
	}
	is(t, v1(h, "GET", "/projects/shop/backups/9", personal, "", nil), 404, `{"error":"no backup 9 of shop"}`)
}

// Settings, storage, a project's volumes, its webhook and its secrets.
func TestV1Reads(t *testing.T) {
	a, h := remote(t)
	is(t, v1(h, "GET", "/settings", personal, "", nil), 200, `{"base_domain":"svnmns.com","time_zone":"UTC"}`)

	exec(t, a, `INSERT INTO storage_locations (id, name, kind, settings, is_default, acknowledged_at, verified_at) VALUES
		(1, 'nas', 'nfs', '{"server":"10.0.1.20","export":"/volume1/houston"}', TRUE, '2026-09-01', '2026-09-01'),
		(2, 'offsite', 's3', '{"bucket":"b","endpoint":"s3.example.com/"}', FALSE, NULL, '2026-09-02'),
		(3, 'unverified', 'local', '{"path":"/srv"}', FALSE, NULL, NULL)`)
	exec(t, a, `INSERT INTO backup_runs (project_id, location_id, kind, reason, status, heartbeat_at, finished_at) VALUES (1, 1, 'auto', 'manual', 'go', ?, '2026-09-30 03:02:00')`, time.Now())
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"name": "blog", "services": []string{"web"}, "variables": []any{}}), nil)
	exec(t, a, `UPDATE projects SET backup_location_id = 2 WHERE name = 'blog'`)
	is(t, v1(h, "GET", "/storage", personal, "", nil), 200, `{"locations":[
		{"name":"nas","kind":"nfs","where":"10.0.1.20:/volume1/houston","live":true,"default":true,"confirmed":true,"used_by":1,
		 "last_write":"2026-09-30T03:02:00Z","pruned_at":null,"prune_error":null},
		{"name":"offsite","kind":"s3","where":"s3:s3.example.com/b/houston","live":false,"default":false,"confirmed":false,"used_by":1,
		 "last_write":null,"pruned_at":null,"prune_error":null}]}`)
	// The default counts the projects without a location only once it's set up.
	exec(t, a, `UPDATE storage_locations SET acknowledged_at = NULL WHERE name = 'nas'`)
	if got := answer(t, v1(h, "GET", "/storage", personal, "", nil)); fmt.Sprint(got["locations"].([]any)[0].(map[string]any)["used_by"]) != "0" {
		t.Errorf("an unconfirmed default: %v", got["locations"])
	}

	exec(t, a, `UPDATE projects SET volumes = '[{"name":"data","path":"/rails/storage"},{"name":"logs","path":"/logs"}]' WHERE name = 'shop'`)
	exec(t, a, `INSERT INTO project_volumes (project_id, name, location_id, placed_at) VALUES (1, 'data', 1, CURRENT_TIMESTAMP)`)
	is(t, v1(h, "GET", "/projects/shop/volumes", personal, "", nil), 200, `{"volumes":[{"name":"data","path":"/rails/storage","location":"nas","placed":true},
		{"name":"logs","path":"/logs","location":null,"placed":false}]}`)

	is(t, v1(h, "GET", "/projects/shop/webhook", personal, "", nil), 422, `{"error":"shop isn't linked to a repo (houston link)"}`)
	exec(t, a, `UPDATE projects SET repo_url = 'git@github.com:scttymn/shop.git', webhook_secret = ? WHERE name = 'shop'`, crypt.Of("whsec"))
	is(t, v1(h, "GET", "/projects/shop/webhook", personal, "", nil), 200, `{"url":"https://hooks.svnmns.com/shop","verified":false,"secret":"whsec"}`)
	exec(t, a, `UPDATE projects SET webhook_verified_at = CURRENT_TIMESTAMP WHERE name = 'shop'`)
	is(t, v1(h, "GET", "/projects/shop/webhook", personal, "", nil), 200, `{"url":"https://hooks.svnmns.com/shop","verified":true,"secret":null}`)

	exec(t, a, `UPDATE projects SET variables = '[{"name":"SECRET_KEY_BASE","required":true},{"name":"OPTIONAL","required":false}]' WHERE name = 'shop'`)
	secret(t, a, "shop", "SECRET_KEY_BASE", "s3cret")
	exec(t, a, `UPDATE secrets SET updated_at = '2026-09-30 12:00:00'`)
	is(t, v1(h, "GET", "/projects/shop/secrets", personal, "", nil), 200, `{"secrets":[
		{"name":"SECRET_KEY_BASE","required":true,"set":true,"updated_at":"2026-09-30T12:00:00Z"},
		{"name":"OPTIONAL","required":false,"set":false,"updated_at":null}]}`)
	for _, path := range []string{"/projects/nope/volumes", "/projects/nope/webhook", "/projects/nope/secrets", "/projects/nope/deploys", "/projects/nope/backups/1"} {
		is(t, v1(h, "GET", path, personal, "", nil), 404, `{"error":"no project nope"}`)
	}
	_ = http.StatusOK
}

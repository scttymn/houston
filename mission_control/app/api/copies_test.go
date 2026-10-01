package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission_control/app"
	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd/dockercmdtest"
)

// storeCompose is shop's compose.yml, renamed store: its branch head once
// shop's deploy held.
const storeCompose = `name: store
services:
  web:
    build: .
    ports: ["3000:3000"]
    environment:
      SECRET_KEY_BASE: ${SECRET_KEY_BASE}
    volumes: [data:/rails/storage]
volumes:
  data:
x-houston:
  health: /up
  domains: [shop.example.com]
`

// copyable is shop, linked, with data and a secret store's compose.yml
// doesn't reference, deployed once, then held: its compose.yml names store.
func copyable(t *testing.T) (*app.App, http.Handler, *dockercmdtest.Fake) {
	t.Helper()
	a, _ := remote(t)
	repo(t, a, storeCompose, true)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, is_default, acknowledged_at) VALUES (1, 'nas', 'nfs', TRUE, CURRENT_TIMESTAMP)`)
	exec(t, a, `UPDATE projects SET repo_url = 'git@github.com:scttymn/shop.git', branch = 'main', compose_path = 'compose.yml', deploy_key_private = ?, deploy_key_public = 'ssh-ed25519 AAAA shop',
		webhook_secret = ?, webhook_verified_at = CURRENT_TIMESTAMP, seen_refs = '{"refs/heads/main":"`+sha1+`"}',
		volumes = '[{"name":"data","path":"/rails/storage"}]'`, crypt.Of("KEY"), crypt.Of("whsec"))
	exec(t, a, `INSERT INTO project_volumes (project_id, name, location_id, placed_at) VALUES (1, 'data', 1, CURRENT_TIMESTAMP), (1, 'cache', 1, NULL)`)
	secret(t, a, "shop", "SECRET_KEY_BASE", "skb")
	secret(t, a, "shop", "OLD_ONE", "old")
	deploy(t, a, "shop", 1, "deploy", "go", 1, sha1, "", 0)
	exec(t, a, `UPDATE deploys SET status = 'hold', proposed_name = 'store' WHERE id = ?`, deploy(t, a, "shop", 2, "deploy", "in_flight", 1, sha1, "", 0))
	return a, a.Handler(), a.DockerCLI.(*dockercmdtest.Fake)
}

// Asking for a copy makes the new project from the branch head, with the
// old one's link, the secrets it references, where its volumes live and
// its backup target, and queues its copy deploy.
func TestV1Copy(t *testing.T) {
	a, h, _ := copyable(t)
	got := answer(t, v1(h, "GET", "/projects/shop", personal, "", nil))
	if b, _ := json.Marshal(got["copy_proposal"]); string(b) != `{"deploy":2,"name":"store","refusal":null,"sha":"`+sha1+`"}` {
		t.Errorf("proposal %s", b)
	}
	is(t, v1(h, "POST", "/projects/shop/copy", personal, `{"confirm":"nope"}`, nil), 422, `{"error":"type shop to confirm"}`)
	w := v1(h, "POST", "/projects/shop/copy", personal, `{"confirm":"shop"}`, nil)
	cp, _ := answer(t, w)["copy"].(map[string]any)
	if w.Code != 202 || cp["from"] != "shop" || cp["to"] != "store" || cp["status"] != "queued" || cp["deploy"] != float64(1) || cp["sha"] != sha1 ||
		cp["by"] != "token laptop" || cp["error"] != nil || cp["handed_over_at"] != nil {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}

	q := models.New(a.DB.Read)
	store, err := q.ProjectByName(t.Context(), "store")
	if err != nil {
		t.Fatal(err)
	}
	shop, _ := q.ProjectByName(t.Context(), "shop")
	if store.RepoUrl != shop.RepoUrl || store.Branch != "main" || store.DeployKeyPrivate.Reveal() != "KEY" || store.WebhookSecret.Reveal() != "whsec" ||
		!store.WebhookVerifiedAt.Valid || store.SeenRefs.V["refs/heads/main"] != sha1 || store.Health != "/up" || strings.Join(store.Domains.V, " ") != "shop.example.com" {
		t.Errorf("store %+v", store)
	}
	if got := hosts(t, a, "store"); got != "store" {
		t.Errorf("hosts %q", got)
	}
	secrets, _ := models.Secrets(t.Context(), q, store.ID)
	if len(secrets) != 1 || secrets["SECRET_KEY_BASE"] != "skb" {
		t.Errorf("secrets %v", secrets)
	}
	var placed string
	a.DB.Read.QueryRow(`SELECT group_concat(name || ':' || location_id || ':' || (placed_at IS NULL)) FROM project_volumes WHERE project_id = ?`, store.ID).Scan(&placed)
	if placed != "data:1:1" {
		t.Errorf("volumes %q", placed)
	}
	d, _ := q.DeployByNumber(t.Context(), models.DeployByNumberParams{ProjectID: store.ID, Number: 1})
	if d.Kind != "copy" || d.Status != "queued" || d.Sha != sha1 || d.Ref != "refs/heads/main" || !strings.Contains(d.SyncPayload.String, `"name":"store"`) {
		t.Errorf("deploy %+v", d)
	}
	var shopDeploys int
	a.DB.Read.QueryRow(`SELECT count(*) FROM deploys WHERE project_id = ?`, shop.ID).Scan(&shopDeploys)
	if shopDeploys != 2 || hosts(t, a, "shop") != "shop shop-db" {
		t.Errorf("shop changed: %d deploys, hosts %q", shopDeploys, hosts(t, a, "shop"))
	}
	is(t, v1(h, "POST", "/projects/shop/copy", personal, `{"confirm":"shop"}`, nil), 422, `{"error":"shop is already being copied to store"}`)

	// Cancelled before the handover: its deploy ends, and store goes.
	is(t, v1(h, "POST", "/projects/shop/copy/cancel", personal, "", nil), 404, `{"error":"shop isn't a copy"}`)
	w = v1(h, "POST", "/projects/store/copy/cancel", personal, "", nil)
	if cp := answer(t, w)["copy"].(map[string]any); w.Code != 200 || cp["status"] != "no_go" || cp["error"] != "cancelled by token laptop" {
		t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
	}
	if d, _ := q.DeployByID(t.Context(), d.ID); d.Status != "no_go" || d.Error != "cancelled by token laptop" || !d.FinishedAt.Valid {
		t.Errorf("deploy %+v", d)
	}
	is(t, v1(h, "POST", "/projects/store/copy/cancel", personal, "", nil), 422, `{"error":"the copy to store is done"}`)
	if pending, _ := a.Jobs.Pending(t.Context()); len(pending) != 1 || pending[0].Name != "copy_cleanup" || pending[0].Queue != "deletions" {
		t.Fatalf("pending %+v", pending)
	}
	a.Jobs.Drain(t.Context())
	var by string
	var deleteBackups bool
	a.DB.Read.QueryRow(`SELECT requested_by, delete_backups FROM project_deletions WHERE name = 'store'`).Scan(&by, &deleteBackups)
	if by != "Houston (the copy of shop was cancelled)" || !deleteBackups {
		t.Errorf("deletion by %q, delete backups %v", by, deleteBackups)
	}
	if _, err := q.ProjectByName(t.Context(), "shop"); err != nil {
		t.Errorf("shop: %v", err)
	}
}

// A copy is asked for only when it can be made.
func TestV1CopyRefused(t *testing.T) {
	a, h, _ := copyable(t)
	count := func() string {
		var n [3]int
		a.DB.Read.QueryRow(`SELECT (SELECT count(*) FROM projects), (SELECT count(*) FROM project_copies), (SELECT count(*) FROM deploys)`).Scan(&n[0], &n[1], &n[2])
		b, _ := json.Marshal(n)
		return string(b)
	}
	before := count()
	refused := func(body string) {
		t.Helper()
		is(t, v1(h, "POST", "/projects/shop/copy", personal, `{"confirm":"shop"}`, nil), 422, body)
		if after := count(); after != before {
			t.Errorf("%s: made something: %s, was %s", body, after, before)
		}
	}
	is(t, v1(h, "POST", "/projects/nope/copy", personal, `{"confirm":"nope"}`, nil), 404, `{"error":"no project nope"}`)
	is(t, v1(h, "POST", "/projects/shop/copy", "", `{"confirm":"shop"}`, nil), 401, `{"error":"the API token is missing, wrong or revoked"}`)

	repo(t, a, strings.Replace(storeCompose, "name: store", "name: store2", 1), true)
	h = a.Handler()
	refused(`{"error":"shop's compose.yml on main now names store2, not store: wait for its deploy to hold, then copy"}`)
	repo(t, a, strings.Replace(storeCompose, "health: /up", "health: no-slash", 1), true)
	h = a.Handler()
	w := v1(h, "POST", "/projects/shop/copy", personal, `{"confirm":"shop"}`, nil)
	if w.Code != 422 || count() != before {
		t.Errorf("an invalid compose.yml = %d %s", w.Code, w.Body.String())
	}
	a.Git.Run = func(ctx context.Context, argv []string, env map[string]string) dockercmd.Result {
		if slices.Contains(argv, "clone") {
			return dockercmd.Result{Output: "fatal: Could not read from remote repository.\n", Code: 128}
		}
		return dockercmd.Result{OK: true}
	}
	h = a.Handler()
	w = v1(h, "POST", "/projects/shop/copy", personal, `{"confirm":"shop"}`, nil)
	if w.Code != 422 || !strings.HasPrefix(fmt.Sprint(answer(t, w)["error"]), "couldn't read git@github.com:scttymn/shop.git: ") {
		t.Errorf("unreadable = %d %s", w.Code, w.Body.String())
	}
	repo(t, a, storeCompose, true)
	h = a.Handler()

	exec(t, a, `INSERT INTO projects (name, app_service, services, domains, health, port) VALUES ('store', 'web', '["web"]', '[]', '/', 80)`)
	before = count()
	refused(`{"error":"store is another project"}`)
	exec(t, a, `DELETE FROM projects WHERE name = 'store'`)
	exec(t, a, `INSERT INTO projects (id, name, app_service, services, domains, health, port) VALUES (9, 'elsewhere', 'web', '["web"]', '[]', '/', 80)`)
	exec(t, a, `INSERT INTO project_hosts (project_id, name) VALUES (9, 'store')`)
	before = count()
	refused(`{"error":"the container name store belongs to project elsewhere; rename a service or the project"}`)
	exec(t, a, `DELETE FROM projects WHERE id = 9`)
	before = count()

	queued := deploy(t, a, "shop", 3, "deploy", "queued", 1, sha1, "", 0)
	before = count()
	refused(`{"error":"deploy #3 is queued; wait for #3"}`)
	exec(t, a, `UPDATE deploys SET status = 'hold', proposed_name = 'store' WHERE id = ?`, queued)
	exec(t, a, `INSERT INTO backup_runs (project_id, location_id, kind, reason, status, heartbeat_at) VALUES (1, 1, 'auto', 'manual', 'running', ?)`, time.Now())
	refused(`{"error":"a backup of shop is running; wait for it"}`)
	exec(t, a, `UPDATE backup_runs SET status = 'go'`)
	exec(t, a, `UPDATE storage_locations SET is_default = FALSE`)
	refused(`{"error":"no backup storage for the copy's snapshot of shop's data (finish setup's storage step)"}`)
	exec(t, a, `UPDATE storage_locations SET is_default = TRUE`)
	exec(t, a, `UPDATE deploys SET proposed_name = 'hooks' WHERE id = ?`, queued)
	refused(`{"error":"hooks can't be a project's name"}`)
	exec(t, a, `UPDATE deploys SET status = 'go' WHERE id = ?`, queued)
	refused(`{"error":"shop's latest deploy doesn't propose a copy: change name: in compose.yml, push, and its deploy holds with the new name"}`)
}

// kamalProxy is kamal-proxy's state: shop serving its hosts, store on its
// placeholder (or the hosts it took).
func kamalProxy(fake *dockercmdtest.Fake, shop, store []string) {
	service := func(name string, hosts []string, target string) map[string]any {
		return map[string]any{"name": name, "options": map[string]any{"hosts": hosts}, "active_targets": []string{target},
			"target_options": map[string]any{"health_check_config": map[string]any{"path": "/up", "interval": 1e9, "timeout": 5e9}, "response_timeout": 3e10}}
	}
	state, _ := json.Marshal([]any{service("shop-web", shop, "aaa:80"), service("store-web", store, "bbb:80")})
	fake.On(dockercmdtest.OK(string(state)), "exec", "kamal-proxy", "cat")
}

// copying is shop's copy to store asked for and claimed: its deploy, and
// the token that owns it.
func copying(t *testing.T) (*app.App, http.Handler, *dockercmdtest.Fake, int64, string) {
	t.Helper()
	a, h, fake := copyable(t)
	if w := v1(h, "POST", "/projects/shop/copy", personal, `{"confirm":"shop"}`, nil); w.Code != 202 {
		t.Fatalf("copy = %d %s", w.Code, w.Body.String())
	}
	j := claimed(t, claim(h, `{"runner":"houston-runner-1","wait":0}`))
	if b, _ := json.Marshal(j.Deploy["copy"]); j.Deploy["kind"] != "copy" ||
		string(b) != `{"exclude_hosts":["shop.example.com"],"from":"shop","placeholder":"store.houston-copy.invalid"}` {
		t.Fatalf("job %v", j.Deploy)
	}
	return a, h, fake, int64(j.Deploy["id"].(float64)), j.Deploy["token"].(string)
}

func copyRow(t *testing.T, a *app.App) models.ProjectCopy {
	t.Helper()
	c, err := models.New(a.DB.Read).CopyByID(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The copy's data: a late snapshot of the old project when its runner asks,
// then that snapshot restored into the new project.
func TestCopyData(t *testing.T) {
	a, h, _, id, token := copying(t)
	if c := copyRow(t, a); c.Status != "running" {
		t.Errorf("claimed copy %q", c.Status)
	}
	w := ask(h, "POST", id, "copy_data", token)
	snap := answer(t, w)
	if w.Code != 202 || snap["status"] != "queued" || snap["reason"] != "copy" || snap["kind"] != "deploy" {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	run, _ := models.New(a.DB.Read).BackupRunByID(t.Context(), int64(snap["id"].(float64)))
	if run.ProjectID != 1 || run.LocationID != 1 || copyRow(t, a).SnapshotRunID.Int64 != run.ID {
		t.Errorf("snapshot %+v", run)
	}
	if pending, _ := a.Jobs.Pending(t.Context()); len(pending) != 1 || pending[0].Queue != "snapshots" {
		t.Errorf("pending %+v", pending)
	}
	if again := answer(t, ask(h, "POST", id, "copy_data", token)); again["id"] != snap["id"] {
		t.Errorf("a retried POST got %v", again)
	}
	if got := answer(t, ask(h, "GET", id, "copy_data", token)); got["status"] != "queued" || got["id"] != snap["id"] {
		t.Errorf("status %v", got)
	}

	exec(t, a, `UPDATE backup_runs SET status = 'go', snapshot_id = ?, sha = ? WHERE id = ?`, strings.Repeat("5c", 32), sha1, run.ID)
	restore := answer(t, ask(h, "GET", id, "copy_data", token))
	if restore["id"] == snap["id"] || restore["status"] != "queued" || restore["reason"] != "restore" || restore["deploy"] != float64(1) {
		t.Fatalf("restore %v", restore)
	}
	r, _ := models.New(a.DB.Read).BackupRunByID(t.Context(), int64(restore["id"].(float64)))
	d, _ := models.New(a.DB.Read).DeployByID(t.Context(), id)
	if r.ProjectID != 2 || r.Operation != "restore" || r.SourceSnapshotID != strings.Repeat("5c", 32) || r.LocationID != 1 ||
		d.SourceSnapshotID != r.SourceSnapshotID || d.SourceLocationID.Int64 != 1 {
		t.Errorf("restore %+v, deploy %+v", r, d)
	}
	if again := answer(t, ask(h, "GET", id, "copy_data", token)); again["id"] != restore["id"] {
		t.Errorf("asked twice: %v", again)
	}
	exec(t, a, `UPDATE backup_runs SET status = 'go' WHERE id = ?`, r.ID)
	if got := answer(t, ask(h, "GET", id, "copy_data", token)); got["status"] != "go" {
		t.Errorf("done %v", got)
	}

	// The snapshot failing is the copy's error.
	exec(t, a, `UPDATE backup_runs SET status = 'no_go', error = 'pg_dump failed' WHERE id = ?`, run.ID)
	if got := answer(t, ask(h, "GET", id, "copy_data", token)); got["status"] != "no_go" || got["error"] != "the snapshot of shop failed: pg_dump failed" {
		t.Errorf("failed %v", got)
	}
	exec(t, a, `UPDATE backup_runs SET status = 'skipped', error = 'nothing to back up' WHERE id = ?`, run.ID)
	is(t, ask(h, "GET", id, "copy_data", token), 200, `{"id":0,"status":"skipped","error":"nothing to back up"}`)

	// Not the copy's token, not a copy.
	is(t, ask(h, "GET", id, "copy_data", "nope"), 403, `{"error":"that token isn't this deploy's"}`)
	exec(t, a, `UPDATE deploys SET kind = 'deploy' WHERE id = ?`, id)
	is(t, ask(h, "POST", id, "copy_data", token), 422, `{"error":"deploy #1 isn't a copy"}`)
}

// With no data to copy, there's nothing to wait for.
func TestCopyDataSkipped(t *testing.T) {
	a, h, _, id, token := copying(t)
	is(t, ask(h, "GET", id, "copy_data", token), 200, `{"id":0,"status":"queued","error":null}`)
	exec(t, a, `UPDATE projects SET volumes = '[]' WHERE name = 'shop'`)
	is(t, ask(h, "POST", id, "copy_data", token), 202, `{"id":0,"status":"skipped","error":"shop has no data to copy"}`)
	is(t, ask(h, "GET", id, "copy_data", token), 200, `{"id":0,"status":"skipped","error":"shop has no data to copy"}`)
	exec(t, a, `UPDATE projects SET volumes = '[{"name":"data","path":"/d"}]' WHERE name = 'shop'`)
	exec(t, a, `UPDATE deploys SET status = 'no_go' WHERE project_id = 1`)
	is(t, ask(h, "POST", id, "copy_data", token), 202, `{"id":0,"status":"skipped","error":"shop has never been deployed: no data to copy"}`)
	exec(t, a, `UPDATE project_copies SET from_project_id = NULL`)
	is(t, ask(h, "POST", id, "copy_data", token), 202, `{"id":0,"status":"skipped","error":"shop is gone: its data can't be copied"}`)
	exec(t, a, `UPDATE project_copies SET from_project_id = 1`)
	exec(t, a, `UPDATE deploys SET status = 'go' WHERE project_id = 1 AND number = 1`)
	exec(t, a, `UPDATE storage_locations SET is_default = FALSE`)
	is(t, ask(h, "POST", id, "copy_data", token), 202, `{"id":0,"status":"no_go","error":"no backup storage for the copy's snapshot"}`)
}

// The handover, asked for by the copy's runner: the hosts shop and store
// share become store's.
func TestCopyHandover(t *testing.T) {
	a, h, fake, id, token := copying(t)
	kamalProxy(fake, []string{"shop.svnmns.com", "shop.example.com"}, []string{"store.houston-copy.invalid"})
	is(t, ask(h, "POST", id, "handover", token), 200, `{"handed_over":["shop.example.com"]}`)
	if c := copyRow(t, a); strings.Join(c.HandedOver.V, " ") != "shop.example.com" || !c.HandedOverAt.Valid {
		t.Errorf("copy %+v", c)
	}
	var script string
	for _, c := range fake.Calls() {
		if len(c.Args) > 2 && c.Args[2] == "sh" {
			script = c.Args[len(c.Args)-1]
		}
	}
	if !strings.Contains(script, "kamal-proxy deploy store-web --target bbb:80 --host store.svnmns.com --host shop.example.com ") {
		t.Errorf("script %s", script)
	}
	// Once it has taken hosts, it isn't cancelled.
	is(t, v1(h, "POST", "/projects/store/copy/cancel", personal, "", nil), 422, `{"error":"store has taken over shop's hosts; undo the copy instead"}`)

	fake.On(dockercmdtest.Fail(1, "Error: host settings conflict with another service"), "exec", "kamal-proxy", "sh")
	w := ask(h, "POST", id, "handover", token)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "conflict") {
		t.Errorf("refused = %d %s", w.Code, w.Body.String())
	}
	is(t, ask(h, "POST", id, "handover", "nope"), 403, `{"error":"that token isn't this deploy's"}`)
	exec(t, a, `UPDATE deploys SET status = 'no_go' WHERE id = ?`, id)
	is(t, ask(h, "POST", id, "handover", token), 409, `{"error":"copy #1 is no longer in flight"}`)
}

// A copy's deploy settles its copy: GO, or NO-GO, when the new project goes
// unless it already serves the hosts it took.
func TestCopySettles(t *testing.T) {
	a, h, _, id, token := copying(t)
	exec(t, a, `UPDATE deploys SET log = 'Build…'||char(10) WHERE id = ?`, id)
	if w := report(h, id, token, `{"status":"no_go","error":"kamal deploy failed"}`); w.Code != 200 {
		t.Fatalf("report = %d %s", w.Code, w.Body.String())
	}
	if c := copyRow(t, a); c.Status != "no_go" || c.Error != "kamal deploy failed" || c.Log != "Build…\n" {
		t.Errorf("copy %+v", c)
	}
	a.Jobs.Drain(t.Context())
	var by string
	a.DB.Read.QueryRow(`SELECT requested_by FROM project_deletions WHERE name = 'store'`).Scan(&by)
	if by != "Houston (the copy of shop failed)" {
		t.Errorf("deletion by %q", by)
	}

	// After the handover, a failed copy keeps its new project.
	a, h, _, id, token = copying(t)
	exec(t, a, `UPDATE project_copies SET handed_over = '["shop.example.com"]', handed_over_at = CURRENT_TIMESTAMP`)
	report(h, id, token, `{"status":"no_go","error":"stopped"}`)
	if c := copyRow(t, a); c.Status != "no_go" || c.Error != "stopped; it serves the hosts it took, so store is kept" {
		t.Errorf("copy %+v", c)
	}
	if n := pendingJobs(t, a); n != 0 {
		t.Errorf("%d jobs pending", n)
	}
	// A clean-up queued before a handover took hosts leaves it be.
	a.CopyCleanUp.Enqueue(t.Context(), models.CopyArgs{ID: 1})
	a.Jobs.Drain(t.Context())
	var deletions int
	a.DB.Read.QueryRow(`SELECT count(*) FROM project_deletions`).Scan(&deletions)
	if deletions != 0 {
		t.Errorf("%d deletions", deletions)
	}

	a, h, _, id, token = copying(t)
	report(h, id, token, `{"status":"go"}`)
	if c := copyRow(t, a); c.Status != "go" {
		t.Errorf("copy %+v", c)
	}
}

// A copy's deploy gone silent is abandoned by the next claim, and its new
// project goes.
func TestCopyAbandoned(t *testing.T) {
	a, h, _, id, _ := copying(t)
	exec(t, a, `UPDATE deploys SET heartbeat_at = ? WHERE id = ?`, time.Now().Add(-time.Hour), id)
	if w := claim(h, `{"runner":"houston-runner-1","wait":0}`); w.Code != 204 {
		t.Fatalf("claim = %d %s", w.Code, w.Body.String())
	}
	if c := copyRow(t, a); c.Status != "no_go" || !strings.HasPrefix(c.Error, "abandoned: no word from houston deploy since ") {
		t.Errorf("copy %+v", c)
	}
	if pending, _ := a.Jobs.Pending(t.Context()); len(pending) != 1 || pending[0].Name != "copy_cleanup" {
		t.Errorf("pending %+v", pending)
	}
}

// A clean-up the new project's restore holds up is tried again.
func TestCopyCleanUpWaits(t *testing.T) {
	a, h, _, id, token := copying(t)
	exec(t, a, `INSERT INTO backup_runs (project_id, location_id, operation, kind, reason, deploy_number, status, heartbeat_at)
		VALUES (2, 1, 'restore', 'restore', 'restore', 1, 'running', ?)`, time.Now())
	report(h, id, token, `{"status":"no_go","error":"x"}`)
	a.Jobs.Drain(t.Context())
	if pending, _ := a.Jobs.Pending(t.Context()); len(pending) != 1 || pending[0].Name != "copy_cleanup" {
		t.Errorf("pending %+v", pending)
	}
	var n int
	a.DB.Read.QueryRow(`SELECT count(*) FROM project_deletions`).Scan(&n)
	if n != 0 {
		t.Errorf("%d deletions", n)
	}
}

// Undo: after a GO copy, the hosts go back to the old project, then the
// new one is deleted, its backups kept.
func TestV1UndoCopy(t *testing.T) {
	a, h, fake, id, token := copying(t)
	is(t, v1(h, "POST", "/projects/store/copy/undo", personal, `{"confirm":"store"}`, nil), 422, `{"error":"the copy to store isn't done"}`)
	report(h, id, token, `{"status":"go"}`)
	exec(t, a, `UPDATE project_copies SET handed_over = '["shop.example.com"]', handed_over_at = CURRENT_TIMESTAMP`)
	kamalProxy(fake, []string{"shop.svnmns.com"}, []string{"store.svnmns.com", "shop.example.com"})

	is(t, v1(h, "POST", "/projects/store/copy/undo", personal, `{"confirm":"nope"}`, nil), 422, `{"error":"type store to confirm"}`)
	w := v1(h, "POST", "/projects/store/copy/undo", personal, `{"confirm":"store"}`, nil)
	got := answer(t, w)
	deletion, _ := got["deletion"].(map[string]any)
	cp, _ := got["copy"].(map[string]any)
	if w.Code != 202 || deletion["name"] != "store" || deletion["delete_backups"] != false || deletion["by"] != "token laptop" ||
		deletion["log"] != nil || cp["undone_at"] == nil || cp["handed_over_at"] != nil {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	var script string
	for _, c := range fake.Calls() {
		if len(c.Args) > 2 && c.Args[2] == "sh" {
			script = c.Args[len(c.Args)-1]
		}
	}
	if !strings.Contains(script, "kamal-proxy deploy shop-web --target aaa:80 --host shop.svnmns.com --host shop.example.com ") {
		t.Errorf("script %s", script)
	}
}

func TestV1UndoCopyRefused(t *testing.T) {
	a, h, fake, id, token := copying(t)
	report(h, id, token, `{"status":"go"}`)
	exec(t, a, `UPDATE project_copies SET handed_over = '["shop.example.com"]', handed_over_at = CURRENT_TIMESTAMP`)
	fake.On(dockercmdtest.OK(`[{"name":"store-web","options":{"hosts":["store.svnmns.com"]}}]`), "exec", "kamal-proxy", "cat")
	is(t, v1(h, "POST", "/projects/store/copy/undo", personal, `{"confirm":"store"}`, nil), 422,
		`{"error":"the hosts didn't go back to shop: shop-web isn't in kamal-proxy any more: its container is gone"}`)
	var n int
	a.DB.Read.QueryRow(`SELECT count(*) FROM project_deletions`).Scan(&n)
	if n != 0 {
		t.Errorf("%d deletions", n)
	}
	exec(t, a, `UPDATE project_copies SET from_project_id = NULL`)
	is(t, v1(h, "POST", "/projects/store/copy/undo", personal, `{"confirm":"store"}`, nil), 422, `{"error":"shop is deleted: there's nothing to go back to"}`)
}

// Once the old project is deleted, its webhook rings the copy that went: the
// git host's webhook, set up for the old name, keeps working.
func TestWebhookRingsTheCopy(t *testing.T) {
	a, h, _, id, token := copying(t)
	report(h, id, token, `{"status":"go"}`)
	hook := http.Header{"X-Houston-Token": {"whsec"}}
	empty(t, ring(h, "shop", "140.82.112.1", "{}", hook), 202, "the old project, still there")
	exec(t, a, `UPDATE project_copies SET from_project_id = NULL`) // as a deletion leaves it
	exec(t, a, `UPDATE projects SET name = 'gone' WHERE name = 'shop'`)
	empty(t, ring(h, "shop", "140.82.112.1", "{}", hook), 202, "the copy, once it's gone")
	pending, _ := a.Jobs.Pending(t.Context())
	var args models.CheckArgs
	json.Unmarshal(pending[len(pending)-1].Args, &args)
	if args.ProjectID != 2 {
		t.Errorf("checked %+v", pending[len(pending)-1])
	}
	exec(t, a, `UPDATE project_copies SET status = 'no_go'`)
	empty(t, ring(h, "shop", "140.82.112.1", "{}", hook), 404, "a copy that didn't go")
}

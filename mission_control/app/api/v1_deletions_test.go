package api_test

import (
	"strconv"
	"testing"
	"time"
)

// A deletion is asked for: confirmed, with nothing else underway; one
// stopped partway resumes; the project says so while it's held, and
// nothing new starts for it.
func TestV1DeleteProject(t *testing.T) {
	a, h := remote(t)
	is(t, v1(h, "DELETE", "/projects/shop", personal, `{"confirm":"nope"}`, nil), 422, `{"error":"type shop to confirm"}`)
	busy := deploy(t, a, "shop", 1, "deploy", "in_flight", 1, sha1, "tok", 0)
	is(t, v1(h, "DELETE", "/projects/shop", personal, `{"confirm":"shop"}`, nil), 422, `{"error":"deploy #1 is in flight; wait for #1"}`)
	exec(t, a, `UPDATE deploys SET status = 'go' WHERE id = ?`, busy)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind) VALUES (1, 'nas', 'nfs')`)
	exec(t, a, `INSERT INTO backup_runs (project_id, location_id, kind, reason, status, heartbeat_at) VALUES (1, 1, 'auto', 'manual', 'running', ?)`, time.Now())
	is(t, v1(h, "DELETE", "/projects/shop", personal, `{"confirm":"shop"}`, nil), 422, `{"error":"a backup of shop is running; wait for it"}`)
	exec(t, a, `UPDATE backup_runs SET status = 'go'`)

	w := v1(h, "DELETE", "/projects/shop", personal, `{"confirm":"shop","delete_backups":true}`, nil)
	got := answer(t, w)
	deletion, _ := got["deletion"].(map[string]any)
	if w.Code != 202 || deletion["status"] != "queued" || deletion["by"] != "token laptop" || deletion["delete_backups"] != true || deletion["name"] != "shop" {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	if pending, _ := a.Jobs.Pending(t.Context()); len(pending) != 1 || pending[0].Name != "delete_project" || pending[0].Queue != "deletions" {
		t.Errorf("pending %+v", pending)
	}
	is(t, v1(h, "DELETE", "/projects/shop", personal, `{"confirm":"shop"}`, nil), 422, `{"error":"shop is already being deleted"}`)
	id := int(deletion["id"].(float64))
	if got := answer(t, v1(h, "GET", "/deletions/"+strconv.Itoa(id), personal, "", nil)); got["status"] != "queued" || got["log"] != "" {
		t.Errorf("deletion %v", got)
	}
	is(t, v1(h, "GET", "/deletions/999", personal, "", nil), 404, `{"error":"no deletion 999"}`)
	if got := answer(t, v1(h, "GET", "/projects/shop", personal, "", nil)); got["deleting"] == nil || got["deleting"].(map[string]any)["log"] != nil {
		t.Errorf("the project's deleting: %v", got["deleting"])
	}

	// Held: sync, a deploy, a backup, a restore, the claim.
	is(t, post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}}), nil), 409, `{"error":"shop is being deleted"}`)
	is(t, post(h, "POST", "/api/projects/shop/deploys", `{"sha":"`+sha1+`","ref":"main"}`, nil), 409, `{"error":"shop is being deleted"}`)
	is(t, v1(h, "POST", "/projects/shop/backups", personal, "", nil), 422, `{"error":"shop is being deleted"}`)
	is(t, v1(h, "POST", "/projects/shop/restores", personal, `{"snapshot":"x","confirm":"shop"}`, nil), 422, `{"error":"shop is being deleted"}`)
	queue(t, a, "shop", 2, "deploy", time.Now())
	if w := claim(h, `{"runner":"houston-runner-1","wait":0}`); w.Code != 204 {
		t.Errorf("claimed a deleting project's deploy: %d", w.Code)
	}

	// Stopped during removal: held still; asking again resumes it.
	exec(t, a, `UPDATE deploys SET status = 'no_go' WHERE number = 2`) // a queued deploy would be in the way
	exec(t, a, `UPDATE project_deletions SET status = 'no_go', removing_at = CURRENT_TIMESTAMP, error = 'stopped at volumes: x'`)
	is(t, post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}}), nil), 409, `{"error":"shop is being deleted"}`)
	w = v1(h, "DELETE", "/projects/shop", personal, `{"confirm":"shop"}`, nil)
	if got := answer(t, w)["deletion"].(map[string]any); w.Code != 202 || got["id"] != float64(id) || got["status"] != "queued" || got["error"] != nil {
		t.Errorf("resumed %d %s", w.Code, w.Body.String())
	}
}

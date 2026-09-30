package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/scttymn/houston/mission-control-go/app"
)

// withStorage is shop synced, with backup storage set up (nas, the
// default) and a GO deploy: something to snapshot.
func withStorage(t *testing.T) (*app.App, http.Handler) {
	t.Helper()
	a, h := synced(t)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, is_default, acknowledged_at) VALUES (1, 'nas', 'nfs', TRUE, ?)`, time.Now())
	deploy(t, a, "shop", 1, "deploy", "go", 1, sha1, "", 0)
	return a, h
}

func ask(h http.Handler, method string, id int64, what, token string) *httptest.ResponseRecorder {
	return post(h, method, fmt.Sprintf("/api/deploys/%d/%s", id, what), "", http.Header{"X-Houston-Deploy-Token": {token}})
}

func pendingJobs(t *testing.T, a *app.App) int {
	t.Helper()
	pending, err := a.Jobs.Pending(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return len(pending)
}

// A deploy asks for its snapshot while it's in flight: one per deploy, its
// job queued once; it can ask where it stands.
func TestSnapshots(t *testing.T) {
	a, h := withStorage(t)
	id := deploy(t, a, "shop", 2, "deploy", "in_flight", 1, sha1, "tok", 0)
	is(t, ask(h, "GET", id, "snapshot", "tok"), 404, `{"error":"deploy #2 has no snapshot"}`)

	w := ask(h, "POST", id, "snapshot", "tok")
	run := answer(t, w)
	if w.Code != 202 || run["status"] != "queued" || run["kind"] != "deploy" || run["reason"] != "deploy" || run["deploy"] != 2.0 || run["bytes"] != nil {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	again := answer(t, ask(h, "POST", id, "snapshot", "tok"))
	if again["id"] != run["id"] || pendingJobs(t, a) != 1 {
		t.Errorf("a retry: %v, %d jobs", again["id"], pendingJobs(t, a))
	}
	if got := answer(t, ask(h, "GET", id, "snapshot", "tok")); got["id"] != run["id"] {
		t.Errorf("GET %v", got)
	}

	// Running and heard from: running.
	exec(t, a, `UPDATE backup_runs SET status = 'running', heartbeat_at = ?`, time.Now())
	if got := answer(t, ask(h, "GET", id, "snapshot", "tok")); got["status"] != "running" || got["error"] != "" {
		t.Errorf("running %v", got)
	}

	// Silent while running: shown as the NO-GO it will be.
	exec(t, a, `UPDATE backup_runs SET status = 'running', heartbeat_at = ?`, time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC))
	if got := answer(t, ask(h, "GET", id, "snapshot", "tok")); got["status"] != "no_go" ||
		got["error"] != "Mission Control stopped during the backup (no word since 2026-09-30T08:00:00Z)" {
		t.Errorf("stale %v", got)
	}

	is(t, ask(h, "POST", id, "snapshot", "wrong"), 403, `{"error":"that token isn't this deploy's"}`)
	is(t, ask(h, "POST", 999, "snapshot", "tok"), 404, `{"error":"no such deploy"}`)
	exec(t, a, `UPDATE deploys SET status = 'no_go' WHERE id = ?`, id)
	is(t, ask(h, "POST", id, "snapshot", "tok"), 409, `{"error":"deploy #2 is no longer in flight"}`)

	// A restore's is its safety snapshot.
	restore := deploy(t, a, "shop", 3, "restore", "in_flight", 2, sha1, "rt", 0)
	if got := answer(t, ask(h, "POST", restore, "snapshot", "rt")); got["reason"] != "restore" || got["kind"] != "deploy" {
		t.Errorf("a restore's %v", got)
	}
}

// Before anything served there's nothing to hold (skipped, 200); without
// backup storage, a snapshot can't be taken.
func TestSnapshotsRefuse(t *testing.T) {
	a, h := synced(t)
	id := deploy(t, a, "shop", 1, "deploy", "in_flight", 1, sha1, "tok", 0)
	is(t, ask(h, "POST", id, "snapshot", "tok"), 200, `{"status":"skipped","error":"nothing deployed yet"}`)
	deploy(t, a, "shop", 2, "deploy", "go", 1, sha1, "", 0)
	is(t, ask(h, "POST", id, "snapshot", "tok"), 422, `{"error":"no backup storage yet (finish setup's storage step)"}`)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, is_default) VALUES (1, 'nas', 'nfs', TRUE)`)
	is(t, ask(h, "POST", id, "snapshot", "tok"), 422, `{"error":"no backup storage yet (finish setup's storage step)"}`)

	// A project's own location, once set up, before the default.
	exec(t, a, `UPDATE storage_locations SET acknowledged_at = ?`, time.Now())
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, acknowledged_at) VALUES (2, 'offsite', 's3', ?)`, time.Now())
	exec(t, a, `UPDATE projects SET backup_location_id = 2`)
	ask(h, "POST", id, "snapshot", "tok")
	var location int
	a.DB.Read.QueryRow(`SELECT location_id FROM backup_runs`).Scan(&location)
	if location != 2 {
		t.Errorf("location %d", location)
	}
}

// A restore asks for its data, from its snapshot's location, once.
func TestRestoreData(t *testing.T) {
	a, h := withStorage(t)
	restore := deploy(t, a, "shop", 2, "restore", "in_flight", 2, sha1, "rt", 0)
	exec(t, a, `UPDATE deploys SET source_location_id = 1, source_snapshot_id = 'abcd1234' WHERE id = ?`, restore)
	is(t, ask(h, "GET", restore, "restore_data", "rt"), 404, `{"error":"restore #2 hasn't asked for its data"}`)
	w := ask(h, "POST", restore, "restore_data", "rt")
	run := answer(t, w)
	if w.Code != 202 || run["kind"] != "restore" || run["reason"] != "restore" || run["deploy"] != 2.0 {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	var operation, source string
	var location int
	a.DB.Read.QueryRow(`SELECT operation, source_snapshot_id, location_id FROM backup_runs`).Scan(&operation, &source, &location)
	if operation != "restore" || source != "abcd1234" || location != 1 {
		t.Errorf("row %s %s %d", operation, source, location)
	}
	if again := answer(t, ask(h, "POST", restore, "restore_data", "rt")); again["id"] != run["id"] || pendingJobs(t, a) != 1 {
		t.Errorf("a retry: %v", again)
	}

	exec(t, a, `UPDATE deploys SET status = 'go' WHERE id = ?`, restore)
	is(t, ask(h, "GET", restore, "restore_data", "rt"), 409, `{"error":"restore #2 is no longer in flight"}`)
	plain := deploy(t, a, "shop", 3, "deploy", "in_flight", 2, sha1, "dt", 0)
	is(t, ask(h, "POST", plain, "restore_data", "dt"), 422, `{"error":"deploy #3 isn't a restore"}`)
}

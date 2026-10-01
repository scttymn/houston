package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission_control/app"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission_control/app/services/gitremote"
)

// withGit gives the app a git whose ls-remote answers refs, and whose
// fetch of a commit answers fetched (its rev-parse the commit asked for).
func withGit(t *testing.T, a *app.App, refs string, fetched bool) {
	t.Helper()
	a.Git = gitremote.Git{KnownHosts: "/data/known_hosts", TempDir: t.TempDir(), Run: func(ctx context.Context, argv []string, env map[string]string) dockercmd.Result {
		line := strings.Join(argv, " ")
		switch {
		case strings.Contains(line, "ls-remote"):
			return dockercmd.Result{OK: true, Output: refs}
		case strings.Contains(line, " fetch ") && !fetched:
			return dockercmd.Result{Output: "fatal: remote error: upload-pack: not our ref\n", Code: 128}
		case strings.Contains(line, "rev-parse"):
			return dockercmd.Result{OK: true, Output: sha1}
		}
		return dockercmd.Result{OK: true}
	}}
}

// Deploy now: the head of what the rule deploys, queued.
func TestV1DeployNow(t *testing.T) {
	a, _ := remote(t)
	withGit(t, a, sha1+"\trefs/heads/main\n", true)
	h := a.Handler()
	is(t, v1(h, "POST", "/projects/shop/deploys", personal, "", nil), 422, `{"error":"link the repo first (houston link)"}`)
	exec(t, a, `UPDATE projects SET repo_url = 'git@github.com:scttymn/shop.git', deploy_key_private = ?`, crypt.Of("KEY"))
	w := v1(h, "POST", "/projects/shop/deploys", personal, `{"fresh":true}`, nil)
	got := answer(t, w)
	if w.Code != 200 || got["status"] != "queued" || got["sha"] != sha1 || got["ref"] != "refs/heads/main" || got["fresh"] != true {
		t.Errorf("= %d %s", w.Code, w.Body.String())
	}
	exec(t, a, `UPDATE projects SET deploy_rule = '{"on":"push","branch":"gone"}'`)
	is(t, v1(h, "POST", "/projects/shop/deploys", personal, "", nil), 502, `{"error":"nothing in the repo matches the deploy rule (every commit to gone)"}`)
}

// A restore is requested only when everything it needs is there: the
// confirmation, a linked repo, nothing else underway, the snapshot in a
// location the project backed up to, and its commit still in the repo.
func TestV1RequestRestore(t *testing.T) {
	a, _, fake := withSnapshots(t)
	withGit(t, a, "", true)
	h := a.Handler()
	fake.On(dockercmdtest.OK(`[{"id":"aaaa1111","short_id":"aaaa","time":"2026-09-29T03:00:00Z","tags":["project:shop","kind:auto","sha:`+sha1+`"]}]`), listArgs...)
	exec(t, a, `INSERT INTO backup_runs (project_id, location_id, kind, reason, status, heartbeat_at) VALUES (1, 1, 'auto', 'manual', 'go', CURRENT_TIMESTAMP)`)
	body := func(extra string) string { return `{"snapshot":"aaaa","confirm":"shop"` + extra + `}` }

	is(t, v1(h, "POST", "/projects/shop/restores", personal, `{"snapshot":"aaaa","confirm":"nope"}`, nil), 422, `{"error":"type shop to confirm"}`)
	is(t, v1(h, "POST", "/projects/shop/restores", personal, body(""), nil), 422, `{"error":"link the repo first (houston link): a restore fetches the snapshot's commit from it"}`)
	exec(t, a, `UPDATE projects SET repo_url = 'git@github.com:scttymn/shop.git', deploy_key_private = ?`, crypt.Of("KEY"))
	busy := deploy(t, a, "shop", 1, "deploy", "in_flight", 1, sha1, "t", 0)
	is(t, v1(h, "POST", "/projects/shop/restores", personal, body(""), nil), 422, `{"error":"deploy #1 is in flight; wait for #1"}`)
	exec(t, a, `UPDATE deploys SET status = 'go' WHERE id = ?`, busy)
	is(t, v1(h, "POST", "/projects/shop/restores", personal, body(`,"location":"offsite"`), nil), 422, `{"error":"shop never backed up to offsite"}`)
	is(t, v1(h, "POST", "/projects/shop/restores", personal, body(`,"location":"nope"`), nil), 422, `{"error":"shop never backed up to that location"}`)
	is(t, v1(h, "POST", "/projects/shop/restores", personal, `{"snapshot":"zzzz","confirm":"shop"}`, nil), 422, `{"error":"snapshot zzzz isn't in nas's snapshots of shop"}`)
	exec(t, a, `INSERT INTO projects (id, name, app_service, services, health, port) VALUES (9, 'other', 'web', '["web"]', '/', 80)`)
	exec(t, a, `INSERT INTO project_hosts (project_id, name) VALUES (9, 'shop-db-g2')`)
	is(t, v1(h, "POST", "/projects/shop/restores", personal, body(""), nil), 422, `{"error":"the container name shop-db-g2 belongs to project other; rename a service or that project first"}`)
	exec(t, a, `DELETE FROM project_hosts WHERE project_id = 9`)

	withGit(t, a, "", false)
	h = a.Handler()
	is(t, v1(h, "POST", "/projects/shop/restores", personal, body(""), nil), 422,
		`{"error":"commit 1111111 isn't in git@github.com:scttymn/shop.git any more (was history rewritten, or the repo relinked?): fatal: remote error: upload-pack: not our ref"}`)

	withGit(t, a, "", true)
	h = a.Handler()
	w := v1(h, "POST", "/projects/shop/restores", personal, body(""), nil)
	got := answer(t, w)
	if w.Code != 202 || got["kind"] != "restore" || got["status"] != "queued" || got["sha"] != sha1 || got["ref"] != "refs/restore/aaaa" {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	d := deployRow(t, a, 2)
	if d.Generation != 2 || d.SourceSnapshotID != "aaaa1111" || d.SourceLocationID.Int64 != 1 {
		t.Errorf("restore %+v", d)
	}
	if got := hosts(t, a, "shop"); got != "shop shop-db shop-db-g2" {
		t.Errorf("names %q", got)
	}
	is(t, v1(h, "POST", "/projects/shop/restores", personal, body(""), nil), 422, `{"error":"restore #2 is queued; wait for #2"}`)
	_ = time.Now
}

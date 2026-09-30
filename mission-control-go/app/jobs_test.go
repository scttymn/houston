package app_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
	"github.com/scttymn/houston/mission-control-go/app/services/registry"
	"github.com/scttymn/houston/mission-control-go/app/services/release"
)

// A deploy's snapshot goes on the snapshots queue and runs; a busy one
// waits and tries again rather than failing.
func TestBackupJobs(t *testing.T) {
	a := newApp(t)
	docker := &dockercmdtest.Fake{}
	a.DockerCLI = docker
	must := func(q string, args ...any) {
		if _, err := a.DB.Write.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(`INSERT INTO storage_locations (id, name, kind, settings, restic_password, is_default, acknowledged_at) VALUES (1, 'nas', 'local', '{"path":"/srv"}', ?, TRUE, CURRENT_TIMESTAMP)`, crypt.Of("pw"))
	must(`INSERT INTO projects (id, name, app_service, services, volumes, health, port) VALUES (1, 'shop', 'web', '["web"]', '[{"name":"data","path":"/data"}]', '/up', 3000)`)
	must(`INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at) VALUES (1, 1, 'go', 'abc', 'main', CURRENT_TIMESTAMP)`)
	must(`INSERT INTO backup_runs (id, project_id, location_id, kind, reason, deploy_number, heartbeat_at) VALUES (1, 1, 1, 'deploy', 'deploy', 1, CURRENT_TIMESTAMP)`)
	docker.On(dockercmdtest.OK(`{"sqlite":[],"warnings":[],"errors":[]}`), "run", "--rm", "--name", "houston-backup.shop.sqlite")
	docker.On(dockercmdtest.OK(`{"message_type":"summary","snapshot_id":"abc","total_bytes_processed":7}`), "run", "--rm", "--name", "houston-backup.shop.restic")

	if _, err := a.Snapshot.Enqueue(t.Context(), models.BackupArgs{RunID: 1}); err != nil {
		t.Fatal(err)
	}
	pending, _ := a.Jobs.Pending(t.Context())
	if len(pending) != 1 || pending[0].Queue != "snapshots" {
		t.Fatalf("pending %+v", pending)
	}
	if err := a.Jobs.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status, snapshot string
	a.DB.Read.QueryRow(`SELECT status, snapshot_id FROM backup_runs WHERE id = 1`).Scan(&status, &snapshot)
	if status != "go" || snapshot != "abc" {
		t.Errorf("run %s %s", status, snapshot)
	}

	// Behind a live backup of the project: it waits.
	must(`INSERT INTO backup_runs (id, project_id, location_id, kind, reason, status, heartbeat_at) VALUES (2, 1, 1, 'auto', 'schedule', 'running', ?)`, time.Now())
	must(`INSERT INTO backup_runs (id, project_id, location_id, kind, reason, heartbeat_at) VALUES (3, 1, 1, 'auto', 'manual', CURRENT_TIMESTAMP)`)
	a.Backup.Enqueue(t.Context(), models.BackupArgs{RunID: 3})
	a.Jobs.Drain(t.Context())
	pending, _ = a.Jobs.Pending(t.Context())
	failed, _ := a.Jobs.Failed(t.Context())
	if len(pending) != 1 || pending[0].Queue != "backups" || pending[0].Attempts != 1 || len(failed) != 0 {
		t.Errorf("busy: pending %+v, failed %+v", pending, failed)
	}
}

// A verified push's check queues the head its rule wants; the poll checks
// the projects that deploy on push.
func TestCheckJobs(t *testing.T) {
	a := newApp(t)
	a.Git = gitremote.Git{KnownHosts: "/x", TempDir: t.TempDir(), Run: func(ctx context.Context, argv []string, env map[string]string) dockercmd.Result {
		return dockercmd.Result{OK: true, Output: strings.Repeat("a", 40) + "\trefs/heads/main\n"}
	}}
	a.DB.Write.Exec(`INSERT INTO projects (id, name, app_service, services, health, port, repo_url, webhook_verified_at) VALUES
		(1, 'shop', 'web', '["web"]', '/', 80, 'git@x:shop.git', CURRENT_TIMESTAMP), (2, 'blog', 'web', '["web"]', '/', 80, 'git@x:blog.git', NULL)`)
	if _, err := a.Poll.Enqueue(t.Context(), struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Jobs.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	var queued string
	a.DB.Read.QueryRow(`SELECT group_concat(projects.name || ':' || deploys.ref) FROM deploys JOIN projects ON projects.id = project_id WHERE status = 'queued'`).Scan(&queued)
	if queued != "shop:refs/heads/main" {
		t.Errorf("queued %q", queued)
	}
}

// The schedule queues each due project's daily backup once: one with
// something to back up, that has served, with storage.
func TestScheduleBackups(t *testing.T) {
	a := newApp(t)
	must := func(q string, args ...any) {
		if _, err := a.DB.Write.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(`INSERT INTO installations (id, base_domain, time_zone) VALUES (1, 'svnmns.com', 'America/Denver')`)
	must(`INSERT INTO storage_locations (id, name, kind, is_default, acknowledged_at) VALUES (1, 'nas', 'nfs', TRUE, CURRENT_TIMESTAMP)`)
	for i, p := range []struct{ name, volumes string }{{"shop", `[{"name":"data","path":"/d"}]`}, {"bare", `[]`}, {"new", `[{"name":"data","path":"/d"}]`}} {
		must(`INSERT INTO projects (id, name, app_service, services, volumes, health, port, backup_schedule) VALUES (?, ?, 'web', '["web"]', ?, '/', 80, 'daily 00:00')`, i+1, p.name, p.volumes)
		if p.name != "new" {
			must(`INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at) VALUES (?, 1, 'go', 'abc', 'main', CURRENT_TIMESTAMP)`, i+1)
		}
	}
	for range 2 {
		if _, err := a.Schedule.Enqueue(t.Context(), struct{}{}); err != nil {
			t.Fatal(err)
		}
		a.Jobs.Drain(t.Context())
	}
	denver, _ := time.LoadLocation("America/Denver")
	var runs string
	a.DB.Read.QueryRow(`SELECT group_concat(projects.name || ' ' || scheduled_for || ' ' || kind || ' ' || reason) FROM backup_runs JOIN projects ON projects.id = project_id`).Scan(&runs)
	if want := "shop " + time.Now().In(denver).Format(time.DateOnly) + " auto schedule"; runs != want {
		t.Errorf("runs %q, want %q", runs, want)
	}
}

// Prune: each location in use; a failure is kept on the location.
func TestPrune(t *testing.T) {
	a := newApp(t)
	docker := &dockercmdtest.Fake{}
	a.DockerCLI = docker
	for _, q := range []string{
		`INSERT INTO storage_locations (id, name, kind, settings, acknowledged_at) VALUES
			(1, 'nas', 'local', '{"path":"/srv"}', CURRENT_TIMESTAMP), (2, 'offsite', 'local', '{"path":"/off"}', CURRENT_TIMESTAMP), (3, 'unused', 'local', '{}', CURRENT_TIMESTAMP)`,
		`INSERT INTO projects (id, name, app_service, services, health, port) VALUES (1, 'shop', 'web', '["web"]', '/', 80)`,
		`INSERT INTO backup_runs (project_id, location_id, kind, reason, status, heartbeat_at) VALUES (1, 1, 'auto', 'manual', 'go', CURRENT_TIMESTAMP), (1, 2, 'auto', 'schedule', 'go', CURRENT_TIMESTAMP)`,
	} {
		if _, err := a.DB.Write.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	docker.On(dockercmdtest.Fail(1, "repository is locked\nFatal: unable to prune\n"), "run", "--rm", "-e", "RESTIC_PASSWORD", "-e", "RESTIC_REPOSITORY", "-v", "houston-restic-cache:/root/.cache/restic", "-v", "/off:/repo")
	a.Prune.Enqueue(t.Context(), struct{}{})
	a.Jobs.Drain(t.Context())
	var got string
	a.DB.Read.QueryRow(`SELECT group_concat(name || ':' || (pruned_at IS NOT NULL) || ':' || prune_error, '|') FROM storage_locations`).Scan(&got)
	if got != "nas:1:|offsite:0:repository is locked\nFatal: unable to prune|unused:0:" || len(docker.Calls()) != 2 {
		t.Errorf("= %q, ran %v", got, docker.Ran())
	}
}

// The latest release: kept when GitHub's answer is Houston's; otherwise
// left as it was.
func TestLatestRelease(t *testing.T) {
	answer := `{"tag_name":"v0.4.28","html_url":"https://github.com/scttymn/houston/releases/tag/v0.4.28"}`
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/scttymn/houston/releases/latest" || r.Header.Get("User-Agent") != "houston/dev" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, answer)
	}))
	defer github.Close()
	a := newApp(t)
	a.Release = release.Checker{API: github.URL, Repo: "scttymn/houston", Version: "dev"}
	a.DB.Write.Exec(`INSERT INTO installations (id, base_domain) VALUES (1, 'svnmns.com')`)
	latest := func() string {
		var tag, url string
		a.DB.Read.QueryRow(`SELECT latest_release, latest_release_url FROM installations`).Scan(&tag, &url)
		return tag + " " + url
	}
	a.LatestRelease.Enqueue(t.Context(), struct{}{})
	a.Jobs.Drain(t.Context())
	if got := latest(); got != "v0.4.28 https://github.com/scttymn/houston/releases/tag/v0.4.28" {
		t.Errorf("= %q", got)
	}
	for _, bad := range []string{`{"tag_name":"latest","html_url":"https://github.com/scttymn/houston/releases/tag/latest"}`,
		`{"tag_name":"v9.9.9","html_url":"https://evil.example/releases/v9.9.9"}`} {
		answer = bad
		a.LatestRelease.Enqueue(t.Context(), struct{}{})
		a.Jobs.Drain(t.Context())
		if got := latest(); got != "v0.4.28 https://github.com/scttymn/houston/releases/tag/v0.4.28" {
			t.Errorf("after %s: %q", bad, got)
		}
	}
}

// The registry's clean-up waits while anything may be pushing, then frees
// the space and says so on the deletion's log, the lock let go.
func TestCleanRegistry(t *testing.T) {
	a := newApp(t)
	docker := &dockercmdtest.Fake{}
	docker.On(dockercmdtest.OK("reg1\n"), "ps")
	docker.On(dockercmdtest.OK("12 blobs and 3 manifests eligible for deletion\n"), "exec")
	a.Registry = registry.Registry{ComposeProject: "houston", Docker: docker}
	must := func(q string, args ...any) {
		if _, err := a.DB.Write.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(`INSERT INTO installations (id, base_domain) VALUES (1, 'svnmns.com')`)
	must(`INSERT INTO projects (id, name, app_service, services, health, port) VALUES (1, 'blog', 'web', '["web"]', '/', 80)`)
	must(`INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at) VALUES (1, 4, 'in_flight', 'abc', 'main', ?)`, time.Now())
	must(`INSERT INTO project_deletions (id, name, requested_by, status, heartbeat_at) VALUES (1, 'shop', 'token laptop', 'go', CURRENT_TIMESTAMP)`)
	a.CleanRegistry.Enqueue(t.Context(), models.DeletionArgs{ID: 1})
	a.Jobs.Drain(t.Context())
	pending, _ := a.Jobs.Pending(t.Context())
	if len(pending) != 1 || pending[0].Attempts != 1 || len(docker.Calls()) != 0 {
		t.Fatalf("behind a push: pending %+v, ran %v", pending, docker.Ran())
	}
	if !strings.Contains(pending[0].Error, "deploy #4 of blog is in flight") {
		t.Errorf("why %q", pending[0].Error)
	}

	must(`UPDATE deploys SET status = 'go'`)
	must(`DELETE FROM gantry_jobs`)
	a.CleanRegistry.Enqueue(t.Context(), models.DeletionArgs{ID: 1})
	a.Jobs.Drain(t.Context())
	var log string
	var locked bool
	a.DB.Read.QueryRow(`SELECT log FROM project_deletions WHERE id = 1`).Scan(&log)
	a.DB.Read.QueryRow(`SELECT registry_cleanup_since IS NOT NULL FROM installations`).Scan(&locked)
	if log != "ok  registry space freed (12 blobs and 3 manifests eligible for deletion)\n" || locked {
		t.Errorf("log %q, locked %v", log, locked)
	}
}

// While an update runs, it's followed every few seconds from its start; the
// minute's look doesn't start a chain, and one that's done ends it.
func TestServerUpdateFollows(t *testing.T) {
	a := newApp(t)
	fake := &dockercmdtest.Fake{}
	a.Updater.Docker = fake
	fake.On(dockercmdtest.OK("running 0\n"), "inspect")
	fake.On(dockercmdtest.OK("==> Pulling Houston v0.4.3\n"), "logs")
	a.DB.Write.Exec(`INSERT INTO server_updates (to_version, from_version, started_at) VALUES ('v0.4.3', 'v0.4.2', CURRENT_TIMESTAMP)`)
	follows := func() int {
		pending, _ := a.Jobs.Pending(t.Context())
		n := 0
		for _, p := range pending {
			if p.Name == "server_update" && strings.Contains(string(p.Args), `"follow":true`) {
				n++
			}
		}
		return n
	}
	a.ServerUpdate.Enqueue(t.Context(), models.UpdateArgs{})
	a.Jobs.Drain(t.Context())
	if n := follows(); n != 0 {
		t.Errorf("the minute's look followed: %d", n)
	}
	a.ServerUpdate.Enqueue(t.Context(), models.UpdateArgs{Follow: true})
	a.Jobs.Drain(t.Context())
	if n := follows(); n != 1 {
		t.Errorf("follows %d", n)
	}
	var step string
	a.DB.Read.QueryRow(`SELECT step FROM server_updates`).Scan(&step)
	if step != "Pulling Houston v0.4.3" {
		t.Errorf("step %q", step)
	}

	// Done: the follow that's due runs, and none comes after it (the one
	// already scheduled is all that's left).
	a.DB.Write.Exec(`UPDATE server_updates SET status = 'go'`)
	a.ServerUpdate.Enqueue(t.Context(), models.UpdateArgs{Follow: true})
	a.Jobs.Drain(t.Context())
	if n := follows(); n != 1 {
		t.Errorf("followed one that's done: %d", n)
	}
}

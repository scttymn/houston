package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
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

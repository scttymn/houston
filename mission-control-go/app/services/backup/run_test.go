package backup_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission-control-go/test"
)

const sha = "5ade5ade5ade5ade5ade5ade5ade5ade5ade5ade"

// setUp is shop (a volume and a Postgres), deployed, backing up to nas,
// and a queued run of reason; its id.
func setUp(t *testing.T, reason string) (*db.DB, *dockercmdtest.Fake, backup.Runner, int64) {
	t.Helper()
	keys, _ := crypt.ParseKeys(crypt.NewKey())
	crypt.Use(keys...)
	t.Cleanup(func() { crypt.Use() })
	d := test.DB(t)
	must := func(q string, args ...any) {
		if _, err := d.Write.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(`INSERT INTO storage_locations (id, name, kind, settings, restic_password, is_default, acknowledged_at) VALUES
		(1, 'nas', 'nfs', '{"server":"10.0.1.20","export":"/volume1/houston"}', ?, TRUE, CURRENT_TIMESTAMP)`, crypt.Of("restic-secret"))
	must(`INSERT INTO projects (id, name, app_service, services, volumes, databases, health, port) VALUES
		(1, 'shop', 'web', '["web","db"]', '[{"name":"data","path":"/rails/storage"}]', '[{"service":"db","image":"postgres:17"}]', '/up', 3000)`)
	must(`INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at) VALUES (1, 1, 'go', ?, 'main', CURRENT_TIMESTAMP)`, sha)
	res, _ := d.Write.Exec(`INSERT INTO backup_runs (project_id, location_id, kind, reason, heartbeat_at) VALUES (1, 1, 'auto', ?, CURRENT_TIMESTAMP)`, reason)
	id, _ := res.LastInsertId()
	docker := &dockercmdtest.Fake{}
	r := backup.Runner{DB: d, Docker: docker, Tools: "houston/mission-control:test", ToolsBin: "/app", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return d, docker, r, id
}

func runRow(t *testing.T, d *db.DB, id int64) models.BackupRun {
	t.Helper()
	r, err := models.New(d.Read).BackupRunByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const list = `psql -U "${POSTGRES_USER:-postgres}" -d postgres -Atc "select encode(convert_to(datname, 'UTF8'), 'hex') from pg_database where not datistemplate order by datname"`

// A backup: the staging volume made, each Postgres database dumped, the
// SQLite databases copied, a manifest written, one restic snapshot of it
// all with the app's volumes, old snapshots forgotten, the staging volume
// removed. Secrets reach docker only through its environment.
func TestBackup(t *testing.T) {
	d, docker, r, id := setUp(t, "manual")
	docker.On(dockercmdtest.OK("617070\n71\n"), "exec", "shop-db", "sh", "-c", list)
	docker.On(dockercmdtest.OK("copying\n"+`{"sqlite":[{"volume":"data","path":"app.db","file":"sqlite/1.sqlite3","uid":1000,"gid":1000,"mode":420}],"warnings":["a warning"],"errors":[]}`+"\n"),
		"run", "--rm", "--name", "houston-backup.shop.sqlite")
	docker.On(dockercmdtest.OK(`{"message_type":"status","percent_done":0.5}`+"\n"+`{"message_type":"summary","snapshot_id":"5eed","total_bytes_processed":1234}`+"\n"),
		"run", "--rm", "--name", "houston-backup.shop.restic")
	if err := r.Do(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	run := runRow(t, d, id)
	if run.Status != "go" || run.SnapshotID != "5eed" || run.Bytes.Int64 != 1234 || run.Sha != sha || run.Error != "a warning" ||
		run.Found != `{"databases":[{"name":"app","service":"db"},{"name":"q","service":"db"}],"sqlite":[{"path":"app.db","volume":"data"}]}` ||
		!strings.Contains(run.Log, "$ docker exec shop-db sh -c …") || !run.StartedAt.Valid || !run.FinishedAt.Valid {
		t.Errorf("run %+v", run)
	}

	ran := docker.Ran()
	restic := "run --rm --name houston-backup.shop.restic -e RESTIC_PASSWORD -e RESTIC_REPOSITORY -v houston-restic-cache:/root/.cache/restic " +
		"-v houston-storage-nas:/repo -v shop_data:/data/data:ro -v houston-backup.shop:/out:ro " + backup.ResticImage +
		" backup --retry-lock 10m --host houston --json --tag project:shop --tag sha:" + sha + " --tag kind:auto --tag reason:manual --exclude-file /out/.houston/exclude /data /out"
	write := "run --rm -i --name houston-backup.shop.write --user 0 -v houston-backup.shop:/out --entrypoint sh houston/mission-control:test -c mkdir -p \"$(dirname \"$1\")\" && cat > \"$1\" sh /out/"
	want := []string{
		"rm -f houston-backup.shop.sqlite houston-backup.shop.restic houston-backup.shop.write",
		"volume rm -f houston-backup.shop",
		"volume create houston-backup.shop",
		"exec shop-db sh -c " + list,
		`exec shop-db sh -c pg_dump -U "${POSTGRES_USER:-postgres}" -Fc -d "$1" sh dbname='app' | ` + write + "postgres/db/1.dump",
		`exec shop-db sh -c pg_dump -U "${POSTGRES_USER:-postgres}" -Fc -d "$1" sh dbname='q' | ` + write + "postgres/db/2.dump",
		`exec shop-db sh -c pg_dumpall -U "${POSTGRES_USER:-postgres}" --globals-only | ` + write + "postgres/db/globals.sql",
		"run --rm --name houston-backup.shop.sqlite --user 0 -v shop_data:/data/data -v houston-backup.shop:/out --entrypoint /app houston/mission-control:test backup-sqlite /data /out",
		write + "houston.json",
		restic,
		"run --rm -e RESTIC_PASSWORD -e RESTIC_REPOSITORY -v houston-restic-cache:/root/.cache/restic -v houston-storage-nas:/repo " + backup.ResticImage +
			" forget --retry-lock 10m --host houston --tag project:shop,kind:auto --group-by  --json --keep-daily 14",
		"volume rm -f houston-backup.shop",
	}
	if strings.Join(ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(ran, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range docker.Calls() {
		if strings.Contains(c.String(), "restic-secret") {
			t.Errorf("a secret in argv: %s", c)
		}
		if strings.Contains(c.String(), "backup --retry-lock") && (c.Env["RESTIC_PASSWORD"] != "restic-secret" || c.Env["RESTIC_REPOSITORY"] != "/repo") {
			t.Errorf("restic's env %v", c.Env)
		}
		if strings.HasSuffix(c.String(), "/out/houston.json") && !strings.Contains(string(c.Stdin), `"project": "shop"`) {
			t.Errorf("the manifest %s", c.Stdin)
		}
	}
}

// Nothing to back up is skipped; a failure is the run's NO-GO, the
// staging volume removed either way; past the deadline it's stopped.
func TestBackupEnds(t *testing.T) {
	d, docker, r, id := setUp(t, "manual")
	d.Write.Exec(`UPDATE projects SET volumes = '[]', databases = '[]'`)
	r.Do(context.Background(), id)
	if run := runRow(t, d, id); run.Status != "skipped" || run.Error != "nothing to back up (no named volumes, no Postgres)" || len(docker.Calls()) != 0 {
		t.Errorf("skipped %+v %v", run, docker.Ran())
	}

	d, docker, r, id = setUp(t, "manual")
	docker.On(dockercmdtest.OK("617070\n"), "exec", "shop-db", "sh", "-c", list)
	docker.On(dockercmdtest.OK(`{"sqlite":[],"warnings":[],"errors":[]}`), "run", "--rm", "--name", "houston-backup.shop.sqlite")
	docker.On(dockercmdtest.Fail(1, "Fatal: unable to open config file: stat /repo/config: no such file or directory\n"), "run", "--rm", "--name", "houston-backup.shop.restic")
	r.Do(context.Background(), id)
	ran := docker.Ran()
	if run := runRow(t, d, id); run.Status != "no_go" || run.Error != "restic backup failed (exit 1): Fatal: unable to open config file: stat /repo/config: no such file or directory" ||
		ran[len(ran)-1] != "volume rm -f houston-backup.shop" {
		t.Errorf("failed %+v, last %s", run, ran[len(ran)-1])
	}

	d, docker, r, id = setUp(t, "manual")
	docker.On(dockercmdtest.Fail(2, "psql: error: connection refused\n"), "exec", "shop-db", "sh", "-c", list)
	r.Do(context.Background(), id)
	if run := runRow(t, d, id); run.Error != "couldn't list db's databases: psql: error: connection refused" {
		t.Errorf("listing failed %q", run.Error)
	}

	d, docker, r, id = setUp(t, "manual")
	r.Deadline = 300 * time.Millisecond
	docker.On(dockercmdtest.OK("617070\n"), "exec", "shop-db", "sh", "-c", list)
	docker.Slow(time.Minute, "exec", "shop-db", "sh", "-c", `pg_dump -U "${POSTGRES_USER:-postgres}" -Fc -d "$1"`)
	r.Do(context.Background(), id)
	ran = docker.Ran()
	if run := runRow(t, d, id); run.Status != "no_go" || run.Error != "took longer than 300ms; stopped" ||
		ran[len(ran)-2] != "rm -f houston-backup.shop.sqlite houston-backup.shop.restic houston-backup.shop.write" {
		t.Errorf("timed out %+v, ran %v", run, ran)
	}
}

// One running backup a project: a live one makes another wait, a silent
// one is abandoned; while a restore is underway only its own runs go.
func TestBackupWaits(t *testing.T) {
	ctx := context.Background()
	d, docker, r, id := setUp(t, "manual")
	d.Write.Exec(`INSERT INTO backup_runs (project_id, location_id, kind, reason, status, heartbeat_at) VALUES (1, 1, 'auto', 'schedule', 'running', ?)`, time.Now())
	if err := r.Do(ctx, id); !errors.Is(err, models.ErrBusy) || runRow(t, d, id).Status != "queued" || len(docker.Calls()) != 0 {
		t.Errorf("behind a live one: %v", err)
	}
	d.Write.Exec(`UPDATE backup_runs SET heartbeat_at = ? WHERE status = 'running'`, time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC))
	d.Write.Exec(`UPDATE projects SET volumes = '[]', databases = '[]'`)
	if err := r.Do(ctx, id); err != nil || runRow(t, d, id).Status != "skipped" {
		t.Errorf("past a silent one: %v %s", err, runRow(t, d, id).Status)
	}
	var abandoned string
	d.Read.QueryRow(`SELECT status || ': ' || error FROM backup_runs WHERE reason = 'schedule'`).Scan(&abandoned)
	if abandoned != "no_go: Mission Control stopped during the backup (no word since 2026-09-30T08:00:00Z)" {
		t.Errorf("the silent one: %s", abandoned)
	}

	d, _, r, id = setUp(t, "manual")
	d.Write.Exec(`INSERT INTO deploys (project_id, number, kind, status, sha, ref, heartbeat_at) VALUES (1, 2, 'restore', 'queued', ?, 'r', CURRENT_TIMESTAMP)`, sha)
	if err := r.Do(ctx, id); !errors.Is(err, models.ErrBusy) {
		t.Errorf("while a restore is queued: %v", err)
	}
	d.Write.Exec(`UPDATE backup_runs SET reason = 'restore', kind = 'deploy', deploy_number = 2`)
	d.Write.Exec(`UPDATE projects SET volumes = '[]', databases = '[]'`)
	if err := r.Do(ctx, id); err != nil || runRow(t, d, id).Status != "skipped" {
		t.Errorf("the restore's own: %v", err)
	}

	// A second delivery of its job finds it done.
	if err := r.Do(ctx, id); err != nil {
		t.Errorf("again: %v", err)
	}
}

// A run beats while it works.
func TestBackupBeats(t *testing.T) {
	d, docker, r, id := setUp(t, "manual")
	r.HeartbeatEvery = 20 * time.Millisecond
	docker.On(dockercmdtest.OK(""), "exec", "shop-db", "sh", "-c", list)
	docker.Slow(300*time.Millisecond, "run", "--rm", "--name", "houston-backup.shop.sqlite")
	began := time.Now()
	r.Do(context.Background(), id)
	run := runRow(t, d, id)
	if !run.HeartbeatAt.After(began.Add(100 * time.Millisecond)) {
		t.Errorf("heartbeat %v, began %v", run.HeartbeatAt, began)
	}
}

// What varies: a project with only Postgres (no SQLite to look for), files
// restic couldn't read (a GO, with a warning), a final snapshot (never
// forgotten), a database that couldn't be copied, a list that isn't one.
func TestBackupCases(t *testing.T) {
	ctx := context.Background()
	d, docker, r, id := setUp(t, "manual")
	d.Write.Exec(`UPDATE projects SET volumes = '[]'`)
	d.Write.Exec(`UPDATE backup_runs SET kind = 'final', reason = 'delete'`)
	docker.On(dockercmdtest.Fail(3, `{"message_type":"error","item":"/data/a"}`+"\n"+`{"message_type":"error","error":{"message":"b: permission denied"}}`+"\n"+
		`{"message_type":"summary","snapshot_id":"5eed","total_bytes_processed":1}`+"\n"), "run", "--rm", "--name", "houston-backup.shop.restic")
	r.Do(ctx, id)
	run := runRow(t, d, id)
	if run.Status != "go" || run.Error != "2 files couldn't be read: /data/a, b: permission denied" {
		t.Errorf("unreadable files: %s %q", run.Status, run.Error)
	}
	for _, c := range docker.Ran() {
		if strings.Contains(c, "backup-sqlite") || strings.Contains(c, " forget ") {
			t.Errorf("ran %s", c)
		}
	}

	d, docker, r, id = setUp(t, "manual")
	docker.On(dockercmdtest.OK(""), "exec", "shop-db", "sh", "-c", list)
	docker.On(dockercmdtest.Fail(1, `{"sqlite":[],"warnings":[],"errors":[{"volume":"data","path":"app.db","message":"database is locked"}]}`), "run", "--rm", "--name", "houston-backup.shop.sqlite")
	r.Do(ctx, id)
	if run := runRow(t, d, id); run.Error != "couldn't copy the SQLite database data/app.db: database is locked" {
		t.Errorf("a copy failed: %q", run.Error)
	}

	d, docker, r, id = setUp(t, "manual")
	docker.On(dockercmdtest.OK("617070\nnot hex\n"), "exec", "shop-db", "sh", "-c", list)
	r.Do(ctx, id)
	if run := runRow(t, d, id); run.Error != "couldn't list db's databases: 617070\nnot hex" {
		t.Errorf("not a list: %q", run.Error)
	}
}

package backup_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/services/backup"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
)

// restoring is shop with restore #2 in flight into generation 2, and its
// data's run queued.
func restoring(t *testing.T) (*db.DB, *dockercmdtest.Fake, backup.Runner, int64) {
	t.Helper()
	d, docker, r, _ := setUp(t, "manual")
	d.Write.Exec(`DELETE FROM backup_runs`)
	d.Write.Exec(`INSERT INTO deploys (project_id, number, kind, status, generation, sha, ref, heartbeat_at, sync_payload, source_location_id, source_snapshot_id)
		VALUES (1, 2, 'restore', 'in_flight', 2, ?, 'refs/restore/abcd', CURRENT_TIMESTAMP, ?, 1, 'abcd1234')`, sha,
		`{"name":"shop","volumes":[{"name":"data","path":"/rails/storage"}]}`)
	res, _ := d.Write.Exec(`INSERT INTO backup_runs (project_id, location_id, operation, kind, reason, deploy_number, source_snapshot_id, heartbeat_at)
		VALUES (1, 1, 'restore', 'restore', 'restore', 2, 'abcd1234', CURRENT_TIMESTAMP)`)
	id, _ := res.LastInsertId()
	r.ReadyEvery = time.Millisecond
	docker.On(dockercmdtest.Fail(1, "no such volume"), "volume", "inspect")
	return d, docker, r, id
}

func manifest(project, commit, file, path string) string {
	return `{"version":1,"project":"` + project + `","sha":"` + commit + `","volumes":[{"name":"data","path":"/rails/storage"}],
		"postgres":[{"service":"db","image":"postgres:17","globals":"postgres/db/globals.sql","databases":[{"name":"app","file":"postgres/db/1.dump"}]}],
		"sqlite":[{"volume":"data","path":"` + path + `","file":"` + file + `","uid":1000,"gid":1000,"mode":420}]}`
}

// A restore's data: generation 2's volumes made, the snapshot staged, its
// manifest checked, each volume emptied and refilled (SQLite copies in
// place, with their owner and mode), each database recreated and restored
// into generation 2's Postgres once it's up.
func TestRestoreData(t *testing.T) {
	d, docker, r, id := restoring(t)
	docker.On(dockercmdtest.OK(manifest("shop", sha, "sqlite/1.sqlite3", "app.db")), "run", "--rm", "--name", "houston-restore.shop.read")
	if err := r.Do(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	run := runRow(t, d, id)
	if run.Status != "go" || run.Found != `{"databases":[{"name":"app","service":"db"}],"sqlite":[{"path":"app.db","volume":"data"}],"volumes":["data"]}` {
		t.Fatalf("run %s %q %s", run.Status, run.Error, run.Found)
	}
	ran := strings.Join(docker.Ran(), "\n")
	for _, want := range []string{
		"volume create shop.g2_data",
		"volume create houston-restore.shop",
		" restore abcd1234 --target /restore",
		"-v shop.g2_data:/v -v houston-restore.shop:/restore:ro --entrypoint sh houston/mission-control:test -c ",
		" sh data sqlite/1.sqlite3 app.db 1000:1000 644",
		`exec shop-db-g2 sh -c pg_isready`,
		"run --rm --name houston-restore.shop.feed --user 0 -v houston-restore.shop:/restore:ro --entrypoint cat houston/mission-control:test /restore/out/postgres/db/globals.sql | exec -i shop-db-g2 sh -c psql",
		`exec shop-db-g2 sh -c dropdb -U "${POSTGRES_USER:-postgres}" --force --if-exists --maintenance-db=template1 -- "$1" && createdb`,
		`/restore/out/postgres/db/1.dump | exec -i shop-db-g2 sh -c pg_restore -U "${POSTGRES_USER:-postgres}" --no-owner --role="${POSTGRES_USER:-postgres}" -d "$1" sh dbname='app'`,
	} {
		if !strings.Contains(ran, want) {
			t.Errorf("no %q in\n%s", want, ran)
		}
	}
	if strings.Contains(ran, "shop_data") || strings.Contains(ran, "shop-db ") {
		t.Errorf("touched the serving generation:\n%s", ran)
	}
}

// Nothing is touched unless the manifest is this restore's, naming only
// what a backup writes, inside its volumes; the serving generation never.
func TestRestoreDataRefuses(t *testing.T) {
	for _, c := range []struct {
		name, manifest, want string
	}{
		{"another project's", manifest("blog", sha, "sqlite/1.sqlite3", "app.db"), "the snapshot is of blog, not shop"},
		{"another commit's", manifest("shop", strings.Repeat("f", 40), "sqlite/1.sqlite3", "app.db"), "the snapshot was taken at fffffff, not the restore's 5ade5ad"},
		{"a file not Houston's", manifest("shop", sha, "../../etc/passwd", "app.db"), `the manifest names "../../etc/passwd", not one Houston writes`},
		{"a path out of its volume", manifest("shop", sha, "sqlite/1.sqlite3", "../escape.db"), `the SQLite path "../escape.db" leaves its volume`},
		{"not Houston's", `{"project":"shop"}`, "the snapshot's manifest isn't Houston's"},
		{"not JSON", `nope`, "the snapshot's manifest isn't JSON"},
		{"a volume the restore hasn't", strings.Replace(manifest("shop", sha, "sqlite/1.sqlite3", "app.db"), `"volumes":[{"name":"data"`, `"volumes":[{"name":"cache"`, 1),
			"shop has no volume cache (the snapshot's compose.yml differs)"},
	} {
		d, docker, r, id := restoring(t)
		docker.On(dockercmdtest.OK(c.manifest), "run", "--rm", "--name", "houston-restore.shop.read")
		r.Do(context.Background(), id)
		run := runRow(t, d, id)
		if run.Status != "no_go" || run.Error != c.want {
			t.Errorf("%s: %s %q", c.name, run.Status, run.Error)
		}
		if strings.Contains(strings.Join(docker.Ran(), "\n"), "--entrypoint sh houston/mission-control:test -c vol=") {
			t.Errorf("%s: a volume was filled", c.name)
		}
	}

	d, docker, r, id := restoring(t)
	docker.On(dockercmdtest.OK(manifest("shop", sha, "sqlite/1.sqlite3", "app.db")), "run", "--rm", "--name", "houston-restore.shop.read")
	docker.On(dockercmdtest.Fail(2, "no response"), "exec", "shop-db-g2", "sh", "-c")
	r.Do(context.Background(), id)
	if run := runRow(t, d, id); run.Error != "db's Postgres (shop-db-g2) isn't ready" {
		t.Errorf("never ready: %q", run.Error)
	}

	d, _, r, id = restoring(t)
	d.Write.Exec(`UPDATE projects SET data_generation = 2`)
	r.Do(context.Background(), id)
	if run := runRow(t, d, id); run.Error != "refusing to restore into generation 2: generation 2 is the one serving" {
		t.Errorf("into the serving one: %q", run.Error)
	}

	d, _, r, id = restoring(t)
	d.Write.Exec(`UPDATE deploys SET sync_payload = NULL WHERE number = 2`)
	r.Do(context.Background(), id)
	if run := runRow(t, d, id); run.Error != "the restore's compose.yml wasn't checked (its runner is older than this Mission Control)" {
		t.Errorf("unchecked: %q", run.Error)
	}
}

// Each volume gets only its own databases; an owner or mode that isn't
// one (an older snapshot's, or out of range) is left to the fill's
// defaults; an absolute path leaves its volume.
func TestRestoreDataDetails(t *testing.T) {
	d, docker, r, id := restoring(t)
	d.Write.Exec(`UPDATE deploys SET sync_payload = ? WHERE number = 2`, `{"volumes":[{"name":"data","path":"/d"},{"name":"cache","path":"/c"}]}`)
	docker.On(dockercmdtest.OK(`{"project":"shop","sha":"`+sha+`","volumes":[{"name":"data"},{"name":"cache"}],"postgres":[],"sqlite":[
		{"volume":"data","path":"a.db","file":"sqlite/1.sqlite3","uid":1000,"gid":-1,"mode":420},
		{"volume":"data","path":"b.db","file":"sqlite/2.sqlite3","uid":"x","gid":1,"mode":99999},
		{"volume":"cache","path":"c.db","file":"sqlite/3.sqlite3","uid":0,"gid":0,"mode":384}]}`), "run", "--rm", "--name", "houston-restore.shop.read")
	r.Do(context.Background(), id)
	var data, cache string
	for _, c := range docker.Ran() {
		if strings.Contains(c, " sh data ") {
			data = c[strings.Index(c, " sh data "):]
		}
		if strings.Contains(c, " sh cache ") {
			cache = c[strings.Index(c, " sh cache "):]
		}
	}
	if data != " sh data sqlite/1.sqlite3 a.db - 644 sqlite/2.sqlite3 b.db - -" || cache != " sh cache sqlite/3.sqlite3 c.db 0:0 600" {
		t.Errorf("data %q, cache %q (%s)", data, cache, runRow(t, d, id).Error)
	}

	d, docker, r, id = restoring(t)
	docker.On(dockercmdtest.OK(manifest("shop", sha, "sqlite/1.sqlite3", "/etc/app.db")), "run", "--rm", "--name", "houston-restore.shop.read")
	r.Do(context.Background(), id)
	if run := runRow(t, d, id); run.Error != `the SQLite path "/etc/app.db" leaves its volume` {
		t.Errorf("absolute: %q", run.Error)
	}
}

// copying is restoring's run as a copy's: shop is the copy of old, never
// served, its deploy building generation 1 from old's snapshot at e…e.
func copying(t *testing.T) (*db.DB, *dockercmdtest.Fake, backup.Runner, int64) {
	t.Helper()
	d, docker, r, id := restoring(t)
	for _, q := range []string{
		`DELETE FROM deploys WHERE number = 1`,
		`UPDATE deploys SET kind = 'copy', generation = 1 WHERE number = 2`,
		`INSERT INTO projects (id, name, app_service, services, health, port) VALUES (2, 'old', 'web', '["web"]', '/up', 3000)`,
		`INSERT INTO backup_runs (id, project_id, location_id, kind, reason, status, sha, heartbeat_at) VALUES (99, 2, 1, 'deploy', 'copy', 'go', '` + strings.Repeat("e", 40) + `', CURRENT_TIMESTAMP)`,
		`INSERT INTO project_copies (project_id, from_project_id, deploy_id, snapshot_run_id, from_name, to_name, sha, requested_by, status)
			SELECT 1, 2, id, 99, 'old', 'shop', sha, 'admin', 'running' FROM deploys WHERE number = 2`,
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return d, docker, r, id
}

// A copy's data is the old project's snapshot, at the commit it was taken
// at, restored into the generation the new project will first serve.
func TestRestoreCopyData(t *testing.T) {
	for _, c := range []struct {
		name, manifest, want string
	}{
		{"the old project's", manifest("old", strings.Repeat("e", 40), "sqlite/1.sqlite3", "app.db"), ""},
		{"the new project's", manifest("shop", strings.Repeat("e", 40), "sqlite/1.sqlite3", "app.db"), "the snapshot is of shop, not old"},
		{"another commit's", manifest("old", sha, "sqlite/1.sqlite3", "app.db"), "the snapshot was taken at 5ade5ad, not the snapshot's eeeeeee"},
	} {
		d, docker, r, id := copying(t)
		docker.On(dockercmdtest.OK(c.manifest), "run", "--rm", "--name", "houston-restore.shop.read")
		r.Do(context.Background(), id)
		if run := runRow(t, d, id); run.Error != c.want || (c.want == "") != (run.Status == "go") {
			t.Errorf("%s: %s %q", c.name, run.Status, run.Error)
		}
	}

	// Once the new project has served, its serving generation is never
	// restored into.
	d, docker, r, id := copying(t)
	docker.On(dockercmdtest.OK(manifest("old", strings.Repeat("e", 40), "sqlite/1.sqlite3", "app.db")), "run", "--rm", "--name", "houston-restore.shop.read")
	d.Write.Exec(`INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at) VALUES (1, 3, 'go', 'x', 'main', CURRENT_TIMESTAMP)`)
	r.Do(context.Background(), id)
	if run := runRow(t, d, id); run.Error != "refusing to restore into generation 1: generation 1 is the one serving" {
		t.Errorf("into the serving one: %q", run.Error)
	}
}

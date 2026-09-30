package removal_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare/cloudflaretest"
	"github.com/scttymn/houston/mission-control-go/app/services/dns"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission-control-go/app/services/registry"
	"github.com/scttymn/houston/mission-control-go/app/services/removal"
	"github.com/scttymn/houston/mission-control-go/test"
)

type world struct {
	db       *db.DB
	docker   *dockercmdtest.Fake
	cf       *cloudflaretest.Fake
	r        removal.Removal
	deletion int64
	registry []string // what the registry was asked to delete
}

// setUp is shop, deployed and in maintenance, with a volume on nas, DNS
// records of its own beside blog's, and its deletion queued.
func setUp(t *testing.T, deleteBackups bool) *world {
	t.Helper()
	keys, _ := crypt.ParseKeys(crypt.NewKey())
	crypt.Use(keys...)
	t.Cleanup(func() { crypt.Use() })
	w := &world{db: test.DB(t), docker: &dockercmdtest.Fake{}, cf: cloudflaretest.New(t)}
	must := func(q string, args ...any) {
		if _, err := w.db.Write.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(`INSERT INTO installations (id, base_domain, cloudflare_zone_id, cloudflare_account_id, tunnel_id, cloudflare_api_token, cloudflare_connected_at)
		VALUES (1, 'svnmns.com', 'zbase', 'acct', 'tun', ?, CURRENT_TIMESTAMP)`, crypt.Of("cf-token"))
	must(`INSERT INTO storage_locations (id, name, kind, settings, restic_password, is_default, acknowledged_at) VALUES (1, 'nas', 'local', '{"path":"/srv"}', ?, TRUE, CURRENT_TIMESTAMP)`, crypt.Of("pw"))
	must(`INSERT INTO projects (id, name, app_service, services, volumes, domains, health, port, maintenance_since) VALUES
		(1, 'shop', 'web', '["web","db"]', '[{"name":"data","path":"/d"}]', '["shop.svnmns.com","shop.example.com"]', '/', 80, CURRENT_TIMESTAMP),
		(2, 'blog', 'web', '["web"]', '[]', '[]', '/', 80, NULL)`)
	must(`INSERT INTO project_hosts (project_id, name) VALUES (1, 'shop'), (1, 'shop-db'), (2, 'blog')`)
	must(`INSERT INTO project_volumes (project_id, name, location_id, placed_at) VALUES (1, 'data', 1, CURRENT_TIMESTAMP)`)
	must(`INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at) VALUES (1, 1, 'go', 'abc', 'main', CURRENT_TIMESTAMP)`)
	res, _ := w.db.Write.Exec(`INSERT INTO project_deletions (project_id, name, requested_by, delete_backups, heartbeat_at) VALUES (1, 'shop', 'token laptop', ?, CURRENT_TIMESTAMP)`, deleteBackups)
	w.deletion, _ = res.LastInsertId()

	w.cf.Zone("zbase", "svnmns.com", "active")
	w.cf.Zone("zcom", "example.com", "active")
	w.cf.Record("zbase", cloudflare.Record{Name: "shop.svnmns.com", Comment: "managed-by:houston project:shop"})
	w.cf.Record("zbase", cloudflare.Record{Name: "blog.svnmns.com", Comment: "managed-by:houston project:blog"})
	w.cf.Record("zcom", cloudflare.Record{Name: "shop.example.com", Comment: "managed-by:houston project:shop"})
	w.cf.Record("zcom", cloudflare.Record{Name: "shop2.example.com", Comment: "managed-by:houston project:shop2"})

	reg := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			io.WriteString(rw, `{"tags":["v1"]}`)
		case "HEAD":
			rw.Header().Set("Docker-Content-Digest", "sha256:abc")
		case "DELETE":
			w.registry = append(w.registry, r.URL.Path)
			rw.WriteHeader(202)
		}
	}))
	t.Cleanup(reg.Close)

	d := w.docker
	d.On(dockercmdtest.OK("27.3.1\n"), "version")
	d.On(dockercmdtest.OK("reg1\n"), "ps", "-q", "--filter", "label=com.docker.compose.project=houston")
	d.On(dockercmdtest.OK(`["REGISTRY_STORAGE_DELETE_ENABLED=true"]`), "inspect", "--format", "{{json .Config.Env}}")
	d.On(dockercmdtest.OK("c1\nc2\n"), "ps", "-aq", "--filter", "label=service=shop")
	d.On(dockercmdtest.OK("shop_data\nshop.g2_data\nshopping_data\nhouston-backup.shop\nblog_data\n"), "volume", "ls", "-q")
	d.On(dockercmdtest.OK("img1\nimg1\nimg2\n"), "images", "-q")
	d.On(dockercmdtest.OK(`{"sqlite":[],"warnings":[],"errors":[]}`), "run", "--rm", "--name", "houston-backup.shop.sqlite")
	d.On(dockercmdtest.OK(`{"message_type":"summary","snapshot_id":"`+strings.Repeat("f", 64)+`","total_bytes_processed":9}`), "run", "--rm", "--name", "houston-backup.shop.restic")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w.r = removal.Removal{DB: w.db, Docker: d, Registry: registry.Registry{URL: reg.URL, ComposeProject: "houston", Docker: d}, Cloudflare: w.cf.URL,
		Services: dns.Services{MissionControl: "http://mc:8080", Apps: "http://kamal-proxy:80"},
		Backups:  backup.Runner{DB: w.db, Docker: d, Tools: "tools:1", ToolsBin: "/app", Log: log}, Snapshots: &backup.Snapshots{Docker: d},
		Tools: "tools:1", KamalHome: "/home/houston/.kamal", RunnersDir: "/var/lib/houston/runners", Log: log}
	return w
}

func (w *world) row(t *testing.T) models.ProjectDeletion {
	t.Helper()
	d, err := models.New(w.db.Read).DeletionByID(context.Background(), w.deletion)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A deletion: the final snapshot, then everything of the project's and
// nothing of anyone else's; the registry's clean-up after.
func TestRemoval(t *testing.T) {
	w := setUp(t, false)
	w.cf.Loose = true                                                                                // what isn't the project's is left even when the list has it
	w.db.Write.Exec(`INSERT INTO project_hosts (project_id, name) VALUES (2, 'houston-kamal-shop')`) // another's name: left
	cleaned := int64(0)
	if err := w.r.Do(context.Background(), w.deletion, func(_ context.Context, id int64) error { cleaned = id; return nil }); err != nil {
		t.Fatal(err)
	}
	d := w.row(t)
	if d.Status != "go" || d.Step != "rows" || d.SnapshotID != strings.Repeat("f", 64) || d.SnapshotLocationID.Int64 != 1 || d.ProjectID.Valid || cleaned != w.deletion {
		t.Fatalf("deletion %s %q %s, cleaned %d\n%s", d.Status, d.Error, d.Step, cleaned, d.Log)
	}
	var projects, runs string
	w.db.Read.QueryRow(`SELECT group_concat(name) FROM projects`).Scan(&projects)
	w.db.Read.QueryRow(`SELECT group_concat(kind || ':' || status) FROM backup_runs`).Scan(&runs)
	if projects != "blog" || runs != "" {
		t.Errorf("left: projects %q, runs %q", projects, runs)
	}
	var names []string
	for _, zone := range []string{"zbase", "zcom"} {
		for _, r := range w.cf.Records(zone) {
			names = append(names, r.Name)
		}
	}
	if strings.Join(names, " ") != "blog.svnmns.com shop2.example.com" {
		t.Errorf("records left %v", names)
	}
	if routes := w.cf.Tunnel("acct", "tun"); strings.Contains(routes, "shop") || routes == "" {
		t.Errorf("routes %s", routes)
	}
	ran := strings.Join(w.docker.Ran(), "\n")
	for _, want := range []string{"rm -f c1 c2", "rm -f shop-db", "exec kamal-proxy kamal-proxy remove shop-web",
		"volume rm shop_data", "volume rm shop.g2_data", "volume rm houston-backup.shop", "rmi -f img1 img2",
		"run --rm --user 0 -v /srv:/location --entrypoint sh tools:1 -c ", " sh shop",
		"run --rm --user 0 -v /home/houston/.kamal:/kamal -v /var/lib/houston/runners:/runners --entrypoint sh tools:1 -c "} {
		if !strings.Contains(ran, want) {
			t.Errorf("no %q in\n%s", want, ran)
		}
	}
	for _, never := range []string{"volume rm shopping_data", "volume rm blog_data", "rm -f blog", " forget ", "rm -f houston-kamal-shop"} {
		if strings.Contains(ran, never) {
			t.Errorf("ran %q", never)
		}
	}
	if strings.Join(w.registry, " ") != "/v2/shop/manifests/sha256:abc" {
		t.Errorf("registry %v", w.registry)
	}
	for _, line := range []string{"== check", "ok  Docker 27.3.1", "== snapshot", "ok  final snapshot ffffffff in nas", "== rows", "GO: shop is deleted"} {
		if !strings.Contains(d.Log, line) {
			t.Errorf("no %q in the log\n%s", line, d.Log)
		}
	}
}

// Before anything is removed, a problem cancels: the project serves on.
func TestRemovalCancels(t *testing.T) {
	w := setUp(t, false)
	w.docker.On(dockercmd.Result{OK: true, Output: `["PATH=/bin"]`}, "inspect", "--format")
	fresh := &dockercmdtest.Fake{}
	fresh.On(dockercmdtest.OK("27\n"), "version")
	fresh.On(dockercmdtest.OK("reg1\n"), "ps")
	fresh.On(dockercmdtest.OK(`["PATH=/bin"]`), "inspect")
	w.r.Docker, w.r.Registry.Docker = fresh, fresh
	w.r.Do(context.Background(), w.deletion, nil)
	d := w.row(t)
	if d.Status != "no_go" || d.Error != "cancelled: the registry doesn't allow deletes yet: run the installer once more" || d.RemovingAt.Valid || !d.ProjectID.Valid {
		t.Errorf("= %s %q", d.Status, d.Error)
	}
	w.r.KamalHome = ""
	w.db.Write.Exec(`UPDATE project_deletions SET status = 'queued'`)
	w.r.Do(context.Background(), w.deletion, nil)
	if d := w.row(t); d.Error != "cancelled: Mission Control doesn't know where Kamal keeps its files: run the installer once more" {
		t.Errorf("no kamal home: %q", d.Error)
	}
}

// A removal step's failure stops it there; asking again resumes, past the
// check and the snapshot, and deletes the backups when asked.
func TestRemovalResumes(t *testing.T) {
	w := setUp(t, true)
	stuck := &dockercmdtest.Fake{}
	for _, r := range []struct {
		out  string
		args []string
	}{{"27\n", []string{"version"}}, {"reg1\n", []string{"ps", "-q"}}, {`["REGISTRY_STORAGE_DELETE_ENABLED=true"]`, []string{"inspect"}}, {"shop_data\n", []string{"volume", "ls"}}} {
		stuck.On(dockercmdtest.OK(r.out), r.args...)
	}
	stuck.On(dockercmdtest.Fail(1, "volume is in use"), "volume", "rm")
	w.r.Docker, w.r.Registry.Docker = stuck, stuck
	w.r.Do(context.Background(), w.deletion, nil)
	d := w.row(t)
	if d.Status != "no_go" || d.Error != "stopped at volumes: couldn't remove shop_data: volume is in use" || !d.RemovingAt.Valid {
		t.Fatalf("= %s %q", d.Status, d.Error)
	}

	var p models.Project
	p, _ = models.New(w.db.Read).ProjectByID(context.Background(), 1)
	w.db.Tx(context.Background(), func(tx *db.Tx) error {
		_, err := models.RequestDeletion(context.Background(), tx, p, "shop", true, "token laptop", time.Now())
		return err
	})
	w.docker.On(dockercmdtest.OK(`[{"id":"`+strings.Repeat("a", 64)+`"},{"id":"short"}]`), "run", "--rm", "-e", "RESTIC_PASSWORD", "-e", "RESTIC_REPOSITORY",
		"-v", "houston-restic-cache:/root/.cache/restic", "-v", "/srv:/repo", backup.ResticImage, "snapshots")
	w.db.Write.Exec(`INSERT INTO backup_runs (project_id, location_id, kind, reason, status, heartbeat_at) VALUES (1, 1, 'auto', 'manual', 'go', CURRENT_TIMESTAMP)`)
	w.r.Docker, w.r.Registry.Docker = w.docker, w.docker
	w.r.Do(context.Background(), w.deletion, nil)
	d = w.row(t)
	ran := strings.Join(w.docker.Ran(), "\n")
	if d.Status != "go" || strings.Contains(ran, "version --format") || !strings.Contains(ran, "forget --retry-lock 30m --prune "+strings.Repeat("a", 64)) ||
		strings.Contains(ran, " short") {
		t.Errorf("resumed %s %q\n%s", d.Status, d.Error, ran)
	}
}

// A project with something to keep and nowhere to keep it: cancelled,
// unless its backups are to go too.
func TestRemovalWantsStorage(t *testing.T) {
	w := setUp(t, false)
	w.db.Write.Exec(`UPDATE storage_locations SET acknowledged_at = NULL`)
	w.r.Do(context.Background(), w.deletion, nil)
	if d := w.row(t); d.Error != "cancelled: no backup storage to keep a final snapshot in (finish setup's storage step, or delete the backups too)" {
		t.Errorf("= %q", d.Error)
	}
}

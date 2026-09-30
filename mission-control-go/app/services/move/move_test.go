package move

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/test"
)

// railsDB is a copy of testdata/rails.sqlite3, the database the Rails app
// made (testdata/rails_fixture.rb), so a test can't change the original.
func railsDB(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/rails.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "production.sqlite3")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func useKeys(t *testing.T) {
	t.Helper()
	keys, _ := crypt.ParseKeys(crypt.NewKey())
	if err := crypt.Use(keys...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { crypt.Use() })
}

// The installation moves: every setting, its times, and its Cloudflare
// tokens decrypted from Rails' encryption and stored in gantry's.
func TestInstallation(t *testing.T) {
	useKeys(t)
	to := test.DB(t)
	from := railsDB(t)
	before, _ := os.ReadFile(from)
	report, err := Run(context.Background(), from, devKeys, to)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(report.Moved, ", "); got != "1 installation, 2 storage locations, 2 projects, 3 container names, 2 secrets, 1 volume, 1 runner, 3 deploys, 2 backup runs, 2 API tokens, 1 deletion, 1 Add project draft, 1 copy, 1 server update" {
		t.Errorf("report %q", got)
	}
	var (
		base, zone, dns, account, zoneID, tunnelID, release, releaseURL string
		portOpen                                                        bool
		apiToken, tunnelToken                                           crypt.String
		storedAPIToken                                                  string
		connected, checked, created, updated                            time.Time
		cleanup                                                         sql.NullTime
	)
	err = to.Read.QueryRow(`SELECT base_domain, time_zone, dns_mode, port_open, cloudflare_account_id, cloudflare_zone_id,
		cloudflare_api_token, cloudflare_api_token, cloudflare_connected_at, tunnel_id, tunnel_token,
		latest_release, latest_release_url, latest_release_checked_at, registry_cleanup_since, created_at, updated_at
		FROM installations WHERE id = 1`).Scan(&base, &zone, &dns, &portOpen, &account, &zoneID,
		&apiToken, &storedAPIToken, &connected, &tunnelID, &tunnelToken,
		&release, &releaseURL, &checked, &cleanup, &created, &updated)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, got, want string }{
		{"base domain", base, "houston.example"},
		{"time zone", zone, "America/Denver"},
		{"dns mode", dns, "tunnel"},
		{"account", account, "acct-1234"},
		{"zone", zoneID, "zone-5678"},
		{"tunnel", tunnelID, "tunnel-9abc"},
		{"release", release, "v0.4.27"},
		{"release url", releaseURL, "https://github.com/scttymn/houston/releases/tag/v0.4.27"},
		{"api token", apiToken.Reveal(), "cf-api-token-café"},
		{"tunnel token", tunnelToken.Reveal(), "tunnel-token-" + strings.Repeat("x", 300)},
	} {
		if c.got != c.want {
			t.Errorf("%s: %q, want %q", c.name, c.got, c.want)
		}
	}
	if portOpen {
		t.Error("port open: true, want false")
	}
	if !strings.HasPrefix(storedAPIToken, "gantry:v1:") {
		t.Errorf("the api token is stored as %q, not in gantry's encryption", storedAPIToken)
	}
	for _, c := range []struct {
		name      string
		got, want time.Time
	}{
		{"connected", connected, time.Date(2026, 9, 1, 12, 30, 15, 0, time.UTC)},
		{"checked", checked, time.Date(2026, 9, 30, 6, 0, 0, 0, time.UTC)},
		{"created", created, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		{"updated", updated, time.Date(2026, 9, 30, 6, 0, 0, 0, time.UTC)},
	} {
		if !c.got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, c.got, c.want)
		}
	}
	if cleanup.Valid {
		t.Errorf("registry cleanup since %v, want none", cleanup.Time)
	}
	if after, _ := os.ReadFile(from); string(after) != string(before) {
		t.Error("the Rails database changed")
	}
}

// The installation is one row, id 1, whatever id Rails gave it; were
// there two, the first is Rails' Installation.current.
func TestInstallationIsOne(t *testing.T) {
	useKeys(t)
	from := railsDB(t)
	r, _ := sql.Open("sqlite", from)
	for _, q := range []string{`UPDATE installations SET id = 7`,
		`INSERT INTO installations (id, base_domain, time_zone, port_open, created_at, updated_at) VALUES (9, 'second.example', 'UTC', 1, '2026-01-01', '2026-01-01')`} {
		if _, err := r.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	to := test.DB(t)
	if _, err := Run(context.Background(), from, devKeys, to); err != nil {
		t.Fatal(err)
	}
	var id int
	var base string
	to.Read.QueryRow(`SELECT id, base_domain FROM installations`).Scan(&id, &base)
	if id != 1 || base != "houston.example" || count(t, to.Read, "installations") != 1 {
		t.Errorf("= %d %s, %d rows", id, base, count(t, to.Read, "installations"))
	}
}

// The move is once: into an empty database, from the Rails schema it was
// written for, with keys that open Rails' values. Anything else moves
// nothing.
func TestMoveRefuses(t *testing.T) {
	useKeys(t)
	ctx := context.Background()

	to := test.DB(t)
	if _, err := Run(ctx, railsDB(t), devKeys, to); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, railsDB(t), devKeys, to); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Errorf("a second move: %v", err)
	}

	wrong := RailsKeys{PrimaryKey: "another-primary-key", Salt: devKeys.Salt}
	to = test.DB(t)
	if _, err := Run(ctx, railsDB(t), wrong, to); !errors.Is(err, ErrNotDecrypted) || !strings.Contains(err.Error(), "installation") {
		t.Errorf("wrong keys: %v", err)
	}
	if n := count(t, to.Read, "installations"); n != 0 {
		t.Errorf("wrong keys moved %d installations", n)
	}

	old := railsDB(t)
	o, _ := sql.Open("sqlite", old)
	if _, err := o.Exec(`DELETE FROM schema_migrations WHERE version = ?`, RailsSchemaVersion); err != nil {
		t.Fatal(err)
	}
	o.Close()
	if _, err := Run(ctx, old, devKeys, test.DB(t)); err == nil || !strings.Contains(err.Error(), RailsSchemaVersion) {
		t.Errorf("another schema: %v", err)
	}

	if _, err := Run(ctx, filepath.Join(t.TempDir(), "none.sqlite3"), devKeys, test.DB(t)); err == nil {
		t.Error("no database")
	}
}

func count(t *testing.T, d interface {
	QueryRow(string, ...any) *sql.Row
}, table string) (n int) {
	t.Helper()
	if err := d.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The projects and what hangs off them move with their ids, their JSON as
// the app reads it, their secrets decrypted from Rails' encryption and
// stored in gantry's, and Rails' NULLs as the Go tables have them.
func TestProjects(t *testing.T) {
	useKeys(t)
	ctx := context.Background()
	to := test.DB(t)
	if _, err := Run(ctx, railsDB(t), devKeys, to); err != nil {
		t.Fatal(err)
	}
	q := models.New(to.Read)
	shop, err := q.ProjectByName(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	blog, _ := q.ProjectByName(ctx, "blog")
	if shop.ID != 1 || blog.ID != 2 {
		t.Errorf("ids %d %d", shop.ID, blog.ID)
	}
	for _, c := range []struct{ name, got, want string }{
		{"services", fmt.Sprint(shop.Services.V), "[web db]"},
		{"domains", fmt.Sprint(shop.Domains.V), "[shop.houston.example shop.example.com]"},
		{"domain states", fmt.Sprint(shop.DomainStates.V), "map[shop.example.com:{OK points at the tunnel}]"},
		{"variables", fmt.Sprint(shop.Variables.V), "[{SECRET_KEY_BASE true} {SENTRY_DSN false}]"},
		{"volumes", fmt.Sprint(shop.Volumes.V), "[{data /rails/storage}]"},
		{"databases", fmt.Sprint(shop.Databases.V), "[{db postgres:17}]"},
		{"details", fmt.Sprintf("%+v", shop.Details.V), "{Images:map[db:postgres:17] CPUs:2 Memory: Console:bin/rails console}"},
		{"deploy rule", string(shop.DeployRule.V), `{"on":"tag","tags":"v*"}`},
		{"seen refs", fmt.Sprint(shop.SeenRefs.V), "map[refs/heads/main:" + strings.Repeat("a", 40) + "]"},
		{"numbers", fmt.Sprint(shop.Port, shop.DataGeneration, shop.KeepAuto, shop.KeepDeploy, shop.BackupLocationID.Int64), "3000 2 7 5 1"},
		{"texts", strings.Join([]string{shop.BackupSchedule, shop.RepoUrl, shop.Branch, shop.ComposePath, shop.DeployKeyPublic,
			shop.MaintenanceBy, shop.MaintenanceMessage, shop.MaintenancePage}, "|"),
			"daily 04:30|git@github.com:scttymn/shop.git|main|compose.yml|ssh-ed25519 AAAA shop|scotty|Back at noon|<h1>Closed</h1>"},
		{"deploy key", shop.DeployKeyPrivate.Reveal(), "-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("k", 400) + "\n-----END OPENSSH PRIVATE KEY-----\n"},
		{"webhook secret", shop.WebhookSecret.Reveal(), "whsec-shop"},
		{"times", fmt.Sprint(shop.MaintenanceSince.Time, shop.SyncedAt.Time, shop.CreatedAt), "2026-09-30 07:00:00 +0000 UTC 2026-09-30 06:30:00 +0000 UTC 2026-01-05 00:00:00 +0000 UTC"},
		// A first sync's project: Rails' NULLs are the Go defaults.
		{"blog", fmt.Sprint(blog.RepoUrl == "", blog.Branch == "", blog.MaintenancePage == "", blog.DeployKeyPrivate.Reveal() == "",
			blog.BackupLocationID.Valid, blog.MaintenanceSince.Valid, blog.Domains.V, blog.DomainStates.V, string(blog.DeployRule.V)),
			"true true true true false false [] map[]{}"}, // Sprint: no space before a string
	} {
		if c.got != c.want {
			t.Errorf("%s: %s\nwant %s", c.name, c.got, c.want)
		}
	}

	secrets, _ := models.Secrets(ctx, q, shop.ID)
	if secrets["SECRET_KEY_BASE"] != "s3cret-ünïcode" || secrets["SENTRY_DSN"] != "https://key@sentry.example/1"+strings.Repeat("0", 200) {
		t.Errorf("secrets %v", secrets)
	}
	if names, _ := q.ProjectHostNames(ctx, shop.ID); fmt.Sprint(names) != "[shop shop-db-g2]" {
		t.Errorf("shop's names %v", names)
	}

	var (
		credentials, password crypt.String
		plainSettings         string
		isDefault             bool
		pruned                sql.NullTime
	)
	to.Read.QueryRow(`SELECT settings, is_default, pruned_at FROM storage_locations WHERE name = 'nas'`).Scan(&plainSettings, &isDefault, &pruned)
	to.Read.QueryRow(`SELECT credentials, restic_password FROM storage_locations WHERE name = 'offsite'`).Scan(&credentials, &password)
	if plainSettings != `{"server":"192.168.0.10","export":"/volume1/houston"}` || !isDefault || !pruned.Valid ||
		credentials.Reveal() != `{"access_key_id":"AKIA123","secret_access_key":"s3cr3t/+="}` || password.Reveal() != strings.Repeat("r", 200) {
		t.Errorf("storage: %s %v %v %q %q", plainSettings, isDefault, pruned, credentials.Reveal(), password.Reveal())
	}

	restore, err := q.DeployByID(ctx, 2)
	queued, _ := q.DeployByID(ctx, 3)
	if err != nil || restore.Kind != "restore" || restore.Status != "in_flight" || restore.Generation != 2 || restore.Runner != "houston-runner-1" ||
		!restore.OwnedBy("two") || !restore.SwitchedAt.Valid || restore.SourceLocationID.Int64 != 1 || restore.SyncPayload.String != `{"name":"shop","port":3000}` {
		t.Errorf("the restore: %+v %v", restore, err)
	}
	if queued.Runner != "" || queued.Step != "" || queued.TokenDigest != "" || !queued.Fresh || queued.SyncPayload.Valid || queued.FinishedAt.Valid {
		t.Errorf("the queued deploy: %+v", queued)
	}
	var runner string
	var seen time.Time
	to.Read.QueryRow(`SELECT name, last_seen_at FROM runners`).Scan(&runner, &seen)
	if runner != "houston-runner-1" || !seen.Equal(time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("runner %s %v", runner, seen)
	}
}

// Backup runs move with their day, size and what they found.
func TestBackupRuns(t *testing.T) {
	useKeys(t)
	to := test.DB(t)
	if _, err := Run(context.Background(), railsDB(t), devKeys, to); err != nil {
		t.Fatal(err)
	}
	var (
		kind, reason, status, day, found, snapshot, log string
		bytes                                           int64
		finished                                        time.Time
	)
	to.Read.QueryRow(`SELECT kind, reason, status, scheduled_for, found, snapshot_id, bytes, log, finished_at FROM backup_runs WHERE id = 1`).
		Scan(&kind, &reason, &status, &day, &found, &snapshot, &bytes, &log, &finished)
	if kind != "auto" || reason != "schedule" || status != "go" || day != "2026-09-30" || found != `{"volumes":["data"],"databases":["db"]}` ||
		snapshot != strings.Repeat("5eed", 16) || bytes != 123456789 || log != "restic backup\n" || !finished.Equal(time.Date(2026, 9, 30, 3, 2, 0, 0, time.UTC)) {
		t.Errorf("the scheduled one: %s %s %s %s %s %s %d %q %v", kind, reason, status, day, found, snapshot, bytes, log, finished)
	}
	var deployNumber int
	var errText string
	var noBytes sql.NullInt64
	var noDay sql.NullString
	to.Read.QueryRow(`SELECT deploy_number, error, bytes, scheduled_for FROM backup_runs WHERE id = 2`).Scan(&deployNumber, &errText, &noBytes, &noDay)
	if deployNumber != 1 || errText != "restic: repository is locked" || noBytes.Valid || noDay.Valid {
		t.Errorf("the deploy's: %d %q %v %v", deployNumber, errText, noBytes, noDay)
	}
}

// Personal tokens keep working: only their SHA-256 was ever kept.
func TestAPITokens(t *testing.T) {
	useKeys(t)
	to := test.DB(t)
	if _, err := Run(context.Background(), railsDB(t), devKeys, to); err != nil {
		t.Fatal(err)
	}
	token, err := models.New(to.Read).APITokenByDigest(context.Background(), models.Digest("hou_laptop-token"))
	if err != nil || token.Name != "laptop" || !token.LastUsedAt.Time.Equal(time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("laptop's: %+v %v", token, err)
	}
}

// An Add project draft moves with its key, decrypted and encrypted again.
func TestRepoLinks(t *testing.T) {
	useKeys(t)
	to := test.DB(t)
	if _, err := Run(context.Background(), railsDB(t), devKeys, to); err != nil {
		t.Fatal(err)
	}
	l, err := models.New(to.Read).LinkByID(context.Background(), 1)
	if err != nil || l.DeployKeyPrivate.Reveal() != "-----NEW KEY-----" || l.WebhookSecret.Reveal() != "new-whsec" ||
		l.Preview.String != `{"sync":{"name":"new"}}` || l.Branch != "main" {
		t.Errorf("draft %+v %v", l, err)
	}
}

// A deleted project's record moves, with who asked and its final snapshot.
func TestDeletions(t *testing.T) {
	useKeys(t)
	to := test.DB(t)
	if _, err := Run(context.Background(), railsDB(t), devKeys, to); err != nil {
		t.Fatal(err)
	}
	d, err := models.New(to.Read).DeletionByID(context.Background(), 1)
	if err != nil || d.Name != "old" || d.RequestedBy != "scotty" || d.ProjectID.Valid || d.SnapshotLocationID.Int64 != 1 || d.Status != "go" || !d.RemovingAt.Valid {
		t.Errorf("= %+v %v", d, err)
	}
}

// A copy moves, its names and who asked renamed, the hosts it took kept.
func TestCopies(t *testing.T) {
	useKeys(t)
	to := test.DB(t)
	if _, err := Run(context.Background(), railsDB(t), devKeys, to); err != nil {
		t.Fatal(err)
	}
	c, err := models.New(to.Read).CopyByID(context.Background(), 1)
	if err != nil || c.FromName != "shop" || c.ToName != "blog" || c.RequestedBy != "token laptop" || c.Status != "go" ||
		c.ProjectID.Int64 != 2 || c.FromProjectID.Int64 != 1 || c.DeployID.Int64 != 3 || c.SnapshotRunID.Int64 != 1 ||
		strings.Join(c.HandedOver.V, ",") != "shop.example.com" || !c.HandedOverAt.Time.Equal(time.Date(2026, 9, 30, 8, 30, 0, 0, time.UTC)) || c.UndoneAt.Valid {
		t.Errorf("= %+v %v", c, err)
	}
}

// The server's updates move: the last one says how it went.
func TestServerUpdates(t *testing.T) {
	useKeys(t)
	to := test.DB(t)
	if _, err := Run(context.Background(), railsDB(t), devKeys, to); err != nil {
		t.Fatal(err)
	}
	u, err := models.New(to.Read).LastUpdate(context.Background())
	if err != nil || u.ToVersion != "v0.4.27" || u.FromVersion != "v0.4.26" || u.Status != "rolled_back" || u.Step != "v0.4.27 didn't install; putting v0.4.26 back" ||
		u.Log != "==> houston update: installing v0.4.27\n" || !u.StartedAt.Equal(time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)) || !u.FinishedAt.Valid {
		t.Errorf("= %+v %v", u, err)
	}
}

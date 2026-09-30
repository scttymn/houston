package move

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

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
	if got := strings.Join(report.Moved, ", "); got != "1 installation" {
		t.Errorf("report %q", got)
	}
	var (
		base, zone, dns, account, zoneID, tunnelID, release, releaseURL string
		portOpen                                                       bool
		apiToken, tunnelToken                                          crypt.String
		storedAPIToken                                                 string
		connected, checked, created, updated                           time.Time
		cleanup                                                        sql.NullTime
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

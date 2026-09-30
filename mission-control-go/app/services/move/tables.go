package move

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"
)

// kind is how a Rails column becomes a Go one.
type kind int

const (
	text      kind = iota // NULL is ''
	integer               // NULL is NULL (a nullable id)
	boolean               // Rails' 1/0 (or t/f)
	jsonText              // JSON as Rails wrote it (its columns are NOT NULL)
	timestamp             // Rails' UTC text; NULL is NULL
	encrypted             // Rails' encryption, into gantry's; NULL is ''
	nullable              // text whose NULL stays NULL (a restore's kept sync)
	one                   // the one row's id: 1, whatever Rails' was
)

// column is one of a table's: its Go name, the Rails one when it differs,
// and how it converts.
type column struct {
	name  string
	kind  kind
	rails string
}

// table is a table moved row by row, ids kept (what refers to a row still
// does).
type table struct {
	name    string // the Go table
	rails   string // the Rails one, when it differs
	words   string // for the report: "storage location"
	columns []column
	first   bool // only the first row (the installation: one row)
}

func c(name string, k kind) column              { return column{name: name, kind: k} }
func renamed(name, rails string, k kind) column { return column{name: name, kind: k, rails: rails} }

func stamps() []column { return []column{c("created_at", timestamp), c("updated_at", timestamp)} }

// tables are moved in this order: each after what it refers to.
var tables = []table{
	{name: "installations", words: "installation", first: true, columns: append([]column{c("id", one), c("base_domain", text),
		c("time_zone", text), c("dns_mode", text), c("port_open", boolean), c("cloudflare_account_id", text), c("cloudflare_zone_id", text),
		c("cloudflare_api_token", encrypted), c("cloudflare_connected_at", timestamp), c("tunnel_id", text), c("tunnel_token", encrypted),
		c("latest_release", text), c("latest_release_url", text), c("latest_release_checked_at", timestamp),
		c("registry_cleanup_since", timestamp)}, stamps()...)},
	{name: "storage_locations", words: "storage location", columns: append([]column{c("id", integer), c("name", text), c("kind", text),
		c("settings", jsonText), c("credentials", encrypted), c("restic_password", encrypted), renamed("is_default", `"default"`, boolean),
		c("acknowledged_at", timestamp), c("verified_at", timestamp), c("pruned_at", timestamp), c("prune_error", text)}, stamps()...)},
	{name: "projects", words: "project", columns: append([]column{c("id", integer), c("name", text), c("app_service", text),
		c("services", jsonText), c("domains", jsonText), c("domain_states", jsonText), c("variables", jsonText), c("volumes", jsonText), c("databases", jsonText),
		c("details", jsonText), c("deploy_rule", jsonText), c("health", text), c("port", integer), c("data_generation", integer),
		c("keep_auto", integer), c("keep_deploy", integer), c("backup_schedule", text), c("backup_location_id", integer),
		c("repo_url", text), c("branch", text), c("compose_path", text), c("deploy_key_private", encrypted), c("deploy_key_public", text),
		c("webhook_secret", encrypted), c("webhook_verified_at", timestamp), c("seen_refs", jsonText), c("last_checked_at", timestamp),
		c("last_check_error", text), c("maintenance_since", timestamp), c("maintenance_by", text), c("maintenance_message", text),
		c("maintenance_page", text), c("synced_at", timestamp)}, stamps()...)},
	{name: "project_hosts", words: "container name", columns: append([]column{c("id", integer), c("project_id", integer), c("name", text)}, stamps()...)},
	{name: "secrets", words: "secret", columns: append([]column{c("id", integer), c("project_id", integer), c("key", text), c("value", encrypted)}, stamps()...)},
	{name: "project_volumes", words: "volume", columns: append([]column{c("id", integer), c("project_id", integer), c("name", text),
		c("location_id", integer), c("placed_at", timestamp)}, stamps()...)},
	{name: "runners", words: "runner", columns: append([]column{c("id", integer), c("name", text), c("last_seen_at", timestamp)}, stamps()...)},
	{name: "deploys", words: "deploy", columns: append([]column{c("id", integer), c("project_id", integer), c("number", integer),
		c("kind", text), c("status", text), c("sha", text), c("ref", text), c("fresh", boolean), c("generation", integer), c("step", text),
		c("error", text), c("log", text), c("runner", text), c("proposed_name", text), c("token_digest", text), c("heartbeat_at", timestamp),
		c("finished_at", timestamp), c("switched_at", timestamp), c("source_location_id", integer), c("source_snapshot_id", text),
		c("sync_payload", nullable)}, stamps()...)},
}

// report is how many rows moved, in words: "2 projects".
func (t table) report(n int) string {
	if n == 1 {
		return "1 " + t.words
	}
	return fmt.Sprintf("%d %ss", n, t.words)
}

// move copies t's rows from Rails into tx, and is how many.
func (t table) move(ctx context.Context, r *rails, tx *db.Tx) (int, error) {
	from := t.rails
	if from == "" {
		from = t.name
	}
	selects := make([]string, len(t.columns))
	names := make([]string, len(t.columns))
	for i, col := range t.columns {
		selects[i], names[i] = col.name, col.name
		if col.rails != "" {
			selects[i] = col.rails
		}
	}
	query := "SELECT " + strings.Join(selects, ", ") + " FROM " + from + " ORDER BY id"
	if t.first {
		query += " LIMIT 1"
	}
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	insert := "INSERT INTO " + t.name + " (" + strings.Join(names, ", ") + ") VALUES (?" + strings.Repeat(", ?", len(names)-1) + ")"
	values := make([]sql.NullString, len(t.columns))
	dest := make([]any, len(values))
	for i := range values {
		dest[i] = &values[i]
	}
	n := 0
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return n, err
		}
		args := make([]any, len(values))
		for i, col := range t.columns {
			if args[i], err = r.convert(col, values[i]); err != nil {
				return n, fmt.Errorf("row %s, %s: %w", values[0].String, col.name, err)
			}
		}
		if _, err := tx.ExecContext(ctx, insert, args...); err != nil {
			return n, fmt.Errorf("row %s: %w", values[0].String, err)
		}
		n++
	}
	return n, rows.Err()
}

// convert is a Rails value as its Go column takes it.
func (r *rails) convert(col column, v sql.NullString) (any, error) {
	switch col.kind {
	case text, jsonText:
		return v.String, nil
	case one:
		return int64(1), nil
	case nullable:
		if !v.Valid {
			return nil, nil
		}
		return v.String, nil
	case integer:
		if !v.Valid {
			return nil, nil
		}
		return strconv.ParseInt(v.String, 10, 64)
	case boolean:
		return v.String == "1" || v.String == "t" || v.String == "true", nil
	case timestamp:
		t, err := railsTime(v)
		if err != nil || !t.Valid {
			return nil, err
		}
		return t.Time, nil
	case encrypted:
		plain, err := r.decrypt(v)
		return crypt.Of(plain), err
	}
	return nil, fmt.Errorf("a column of kind %d", col.kind)
}

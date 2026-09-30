package move

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/scttymn/gantry/db"
)

// RailsSchemaVersion is the Rails database the move is written for: the
// last of its migrations (mission_control/db/schema.rb). A database at
// another version is refused, not guessed at.
const RailsSchemaVersion = "20260929220000"

// Report is what moved, a line a table ("1 installation").
type Report struct{ Moved []string }

// Run moves the Rails app's database at railsPath into to, which must be
// new: it reads Rails' values with keys and writes each table's rows in
// one transaction, so a failure moves nothing. The Rails database is only
// read.
func Run(ctx context.Context, railsPath string, keys RailsKeys, to *db.DB) (Report, error) {
	var report Report
	c, err := NewRailsCipher(keys)
	if err != nil {
		return report, err
	}
	from, err := openRails(ctx, railsPath)
	if err != nil {
		return report, err
	}
	defer from.Close()
	var n int
	if err := to.Read.QueryRowContext(ctx, `SELECT count(*) FROM installations`).Scan(&n); err != nil {
		return report, err
	}
	if n > 0 {
		return report, errors.New("move: this database already has an installation: the move is into a new one")
	}
	src := &rails{db: from, cipher: c}
	err = to.Tx(ctx, func(tx *db.Tx) error {
		for _, t := range tables {
			moved, err := t.move(ctx, src, tx)
			if err != nil {
				return fmt.Errorf("move: %s: %w", t.name, err)
			}
			report.Moved = append(report.Moved, fmt.Sprintf("%d %s", moved, t.name))
		}
		return nil
	})
	return report, err
}

// tables are moved in order, each by its own func (installation.go, ...).
var tables = []struct {
	name string
	move func(context.Context, *rails, *db.Tx) (int, error)
}{
	{"installation", moveInstallation},
}

// openRails opens the Rails database read-only, and checks it's at the
// version the move is written for.
func openRails(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("move: the Rails database: %w", err)
	}
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	var version string
	err = d.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version)
	if err == nil && version != RailsSchemaVersion {
		err = fmt.Errorf("it's at migration %s; the move is written for %s", version, RailsSchemaVersion)
	}
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("move: the Rails database %s: %w", path, err)
	}
	return d, nil
}

// rails is the Rails database and what reads its encrypted columns.
type rails struct {
	db     *sql.DB
	cipher *RailsCipher
}

// decrypt is an encrypted column's value (NULL is "").
func (r *rails) decrypt(stored sql.NullString) (string, error) {
	if !stored.Valid {
		return "", nil
	}
	return r.cipher.Decrypt(stored.String)
}

// railsTime reads a Rails datetime column, stored as text in UTC
// ("2006-01-02 15:04:05.000000"). NULL is a zero NullTime.
func railsTime(s sql.NullString) (sql.NullTime, error) {
	if !s.Valid || s.String == "" {
		return sql.NullTime{}, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", time.RFC3339Nano} {
		if t, err := time.ParseInLocation(layout, s.String, time.UTC); err == nil {
			return sql.NullTime{Time: t.UTC(), Valid: true}, nil
		}
	}
	return sql.NullTime{}, fmt.Errorf("a time %q isn't Rails'", s.String)
}

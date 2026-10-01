package backup

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/scttymn/gantry/db" // the sqlite driver
)

func makeDB(t *testing.T, path, value string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	d, err := sql.Open("sqlite", fileURI(path)+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`CREATE TABLE t (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO t VALUES (?)`, value); err != nil {
		t.Fatal(err)
	}
}

func value(t *testing.T, path string) string {
	t.Helper()
	d, err := sql.Open("sqlite", fileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var v string
	if err := d.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// The SQLite databases in the app's volumes are found (a regular file of
// at least 512 bytes with SQLite's header, links not followed) and each is
// copied consistently; the exclude file keeps their live files out of the
// file copy.
func TestCopySQLite(t *testing.T) {
	data, out := t.TempDir(), t.TempDir()
	makeDB(t, filepath.Join(data, "app", "production.sqlite3"), "app's")
	makeDB(t, filepath.Join(data, "app", "db", "queue [1].sqlite3"), "queue's")
	makeDB(t, filepath.Join(data, "logs", "a*b?.db"), "odd")
	os.WriteFile(filepath.Join(data, "app", "small"), []byte("SQLite format 3\x00"), 0o644)
	os.WriteFile(filepath.Join(data, "app", "big.txt"), []byte(strings.Repeat("x", 600)), 0o644)
	os.Symlink(filepath.Join(data, "app", "production.sqlite3"), filepath.Join(data, "app", "link.sqlite3"))
	os.WriteFile(filepath.Join(data, "loose-file"), []byte("not a volume"), 0o644)

	report := CopySQLite(data, out)
	if len(report.Errors) > 0 || len(report.Warnings) > 0 {
		t.Fatalf("report %+v", report)
	}
	var got []string
	for _, c := range report.SQLite {
		got = append(got, c.Volume+":"+c.Path+"→"+c.File)
		if c.Mode == 0 {
			t.Errorf("%s: no mode", c.Path)
		}
	}
	if strings.Join(got, " ") != "app:db/queue [1].sqlite3→sqlite/1.sqlite3 app:production.sqlite3→sqlite/2.sqlite3 logs:a*b?.db→sqlite/3.sqlite3" {
		t.Errorf("copied %v", got)
	}
	if v := value(t, filepath.Join(out, "sqlite", "2.sqlite3")); v != "app's" {
		t.Errorf("the copy has %q", v)
	}
	exclude, _ := os.ReadFile(filepath.Join(out, ".houston", "exclude"))
	want := "/data/app/db/queue \\[1].sqlite3\n/data/app/db/queue \\[1].sqlite3-wal\n/data/app/db/queue \\[1].sqlite3-shm\n/data/app/db/queue \\[1].sqlite3-journal\n" +
		"/data/app/production.sqlite3\n/data/app/production.sqlite3-wal\n/data/app/production.sqlite3-shm\n/data/app/production.sqlite3-journal\n" +
		"/data/logs/a\\*b\\?.db\n/data/logs/a\\*b\\?.db-wal\n/data/logs/a\\*b\\?.db-shm\n/data/logs/a\\*b\\?.db-journal\n"
	if string(exclude) != want {
		t.Errorf("exclude\n%s\nwant\n%s", exclude, want)
	}
	b, _ := json.Marshal(report)
	if !strings.HasPrefix(string(b), `{"sqlite":[{"volume":"app","path":"db/queue [1].sqlite3","file":"sqlite/1.sqlite3","uid":`) ||
		!strings.HasSuffix(string(b), `"warnings":[],"errors":[]}`) {
		t.Errorf("json %s", b)
	}
}

// A database that can't be copied is an error; its name with a line break
// is copied, its live file kept in the file copy (a warning).
func TestCopySQLiteTroubles(t *testing.T) {
	data, out := t.TempDir(), t.TempDir()
	makeDB(t, filepath.Join(data, "app", "line\nbreak.db"), "x")
	broken := filepath.Join(data, "app", "broken.db")
	os.WriteFile(broken, append([]byte("SQLite format 3\x00"), make([]byte, 600)...), 0o644)
	report := CopySQLite(data, out)
	if len(report.Errors) != 1 || report.Errors[0].Path != "broken.db" || report.Errors[0].Message == "" {
		t.Errorf("errors %+v", report.Errors)
	}
	if len(report.SQLite) != 1 || len(report.Warnings) != 1 || report.Warnings[0] != "app/line\nbreak.db: its name has a line break, so its live file is backed up as well" {
		t.Errorf("copied %+v, warnings %q", report.SQLite, report.Warnings)
	}
}

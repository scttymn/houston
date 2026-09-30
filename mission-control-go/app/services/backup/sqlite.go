// Package backup is what a backup runs besides restic: finding and copying
// the app's SQLite databases (its own subcommand, run in a helper container
// from Mission Control's image).
package backup

import (
	"bytes"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

const (
	sqliteHeader = "SQLite format 3\x00"
	minSize      = 512
	maxDatabases = 1000
	maxWarnings  = 20
)

// Copied is a database copied for a backup: where it was, where its copy
// is in the staging volume, and its owner and permissions (a restore puts
// the copy back as the app's to write).
type Copied struct {
	Volume string `json:"volume"`
	Path   string `json:"path"`
	File   string `json:"file"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	Mode   int    `json:"mode"`
}

// Problem is a database that couldn't be copied.
type Problem struct {
	Volume  string `json:"volume"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Report is what CopySQLite did: the last line of its subcommand's output.
type Report struct {
	SQLite   []Copied  `json:"sqlite"`
	Warnings []string  `json:"warnings"`
	Errors   []Problem `json:"errors"`
}

// CopySQLite finds the SQLite databases in the volumes under data (each at
// data/<volume>) and copies each, consistently while the app writes, to
// out/sqlite/<n>.sqlite3. out/.houston/exclude lists each live file and its
// -wal, -shm and -journal as literal restic patterns, so the file copy
// leaves them out. (The Rails app's lib/backup/sqlite.rb.)
func CopySQLite(data, out string) Report {
	r := Report{SQLite: []Copied{}, Warnings: []string{}, Errors: []Problem{}}
	type found struct{ volume, path string }
	var databases []found
	unreadable := 0
	volumes, _ := os.ReadDir(data)
	for _, v := range volumes {
		if !v.IsDir() {
			continue
		}
		filepath.WalkDir(filepath.Join(data, v.Name()), func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				err = func() error {
					info, err := d.Info()
					if err != nil || info.Size() < minSize {
						return err
					}
					f, err := os.Open(path)
					if err != nil {
						return err
					}
					defer f.Close()
					head := make([]byte, len(sqliteHeader))
					if n, _ := f.Read(head); n == len(head) && string(head) == sqliteHeader {
						databases = append(databases, found{v.Name(), path})
					}
					return nil
				}()
			}
			if err != nil {
				unreadable++
				if unreadable <= maxWarnings {
					r.Warnings = append(r.Warnings, fmt.Sprintf("%s: %v", strings.TrimPrefix(path, data+"/"), err))
				}
			}
			return nil
		})
	}
	sort.Slice(databases, func(i, j int) bool {
		return databases[i].volume < databases[j].volume || databases[i].volume == databases[j].volume && databases[i].path < databases[j].path
	})
	if unreadable > maxWarnings {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d more files couldn't be checked", unreadable-maxWarnings))
	}
	if len(databases) > maxDatabases {
		r.Errors = append(r.Errors, Problem{Message: fmt.Sprintf("more than %d SQLite databases; Houston stops there", maxDatabases)})
		databases = nil
	}
	os.MkdirAll(filepath.Join(out, "sqlite"), 0o755)
	os.MkdirAll(filepath.Join(out, ".houston"), 0o755)
	var patterns bytes.Buffer
	for i, db := range databases {
		relative := strings.TrimPrefix(db.path, filepath.Join(data, db.volume)+"/")
		if !utf8.ValidString(relative) {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s/%s: its name isn't UTF-8, so it's backed up only as a file", db.volume, strings.ToValidUTF8(relative, "\uFFFD")))
			continue
		}
		file := fmt.Sprintf("sqlite/%d.sqlite3", i+1)
		if err := copyDatabase(db.path, filepath.Join(out, file)); err != nil {
			r.Errors = append(r.Errors, Problem{Volume: db.volume, Path: relative, Message: err.Error()})
			continue
		}
		c := Copied{Volume: db.volume, Path: relative, File: file}
		if info, err := os.Stat(db.path); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				c.UID, c.GID, c.Mode = int(st.Uid), int(st.Gid), int(st.Mode&0o7777)
			}
		}
		r.SQLite = append(r.SQLite, c)
		if strings.ContainsAny(db.path, "\r\n") {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s/%s: its name has a line break, so its live file is backed up as well", db.volume, relative))
			continue
		}
		literal := globEscape.Replace("/data" + strings.TrimPrefix(db.path, data))
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			patterns.WriteString(literal + suffix + "\n")
		}
	}
	os.WriteFile(filepath.Join(out, ".houston", "exclude"), patterns.Bytes(), 0o644)
	return r
}

// globEscape makes a path a literal restic pattern.
var globEscape = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)

// copyDatabase copies a database that may be in use: VACUUM INTO reads it
// in one transaction, so the copy is consistent however the app writes.
func copyDatabase(from, to string) error {
	d, err := sql.Open("sqlite", fileURI(from)+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer d.Close()
	_, err = d.Exec(`VACUUM INTO ?`, to)
	return err
}

// fileURI is path as an SQLite URI filename: its %, ? and # escaped, so
// a database named "a?.db" is that file.
func fileURI(path string) string {
	return "file:" + strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

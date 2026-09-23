package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// x-houston.maintenance: the project's own maintenance page, a file in the
// repo next to compose.yml.
func TestLoad_MaintenancePage(t *testing.T) {
	page := "<!doctype html><h1>{{project}}</h1><p>{{message}}</p>\n"
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "public"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "public/maintenance.html"), []byte(page), 0o644))
	compose := filepath.Join(dir, "compose.yml")
	write := func(xh string) {
		t.Helper()
		must(t, os.WriteFile(compose, []byte(doc{xh: "  health: /up\n" + xh}.String()), 0o644))
	}

	write("  maintenance: public/maintenance.html\n")
	if p := mustLoad(t, compose); p.Houston.MaintenancePage != page {
		t.Errorf("MaintenancePage = %q", p.Houston.MaintenancePage)
	}
	write("")
	if p := mustLoad(t, compose); p.Houston.MaintenancePage != "" {
		t.Errorf("no key: MaintenancePage = %q", p.Houston.MaintenancePage)
	}

	outside := filepath.Join(filepath.Dir(dir), "outside.html")
	must(t, os.WriteFile(outside, []byte("<p>secret</p>"), 0o644))
	t.Cleanup(func() { os.Remove(outside) })
	must(t, os.WriteFile(filepath.Join(dir, "big.html"), []byte(strings.Repeat("x", 512*1024+1)), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "latin1.html"), []byte("caf\xe9"), 0o644))
	// Symlinks out of the repo: on a runner, the checkout sits next to its
	// deploy keys, and the page is served to anyone.
	must(t, os.Symlink(outside, filepath.Join(dir, "link.html")))
	must(t, os.Symlink(filepath.Dir(dir), filepath.Join(dir, "up")))
	must(t, os.Symlink("public/maintenance.html", filepath.Join(dir, "inside-link.html")))
	write("  maintenance: inside-link.html\n")
	if p := mustLoad(t, compose); p.Houston.MaintenancePage != page {
		t.Errorf("a symlink within the repo: MaintenancePage = %q", p.Houston.MaintenancePage)
	}
	for _, c := range []struct{ value, msg string }{
		{"42", "must be a path"},
		{"/etc/passwd", "relative"},
		{"../outside.html", "inside"},
		{"public/../../outside.html", "inside"},
		{"public/missing.html", "doesn't exist"},
		{"public", "a file"},
		{"big.html", "512 KB"},
		{"latin1.html", "UTF-8"},
		{"link.html", "inside"},
		{"up/outside.html", "inside"},
	} {
		write("  maintenance: " + c.value + "\n")
		problems := loadProblems(t, compose)
		if len(problems) != 1 || problems[0].Path != "x-houston.maintenance" || !strings.Contains(problems[0].Msg, c.msg) {
			t.Errorf("maintenance: %s → %+v, want one problem mentioning %q", c.value, problems, c.msg)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// <service>-g<n> is a data generation's accessory name (docs/plans/restore.md,
// Batch 3): a compose service can't be called that.
func TestLoad_ReservedGenerationNames(t *testing.T) {
	path := writeCompose(t, doc{services: "  db-g2:\n    image: postgres:17\n"}.String())
	problems := loadProblems(t, path)
	if len(problems) != 1 || problems[0].Path != "services.db-g2" || !strings.Contains(problems[0].Msg, "reserved for Houston's data generations") {
		t.Errorf("problems = %+v", problems)
	}
	mustLoad(t, writeCompose(t, doc{services: "  db-go:\n    image: postgres:17\n  g2:\n    image: redis:7\n"}.String()))
}

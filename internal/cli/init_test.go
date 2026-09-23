package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sevenmoons/houston/internal/project"
)

// railsApp copies testdata/rails into a fresh folder named dirName and
// returns its path.
func railsApp(t *testing.T, dirName string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), dirName)
	must(t, filepath.WalkDir("testdata/rails", func(src string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("testdata/rails", src)
		dst := filepath.Join(dir, rel)
		if e.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	}))
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func expected(t *testing.T, name string) string {
	return read(t, filepath.Join("testdata", "rails-expected", name))
}

// snapshot reads every file under dir, to prove a run changed nothing.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	must(t, filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			files[p] = read(t, p)
		}
		return err
	}))
	return files
}

func sameFiles(t *testing.T, before, after map[string]string) {
	t.Helper()
	for p, content := range after {
		if b, ok := before[p]; !ok {
			t.Errorf("created %s", p)
		} else if b != content {
			t.Errorf("changed %s", p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			t.Errorf("removed %s", p)
		}
	}
}

func initIn(t *testing.T, dir string, input string, args ...string) (int, string, string) {
	t.Helper()
	d := &fakeDocker{}
	code, stdout, stderr := runWithInput(d, input, append([]string{"-f", filepath.Join(dir, "compose.yml"), "init"}, args...)...)
	if d.calls() != 0 {
		t.Errorf("init called docker: %q %q", d.outputs, d.runs)
	}
	return code, stdout, stderr
}

func TestInit_RailsSQLite(t *testing.T) {
	terminal(t, false)
	dir := railsApp(t, "demo")
	gitignore := read(t, filepath.Join(dir, ".gitignore"))

	code, stdout, stderr := initIn(t, dir, "")

	if code != 0 {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr)
	}
	if got, want := read(t, filepath.Join(dir, "compose.yml")), expected(t, "compose.yml"); got != want {
		t.Errorf("compose.yml =\n%s\nwant\n%s", got, want)
	}
	if got, want := read(t, filepath.Join(dir, "Dockerfile")), expected(t, "Dockerfile"); got != want {
		t.Errorf("Dockerfile =\n%s\nwant\n%s", got, want)
	}
	if got := read(t, filepath.Join(dir, ".env")); got != "RAILS_MASTER_KEY=\n" {
		t.Errorf(".env = %q", got)
	}
	if got := read(t, filepath.Join(dir, ".gitignore")); got != gitignore {
		t.Errorf(".gitignore changed although /.env* already covers .env:\n%s", got)
	}
	for _, line := range []string{
		"added dev and test stages to Dockerfile; named the final stage production",
		"created compose.yml",
		"created .env (RAILS_MASTER_KEY)",
		"Next: houston dev",
	} {
		if !strings.Contains(stdout, line) {
			t.Errorf("stdout doesn't say %q:\n%s", line, stdout)
		}
	}
	if _, err := project.Load(filepath.Join(dir, "compose.yml")); err != nil {
		t.Errorf("the written compose.yml doesn't load:\n%v", err)
	}
	for _, f := range []string{"Dockerfile", "compose.yml"} {
		if info, err := os.Stat(filepath.Join(dir, f)); err != nil || info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %v, want 0644 (kept from the original, or the default for new files)", f, info.Mode().Perm())
		}
	}
}

func TestInit_ProjectName(t *testing.T) {
	cases := []struct {
		name     string
		dir      string
		terminal bool
		input    string
		want     string // "" → expect a usage error
	}{
		{"folder name", "My_App", false, "", "my-app"},
		{"typed name", "demo", true, "equip2\n", "equip2"},
		{"empty takes default", "demo", true, "\n", "demo"},
		{"invalid typed name", "demo", true, "Bad_Name\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			terminal(t, tc.terminal)
			dir := railsApp(t, tc.dir)
			before := snapshot(t, dir)

			code, stdout, stderr := initIn(t, dir, tc.input)

			if tc.want == "" {
				if code != 2 {
					t.Errorf("exit = %d, want 2", code)
				}
				sameFiles(t, before, snapshot(t, dir))
				return
			}
			if code != 0 {
				t.Fatalf("exit = %d (stderr: %s)", code, stderr)
			}
			if !strings.HasPrefix(read(t, filepath.Join(dir, "compose.yml")), "name: "+tc.want+"\n") {
				t.Errorf("compose.yml name isn't %q", tc.want)
			}
			if prompted := strings.Contains(stdout, "Project name"); prompted != tc.terminal {
				t.Errorf("prompted = %v with terminal = %v", prompted, tc.terminal)
			}
		})
	}
}

func TestInit_IdempotentAndResumes(t *testing.T) {
	terminal(t, false)
	t.Run("second run changes nothing", func(t *testing.T) {
		dir := railsApp(t, "demo")
		initIn(t, dir, "")
		before := snapshot(t, dir)

		code, stdout, _ := initIn(t, dir, "")

		if code != 0 || !strings.Contains(stdout, "already set up") {
			t.Errorf("exit = %d, stdout = %s", code, stdout)
		}
		sameFiles(t, before, snapshot(t, dir))
	})
	t.Run("finishes a half-done run", func(t *testing.T) {
		dir := railsApp(t, "demo")
		must(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(expected(t, "Dockerfile")), 0o644))

		code, stdout, stderr := initIn(t, dir, "")

		if code != 0 {
			t.Fatalf("exit = %d (stderr: %s)", code, stderr)
		}
		if strings.Contains(stdout, "Dockerfile") && !strings.Contains(stdout, "Dockerfile already") {
			t.Errorf("Dockerfile reported as changed:\n%s", stdout)
		}
		if read(t, filepath.Join(dir, "Dockerfile")) != expected(t, "Dockerfile") {
			t.Errorf("Dockerfile was modified again")
		}
		if read(t, filepath.Join(dir, "compose.yml")) != expected(t, "compose.yml") {
			t.Errorf("compose.yml wasn't written")
		}
	})
}

func TestInit_NeverOverwrites(t *testing.T) {
	terminal(t, false)
	t.Run(".env keeps values", func(t *testing.T) {
		dir := railsApp(t, "demo")
		must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("OTHER=keep-me\n"), 0o644))

		code, stdout, _ := initIn(t, dir, "")

		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		if got := read(t, filepath.Join(dir, ".env")); got != "OTHER=keep-me\nRAILS_MASTER_KEY=\n" {
			t.Errorf(".env = %q", got)
		}
		if !strings.Contains(stdout, "added RAILS_MASTER_KEY to .env") {
			t.Errorf("stdout = %s", stdout)
		}
	})
	t.Run("compose.yml without x-houston gets it appended", func(t *testing.T) {
		dir := railsApp(t, "demo")
		existing := "name: demo\n# my comment\nservices:\n  web:\n    build: .\n    ports: [\"3000:3000\"]\n"
		must(t, os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(existing), 0o644))

		code, stdout, stderr := initIn(t, dir, "")

		if code != 0 {
			t.Fatalf("exit = %d (stderr: %s)", code, stderr)
		}
		got := read(t, filepath.Join(dir, "compose.yml"))
		if !strings.HasPrefix(got, existing) || !strings.Contains(got[len(existing):], "x-houston:\n  health: /up\n") {
			t.Errorf("compose.yml =\n%s", got)
		}
		if !strings.Contains(stdout, "added x-houston to compose.yml") {
			t.Errorf("stdout = %s", stdout)
		}
		if _, err := project.Load(filepath.Join(dir, "compose.yml")); err != nil {
			t.Errorf("result doesn't load: %v", err)
		}
	})
	t.Run("invalid existing compose writes nothing", func(t *testing.T) {
		dir := railsApp(t, "demo")
		existing := "name: demo\nservices:\n  web:\n    build: .\n    ports: [\"3000:3000\"]\n    network_mode: host\n"
		must(t, os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(existing), 0o644))
		before := snapshot(t, dir)

		code, _, stderr := initIn(t, dir, "")

		if code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(stderr, "network_mode") {
			t.Errorf("stderr doesn't show the problem: %s", stderr)
		}
		sameFiles(t, before, snapshot(t, dir))
	})
}

func TestInit_Unsupported(t *testing.T) {
	terminal(t, false)
	cases := []struct {
		name  string
		setup func(dir string)
		msg   string
	}{
		{"not a Rails app", func(dir string) { os.Remove(filepath.Join(dir, "Gemfile")) }, "Rails apps for now"},
		{"Postgres", func(dir string) {
			f, _ := os.OpenFile(filepath.Join(dir, "Gemfile"), os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("gem \"pg\", \"~> 1.1\"\n")
			f.Close()
		}, "Postgres isn't supported"},
		{"no Dockerfile", func(dir string) { os.Remove(filepath.Join(dir, "Dockerfile")) }, "no Dockerfile"},
		{"final stage named app", func(dir string) {
			b, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
			s := strings.Replace(string(b), "# Final stage for app image\nFROM base\n", "# Final stage for app image\nFROM base AS app\n", 1)
			os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(s), 0o644)
		}, "production"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := railsApp(t, "demo")
			tc.setup(dir)
			before := snapshot(t, dir)

			code, _, stderr := initIn(t, dir, "")

			if code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
			if !strings.Contains(stderr, tc.msg) {
				t.Errorf("stderr = %q, want %q", stderr, tc.msg)
			}
			sameFiles(t, before, snapshot(t, dir))
		})
	}
}

func TestInit_Gitignore(t *testing.T) {
	terminal(t, false)
	t.Run("adds /.env", func(t *testing.T) {
		dir := railsApp(t, "demo")
		must(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/log/*\n"), 0o644))
		_, stdout, _ := initIn(t, dir, "")
		if got := read(t, filepath.Join(dir, ".gitignore")); got != "/log/*\n/.env\n" {
			t.Errorf(".gitignore = %q", got)
		}
		if !strings.Contains(stdout, "added /.env to .gitignore") {
			t.Errorf("stdout = %s", stdout)
		}
	})
	t.Run("creates .gitignore", func(t *testing.T) {
		dir := railsApp(t, "demo")
		must(t, os.Remove(filepath.Join(dir, ".gitignore")))
		initIn(t, dir, "")
		if got := read(t, filepath.Join(dir, ".gitignore")); got != "/.env\n" {
			t.Errorf(".gitignore = %q", got)
		}
	})
}

func TestInit_FileFlagAndUsage(t *testing.T) {
	terminal(t, false)
	t.Run("-f picks the project folder", func(t *testing.T) {
		root := t.TempDir()
		dir := railsApp(t, "demo")
		must(t, os.Rename(dir, filepath.Join(root, "sub")))
		t.Chdir(root)
		code, _, stderr := runWithInput(&fakeDocker{}, "", "-f", "sub/compose.yml", "init")
		if code != 0 {
			t.Fatalf("exit = %d (stderr: %s)", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(root, "sub", "compose.yml")); err != nil {
			t.Errorf("sub/compose.yml wasn't written: %v", err)
		}
	})
	t.Run("--server", func(t *testing.T) {
		if code, _, _ := run(&fakeDocker{}, "init", "--server"); code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
	})
}

func TestInit_ProductionPortFromExpose(t *testing.T) {
	terminal(t, false)
	t.Run("EXPOSE 80 in the final stage", func(t *testing.T) {
		dir := railsApp(t, "demo")
		initIn(t, dir, "")
		p, err := project.Load(filepath.Join(dir, "compose.yml"))
		if err != nil {
			t.Fatal(err)
		}
		if p.AppPort != 80 {
			t.Errorf("AppPort = %d, want 80 (Thruster in the production image)", p.AppPort)
		}
	})
	t.Run("no EXPOSE", func(t *testing.T) {
		dir := railsApp(t, "demo")
		src := read(t, filepath.Join(dir, "Dockerfile"))
		must(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(strings.Replace(src, "EXPOSE 80\n", "", 1)), 0o644))
		initIn(t, dir, "")
		compose := read(t, filepath.Join(dir, "compose.yml"))
		if strings.Contains(compose, "port:") {
			t.Errorf("compose.yml has a port line without EXPOSE:\n%s", compose)
		}
		p, err := project.Load(filepath.Join(dir, "compose.yml"))
		if err != nil {
			t.Fatal(err)
		}
		if p.AppPort != 3000 {
			t.Errorf("AppPort = %d, want 3000", p.AppPort)
		}
	})
}

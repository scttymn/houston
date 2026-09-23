package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/houston/internal/project"
)

// `houston init` is generic (docs/plans/init-generic.md): it upserts the
// Docker config Houston needs, with defaults that serve a static site, and
// knows no frameworks.

// app copies testdata/<fixture> into a fresh folder named dirName.
func app(t *testing.T, fixture, dirName string) string {
	t.Helper()
	src := filepath.Join("testdata", fixture)
	dir := filepath.Join(t.TempDir(), dirName)
	must(t, filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(dir, rel)
		if e.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
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
	return read(t, filepath.Join("testdata", "init-expected", name))
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	must(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
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

func mustSay(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(out, l) {
			t.Errorf("output doesn't say %q:\n%s", l, out)
		}
	}
}

// Row 1: a folder with only index.html gets the defaults, which serve it.
func TestInit_EmptyFolder(t *testing.T) {
	terminal(t, false)
	dir := app(t, "static", "demo")

	code, stdout, stderr := initIn(t, dir, "")

	if code != 0 {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr)
	}
	for _, f := range []string{"Dockerfile", "compose.yml"} {
		if got, want := read(t, filepath.Join(dir, f)), expected(t, f); got != want {
			t.Errorf("%s =\n%s\nwant\n%s", f, got, want)
		}
	}
	if got := read(t, filepath.Join(dir, ".gitignore")); got != "/.env\n" {
		t.Errorf(".gitignore = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		t.Errorf(".env written although the compose file references no variables")
	}
	if got := read(t, filepath.Join(dir, ".dockerignore")); got != expected(t, "dockerignore") {
		t.Errorf(".dockerignore = %q", got)
	}
	mustSay(t, stdout, "created Dockerfile", "created compose.yml", "created .dockerignore", "created .gitignore (/.env)",
		"Next: edit the defaults for your project, then houston dev")
	p, err := project.Load(filepath.Join(dir, "compose.yml"))
	if err != nil {
		t.Fatalf("the written compose.yml doesn't load:\n%v", err)
	}
	if p.AppPort != 8080 || p.Houston.Health != "/" {
		t.Errorf("AppPort %d, health %q; want 8080 and /", p.AppPort, p.Houston.Health)
	}
	if strings.Contains(read(t, filepath.Join(dir, "index.html")), "houston") {
		t.Errorf("index.html touched")
	}
}

// Row 2: an existing Dockerfile gets only the stages Houston needs.
func TestInit_UpsertsTheDockerfile(t *testing.T) {
	terminal(t, false)
	const note = "# Added by houston init: Houston builds dev (houston dev), test (houston test)\n" +
		"# and production (deploys). dev and test start as production: make them your\n" +
		"# project's own. Plain `docker build` now builds the last stage; pass\n" +
		"# --target production for the production image.\n"
	cases := []struct {
		name, src, want, says string
	}{
		{"unnamed final stage",
			"FROM node:22 AS build\nRUN make\n\nFROM nginx\nCOPY --from=build /out /usr/share/nginx/html\n",
			"FROM node:22 AS build\nRUN make\n\nFROM nginx AS production\nCOPY --from=build /out /usr/share/nginx/html\n\n" + note + "FROM production AS dev\n\nFROM production AS test\n",
			"Dockerfile: named the final stage production; added dev and test"},
		{"final stage named runner",
			"# keep me\nFROM golang:1.25 AS build\nRUN go build\nFROM --platform=linux/amd64 gcr.io/distroless/base AS runner\nCOPY --from=build /app /app\n",
			"# keep me\nFROM golang:1.25 AS build\nRUN go build\nFROM --platform=linux/amd64 gcr.io/distroless/base AS runner\nCOPY --from=build /app /app\n\n" + note + "FROM runner AS production\n\nFROM production AS dev\n\nFROM production AS test\n",
			"Dockerfile: added production, dev and test"},
		{"only test missing",
			"FROM alpine AS base\nFROM base AS dev\nFROM base AS production\n",
			"FROM alpine AS base\nFROM base AS dev\nFROM base AS production\n\n" + note + "FROM production AS test\n",
			"Dockerfile: added test"},
		{"no newline at the end",
			"FROM alpine",
			"FROM alpine AS production\n\n" + note + "FROM production AS dev\n\nFROM production AS test\n",
			"Dockerfile: named the final stage production; added dev and test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := app(t, "static", "demo")
			write(t, dir, "Dockerfile", tc.src)
			code, stdout, stderr := initIn(t, dir, "")
			if code != 0 {
				t.Fatalf("exit = %d (stderr: %s)", code, stderr)
			}
			if got := read(t, filepath.Join(dir, "Dockerfile")); got != tc.want {
				t.Errorf("Dockerfile =\n%s\nwant\n%s", got, tc.want)
			}
			mustSay(t, stdout, tc.says)
		})
	}
	t.Run("all four stages: untouched", func(t *testing.T) {
		dir := app(t, "static", "demo")
		src := "FROM alpine AS base\n\nfrom base as Dev\nFROM base AS TEST\nFROM base AS production\n"
		write(t, dir, "Dockerfile", src)
		_, stdout, _ := initIn(t, dir, "")
		if read(t, filepath.Join(dir, "Dockerfile")) != src || strings.Contains(stdout, "Dockerfile") {
			t.Errorf("Dockerfile changed or reported:\n%s", stdout)
		}
	})
	t.Run("no FROM: refused, nothing written", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "Dockerfile", "# just a comment\nRUN echo\n")
		before := snapshot(t, dir)
		code, _, stderr := initIn(t, dir, "")
		if code != 1 || !strings.Contains(stderr, "the Dockerfile has no FROM line") {
			t.Errorf("exit %d, stderr %s", code, stderr)
		}
		sameFiles(t, before, snapshot(t, dir))
	})
}

// Row 3: an existing compose.yml gets only the x-houston keys it lacks.
func TestInit_UpsertsXHouston(t *testing.T) {
	terminal(t, false)
	app1 := "name: demo\n# my comment\nservices:\n  web:\n    build: .\n    ports: [\"3000:3000\"]\n"
	t.Run("no x-houston: the default block appended", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "compose.yml", app1)
		code, stdout, stderr := initIn(t, dir, "")
		if code != 0 {
			t.Fatalf("exit = %d (stderr: %s)", code, stderr)
		}
		got := read(t, filepath.Join(dir, "compose.yml"))
		block := expected(t, "compose.yml")[strings.Index(expected(t, "compose.yml"), "x-houston:"):]
		if got != app1+"\n"+block {
			t.Errorf("compose.yml =\n%s", got)
		}
		mustSay(t, stdout, "added x-houston to compose.yml")
	})
	t.Run("x-houston without health: one line added inside it", func(t *testing.T) {
		dir := app(t, "static", "demo")
		src := app1 + "x-houston:\n    # our deploy rule\n    deploy: { on: tag }\n    commands: { test: make test }\nvolumes: {}\n"
		write(t, dir, "compose.yml", src)
		code, stdout, stderr := initIn(t, dir, "")
		if code != 0 {
			t.Fatalf("exit = %d (stderr: %s)", code, stderr)
		}
		want := strings.Replace(src, "x-houston:\n", "x-houston:\n    health: /\n", 1)
		if got := read(t, filepath.Join(dir, "compose.yml")); got != want {
			t.Errorf("compose.yml =\n%s\nwant\n%s", got, want)
		}
		mustSay(t, stdout, "added health to x-houston in compose.yml")
	})
	t.Run("an empty x-houston", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "compose.yml", app1+"x-houston:\n")
		initIn(t, dir, "")
		if got := read(t, filepath.Join(dir, "compose.yml")); got != app1+"x-houston:\n  health: /\n" {
			t.Errorf("compose.yml =\n%s", got)
		}
	})
	for _, tc := range []struct{ name, src, want string }{
		{"a comment after the key", app1 + "x-houston: # ours\n  domains: [a.com]\n", app1 + "x-houston: # ours\n  health: /\n  domains: [a.com]\n"},
		{"an anchor", app1 + "x-houston: &xh\n  domains: [a.com]\n", app1 + "x-houston: &xh\n  health: /\n  domains: [a.com]\n"},
		{"a quoted key", app1 + "\"x-houston\":\n  domains: [a.com]\n", app1 + "\"x-houston\":\n  health: /\n  domains: [a.com]\n"},
		{"no final newline", app1 + "x-houston:\n  domains: [a.com]", app1 + "x-houston:\n  health: /\n  domains: [a.com]"},
		{"CRLF", strings.ReplaceAll(app1+"x-houston:\n  domains: [a.com]\n", "\n", "\r\n"), strings.ReplaceAll(app1+"x-houston:\n  health: /\n  domains: [a.com]\n", "\n", "\r\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := app(t, "static", "demo")
			write(t, dir, "compose.yml", tc.src)
			if code, _, stderr := initIn(t, dir, ""); code != 0 {
				t.Fatalf("exit = %d (stderr: %s)", code, stderr)
			}
			if got := read(t, filepath.Join(dir, "compose.yml")); got != tc.want {
				t.Errorf("compose.yml = %q\nwant %q", got, tc.want)
			}
		})
	}
	for _, null := range []string{"~", "null"} {
		t.Run("x-houston: "+null+" is refused", func(t *testing.T) {
			dir := app(t, "static", "demo")
			write(t, dir, "compose.yml", app1+"x-houston: "+null+"\n")
			before := snapshot(t, dir)
			code, _, stderr := initIn(t, dir, "")
			if code != 1 || !strings.Contains(stderr, "x-houston must be a mapping") {
				t.Errorf("exit %d, stderr %s", code, stderr)
			}
			sameFiles(t, before, snapshot(t, dir))
		})
	}
	t.Run("a changed health is kept", func(t *testing.T) {
		dir := app(t, "static", "demo")
		src := app1 + "x-houston:\n  health: /healthz\n"
		write(t, dir, "compose.yml", src)
		_, stdout, _ := initIn(t, dir, "")
		if got := read(t, filepath.Join(dir, "compose.yml")); got != src || strings.Contains(stdout, "compose.yml") {
			t.Errorf("compose.yml changed:\n%s\n%s", got, stdout)
		}
	})
}

// Row 4: app_port only when the app has no single `ports` entry.
func TestInit_AddsTheAppPortOnlyWhenNeeded(t *testing.T) {
	terminal(t, false)
	t.Run("no x-houston and no ports: the block has app_port", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "compose.yml", "name: demo\nservices:\n  web:\n    build: .\n")
		if code, _, stderr := initIn(t, dir, ""); code != 0 {
			t.Fatalf("exit = %d (stderr: %s)", code, stderr)
		}
		got := read(t, filepath.Join(dir, "compose.yml"))
		if !strings.Contains(got, "x-houston:\n  health: /\n  app_port: 8080                # the port your app listens on; change it\n") || strings.Contains(got, "# app_port") {
			t.Errorf("compose.yml =\n%s", got)
		}
	})
	for _, tc := range []struct {
		name, ports string
		wantLine    bool
	}{
		{"no ports", "", true},
		{"two ports", "    ports: [\"3000:3000\", \"3035:3035\"]\n", true},
		{"one port", "    ports: [\"3000:3000\"]\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := app(t, "static", "demo")
			write(t, dir, "compose.yml", "name: demo\nservices:\n  web:\n    build: .\n"+tc.ports+"x-houston:\n  health: /up\n")
			code, _, stderr := initIn(t, dir, "")
			if code != 0 {
				t.Fatalf("exit = %d (stderr: %s)", code, stderr)
			}
			got := read(t, filepath.Join(dir, "compose.yml"))
			line := "  app_port: 8080                # the port your app listens on; change it\n"
			if strings.Contains(got, line) != tc.wantLine {
				t.Errorf("app_port line present = %v, want %v:\n%s", !tc.wantLine, tc.wantLine, got)
			}
			if _, err := project.Load(filepath.Join(dir, "compose.yml")); err != nil {
				t.Errorf("doesn't load: %v", err)
			}
		})
	}
}

// Row 5: what init can't fix without guessing is named, and nothing written.
func TestInit_RefusesWhatItCantFix(t *testing.T) {
	terminal(t, false)
	for _, tc := range []struct{ name, compose, says string }{
		{"no built service", "name: demo\nservices:\n  web:\n    image: nginx\n", "no service has `build:`"},
		{"two built services", "name: demo\nservices:\n  web:\n    build: .\n  worker:\n    build: .\n", "only one built service"},
		{"network_mode", "name: demo\nservices:\n  web:\n    build: .\n    ports: [\"80:80\"]\n    network_mode: host\n", "network_mode"},
		{"env_file", "name: demo\nservices:\n  web:\n    build: .\n    ports: [\"80:80\"]\n    env_file: .env\n", "env_file"},
		{"flow-style x-houston without health", "name: demo\nservices:\n  web:\n    build: .\n    ports: [\"80:80\"]\nx-houston: { domains: [a.com] }\n",
			"x-houston is written on one line; add health: / to it"},
		{"the old port key", "name: demo\nservices:\n  web:\n    build: .\nx-houston:\n  health: /\n  port: 80\n", "`port` is now `app_port`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := app(t, "static", "demo")
			write(t, dir, "compose.yml", tc.compose)
			before := snapshot(t, dir)
			code, _, stderr := initIn(t, dir, "")
			if code != 1 || !strings.Contains(stderr, tc.says) {
				t.Errorf("exit %d, stderr %s; want 1 and %q", code, stderr, tc.says)
			}
			sameFiles(t, before, snapshot(t, dir))
		})
	}
}

// Row 6: a rerun changes nothing; a run cut short is finished.
func TestInit_IdempotentAndResumes(t *testing.T) {
	terminal(t, false)
	t.Run("second run changes nothing", func(t *testing.T) {
		dir := app(t, "static", "demo")
		initIn(t, dir, "")
		before := snapshot(t, dir)
		code, stdout, _ := initIn(t, dir, "")
		if code != 0 || !strings.Contains(stdout, "already set up") {
			t.Errorf("exit = %d, stdout = %s", code, stdout)
		}
		sameFiles(t, before, snapshot(t, dir))
	})
	t.Run("finishes a run cut short", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "Dockerfile", expected(t, "Dockerfile"))
		code, stdout, stderr := initIn(t, dir, "")
		if code != 0 {
			t.Fatalf("exit = %d (stderr: %s)", code, stderr)
		}
		if strings.Contains(stdout, "Dockerfile") {
			t.Errorf("Dockerfile reported as changed:\n%s", stdout)
		}
		if read(t, filepath.Join(dir, "compose.yml")) != expected(t, "compose.yml") {
			t.Errorf("compose.yml wasn't written")
		}
	})
}

// Row 7: .env gets the referenced variables that aren't there; .gitignore
// ignores .env.
func TestInit_EnvAndGitignore(t *testing.T) {
	terminal(t, false)
	compose := "name: demo\nservices:\n  web:\n    build: .\n    ports: [\"80:80\"]\n    environment:\n      SECRET_KEY_BASE: ${SECRET_KEY_BASE}\n      DB_URL: postgres://${DB_HOST:-db}/demo\n      TOKEN: ${TOKEN}\n  db:\n    image: postgres:17\nx-houston:\n  health: /\n"
	t.Run(".env", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "compose.yml", compose)
		write(t, dir, ".env", "TOKEN=keep-me\n")
		_, stdout, _ := initIn(t, dir, "")
		if got := read(t, filepath.Join(dir, ".env")); got != "TOKEN=keep-me\nSECRET_KEY_BASE=\n" {
			t.Errorf(".env = %q", got)
		}
		mustSay(t, stdout, "added SECRET_KEY_BASE to .env")
	})
	for name, tc := range map[string]struct{ before, want string }{
		"appended":         {"/log/*\n", "/log/*\n/.env\n"},
		"already ignored":  {"/.env*\n", "/.env*\n"},
		"no final newline": {"node_modules", "node_modules\n/.env\n"},
	} {
		t.Run(".gitignore "+name, func(t *testing.T) {
			dir := app(t, "static", "demo")
			write(t, dir, ".gitignore", tc.before)
			initIn(t, dir, "")
			if got := read(t, filepath.Join(dir, ".gitignore")); got != tc.want {
				t.Errorf(".gitignore = %q, want %q", got, tc.want)
			}
		})
	}
}

// Row 8: a Rails app is just a folder with a Dockerfile: the generic upsert,
// nothing Rails-specific.
func TestInit_RailsGetsTheGenericSetup(t *testing.T) {
	terminal(t, false)
	dir := app(t, "rails", "demo")
	code, _, stderr := initIn(t, dir, "")
	if code != 0 {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr)
	}
	if got, want := read(t, filepath.Join(dir, "Dockerfile")), expected(t, "rails.Dockerfile"); got != want {
		t.Errorf("Dockerfile =\n%s\nwant\n%s", got, want)
	}
	if got, want := read(t, filepath.Join(dir, "compose.yml")), expected(t, "compose.yml"); got != want {
		t.Errorf("compose.yml isn't the generic default:\n%s", got)
	}
	for _, f := range []string{"Dockerfile", "compose.yml"} {
		body := read(t, filepath.Join(dir, f))
		added := body
		if f == "Dockerfile" {
			added = body[strings.Index(body, "# Added by houston init"):]
		}
		for _, word := range []string{"rails", "Rails", "RAILS", "bundle", "/up"} {
			if strings.Contains(added, word) {
				t.Errorf("init wrote %q into %s", word, f)
			}
		}
	}
}

// Row 9: the help says what init does now.
func TestInit_Help(t *testing.T) {
	_, stdout, _ := run(&fakeDocker{}, "--help")
	mustSay(t, stdout, "Set up this folder for Houston")
	if strings.Contains(stdout, "Rails") {
		t.Errorf("help still names Rails:\n%s", stdout)
	}
}

// Row 10: -f picks the folder; the name prompt; usage.
func TestInit_FileFlagNameAndUsage(t *testing.T) {
	t.Run("-f picks the folder", func(t *testing.T) {
		terminal(t, false)
		root := t.TempDir()
		must(t, os.Rename(app(t, "static", "demo"), filepath.Join(root, "sub")))
		t.Chdir(root)
		if code, _, stderr := runWithInput(&fakeDocker{}, "", "-f", "sub/compose.yml", "init"); code != 0 {
			t.Fatalf("exit = %d (stderr: %s)", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(root, "sub", "compose.yml")); err != nil {
			t.Errorf("sub/compose.yml wasn't written: %v", err)
		}
	})
	for _, tc := range []struct {
		name, dir, input string
		terminal         bool
		want             string // "" → usage error, nothing written
	}{
		{"folder name", "My_Site", "", false, "my-site"},
		{"typed name", "demo", "site2\n", true, "site2"},
		{"empty takes the default", "demo", "\n", true, "demo"},
		{"invalid typed name", "demo", "Bad_Name\n", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal(t, tc.terminal)
			dir := app(t, "static", tc.dir)
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
	t.Run("--server", func(t *testing.T) {
		if code, _, _ := run(&fakeDocker{}, "init", "--server"); code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
	})
}

// Review R1: the Dockerfile init completes is the one the compose file builds.
func TestInit_CompletesTheDockerfileComposeBuilds(t *testing.T) {
	terminal(t, false)
	stageless := "FROM alpine\nCMD [\"true\"]\n"
	t.Run("build: ./web", func(t *testing.T) {
		dir := app(t, "static", "demo")
		must(t, os.MkdirAll(filepath.Join(dir, "web"), 0o755))
		write(t, dir, "web/Dockerfile", stageless)
		write(t, dir, "compose.yml", "name: demo\nservices:\n  app:\n    build: ./web\n    ports: [\"80:80\"]\nx-houston:\n  health: /\n")
		if code, stdout, stderr := initIn(t, dir, ""); code != 0 || !strings.Contains(stdout, "web/Dockerfile: named the final stage production") {
			t.Fatalf("exit = %d (stdout %s, stderr: %s)", code, stdout, stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
			t.Errorf("a stray Dockerfile was written next to compose.yml")
		}
		if !strings.Contains(read(t, filepath.Join(dir, "web", "Dockerfile")), "FROM production AS dev") {
			t.Errorf("web/Dockerfile wasn't completed")
		}
	})
	t.Run("dockerfile: Dockerfile.prod", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "Dockerfile.prod", stageless)
		write(t, dir, "compose.yml", "name: demo\nservices:\n  app:\n    build: { context: ., dockerfile: Dockerfile.prod }\n    ports: [\"80:80\"]\nx-houston:\n  health: /\n")
		if code, _, stderr := initIn(t, dir, ""); code != 0 {
			t.Fatalf("exit = %d (stderr: %s)", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
			t.Errorf("a stray Dockerfile was written")
		}
		if !strings.Contains(read(t, filepath.Join(dir, "Dockerfile.prod")), "FROM production AS test") {
			t.Errorf("Dockerfile.prod wasn't completed")
		}
	})
	for _, tc := range []struct{ name, build, says string }{
		{"a variable", "${CTX:-.}", "builds from ${CTX:-.}, which Houston can't resolve here"},
		{"a bare GitHub remote", "github.com/example/app.git", "builds from github.com/example/app.git; houston init only completes a Dockerfile in this folder"},
		{"outside the folder", "..", "builds from .., outside this folder; houston init only completes a Dockerfile in this folder"},
		{"a variable Dockerfile", "{ context: ., dockerfile: \"${DF:-Dockerfile}\" }", "can't resolve here"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			dir := app(t, "static", "demo")
			write(t, dir, "compose.yml", "name: demo\nservices:\n  app:\n    build: "+tc.build+"\n    ports: [\"80:80\"]\nx-houston:\n  health: /\n")
			before := snapshot(t, dir)
			code, _, stderr := initIn(t, dir, "")
			if code != 1 || !strings.Contains(stderr, tc.says) {
				t.Errorf("exit %d, stderr %s; want %q", code, stderr, tc.says)
			}
			sameFiles(t, before, snapshot(t, dir))
		})
	}
	t.Run("a remote context is refused", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "compose.yml", "name: demo\nservices:\n  app:\n    build: https://github.com/example/app.git\n    ports: [\"80:80\"]\nx-houston:\n  health: /\n")
		before := snapshot(t, dir)
		code, _, stderr := initIn(t, dir, "")
		if code != 1 || !strings.Contains(stderr, "builds from https://github.com/example/app.git") {
			t.Errorf("exit %d, stderr %s", code, stderr)
		}
		sameFiles(t, before, snapshot(t, dir))
	})
}

// Review R2: instructions are read as Docker reads them.
func TestInit_ReadsInstructionsAsDockerDoes(t *testing.T) {
	terminal(t, false)
	const note = "# Added by houston init: Houston builds dev (houston dev), test (houston test)\n" +
		"# and production (deploys). dev and test start as production: make them your\n" +
		"# project's own. Plain `docker build` now builds the last stage; pass\n" +
		"# --target production for the production image.\n"
	const devTest = "FROM production AS dev\n\nFROM production AS test\n"
	for _, tc := range []struct{ name, src, want string }{
		{"FROM continued, unnamed",
			"FROM alpine AS base\nFROM --platform=$TARGETPLATFORM \\\n    alpine:3.20\nCMD [\"true\"]\n",
			"FROM alpine AS base\nFROM --platform=$TARGETPLATFORM \\\n    alpine:3.20 AS production\nCMD [\"true\"]\n\n" + note + devTest},
		{"FROM continued, named on the next line",
			"FROM alpine AS base\nFROM base \\\n  AS production\nCMD [\"keep\"]\n",
			"FROM alpine AS base\nFROM base \\\n  AS production\nCMD [\"keep\"]\n\n" + note + devTest},
		{"escape directive",
			"# escape=`\nFROM mcr.microsoft.com/windows/servercore `\n  AS runner\n",
			"# escape=`\nFROM mcr.microsoft.com/windows/servercore `\n  AS runner\n\n" +
				"# Added by houston init: Houston builds dev (houston dev), test (houston test)\n# and production (deploys). dev and test start as production: make them your\n# project's own. Plain `docker build` now builds the last stage; pass\n# --target production for the production image.\nFROM runner AS production\n\n" + devTest},
		{"a heredoc containing FROM",
			"FROM alpine\nCOPY <<EOF /inner/Dockerfile\nFROM scratch\nEOF\n",
			"FROM alpine AS production\nCOPY <<EOF /inner/Dockerfile\nFROM scratch\nEOF\n\n" + note + devTest},
		{"a BOM", "\ufeffFROM alpine\n", "\ufeffFROM alpine AS production\n\n" + note + devTest},
		{"a comment inside a continued FROM",
			"FROM alpine AS base\nFROM base \\\n# the name is on the next line\n  AS production\nCMD [\"keep\"]\n",
			"FROM alpine AS base\nFROM base \\\n# the name is on the next line\n  AS production\nCMD [\"keep\"]\n\n" + note + devTest},
		{"a blank line inside a continued FROM",
			"FROM alpine \\\n\n  AS runner\n",
			"FROM alpine \\\n\n  AS runner\n\n" + note + "FROM runner AS production\n\n" + devTest},
		{"mixed line endings: only what init writes changes",
			"FROM alpine\r\nCMD [\"true\"]\n",
			"FROM alpine AS production\r\nCMD [\"true\"]\n\r\n" + strings.ReplaceAll(note+devTest, "\n", "\r\n")},
		{"CRLF", "FROM alpine\r\nCMD [\"true\"]\r\n",
			"FROM alpine AS production\r\nCMD [\"true\"]\r\n\r\n" + strings.ReplaceAll(note+devTest, "\n", "\r\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := app(t, "static", "demo")
			write(t, dir, "Dockerfile", tc.src)
			if code, _, stderr := initIn(t, dir, ""); code != 0 {
				t.Fatalf("exit = %d (stderr: %s)", code, stderr)
			}
			if got := read(t, filepath.Join(dir, "Dockerfile")); got != tc.want {
				t.Errorf("Dockerfile = %q\nwant      %q", got, tc.want)
			}
		})
	}
}

// A Dockerfile ending inside an unterminated heredoc: stages appended after
// it would be heredoc body, not stages. The re-read catches it: refused.
func TestInit_RefusesADockerfileItCantComplete(t *testing.T) {
	terminal(t, false)
	dir := app(t, "static", "demo")
	write(t, dir, "Dockerfile", "FROM alpine\nRUN <<EOF\necho unterminated\n")
	before := snapshot(t, dir)
	code, _, stderr := initIn(t, dir, "")
	if code != 1 || !strings.Contains(stderr, "couldn't complete this Dockerfile's stages") {
		t.Errorf("exit %d, stderr %s", code, stderr)
	}
	sameFiles(t, before, snapshot(t, dir))
}

// Review R3: an existing compose file under another name isn't shadowed.
func TestInit_DoesntShadowAnotherComposeFile(t *testing.T) {
	terminal(t, false)
	for _, other := range []string{"docker-compose.yml", "compose.yaml", "docker-compose.yaml"} {
		t.Run(other, func(t *testing.T) {
			dir := app(t, "static", "demo")
			write(t, dir, other, "name: demo\nservices:\n  app:\n    build: .\n    ports: [\"80:80\"]\n")
			before := snapshot(t, dir)
			code, _, stderr := initIn(t, dir, "")
			if code != 1 || !strings.Contains(stderr, "this folder has "+other+"; Houston reads compose.yml") {
				t.Errorf("exit %d, stderr %s", code, stderr)
			}
			sameFiles(t, before, snapshot(t, dir))
		})
	}
	t.Run("-f docker-compose.yml upserts it", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "docker-compose.yml", "name: demo\nservices:\n  app:\n    build: .\n    ports: [\"80:80\"]\n")
		code, _, stderr := runWithInput(&fakeDocker{}, "", "-f", filepath.Join(dir, "docker-compose.yml"), "init")
		if code != 0 {
			t.Fatalf("exit = %d (stderr: %s)", code, stderr)
		}
		if !strings.Contains(read(t, filepath.Join(dir, "docker-compose.yml")), "x-houston:") {
			t.Errorf("docker-compose.yml wasn't completed")
		}
		if _, err := os.Stat(filepath.Join(dir, "compose.yml")); err == nil {
			t.Errorf("compose.yml written")
		}
	})
}

// Review R4: .dockerignore keeps .git, .env and .houston out of the image.
func TestInit_Dockerignore(t *testing.T) {
	terminal(t, false)
	// Written fresh, it leaves out the build files init actually used, by
	// their names in the build context.
	t.Run("the build files by their own names", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "docker-compose.yml", "name: demo\nservices:\n  app:\n    build: { context: ., dockerfile: Dockerfile.site }\n    ports: [\"8080:8080\"]\nx-houston:\n  health: /\n")
		if code, _, stderr := runWithInput(&fakeDocker{}, "", "-f", filepath.Join(dir, "docker-compose.yml"), "init"); code != 0 {
			t.Fatalf("exit %d, stderr %s", code, stderr)
		}
		got := read(t, filepath.Join(dir, ".dockerignore"))
		for _, want := range []string{"\ndocker-compose.yml\n", "\nDockerfile.site\n"} {
			if !strings.Contains(got, want) {
				t.Errorf(".dockerignore lacks %q:\n%s", want, got)
			}
		}
		for _, not := range []string{"\ncompose.yml\n", "\nDockerfile\n"} {
			if strings.Contains(got, not) {
				t.Errorf(".dockerignore names %q, which this project doesn't use:\n%s", not, got)
			}
		}
	})
	t.Run("a Dockerfile's own ignore file wins", func(t *testing.T) {
		dir := app(t, "static", "demo")
		write(t, dir, "Dockerfile.dockerignore", "tmp\n")
		if code, stdout, stderr := initIn(t, dir, ""); code != 0 || !strings.Contains(stdout, "added .git, .env and .houston to Dockerfile.dockerignore") {
			t.Fatalf("exit %d, stdout %s, stderr %s", code, stdout, stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, ".dockerignore")); err == nil {
			t.Errorf(".dockerignore written, which BuildKit won't read beside Dockerfile.dockerignore")
		}
	})
	for name, tc := range map[string]struct{ before, want, says string }{
		"only what's missing": {"/.git/\n.env*\nnode_modules\n", "/.git/\n.env*\nnode_modules\n.houston\n", "added .houston to .dockerignore"},
		"all there":           {".git\n/.env\n.houston/\n", ".git\n/.env\n.houston/\n", ""},
		"no final newline":    {"tmp", "tmp\n.git\n.env\n.houston\n", "added .git, .env and .houston to .dockerignore"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := app(t, "static", "demo")
			write(t, dir, ".dockerignore", tc.before)
			_, stdout, _ := initIn(t, dir, "")
			if got := read(t, filepath.Join(dir, ".dockerignore")); got != tc.want {
				t.Errorf(".dockerignore = %q, want %q", got, tc.want)
			}
			if tc.says != "" {
				mustSay(t, stdout, tc.says)
			} else if strings.Contains(stdout, ".dockerignore") {
				t.Errorf(".dockerignore reported: %s", stdout)
			}
		})
	}
}

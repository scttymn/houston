package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDevProduction_RunsCompose(t *testing.T) {
	t.Run("argv, override and exit code", func(t *testing.T) {
		dir, path := newProject(t, phoenix, map[string]string{".env": "POSTGRES_PASSWORD=x\nSECRET_KEY_BASE=y\n"})
		var atRun string
		d := &fakeDocker{runExit: 5, onRun: func() {
			b, _ := os.ReadFile(filepath.Join(dir, ".houston", "compose.production.yml"))
			atRun = string(b)
		}}

		code, _, stderr := run(d, "-f", path, "dev", "--production")

		if code != 5 {
			t.Errorf("exit = %d, want compose's 5 (stderr: %s)", code, stderr)
		}
		// Its own project, so its own volumes: dev's were made by the dev
		// stage (often root), and a production image running as a user
		// can't write them. A fresh volume is seeded from the image, with
		// the image's ownership, as on the server.
		want := []string{"compose", "-p", "phoenixapp-production", "--project-directory", dir, "-f", path,
			"-f", filepath.Join(dir, ".houston", "compose.production.yml"), "up", "--build"}
		if len(d.runs) != 1 || !reflect.DeepEqual(d.runs[0], want) {
			t.Errorf("runs = %q\nwant %q", d.runs, want)
		}
		if !strings.Contains(atRun, "target: production") {
			t.Errorf("override when compose ran =\n%s", atRun)
		}
		if withoutUpLine(stderr) != "" {
			t.Errorf("unexpected stderr: %s", stderr)
		}
	})
	t.Run("same warnings as dev", func(t *testing.T) {
		_, path := newProject(t, phoenix, nil)
		code, _, stderr := run(&fakeDocker{}, "-f", path, "dev", "--production")
		if code != 0 || !strings.Contains(stderr, "POSTGRES_PASSWORD") {
			t.Errorf("exit = %d, stderr = %s", code, stderr)
		}
	})
	// Generic (docs/plans/init-generic.md): an optional variable left blank
	// may be what the production image needs (a key dev falls back without).
	t.Run("blank optional variables note", func(t *testing.T) {
		app := "name: demo\nservices:\n  app:\n    build: .\n    ports: [\"3000:3000\"]\n    environment:\n      APP_KEY: ${APP_KEY:-}\n      MODE: ${MODE:-dev}\n      TOKEN: ${TOKEN}\nx-houston:\n  health: /up\n  app_port: 80\n"
		_, path := newProject(t, app, map[string]string{".env": "APP_KEY=\nTOKEN=t\n"})
		_, _, stderr := run(&fakeDocker{}, "-f", path, "dev", "--production")
		if !strings.Contains(stderr, "note: APP_KEY is blank in .env") || strings.Contains(stderr, "MODE") {
			t.Errorf("stderr = %s", stderr)
		}
		_, path = newProject(t, app, map[string]string{".env": "APP_KEY=abc\nTOKEN=t\n"})
		if _, _, stderr := run(&fakeDocker{}, "-f", path, "dev", "--production"); withoutUpLine(stderr) != "" {
			t.Errorf("note shown although it's set: %s", stderr)
		}
		_, path = newProject(t, app, map[string]string{".env": "APP_KEY=\nTOKEN=t\n"})
		if _, _, stderr := run(&fakeDocker{}, "-f", path, "dev"); withoutUpLine(stderr) != "" {
			t.Errorf("plain dev shows a production note: %s", stderr)
		}
		two := strings.Replace(app, "      MODE: ${MODE:-dev}\n", "      OTHER: ${OTHER:-}\n", 1)
		_, path = newProject(t, two, map[string]string{".env": "TOKEN=t\n"})
		if _, _, stderr := run(&fakeDocker{}, "-f", path, "dev", "--production"); !strings.Contains(stderr, "note: APP_KEY, OTHER are blank in .env; the production image may need them") {
			t.Errorf("plural: %s", stderr)
		}
		t.Setenv("OTHER", "from-the-shell")
		if _, _, stderr := run(&fakeDocker{}, "-f", path, "dev", "--production"); !strings.Contains(stderr, "note: APP_KEY is blank") || strings.Contains(stderr, "OTHER") {
			t.Errorf("a variable set in the shell is named: %s", stderr)
		}
	})
}

// Guard, not TDD: these already fail as unknown flags before --production
// exists; they keep it from leaking onto other commands.
func TestCLI_ProductionOnlyOnDev(t *testing.T) {
	_, path := newProject(t, phoenixWithConsole, nil)
	for _, cmd := range []string{"test", "console", "logs", "init"} {
		d := &fakeDocker{psOut: "abc\n"}
		if code, _, _ := run(d, "-f", path, cmd, "--production"); code != 2 || d.calls() != 0 {
			t.Errorf("%s --production: exit = %d, calls = %d; want 2 and none", cmd, code, d.calls())
		}
	}
}

func TestCLI_VersionAndNoCompletion(t *testing.T) {
	code, stdout, _ := run(&fakeDocker{}, "--version")
	if code != 0 || !strings.Contains(stdout, "houston version dev") {
		t.Errorf("unstamped: exit = %d, stdout = %q", code, stdout)
	}
	was := version
	version = "v1.2.3"
	t.Cleanup(func() { version = was })
	if _, stdout, _ := run(&fakeDocker{}, "--version"); !strings.Contains(stdout, "houston version v1.2.3") {
		t.Errorf("stamped: stdout = %q", stdout)
	}
	if code, _, _ := run(&fakeDocker{}, "completion", "bash"); code != 2 {
		t.Errorf("completion: exit = %d, want 2 (removed)", code)
	}
}

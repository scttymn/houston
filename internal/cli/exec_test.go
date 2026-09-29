package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// houston exec (docs/plans/exec-and-init-name.md): a command in this
// checkout's dev instance, running or not.

const phoenixEnv = "POSTGRES_PASSWORD=x\nSECRET_KEY_BASE=y\n"

// execDocker answers exec's lookups: app is the running app container's id
// ("" when it isn't running), and others the project's other running
// containers (a database left up).
func execDocker(app, others string) *fakeDocker {
	return &fakeDocker{outputFn: func(args []string) ([]byte, error, bool) {
		switch {
		case args[0] == "ps" && slices.Contains(args, "label=com.docker.compose.service=app"):
			return []byte(app), nil, true
		case args[0] == "ps":
			return []byte(strings.TrimSpace(app + "\n" + others)), nil, true
		case args[0] == "compose" && args[len(args)-1] == "stop":
			return nil, nil, true
		}
		return nil, nil, false
	}}
}

// appLookup is the running app query for project.
func appLookup(project string) []string {
	return []string{"ps", "-q",
		"--filter", "label=com.docker.compose.project=" + project,
		"--filter", "label=com.docker.compose.service=app",
		"--filter", "label=com.docker.compose.oneoff=False"}
}

// oneOff is the compose run exec makes when the app isn't running.
func oneOff(dir, path, project string, tty bool, command ...string) []string {
	args := []string{"compose", "-p", project, "--project-directory", dir, "-f", path, "-f", filepath.Join(dir, ".houston", "compose.exec.yml"), "run", "--rm"}
	if !tty {
		args = append(args, "-T")
	}
	return append(append(args, "app"), command...)
}

// stopped reports whether exec stopped the project's services afterwards.
func stopped(d *fakeDocker) bool {
	for _, o := range d.outputs {
		if o[0] == "compose" && o[len(o)-1] == "stop" {
			return true
		}
	}
	return false
}

// Row 1.
func TestExec_RunsInRunningApp(t *testing.T) {
	terminal(t, true)
	_, path := newProject(t, phoenix, nil)
	d := execDocker("abc123\n", "")
	d.runExit = 4

	code, _, stderr := run(d, "-f", path, "exec", "bin/myapp", "db", "migrate")

	if code != 4 {
		t.Errorf("exit = %d, want the command's 4 (stderr: %s)", code, stderr)
	}
	if !d.called(appLookup("phoenixapp")...) {
		t.Errorf("lookups = %q", d.outputs)
	}
	want := []string{"exec", "-i", "-t", "abc123", "bin/myapp", "db", "migrate"}
	if len(d.runs) != 1 || !reflect.DeepEqual(d.runs[0], want) {
		t.Errorf("runs = %q, want %q", d.runs, want)
	}
}

func TestExec_PipedInputSkipsTTY(t *testing.T) {
	terminal(t, false)
	_, path := newProject(t, phoenix, nil)
	d := execDocker("abc123\n", "")

	run(d, "-f", path, "exec", "cat", "/stage")

	want := []string{"exec", "-i", "abc123", "cat", "/stage"}
	if len(d.runs) != 1 || !reflect.DeepEqual(d.runs[0], want) {
		t.Errorf("runs = %q, want %q", d.runs, want)
	}
}

// Row 2.
func TestExec_PassesFlagsThrough(t *testing.T) {
	terminal(t, false)
	_, path := newProject(t, phoenix, nil)
	d := execDocker("abc123\n", "")

	code, _, stderr := run(d, "-f", path, "exec", "ls", "-la", "--color=never", "-f", "x")

	want := []string{"exec", "-i", "abc123", "ls", "-la", "--color=never", "-f", "x"}
	if code != 0 || len(d.runs) != 1 || !reflect.DeepEqual(d.runs[0], want) {
		t.Errorf("exit %d, runs = %q, want %q (stderr: %s)", code, d.runs, want, stderr)
	}
}

// Row 3.
func TestExec_BranchInstance(t *testing.T) {
	terminal(t, false)
	_, path := onBranch(t, "feature1")
	d := execDocker("def456\n", "")

	run(d, "-f", path, "exec", "true")

	if !d.called(appLookup("shop-feature1")...) || d.called(appLookup("shop")...) {
		t.Errorf("lookups = %q, want the branch's project", d.outputs)
	}
	if len(d.runs) != 1 || d.runs[0][2] != "def456" {
		t.Errorf("runs = %q", d.runs)
	}
}

// Row 4.
func TestExec_OneOffWhenNotRunning(t *testing.T) {
	for _, tty := range []bool{true, false} {
		terminal(t, tty)
		dir, path := newProject(t, phoenix, map[string]string{".env": phoenixEnv})
		var override string
		d := execDocker("", "")
		d.runExit = 5
		d.onRun = func() {
			b, _ := os.ReadFile(filepath.Join(dir, ".houston", "compose.exec.yml"))
			override = string(b)
		}

		code, _, stderr := run(d, "-f", path, "exec", "bin/myapp", "db", "migrate")

		if code != 5 {
			t.Errorf("tty %v: exit = %d, want the command's 5 (stderr: %s)", tty, code, stderr)
		}
		want := oneOff(dir, path, "phoenixapp", tty, "bin/myapp", "db", "migrate")
		if len(d.runs) != 1 || !reflect.DeepEqual(d.runs[0], want) {
			t.Errorf("tty %v: runs = %q\nwant %q", tty, d.runs, want)
		}
		if !strings.Contains(override, "target: dev") || strings.Contains(override, "houston-dev") {
			t.Errorf("tty %v: compose.exec.yml =\n%s", tty, override)
		}
		if _, err := os.Stat(filepath.Join(dir, ".houston", "compose.dev.yml")); err == nil {
			t.Errorf("tty %v: wrote compose.dev.yml, which a running houston dev uses", tty)
		}
	}
}

// Row 5.
func TestExec_OneOffStopsWhatItStarted(t *testing.T) {
	terminal(t, false)
	dir, path := newProject(t, phoenix, map[string]string{".env": phoenixEnv})
	d := execDocker("", "")

	run(d, "-f", path, "exec", "true")

	want := []string{"compose", "-p", "phoenixapp", "--project-directory", dir, "-f", path, "-f", filepath.Join(dir, ".houston", "compose.exec.yml"), "stop"}
	if !d.called(want...) {
		t.Errorf("didn't stop what it started; outputs %q", d.outputs)
	}
}

func TestExec_OneOffLeavesRunningServices(t *testing.T) {
	terminal(t, false)
	_, path := newProject(t, phoenix, map[string]string{".env": phoenixEnv})
	d := execDocker("", "db789\n")

	run(d, "-f", path, "exec", "true")

	if len(d.runs) != 1 || d.runs[0][len(d.runs[0])-1] != "true" {
		t.Fatalf("runs = %q", d.runs)
	}
	if stopped(d) {
		t.Errorf("stopped a database that was running before; outputs %q", d.outputs)
	}
}

// Row 6.
func TestExec_BranchOneOffCopiesMainData(t *testing.T) {
	terminal(t, false)
	dir, path := onBranch(t, "feature1")
	d := &fakeDocker{outputFn: volumes([]string{"shop_storage", "shop_pgdata"}, "", nil)}

	code, _, stderr := run(d, "-f", path, "exec", "true")

	want := []string{"volume create shop-feature1_pgdata", "copy shop_pgdata:/from:ro shop-feature1_pgdata:/to",
		"volume create shop-feature1_storage", "copy shop_storage:/from:ro shop-feature1_storage:/to"}
	if code != 0 || !reflect.DeepEqual(verbs(d), want) {
		t.Errorf("exit %d\n got %q\nwant %q\n%s", code, verbs(d), want, stderr)
	}
	if len(d.runs) != 1 || !reflect.DeepEqual(d.runs[0], oneOff(dir, path, "shop-feature1", false, "true")) {
		t.Errorf("runs = %q", d.runs)
	}
}

// Row 7.
func TestExec_OneOffUsesMainCheckoutEnv(t *testing.T) {
	terminal(t, false)
	main, _ := mainCheckout(t, "SECRET_KEY_BASE=x\n")
	_, wtPath := addWorktree(t, main, "feature1", "feature1")
	d := &fakeDocker{outputFn: volumes(nil, "", nil)}

	code, _, stderr := run(d, "-f", wtPath, "exec", "true")

	mainEnv := filepath.Join(main, ".env")
	if code != 0 || len(d.runs) != 1 || !slices.Contains(d.runs[0], "--env-file") || d.runs[0][slices.Index(d.runs[0], "--env-file")+1] != mainEnv {
		t.Errorf("exit %d, runs %q\n%s", code, d.runs, stderr)
	}
}

// Row 8.
func TestExec_NeedsACommand(t *testing.T) {
	_, path := newProject(t, phoenix, nil)
	d := execDocker("abc123\n", "")

	code, _, _ := run(d, "-f", path, "exec")

	if code != 2 || len(d.runs) != 0 {
		t.Errorf("exit = %d, runs %q; want 2 and nothing run", code, d.runs)
	}
}

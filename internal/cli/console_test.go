package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

var phoenixWithConsole = strings.Replace(phoenix, "  health: /health\n",
	"  health: /health\n  commands:\n    console: { dev: iex -S mix, server: bin/phoenixapp remote }\n", 1)

var runningAppQuery = []string{"ps", "-q",
	"--filter", "label=com.docker.compose.project=phoenixapp",
	"--filter", "label=com.docker.compose.service=app",
	"--filter", "label=com.docker.compose.oneoff=False"}

// terminal pretends Houston's stdin is (or isn't) a terminal for one test.
func terminal(t *testing.T, is bool) {
	t.Helper()
	was := stdinIsTerminal
	stdinIsTerminal = func() bool { return is }
	t.Cleanup(func() { stdinIsTerminal = was })
}

// lastOutput is the last docker Output call (the container lookup).
func (f *fakeDocker) lastOutput() []string {
	if len(f.outputs) == 0 {
		return nil
	}
	return f.outputs[len(f.outputs)-1]
}

func TestConsole_ExecsInRunningApp(t *testing.T) {
	terminal(t, true)
	_, path := newProject(t, phoenixWithConsole, nil)
	d := &fakeDocker{psOut: "abc123\n", runExit: 4}

	code, _, stderr := run(d, "-f", path, "console")

	if code != 4 {
		t.Errorf("exit = %d, want the console's 4 (stderr: %s)", code, stderr)
	}
	if !reflect.DeepEqual(d.lastOutput(), runningAppQuery) {
		t.Errorf("lookup = %q\nwant %q", d.lastOutput(), runningAppQuery)
	}
	want := []string{"exec", "-i", "-t", "abc123", "sh", "-c", "iex -S mix"}
	if len(d.runs) != 1 || !reflect.DeepEqual(d.runs[0], want) {
		t.Errorf("runs = %q, want %q", d.runs, want)
	}
}

func TestConsole_PipedInputSkipsTTY(t *testing.T) {
	terminal(t, false)
	_, path := newProject(t, phoenixWithConsole, nil)
	d := &fakeDocker{psOut: "abc123\n"}

	run(d, "-f", path, "console")

	want := []string{"exec", "-i", "abc123", "sh", "-c", "iex -S mix"}
	if len(d.runs) != 1 || !reflect.DeepEqual(d.runs[0], want) {
		t.Errorf("runs = %q, want %q", d.runs, want)
	}
}

func TestConsole_UsesDevCommand(t *testing.T) {
	terminal(t, false)
	compose := strings.Replace(phoenixWithConsole, "dev: iex -S mix", "dev: 'iex --sname $$NODE -S mix'", 1)
	_, path := newProject(t, compose, nil)
	d := &fakeDocker{psOut: "abc123\n"}

	code, _, stderr := run(d, "-f", path, "console")

	if code != 0 || len(d.runs) != 1 {
		t.Fatalf("exit = %d, runs = %q (stderr: %s)", code, d.runs, stderr)
	}
	if got := d.runs[0][len(d.runs[0])-1]; got != "iex --sname $NODE -S mix" {
		t.Errorf("console command = %q, want the dev one with $$ unescaped", got)
	}
}

func TestConsole_AppNotRunning(t *testing.T) {
	_, path := newProject(t, phoenixWithConsole, nil)
	d := &fakeDocker{psOut: ""}

	code, _, stderr := run(d, "-f", path, "console")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "phoenixapp isn't running") || !strings.Contains(stderr, "houston dev") {
		t.Errorf("stderr = %q", stderr)
	}
	if len(d.runs) != 0 {
		t.Errorf("exec ran: %q", d.runs)
	}
}

func TestConsole_NoConsoleCommand(t *testing.T) {
	_, path := newProject(t, phoenix, nil)
	d := &fakeDocker{psOut: "abc123\n"}

	code, _, stderr := run(d, "-f", path, "console")

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "no x-houston.commands.console") {
		t.Errorf("stderr = %q", stderr)
	}
	if d.calls() != 0 {
		t.Errorf("docker was called: %q %q", d.outputs, d.runs)
	}
}

func TestConsoleAndLogs_UseNewestContainer(t *testing.T) {
	terminal(t, false)
	for _, cmd := range []string{"console", "logs"} {
		t.Run(cmd, func(t *testing.T) {
			_, path := newProject(t, phoenixWithConsole, nil)
			d := &fakeDocker{psOut: "newest\nolder\n"}
			run(d, "-f", path, cmd)
			if len(d.runs) != 1 || !contains(d.runs[0], "newest") || contains(d.runs[0], "older") {
				t.Errorf("runs = %q, want the newest container only", d.runs)
			}
		})
	}
}

func TestConsoleAndLogs_StopEarly(t *testing.T) {
	boom := errors.New("boom")
	for _, cmd := range []string{"console", "logs"} {
		t.Run(cmd+"/invalid compose file", func(t *testing.T) {
			_, path := newProject(t, strings.Replace(phoenixWithConsole, "health: /health", "health: health", 1), nil)
			d := &fakeDocker{psOut: "abc123\n"}
			code, _, stderr := run(d, "-f", path, cmd)
			if code != 2 || d.calls() != 0 || !strings.Contains(stderr, "x-houston.health") {
				t.Errorf("exit = %d, docker calls = %d, stderr = %s", code, d.calls(), stderr)
			}
		})
		for name, d := range map[string]*fakeDocker{
			"not installed": {lookErr: boom, psOut: "abc123\n"},
			"not running":   {versionErr: boom, psOut: "abc123\n"},
			"no compose v2": {composeErr: boom, psOut: "abc123\n"},
		} {
			t.Run(cmd+"/"+name, func(t *testing.T) {
				_, path := newProject(t, phoenixWithConsole, nil)
				code, _, _ := run(d, "-f", path, cmd)
				if code != 1 || len(d.runs) != 0 {
					t.Errorf("exit = %d, runs = %q; want 1 and none", code, d.runs)
				}
			})
		}
		for _, flag := range []string{"--server", "--tail"} {
			t.Run(cmd+"/"+flag, func(t *testing.T) {
				d := &fakeDocker{}
				if code, _, _ := run(d, cmd, flag); code != 2 || d.calls() != 0 {
					t.Errorf("exit = %d, calls = %d; want 2 and none", code, d.calls())
				}
			})
		}
	}
}

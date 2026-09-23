package cli

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLogsServer(t *testing.T) {
	var query string
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/garage/logs": func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			io.WriteString(w, "line one\nline two\n")
		},
	})
	code, out, errOut := run(&fakeDocker{}, "logs", "--server", "-f", "--tail", "50", "--project", "garage")
	if code != 0 || out != "line one\nline two\n" {
		t.Errorf("exit %d, out %q, err %q", code, out, errOut)
	}
	if query != "follow=1&tail=50" {
		t.Errorf("query = %q", query)
	}
}

// fakeSSH records the ssh command console --server runs.
type fakeSSH struct {
	args []string
	exit int
}

func (f *fakeSSH) run(args []string) int {
	f.args = args
	return f.exit
}

func TestConsoleServer(t *testing.T) {
	remoteServer(t, nil)
	ssh := &fakeSSH{exit: 3}
	was := runSSH
	runSSH = ssh.run
	t.Cleanup(func() { runSSH = was })

	hostile := strings.Replace(phoenix, "  health: /health\n", "  health: /health\n  commands:\n    console: { dev: iex -S mix, server: \"echo 'hi'; id\" }\n", 1)
	_, path := newProject(t, hostile, nil)

	code, _, errOut := run(&fakeDocker{}, "-f", path, "console", "--server")
	if code != exitUsage || !strings.Contains(errOut, "houston login --ssh") || !strings.Contains(errOut, "ssh-copy-id") {
		t.Errorf("no SSH target: exit %d, %q", code, errOut)
	}

	t.Setenv("HOUSTON_SSH", "houston@192.168.1.20")
	code, _, errOut = run(&fakeDocker{}, "-f", path, "console", "--server")
	if code != 3 {
		t.Errorf("exit %d, want the console's 3\n%s", code, errOut)
	}
	remote := `docker exec -it "$(docker ps -q --filter label=service=phoenixapp --filter label=role=web | head -n1)" sh -c 'echo '\''hi'\''; id'`
	if want := []string{"ssh", "-t", "houston@192.168.1.20", remote}; !reflect.DeepEqual(ssh.args, want) {
		t.Errorf("ssh args =\n%q\nwant\n%q", ssh.args, want)
	}

	_, bare := newProject(t, phoenix, nil)
	if code, _, errOut := run(&fakeDocker{}, "-f", bare, "console", "--server"); code != exitUsage || !strings.Contains(errOut, "commands.console") {
		t.Errorf("no console command: exit %d, %q", code, errOut)
	}
}

func TestLoginSavesTheSSHTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOUSTON_API_TOKEN", "")
	terminal(t, false)
	s := meServer(t)
	if code, _, errOut := runWithInput(&fakeDocker{}, "hou_good\n", "login", s.URL, "--ssh", "houston@192.168.1.20"); code != 0 {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".config", "houston", "server.json"))
	if !strings.Contains(string(data), `"ssh": "houston@192.168.1.20"`) {
		t.Errorf("saved = %s", data)
	}
}

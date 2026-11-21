package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/scttymn/houston/internal/execstream"
)

// nowhere is a compose file path that doesn't exist: run outside a project.
func nowhere(t *testing.T) string {
	return filepath.Join(t.TempDir(), "compose.yml")
}

// execRoute is Mission Control's exec route for project: it records the
// command it's sent and answers with frames.
func execRoute(t *testing.T, project string, got *[]string, answer func(*execstream.Writer)) map[string]func(http.ResponseWriter, *http.Request) {
	return map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/" + project + "/exec": func(w http.ResponseWriter, r *http.Request) {
			var body struct{ Command []string }
			data, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer hou_x" || json.Unmarshal(data, &body) != nil {
				t.Errorf("%s with %q, body %s", r.Method, r.Header.Get("Authorization"), data)
			}
			*got = body.Command
			w.Header().Set(execstream.Header, "1")
			w.Header().Set("Content-Type", execstream.ContentType)
			answer(execstream.NewWriter(w, nil))
		},
	}
}

// noSSH fails the test if anything runs ssh.
func noSSH(t *testing.T) {
	was := runSSH
	runSSH = func(args []string) int { t.Errorf("ssh ran: %q", args); return 0 }
	t.Cleanup(func() { runSSH = was })
}

func TestExecServer_RunsThroughTheAPI(t *testing.T) {
	noSSH(t)
	t.Setenv("HOUSTON_SSH", "")
	var got []string
	remoteServer(t, execRoute(t, "svnmns", &got, func(w *execstream.Writer) {
		io.WriteString(w.Stdout(), "CODE-4f9a\n")
		io.WriteString(w.Stderr(), "a warning\n")
		w.Exit(7)
	}))
	command := []string{"/app", "task", "auth:setup-code; touch pwned", "$(id)", "a b", ""}

	code, stdout, errOut := run(&fakeDocker{}, append([]string{"-f", nowhere(t), "exec", "--server", "--project", "svnmns"}, command...)...)

	if code != 7 {
		t.Errorf("exit = %d, want the command's 7 (stderr: %s)", code, errOut)
	}
	if !reflect.DeepEqual(got, command) {
		t.Errorf("sent %q\nwant %q (as a list, never joined)", got, command)
	}
	if stdout != "CODE-4f9a\n" || errOut != "a warning\n" {
		t.Errorf("stdout %q, stderr %q: want the command's, apart, and nothing of Houston's", stdout, errOut)
	}
}

func TestExecServer_ProjectFromComposeFile(t *testing.T) {
	var got []string
	remoteServer(t, execRoute(t, "phoenixapp", &got, func(w *execstream.Writer) { w.Exit(0) }))
	_, path := newProject(t, phoenix, nil)

	// Flags after the command are the command's.
	code, _, errOut := run(&fakeDocker{}, "-f", path, "exec", "--server", "ls", "-la", "--server")

	if code != 0 || !reflect.DeepEqual(got, []string{"ls", "-la", "--server"}) {
		t.Errorf("exit = %d, sent %q (stderr: %s)", code, got, errOut)
	}
}

func TestExecServer_Failures(t *testing.T) {
	html404 := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "<!doctype html><title>404</title>")
	}
	for name, c := range map[string]struct {
		handler func(http.ResponseWriter, *http.Request)
		want    string
	}{
		"a server too old for exec": {html404, "doesn't have exec --server yet; update it: houston update"},
		"not running": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(execstream.Header, "1")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"svnmns isn't running"}`)
		}, "houston: svnmns isn't running\n"},
		"refused token": {func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"the API token is missing, wrong or revoked"}`)
		}, "run houston login"},
		"cut off": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(execstream.Header, "1")
			w.Header().Set("Content-Type", execstream.ContentType)
			io.WriteString(execstream.NewWriter(w, nil).Stdout(), "half")
		}, "the connection ended before the command did"},
		"not frames": {func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "<html>a login page</html>")
		}, "didn't answer as Mission Control's exec route"},
	} {
		t.Run(name, func(t *testing.T) {
			remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){"/api/v1/projects/svnmns/exec": c.handler})
			code, _, errOut := run(&fakeDocker{}, "-f", nowhere(t), "exec", "--server", "--project", "svnmns", "true")
			if code != exitFailure || !strings.Contains(errOut, c.want) {
				t.Errorf("exit = %d, stderr = %q; want 1 and %q", code, errOut, c.want)
			}
		})
	}
}

func TestExecServer_NotLoggedIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HOUSTON_SERVER", "")
	t.Setenv("HOUSTON_API_TOKEN", "")
	code, _, errOut := run(&fakeDocker{}, "-f", nowhere(t), "exec", "--server", "--project", "svnmns", "true")
	if code != exitFailure || !strings.Contains(errOut, "houston login") {
		t.Errorf("exit = %d, stderr = %q", code, errOut)
	}
}

func TestExecServer_Usage(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no project":             {[]string{"exec", "--server", "true"}, "--project"},
		"no command":             {[]string{"exec", "--server", "--project", "svnmns"}, "requires at least 1 arg"},
		"--project, no --server": {[]string{"exec", "--project", "svnmns", "true"}, "--project goes with --server"},
	} {
		t.Run(name, func(t *testing.T) {
			asked := false
			remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
				"/api/v1/projects/svnmns/exec": func(http.ResponseWriter, *http.Request) { asked = true },
			})
			d := &fakeDocker{}
			code, _, errOut := run(d, append([]string{"-f", nowhere(t)}, c.args...)...)
			if code != exitUsage || !strings.Contains(errOut, c.want) {
				t.Errorf("exit = %d, stderr = %q; want 2 and %q", code, errOut, c.want)
			}
			if asked || d.calls() != 0 {
				t.Errorf("ran something: server asked %v, docker %q %q", asked, d.outputs, d.runs)
			}
		})
	}
}

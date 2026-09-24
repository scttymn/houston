package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// remoteServer answers /api/v1 like Mission Control, one handler per path.
func remoteServer(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := routes[r.URL.Path]; ok {
			h(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"no project nope"}`)
	}))
	t.Cleanup(s.Close)
	t.Setenv("HOUSTON_SERVER", s.URL)
	t.Setenv("HOUSTON_API_TOKEN", "hou_x")
	t.Setenv("HOME", t.TempDir())
}

func respond(body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }
}

const garageJSON = `{"name":"garage","status":"go","running_sha":"4be21c0aa11b2c3d4e5f60718293a4b5c6d7e8f9","host":"garage.svnmns.com",
	"domains":{"equipping.com":{"state":"DNS PENDING","reason":"nameservers"}},"repo_url":"git@forgejo:h/garage.git","branch":"main",
	"webhook_verified":true,"services":["app","db"],"deploy_rule":{"on":"commit","branch":"main"},
	"secrets":[{"name":"RAILS_MASTER_KEY","required":true,"set":true}],
	"last_deploy":{"number":7,"status":"go","sha":"4be21c0aa11b2c3d4e5f60718293a4b5c6d7e8f9","ref":"refs/heads/main","duration":108}}`

func TestStatus(t *testing.T) {
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/me":              respond(`{"token":"laptop","server":"svnmns.com","version":"v0.1.0"}`),
		"/api/v1/projects":        respond(`{"projects":[` + garageJSON + `,{"name":"fresh","status":"standby","host":"fresh.svnmns.com","domains":{}}]}`),
		"/api/v1/projects/garage": respond(garageJSON),
	})
	t.Chdir(t.TempDir()) // no compose.yml here

	code, out, errOut := run(&fakeDocker{}, "status")
	if code != 0 || !strings.Contains(out, "garage") || !strings.Contains(out, "fresh") || !strings.Contains(out, "STANDBY") {
		t.Errorf("all projects: exit %d\n%s%s", code, out, errOut)
	}

	code, out, _ = run(&fakeDocker{}, "status", "--project", "garage")
	for _, want := range []string{"GO", "4be21c0", "garage.svnmns.com", "equipping.com: DNS PENDING", "#7", "Every commit to main"} {
		if !strings.Contains(out, want) {
			t.Errorf("status --project garage lacks %q:\n%s", want, out)
		}
	}

	_, path := newProject(t, strings.Replace(phoenix, "name: phoenixapp", "name: garage", 1), nil)
	code, out, _ = run(&fakeDocker{}, "-f", path, "status", "--json")
	var got map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got["name"] != "garage" {
		t.Errorf("--json from the compose name: exit %d\n%s", code, out)
	}
}

// The server's version (docs/plans/releases.md, row 3): first, before the
// projects, from /api/v1/me.
func TestStatusPrintsServerVersion(t *testing.T) {
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/me":       respond(`{"token":"laptop","server":"svnmns.com","version":"v0.1.0"}`),
		"/api/v1/projects": respond(`{"projects":[` + garageJSON + `]}`),
	})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "status")
	first, _, _ := strings.Cut(out, "\n")
	if code != 0 || first != "Houston v0.1.0 at svnmns.com" || !strings.Contains(out, "garage") {
		t.Errorf("status: exit %d\n%s%s", code, out, errOut)
	}

	// A newer release (docs/plans/update-available.md): said on the same line.
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/me":       respond(`{"token":"laptop","server":"svnmns.com","version":"v0.1.0","latest":"v0.1.1"}`),
		"/api/v1/projects": respond(`{"projects":[]}`),
	})
	_, out, _ = run(&fakeDocker{}, "status")
	if first, _, _ := strings.Cut(out, "\n"); first != "Houston v0.1.0 at svnmns.com (v0.1.1 available)" {
		t.Errorf("status with a newer release:\n%s", out)
	}
}

func TestDeploysFollow(t *testing.T) {
	polls := []string{
		`{"number":8,"status":"queued","sha":"` + strings.Repeat("c", 40) + `","ref":"refs/heads/main","log":"","log_next":0}`,
		`{"number":8,"status":"in_flight","sha":"` + strings.Repeat("c", 40) + `","ref":"refs/heads/main","step":"Test","log":"testing\n","log_next":8}`,
		`{"number":8,"status":"in_flight","sha":"` + strings.Repeat("c", 40) + `","ref":"refs/heads/main","step":"Build","log":"building\n","log_next":17}`,
		`{"number":8,"status":"no_go","sha":"` + strings.Repeat("c", 40) + `","ref":"refs/heads/main","error":"release hook failed (exit 3)","log":"","log_next":17}`,
	}
	var n atomic.Int32
	var froms []string
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/garage/deploys/8": func(w http.ResponseWriter, r *http.Request) {
			froms = append(froms, r.URL.Query().Get("log_from"))
			i := min(int(n.Add(1))-1, len(polls)-1)
			io.WriteString(w, polls[i])
		},
	})
	followEvery = time.Millisecond

	code, out, errOut := run(&fakeDocker{}, "deploys", "show", "8", "--project", "garage", "--follow")
	if code != 1 {
		t.Errorf("exit %d, want 1 (NO-GO)\n%s", code, errOut)
	}
	if !strings.Contains(out, "testing\nbuilding\n") || strings.Count(out, "testing") != 1 {
		t.Errorf("each log byte once, in order:\n%s", out)
	}
	if !strings.Contains(out+errOut, "NO-GO") || !strings.Contains(out+errOut, "release hook failed (exit 3)") {
		t.Errorf("the result and its reason:\n%s%s", out, errOut)
	}
	if strings.Join(froms, ",") != "0,0,8,17" {
		t.Errorf("log_from sequence = %v", froms)
	}

	n.Store(3)
	polls[3] = strings.Replace(strings.Replace(polls[3], `"no_go"`, `"go"`, 1), `"error":"release hook failed (exit 3)",`, "", 1)
	if code, _, _ := run(&fakeDocker{}, "deploys", "show", "8", "--project", "garage", "--follow"); code != 0 {
		t.Errorf("GO: exit %d, want 0", code)
	}
}

func TestProjectResolution(t *testing.T) {
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){})
	t.Chdir(t.TempDir())
	code, _, errOut := run(&fakeDocker{}, "deploys")
	if code != exitUsage || !strings.Contains(errOut, "--project") {
		t.Errorf("no project anywhere: exit %d, %q", code, errOut)
	}
	_, path := newProject(t, strings.Replace(phoenix, "health: /health", "health: health", 1), nil)
	code, _, errOut = run(&fakeDocker{}, "-f", path, "deploys")
	if code != exitUsage || !strings.Contains(errOut, "x-houston.health") {
		t.Errorf("broken compose: exit %d, %q", code, errOut)
	}
}

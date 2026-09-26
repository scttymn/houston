package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// houston update: update the server to a release from anywhere
// (docs/plans/update-from-mission-control.md, row 11).

// updateServer answers POST /api/v1/update with a running update, then each
// GET with the next of polls ("" for Mission Control not answering, as while
// it restarts).
func updateServer(t *testing.T, polls ...string) (sent *[]map[string]any) {
	var mu sync.Mutex
	sent = &[]map[string]any{}
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/update": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if r.Method == http.MethodPost {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				*sent = append(*sent, body)
				w.WriteHeader(http.StatusAccepted)
				io.WriteString(w, `{"version":"v0.4.2","latest":"v0.4.3","update":{"id":7,"to":"v0.4.3","from":"v0.4.2","status":"running"},"message":"Updating to v0.4.3. Mission Control restarts on the way; deploys and backups wait."}`)
				return
			}
			if len(polls) == 0 {
				t.Error("polled after the result")
				return
			}
			next := polls[0]
			polls = polls[1:]
			if next == "" {
				w.WriteHeader(http.StatusBadGateway)
				io.WriteString(w, "Bad gateway")
				return
			}
			io.WriteString(w, next)
		},
	})
	return sent
}

func updateAnswer(id int, status, log string) string {
	b, _ := json.Marshal(map[string]any{"version": "v0.4.2", "latest": "v0.4.3",
		"update": map[string]any{"id": id, "to": "v0.4.3", "from": "v0.4.2", "status": status, "log": log}})
	return string(b)
}

func TestUpdate(t *testing.T) {
	defer func(d time.Duration) { updatePoll = d }(updatePoll)
	updatePoll = time.Millisecond
	t.Chdir(t.TempDir())

	sent := updateServer(t, updateAnswer(7, "running", ""), "", "", updateAnswer(7, "go", "==> Starting Houston\n"))
	code, out, errOut := run(&fakeDocker{}, "update")
	if code != 0 || len(*sent) != 1 || (*sent)[0]["version"] != nil ||
		!strings.Contains(out, "Updating to v0.4.3") || !strings.Contains(out, "GO  the server runs v0.4.3") {
		t.Errorf("exit %d, sent %v\n%s%s", code, *sent, out, errOut)
	}
	if strings.Count(errOut, "isn't answering") != 1 {
		t.Errorf("the restart is said once: %q", errOut)
	}

	sent = updateServer(t, updateAnswer(6, "go", ""), updateAnswer(7, "rolled_back", "curl: (22) 404\n"))
	code, out, errOut = run(&fakeDocker{}, "update", "v0.4.3")
	if code != 1 || (*sent)[0]["version"] != "v0.4.3" || !strings.Contains(out, "NO-GO  the update to v0.4.3 failed; the server went back to v0.4.2") ||
		!strings.Contains(out, "curl: (22) 404") {
		t.Errorf("rolled back: exit %d, sent %v\n%s%s", code, *sent, out, errOut)
	}

	updateServer(t, updateAnswer(7, "no_go", "the helper is gone\n"))
	if code, out, _ := run(&fakeDocker{}, "update"); code != 1 || !strings.Contains(out, "NO-GO  the update to v0.4.3 failed") {
		t.Errorf("no-go: exit %d\n%s", code, out)
	}
}

func TestUpdateRefusedAndTimedOut(t *testing.T) {
	defer func(d, w time.Duration) { updatePoll, updateWait = d, w }(updatePoll, updateWait)
	updatePoll, updateWait = time.Millisecond, 20*time.Millisecond
	t.Chdir(t.TempDir())

	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/update": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"error":"Houston deploy #3 of equip is in flight; update when it's done"}`)
		},
	})
	if code, _, errOut := run(&fakeDocker{}, "update"); code != 1 || !strings.Contains(errOut, "deploy #3 of equip is in flight") {
		t.Errorf("refused: exit %d: %s", code, errOut)
	}

	polls := make([]string, 1000)
	for i := range polls {
		polls[i] = updateAnswer(7, "running", "")
	}
	updateServer(t, polls...)
	if code, _, errOut := run(&fakeDocker{}, "update"); code != 1 || !strings.Contains(errOut, "docker logs houston-update") {
		t.Errorf("never finished: exit %d: %s", code, errOut)
	}
	if code, _, _ := run(&fakeDocker{}, "update", "v1", "v2"); code != 2 {
		t.Errorf("two versions: exit %d", code)
	}
}

func TestStatusSaysWhenUpdating(t *testing.T) {
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/me":       respond(`{"token":"laptop","server":"svnmns.com","version":"v0.4.2","latest":"v0.4.3","updating":"v0.4.3"}`),
		"/api/v1/projects": respond(`{"projects":[]}`),
	})
	t.Chdir(t.TempDir())
	_, out, _ := run(&fakeDocker{}, "status")
	if first, _, _ := strings.Cut(out, "\n"); first != "Houston v0.4.2 at svnmns.com (updating to v0.4.3)" {
		t.Errorf("status while updating:\n%s", out)
	}
}

func TestUpdateCheck(t *testing.T) {
	status, answer := http.StatusOK, `{"version":"v0.4.4","latest":"v0.4.5","update":null,"message":"v0.4.5 is out."}`
	var posts int
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/update/check": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts++
			}
			w.WriteHeader(status)
			io.WriteString(w, answer)
		},
		"/api/v1/update": func(w http.ResponseWriter, r *http.Request) { t.Error("--check started an update") },
	})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "update", "--check")
	if code != 0 || posts != 1 || !strings.Contains(out, "v0.4.5 is out. houston update installs it.") {
		t.Errorf("newer: exit %d, %d posts\n%s%s", code, posts, out, errOut)
	}
	answer = `{"version":"v0.4.5","latest":null,"update":null,"message":"v0.4.5 is the latest."}`
	if code, out, _ := run(&fakeDocker{}, "update", "--check"); code != 0 || strings.TrimSpace(out) != "v0.4.5 is the latest." {
		t.Errorf("current: exit %d\n%s", code, out)
	}
	status, answer = http.StatusBadGateway, `{"error":"Couldn't reach GitHub just now; try again in a minute."}`
	if code, _, errOut := run(&fakeDocker{}, "update", "--check"); code != 1 || !strings.Contains(errOut, "Couldn't reach GitHub") {
		t.Errorf("GitHub down: exit %d: %s", code, errOut)
	}
	if code, _, _ := run(&fakeDocker{}, "update", "--check", "v0.4.5"); code != 2 {
		t.Errorf("--check with a version: exit %d", code)
	}
}

func TestUpdatePrintsSteps(t *testing.T) {
	defer func(d time.Duration) { updatePoll = d }(updatePoll)
	updatePoll = time.Millisecond
	t.Chdir(t.TempDir())
	step := func(s string) string {
		return `{"version":"v0.4.2","update":{"id":7,"to":"v0.4.3","from":"v0.4.2","status":"running","step":"` + s + `"}}`
	}
	updateServer(t, step("Docker is installed"), step("Pulling Houston v0.4.3"), step("Pulling Houston v0.4.3"), "", step("Starting Houston"), updateAnswer(7, "go", ""))
	code, out, errOut := run(&fakeDocker{}, "update")
	want := "  Docker is installed\n  Pulling Houston v0.4.3\n  Starting Houston\nGO  the server runs v0.4.3\n"
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Errorf("exit %d\n%s\nwant it to end with\n%s%s", code, out, want, errOut)
	}
}

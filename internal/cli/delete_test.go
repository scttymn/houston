package cli

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scttymn/houston/internal/server"
)

// docs/plans/delete-project.md, Batch 5: houston delete.
func TestDelete(t *testing.T) {
	followEvery = time.Millisecond
	t.Cleanup(func() { followEvery = 2 * time.Second })
	var method, body string
	var requests, polls atomic.Int32
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/equip": func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			method, body = r.Method, readBody(r)
			if strings.Contains(body, `"confirm":"nope"`) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				w.Write([]byte(`{"error":"type equip to confirm"}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"deletion":{"id":4,"name":"equip","status":"queued","delete_backups":true}}`))
		},
		"/api/v1/deletions/4": func(w http.ResponseWriter, r *http.Request) {
			switch polls.Add(1) {
			case 1:
				w.Write([]byte(`{"id":4,"name":"equip","status":"running","step":"volumes"}`))
			case 2:
				w.Write([]byte(`{"id":4,"name":"equip","status":"running","step":"volumes"}`))
			default:
				w.Write([]byte(`{"id":4,"name":"equip","status":"go","step":"rows","snapshot_id":"5c5edd4c00000000","snapshot_location":"unas-nfs",
					"repo_url":"git@forgejo:houston/equip.git"}`))
			}
		},
	})
	t.Chdir(t.TempDir())

	code, _, errOut := run(&fakeDocker{}, "delete", "--project", "equip")
	if code != 2 || !strings.Contains(errOut, "add --confirm equip") || requests.Load() != 0 {
		t.Errorf("no --confirm: exit %d, %d requests, %q", code, requests.Load(), errOut)
	}

	code, out, errOut := run(&fakeDocker{}, "delete", "--confirm", "equip", "--project", "equip", "--delete-backups", "--follow")
	if code != 0 || method != http.MethodDelete || body != `{"confirm":"equip","delete_backups":true}` {
		t.Errorf("delete --follow: exit %d, %s %s\n%s%s", code, method, body, out, errOut)
	}
	for _, want := range []string{
		"Deleting equip (deletion 4); its backups go too.",
		"  volumes\n",
		"GO: equip is deleted. Its final snapshot 5c5edd4c is kept in unas-nfs.",
		"Remove the deploy key and the webhook from git@forgejo:houston/equip.git",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("delete --follow: missing %q in\n%s", want, out)
		}
	}
	if strings.Count(out, "  volumes\n") != 1 {
		t.Errorf("each step once:\n%s", out)
	}

	code, out, errOut = run(&fakeDocker{}, "delete", "--confirm", "equip", "--project", "equip")
	if code != 0 || body != `{"confirm":"equip","delete_backups":false}` || !strings.Contains(out, "houston status") {
		t.Errorf("no --follow: exit %d, %s\n%s%s", code, body, out, errOut)
	}

	code, _, errOut = run(&fakeDocker{}, "delete", "--confirm", "nope", "--project", "equip")
	if code != 1 || !strings.Contains(errOut, "type equip to confirm") {
		t.Errorf("refused: exit %d, %q", code, errOut)
	}
}

func TestDeleteFollowNoGo(t *testing.T) {
	followEvery = time.Millisecond
	t.Cleanup(func() { followEvery = 2 * time.Second })
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/equip": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"deletion":{"id":5,"name":"equip","status":"queued"}}`))
		},
		"/api/v1/deletions/5": respond(`{"id":5,"name":"equip","status":"no_go","step":"volumes","error":"stopped at volumes: volume is in use"}`),
	})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "delete", "--confirm", "equip", "--project", "equip", "--follow")
	if code != 1 || !strings.Contains(out, "NO-GO: deleting equip stopped at volumes: volume is in use") || !strings.Contains(out, "run this again to finish") {
		t.Errorf("NO-GO: exit %d\n%s%s", code, out, errOut)
	}
}

// houston status says a project is being deleted, and how to finish one
// that stopped partway.
func TestStatusWhileDeleting(t *testing.T) {
	deleting := `{"id":4,"status":"running","step":"volumes"}`
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/garage": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(strings.Replace(garageJSON, `"name":"garage",`, `"name":"garage","deleting":`+deleting+`,`, 1)))
		},
	})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "status", "--project", "garage")
	if code != 0 || !strings.Contains(out, "deleting  volumes (deletion 4)\n") {
		t.Errorf("running: exit %d\n%s%s", code, out, errOut)
	}
	deleting = `{"id":4,"status":"no_go","step":"volumes","error":"stopped at volumes: busy"}`
	code, out, errOut = run(&fakeDocker{}, "status", "--project", "garage")
	if code != 0 || !strings.Contains(out, "deleting  NO-GO: stopped at volumes: busy; houston delete --confirm garage finishes it\n") {
		t.Errorf("stopped: exit %d\n%s%s", code, out, errOut)
	}
	deleting = "null"
	_, out, _ = run(&fakeDocker{}, "status", "--project", "garage")
	if strings.Contains(out, "deleting") {
		t.Errorf("not deleting:\n%s", out)
	}
}

// A deleted project's final snapshot, listed once a project of its name is
// added again.
func TestSnapshotNoteFinal(t *testing.T) {
	if got := snapshotNote(server.Snapshot{Kind: "final", Reason: "delete"}); got != "before it was deleted" {
		t.Errorf("final: %q", got)
	}
}

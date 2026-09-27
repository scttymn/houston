package cli

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// docs/plans/copy-project.md, Batch 5: houston copy.
func TestCopy(t *testing.T) {
	followEvery = time.Millisecond
	t.Cleanup(func() { followEvery = 2 * time.Second })
	var path, body string
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/equip-go/copy": func(w http.ResponseWriter, r *http.Request) {
			path, body = r.URL.Path, readBody(r)
			if strings.Contains(body, `"confirm":"nope"`) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				w.Write([]byte(`{"error":"type equip-go to confirm"}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"copy":{"id":3,"from":"equip-go","to":"equip","status":"queued","deploy":1,"sha":"c0bb1e5000000000000000000000000000000000"}}`))
		},
		"/api/v1/projects/equip/deploys/1": respond(`{"number":1,"status":"go","kind":"copy","sha":"c0bb1e5000000000000000000000000000000000","log":"","log_next":0}`),
		"/api/v1/projects/equip/copy/cancel": func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			w.Write([]byte(`{"copy":{"id":3,"from":"equip-go","to":"equip","status":"no_go","error":"cancelled by token agent"}}`))
		},
		"/api/v1/projects/equip/copy/undo": func(w http.ResponseWriter, r *http.Request) {
			path, body = r.URL.Path, readBody(r)
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"deletion":{"id":4,"name":"equip","status":"queued"},"copy":{"id":3,"from":"equip-go","to":"equip","status":"go"}}`))
		},
	})
	t.Chdir(t.TempDir())

	code, _, errOut := run(&fakeDocker{}, "copy")
	if code != 2 || !strings.Contains(errOut, "--confirm") {
		t.Errorf("no flags: exit %d, %q", code, errOut)
	}

	code, out, errOut := run(&fakeDocker{}, "copy", "--confirm", "equip-go", "--follow")
	if code != 0 || path != "/api/v1/projects/equip-go/copy" || body != `{"confirm":"equip-go"}` ||
		!strings.Contains(out, "Copying equip-go to equip: deploy #1 of equip.") || !strings.Contains(out, "GO: equip #1") {
		t.Errorf("copy --follow: exit %d, %s %s\n%s%s", code, path, body, out, errOut)
	}

	code, _, errOut = run(&fakeDocker{}, "copy", "--confirm", "nope", "--project", "equip-go")
	if code != 1 || !strings.Contains(errOut, "type equip-go to confirm") {
		t.Errorf("refused: exit %d, %q", code, errOut)
	}

	code, out, _ = run(&fakeDocker{}, "copy", "--cancel", "--project", "equip")
	if code != 0 || path != "/api/v1/projects/equip/copy/cancel" || !strings.Contains(out, "Cancelled the copy of equip-go to equip; equip goes, and equip-go keeps serving.") {
		t.Errorf("cancel: exit %d, %s\n%s", code, path, out)
	}

	code, _, errOut = run(&fakeDocker{}, "copy", "--undo", "--project", "equip")
	if code != 2 || !strings.Contains(errOut, "add --confirm equip") {
		t.Errorf("undo without --confirm: exit %d, %q", code, errOut)
	}
	code, out, _ = run(&fakeDocker{}, "copy", "--undo", "--confirm", "equip", "--project", "equip")
	if code != 0 || path != "/api/v1/projects/equip/copy/undo" || body != `{"confirm":"equip"}` || !strings.Contains(out, "The hosts are equip-go's again; equip is being deleted (deletion 4).") {
		t.Errorf("undo: exit %d, %s %s\n%s", code, path, body, out)
	}
}

func TestStatusShowsACopyProposal(t *testing.T) {
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/garage": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(strings.Replace(garageJSON, `"name":"garage",`, `"name":"garage","copy_proposal":{"name":"garage2","sha":"c0bb1e5000000000000000000000000000000000","deploy":8,"refusal":null},`, 1)))
		},
	})
	t.Chdir(t.TempDir())
	_, out, _ := run(&fakeDocker{}, "status", "--project", "garage")
	if !strings.Contains(out, "hold      compose.yml names garage2 (#8): houston copy --confirm garage\n") {
		t.Errorf("status:\n%s", out)
	}
}

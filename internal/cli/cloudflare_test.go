package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// houston cloudflare (docs/plans/cloudflare-settings.md, row 10).

const cloudflareJSON = `{"tunnel":{"id":"5c1f0e2a-tunnel","name":"houston-svnmns","status":"healthy","created_at":"2026-09-23T04:00:00Z"},
	"connections":[{"colo":"MCI01","version":"2026.9.1","origin":"99.98.226.252","since":"2026-09-24T20:10:01Z"},
	               {"colo":"DFW06","version":"2026.9.1","origin":"99.98.226.252","since":"2026-09-24T20:10:02Z"}],
	"routes":[{"hostname":"admin.svnmns.com","path":null,"service":"http://mission-control:80","drift":false},
	          {"hostname":null,"path":null,"service":"http://kamal-proxy:80","drift":true}],
	"missing_routes":[],"drift":true,
	"records":[{"zone":"svnmns.com","name":"equip.svnmns.com","project":"equip","proxied":true,"here":true},
	           {"zone":"svnmns.com","name":"other.svnmns.com","project":"other","proxied":true,"here":false}],
	"problems":[]}`

func TestCloudflareShow(t *testing.T) {
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){"/api/v1/cloudflare": respond(cloudflareJSON)})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "cloudflare")
	for _, want := range []string{"houston-svnmns", "healthy", "5c1f0e2a-tunnel", "MCI01", "DFW06", "2026.9.1",
		"admin.svnmns.com", "everything else", "DRIFT", "equip.svnmns.com", "this server", "other.svnmns.com", "another server"} {
		if !strings.Contains(out, want) {
			t.Errorf("houston cloudflare lacks %q (exit %d):\n%s%s", want, code, out, errOut)
		}
	}

	code, out, _ = run(&fakeDocker{}, "cloudflare", "--json")
	var got map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got["drift"] != true {
		t.Errorf("--json: exit %d\n%s", code, out)
	}
}

func TestCloudflareTokenFromStdinOnly(t *testing.T) {
	var sent map[string]string
	answer := `{"replaced":true,"checks":[{"ok":true,"label":"Account · Seven Moons"},{"ok":true,"label":"Zone · DNS on svnmns.com"}]}`
	status := http.StatusOK
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/cloudflare/token": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut {
				t.Errorf("method %s", r.Method)
			}
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &sent)
			w.WriteHeader(status)
			io.WriteString(w, answer)
		},
	})
	t.Chdir(t.TempDir())

	code, out, errOut := runWithInput(&fakeDocker{}, "cf-new-token\n", "cloudflare", "token")
	if code != 0 || sent["token"] != "cf-new-token" || !strings.Contains(out, "Account · Seven Moons") || strings.Contains(out+errOut, "cf-new-token") {
		t.Errorf("token from stdin: exit %d sent %v\n%s%s", code, sent, out, errOut)
	}

	sent = nil
	code, _, errOut = run(&fakeDocker{}, "cloudflare", "token", "cf-on-the-command-line")
	if code != 2 || sent != nil || !strings.Contains(errOut, "never on the command line") {
		t.Errorf("a token argument: exit %d, sent %v: %s", code, sent, errOut)
	}

	status = http.StatusUnprocessableEntity
	answer = `{"replaced":false,"checks":[{"ok":true,"label":"Account · Seven Moons"},{"ok":false,"label":"Zone · DNS on estherpictures.com: the token needs Zone · DNS · Edit"}]}`
	code, out, _ = runWithInput(&fakeDocker{}, "cf-weak\n", "cloudflare", "token")
	if code != 1 || !strings.Contains(out, "NO-GO") || !strings.Contains(out, "estherpictures.com") || !strings.Contains(out, "The old token is still in use") {
		t.Errorf("a refused token: exit %d\n%s", code, out)
	}
}

func TestCloudflareRepair(t *testing.T) {
	answer := `{"results":[{"item":"routes","state":"OK","reason":null},{"item":"admin.svnmns.com","state":"DNS OK","reason":null}]}`
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/cloudflare/repair": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method %s", r.Method)
			}
			io.WriteString(w, answer)
		},
	})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "cloudflare", "repair")
	if code != 0 || !strings.Contains(out, "routes") || !strings.Contains(out, "admin.svnmns.com") || !strings.Contains(out, "DNS OK") {
		t.Errorf("repair: exit %d\n%s%s", code, out, errOut)
	}

	answer = `{"results":[{"item":"routes","state":"OK","reason":null},{"item":"hooks.svnmns.com","state":"NO-GO","reason":"hooks.svnmns.com has a record Houston didn't create"}]}`
	code, out, _ = run(&fakeDocker{}, "cloudflare", "repair")
	if code != 1 || !strings.Contains(out, "NO-GO") || !strings.Contains(out, "didn't create") {
		t.Errorf("repair with a NO-GO: exit %d\n%s", code, out)
	}
}

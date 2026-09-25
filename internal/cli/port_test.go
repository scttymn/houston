package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// houston port / port open / port close: Settings › Port 3000
// (docs/plans/security-fixes.md, H3).
func TestPortShow(t *testing.T) {
	answer := `{"open":true,"address":"0.0.0.0","saved":"open"}`
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){"/api/v1/port": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, answer) }})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "port")
	if code != 0 || !strings.Contains(out, "OPEN") || !strings.Contains(out, "to your network") ||
		!strings.Contains(out, "To close it: houston port close. Close it on a server with a public address: Docker publishes it past the firewall.") {
		t.Errorf("open: exit %d\n%s%s", code, out, errOut)
	}
	answer = `{"open":false,"address":"127.0.0.1","saved":"closed"}`
	code, out, _ = run(&fakeDocker{}, "port")
	if code != 0 || !strings.Contains(out, "CLOSED") || !strings.Contains(out, "127.0.0.1") || !strings.Contains(out, "ssh -L 3000:127.0.0.1:3000") ||
		!strings.Contains(out, "To open it to the network: houston port open") {
		t.Errorf("closed: exit %d\n%s", code, out)
	}
	code, out, _ = run(&fakeDocker{}, "port", "--json")
	var got map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got["saved"] != "closed" {
		t.Errorf("--json: exit %d\n%s", code, out)
	}
}

func TestPortOpenAndClose(t *testing.T) {
	var sent []map[string]any
	status, answer := http.StatusAccepted, `{"open":true,"address":"0.0.0.0","saved":"closed","message":"Port 3000 is closing to the network. Mission Control restarts for a few seconds."}`
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/port": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut {
				t.Errorf("method %s", r.Method)
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			sent = append(sent, body)
			w.WriteHeader(status)
			io.WriteString(w, answer)
		},
	})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "port", "close")
	if code != 0 || len(sent) != 1 || sent[0]["open"] != false || !strings.Contains(out, "restarts for a few seconds") {
		t.Errorf("close: exit %d, sent %v\n%s%s", code, sent, out, errOut)
	}
	run(&fakeDocker{}, "port", "open")
	if len(sent) != 2 || sent[1]["open"] != true {
		t.Errorf("open: sent %v", sent)
	}

	status, answer = http.StatusUnprocessableEntity, `{"error":"Houston can't change it from here yet: run the installer once more"}`
	code, _, errOut = run(&fakeDocker{}, "port", "close")
	if code != 1 || !strings.Contains(errOut, "run the installer once more") {
		t.Errorf("refused: exit %d: %s", code, errOut)
	}
	code, _, errOut = run(&fakeDocker{}, "port", "sideways")
	if code != 2 {
		t.Errorf("an unknown word: exit %d: %s", code, errOut)
	}
}

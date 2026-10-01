package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app"
	"github.com/scttymn/houston/mission_control/app/api"
	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission_control/app/services/owncontainer"
	"github.com/scttymn/houston/mission_control/app/services/port"
	"github.com/scttymn/houston/mission_control/app/services/release"
)

const composeLabels = `{"com.docker.compose.project.config_files":"/opt/houston/compose.yml","com.docker.compose.project.working_dir":"/opt/houston"}`

// updatable is a v0.4.2 server installed by the installer, v0.4.3 out.
func updatable(t *testing.T) (*app.App, http.Handler, *dockercmdtest.Fake) {
	t.Helper()
	a, _ := remote(t)
	a.Version, a.Updater.Version, a.Updater.RunnerImage, a.Port.RunnerImage = "v0.4.2", "v0.4.2", "houston/runner:local", "houston/runner:local"
	exec(t, a, `UPDATE installations SET latest_release = 'v0.4.3'`)
	fake := a.DockerCLI.(*dockercmdtest.Fake)
	fake.On(dockercmdtest.OK(`{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"3000"}]}`), "inspect", "--format", "{{json .HostConfig.PortBindings}}")
	fake.On(dockercmdtest.OK(composeLabels), "inspect", "--format", "{{json .Config.Labels}}")
	return a, a.Handler(), fake
}

func TestV1Update(t *testing.T) {
	a, h, _ := updatable(t)
	is(t, v1(h, "GET", "/update", personal, "", nil), 200, `{"version":"v0.4.2","latest":"v0.4.3","update":null}`)
	is(t, v1(h, "GET", "/me", personal, "", nil), 200, `{"token":"laptop","server":"svnmns.com","version":"v0.4.2","latest":"v0.4.3","updating":null}`)

	w := v1(h, "POST", "/update", personal, `{}`, nil)
	got := answer(t, w)
	update, _ := got["update"].(map[string]any)
	if w.Code != 202 || update["to"] != "v0.4.3" || update["from"] != "v0.4.2" || update["status"] != "running" ||
		got["message"] != "Updating to v0.4.3. Mission Control restarts on the way; deploys and backups wait." {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	pending, _ := a.Jobs.Pending(t.Context())
	var follow models.UpdateArgs
	for _, p := range pending {
		if p.Name == "server_update" {
			json.Unmarshal(p.Args, &follow)
		}
	}
	if !follow.Follow {
		t.Errorf("not followed: %+v", pending)
	}
	if got := answer(t, v1(h, "GET", "/me", personal, "", nil)); got["updating"] != "v0.4.3" {
		t.Errorf("me %v", got)
	}

	exec(t, a, `UPDATE server_updates SET step = 'Pulling Houston v0.4.3'`)
	if got := answer(t, v1(h, "GET", "/update", personal, "", nil))["update"].(map[string]any); got["step"] != "Pulling Houston v0.4.3" || got["log"] != nil {
		t.Errorf("update %v", got)
	}
	exec(t, a, `UPDATE server_updates SET status = 'rolled_back', finished_at = CURRENT_TIMESTAMP, log = '404'||char(10)`)
	if got := answer(t, v1(h, "GET", "/update", personal, "", nil))["update"].(map[string]any); got["status"] != "rolled_back" || got["log"] != "404\n" || got["finished_at"] == nil {
		t.Errorf("update %v", got)
	}
}

func TestV1UpdateRefused(t *testing.T) {
	a, h, fake := updatable(t)
	is(t, v1(h, "POST", "/update", personal, `{"version":"v0.4.1"}`, nil), 422, `{"error":"Houston v0.4.1 isn't newer than v0.4.2, which this server runs"}`)
	is(t, v1(h, "POST", "/update", personal, `{"version":4}`, nil), 400, `{"error":"version must be a release tag, like v0.4.3"}`)
	is(t, v1(h, "POST", "/update", "", `{}`, nil), 401, `{"error":"the API token is missing, wrong or revoked"}`)
	is(t, v1(h, "GET", "/update", "", "", nil), 401, `{"error":"the API token is missing, wrong or revoked"}`)
	var n int
	a.DB.Read.QueryRow(`SELECT count(*) FROM server_updates`).Scan(&n)
	for _, c := range fake.Calls() {
		if c.Args[0] == "run" {
			t.Errorf("ran %v", c)
		}
	}
	if n != 0 {
		t.Errorf("%d updates", n)
	}
	// A null version is the latest.
	if w := v1(h, "POST", "/update", personal, `{"version":null}`, nil); w.Code != 202 {
		t.Errorf("null = %d %s", w.Code, w.Body.String())
	}
}

// Check asks GitHub now.
func TestV1CheckRelease(t *testing.T) {
	a, _, _ := updatable(t)
	tag := "v0.4.5"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag == "" {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, `{"tag_name":"`+tag+`","html_url":"https://github.com/scttymn/houston/releases/tag/`+tag+`"}`)
	}))
	t.Cleanup(github.Close)
	a.Release = release.Checker{API: github.URL, Repo: "scttymn/houston", Version: "v0.4.2"}
	h := a.Handler()
	board, stop := a.Live.Listen(api.FlightBoard)
	defer stop()
	w := v1(h, "POST", "/update/check", personal, "", nil)
	if got := answer(t, w); w.Code != 200 || got["version"] != "v0.4.2" || got["latest"] != "v0.4.5" || got["message"] != "v0.4.5 is out." {
		t.Errorf("= %d %s", w.Code, w.Body.String())
	}
	select {
	case <-board:
	case <-time.After(time.Second):
		t.Error("the board wasn't told")
	}
	tag = "v0.4.2"
	if got := answer(t, v1(h, "POST", "/update/check", personal, "", nil)); got["latest"] != nil || got["message"] != "v0.4.2 is the latest." {
		t.Errorf("= %v", got)
	}
	tag = ""
	is(t, v1(h, "POST", "/update/check", personal, "", nil), 502, `{"error":"Couldn't reach GitHub just now; try again in a minute."}`)
	is(t, v1(h, "POST", "/update/check", "", "", nil), 401, `{"error":"the API token is missing, wrong or revoked"}`)
}

// Deploys and backups wait for the update.
func TestUpdateHolds(t *testing.T) {
	a, h, _ := updatable(t)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind) VALUES (1, 'nas', 'nfs')`)
	exec(t, a, `INSERT INTO server_updates (to_version, from_version, started_at) VALUES ('v0.4.3', 'v0.4.2', CURRENT_TIMESTAMP)`)
	queue(t, a, "shop", 1, "deploy", time.Now())
	if w := claim(h, `{"runner":"houston-runner-1","wait":0}`); w.Code != 204 {
		t.Errorf("claimed during an update: %d %s", w.Code, w.Body.String())
	}
	exec(t, a, `INSERT INTO backup_runs (id, project_id, location_id, kind, reason, heartbeat_at) VALUES (7, 1, 1, 'auto', 'manual', CURRENT_TIMESTAMP)`)
	run, _ := models.New(a.DB.Read).BackupRunByID(t.Context(), 7)
	claimRun := func() error {
		return a.DB.Tx(t.Context(), func(tx *db.Tx) error {
			_, err := models.ClaimRun(t.Context(), tx, run, time.Now())
			return err
		})
	}
	if err := claimRun(); err != models.ErrBusy {
		t.Errorf("a backup during an update: %v", err)
	}
	exec(t, a, `UPDATE server_updates SET status = 'go', finished_at = CURRENT_TIMESTAMP`)
	if w := claim(h, `{"runner":"houston-runner-1","wait":0}`); w.Code != 200 {
		t.Errorf("after: %d %s", w.Code, w.Body.String())
	}
}

func TestV1Port(t *testing.T) {
	a, h, fake := updatable(t)
	is(t, v1(h, "GET", "/port", personal, "", nil), 200, `{"open":true,"address":"0.0.0.0","saved":"open"}`)
	w := v1(h, "PUT", "/port", personal, `{"open":false}`, nil)
	if got := answer(t, w); w.Code != 202 || got["saved"] != "closed" || got["message"] != "Port 3000 is closing to the network. Mission Control restarts for a few seconds." {
		t.Errorf("= %d %s", w.Code, w.Body.String())
	}
	want := "run -d --rm --name houston-port-3000 --user 0 -e HOUSTON_BIND=127.0.0.1 -v /var/run/docker.sock:/var/run/docker.sock -v /opt/houston:/opt/houston:ro " +
		"--entrypoint sh houston/runner:local -c sleep 5 && exec docker compose -f /opt/houston/compose.yml up -d --no-deps mission-control"
	ran := fake.Ran()
	if ran[len(ran)-1] != want {
		t.Errorf("ran %q", ran)
	}
	w = v1(h, "PATCH", "/port", personal, `{"open":true}`, nil)
	if got := answer(t, w); w.Code != 202 || got["saved"] != "open" || fake.Ran()[len(fake.Ran())-1] != strings.Replace(want, "127.0.0.1", "0.0.0.0", 1) {
		t.Errorf("= %d %s", w.Code, w.Body.String())
	}

	is(t, v1(h, "PUT", "/port", personal, `{"open":"maybe"}`, nil), 400, `{"error":"open must be true or false"}`)
	is(t, v1(h, "PUT", "/port", "", `{"open":false}`, nil), 401, `{"error":"the API token is missing, wrong or revoked"}`)
	a.Port.RunnerImage = ""
	is(t, v1(h, "PUT", "/port", personal, `{"open":false}`, nil), 422,
		`{"error":"Houston can't change it from here yet: run the installer once more (it tells Mission Control the runner image to do it with)"}`)
	a.Port.RunnerImage = "houston/runner:local"
	fake.On(dockercmdtest.Fail(125, "Conflict. The container name \"/houston-port-3000\" is already in use\n"), "run")
	is(t, v1(h, "PUT", "/port", personal, `{"open":false}`, nil), 422,
		`{"error":"Houston can't change it right now: Conflict. The container name \"/houston-port-3000\" is already in use"}`)
	if got := answer(t, v1(h, "GET", "/port", personal, "", nil)); got["saved"] != "open" {
		t.Errorf("the choice changed: %v", got)
	}

	// Closed: bound to 127.0.0.1 (read afresh by a new process).
	closed := &dockercmdtest.Fake{}
	closed.On(dockercmdtest.OK(`{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"3000"}]}`), "inspect")
	a.Port = &port.Port{DB: a.DB, Own: owncontainer.Own{Docker: closed, Hostname: "mc"}}
	is(t, v1(a.Handler(), "GET", "/port", personal, "", nil), 200, `{"open":false,"address":"127.0.0.1","saved":"open"}`)
	unknown := &dockercmdtest.Fake{}
	unknown.On(dockercmdtest.Fail(1, "Cannot connect"), "inspect")
	a.Port = &port.Port{DB: a.DB, Own: owncontainer.Own{Docker: unknown, Hostname: "mc"}}
	is(t, v1(a.Handler(), "GET", "/port", personal, "", nil), 200, `{"open":null,"address":null,"saved":"open"}`)
}

// A project's logs: its running app container's, the last lines, or
// followed.
func TestV1Logs(t *testing.T) {
	_, h, fake := updatable(t)
	ps := []string{"ps", "--filter", "label=service=shop", "--filter", "label=role=web", "--format", "{{.Names}}"}
	is(t, v1(h, "GET", "/projects/shop/logs", personal, "", nil), 404, `{"error":"shop isn't running"}`)
	fake.On(dockercmdtest.OK("shop-web-abc123\nshop-web-old\n"), ps...)
	fake.On(dockercmdtest.OK("2026-09-30T10:00:00Z started\n"), "logs")
	w := v1(h, "GET", "/projects/shop/logs", personal, "", nil)
	if w.Code != 200 || w.Body.String() != "2026-09-30T10:00:00Z started\n" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("= %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	v1(h, "GET", "/projects/shop/logs?tail=5&follow=1", personal, "", nil)
	ran := fake.Ran()
	if ran[len(ran)-3] != "logs --timestamps --tail 200 shop-web-abc123" || ran[len(ran)-1] != "logs --timestamps --tail 5 --follow shop-web-abc123" {
		t.Errorf("ran %q", ran)
	}
	calls := fake.Calls()
	if calls[len(calls)-1].Timeout != time.Hour || calls[len(calls)-3].Timeout != 0 {
		t.Errorf("timeouts %v %v", calls[len(calls)-1].Timeout, calls[len(calls)-3].Timeout)
	}
	for _, tail := range []string{"0", "10001", "x", "-1", "1e3", "%2B5"} {
		is(t, v1(h, "GET", "/projects/shop/logs?tail="+tail, personal, "", nil), 422, `{"error":"tail must be 1–10000"}`)
	}
	is(t, v1(h, "GET", "/projects/nope/logs", personal, "", nil), 404, `{"error":"no project nope"}`)
}

// A project's view has its app's stats, read while someone looks.
func TestV1ProjectStats(t *testing.T) {
	_, h, fake := updatable(t)
	fake.On(dockercmdtest.OK(`{"ID":"w1","Name":"shop-web-`+sha1+`","CPUPerc":"12.50%","MemUsage":"300MiB / 1GiB"}`), "stats")
	fake.On(dockercmdtest.OK("w1 0 0"), "inspect", "--format", "{{slice .Id 0 12}} {{.HostConfig.Memory}} {{.HostConfig.NanoCpus}}")
	fake.On(dockercmdtest.OK(`[{"Name":"shop_data","Size":"1kB"}]`), "system")
	got := answer(t, v1(h, "GET", "/projects/shop", personal, "", nil))["stats"].(map[string]any)
	if got["cpu_cores"] != 0.125 || got["memory_bytes"] != float64(300<<20) || got["disk_bytes"] != float64(1000) || got["cpu_limit"] != nil {
		t.Errorf("stats %v", got)
	}
	list := answer(t, v1(h, "GET", "/projects", personal, "", nil))["projects"].([]any)
	if list[0].(map[string]any)["stats"] == nil {
		t.Errorf("list %v", list)
	}
}

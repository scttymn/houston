package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/app/api"
)

func claim(h http.Handler, body string) *httptest.ResponseRecorder {
	return post(h, "POST", "/api/runner/jobs/claim", body, nil)
}

// queue queues a deploy of project, created at when.
func queue(t *testing.T, a *app.App, project string, number int, kind string, when time.Time) int64 {
	t.Helper()
	res, err := a.DB.Write.Exec(`INSERT INTO deploys (project_id, number, kind, status, sha, ref, heartbeat_at, created_at)
		SELECT id, ?, ?, 'queued', ?, 'refs/heads/main', ?, ? FROM projects WHERE name = ?`, number, kind, sha1, when, when, project)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

type job struct {
	Deploy     map[string]any `json:"deploy"`
	Project    map[string]any `json:"project"`
	KnownHosts []string       `json:"known_hosts"`
}

func claimed(t *testing.T, w *httptest.ResponseRecorder) job {
	t.Helper()
	var j job
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &j) != nil {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	return j
}

// A runner asks for work: the oldest queued deploy it may have, with what
// it needs to fetch, test and deploy it; the runner is seen either way.
func TestClaim(t *testing.T) {
	a, h := synced(t)
	known := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(known, []byte("github.com ssh-ed25519 AAAAgithub\n"), 0o600)
	a.KnownHosts = known
	h = a.Handler()
	exec(t, a, `UPDATE projects SET repo_url = 'git@github.com:scttymn/shop.git', branch = 'main', compose_path = 'compose.yml', deploy_key_private = ?`, crypt.Of("-----KEY-----"))

	if w := claim(h, `{"runner":"houston-runner-1","wait":0}`); w.Code != 204 {
		t.Errorf("nothing queued: %d %s", w.Code, w.Body.String())
	}
	var seen time.Time
	a.DB.Read.QueryRow(`SELECT last_seen_at FROM runners WHERE name = 'houston-runner-1'`).Scan(&seen)
	if time.Since(seen) > time.Minute {
		t.Errorf("the runner wasn't seen: %v", seen)
	}

	board, stop := a.Live.Listen(api.FlightBoard)
	defer stop()
	id := queue(t, a, "shop", 1, "deploy", time.Now())
	exec(t, a, `UPDATE deploys SET fresh = TRUE WHERE id = ?`, id)
	j := claimed(t, claim(h, `{"runner":"houston-runner-2","wait":0}`))
	token, _ := j.Deploy["token"].(string)
	delete(j.Deploy, "token")
	b, _ := json.Marshal(j)
	want := `{"deploy":{"fresh":true,"generation":1,"id":1,"kind":"deploy","number":1,"previous_accessories":null,"previous_databases":null,` +
		`"previous_generation":null,"previous_volumes":null,"ref":"refs/heads/main","sha":"` + sha1 + `","took_over":null},` +
		`"project":{"compose_path":"compose.yml","deploy_key":"-----KEY-----","branch":"main","name":"shop","repo_url":"git@github.com:scttymn/shop.git"},` +
		`"known_hosts":["github.com ssh-ed25519 AAAAgithub"]}`
	if !jsonEqual(string(b), want) {
		t.Errorf("job\n %s\nwant\n %s", b, want)
	}
	d := deployRow(t, a, id)
	if d.Status != "in_flight" || d.Runner != "houston-runner-2" || !d.OwnedBy(token) || time.Since(d.HeartbeatAt) > time.Minute {
		t.Errorf("claimed row %+v", d)
	}
	if m := next(board); !strings.Contains(m, `action="refresh"`) {
		t.Errorf("the board: %q", m)
	}
	if w := claim(h, `{"runner":"houston-runner-1","wait":0}`); w.Code != 204 {
		t.Errorf("claimed twice: %d", w.Code)
	}
}

func jsonEqual(a, b string) bool {
	var x, y any
	json.Unmarshal([]byte(a), &x)
	json.Unmarshal([]byte(b), &y)
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}

// A project with a live deploy keeps its queued one back; a silent one is
// finished first and the queued one goes, saying which it took over. The
// oldest queued goes first; none while the registry is cleaned.
func TestClaimOrder(t *testing.T) {
	a, h := synced(t)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"name": "blog", "services": []string{"web"}, "variables": []any{}}), nil)
	live := deploy(t, a, "shop", 1, "deploy", "in_flight", 1, sha1, "t", 0)
	shopQueued := queue(t, a, "shop", 2, "deploy", time.Now().Add(-time.Hour))
	blogQueued := queue(t, a, "blog", 1, "deploy", time.Now())

	if j := claimed(t, claim(h, `{"runner":"houston-runner-1","wait":0}`)); j.Deploy["id"] != float64(blogQueued) {
		t.Errorf("claimed %v past shop's live deploy", j.Deploy["id"])
	}
	exec(t, a, `UPDATE deploys SET heartbeat_at = ? WHERE id = ?`, time.Now().Add(-3*time.Minute), live)
	exec(t, a, `UPDATE installations SET registry_cleanup_since = ?`, time.Now())
	if w := claim(h, `{"runner":"houston-runner-1","wait":0}`); w.Code != 204 {
		t.Errorf("while the registry is cleaned: %d", w.Code)
	}
	exec(t, a, `UPDATE installations SET registry_cleanup_since = NULL`)
	j := claimed(t, claim(h, `{"runner":"houston-runner-1","wait":0}`))
	if j.Deploy["id"] != float64(shopQueued) || j.Deploy["took_over"] != float64(1) {
		t.Errorf("claimed %v", j.Deploy)
	}
	if d := deployRow(t, a, live); d.Status != "no_go" || !strings.HasPrefix(d.Error, "abandoned: ") {
		t.Errorf("the silent one: %+v", d)
	}
}

// A restore builds the next generation beside the serving one, and is told
// what of the serving one to remove after its switch.
func TestClaimRestore(t *testing.T) {
	a, h := synced(t)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}, "services": []string{"web", "db", "cache"},
		"volumes": []map[string]string{{"name": "data", "path": "/data"}}, "databases": []map[string]string{{"service": "db", "image": "postgres:17"}}}), nil)
	exec(t, a, `UPDATE projects SET data_generation = 2`)
	queue(t, a, "shop", 1, "restore", time.Now())
	j := claimed(t, claim(h, `{"runner":"houston-runner-1","wait":0}`))
	if j.KnownHosts == nil {
		t.Error("known_hosts is null, not []")
	}
	b, _ := json.Marshal(j.Deploy)
	for _, want := range []string{`"kind":"restore"`, `"generation":3`, `"previous_generation":2`, `"previous_accessories":["shop-cache-g2","shop-db-g2"]`,
		`"previous_volumes":["shop.g2_data"]`, `"previous_databases":["shop-db-g2"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("no %s in %s", want, b)
		}
	}
}

// The claim waits up to wait seconds for work, and answers as soon as some
// is queued.
func TestClaimWaits(t *testing.T) {
	a, h := synced(t)
	go func() {
		time.Sleep(300 * time.Millisecond)
		queue(t, a, "shop", 1, "deploy", time.Now())
	}()
	began := time.Now()
	claimed(t, claim(h, `{"runner":"houston-runner-1","wait":5}`))
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("took %v", took)
	}
	began = time.Now()
	if w := claim(h, `{"runner":"houston-runner-1","wait":1}`); w.Code != 204 || time.Since(began) < time.Second {
		t.Errorf("= %d after %v", w.Code, time.Since(began))
	}
}

func TestClaimRefuses(t *testing.T) {
	_, h := synced(t)
	for _, body := range []string{`{"runner":"bob"}`, `{"runner":"houston-runner-1","wait":26}`, `{"runner":"houston-runner-1","wait":-1}`,
		`{"runner":"houston-runner-1","wait":"5"}`, `{"wait":5}`, `{"runner":"houston-runner-x"}`} {
		is(t, claim(h, body), 422, `{"error":"runner must be houston-runner-N and wait 0–25 seconds"}`)
	}
}

// A claim tells the deploy's page: in flight now, on its runner.
func TestClaimTellsThePage(t *testing.T) {
	a, h := synced(t)
	id := queue(t, a, "shop", 1, "deploy", time.Now())
	page, stop := a.Live.Listen(fmt.Sprintf("deploy:%d", id))
	defer stop()
	claimed(t, claim(h, `{"runner":"houston-runner-1","wait":0}`))
	select {
	case m := <-page:
		if !strings.Contains(m, `<turbo-stream action="replace" target="deploy_status">`) || !strings.Contains(m, `runner <span class="mono">houston-runner-1</span>`) {
			t.Errorf("the page got %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the page wasn't told")
	}
}

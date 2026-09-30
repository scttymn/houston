package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/app/api"
	"github.com/scttymn/houston/mission-control-go/test/testapp"
)

// runnerAPI is the app with setup finished (base domain svnmns.com, DNS by
// wildcard), and its handler.
func runnerAPI(t *testing.T) (*app.App, http.Handler) {
	t.Helper()
	a := testapp.New(t)
	exec(t, a, `INSERT INTO installations (id, base_domain, cloudflare_connected_at) VALUES (1, 'svnmns.com', ?)`, time.Now())
	return a, a.Handler()
}

func exec(t *testing.T, a *app.App, query string, args ...any) {
	t.Helper()
	if _, err := a.DB.Write.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// post sends body to path as the runner, with header's extra headers.
func post(h http.Handler, method, path, body string, header http.Header) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "172.18.0.1:40000"
	r.Header.Set("Authorization", "Bearer "+testapp.RunnerToken)
	r.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		r.Header[k] = v
	}
	h.ServeHTTP(w, r)
	return w
}

// payload is shop's compose.yml as houston deploy sends it, with fields
// changed or added.
func payload(changes map[string]any) string {
	p := map[string]any{"name": "shop", "app_service": "web", "services": []string{"web", "db"},
		"domains": []string{"shop.svnmns.com", "shop.example.com"}, "variables": []map[string]any{{"name": "SECRET_KEY_BASE", "required": true}},
		"health": "/up", "port": 3000}
	for k, v := range changes {
		if v == nil {
			delete(p, k)
		} else {
			p[k] = v
		}
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// answer is a JSON answer, decoded.
func answer(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON (%d): %q", w.Code, w.Body.String())
	}
	return m
}

// is checks w's status and JSON body.
func is(t *testing.T, w *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	var want map[string]any
	if err := json.Unmarshal([]byte(body), &want); err != nil {
		t.Fatal(err)
	}
	if got := answer(t, w); w.Code != code || !reflect.DeepEqual(got, want) {
		t.Errorf("= %d %s\nwant %d %s", w.Code, w.Body.String(), code, body)
	}
}

func secret(t *testing.T, a *app.App, project, key, value string) {
	t.Helper()
	exec(t, a, `INSERT INTO secrets (project_id, key, value) SELECT id, ?, ? FROM projects WHERE name = ?`, key, crypt.Of(value), project)
}

func hosts(t *testing.T, a *app.App, project string) string {
	t.Helper()
	rows, err := a.DB.Read.Query(`SELECT project_hosts.name FROM project_hosts JOIN projects ON projects.id = project_id WHERE projects.name = ? ORDER BY 1`, project)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	return strings.Join(names, " ")
}

func digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// deploy adds a deploy of project: kind, status, generation, sha, and the
// token that owns it; heard is how long ago it last reported.
func deploy(t *testing.T, a *app.App, project string, number int, kind, status string, generation int, sha, token string, heard time.Duration) int64 {
	t.Helper()
	res, err := a.DB.Write.Exec(`INSERT INTO deploys (project_id, number, kind, status, generation, sha, ref, token_digest, heartbeat_at)
		SELECT id, ?, ?, ?, ?, ?, 'refs/heads/main', ?, ? FROM projects WHERE name = ?`,
		number, kind, status, generation, sha, digest(token), time.Now().Add(-heard), project)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

const sha1 = "1111111111111111111111111111111111111111"

// A project is saved with the container names it owns even when its
// required secrets have no value (HOLD), so the admin can fill them in;
// then the sync goes on. Its default host isn't one of its custom domains,
// and those wait for its first GO.
func TestSync(t *testing.T) {
	a, h := runnerAPI(t)
	is(t, post(h, "POST", "/api/projects/sync", payload(nil), nil), 422,
		`{"error":"HOLD: set SECRET_KEY_BASE in Mission Control first","missing":["SECRET_KEY_BASE"]}`)
	if got := hosts(t, a, "shop"); got != "shop shop-db" {
		t.Errorf("hosts %q", got)
	}
	two := payload(map[string]any{"variables": []map[string]any{{"name": "A", "required": true}, {"name": "B", "required": true}, {"name": "C"}}})
	is(t, post(h, "POST", "/api/projects/sync", two, nil), 422, `{"error":"HOLD: set A and B in Mission Control first","missing":["A","B"]}`)

	secret(t, a, "shop", "SECRET_KEY_BASE", "s3cret")
	is(t, post(h, "POST", "/api/projects/sync", payload(nil), nil), 200, `{"project":"shop","host":"shop.svnmns.com","dns":"wildcard",
		"domains":{"shop.example.com":{"state":"AFTER FIRST GO","reason":"pointed here once a deploy of shop is GO"}},"generation":1}`)

	var services, domains, states string
	var port int
	a.DB.Read.QueryRow(`SELECT services, domains, domain_states, port FROM projects WHERE name = 'shop'`).Scan(&services, &domains, &states, &port)
	if services != `["web","db"]` || domains != `["shop.svnmns.com","shop.example.com"]` || port != 3000 ||
		states != `{"shop.example.com":{"state":"AFTER FIRST GO","reason":"pointed here once a deploy of shop is GO"}}` {
		t.Errorf("saved %s %s %s %d", services, domains, states, port)
	}

	// A maintenance page of 512 KB, which JSON escapes to about 3 MB.
	page := strings.Repeat("<", 512<<10)
	body, _ := json.Marshal(map[string]any{"name": "shop", "app_service": "web", "services": []string{"web", "db"}, "domains": []string{},
		"variables": []any{}, "health": "/up", "port": 3000, "maintenance_page": page})
	if w := post(h, "POST", "/api/projects/sync", string(body), nil); w.Code != 200 || len(body) < 3<<20 {
		t.Errorf("a big page (%d bytes): %d %s", len(body), w.Code, w.Body.String())
	}

	// A payload that isn't what Houston expects: 422, field by field.
	w := post(h, "POST", "/api/projects/sync", payload(map[string]any{"port": 0, "health": "up"}), nil)
	is(t, w, 422, `{"error":"compose.yml doesn't match what Houston expects","errors":{"port":["must be a port number"],"health":["must be a path starting with /"]}}`)
}

// Container names are claimed at sync: one another project owns refuses
// the sync, and nothing of it is saved; a service dropped lets its go.
func TestSyncContainerNames(t *testing.T) {
	a, h := runnerAPI(t)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"name": "shop-db", "services": []string{"web"}, "variables": []any{}}), nil)
	is(t, post(h, "POST", "/api/projects/sync", payload(nil), nil), 422,
		`{"error":"the container name shop-db belongs to project shop-db; rename a service or the project"}`)
	var n int
	a.DB.Read.QueryRow(`SELECT count(*) FROM projects WHERE name = 'shop'`).Scan(&n)
	if n != 0 {
		t.Error("the refused sync saved its project")
	}

	three := payload(map[string]any{"services": []string{"web", "cache", "queue"}, "variables": []any{}})
	post(h, "POST", "/api/projects/sync", three, nil)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"services": []string{"web", "queue"}, "variables": []any{}}), nil)
	if got := hosts(t, a, "shop"); got != "shop shop-queue" {
		t.Errorf("after dropping cache: %q", got)
	}
}

// A restore owns its project's config until it's done: a sync waits for a
// queued one or one heard from lately, and takes over a silent one.
func TestSyncWaitsForARestore(t *testing.T) {
	a, h := runnerAPI(t)
	body := payload(map[string]any{"variables": []any{}})
	post(h, "POST", "/api/projects/sync", body, nil)

	id := deploy(t, a, "shop", 3, "restore", "queued", 2, sha1, "", 0)
	is(t, post(h, "POST", "/api/projects/sync", body, nil), 409, `{"error":"restore #3 is queued; wait for it"}`)
	exec(t, a, `UPDATE deploys SET status = 'in_flight' WHERE id = ?`, id)
	is(t, post(h, "POST", "/api/projects/sync", body, nil), 409, `{"error":"restore #3 is in flight; wait for it"}`)
	exec(t, a, `UPDATE deploys SET heartbeat_at = ? WHERE id = ?`, time.Now().Add(-3*time.Minute), id)
	if w := post(h, "POST", "/api/projects/sync", body, nil); w.Code != 200 {
		t.Errorf("past a silent restore: %d %s", w.Code, w.Body.String())
	}
}

// A runner syncs for the deploy it claimed: the deploy must exist, be the
// runner's (its token), and be of the project compose.yml names.
func TestSyncClaimedDeploy(t *testing.T) {
	a, h := runnerAPI(t)
	body := payload(map[string]any{"variables": []any{}})
	post(h, "POST", "/api/projects/sync", body, nil)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"name": "other", "services": []string{"web"}, "variables": []any{}}), nil)
	id := deploy(t, a, "shop", 1, "deploy", "in_flight", 1, sha1, "the-token", 0)
	token := http.Header{"X-Houston-Deploy-Token": {"the-token"}}

	is(t, post(h, "POST", "/api/projects/sync", payload(map[string]any{"claimed_deploy": 999, "variables": []any{}}), token), 422, `{"error":"no such deploy"}`)
	is(t, post(h, "POST", "/api/projects/sync", payload(map[string]any{"claimed_deploy": id, "variables": []any{}}), http.Header{"X-Houston-Deploy-Token": {"wrong"}}),
		403, `{"error":"that token isn't this deploy's"}`)
	is(t, post(h, "POST", "/api/projects/sync", payload(map[string]any{"claimed_deploy": id, "name": "other", "services": []string{"web"}, "variables": []any{}}), token),
		422, `{"error":"compose.yml names project \"other\", but deploy #1 is for shop; nothing was synced"}`)
	if w := post(h, "POST", "/api/projects/sync", payload(map[string]any{"claimed_deploy": id, "variables": []any{}}), token); w.Code != 200 {
		t.Errorf("its own deploy: %d %s", w.Code, w.Body.String())
	}
}

// A restore's sync only checks its snapshot's compose.yml, and keeps it for
// the switch: nothing is stored on the project now.
func TestSyncRestoreCheck(t *testing.T) {
	a, h := runnerAPI(t)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}}), nil)
	restore := deploy(t, a, "shop", 2, "restore", "in_flight", 2, sha1, "restore-token", 0)
	plain := deploy(t, a, "shop", 1, "deploy", "go", 1, sha1, "deploy-token", 0)
	token := http.Header{"X-Houston-Deploy-Token": {"restore-token"}}
	check := payload(map[string]any{"restore_deploy": restore, "services": []string{"web", "db", "cache"}, "serving_generation": 1, "serving_sha": sha1})

	is(t, post(h, "POST", "/api/projects/sync", check, token), 422, `{"error":"HOLD: set SECRET_KEY_BASE in Mission Control first","missing":["SECRET_KEY_BASE"]}`)
	secret(t, a, "shop", "SECRET_KEY_BASE", "s3cret")
	is(t, post(h, "POST", "/api/projects/sync", check, token), 200, `{"project":"shop","host":"shop.svnmns.com","dns":"wildcard",
		"domains":{"shop.example.com":{"state":"AFTER FIRST GO","reason":"pointed here once a deploy of shop is GO"}},"generation":1}`)

	var kept string
	var services string
	a.DB.Read.QueryRow(`SELECT sync_payload FROM deploys WHERE id = ?`, restore).Scan(&kept)
	a.DB.Read.QueryRow(`SELECT services FROM projects WHERE name = 'shop'`).Scan(&services)
	if strings.Contains(kept, "restore_deploy") || strings.Contains(kept, "serving_") || !strings.Contains(kept, `"cache"`) {
		t.Errorf("kept %s", kept)
	}
	if services != `["web","db"]` {
		t.Errorf("the check changed the project: %s", services)
	}

	is(t, post(h, "POST", "/api/projects/sync", check, http.Header{"X-Houston-Deploy-Token": {"wrong"}}), 403, `{"error":"that token isn't this restore's"}`)
	otherProject := payload(map[string]any{"restore_deploy": restore, "name": "other", "services": []string{"web"}})
	is(t, post(h, "POST", "/api/projects/sync", otherProject, token), 422, `{"error":"no such restore"}`)
	notRestore := payload(map[string]any{"restore_deploy": plain})
	is(t, post(h, "POST", "/api/projects/sync", notRestore, http.Header{"X-Houston-Deploy-Token": {"deploy-token"}}), 422, `{"error":"no such restore"}`)
	exec(t, a, `UPDATE deploys SET status = 'no_go' WHERE id = ?`, restore)
	is(t, post(h, "POST", "/api/projects/sync", check, token), 409, `{"error":"restore #2 is no longer in flight"}`)
}

// A restore that switched but never said so (its runner died) leaves its
// generation serving and the project behind: the next sync that reads what
// serves moves the project forward to it, marks the restore switched, and
// applies the compose.yml it kept.
func TestSyncCatchesUpAGeneration(t *testing.T) {
	a, h := runnerAPI(t)
	own := []string{"shop.svnmns.com"} // no custom domains: Cloudflare isn't asked (1c)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}, "domains": own}), nil)
	restore := deploy(t, a, "shop", 2, "restore", "no_go", 2, sha1, "", 10*time.Minute)
	exec(t, a, `UPDATE deploys SET sync_payload = ? WHERE id = ?`, payload(map[string]any{"variables": []any{}, "domains": own, "port": 4000}), restore)

	body := payload(map[string]any{"variables": []any{}, "domains": own, "serving_generation": 2, "serving_sha": sha1})
	if w := post(h, "POST", "/api/projects/sync", body, nil); w.Code != 200 || answer(t, w)["generation"] != 2.0 {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	var switched bool
	a.DB.Read.QueryRow(`SELECT switched_at IS NOT NULL FROM deploys WHERE id = ?`, restore).Scan(&switched)
	if !switched {
		t.Error("the restore isn't marked switched")
	}
	if got := hosts(t, a, "shop"); got != "shop shop-db-g2" {
		t.Errorf("hosts %q", got) // the new generation's names, the old one's let go
	}

	// Never backward, and not for another commit.
	exec(t, a, `UPDATE projects SET data_generation = 3`)
	if w := post(h, "POST", "/api/projects/sync", body, nil); answer(t, w)["generation"] != 3.0 {
		t.Errorf("moved back: %s", w.Body.String())
	}
}

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

func testRunnerToken() string { return testapp.RunnerToken }

// A sync saves the project the board shows: it's told.
func TestSyncRefreshesTheBoard(t *testing.T) {
	a, h := runnerAPI(t)
	board, stop := a.Live.Listen(api.FlightBoard)
	defer stop()
	post(h, "POST", "/api/projects/sync", payload(nil), nil) // held: a secret's missing, but saved
	select {
	case <-board:
	case <-time.After(2 * time.Second):
		t.Fatal("the board wasn't told")
	}
}

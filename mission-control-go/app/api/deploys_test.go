package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/app/models"
)

// synced is the runner API with shop synced, and its handler.
func synced(t *testing.T) (*app.App, http.Handler) {
	t.Helper()
	a, h := runnerAPI(t)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}}), nil)
	return a, h
}

// started is a deploy start's answer.
type started struct {
	ID       int64  `json:"id"`
	Number   int64  `json:"number"`
	Token    string `json:"token"`
	TookOver *int64 `json:"took_over"`
}

func start(t *testing.T, h http.Handler, project string) started {
	t.Helper()
	w := post(h, "POST", "/api/projects/"+project+"/deploys", `{"sha":"`+sha1+`","ref":"refs/heads/main"}`, nil)
	var s started
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil || w.Code != 201 {
		t.Fatalf("start = %d %s", w.Code, w.Body.String())
	}
	return s
}

func deployRow(t *testing.T, a *app.App, id int64) models.Deploy {
	t.Helper()
	d, err := models.New(a.DB.Read).DeployByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// houston deploy starts the project's next deploy and holds its token; a
// live one is in the way (409 with its number), a silent one is taken over.
func TestStartDeploy(t *testing.T) {
	a, h := synced(t)
	first := start(t, h, "shop")
	if first.Number != 1 || len(first.Token) < 40 || first.TookOver != nil {
		t.Errorf("first %+v", first)
	}
	d := deployRow(t, a, first.ID)
	if d.Status != "in_flight" || d.Sha != sha1 || d.Ref != "refs/heads/main" || !d.OwnedBy(first.Token) || d.Generation != 1 || d.Runner != "" {
		t.Errorf("row %+v", d)
	}

	w := post(h, "POST", "/api/projects/shop/deploys", `{"sha":"`+sha1+`","ref":"refs/heads/main"}`, nil)
	heard := d.HeartbeatAt.UTC().Format(time.RFC3339)
	is(t, w, 409, `{"error":"deploy #1 is in flight (last heard from `+heard+`)","number":1}`)

	exec(t, a, `UPDATE deploys SET heartbeat_at = ? WHERE id = ?`, time.Now().Add(-3*time.Minute), first.ID)
	stale := deployRow(t, a, first.ID).HeartbeatAt.UTC().Format(time.RFC3339)
	second := start(t, h, "shop")
	if second.Number != 2 || second.TookOver == nil || *second.TookOver != 1 {
		t.Errorf("second %+v", second)
	}

	// A deploy builds the generation that serves.
	exec(t, a, `UPDATE deploys SET status = 'go' WHERE id = ?`, second.ID)
	exec(t, a, `UPDATE projects SET data_generation = 3`)
	if d := deployRow(t, a, start(t, h, "shop").ID); d.Generation != 3 {
		t.Errorf("generation %d", d.Generation)
	}
	if old := deployRow(t, a, first.ID); old.Status != "no_go" || !old.FinishedAt.Valid || old.Error != "abandoned: no word from houston deploy since "+stale {
		t.Errorf("the abandoned one %+v", old)
	}
}

// A deploy starts only for a synced project, with a full commit and a ref,
// and not while the registry is cleaned.
func TestStartDeployRefuses(t *testing.T) {
	a, h := synced(t)
	is(t, post(h, "POST", "/api/projects/nope/deploys", `{"sha":"`+sha1+`","ref":"main"}`, nil), 404, `{"error":"no project nope; houston deploy syncs it first"}`)
	for body, msg := range map[string]string{
		`{"sha":"abc","ref":"main"}`:                             "Sha must be a full commit SHA (40 lowercase hex characters)",
		`{"sha":"` + sha1 + `"}`:                                 "Ref can't be blank",
		`{"sha":"` + strings.Repeat("A", 40) + `","ref":"main"}`: "Sha must be a full commit SHA (40 lowercase hex characters)",
		`{"ref":"` + strings.Repeat("r", 256) + `"}`:             "Sha must be a full commit SHA (40 lowercase hex characters) and Ref is too long (maximum is 255 characters)",
		`{"sha":7,"ref":["main"]}`:                               "Sha must be a full commit SHA (40 lowercase hex characters) and Ref can't be blank",
	} {
		is(t, post(h, "POST", "/api/projects/shop/deploys", body, nil), 422, `{"error":`+jsonString(msg)+`}`)
	}
	exec(t, a, `UPDATE installations SET registry_cleanup_since = ?`, time.Now().Add(-10*time.Minute))
	is(t, post(h, "POST", "/api/projects/shop/deploys", `{"sha":"`+sha1+`","ref":"main"}`, nil), 409, `{"error":"Houston is cleaning its registry; try again in a minute"}`)
	exec(t, a, `UPDATE installations SET registry_cleanup_since = ?`, time.Now().Add(-31*time.Minute))
	start(t, h, "shop")
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

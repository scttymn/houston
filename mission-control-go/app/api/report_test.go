package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/api"
)

func report(h http.Handler, id int64, token, body string) *httptest.ResponseRecorder {
	return post(h, "PATCH", fmt.Sprintf("/api/deploys/%d", id), body, http.Header{"X-Houston-Deploy-Token": {token}})
}

// next is the next message on a live stream, or "" after a second.
func next(messages <-chan string) string {
	select {
	case m := <-messages:
		return m
	case <-time.After(2 * time.Second):
		return ""
	}
}

// houston deploy reports its steps, its log and how it ended; open pages
// are told: the deploy's gets the log, the flight board a refresh.
func TestReport(t *testing.T) {
	a, h := synced(t)
	board, stopBoard := a.Live.Listen(api.FlightBoard)
	defer stopBoard()
	s := start(t, h, "shop")
	if m := next(board); !strings.Contains(m, `action="refresh"`) {
		t.Errorf("the board after a start: %q", m)
	}
	page, stopPage := a.Live.Listen(fmt.Sprintf("deploy:%d", s.ID))
	defer stopPage()

	is(t, report(h, s.ID, s.Token, `{"step":"Build","log":"<b>built</b>\n"}`), 200, `{"number":1,"status":"in_flight"}`)
	// The log appended, and the head and steps again for the new step.
	if m := next(page); !strings.HasPrefix(m, `<turbo-stream action="append" target="deploy_log"><template>&lt;b&gt;built&lt;/b&gt;`+"\n"+`</template></turbo-stream>`) ||
		!strings.Contains(m, `<turbo-stream action="replace" target="deploy_status"><template><div id="deploy_status"`) ||
		!strings.Contains(m, `<li data-step="Build" data-state="current">`) {
		t.Errorf("the page got %q", m)
	}
	if m := next(board); !strings.Contains(m, `action="refresh"`) {
		t.Errorf("the board after a step: %q", m)
	}
	d := deployRow(t, a, s.ID)
	if d.Step != "Build" || d.Log != "<b>built</b>\n" || d.FinishedAt.Valid {
		t.Errorf("row %+v", d)
	}

	is(t, report(h, s.ID, s.Token, `{"log":"more\n"}`), 200, `{"number":1,"status":"in_flight"}`)
	if m := next(page); m != `<turbo-stream action="append" target="deploy_log"><template>more`+"\n"+`</template></turbo-stream>` { // the log alone
		t.Errorf("the page got %q", m)
	}
	is(t, report(h, s.ID, s.Token, `{"status":"no_go","error":"the health check failed"}`), 200, `{"number":1,"status":"no_go"}`)
	if m := next(page); !strings.Contains(m, `<div class="notice notice--nogo"><span class="mono">NO-GO</span><span class="notice__text">the health check failed</span></div>`) ||
		!strings.Contains(m, `data-state="failed"`) {
		t.Errorf("the page got %q", m)
	}
	if d := deployRow(t, a, s.ID); d.Log != "<b>built</b>\nmore\n" || !d.FinishedAt.Valid || d.Error != "the health check failed" {
		t.Errorf("finished %+v", d)
	}
	is(t, report(h, s.ID, s.Token, `{"log":"late"}`), 409, `{"error":"deploy #1 is no longer in flight (the health check failed); it was finished or taken over"}`)

	second := start(t, h, "shop")
	report(h, second.ID, second.Token, `{"status":"go"}`)
	is(t, report(h, second.ID, second.Token, `{"step":"x"}`), 409, `{"error":"deploy #2 is no longer in flight (go); it was finished or taken over"}`)
	is(t, report(h, second.ID, s.Token, `{"step":"x"}`), 403, `{"error":"that token isn't this deploy's"}`)
	is(t, report(h, 999, s.Token, `{"step":"x"}`), 404, `{"error":"no such deploy"}`)
}

// What a report may say, and in what words it's refused.
func TestReportRefuses(t *testing.T) {
	_, h := synced(t)
	s := start(t, h, "shop")
	for body, msg := range map[string]string{
		`{"status":"done"}`:                             "status must be go, no_go or hold",
		`{"status":null}`:                               "status must be go, no_go or hold",
		`{"status":"hold"}`:                             "hold comes with proposed_name, and proposed_name only with hold",
		`{"proposed_name":"shop-2"}`:                    "hold comes with proposed_name, and proposed_name only with hold",
		`{"status":"go","proposed_name":"shop-2"}`:      "hold comes with proposed_name, and proposed_name only with hold",
		`{"status":"hold","proposed_name":"Shop 2"}`:    "proposed_name must be a project name (a DNS label)",
		`{"step":"` + strings.Repeat("s", 101) + `"}`:   "step must be at most 100 characters",
		`{"step":7}`:                                    "step must be at most 100 characters",
		`{"error":"` + strings.Repeat("e", 1001) + `"}`: "error must be at most 1000 characters",
		`{"log":["a"]}`:                                 "log must be text",
	} {
		is(t, report(h, s.ID, s.Token, body), 422, `{"error":`+jsonString(msg)+`}`)
	}
	is(t, report(h, s.ID, s.Token, `{"status":"hold","proposed_name":"shop-2"}`), 200, `{"number":1,"status":"hold"}`)

	// A log chunk is at most 256 KiB; the body a little more.
	t2 := start(t, h, "shop")
	is(t, report(h, t2.ID, t2.Token, `{"log":"`+strings.Repeat("l", 256<<10+1)+`"}`), 413, `{"error":"a log chunk can be at most 256 KiB"}`)
	is(t, report(h, t2.ID, t2.Token, `{"log":"`+strings.Repeat("l", 256<<10)+`","error":"`+strings.Repeat("e", 5<<10)+`"}`), 413,
		`{"error":"the request is larger than 260 KiB"}`)
}

// Houston keeps the first 4 MiB of a deploy's log: a chunk that crosses it
// is cut (never mid-character) and marked; later ones are dropped.
func TestReportCapsTheLog(t *testing.T) {
	a, h := synced(t)
	s := start(t, h, "shop")
	exec(t, a, `UPDATE deploys SET log = ? WHERE id = ?`, strings.Repeat("x", 4<<20-4), s.ID)
	report(h, s.ID, s.Token, `{"log":"aéé"}`) // room for a (1), é (2) and half the next é
	is(t, report(h, s.ID, s.Token, `{"log":"dropped"}`), 200, `{"number":1,"status":"in_flight"}`)
	d := deployRow(t, a, s.ID)
	if tail := d.Log[4<<20-4:]; tail != "aé\n[log truncated: Houston keeps the first 4 MiB of a deploy's log]\n" {
		t.Errorf("the log ends %q", tail)
	}
}

// A restore's switch (its Clean up step, or a GO whose switch report was
// lost) moves the project to its generation and applies the compose.yml it
// kept, once.
func TestReportRestoreSwitch(t *testing.T) {
	a, h := synced(t)
	id := deploy(t, a, "shop", 2, "restore", "in_flight", 2, sha1, "rt", 0)
	exec(t, a, `UPDATE deploys SET sync_payload = ? WHERE id = ?`, payload(map[string]any{"variables": []any{}, "port": 4000}), id)
	var port, generation int
	project := func() {
		a.DB.Read.QueryRow(`SELECT port, data_generation FROM projects WHERE name = 'shop'`).Scan(&port, &generation)
	}

	report(h, id, "rt", `{"step":"Safety snapshot"}`)
	if project(); generation != 1 || deployRow(t, a, id).SwitchedAt.Valid {
		t.Errorf("before the switch: generation %d", generation)
	}
	is(t, report(h, id, "rt", `{"step":"Clean up"}`), 200, `{"number":2,"status":"in_flight"}`)
	if project(); generation != 2 || port != 4000 || !deployRow(t, a, id).SwitchedAt.Valid {
		t.Errorf("at the switch: generation %d, port %d", generation, port)
	}
	if got := hosts(t, a, "shop"); got != "shop shop-db-g2" {
		t.Errorf("hosts %q", got)
	}
	exec(t, a, `UPDATE projects SET port = 5000`)
	report(h, id, "rt", `{"status":"go"}`)
	if project(); port != 5000 {
		t.Error("the kept compose.yml was applied again")
	}

	// A GO whose switch report was lost switches it.
	lost := deploy(t, a, "shop", 3, "restore", "in_flight", 3, sha1, "lost", 0)
	report(h, lost, "lost", `{"status":"go"}`)
	if project(); generation != 3 || !deployRow(t, a, lost).SwitchedAt.Valid {
		t.Errorf("a GO without its switch: generation %d", generation)
	}
}

// A project's first GO points its names; later ones leave them to sync.
func TestReportFirstGoPointsDNS(t *testing.T) {
	a, h, f := throughCloudflare(t)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}}), nil)
	failed := start(t, h, "shop")
	report(h, failed.ID, failed.Token, `{"status":"no_go"}`)
	if len(f.Calls()) != 0 {
		t.Errorf("a NO-GO pointed names: %v", f.Calls())
	}
	s := start(t, h, "shop")
	report(h, s.ID, s.Token, `{"status":"go"}`)
	if got := names(f.Records("zbase")) + " | " + names(f.Records("zcom")); got != "shop.svnmns.com | shop.example.com" {
		t.Errorf("records %s", got)
	}
	var states string
	a.DB.Read.QueryRow(`SELECT domain_states FROM projects`).Scan(&states)
	if states != `{"shop.example.com":{"state":"DNS OK","reason":null}}` {
		t.Errorf("states %s", states)
	}
	calls := len(f.Calls())
	second := start(t, h, "shop")
	report(h, second.ID, second.Token, `{"status":"go"}`)
	if len(f.Calls()) != calls {
		t.Errorf("a second GO asked Cloudflare: %v", f.Calls()[calls:])
	}
}

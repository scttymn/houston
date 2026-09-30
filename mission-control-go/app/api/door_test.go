package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/test"
)

const runnerToken = "runner-token-0123456789abcdef0123456789"

// connected is an installation whose Cloudflare setup is done.
func connected(t *testing.T, d *db.DB) {
	t.Helper()
	if _, err := d.Write.Exec(`INSERT INTO installations (id, base_domain, cloudflare_connected_at) VALUES (1, 'svnmns.com', ?)`, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// probe is a route behind the door that reads a body of at most 1 KiB
// and answers it back.
func probe(d *db.DB, token string) http.Handler {
	rt := web.NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	rt.Scope("/api", Door{DB: d, RunnerToken: token}.Pipeline(), func(s *web.Scope) {
		s.Handle("POST /probe", func(w http.ResponseWriter, r *http.Request) error {
			body, err := readBody(r, 1024)
			if err != nil {
				return err
			}
			return web.JSON(w, 200, body)
		})
	})
	return rt.Handler()
}

func call(h http.Handler, header http.Header, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/probe", strings.NewReader(body))
	r.RemoteAddr = "172.18.0.1:40000"
	for k, v := range header {
		r.Header[k] = v
	}
	h.ServeHTTP(w, r)
	return w
}

func bearer(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }

// errorOf is a JSON answer's error.
func errorOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Error string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON (%d): %q", w.Code, w.Body.String())
	}
	return body.Error
}

// The runner API answers only the runner token, never through the tunnel,
// and only once setup has connected Cloudflare (the Rails app's
// Api::BaseController).
func TestDoor(t *testing.T) {
	d := test.DB(t)
	h := probe(d, runnerToken)

	// Through the tunnel: an empty 404, with the token or without (a
	// visitor never learns there's an API here).
	for _, header := range []string{"Cf-Ray", "Cf-Connecting-Ip"} {
		for _, hdr := range []http.Header{bearer(runnerToken), {}} {
			hdr.Set(header, "x")
			if w := call(h, hdr, `{}`); w.Code != 404 || w.Body.Len() > 0 {
				t.Errorf("with %s: %d %q", header, w.Code, w.Body.String())
			}
		}
	}

	// A missing, wrong or near-miss token: 401 JSON.
	for _, header := range []http.Header{nil, bearer("wrong"), bearer(runnerToken[:len(runnerToken)-1]), bearer(runnerToken + "x"),
		{"Authorization": {"Basic " + runnerToken}}} {
		if w := call(h, header, `{}`); w.Code != 401 || errorOf(t, w) != "the runner token is missing or wrong" {
			t.Errorf("%v: %d %q", header, w.Code, w.Body.String())
		}
	}
	// A server whose token is too short to trust answers nobody.
	short := probe(d, "short")
	if w := call(short, bearer("short"), `{}`); w.Code != 401 {
		t.Errorf("a short token: %d", w.Code)
	}

	// The right token before Cloudflare is connected: 409.
	if w := call(h, bearer(runnerToken), `{}`); w.Code != 409 || errorOf(t, w) != "finish setup in Mission Control first" {
		t.Errorf("before setup: %d %q", w.Code, w.Body.String())
	}
	// Setup begun (a base domain), Cloudflare not connected yet: still 409.
	if _, err := d.Write.Exec(`INSERT INTO installations (id, base_domain) VALUES (1, 'svnmns.com')`); err != nil {
		t.Fatal(err)
	}
	if w := call(h, bearer(runnerToken), `{}`); w.Code != 409 {
		t.Errorf("mid-setup: %d %q", w.Code, w.Body.String())
	}
	d.Write.Exec(`DELETE FROM installations`)
	connected(t, d)
	if w := call(h, bearer(runnerToken), `{"a":1}`); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"a":1}` {
		t.Errorf("the runner: %d %q", w.Code, w.Body.String())
	}

	// The body: over its limit 413, not a JSON object 400.
	for _, c := range []struct {
		body string
		code int
		msg  string
	}{
		{`{"a":"` + strings.Repeat("x", 1020) + `"}`, 413, "the request is larger than 1 KiB"},
		{`{"a":`, 400, "the request isn't JSON"},
		{``, 400, "the request isn't JSON"},
		{`[1,2]`, 400, "the request isn't JSON"},
		{`null`, 400, "the request isn't JSON"},
	} {
		if w := call(h, bearer(runnerToken), c.body); w.Code != c.code || errorOf(t, w) != c.msg {
			t.Errorf("%.20q: %d %q", c.body, w.Code, w.Body.String())
		}
	}
}

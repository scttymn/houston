package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/scttymn/houston/mission_control/app"
	"github.com/scttymn/houston/mission_control/app/models"
)

const personal = "hou_personal-token-for-tests"

// remote is the app with setup finished and a personal token, "laptop".
func remote(t *testing.T) (*app.App, http.Handler) {
	t.Helper()
	a, h := synced(t)
	exec(t, a, `INSERT INTO api_tokens (name, token_digest) VALUES ('laptop', ?)`, models.Digest(personal))
	return a, h
}

// v1 sends a request to the personal API with token (none: "").
func v1(h http.Handler, method, path, token, body string, header http.Header) *httptest.ResponseRecorder {
	if header == nil {
		header = http.Header{}
	}
	header.Set("Authorization", "")
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "/api/v1"+path, stringsReader(body))
	r.RemoteAddr = "203.0.113.7:40000"
	r.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		if v[0] == "" {
			r.Header.Del(k)
		} else {
			r.Header[k] = v
		}
	}
	h.ServeHTTP(w, r)
	return w
}

// The personal API answers a hou_ token, through the tunnel too, once
// setup is done; a token's use is recorded at most once a minute.
func TestV1Door(t *testing.T) {
	a, h := remote(t)
	for _, token := range []string{"", "hou_wrong", "not-hou_" + personal, personal[len("hou_"):], testRunnerToken()} {
		is(t, v1(h, "GET", "/me", token, "", nil), 401, `{"error":"the API token is missing, wrong or revoked"}`)
	}
	if w := v1(h, "GET", "/me", personal, "", http.Header{"Cf-Ray": {"8f-MCI"}, "Cf-Connecting-Ip": {"198.51.100.4"}}); w.Code != 200 {
		t.Errorf("through the tunnel: %d %s", w.Code, w.Body.String())
	}
	used := func() time.Time {
		var at time.Time
		a.DB.Read.QueryRow(`SELECT last_used_at FROM api_tokens`).Scan(&at)
		return at
	}
	first := used()
	if time.Since(first) > time.Minute {
		t.Errorf("last used %v", first)
	}
	exec(t, a, `UPDATE api_tokens SET last_used_at = ?`, time.Now().Add(-30*time.Second))
	half := used()
	v1(h, "GET", "/me", personal, "", nil)
	if !used().Equal(half) {
		t.Error("recorded twice in a minute")
	}
	exec(t, a, `UPDATE api_tokens SET last_used_at = ?`, time.Now().Add(-2*time.Minute))
	v1(h, "GET", "/me", personal, "", nil)
	if time.Since(used()) > 10*time.Second {
		t.Error("not recorded after a minute")
	}

	exec(t, a, `UPDATE installations SET cloudflare_connected_at = NULL`)
	is(t, v1(h, "GET", "/me", personal, "", nil), 409, `{"error":"finish setup in Mission Control first"}`)
}

// Which token, which server, which Houston, and a newer release.
func TestMe(t *testing.T) {
	for _, c := range []struct {
		version, latest, want string
	}{
		{"v0.4.27", "v0.4.28", `{"token":"laptop","server":"svnmns.com","version":"v0.4.27","latest":"v0.4.28","updating":null}`},
		{"v0.4.27", "v0.4.27", `{"token":"laptop","server":"svnmns.com","version":"v0.4.27","latest":null,"updating":null}`},
		{"v0.4.27", "v0.4.26", `{"token":"laptop","server":"svnmns.com","version":"v0.4.27","latest":null,"updating":null}`},
		{"v0.10.0", "v0.9.9", `{"token":"laptop","server":"svnmns.com","version":"v0.10.0","latest":null,"updating":null}`},
		{"dev", "v0.4.28", `{"token":"laptop","server":"svnmns.com","version":"dev","latest":null,"updating":null}`},
		{"source abc1234", "v0.4.28", `{"token":"laptop","server":"svnmns.com","version":"source abc1234","latest":null,"updating":null}`},
	} {
		a, _ := remote(t)
		a.Version = c.version
		exec(t, a, `UPDATE installations SET latest_release = ?`, c.latest)
		is(t, v1(a.Handler(), "GET", "/me", personal, "", nil), 200, c.want)
	}
}

// Which Houston this is, from what the image was built with.
func TestHoustonVersion(t *testing.T) {
	for _, c := range []struct{ tag, sha, want string }{
		{"v0.4.27", "", "v0.4.27"},
		{"v0.5.0-rc.1", "abcdef1", "v0.5.0-rc.1"},
		{"latest", "abcdef1234567", "source abcdef1"},
		{"", "xyz", "dev"},
		{"", "", "dev"},
	} {
		if got := app.HoustonVersion(c.tag, c.sha); got != c.want {
			t.Errorf("(%q, %q) = %q, want %q", c.tag, c.sha, got, c.want)
		}
	}
}

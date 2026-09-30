package app_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app"
)

// setUp gives the installation its base domain, as setup does.
func setUp(t *testing.T, a *app.App, base string) {
	t.Helper()
	if _, err := a.DB.Write.Exec(`INSERT INTO installations (id, base_domain) VALUES (1, ?)`, base); err != nil {
		t.Fatal(err)
	}
}

// request is method path on host, from peer, with header.
func request(h http.Handler, method, host, path, peer string, header http.Header) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, nil)
	r.Host, r.RemoteAddr = host, peer+":40000"
	for k, v := range header {
		r.Header[k] = v
	}
	h.ServeHTTP(w, r)
	return w
}

// hooks.<base> answers only its ping and the webhook (an unknown one an
// empty 404, as a push that doesn't verify); anything else there is an
// empty 404. Every other host is Mission Control.
func TestHosts(t *testing.T) {
	a := newApp(t)
	setUp(t, a, "Svnmns.com")
	h := a.Handler()
	for _, c := range []struct {
		method, host, path string
		code               int
		body               string
	}{
		{"GET", "hooks.svnmns.com", "/ping", 200, a.Identity},
		{"POST", "hooks.svnmns.com", "/no-such-project", 404, ""},
		{"GET", "hooks.svnmns.com", "/", 404, ""},
		{"GET", "hooks.svnmns.com", "/sign-in", 404, ""},
		{"GET", "HOOKS.svnmns.com:443", "/assets/x", 404, ""},
		{"POST", "hooks.svnmns.com", "/a/b", 404, ""},
		{"GET", "admin.svnmns.com", "/ping", 200, a.Identity},
		{"GET", "admin.svnmns.com", "/", 302, `<a href="/sign-in">Found</a>`},
		{"GET", "192.168.0.56", "/", 302, `<a href="/sign-in">Found</a>`},
	} {
		w := request(h, c.method, c.host, c.path, "192.168.0.9", nil)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.body) || c.body == "" && w.Body.Len() > 0 {
			t.Errorf("%s %s%s = %d %q, want %d %q", c.method, c.host, c.path, w.Code, w.Body.String(), c.code, c.body)
		}
	}

	// Before setup there's no base domain: no host is the hooks host.
	fresh := newApp(t)
	if w := request(fresh.Handler(), "GET", "hooks.", "/", "192.168.0.9", nil); w.Code != 302 {
		t.Errorf("hooks. before setup = %d", w.Code)
	}
}

// A request comes from its peer, or through the tunnel from cloudflared,
// whose word for the visitor (Cf-Connecting-Ip) and the scheme is taken.
// Anyone else's forwarding headers are ignored: X-Forwarded-For, a forged
// Cf-Connecting-Ip, X-Forwarded-Proto, and a host named in X-Forwarded-Host
// or Forwarded, even through the tunnel (Cloudflare passes a visitor's own
// on).
func TestForwarded(t *testing.T) {
	a := newApp(t)
	a.TunnelHost = "172.18.0.3" // cloudflared's name, here its address
	setUp(t, a, "svnmns.com")

	rt := a.Router()
	rt.Handle("GET /who", func(w http.ResponseWriter, r *http.Request) error {
		fmt.Fprintf(w, "%s %s %s", web.ClientIP(r), web.Scheme(r), web.RequestHost(r))
		return nil
	})
	who := rt.Handler()
	tunnel := http.Header{"Cf-Ray": {"8f-MCI"}, "Cf-Connecting-Ip": {"203.0.113.1"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-For": {"6.6.6.6"}}
	lan := http.Header{"Cf-Connecting-Ip": {"6.6.6.6"}, "X-Forwarded-For": {"6.6.6.7"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"evil.example"}}
	for _, c := range []struct {
		name, peer string
		header     http.Header
		want       string
	}{
		{"through the tunnel", "172.18.0.3", tunnel, "203.0.113.1 https admin.svnmns.com"},
		{"on the network, forged", "192.168.0.5", lan, "192.168.0.5 http admin.svnmns.com"},
		{"on the server (the Docker gateway)", "172.18.0.1", lan, "172.18.0.1 http admin.svnmns.com"},
		{"loopback", "127.0.0.1", lan, "127.0.0.1 http admin.svnmns.com"},
	} {
		if got := request(who, "GET", "admin.svnmns.com", "/who", c.peer, c.header).Body.String(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	// With no tunnel named, no one is trusted: not the private networks.
	none := newApp(t)
	nobody := none.Router()
	nobody.Handle("GET /who", func(w http.ResponseWriter, r *http.Request) error {
		fmt.Fprint(w, web.ClientIP(r))
		return nil
	})
	if got := request(nobody.Handler(), "GET", "admin.svnmns.com", "/who", "172.18.0.3", tunnel).Body.String(); got != "172.18.0.3" {
		t.Errorf("no tunnel named: %q", got)
	}

	// A host named in a header doesn't move a request off hooks.<base>.
	h := a.Handler()
	for _, header := range []http.Header{{"X-Forwarded-Host": {"admin.svnmns.com"}}, {"Forwarded": {"host=admin.svnmns.com"}}} {
		for _, peer := range []string{"192.168.0.5", "172.18.0.3"} {
			header := header.Clone()
			header.Set("Cf-Connecting-Ip", "203.0.113.1")
			if w := request(h, "GET", "hooks.svnmns.com", "/", peer, header); w.Code != 404 || w.Body.Len() > 0 {
				t.Errorf("%v from %s: %d %q", header, peer, w.Code, w.Body.String())
			}
		}
	}
}

// /up is 200 for health checks; /ping is the installation's identity, plain
// text and no cookie, derived one way from SECRET_KEY_BASE as the Rails
// app derives it (Installation.identity), so a server's answer is the same
// after the switch.
func TestUpAndPing(t *testing.T) {
	a := newApp(t)
	if w := get(t, a.Handler(), "/up"); w.Code != 200 {
		t.Errorf("/up = %d", w.Code)
	}
	// What the Rails app answers with SECRET_KEY_BASE "test-secret-key-base"
	// (bin/rails runner 'puts Installation.identity').
	if got, want := app.Identity("test-secret-key-base"), railsIdentity; got != want {
		t.Errorf("identity %q, want the Rails app's %q", got, want)
	}
	a.Identity = app.Identity("test-secret-key-base")
	w := get(t, a.Handler(), "/ping")
	if w.Code != 200 || w.Body.String() != railsIdentity || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("/ping = %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if c := w.Header().Values("Set-Cookie"); len(c) > 0 {
		t.Errorf("/ping set cookies %v", c)
	}
}

const railsIdentity = "2ca843a56248c803f1f177acd92920d8"

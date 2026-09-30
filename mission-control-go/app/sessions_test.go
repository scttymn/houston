package app_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/testkit"
	"golang.org/x/crypto/bcrypt"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare/cloudflaretest"
	"github.com/scttymn/houston/mission-control-go/app/services/systemstatus"
)

const password = "correct horse battery"

// browser is someone's browser: its cookies, its address, and whether it
// comes through the tunnel.
type browser struct {
	t      *testing.T
	h      http.Handler
	jar    []*http.Cookie
	addr   string
	tunnel bool
}

// admin is the app with its admin, and a browser on the network.
func admin(t *testing.T) (*app.App, *browser) {
	t.Helper()
	a := newApp(t)
	digest, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if _, err := a.DB.Write.Exec(`INSERT INTO users (email_address, password_digest) VALUES ('one@example.com', ?)`, string(digest)); err != nil {
		t.Fatal(err)
	}
	return a, &browser{t: t, h: a.Handler(), addr: "192.168.0.20"}
}

func (b *browser) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, "http://mc.local"+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.Header.Set("Accept", "text/html")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.RemoteAddr = b.addr + ":40000"
	if b.tunnel {
		r.Header.Set("Cf-Ray", "8f-MCI")
	}
	for _, c := range b.jar {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	b.h.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		kept := b.jar[:0]
		for _, old := range b.jar {
			if old.Name != c.Name {
				kept = append(kept, old)
			}
		}
		b.jar = kept
		if c.MaxAge >= 0 {
			b.jar = append(b.jar, c)
		}
	}
	return w
}

func (b *browser) signIn(email, pw string) *httptest.ResponseRecorder {
	return b.do("POST", "/sign-in", url.Values{"email_address": {email}, "password": {pw}})
}

func location(w *httptest.ResponseRecorder) string { return w.Header().Get("Location") }

func count(t *testing.T, a *app.App, table string) int {
	var n int
	a.DB.Read.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
	return n
}

// Signing in and out: every page asks for it, a wrong password says so,
// and signing out ends the session.
func TestSignIn(t *testing.T) {
	a, b := admin(t)
	if w := b.do("GET", "/", nil); w.Code != 302 || location(w) != "/sign-in" {
		t.Fatalf("a page signed out = %d %v", w.Code, w.Header())
	}
	w := b.do("GET", "/sign-in", nil)
	page := w.Body.String()
	if w.Code != 200 || !strings.Contains(page, `<title>Sign in · Mission Control</title>`) || !strings.Contains(page, `action="/sign-in"`) ||
		strings.Contains(page, "topbar") {
		t.Fatalf("the sign-in page = %d\n%s", w.Code, page)
	}
	testkit.Links(t, b.h, testkit.Page{Path: "/sign-in"}) // the stylesheet, its fonts, the scripts and pictures load
	if w := b.do("GET", "/session/new", nil); w.Code != 301 || location(w) != "/sign-in" {
		t.Errorf("session/new = %d %v", w.Code, w.Header())
	}

	if w := b.signIn("one@example.com", "wrong"); location(w) != "/sign-in" {
		t.Fatalf("a wrong password = %d %v", w.Code, w.Header())
	}
	if page := b.do("GET", "/sign-in", nil).Body.String(); !strings.Contains(page, `<div class="notice notice--nogo" role="alert">Try another email address or password.</div>`) {
		t.Errorf("no alert:\n%s", page)
	}
	if w := b.signIn(" ONE@example.com ", password); w.Code != 302 || location(w) != "/" {
		t.Fatalf("sign in = %d %v", w.Code, w.Header())
	}
	c := b.jar[len(b.jar)-1]
	if c.Name != "session_id" || !c.HttpOnly || c.MaxAge != int(app.SessionLifetime.Seconds()) {
		t.Errorf("cookie %+v", c)
	}
	page = b.do("GET", "/", nil).Body.String()
	if !strings.Contains(page, `class="topbar"`) || !strings.Contains(page, `<button class="topbar__signout" type="submit">Sign out</button>`) {
		t.Fatalf("the page signed in:\n%s", page)
	}

	if w := b.do("POST", "/session", url.Values{"_method": {"delete"}}); w.Code != 303 || location(w) != "/sign-in" || count(t, a, "sessions") != 0 {
		t.Fatalf("sign out = %d %v, %d sessions", w.Code, w.Header(), count(t, a, "sessions"))
	}
	if w := b.do("GET", "/", nil); w.Code != 302 {
		t.Error("still signed in")
	}
}

// A session works only the way it was made: a cookie from plain HTTP on the
// network can't be replayed through the tunnel.
func TestSessionBound(t *testing.T) {
	_, b := admin(t)
	b.signIn("one@example.com", password)
	b.tunnel = true
	if w := b.do("GET", "/", nil); w.Code != 302 {
		t.Fatal("a direct session worked through the tunnel")
	}
	b.tunnel = false
	if w := b.do("GET", "/", nil); w.Code != 200 {
		t.Fatal("it stopped working directly")
	}
}

// A session ends 30 days after signing in, or after 2 weeks unused.
func TestSessionEnds(t *testing.T) {
	a, b := admin(t)
	b.signIn("one@example.com", password)
	a.DB.Write.Exec(`UPDATE sessions SET created_at = ?, last_seen_at = ?`, time.Now().Add(-29*24*time.Hour), time.Now().Add(-13*24*time.Hour))
	if w := b.do("GET", "/", nil); w.Code != 200 {
		t.Fatal("ended early")
	}
	a.DB.Write.Exec(`UPDATE sessions SET created_at = ?`, time.Now().Add(-31*24*time.Hour))
	if w := b.do("GET", "/", nil); w.Code != 302 || count(t, a, "sessions") != 0 {
		t.Fatal("lasted past 30 days")
	}
	b.signIn("one@example.com", password)
	a.DB.Write.Exec(`UPDATE sessions SET last_seen_at = ?`, time.Now().Add(-15*24*time.Hour))
	if w := b.do("GET", "/", nil); w.Code != 302 {
		t.Fatal("lasted 2 weeks unused")
	}
}

// From one address, 10 tries in 3 minutes; through the tunnel, 50 failures
// from every address together. The server itself is never capped that way.
func TestSignInLimits(t *testing.T) {
	_, b := admin(t)
	for range 10 {
		b.signIn("one@example.com", "wrong")
	}
	if w := b.signIn("one@example.com", password); location(w) != "/sign-in" {
		t.Fatal("an 11th try from one address")
	}
	b.tunnel = true
	for i := range 50 {
		b.addr = "203.0.113." + strconv.Itoa(1+i/10)
		b.signIn("one@example.com", "wrong")
	}
	b.addr = "198.51.100.7"
	if w := b.signIn("one@example.com", password); location(w) != "/sign-in" {
		t.Fatal("signed in through the tunnel past 50 failures")
	}
	if page := b.do("GET", "/sign-in", nil).Body.String(); !strings.Contains(page, "Try again later.") {
		t.Errorf("no word:\n%s", page)
	}
	b.tunnel, b.addr = false, "127.0.0.1"
	if w := b.signIn("one@example.com", password); location(w) != "/" {
		t.Fatalf("the server itself was capped: %v", w.Header())
	}
}

// Once setup is done, the top bar shows the systems: the tunnel's
// connections, the runners heard from, the registry, the time in
// Houston's zone. Before, none.
func TestHeaderStatus(t *testing.T) {
	a, b := admin(t)
	b.signIn("one@example.com", password)
	if page := b.do("GET", "/", nil).Body.String(); strings.Contains(page, "TUNNEL") {
		t.Fatalf("status before setup:\n%s", page)
	}
	fake := cloudflaretest.New(t)
	fake.TunnelDetails("acct", "tun", map[string]any{"connections": []map[string]any{{"colo_name": "mci01"}, {"colo_name": "dfw08"}}})
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	t.Cleanup(registry.Close)
	a.SystemStatus = &systemstatus.Status{Cloudflare: fake.URL, Registry: registry.URL}
	a.DB.Write.Exec(`INSERT INTO installations (id, base_domain, time_zone, cloudflare_account_id, tunnel_id, cloudflare_api_token, cloudflare_connected_at)
		VALUES (1, 'svnmns.com', 'America/Denver', 'acct', 'tun', ?, CURRENT_TIMESTAMP)`, crypt.Of("cf-token"))
	b.h = a.Handler()
	if page := b.do("GET", "/", nil).Body.String(); strings.Contains(page, "TUNNEL") { // connected, but no storage yet
		t.Fatalf("status before setup's storage step:\n%s", page)
	}
	a.DB.Write.Exec(`INSERT INTO storage_locations (name, kind, is_default, acknowledged_at) VALUES ('nas', 'nfs', TRUE, CURRENT_TIMESTAMP)`)
	a.DB.Write.Exec(`INSERT INTO runners (name, last_seen_at) VALUES ('houston-runner-1', ?), ('houston-runner-2', ?), ('houston-runner-3', ?)`,
		time.Now(), time.Now().Add(-30*time.Second), time.Now().Add(-2*time.Minute))
	b.h = a.Handler()
	if page := b.do("GET", "/", nil).Body.String(); !strings.Contains(page, "TUNNEL") || strings.Contains(page, "RUNNERS") {
		t.Errorf("runners shown without a count to expect, or no status:\n%s", page)
	}
	a.Runners = 3
	b.h = a.Handler()
	page := b.do("GET", "/", nil).Body.String()
	denver, _ := time.LoadLocation("America/Denver")
	for _, want := range []string{
		`<span>TUNNEL <span class="status__go">GO</span></span>`,
		`<span>RUNNERS 2/3 <span class="status__nogo">NO-GO</span></span>`,
		`<span>REGISTRY <span class="status__go">GO</span></span>`,
		`<span class="status__clock">` + time.Now().In(denver).Format("15:04 MST") + `</span>`,
		`<span>Version</span><span class="mono">dev</span>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("no %s in\n%s", want, page)
		}
	}
}

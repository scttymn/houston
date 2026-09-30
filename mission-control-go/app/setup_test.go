package app_test

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare/cloudflaretest"
)

// firstRun is a server fresh from the installer: no admin, a code printed,
// and a browser on the network.
func firstRun(t *testing.T) (*app.App, *browser, string) {
	t.Helper()
	a := newApp(t)
	code, err := models.IssueSetupCode(context.Background(), a.DB)
	if err != nil {
		t.Fatal(err)
	}
	return a, &browser{t: t, h: a.Handler(), addr: "192.168.0.20"}, code
}

func adminForm(code, email, pw, confirmation string) url.Values {
	return url.Values{"setup[code]": {code}, "setup[email_address]": {email}, "setup[password]": {pw}, "setup[password_confirmation]": {confirmation}}
}

// The code: 8 of the letters and digits that can't be misread, shown in
// two halves; a new one replaces the last; none once the admin exists.
func TestSetupCode(t *testing.T) {
	a, _, first := firstRun(t)
	if !regexp.MustCompile(`^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$`).MatchString(first) {
		t.Errorf("code %q", first)
	}
	second, err := models.IssueSetupCode(context.Background(), a.DB)
	if err != nil || second == first || count(t, a, "setup_codes") != 1 {
		t.Fatalf("a second code = %q %v, %d kept", second, err, count(t, a, "setup_codes"))
	}
	var digest string
	a.DB.Read.QueryRow(`SELECT code_digest FROM setup_codes`).Scan(&digest)
	if strings.Contains(digest, strings.ReplaceAll(second, "-", "")) {
		t.Error("the code kept as it is")
	}
	_, err = models.Admin{Code: first, EmailAddress: "one@example.com", Password: password, PasswordConfirmation: password}.Save(context.Background(), a.DB, time.Now())
	if err == nil {
		t.Error("the replaced code still works")
	}
	must(t, a, `INSERT INTO users (email_address, password_digest) VALUES ('one@example.com', 'x')`)
	if _, err := models.IssueSetupCode(context.Background(), a.DB); !errors.Is(err, models.ErrSetUp) {
		t.Errorf("a code once set up: %v", err)
	}
}

// Until the admin exists every page is setup's, sign-in too; the admin
// is made with the code, signed in, and taken to the next step.
func TestSetupAdmin(t *testing.T) {
	a, b, code := firstRun(t)
	for _, path := range []string{"/", "/sign-in", "/settings", "/projects/shop"} {
		if w := b.do("GET", path, nil); w.Code != 302 || location(w) != "/setup" {
			t.Errorf("GET %s before setup = %d %q", path, w.Code, location(w))
		}
	}
	page := b.do("GET", "/setup", nil).Body.String()
	contains(t, page, "<title>First-run setup · Mission Control</title>", `<header class="topbar topbar--setup">`,
		`<span class="mono topbar__label">FIRST-RUN SETUP</span>`,
		`<li class="steps__step is-current" aria-current="step"><span class="mono steps__number">01</span> <span class="mono steps__name">ADMIN ACCOUNT</span></li>`,
		`<li class="steps__line" aria-hidden="true"></li> <li class="steps__step"><span class="mono steps__number">02</span> <span class="mono steps__name">CLOUDFLARE</span></li>`,
		`<span class="mono topbar__meta">mc.local</span>`, `<form class="setup__form" action="/setup" accept-charset="UTF-8" method="post">`,
		`name="setup[code]" id="setup_code"`, `aria-invalid="false"`,
		"Houston is running.\nFinish setup at  http://mc.local\nSetup code       <span class=\"terminal__code\">XXXX-XXXX</span>")
	if strings.Contains(page, `class="topbar"`) || strings.Contains(page, "Sign out") {
		t.Error("the top bar during setup")
	}

	// Wrong: every field says why, and what was typed stays but passwords.
	w := b.do("POST", "/setup", adminForm("AAAA-AAAA", "not an email", "short", "other"))
	if w.Code != 422 {
		t.Fatalf("a wrong form = %d", w.Code)
	}
	contains(t, w.Body.String(), `<span class="field__error">Setup code doesn&#39;t match the one the installer printed.</span>`,
		`<span class="field__error">Email doesn&#39;t look like an email address.</span>`,
		`<span class="field__error">Password needs at least 12 characters and doesn&#39;t match.</span>`,
		`aria-invalid="true" type="text" value="AAAA-AAAA" name="setup[code]"`, `value="not an email" name="setup[email_address]"`)
	if strings.Contains(w.Body.String(), `value="short"`) || count(t, a, "users") != 0 {
		t.Error("a password shown again, or an admin made")
	}
	for pw, short := range map[string]bool{"elevenchars": true, "twelve chars": false} {
		if page := b.do("POST", "/setup", adminForm("AAAA-AAAA", "one@example.com", pw, pw)).Body.String(); strings.Contains(page, "needs at least 12") != short {
			t.Errorf("%d characters: short = %v", len(pw), !short)
		}
	}
	contains(t, b.do("POST", "/setup", adminForm(code, "", password, password)).Body.String(),
		`<span class="field__error">Email can&#39;t be blank and doesn&#39;t look like an email address.</span>`)

	// Right: the code's case and dash don't matter; it's used up.
	w = b.do("POST", "/setup", adminForm(strings.ToLower(strings.ReplaceAll(code, "-", "")), "One@Example.com", password, password))
	if w.Code != 302 || location(w) != "/" {
		t.Fatalf("setup = %d %q\n%s", w.Code, location(w), w.Body.String())
	}
	if count(t, a, "setup_codes") != 0 || count(t, a, "users") != 1 {
		t.Errorf("%d codes, %d users", count(t, a, "setup_codes"), count(t, a, "users"))
	}
	if w := b.do("GET", "/", nil); location(w) != "/setup/cloudflare" {
		t.Errorf("after the admin = %d %q", w.Code, location(w))
	}
	if w := b.do("GET", "/setup", nil); location(w) != "/" {
		t.Errorf("setup again, signed in = %d %q", w.Code, location(w))
	}

	// Closed: signed out, it's sign-in, which is open now.
	b.do("POST", "/session", url.Values{"_method": {"delete"}})
	for _, method := range []string{"GET", "POST"} {
		if w := b.do(method, "/setup", adminForm(code, "two@example.com", password, password)); location(w) != "/sign-in" {
			t.Errorf("%s /setup set up = %d %q", method, w.Code, location(w))
		}
	}
	if w := b.do("GET", "/sign-in", nil); w.Code != 200 {
		t.Errorf("sign-in = %d", w.Code)
	}
	if w := b.signIn("one@example.com", password); location(w) != "/" {
		t.Errorf("signing in = %q", location(w))
	}
	if count(t, a, "users") != 1 {
		t.Error("a second admin")
	}
}

// Two setups at once with the same code: one admin.
func TestSetupOnce(t *testing.T) {
	a, _, code := firstRun(t)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = models.Admin{Code: code, EmailAddress: "admin" + string(rune('a'+i)) + "@example.com", Password: password,
				PasswordConfirmation: password}.Save(context.Background(), a.DB, time.Now())
		})
	}
	wg.Wait()
	if n := count(t, a, "users"); n != 1 {
		t.Fatalf("%d admins (%v)", n, errs)
	}
}

// Ten tries in 3 minutes an address.
func TestSetupLimit(t *testing.T) {
	_, b, _ := firstRun(t)
	for range 10 {
		if w := b.do("POST", "/setup", adminForm("AAAA-AAAA", "one@example.com", password, password)); w.Code != 422 {
			t.Fatalf("a try = %d", w.Code)
		}
	}
	if w := b.do("POST", "/setup", adminForm("AAAA-AAAA", "one@example.com", password, password)); w.Code != 429 || w.Body.Len() != 0 {
		t.Errorf("the 11th = %d %q", w.Code, w.Body.String())
	}
	b.addr = "192.168.0.21"
	if w := b.do("POST", "/setup", adminForm("AAAA-AAAA", "one@example.com", password, password)); w.Code != 422 {
		t.Errorf("another address = %d", w.Code)
	}
}

// Signed in, each unfinished step in turn; then the board.
func TestSetupNextStep(t *testing.T) {
	a, b := signedIn(t)
	must(t, a, `INSERT INTO installations (id, base_domain) VALUES (1, 'svnmns.com')`)
	for _, path := range []string{"/", "/settings", "/link"} {
		if w := b.do("GET", path, nil); location(w) != "/setup/cloudflare" {
			t.Errorf("GET %s = %d %q", path, w.Code, location(w))
		}
	}
	if w := b.do("POST", "/projects/shop/deploys", nil); location(w) != "/setup/cloudflare" {
		t.Errorf("a form = %d %q", w.Code, location(w))
	}
	must(t, a, `UPDATE installations SET cloudflare_connected_at = CURRENT_TIMESTAMP`)
	if w := b.do("GET", "/", nil); location(w) != "/setup/storage" {
		t.Errorf("connected = %d %q", w.Code, location(w))
	}
	must(t, a, `INSERT INTO storage_locations (name, kind, is_default) VALUES ('nas', 'nfs', TRUE)`)
	if w := b.do("GET", "/", nil); location(w) != "/setup/storage" {
		t.Errorf("its password not confirmed = %d %q", w.Code, location(w))
	}
	must(t, a, `UPDATE storage_locations SET acknowledged_at = CURRENT_TIMESTAMP`)
	reachable(t, a)
	if w := b.do("GET", "/", nil); w.Code != 200 {
		t.Errorf("set up = %d %q", w.Code, location(w))
	}
	// Signing in and out is never a step's.
	if w := b.do("GET", "/sign-in", nil); w.Code != 200 {
		t.Errorf("sign-in = %d", w.Code)
	}
}

// Step 2: the base domain and the token; its checks when it fails, and on
// to storage when it passes. Closed once connected.
func TestSetupCloudflare(t *testing.T) {
	a, b := signedIn(t)
	fake := cloudflaretest.New(t)
	fake.Account("acct", "Seven Moons")
	fake.Zone("zbase", "svnmns.com", "active")
	a.Cloudflare = fake.URL
	b.h = a.Handler()
	page := b.do("GET", "/setup/cloudflare", nil).Body.String()
	contains(t, page, "<title>Connect Cloudflare · Mission Control</title>",
		`<li class="steps__step is-done"><span class="mono steps__number">GO</span> <span class="mono steps__name">ADMIN ACCOUNT</span></li>`,
		`<li class="steps__step is-current" aria-current="step"><span class="mono steps__number">02</span>`,
		`<form class="setup__form" action="/setup/cloudflare" accept-charset="UTF-8" method="post">`, `name="cloudflare[base_domain]"`,
		`<span class="mono">houston-&lt;base&gt;</span>`, `<span class="mono">*.example.com</span>`)

	w := b.do("POST", "/setup/cloudflare", url.Values{"cloudflare[base_domain]": {"svnmns.com"}, "cloudflare[api_token]": {"nope"}})
	if w.Code != 422 {
		t.Fatalf("a wrong token = %d", w.Code)
	}
	contains(t, w.Body.String(), `<span class="mono eyebrow">TOKEN CHECK</span>`,
		`<p class="check"><span class="mono check__state check__state--nogo">NO-GO</span> <span>The token isn&#39;t valid (Cloudflare: Invalid API Token).`,
		`value="svnmns.com" name="cloudflare[base_domain]"`, `<span class="mono">houston-svnmns</span>`, `<span class="mono">admin.svnmns.com</span>`)
	w = b.do("POST", "/setup/cloudflare", url.Values{"cloudflare[base_domain]": {"https://svnmns.com"}, "cloudflare[api_token]": {"cf-token"}})
	contains(t, w.Body.String(), `<span class="field__error">Base domain must be a domain like example.com, with no scheme, path or wildcard.</span>`)

	w = b.do("POST", "/setup/cloudflare", url.Values{"cloudflare[base_domain]": {"svnmns.com"}, "cloudflare[api_token]": {"cf-token"}})
	if w.Code != 302 || location(w) != "/" {
		t.Fatalf("connect = %d %q\n%s", w.Code, location(w), w.Body.String())
	}
	if w := b.do("GET", "/", nil); location(w) != "/setup/storage" {
		t.Errorf("after = %q", location(w))
	}
	for _, method := range []string{"GET", "POST"} {
		if w := b.do(method, "/setup/cloudflare", nil); w.Code != 302 || location(w) != "/" {
			t.Errorf("%s once connected = %d %q", method, w.Code, location(w))
		}
	}
	b.jar = nil
	if w := b.do("GET", "/setup/cloudflare", nil); location(w) != "/sign-in" {
		t.Errorf("signed out = %q", location(w))
	}
}

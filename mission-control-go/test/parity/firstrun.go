package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
	"golang.org/x/crypto/bcrypt"
)

// setupCode is the installer's code, put in both (its digest, as each
// keeps it).
const setupCode = "PART-YCDE"

// visit is a request of first run's: a GET, or a POST of form through
// the form on the page at from that posts to path (its hidden fields,
// Rails' CSRF token, added: Rails' tokens are each form's own). before
// changes both databases first.
type visit struct {
	name, method, path string
	from               string
	form               url.Values
	before             func() error
}

// firstRun compares setup's pages, both started on an empty database:
// what's typed is the same, and what would reach Cloudflare or restic is
// refused before it does. It's how many differ, of how many.
func firstRun(rails, gov *side, railsDB, goDB string) (failed, compared int) {
	ctx := context.Background()
	open := func(path string) *db.DB {
		d, err := db.Open(ctx, "sqlite://"+path)
		if err != nil {
			panic(err)
		}
		return d
	}
	r, g := open(railsDB), open(goDB)
	defer r.Close()
	defer g.Close()
	digest, _ := bcrypt.GenerateFromPassword([]byte(strings.ReplaceAll(setupCode, "-", "")), bcrypt.MinCost)
	now := time.Now().UTC()
	if _, err := r.Write.Exec(`INSERT INTO setup_codes (code_digest, created_at, updated_at) VALUES (?, ?, ?)`, string(digest), now, now); err != nil {
		panic(err)
	}
	if _, err := g.Write.Exec(`INSERT INTO setup_codes (code_digest) VALUES (?)`, string(digest)); err != nil {
		panic(err)
	}
	connected := func() error {
		if _, err := r.Write.Exec(`INSERT INTO installations (base_domain, cloudflare_connected_at, created_at, updated_at) VALUES ('houston.localhost', ?, ?, ?)`, now, now, now); err != nil {
			return err
		}
		_, err := g.Write.Exec(`INSERT INTO installations (id, base_domain, cloudflare_connected_at) VALUES (1, 'houston.localhost', ?)`, now)
		return err
	}

	visits := []visit{
		{name: "a page before setup", method: "GET", path: "/"},
		{name: "sign-in before setup", method: "GET", path: "/sign-in"},
		{name: "setup", method: "GET", path: "/setup"},
		{name: "setup, wrong", method: "POST", path: "/setup", from: "/setup", form: url.Values{"setup[code]": {"AAAA-AAAA"},
			"setup[email_address]": {"not an email"}, "setup[password]": {"short"}, "setup[password_confirmation]": {"other"}}},
		{name: "setup, the admin", method: "POST", path: "/setup", from: "/setup", form: url.Values{"setup[code]": {strings.ToLower(setupCode)},
			"setup[email_address]": {"admin@houston.localhost"}, "setup[password]": {"parity-password"}, "setup[password_confirmation]": {"parity-password"}}},
		{name: "setup, again", method: "GET", path: "/setup"},
		{name: "the next step", method: "GET", path: "/"},
		{name: "Cloudflare", method: "GET", path: "/setup/cloudflare"},
		{name: "Cloudflare, not a domain", method: "POST", path: "/setup/cloudflare", from: "/setup/cloudflare", form: url.Values{
			"cloudflare[base_domain]": {"https://Houston.localhost/"}, "cloudflare[api_token]": {"x"}}},
		{name: "storage before Cloudflare", method: "GET", path: "/setup/storage"},
		{name: "storage", method: "GET", path: "/setup/storage", before: connected},
		{name: "storage, wrong", method: "POST", path: "/setup/storage", from: "/setup/storage", form: url.Values{"storage[kind]": {"local"},
			"storage[name]": {"Bad Name"}, "storage[local_path]": {"srv"}}},
	}
	for _, s := range []*side{rails, gov} {
		jar, _ := cookiejar.New(nil)
		s.browser = &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	for _, v := range visits {
		compared++
		if v.before != nil {
			if err := v.before(); err != nil {
				fmt.Printf("FAIL first run, %s: %v\n", v.name, err)
				failed++
				continue
			}
		}
		var got [2][]string
		var err error
		for i, s := range []*side{rails, gov} {
			if got[i], err = s.answer(v); err != nil {
				break
			}
		}
		if err != nil {
			fmt.Printf("FAIL first run, %s: %v\n", v.name, err)
			failed++
			continue
		}
		if diff := differ(got[0], got[1]); diff != "" {
			fmt.Printf("FAIL first run, %s\n%s\n", v.name, diff)
			failed++
			continue
		}
		fmt.Printf("ok   first run, %s (%s)\n", v.name, got[0][0])
	}
	return failed, compared
}

// answer is v's answer from s, as lines: its status, then where it
// redirects or the page's outline.
func (s *side) answer(v visit) ([]string, error) {
	body := url.Values{}
	if v.method == "POST" {
		resp, err := s.get(v.from)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
		page, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		action, hidden := form(page, v.path)
		if action == "" {
			return nil, fmt.Errorf("%s: no form posting to %s at %s", s.name, v.path, v.from)
		}
		body = hidden
		for k, vs := range v.form {
			body[k] = vs
		}
	}
	req, err := http.NewRequest(v.method, s.base+v.path, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Host = admin
	req.Header.Set("Accept", "text/html")
	if v.method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	resp, err := s.browser.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.name, err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	lines := []string{fmt.Sprint("status ", resp.StatusCode)}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		to, _ := url.Parse(resp.Header.Get("Location"))
		return append(lines, "to "+to.RequestURI()), nil
	}
	return append(lines, outline(page)...), nil
}

// differ is where two answers part, "" when they don't.
func differ(r, g []string) string {
	for i := 0; i < len(r) || i < len(g); i++ {
		if i < len(r) && i < len(g) && r[i] == g[i] {
			continue
		}
		return "  rails " + around(r, i) + "\n  go    " + around(g, i)
	}
	return ""
}

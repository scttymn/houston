package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// pages are the pages compared, signed in as the fixture's admin: their
// text and their structure (tags, classes, Stimulus wiring), which with
// the same stylesheet is how they look. Run after the steps, so both have
// the same deploys to show.
var pages = []string{
	"/",
	"/projects/shop",
	"/projects/blog",
	"/projects/api",
	"/projects/shop/deploys/1",
	"/projects/shop/deploys/2",
	"/projects/blog/deploys/1",
	"/projects/shop/snapshots",
	"/projects/shop/copy/new",
	"/projects/shop/deletion/new",
	"/settings",
	"/settings/storage/new",
	"/link",
}

// signIn signs s in as the fixture's admin, through its sign-in form.
func signIn(s *side) error {
	jar, _ := cookiejar.New(nil)
	s.browser = &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	path := "/session/new"
	var form []byte
	for range 3 {
		resp, err := s.get(path)
		if err != nil {
			return err
		}
		form, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			break
		}
		u, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			return err
		}
		path = u.RequestURI()
	}
	action, fields := formFields(form)
	if action == "" {
		return fmt.Errorf("no sign-in form at %s", path)
	}
	req, err := http.NewRequest("POST", s.base+action, strings.NewReader(fields.Encode()))
	if err != nil {
		return err
	}
	req.Host = admin
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := s.browser.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		return fmt.Errorf("signing in answered %d", resp.StatusCode)
	}
	return nil
}

// formFields is a page's first form: where it posts, and its fields filled
// in, the email and password with the fixture admin's.
func formFields(page []byte) (string, url.Values) {
	z := html.NewTokenizer(bytes.NewReader(page))
	action, fields := "", url.Values{}
	for {
		switch z.Next() {
		case html.ErrorToken:
			return action, fields
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			a := attrs(t)
			switch {
			case t.Data == "form" && action == "":
				action = a["action"]
			case t.Data == "input" && a["name"] != "":
				switch {
				case a["type"] == "password":
					fields.Set(a["name"], "parity-password")
				case a["type"] == "email" || strings.Contains(a["name"], "email"):
					fields.Set(a["name"], "admin@houston.localhost")
				case a["type"] == "hidden":
					fields.Set(a["name"], a["value"])
				}
			}
		case html.EndTagToken:
			if z.Token().Data == "form" && action != "" {
				return action, fields
			}
		}
	}
}

func attrs(t html.Token) map[string]string {
	m := map[string]string{}
	for _, a := range t.Attr {
		m[a.Key] = a.Val
	}
	return m
}

func (s *side) get(path string) (*http.Response, error) {
	req, err := http.NewRequest("GET", s.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Host = admin
	req.Header.Set("Accept", "text/html")
	return s.browser.Do(req)
}

// comparePage is how the page at path differs between the two, "" when
// it doesn't.
func comparePage(rails, gov *side, path string) (string, error) {
	var got [2][]string
	for i, s := range []*side{rails, gov} {
		resp, err := s.get(path)
		if err != nil {
			return "", fmt.Errorf("%s: %w", s.name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got[i] = append([]string{fmt.Sprint("status ", resp.StatusCode)}, outline(body)...)
	}
	r, g := got[0], got[1]
	for i := 0; i < len(r) || i < len(g); i++ {
		if i < len(r) && i < len(g) && r[i] == g[i] {
			continue
		}
		return "  rails " + around(r, i) + "\n  go    " + around(g, i), nil
	}
	return "", nil
}

// around is the outline's lines from i on, a few of them.
func around(lines []string, i int) string {
	if i >= len(lines) {
		return "(ends)"
	}
	return strings.Join(lines[i:min(i+4, len(lines))], " | ")
}

var (
	// kept is what's compared of an element's attributes.
	kept = []string{"id", "class", "role", "href", "action", "method", "name", "type", "placeholder", "src",
		"data-controller", "data-action", "data-turbo-frame", "data-turbo-confirm", "data-turbo", "aria-label",
		"aria-current", "aria-selected", "hidden", "disabled", "required", "target", "rel", "loading"}
	digest  = regexp.MustCompile(`-[0-9a-f]{8}\.(css|js|svg|png|woff2)`)
	counted = regexp.MustCompile(`\d+`)
	plural  = regexp.MustCompile(`\b0 (second|minute|hour|day|month|year)s\b`)
	// unreachable is why a host couldn't be reached, in the words of each
	// language's HTTP client.
	unreachable = regexp.MustCompile(`from this server \(.*`)
)

// outline is a page's body as lines: each element with the attributes
// that make how it looks and acts, and each run of text. What can't
// match is made to: numbers (times, ids) are 0, and asset digests go.
// The live-updates source is left out (Action Cable, or server-sent
// events), and so are hidden fields (Rails' CSRF tokens).
func outline(page []byte) []string {
	z := html.NewTokenizer(bytes.NewReader(page))
	var out []string
	inBody, skip := false, 0
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if t.Data == "body" {
				inBody = true
			}
			if !inBody {
				continue
			}
			if skip > 0 {
				if tt == html.StartTagToken {
					skip++
				}
				continue
			}
			// An icon is its tag; a script or template, nothing.
			switch t.Data {
			case "svg", "script", "template", "style":
				if tt == html.StartTagToken {
					skip = 1
				}
				if t.Data != "svg" {
					continue
				}
			}
			a := attrs(t)
			if t.Data == "turbo-cable-stream-source" || t.Data == "turbo-stream-source" || (t.Data == "input" && a["type"] == "hidden") {
				continue
			}
			line := "<" + t.Data
			for _, k := range kept {
				v, ok := a[k]
				switch {
				case !ok, k == "class" && strings.TrimSpace(v) == "":
					continue
				case k == "class":
					v = strings.Join(strings.Fields(v), " ")
				case k == "hidden" || k == "disabled" || k == "required":
					v = ""
				}
				line += " " + k + "=" + strings.TrimSpace(digest.ReplaceAllString(v, ".$1"))
			}
			out = append(out, counted.ReplaceAllString(line, "0")+">")
		case html.EndTagToken:
			if skip > 0 {
				skip--
			}
		case html.TextToken:
			if !inBody || skip > 0 {
				continue
			}
			text := strings.Join(strings.Fields(string(z.Text())), " ")
			if text == "" {
				continue
			}
			text = unreachable.ReplaceAllString(text, "from this server (…)")
			out = append(out, plural.ReplaceAllString(counted.ReplaceAllString(text, "0"), "0 $1"))
		}
	}
}

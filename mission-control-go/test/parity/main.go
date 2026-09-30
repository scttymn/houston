// Command parity sends the Rails and Go versions of Mission Control the
// same requests and compares their answers (bin/parity starts both on the
// same data). A value that can't match (a token, a time) is compared by
// its shape; null and "" are the same, as the Go clients read them.
//
//	go run ./test/parity -rails http://rails:3000 -go http://go:8080 -token RUNNER_TOKEN
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// side is one version: where it is, and what its answers handed back (a
// deploy's id and token differ between them).
type side struct {
	name, base string
	state      map[string]string
	// browser is signed in, for the pages.
	browser *http.Client
}

// step is one request. Its path, headers and body may name what an
// earlier step kept: {deploy}, {token}.
type step struct {
	name   string
	method string
	path   string
	host   string // the Host header: MC's admin host unless set
	header map[string]string
	body   string
	// keep takes values from the answer for later steps.
	keep map[string]string // state name → answer field
	// statusOnly compares the status alone (a page, not the API).
	statusOnly bool
	// known is why the answers differ for now, until the batch that closes
	// the gap: reported, not failed.
	known string
}

const admin = "admin.houston.localhost"

var steps = []step{
	{name: "health", method: "GET", path: "/up", statusOnly: true},
	{name: "ping", method: "GET", path: "/ping"},
	{name: "no token", method: "POST", path: "/api/projects/sync", header: map[string]string{"Authorization": ""}, body: `{}`},
	{name: "through the tunnel", method: "POST", path: "/api/projects/sync", header: map[string]string{"Cf-Ray": "8f-MCI"}, body: `{}`},
	{name: "sync", method: "POST", path: "/api/projects/sync", body: `{"name":"shop","app_service":"web","services":["web","db"],
		"domains":["shop.houston.localhost"],"variables":[{"name":"SECRET_KEY_BASE","required":true}],"health":"/up","port":3000}`},
	{name: "sync, invalid", method: "POST", path: "/api/projects/sync", body: `{"name":"shop","app_service":"worker","services":["web"],
		"domains":["Bad"],"variables":[],"health":"up","port":0,"backups":{"keep_auto":0}}`},
	{name: "sync, a new project on HOLD", method: "POST", path: "/api/projects/sync", body: `{"name":"api","app_service":"web","services":["web"],
		"domains":[],"variables":[{"name":"TOKEN","required":true},{"name":"KEY","required":true}],"health":"/","port":80}`},
	{name: "sync, another project's container name", method: "POST", path: "/api/projects/sync", body: `{"name":"shop-db","app_service":"web",
		"services":["web"],"domains":[],"variables":[],"health":"/","port":80}`},
	{name: "a secret", method: "GET", path: "/api/projects/shop/secrets/SECRET_KEY_BASE"},
	{name: "a secret not referenced", method: "GET", path: "/api/projects/shop/secrets/NOPE"},
	{name: "a deploy of no project", method: "POST", path: "/api/projects/nope/deploys", body: `{"sha":"` + strings.Repeat("c", 40) + `","ref":"main"}`},
	{name: "a deploy, invalid", method: "POST", path: "/api/projects/shop/deploys", body: `{"sha":"abc"}`},
	{name: "a deploy", method: "POST", path: "/api/projects/shop/deploys", body: `{"sha":"` + strings.Repeat("c", 40) + `","ref":"refs/heads/main"}`,
		keep: map[string]string{"deploy": "id", "token": "token"}},
	{name: "a deploy in the way", method: "POST", path: "/api/projects/shop/deploys", body: `{"sha":"` + strings.Repeat("c", 40) + `","ref":"main"}`},
	{name: "progress", method: "PATCH", path: "/api/deploys/{deploy}", header: map[string]string{"X-Houston-Deploy-Token": "{token}"},
		body: `{"step":"Build","log":"built\n"}`},
	{name: "progress, invalid", method: "PATCH", path: "/api/deploys/{deploy}", header: map[string]string{"X-Houston-Deploy-Token": "{token}"},
		body: `{"status":"hold"}`},
	{name: "progress, another's token", method: "PATCH", path: "/api/deploys/{deploy}", header: map[string]string{"X-Houston-Deploy-Token": "old"},
		body: `{"step":"x"}`},
	{name: "a snapshot", method: "POST", path: "/api/deploys/{deploy}/snapshot", header: map[string]string{"X-Houston-Deploy-Token": "{token}"}},
	{name: "a snapshot, again", method: "POST", path: "/api/deploys/{deploy}/snapshot", header: map[string]string{"X-Houston-Deploy-Token": "{token}"}},
	{name: "the snapshot", method: "GET", path: "/api/deploys/{deploy}/snapshot", header: map[string]string{"X-Houston-Deploy-Token": "{token}"}},
	{name: "a restore's data, not a restore", method: "POST", path: "/api/deploys/{deploy}/restore_data", header: map[string]string{"X-Houston-Deploy-Token": "{token}"}},
	{name: "a copy's data, not a copy", method: "POST", path: "/api/deploys/{deploy}/copy_data", header: map[string]string{"X-Houston-Deploy-Token": "{token}"}},
	{name: "a handover, another's token", method: "POST", path: "/api/deploys/{deploy}/handover", header: map[string]string{"X-Houston-Deploy-Token": "old"}},
	{name: "a handover, not a copy", method: "POST", path: "/api/deploys/{deploy}/handover", header: map[string]string{"X-Houston-Deploy-Token": "{token}"}},
	{name: "GO", method: "PATCH", path: "/api/deploys/{deploy}", header: map[string]string{"X-Houston-Deploy-Token": "{token}"}, body: `{"status":"go"}`},
	{name: "after GO", method: "PATCH", path: "/api/deploys/{deploy}", header: map[string]string{"X-Houston-Deploy-Token": "{token}"}, body: `{"step":"x"}`},
	{name: "a claim, invalid", method: "POST", path: "/api/runner/jobs/claim", body: `{"runner":"bob"}`},
	{name: "a claim", method: "POST", path: "/api/runner/jobs/claim", body: `{"runner":"houston-runner-1","wait":0}`},
	{name: "a claim, nothing left", method: "POST", path: "/api/runner/jobs/claim", body: `{"runner":"houston-runner-1","wait":0}`},
	{name: "a webhook", method: "POST", path: "/shop", host: "hooks.houston.localhost", header: map[string]string{"X-Houston-Token": "whsec", "Authorization": ""}, body: `{}`},
	{name: "a webhook, unverified", method: "POST", path: "/shop", host: "hooks.houston.localhost", header: map[string]string{"X-Houston-Token": "nope", "Authorization": ""}, body: `{}`},
	{name: "hooks, anything else", method: "GET", path: "/sign-in", host: "hooks.houston.localhost", header: map[string]string{"Authorization": ""}},

	// The personal API: reads.
	{name: "v1, no token", method: "GET", path: "/api/v1/me", header: map[string]string{"Authorization": ""}},
	{name: "v1, the runner's token", method: "GET", path: "/api/v1/me"},
	{name: "v1 me", method: "GET", path: "/api/v1/me", header: personal},
	{name: "v1 projects", method: "GET", path: "/api/v1/projects", header: personal},
	{name: "v1 a project", method: "GET", path: "/api/v1/projects/shop", header: personal},
	{name: "v1 no project", method: "GET", path: "/api/v1/projects/nope", header: personal},
	{name: "v1 deploys", method: "GET", path: "/api/v1/projects/shop/deploys", header: personal},
	{name: "v1 the latest deploy", method: "GET", path: "/api/v1/projects/shop/deploys/latest", header: personal},
	{name: "v1 a deploy's log from a byte", method: "GET", path: "/api/v1/projects/shop/deploys/2?log_from=3", header: personal},
	{name: "v1 no deploy", method: "GET", path: "/api/v1/projects/shop/deploys/99", header: personal},
	{name: "v1 the latest backup", method: "GET", path: "/api/v1/projects/shop/backups/latest", header: personal},
	{name: "v1 settings", method: "GET", path: "/api/v1/settings", header: personal},
	{name: "v1 storage", method: "GET", path: "/api/v1/storage", header: personal},
	{name: "v1 volumes", method: "GET", path: "/api/v1/projects/shop/volumes", header: personal},
	{name: "v1 a webhook, no repo", method: "GET", path: "/api/v1/projects/shop/webhook", header: personal},
	{name: "v1 secrets", method: "GET", path: "/api/v1/projects/shop/secrets", header: personal},

	// The personal API: Mission Control's own writes. (Maintenance isn't
	// here: both would push the tunnel's routes to Cloudflare itself.)
	{name: "v1 the time zone", method: "PUT", path: "/api/v1/settings", header: personal, body: `{"time_zone":"Europe/Berlin"}`},
	{name: "v1 not a time zone", method: "PATCH", path: "/api/v1/settings", header: personal, body: `{"time_zone":"Mars/Base"}`},
	{name: "v1 a secret set", method: "PUT", path: "/api/v1/projects/shop/secrets/SECRET_KEY_BASE", header: personal, body: `{"value":"new"}`},
	{name: "v1 a secret Kamal can't carry", method: "PUT", path: "/api/v1/projects/shop/secrets/SECRET_KEY_BASE", header: personal, body: `{"value":"a\\b"}`},
	{name: "v1 a secret generated", method: "POST", path: "/api/v1/projects/shop/secrets/SECRET_KEY_BASE/generate", header: personal},
	{name: "v1 a secret removed", method: "DELETE", path: "/api/v1/projects/shop/secrets/SECRET_KEY_BASE", header: personal},
	{name: "v1 a secret not referenced", method: "PUT", path: "/api/v1/projects/shop/secrets/NOPE", header: personal, body: `{"value":"x"}`},
	{name: "v1 the webhook rotated, no repo", method: "POST", path: "/api/v1/projects/shop/webhook/rotate", header: personal},
	{name: "v1 no such volume", method: "PUT", path: "/api/v1/projects/shop/volumes/data", header: personal, body: `{"location":"nas"}`},
	{name: "v1 the backup target", method: "PUT", path: "/api/v1/projects/shop/backup_target", header: personal, body: `{"location":"nas"}`},
	{name: "v1 no such target", method: "PUT", path: "/api/v1/projects/shop/backup_target", header: personal, body: `{"location":"nope"}`},
	{name: "v1 the default storage", method: "PUT", path: "/api/v1/storage/nas", header: personal, body: `{"default":true}`},
	{name: "v1 the default, not asked right", method: "PUT", path: "/api/v1/storage/nas", header: personal, body: `{"default":"yes"}`},
	{name: "v1 a backup now", method: "POST", path: "/api/v1/projects/shop/backups", header: personal},
	{name: "v1 the update", method: "GET", path: "/api/v1/update", header: personal},
	{name: "v1 an update, not a version", method: "POST", path: "/api/v1/update", header: personal, body: `{"version":4}`},
	{name: "v1 an update, refused", method: "POST", path: "/api/v1/update", header: personal, body: `{}`},
	{name: "v1 the port, not asked right", method: "PUT", path: "/api/v1/port", header: personal, body: `{"open":"maybe"}`},
	{name: "v1 a copy, not confirmed", method: "POST", path: "/api/v1/projects/shop/copy", header: personal, body: `{"confirm":"nope"}`},
	{name: "v1 a copy, refused", method: "POST", path: "/api/v1/projects/shop/copy", header: personal, body: `{"confirm":"shop"}`},
	{name: "v1 a copy of no project", method: "POST", path: "/api/v1/projects/nope/copy", header: personal, body: `{"confirm":"nope"}`},
	{name: "v1 cancel, not a copy", method: "POST", path: "/api/v1/projects/shop/copy/cancel", header: personal},
	{name: "v1 undo, not a copy", method: "POST", path: "/api/v1/projects/blog/copy/undo", header: personal, body: `{"confirm":"blog"}`},
	{name: "v1 settings, after", method: "GET", path: "/api/v1/settings", header: personal},
	{name: "v1 secrets, after", method: "GET", path: "/api/v1/projects/shop/secrets", header: personal},
}

// personal is the fixture's personal token, for the /api/v1 steps.
var personal = map[string]string{"Authorization": "Bearer hou_parity-personal-token"}

func main() {
	railsURL := flag.String("rails", "", "the Rails version's address")
	goURL := flag.String("go", "", "the Go version's address")
	token := flag.String("token", "", "the runner token both have")
	flag.Parse()
	rails := &side{name: "rails", base: *railsURL, state: map[string]string{}}
	gov := &side{name: "go", base: *goURL, state: map[string]string{}}
	failed, known := 0, 0
	for _, st := range steps {
		ra, err := send(rails, st, *token)
		if err != nil {
			fmt.Printf("FAIL %s: rails: %v\n", st.name, err)
			failed++
			continue
		}
		ga, err := send(gov, st, *token)
		if err != nil {
			fmt.Printf("FAIL %s: go: %v\n", st.name, err)
			failed++
			continue
		}
		switch diff := compare(st, ra, ga); {
		case diff != "" && st.known != "":
			fmt.Printf("KNOWN %s: %s\n", st.name, st.known)
			known++
		case diff != "":
			fmt.Printf("FAIL %s\n%s\n", st.name, diff)
			failed++
		default:
			fmt.Printf("ok   %s (%d)\n", st.name, ra.status)
		}
	}
	for _, s := range []*side{rails, gov} {
		if err := signIn(s); err != nil {
			fmt.Printf("FAIL signing in: %s: %v\n", s.name, err)
			os.Exit(1)
		}
	}
	for _, path := range pages {
		switch diff, err := comparePage(rails, gov, path); {
		case err != nil:
			fmt.Printf("FAIL page %s: %v\n", path, err)
			failed++
		case diff != "":
			fmt.Printf("FAIL page %s\n%s\n", path, diff)
			failed++
		default:
			fmt.Printf("ok   page %s\n", path)
		}
	}
	fmt.Printf("\n%d of %d steps and pages differ (and %d as known)\n", failed, len(steps)+len(pages), known)
	if failed > 0 {
		os.Exit(1)
	}
}

// answer is a response: its status, its kind of body, and the body.
type answer struct {
	status int
	kind   string // json, text, html or empty
	body   []byte
}

func fill(s *side, text string) string {
	for k, v := range s.state {
		text = strings.ReplaceAll(text, "{"+k+"}", v)
	}
	return text
}

func send(s *side, st step, token string) (answer, error) {
	req, err := http.NewRequest(st.method, s.base+fill(s, st.path), strings.NewReader(fill(s, st.body)))
	if err != nil {
		return answer{}, err
	}
	req.Host = admin
	if st.host != "" {
		req.Host = st.host
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range st.header {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, fill(s, v))
		}
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	a := answer{status: resp.StatusCode, body: body}
	switch ct := resp.Header.Get("Content-Type"); {
	case len(body) == 0:
		a.kind = "empty"
	case strings.HasPrefix(ct, "application/json"):
		a.kind = "json"
	case strings.HasPrefix(ct, "text/html"):
		a.kind = "html"
	default:
		a.kind = "text"
	}
	if a.kind == "json" && st.keep != nil {
		var m map[string]any
		json.Unmarshal(body, &m)
		for name, field := range st.keep {
			s.state[name] = fmt.Sprint(m[field])
		}
	}
	return a, nil
}

var (
	isoTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`)
	// A deploy's duration is its own clock's: a second apart in flight.
	volatiles = map[string]bool{"token": true, "queued_at": true, "started_at": true, "finished_at": true, "duration": true, "updated_at": true}
)

// normal is a JSON value with what can't match between the versions made
// comparable: tokens and times by their shape, "" as null.
func normal(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			out[k] = normal(val, k)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normal(val, "")
		}
		return out
	case string:
		if x == "" {
			return nil
		}
		if volatiles[key] {
			return "<" + key + ">"
		}
		return isoTime.ReplaceAllString(x, "<time>")
	}
	if v != nil && volatiles[key] {
		return "<" + key + ">"
	}
	return v
}

func compare(st step, rails, gov answer) string {
	var diffs []string
	if rails.status != gov.status {
		diffs = append(diffs, fmt.Sprintf("  status: rails %d, go %d", rails.status, gov.status))
	}
	if st.statusOnly {
		return strings.Join(diffs, "\n")
	}
	if rails.kind != gov.kind {
		diffs = append(diffs, fmt.Sprintf("  body: rails %s, go %s", rails.kind, gov.kind))
	}
	switch {
	case rails.kind == "json" && gov.kind == "json":
		var r, g any
		json.Unmarshal(rails.body, &r)
		json.Unmarshal(gov.body, &g)
		if !reflect.DeepEqual(normal(r, ""), normal(g, "")) {
			rb, _ := json.Marshal(normal(r, ""))
			gb, _ := json.Marshal(normal(g, ""))
			diffs = append(diffs, fmt.Sprintf("  rails %s\n  go    %s", rb, gb))
		}
	case rails.kind == "text" && gov.kind == "text":
		if !bytes.Equal(rails.body, gov.body) {
			diffs = append(diffs, fmt.Sprintf("  rails %q\n  go    %q", rails.body, gov.body))
		}
	}
	return strings.Join(diffs, "\n")
}

// Package cloudflaretest is a fake of the parts of Cloudflare's API Houston
// calls: zones, their DNS records, and a tunnel's configuration.
package cloudflaretest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare"
)

// Fake is the API, in memory.
type Fake struct {
	URL string // its address, for Client.Base

	mu      sync.Mutex
	zones   []cloudflare.Zone
	records map[string][]cloudflare.Record // zone id → records
	nextID  int
	// Tunnels are each tunnel's last configuration, by "account/tunnel".
	tunnels map[string]json.RawMessage
	// Fail, when set, is the error every call answers.
	fail string
	// Calls are what was asked, "GET /zones?name=x" style.
	calls []string
	// Loose makes it ignore a list's comment.exact filter, as an API that
	// didn't know it would.
	Loose bool
}

// New is a fake, stopped when the test ends.
func New(t testing.TB) *Fake {
	f := &Fake{records: map[string][]cloudflare.Record{}, tunnels: map[string]json.RawMessage{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// Client is a client of the fake.
func (f *Fake) Client() cloudflare.Client { return cloudflare.Client{Token: "cf-token", Base: f.URL} }

// Zone adds a zone: status "active", or "pending" before its nameservers
// are switched.
func (f *Fake) Zone(id, name, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zones = append(f.zones, cloudflare.Zone{ID: id, Name: name, Status: status})
}

// Record adds a record to zone.
func (f *Fake) Record(zone string, r cloudflare.Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	r.ID = fmt.Sprintf("rec%d", f.nextID)
	f.records[zone] = append(f.records[zone], r)
}

// Records are zone's records.
func (f *Fake) Records(zone string) []cloudflare.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cloudflare.Record(nil), f.records[zone]...)
}

// Tunnel is a tunnel's last configuration, as sent.
func (f *Fake) Tunnel(account, tunnel string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.tunnels[account+"/"+tunnel])
}

// Fail makes every call answer Cloudflare's error msg ("" for none).
func (f *Fake) Fail(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = msg
}

// Calls are the calls made so far.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		call += "?" + r.URL.RawQuery
	}
	f.calls = append(f.calls, call)
	if r.Header.Get("Authorization") != "Bearer cf-token" {
		answer(w, 403, nil, "Invalid API Token")
		return
	}
	if f.fail != "" {
		answer(w, 400, nil, f.fail)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == "GET" && len(parts) == 1 && parts[0] == "zones":
		found := []cloudflare.Zone{}
		for _, z := range f.zones {
			if z.Name == r.URL.Query().Get("name") {
				found = append(found, z)
			}
		}
		answer(w, 200, found, "")
	case len(parts) >= 3 && parts[0] == "zones" && parts[2] == "dns_records":
		f.dnsRecords(w, r, parts[1], parts[3:])
	case r.Method == "PUT" && len(parts) == 5 && parts[0] == "accounts" && parts[2] == "cfd_tunnel" && parts[4] == "configurations":
		var body json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		f.tunnels[parts[1]+"/"+parts[3]] = body
		answer(w, 200, map[string]any{}, "")
	default:
		answer(w, 404, nil, "no route for "+call)
	}
}

func (f *Fake) dnsRecords(w http.ResponseWriter, r *http.Request, zone string, rest []string) {
	records := f.records[zone]
	switch {
	case r.Method == "GET" && len(rest) == 0:
		found := []cloudflare.Record{}
		q := r.URL.Query()
		for _, rec := range records {
			if (!q.Has("name") || rec.Name == q.Get("name")) && (f.Loose || !q.Has("comment.exact") || rec.Comment == q.Get("comment.exact")) {
				found = append(found, rec)
			}
		}
		answer(w, 200, found, "")
	case r.Method == "POST" && len(rest) == 0:
		var rec cloudflare.Record
		json.NewDecoder(r.Body).Decode(&rec)
		f.nextID++
		rec.ID = fmt.Sprintf("rec%d", f.nextID)
		f.records[zone] = append(records, rec)
		answer(w, 200, rec, "")
	case (r.Method == "PATCH" || r.Method == "DELETE") && len(rest) == 1:
		for i, rec := range records {
			if rec.ID != rest[0] {
				continue
			}
			if r.Method == "DELETE" {
				f.records[zone] = append(records[:i:i], records[i+1:]...)
				answer(w, 200, map[string]string{"id": rec.ID}, "")
				return
			}
			json.NewDecoder(r.Body).Decode(&records[i])
			records[i].ID = rec.ID
			answer(w, 200, records[i], "")
			return
		}
		answer(w, 404, nil, "Record not found")
	default:
		answer(w, 404, nil, "no route")
	}
}

func answer(w http.ResponseWriter, status int, result any, msg string) {
	body := map[string]any{"success": msg == "", "result": result, "errors": []any{}}
	if msg != "" {
		body["errors"] = []map[string]any{{"code": 1000, "message": msg}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

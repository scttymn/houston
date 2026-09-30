package api_test

import (
	"strings"
	"testing"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare/cloudflaretest"
)

// houston cloudflare: the view, the token (write-only) and repair.
func TestV1Cloudflare(t *testing.T) {
	a, _ := remote(t)
	fake := cloudflaretest.New(t)
	fake.Zone("zbase", "svnmns.com", "active")
	fake.Zone("zcom", "example.com", "active") // shop.example.com's
	fake.Account("acct", "Seven Moons")
	fake.TunnelDetails("acct", "tun", map[string]any{"id": "tun", "name": "houston-svnmns", "status": "healthy"})
	exec(t, a, `UPDATE installations SET cloudflare_zone_id = 'zbase', cloudflare_account_id = 'acct', tunnel_id = 'tun', cloudflare_api_token = ?`, crypt.Of("old-token"))
	a.CloudflareSettings.API = fake.URL
	h := a.Handler()

	w := v1(h, "GET", "/cloudflare", personal, "", nil)
	got := answer(t, w)
	if w.Code != 200 || got["tunnel"] != nil || !strings.Contains(w.Body.String(), `"problems":["tunnel: Invalid API Token"`) {
		t.Errorf("view with the old token = %d %s", w.Code, w.Body.String())
	}
	w = v1(h, "PUT", "/cloudflare/token", personal, `{"token":"wrong"}`, nil)
	if got := answer(t, w); w.Code != 422 || got["replaced"] != false || strings.Contains(w.Body.String(), "wrong") {
		t.Errorf("a bad token = %d %s", w.Code, w.Body.String())
	}
	w = v1(h, "PUT", "/cloudflare/token", personal, `{"token":"cf-token"}`, nil)
	if got := answer(t, w); w.Code != 200 || got["replaced"] != true || len(got["checks"].([]any)) != 4 || strings.Contains(w.Body.String(), "cf-token") {
		t.Errorf("= %d %s", w.Code, w.Body.String())
	}
	if got := answer(t, v1(h, "GET", "/cloudflare", personal, "", nil)); got["tunnel"].(map[string]any)["name"] != "houston-svnmns" {
		t.Errorf("view with the new token %v", got)
	}
	w = v1(h, "POST", "/cloudflare/repair", personal, "", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"results":[{"item":"routes","state":"OK","reason":null}`) {
		t.Errorf("repair = %d %s", w.Code, w.Body.String())
	}
	for _, c := range [][2]string{{"GET", "/cloudflare"}, {"PUT", "/cloudflare/token"}, {"POST", "/cloudflare/repair"}} {
		is(t, v1(h, c[0], c[1], "", `{}`, nil), 401, `{"error":"the API token is missing, wrong or revoked"}`)
	}
}

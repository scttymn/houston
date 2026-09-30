package api_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scttymn/gantry/crypt"
)

// ring posts a push to hooks.<base>/name from peer.
func ring(h http.Handler, name, peer, body string, header http.Header) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/"+name, strings.NewReader(body))
	r.Host, r.RemoteAddr = "hooks.svnmns.com", peer+":40000"
	for k, v := range header {
		r.Header[k] = v
	}
	h.ServeHTTP(w, r)
	return w
}

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func empty(t *testing.T, w *httptest.ResponseRecorder, code int, what string) {
	t.Helper()
	if w.Code != code || w.Body.Len() > 0 {
		t.Errorf("%s: %d %q, want an empty %d", what, w.Code, w.Body.String(), code)
	}
}

// A push rings the doorbell: verified by the git host's signature or token,
// it queues a look at the repo's refs (the payload is never read).
// Anything unverified is the same empty 404 as an unknown project.
func TestWebhook(t *testing.T) {
	a, h := synced(t)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"name": "blog", "services": []string{"web"}, "variables": []any{}}), nil)
	exec(t, a, `UPDATE projects SET webhook_secret = ? WHERE name = 'shop'`, crypt.Of("whsec"))
	body := `{"ref":"refs/heads/main"}`
	sig := sign("whsec", body)
	for name, header := range map[string]http.Header{
		"GitHub":           {"X-Hub-Signature-256": {"sha256=" + sig}},
		"Gitea":            {"X-Gitea-Signature": {sig}},
		"Forgejo, in caps": {"X-Forgejo-Signature": {strings.ToUpper(sig)}},
		"GitLab":           {"X-Gitlab-Token": {"whsec"}},
		"Houston":          {"X-Houston-Token": {"whsec"}},
	} {
		empty(t, ring(h, "shop", "140.82.112.1", body, header), 202, name)
	}
	var verified bool
	a.DB.Read.QueryRow(`SELECT webhook_verified_at IS NOT NULL FROM projects WHERE name = 'shop'`).Scan(&verified)
	pending, _ := a.Jobs.Pending(t.Context())
	if !verified || len(pending) != 5 || pending[0].Name != "check_for_changes" || !strings.Contains(string(pending[0].Args), `"project_id":1`) {
		t.Errorf("verified %v, jobs %+v", verified, pending)
	}

	for name, c := range map[string]struct {
		project string
		header  http.Header
	}{
		"a wrong signature":            {"shop", http.Header{"X-Hub-Signature-256": {"sha256=" + sign("other", body)}}},
		"a signature of another body":  {"shop", http.Header{"X-Gitea-Signature": {sign("whsec", "{}")}}},
		"a wrong token":                {"shop", http.Header{"X-Gitlab-Token": {"whsec2"}}},
		"no signature":                 {"shop", nil},
		"an unknown project":           {"nope", http.Header{"X-Houston-Token": {"whsec"}}},
		"a project without a secret":   {"blog", http.Header{"X-Houston-Token": {""}}},
		"signed with its empty secret": {"blog", http.Header{"X-Hub-Signature-256": {"sha256=" + sign("", body)}}},
		"not a name":                   {"Shop", http.Header{"X-Houston-Token": {"whsec"}}},
	} {
		empty(t, ring(h, c.project, "203.0.113.9", body, c.header), 404, name)
	}
	empty(t, ring(h, "shop", "203.0.113.10", strings.Repeat("x", 5<<20+1), nil), 413, "over 5 MiB")
	big := strings.Repeat("x", 2<<20)
	empty(t, ring(h, "shop", "140.82.112.1", big, http.Header{"X-Hub-Signature-256": {"sha256=" + sign("whsec", big)}}), 202, "a big push")
}

// Limits, a minute at a time: unverified requests per address and name,
// checked before the body is read, so junk can't block real pushes from
// another address or to another project; verified ones per project.
func TestWebhookLimits(t *testing.T) {
	a, h := synced(t)
	exec(t, a, `UPDATE projects SET webhook_secret = ? WHERE name = 'shop'`, crypt.Of("whsec"))
	for i := range 30 {
		empty(t, ring(h, "shop", "203.0.113.9", "{}", nil), 404, fmt.Sprint("junk ", i+1))
	}
	empty(t, ring(h, "shop", "203.0.113.9", "{}", http.Header{"X-Houston-Token": {"whsec"}}), 429, "the 31st from that address, even verified")
	empty(t, ring(h, "blog", "203.0.113.9", "{}", nil), 404, "that address, another name")
	empty(t, ring(h, "shop", "140.82.112.1", "{}", http.Header{"X-Houston-Token": {"whsec"}}), 202, "another address")

	for i := range 59 {
		empty(t, ring(h, "shop", "140.82.112.1", "{}", http.Header{"X-Houston-Token": {"whsec"}}), 202, fmt.Sprint("push ", i+2))
	}
	empty(t, ring(h, "shop", "140.82.112.2", "{}", http.Header{"X-Houston-Token": {"whsec"}}), 429, "the 61st push to shop")
}

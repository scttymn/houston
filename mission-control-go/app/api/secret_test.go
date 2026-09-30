package api_test

import (
	"net/http/httptest"
	"testing"
)

// houston deploy asks for each value to hand to Kamal: only keys the
// project's compose.yml references, as plain text; anything else, or a
// key without a value, is 404.
func TestSecret(t *testing.T) {
	a, h := runnerAPI(t)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []map[string]any{{"name": "SECRET_KEY_BASE", "required": true}, {"name": "OPTIONAL"}, {"name": "BLANK"}}}), nil)
	secret(t, a, "shop", "SECRET_KEY_BASE", "s3cret = ünïcode")
	secret(t, a, "shop", "NOT_REFERENCED", "x")
	secret(t, a, "shop", "BLANK", "   ")

	get := func(path string) *httptest.ResponseRecorder { return post(h, "GET", path, "", nil) }
	w := get("/api/projects/shop/secrets/SECRET_KEY_BASE")
	if w.Code != 200 || w.Body.String() != "s3cret = ünïcode" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("= %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	for path, key := range map[string]string{
		"/api/projects/shop/secrets/NOT_REFERENCED":  "NOT_REFERENCED",
		"/api/projects/shop/secrets/OPTIONAL":        "OPTIONAL",
		"/api/projects/shop/secrets/BLANK":           "BLANK",
		"/api/projects/nope/secrets/SECRET_KEY_BASE": "SECRET_KEY_BASE",
	} {
		is(t, get(path), 404, `{"error":"no value for `+key+`"}`)
	}
}

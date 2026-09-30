package app_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/test/testapp"
)

// newApp is the app on a test database.
func newApp(t *testing.T) *app.App { return testapp.New(t) }

func handler(t *testing.T) http.Handler { return newApp(t).Handler() }

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Accept", "text/html")
	h.ServeHTTP(w, r)
	return w
}

func TestUp(t *testing.T) {
	if w := get(t, handler(t), "/up"); w.Code != http.StatusOK {
		t.Fatalf("GET /up = %d", w.Code)
	}
}

func TestErrorPages(t *testing.T) {
	w := get(t, handler(t), "/no-such-page")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "doesn't exist") {
		t.Fatalf("a missing page = %d\n%s", w.Code, w.Body)
	}
}

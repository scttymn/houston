package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func meServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me" || r.Header.Get("Authorization") != "Bearer hou_good" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"the API token is missing, wrong or revoked"}`)
			return
		}
		io.WriteString(w, `{"token":"laptop","server":"svnmns.com"}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOUSTON_API_TOKEN", "")
	terminal(t, false)
	s := meServer(t)

	code, stdout, stderr := runWithInput(&fakeDocker{}, "hou_good\n", "login", s.URL)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "Logged in to svnmns.com as token laptop") {
		t.Errorf("stdout = %q", stdout)
	}
	path := filepath.Join(home, ".config", "houston", "server.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]string
	json.Unmarshal(data, &saved)
	if saved["url"] != s.URL || saved["token"] != "hou_good" {
		t.Errorf("saved = %v", saved)
	}
	info, _ := os.Stat(path)
	dir, _ := os.Stat(filepath.Dir(path))
	if info.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Errorf("modes: file %o, dir %o", info.Mode().Perm(), dir.Mode().Perm())
	}
	if strings.Contains(stdout+stderr, "hou_good") {
		t.Error("the token was printed")
	}
}

func TestLoginRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOUSTON_API_TOKEN", "")
	terminal(t, false)
	s := meServer(t)
	path := filepath.Join(home, ".config", "houston", "server.json")

	code, _, stderr := runWithInput(&fakeDocker{}, "hou_revoked\n", "login", s.URL)
	if code != exitFailure || !strings.Contains(stderr, "refused") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a refused login wrote %s", path)
	}

	runWithInput(&fakeDocker{}, "hou_good\n", "login", s.URL)
	if code, _, _ := run(&fakeDocker{}, "logout"); code != 0 {
		t.Errorf("logout exit %d", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("logout left the saved token")
	}
}

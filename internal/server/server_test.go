package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigAndHeaders(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	if _, err := Load(getenv, home); err == nil || !strings.Contains(err.Error(), "houston login") {
		t.Errorf("nothing configured: err = %v, want one pointing at houston login", err)
	}
	if err := Save(home, Config{URL: "https://admin.file.test", Token: "hou_file"}); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(getenv, home); err != nil || c.URL != "https://admin.file.test" || c.Token != "hou_file" {
		t.Errorf("from the file: %+v, %v", c, err)
	}

	env["HOUSTON_SERVER"] = "https://admin.env.test/"
	if _, err := Load(getenv, home); err == nil || !strings.Contains(err.Error(), "HOUSTON_API_TOKEN") {
		t.Errorf("HOUSTON_SERVER alone: err = %v, want one asking for both", err)
	}
	env["HOUSTON_API_TOKEN"] = "hou_env"
	env["HOUSTON_ACCESS_CLIENT_ID"] = "cid.access"
	env["HOUSTON_ACCESS_CLIENT_SECRET"] = "csecret"
	c, err := Load(getenv, home)
	if err != nil || c != (Config{URL: "https://admin.env.test", Token: "hou_env", AccessClientID: "cid.access", AccessClientSecret: "csecret"}) {
		t.Errorf("env beats the file: %+v, %v", c, err)
	}

	var seen http.Header
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		if r.Header.Get("Authorization") != "Bearer hou_env" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"the API token is missing, wrong or revoked"}`)
			return
		}
		io.WriteString(w, `{"token":"laptop","server":"svnmns.com"}`)
	}))
	defer s.Close()
	c.URL = s.URL
	me, err := New(c).Me(context.Background())
	if err != nil || me != (Me{Token: "laptop", Server: "svnmns.com"}) {
		t.Errorf("Me = %+v, %v", me, err)
	}
	if seen.Get("CF-Access-Client-Id") != "cid.access" || seen.Get("CF-Access-Client-Secret") != "csecret" {
		t.Errorf("Access headers = %v", seen)
	}

	c.Token = "hou_revoked"
	_, err = New(c).Me(context.Background())
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "houston login") {
		t.Errorf("refused: err = %v", err)
	}
	c.AccessClientID, c.AccessClientSecret = "", ""
	New(c).Me(context.Background())
	if seen.Get("CF-Access-Client-Id") != "" {
		t.Error("Access headers sent without Access credentials")
	}

	info, _ := os.Stat(filepath.Join(home, ".config", "houston", "server.json"))
	dir, _ := os.Stat(filepath.Join(home, ".config", "houston"))
	if info.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Errorf("modes: file %o, dir %o", info.Mode().Perm(), dir.Mode().Perm())
	}
}

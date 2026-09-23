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
	"time"
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

func TestReading(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/api/v1/projects?":
			io.WriteString(w, `{"projects":[{"name":"garage","status":"no_go","running_sha":"`+strings.Repeat("a", 40)+`","host":"garage.svnmns.com",
				"domains":{"equipping.com":{"state":"DNS PENDING","reason":"not yet"}},"last_deploy":{"number":2,"status":"no_go","sha":"`+strings.Repeat("b", 40)+`","ref":"refs/heads/main","error":"release hook failed (exit 3)","duration":41}}]}`)
		case "/api/v1/projects/garage?":
			io.WriteString(w, `{"name":"garage","status":"go","host":"garage.svnmns.com","domains":{},"repo_url":"git@forgejo:h/garage.git","branch":"main",
				"webhook_verified":true,"services":["app","db"],"deploy_rule":{"on":"commit","branch":"main"},
				"secrets":[{"name":"RAILS_MASTER_KEY","required":true,"set":false}]}`)
		case "/api/v1/projects/garage/deploys?page=1":
			io.WriteString(w, `{"deploys":[{"number":2,"status":"go","sha":"`+strings.Repeat("c", 40)+`","ref":"refs/heads/main","runner":"houston-runner-1","duration":90}]}`)
		case "/api/v1/projects/garage/deploys/latest?log_from=0":
			io.WriteString(w, `{"number":2,"status":"in_flight","sha":"`+strings.Repeat("c", 40)+`","ref":"refs/heads/main","step":"Build",
				"steps":[{"name":"Test","state":"done"},{"name":"Build","state":"current"}],"log":"hello\n","log_size":6,"log_next":6}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"no project nope"}`)
		}
	}))
	defer s.Close()
	c := New(Config{URL: s.URL, Token: "hou_x"})
	ctx := context.Background()

	projects, err := c.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Domains["equipping.com"].State != "DNS PENDING" || projects[0].LastDeploy.Error != "release hook failed (exit 3)" {
		t.Errorf("Projects = %+v, %v", projects, err)
	}
	p, err := c.Project(ctx, "garage")
	if err != nil || !p.WebhookVerified || p.Secrets[0].Name != "RAILS_MASTER_KEY" || p.Secrets[0].Set || p.DeployRule.On != "commit" {
		t.Errorf("Project = %+v, %v", p, err)
	}
	deploys, err := c.Deploys(ctx, "garage", 1)
	if err != nil || len(deploys) != 1 || deploys[0].Runner != "houston-runner-1" {
		t.Errorf("Deploys = %+v, %v", deploys, err)
	}
	d, err := c.Deploy(ctx, "garage", "latest", 0)
	if err != nil || d.Log != "hello\n" || d.LogNext != 6 || d.Steps[1].State != "current" {
		t.Errorf("Deploy = %+v, %v", d, err)
	}
	if _, err := c.Project(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "no project nope") {
		t.Errorf("unknown project: %v", err)
	}
}

func TestBackupsClient(t *testing.T) {
	var posted string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/projects/equip/snapshots":
			io.WriteString(w, `{"snapshots":[{"id":"`+strings.Repeat("3", 64)+`","short_id":"33333333","time":"2026-09-22T12:31:00Z","kind":"deploy","reason":"deploy","deploy":7,"sha":"`+strings.Repeat("a", 40)+`","bytes":5}]}`)
		case "POST /api/v1/projects/equip/backups":
			posted = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"id":12,"status":"queued","kind":"auto","reason":"manual"}`)
		case "GET /api/v1/projects/equip/backups/12":
			io.WriteString(w, `{"id":12,"status":"go","snapshot_id":"`+strings.Repeat("5", 64)+`","bytes":410000000,"error":"1 file couldn't be read: /data/x"}`)
		case "POST /api/v1/projects/fresh/backups":
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"error":"nothing deployed yet"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"no project nope"}`)
		}
	}))
	defer s.Close()
	c := New(Config{URL: s.URL, Token: "hou_x"})
	ctx := context.Background()

	snapshots, err := c.Snapshots(ctx, "equip")
	if err != nil || len(snapshots) != 1 || snapshots[0].ShortID != "33333333" || snapshots[0].Deploy != 7 || snapshots[0].Bytes != 5 ||
		!snapshots[0].Time.Equal(time.Date(2026, 9, 22, 12, 31, 0, 0, time.UTC)) {
		t.Errorf("Snapshots = %+v, %v", snapshots, err)
	}
	b, err := c.BackupNow(ctx, "equip")
	if err != nil || b.ID != 12 || b.Status != "queued" || posted != "Bearer hou_x" {
		t.Errorf("BackupNow = %+v, %v (auth %q)", b, err, posted)
	}
	b, err = c.Backup(ctx, "equip", "12")
	if err != nil || b.Status != "go" || b.Bytes != 410000000 || !strings.HasPrefix(b.SnapshotID, "5555") || b.Error == "" {
		t.Errorf("Backup = %+v, %v", b, err)
	}
	if _, err := c.BackupNow(ctx, "fresh"); err == nil || err.Error() != "nothing deployed yet" {
		t.Errorf("refused: %v", err)
	}
	if _, err := c.Snapshots(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "no project nope") {
		t.Errorf("unknown project: %v", err)
	}
}

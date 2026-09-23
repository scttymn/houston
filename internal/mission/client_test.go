package mission

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const token = "runner-token-for-tests-0123456789abcdef"

// server answers like Mission Control's /api (mission_control/app/controllers/api).
func server(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body map[string]any)) *Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"the runner token is missing or wrong"}`)
			return
		}
		var body map[string]any
		if data, _ := io.ReadAll(r.Body); len(data) > 0 {
			if err := json.Unmarshal(data, &body); err != nil {
				t.Errorf("%s %s: body isn't JSON: %s", r.Method, r.URL.Path, data)
			}
		}
		handle(w, r, body)
	}))
	t.Cleanup(s.Close)
	return New(s.URL, token)
}

func TestClientTalksToMissionControl(t *testing.T) {
	ctx := context.Background()
	var seen []string
	c := server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/projects/sync":
			want := map[string]any{
				"name": "equip", "app_service": "app", "services": []any{"app", "db"}, "domains": []any{},
				"variables": []any{map[string]any{"name": "RAILS_MASTER_KEY", "required": true}},
				"health":    "/up", "port": float64(80), "deploy_rule": map[string]any{"on": "commit", "branch": "main", "tags": "v*"},
			}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("sync body = %#v\nwant %#v", body, want)
			}
			io.WriteString(w, `{"project":"equip","host":"equip.svnmns.com","dns":"per_host"}`)
		case "GET /api/projects/equip/secrets/RAILS_MASTER_KEY":
			io.WriteString(w, "k3y $HOME café")
		case "GET /api/projects/equip/secrets/SENTRY_DSN":
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"no value for SENTRY_DSN"}`)
		case "POST /api/projects/equip/deploys":
			if body["sha"] != strings.Repeat("a", 40) || body["ref"] != "refs/heads/main" {
				t.Errorf("start body = %v", body)
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":7,"number":3,"token":"deploy-token","took_over":2}`)
		case "PATCH /api/deploys/7":
			if r.Header.Get("X-Houston-Deploy-Token") != "deploy-token" {
				t.Errorf("report without the deploy token")
			}
			want := map[string]any{"step": "Build", "log": "line\n"}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("report body = %#v, want %#v (empty fields omitted)", body, want)
			}
			io.WriteString(w, `{"number":3,"status":"in_flight"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	res, err := c.Sync(ctx, SyncRequest{Name: "equip", AppService: "app", Services: []string{"app", "db"}, Domains: []string{},
		Variables: []Variable{{Name: "RAILS_MASTER_KEY", Required: true}}, Health: "/up", Port: 80,
		DeployRule: DeployRule{On: "commit", Branch: "main", Tags: "v*"}})
	if err != nil || res != (SyncResult{Project: "equip", Host: "equip.svnmns.com", DNS: "per_host"}) {
		t.Errorf("Sync = %+v, %v", res, err)
	}
	if v, ok, err := c.Secret(ctx, "equip", "RAILS_MASTER_KEY"); v != "k3y $HOME café" || !ok || err != nil {
		t.Errorf("Secret = %q, %v, %v", v, ok, err)
	}
	if v, ok, err := c.Secret(ctx, "equip", "SENTRY_DSN"); v != "" || ok || err != nil {
		t.Errorf("unset Secret = %q, %v, %v", v, ok, err)
	}
	d, err := c.StartDeploy(ctx, "equip", strings.Repeat("a", 40), "refs/heads/main")
	if err != nil || d.ID != 7 || d.Number != 3 || d.Token != "deploy-token" || d.TookOver != 2 {
		t.Errorf("StartDeploy = %+v, %v", d, err)
	}
	if err := c.Report(ctx, d, Progress{Step: "Build", Log: "line\n"}); err != nil {
		t.Errorf("Report: %v", err)
	}
	if len(seen) != 5 {
		t.Errorf("requests = %v", seen)
	}
}

func TestClientErrors(t *testing.T) {
	ctx := context.Background()
	c := server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/projects/sync":
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"error":"HOLD: set RAILS_MASTER_KEY and SECRET in Mission Control first","missing":["RAILS_MASTER_KEY","SECRET"]}`)
		case "POST /api/projects/equip/deploys":
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error":"deploy #4 is in flight (last heard from 2026-09-23T07:00:00Z)","number":4}`)
		case "PATCH /api/deploys/7":
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error":"deploy #3 is no longer in flight (abandoned); it was finished or taken over"}`)
		case "GET /api/projects/equip/secrets/BROKEN":
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	_, err := c.Sync(ctx, SyncRequest{Name: "equip"})
	var hold *HoldError
	if !errors.As(err, &hold) || !reflect.DeepEqual(hold.Missing, []string{"RAILS_MASTER_KEY", "SECRET"}) {
		t.Errorf("Sync HOLD: err = %v", err)
	}
	_, err = c.StartDeploy(ctx, "equip", strings.Repeat("a", 40), "refs/heads/main")
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Number != 4 {
		t.Errorf("StartDeploy busy: err = %v", err)
	}
	if err := c.Report(ctx, Deploy{ID: 7, Token: "t"}, Progress{Status: "go"}); !errors.Is(err, ErrTakenOver) {
		t.Errorf("Report after takeover: err = %v, want ErrTakenOver", err)
	}
	if _, _, err := c.Secret(ctx, "equip", "BROKEN"); err == nil {
		t.Error("Secret on a 500: want an error")
	}
	if _, err := New(c.URL, "wrong").Sync(ctx, SyncRequest{Name: "equip"}); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong token: err = %v, want ErrUnauthorized", err)
	}
}

func TestFromEnvironment(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	if _, err := FromEnvironment(getenv, home); err == nil || !strings.Contains(err.Error(), "HOUSTON_TOKEN") || !strings.Contains(err.Error(), ".config/houston/runner-token") {
		t.Errorf("no token: err = %v, want one naming HOUSTON_TOKEN and the file", err)
	}

	os.MkdirAll(filepath.Join(home, ".config", "houston"), 0o700)
	os.WriteFile(filepath.Join(home, ".config", "houston", "runner-token"), []byte("from-file\n"), 0o600)
	c, err := FromEnvironment(getenv, home)
	if err != nil || c.URL != "http://127.0.0.1:3000" || c.token != "from-file" {
		t.Errorf("from file: %+v, %v", c, err)
	}

	env["HOUSTON_TOKEN"] = "from-env"
	env["HOUSTON_URL"] = "http://mc.test:3000/"
	c, err = FromEnvironment(getenv, home)
	if err != nil || c.URL != "http://mc.test:3000" || c.token != "from-env" {
		t.Errorf("from env: %+v, %v", c, err)
	}
}

func TestClientClaims(t *testing.T) {
	ctx := context.Background()
	answer := http.StatusOK
	c := server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method+" "+r.URL.Path != "POST /api/runner/jobs/claim" || body["runner"] != "houston-runner-1" || body["wait"] != float64(25) {
			t.Errorf("claim request: %s %s %v", r.Method, r.URL.Path, body)
		}
		w.WriteHeader(answer)
		if answer == http.StatusOK {
			io.WriteString(w, `{"deploy":{"id":7,"number":3,"token":"tok","sha":"`+strings.Repeat("a", 40)+`","ref":"refs/heads/main","took_over":2},
				"project":{"name":"garage","repo_url":"git@forgejo:houston/garage.git","branch":"main","compose_path":"compose.yml","deploy_key":"KEY"},
				"known_hosts":["forgejo ssh-ed25519 AAAA"]}`)
		}
	})

	job, ok, err := c.Claim(ctx, "houston-runner-1", 25)
	if err != nil || !ok {
		t.Fatalf("Claim = %v, %v", ok, err)
	}
	want := Job{
		Deploy:     Deploy{ID: 7, Number: 3, Token: "tok", TookOver: 2},
		SHA:        strings.Repeat("a", 40),
		Ref:        "refs/heads/main",
		Project:    JobProject{Name: "garage", RepoURL: "git@forgejo:houston/garage.git", Branch: "main", ComposePath: "compose.yml", DeployKey: "KEY"},
		KnownHosts: []string{"forgejo ssh-ed25519 AAAA"},
	}
	if !reflect.DeepEqual(job, want) {
		t.Errorf("job =\n%#v\nwant\n%#v", job, want)
	}

	answer = http.StatusNoContent
	if _, ok, err := c.Claim(ctx, "houston-runner-1", 25); ok || err != nil {
		t.Errorf("204: ok=%v err=%v", ok, err)
	}
	if _, _, err := New(c.URL, "wrong").Claim(ctx, "houston-runner-1", 25); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong token: %v", err)
	}
}

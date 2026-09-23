package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeployServer(t *testing.T) {
	var posted atomic.Bool
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/garage/deploys": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method %s", r.Method)
			}
			posted.Store(true)
			io.WriteString(w, `{"number":9,"status":"queued","sha":"`+strings.Repeat("d", 40)+`","ref":"refs/heads/main"}`)
		},
		"/api/v1/projects/garage/deploys/9": respond(`{"number":9,"status":"go","sha":"` + strings.Repeat("d", 40) + `","ref":"refs/heads/main","log":"deployed\n","log_next":9}`),
	})
	followEvery = time.Millisecond

	code, out, errOut := run(&fakeDocker{}, "deploy", "--server", "--project", "garage")
	if code != 0 || !posted.Load() || !strings.Contains(out, "#9") || !strings.Contains(out, "ddddddd") {
		t.Errorf("deploy --server: exit %d\n%s%s", code, out, errOut)
	}
	code, out, _ = run(&fakeDocker{}, "deploy", "--server", "--project", "garage", "--follow")
	if code != 0 || !strings.Contains(out, "deployed\n") || !strings.Contains(out, "GO") {
		t.Errorf("--follow: exit %d\n%s", code, out)
	}
}

func TestLink(t *testing.T) {
	var accessChecks atomic.Int32
	accessOK := func() bool { return accessChecks.Add(1) >= 3 }
	var readBody, createBody map[string]string
	routes := map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/links": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&createBody)
			io.WriteString(w, `{"id":5,"deploy_key":"ssh-ed25519 AAAAkey houston@svnmns.com","access":{"ok":false,"message":"git@forgejo: Permission denied (publickey). Add the deploy key above…"}}`)
		},
		"/api/v1/links/5/access": func(w http.ResponseWriter, r *http.Request) {
			if accessOK() {
				io.WriteString(w, `{"ok":true,"message":"Houston can read the repo"}`)
			} else {
				io.WriteString(w, `{"ok":false,"message":"Permission denied (publickey)"}`)
			}
		},
		"/api/v1/links/5/read": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&readBody)
			io.WriteString(w, `{"ok":true,"found":{"name":"garage","sha":"`+strings.Repeat("e", 40)+`","branch":"main","domains":[],"variables":[{"name":"RAILS_MASTER_KEY","required":true}]}}`)
		},
		"/api/v1/links/5/save": respond(`{"project":"garage","webhook_url":"https://hooks.svnmns.com/garage","webhook_secret":"whsec-abc"}`),
	}
	remoteServer(t, routes)
	linkEvery = time.Millisecond

	code, out, errOut := run(&fakeDocker{}, "link", "git@forgejo:houston/garage.git")
	if code != exitFailure || !strings.Contains(out, "ssh-ed25519 AAAAkey") || !strings.Contains(errOut, "houston link --continue 5") {
		t.Errorf("access denied: exit %d\n%s%s", code, out, errOut)
	}
	if createBody["repo_url"] != "git@forgejo:houston/garage.git" {
		t.Errorf("create body = %v", createBody)
	}

	code, out, errOut = run(&fakeDocker{}, "link", "--continue", "5", "--wait", "--branch", "trunk", "--file", "deploy/compose.yml")
	if code != 0 {
		t.Fatalf("continue --wait: exit %d\n%s%s", code, out, errOut)
	}
	if readBody["branch"] != "trunk" || readBody["compose_path"] != "deploy/compose.yml" {
		t.Errorf("read body = %v", readBody)
	}
	for _, want := range []string{"garage", "https://hooks.svnmns.com/garage", "whsec-abc", "RAILS_MASTER_KEY"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	routes["/api/v1/links/5/read"] = respond(`{"ok":false,"problems":"compose.yml: x-houston.health: must start with /\n"}`)
	code, _, errOut = run(&fakeDocker{}, "link", "--continue", "5")
	if code != exitUsage || !strings.Contains(errOut, "x-houston.health") {
		t.Errorf("read problems: exit %d, %q", code, errOut)
	}
}

func TestWebhook(t *testing.T) {
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/garage/webhook":        respond(`{"url":"https://hooks.svnmns.com/garage","verified":true,"secret":null}`),
		"/api/v1/projects/garage/webhook/rotate": respond(`{"url":"https://hooks.svnmns.com/garage","verified":false,"secret":"whsec-new"}`),
	})
	code, out, _ := run(&fakeDocker{}, "webhook", "--project", "garage")
	if code != 0 || !strings.Contains(out, "https://hooks.svnmns.com/garage") || !strings.Contains(out, "verified") {
		t.Errorf("webhook: exit %d\n%s", code, out)
	}
	code, out, _ = run(&fakeDocker{}, "webhook", "--project", "garage", "--rotate")
	if code != 0 || !strings.Contains(out, "whsec-new") {
		t.Errorf("--rotate: exit %d\n%s", code, out)
	}
}

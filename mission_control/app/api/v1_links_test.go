package api_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/scttymn/houston/mission_control/app"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
	"github.com/scttymn/houston/mission_control/app/services/gitremote"
)

const shopCompose = `name: shop
services:
  web:
    build: .
    ports: ["3000:3000"]
    environment:
      SECRET_KEY_BASE: ${SECRET_KEY_BASE}
x-houston:
  health: /up
  domains: [shop.example.com]
`

// repo is a git that can read a repo: ls-remote shows main, a clone's
// checkout of the compose file writes compose into it.
func repo(t *testing.T, a *app.App, compose string, access bool) {
	t.Helper()
	a.Git = gitremote.Git{KnownHosts: "/data/known_hosts", TempDir: t.TempDir(), Run: func(ctx context.Context, argv []string, env map[string]string) dockercmd.Result {
		line := strings.Join(argv, " ")
		switch {
		case strings.Contains(line, "ls-remote") && !access:
			return dockercmd.Result{Output: "git@github.com: Permission denied (publickey).\n", Code: 128}
		case strings.Contains(line, "ls-remote"):
			return dockercmd.Result{OK: true, Output: sha1 + "\trefs/heads/main\n"}
		case strings.Contains(line, " checkout HEAD -- "):
			checkout := argv[2]
			os.MkdirAll(checkout, 0o755)
			os.WriteFile(filepath.Join(checkout, argv[len(argv)-1]), []byte(compose), 0o644)
		case strings.Contains(line, "rev-parse"):
			return dockercmd.Result{OK: true, Output: sha1 + "\n"}
		}
		return dockercmd.Result{OK: true}
	}}
}

// Add project: a deploy key for the repo, access checked, the compose file
// read, then the project saved from it and linked.
func TestV1AddProject(t *testing.T) {
	a, _ := remote(t)
	repo(t, a, shopCompose, false)
	h := a.Handler()
	is(t, v1(h, "POST", "/links", personal, `{"repo_url":"/srv/shop.git"}`, nil), 422,
		`{"error":"the repo URL must be an ssh://, https:// or user@host:path repo URL (no credentials, options or local paths)"}`)
	w := v1(h, "POST", "/links", personal, `{"repo_url":" git@github.com:scttymn/shop.git "}`, nil)
	started := answer(t, w)
	key, _ := started["deploy_key"].(string)
	if w.Code != 200 || !strings.HasPrefix(key, "ssh-ed25519 ") || !strings.HasSuffix(key, " houston@svnmns.com") ||
		started["access"].(map[string]any)["ok"] != false || !strings.Contains(started["access"].(map[string]any)["message"].(string), "Add the deploy key") {
		t.Fatalf("started %d %s", w.Code, w.Body.String())
	}
	id := int(started["id"].(float64))
	path := func(what string) string { return "/links/" + strconv.Itoa(id) + "/" + what }

	repo(t, a, shopCompose, true)
	h = a.Handler()
	is(t, v1(h, "POST", path("access"), personal, "", nil), 200, `{"ok":true,"message":"Houston can read the repo"}`)
	is(t, v1(h, "POST", path("save"), personal, "", nil), 422, `{"error":"Read the file first: Houston saves what it read from the repo."}`)
	is(t, v1(h, "POST", path("read"), personal, `{"branch":"a..b"}`, nil), 422, `{"error":"Branch isn't a branch name git accepts"}`)
	w = v1(h, "POST", path("read"), personal, `{}`, nil)
	var read struct {
		OK    bool
		Found struct {
			Name, Sha, Branch string
			Domains           []string
			Variables         []struct{ Name string }
		}
	}
	json.Unmarshal(w.Body.Bytes(), &read)
	if !read.OK || read.Found.Name != "shop" || read.Found.Sha != sha1 || read.Found.Branch != "main" || len(read.Found.Variables) != 1 {
		t.Fatalf("read %s", w.Body.String())
	}

	w = v1(h, "POST", path("save"), personal, "", nil)
	saved := answer(t, w)
	if w.Code != 200 || saved["project"] != "shop" || saved["webhook_url"] != "https://hooks.svnmns.com/shop" || len(saved["webhook_secret"].(string)) < 40 {
		t.Fatalf("saved %d %s", w.Code, w.Body.String())
	}
	var url, branch, public string
	a.DB.Read.QueryRow(`SELECT repo_url, branch, deploy_key_public FROM projects WHERE name = 'shop'`).Scan(&url, &branch, &public)
	if url != "git@github.com:scttymn/shop.git" || branch != "main" || public != key {
		t.Errorf("project %s %s %s", url, branch, public)
	}
	is(t, v1(h, "POST", path("read"), personal, "", nil), 404, `{"error":"no link `+strconv.Itoa(id)+` (drafts last a day)"}`)

	// A compose file with problems: they're what the read answers.
	w = v1(h, "POST", "/links", personal, `{"repo_url":"git@github.com:scttymn/other.git"}`, nil)
	other := int(answer(t, w)["id"].(float64))
	repo(t, a, "name: Bad Name\n", true)
	h = a.Handler()
	got := answer(t, v1(h, "POST", "/links/"+strconv.Itoa(other)+"/read", personal, "", nil))
	if got["ok"] != false || got["problems"] == "" || strings.Contains(got["problems"].(string), "houston-git") {
		t.Errorf("problems %v", got)
	}

	// Another repo can't link a project that's linked.
	repo(t, a, shopCompose, true)
	h = a.Handler()
	v1(h, "POST", "/links/"+strconv.Itoa(other)+"/read", personal, "", nil)
	is(t, v1(h, "POST", "/links/"+strconv.Itoa(other)+"/save", personal, "", nil), 422, `{"error":"shop is already linked to git@github.com:scttymn/shop.git"}`)
}

// Relink: the repo's new address, read with the same key, naming the
// same project.
func TestV1MoveRepo(t *testing.T) {
	a, _ := remote(t)
	exec(t, a, `UPDATE projects SET repo_url = 'git@github.com:scttymn/shop.git', compose_path = 'compose.yml', branch = 'main'`)
	repo(t, a, shopCompose, true)
	h := a.Handler()
	is(t, v1(h, "PUT", "/projects/shop/repo", personal, `{"repo_url":"git@github.com:scttymn/shop.git"}`, nil), 422, `{"error":"shop's repo is already git@github.com:scttymn/shop.git"}`)
	is(t, v1(h, "PUT", "/projects/shop/repo", personal, `{"repo_url":"git@github.com:acme/shop.git"}`, nil), 200, `{"repo_url":"git@github.com:acme/shop.git"}`)
	repo(t, a, strings.Replace(shopCompose, "name: shop", "name: blog", 1), true)
	h = a.Handler()
	is(t, v1(h, "PATCH", "/projects/shop/repo", personal, `{"repo_url":"git@github.com:acme/blog.git"}`, nil), 422,
		`{"error":"git@github.com:acme/blog.git's compose.yml there names blog, not shop"}`)
	repo(t, a, shopCompose, false)
	h = a.Handler()
	w := v1(h, "PATCH", "/projects/shop/repo", personal, `{"repo_url":"git@github.com:acme/private.git"}`, nil)
	if w.Code != 422 || !strings.HasPrefix(answer(t, w)["error"].(string), "git@github.com:acme/private.git: git@github.com: Permission denied") {
		t.Errorf("no access: %s", w.Body.String())
	}
}

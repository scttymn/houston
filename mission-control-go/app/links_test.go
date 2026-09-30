package app_test

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
)

const shopCompose = `name: shop
services:
  web:
    build: .
    ports: ["3000:3000"]
    environment:
      SECRET_KEY_BASE: ${SECRET_KEY_BASE}
      SENTRY_DSN: ${SENTRY_DSN:-}
  db:
    image: postgres:17
x-houston:
  health: /up
  domains: [shop.example.com]
`

// repo is a git that can read a repo when access is on: ls-remote shows
// main, a checkout of the compose file writes compose into it.
func repo(t *testing.T, a *app.App, compose string, access bool) {
	t.Helper()
	a.Git = gitremote.Git{KnownHosts: "/data/known_hosts", TempDir: t.TempDir(), Run: func(ctx context.Context, argv []string, env map[string]string) dockercmd.Result {
		line := strings.Join(argv, " ")
		switch {
		case !access && (strings.Contains(line, "ls-remote") || strings.Contains(line, "clone")):
			return dockercmd.Result{Output: "git@github.com: Permission denied (publickey).\n", Code: 128}
		case strings.Contains(line, "ls-remote"):
			return dockercmd.Result{OK: true, Output: sha + "\trefs/heads/main\n"}
		case strings.Contains(line, " checkout HEAD -- "):
			checkout := argv[2]
			os.MkdirAll(checkout, 0o755)
			os.WriteFile(filepath.Join(checkout, argv[len(argv)-1]), []byte(compose), 0o644)
		case strings.Contains(line, "rev-parse"):
			return dockercmd.Result{OK: true, Output: sha + "\n"}
		}
		return dockercmd.Result{OK: true}
	}}
}

func TestAddProject(t *testing.T) {
	a, b := signedIn(t)
	must(t, a, `INSERT INTO installations (id, base_domain) VALUES (1, 'svnmns.com')`)
	repo(t, a, shopCompose, false)
	b.h = a.Handler()
	page := b.do("GET", "/link", nil).Body.String()
	contains(t, page, "<title>Add project · Mission Control</title>", `<input type="submit" name="commit" value="Check access" class="button button--medium">`,
		"Read compose.yml to see what Houston found")
	if strings.Contains(page, "DEPLOY KEY") {
		t.Error("a draft before asking")
	}
	w := b.do("POST", "/link/access", url.Values{"repo_url": {"/srv/shop.git"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `<span class="field__error">The repo URL must be an ssh://, https:// or user@host:path repo URL`) ||
		!strings.Contains(w.Body.String(), `value="/srv/shop.git"`) {
		t.Fatalf("a bad URL = %d", w.Code)
	}
	page = b.do("POST", "/link/access", url.Values{"repo_url": {" git@github.com:scttymn/shop.git "}}).Body.String()
	contains(t, page, `<span class="mono eyebrow">READ ACCESS · DEPLOY KEY</span>`, `<pre class="mono deploy-key" data-clipboard-target="source">ssh-ed25519 `,
		`<span class="mono check__state check__state--nogo">NO-GO</span>`, `value="main"`, `value="compose.yml"`, `disabled>Save</button>`)
	key := page[strings.Index(page, "ssh-ed25519 "):]
	key = key[:strings.Index(key, "<")]

	// Access granted: the same draft (the same key).
	repo(t, a, shopCompose, true)
	b.h = a.Handler()
	page = b.do("POST", "/link/access", url.Values{"repo_url": {"git@github.com:scttymn/shop.git"}}).Body.String()
	contains(t, page, key, `<span class="mono check__state check__state--go">GO</span>`)
	if n := count(t, a, "repo_links"); n != 1 {
		t.Errorf("%d drafts", n)
	}

	w = b.do("POST", "/link/read", url.Values{"branch": {"a..b"}, "compose_path": {"/etc/passwd"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "The branch isn&#39;t a branch name git accepts") ||
		!strings.Contains(w.Body.String(), "The config path must be a .yml or .yaml path inside the repo") {
		t.Fatalf("bad fields = %d", w.Code)
	}
	page = b.do("POST", "/link/read", url.Values{"branch": {"main"}, "compose_path": {"compose.yml"}}).Body.String()
	contains(t, page, `<span class="mono panel__meta muted">main @ 1111111</span>`, `<span class="mono">shop</span> → shop.svnmns.com`,
		`<span>shop.svnmns.com</span><span>shop.example.com</span>`, `<dd>Port 3000 · health /up</dd>`, `<dd>db · postgres:17</dd>`,
		`<dd>Every commit to main</dd>`, `name="secrets[SECRET_KEY_BASE]" id="secret_SECRET_KEY_BASE" class="mono" autocomplete="off" placeholder="Required"`,
		`placeholder="Optional"`, `https://hooks.svnmns.com/shop`, `data-webhook-secret data-clipboard-target="source">`)
	if strings.Contains(page, "disabled>Save") {
		t.Error("Save disabled after a read")
	}

	w = b.do("POST", "/link", url.Values{"secrets[SECRET_KEY_BASE]": {`a\b`}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `<span class="field__error">SECRET_KEY_BASE can&#39;t contain a backslash`) {
		t.Fatalf("a secret refused = %d", w.Code)
	}
	w = b.do("POST", "/link", url.Values{"secrets[SECRET_KEY_BASE]": {"skb"}, "secrets[SENTRY_DSN]": {""}, "secrets[NOPE]": {"x"}})
	if w.Code != 303 || location(w) != "/projects/shop" {
		t.Fatalf("save = %d %v\n%s", w.Code, w.Header(), w.Body.String())
	}
	contains(t, b.do("GET", "/projects/shop", nil).Body.String(), "shop is linked to git@github.com:scttymn/shop.git.")
	var keys []string
	rows, _ := a.DB.Read.Query(`SELECT key FROM secrets ORDER BY key`)
	for rows.Next() {
		var k string
		rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	if strings.Join(keys, ",") != "SECRET_KEY_BASE" || count(t, a, "repo_links") != 0 {
		t.Errorf("secrets %v, %d drafts", keys, count(t, a, "repo_links"))
	}
	for _, c := range b.jar {
		if c.Name == "repo_link" {
			t.Error("the draft's cookie outlived Save")
		}
	}

	// Again, for the project that's there now: it's updated, then deployed.
	b.do("POST", "/link/access", url.Values{"repo_url": {"git@github.com:scttymn/shop.git"}})
	page = b.do("POST", "/link/read", url.Values{"branch": {"main"}, "compose_path": {"compose.yml"}}).Body.String()
	shopRow, _ := models.New(a.DB.Read).ProjectByName(t.Context(), "shop")
	projectSecret := shopRow.WebhookSecret.Reveal()
	contains(t, page, `<span class="mono">UPDATE</span>`, `<a href="/projects/shop">shop</a> is already a project.`,
		`data-webhook-secret data-clipboard-target="source">`+projectSecret+`</span>`) // its own, so its webhook keeps working
	w = b.do("POST", "/link", url.Values{"deploy": {"1"}})
	if w.Code != 303 || location(w) != "/projects/shop/deploys/1" {
		t.Fatalf("deploy = %d %v", w.Code, w.Header())
	}

	// Opening the page again ends a draft.
	b.do("POST", "/link/access", url.Values{"repo_url": {"git@github.com:scttymn/wiki.git"}})
	b.do("GET", "/link", nil)
	if n := count(t, a, "repo_links"); n != 0 {
		t.Errorf("%d drafts after opening the page again", n)
	}

	// Cancel ends the draft.
	b.do("POST", "/link/access", url.Values{"repo_url": {"git@github.com:scttymn/blog.git"}})
	if w := b.do("POST", "/link", url.Values{"_method": {"delete"}}); w.Code != 303 || location(w) != "/" || count(t, a, "repo_links") != 0 {
		t.Errorf("cancel = %d %v, %d drafts", w.Code, w.Header(), count(t, a, "repo_links"))
	}
	if w := b.do("POST", "/link/read", url.Values{"branch": {"main"}, "compose_path": {"compose.yml"}}); w.Code != 303 || location(w) != "/link" {
		t.Errorf("read without a draft = %d %v", w.Code, w.Header())
	}
}

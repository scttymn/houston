package app_test

import (
	"strings"
	"testing"
	"time"
)

// A project's hostnames answer only its maintenance page while it's in
// maintenance, whatever the method and path; a plain 404 otherwise.
func TestMaintenancePage(t *testing.T) {
	a, b := shop(t)
	h := a.Handler()
	w := request(h, "GET", "shop.example.com", "/cart", "172.18.0.5", nil)
	if w.Code != 404 || w.Body.Len() != 0 {
		t.Errorf("not in maintenance = %d %q", w.Code, w.Body.String())
	}
	must(t, a, `UPDATE projects SET maintenance_since = ?, maintenance_message = 'Back <soon>'`, time.Now())
	for _, c := range []struct{ method, host, path string }{{"GET", "shop.example.com", "/"}, {"POST", "SHOP.svnmns.com", "/checkout"}, {"GET", "shop.example.com", "/sign-in"}} {
		w := request(h, c.method, c.host, c.path, "172.18.0.5", nil)
		page := w.Body.String()
		if w.Code != 503 || w.Header().Get("Retry-After") != "60" || w.Header().Get("Cache-Control") != "no-store" ||
			!strings.Contains(page, "<title>shop · down for maintenance</title>") || !strings.Contains(page, `<p class="message">Back &lt;soon&gt;</p>`) ||
			!strings.Contains(page, `<div class="logo" role="img" aria-label="Houston"><svg`) || strings.Contains(page, "<!--") || strings.Contains(page, "topbar") {
			t.Errorf("%s %s%s = %d %v\n%s", c.method, c.host, c.path, w.Code, w.Header(), page)
		}
	}
	// admin.<base> and hooks.<base> are Mission Control still, even named
	// among a project's domains.
	must(t, a, `UPDATE projects SET domains = '["shop.example.com","admin.svnmns.com","hooks.svnmns.com"]'`)
	if w := request(h, "GET", "admin.svnmns.com", "/", "172.18.0.5", nil); w.Code != 302 {
		t.Errorf("admin. = %d", w.Code)
	}
	if w := request(h, "GET", "hooks.svnmns.com", "/ping", "172.18.0.5", nil); w.Code != 200 || w.Body.String() != a.Identity {
		t.Errorf("hooks. = %d", w.Code)
	}

	// The project's own page, its placeholders filled in and escaped.
	must(t, a, `UPDATE projects SET maintenance_page = '<h1>{{project}} is resting</h1><p>{{message}}</p>'`)
	w = request(h, "GET", "shop.example.com", "/", "172.18.0.5", nil)
	if w.Code != 503 || w.Body.String() != `<h1>shop is resting</h1><p>Back &lt;soon&gt;</p>` {
		t.Errorf("its own page = %d %q", w.Code, w.Body.String())
	}

	// The preview, on the project page: sandboxed.
	w = b.do("GET", "/projects/shop/maintenance/preview", nil)
	if w.Code != 200 || w.Header().Get("Content-Security-Policy") != "sandbox" || w.Body.String() != `<h1>shop is resting</h1><p>Back &lt;soon&gt;</p>` {
		t.Errorf("preview = %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	must(t, a, `UPDATE projects SET maintenance_page = '', maintenance_message = '', maintenance_since = NULL`)
	if page := b.do("GET", "/projects/shop/maintenance/preview", nil).Body.String(); !strings.Contains(page, `<p class="message">(your message)</p>`) {
		t.Errorf("default preview:\n%s", page)
	}
}

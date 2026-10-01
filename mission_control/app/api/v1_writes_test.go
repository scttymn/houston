package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission_control/app/models"
)

func TestV1Settings(t *testing.T) {
	a, h := remote(t)
	for _, method := range []string{"PATCH", "PUT"} {
		is(t, v1(h, method, "/settings", personal, `{"time_zone":"America/Denver"}`, nil), 200, `{"base_domain":"svnmns.com","time_zone":"America/Denver"}`)
	}
	for _, zone := range []string{"Mars/Base", "", "Local", "america/denver"} {
		is(t, v1(h, "PATCH", "/settings", personal, `{"time_zone":"`+zone+`"}`, nil), 422,
			`{"error":"Time zone `+zone+` isn't a time zone (use an IANA name, like Europe/Berlin)"}`)
	}
	var zone string
	a.DB.Read.QueryRow(`SELECT time_zone FROM installations`).Scan(&zone)
	if zone != "America/Denver" {
		t.Errorf("zone %s", zone)
	}
}

// Secrets are write-only: set, generated or removed, a value never comes
// back; one Kamal can't carry to the container is refused.
func TestV1Secrets(t *testing.T) {
	a, h := remote(t)
	exec(t, a, `UPDATE projects SET variables = '[{"name":"API_KEY","required":true},{"name":"SECRET_KEY_BASE","required":true}]'`)
	value := func(key string) string {
		values, _ := models.Secrets(t.Context(), models.New(a.DB.Read), 1)
		return values[key]
	}
	is(t, v1(h, "PUT", "/projects/shop/secrets/API_KEY", personal, `{"value":"abc 123"}`, nil), 200, `{"name":"API_KEY","set":true}`)
	is(t, v1(h, "PATCH", "/projects/shop/secrets/API_KEY", personal, `{"value":"def"}`, nil), 200, `{"name":"API_KEY","set":true}`)
	if value("API_KEY") != "def" {
		t.Errorf("= %q", value("API_KEY"))
	}
	for body, msg := range map[string]string{
		`{"value":""}`:           "API_KEY needs a value",
		`{}`:                     "API_KEY needs a value",
		`{"value":"a\\nb"}`:      "API_KEY can't contain a backslash, line break, tab or other control character: Kamal would change it on the way to the app. Base64-encode it instead.",
		`{"value":"C:\\\\path"}`: "API_KEY can't contain a backslash, line break, tab or other control character: Kamal would change it on the way to the app. Base64-encode it instead.",
		`{"value":"\t"}`:         "API_KEY needs a value and can't contain a backslash, line break, tab or other control character: Kamal would change it on the way to the app. Base64-encode it instead.",
	} {
		is(t, v1(h, "PUT", "/projects/shop/secrets/API_KEY", personal, body, nil), 422, `{"error":`+jsonString(msg)+`}`)
	}
	if value("API_KEY") != "def" {
		t.Errorf("a refused value was kept: %q", value("API_KEY"))
	}
	is(t, v1(h, "PUT", "/projects/shop/secrets/API_KEY", personal, `{"value":"`+strings.Repeat("v", 64<<10)+`"}`, nil), 413, `{"error":"a value can be at most 64 KiB"}`)

	is(t, v1(h, "POST", "/projects/shop/secrets/SECRET_KEY_BASE/generate", personal, "", nil), 200, `{"name":"SECRET_KEY_BASE","set":true}`)
	if g := value("SECRET_KEY_BASE"); len(g) != 86 || strings.ContainsAny(g, "+/=") {
		t.Errorf("generated %q", g)
	}
	is(t, v1(h, "DELETE", "/projects/shop/secrets/API_KEY", personal, "", nil), 200, `{"name":"API_KEY","set":false}`)
	if value("API_KEY") != "" {
		t.Error("not removed")
	}
	is(t, v1(h, "PUT", "/projects/shop/secrets/OTHER", personal, `{"value":"x"}`, nil), 404, `{"error":"shop's compose.yml doesn't reference OTHER"}`)
	is(t, v1(h, "PUT", "/projects/nope/secrets/API_KEY", personal, `{"value":"x"}`, nil), 404, `{"error":"no project nope"}`)
}

// A webhook's secret is rotated: shown again until a push verifies it.
func TestV1RotateWebhook(t *testing.T) {
	a, h := remote(t)
	is(t, v1(h, "POST", "/projects/shop/webhook/rotate", personal, "", nil), 422, `{"error":"shop isn't linked to a repo (houston link)"}`)
	exec(t, a, `UPDATE projects SET repo_url = 'git@github.com:scttymn/shop.git', webhook_secret = ?, webhook_verified_at = CURRENT_TIMESTAMP`, crypt.Of("old"))
	got := answer(t, v1(h, "POST", "/projects/shop/webhook/rotate", personal, "", nil))
	secret, _ := got["secret"].(string)
	if got["verified"] != false || len(secret) != 43 || secret == "old" {
		t.Errorf("rotated %v", got)
	}
}

// Maintenance is the admin's switch: on routes the project's hostnames to
// Mission Control's page at the tunnel, off routes them back; a toggle
// Cloudflare refuses is rolled back.
func TestV1Maintenance(t *testing.T) {
	a, h, f := throughCloudflare(t)
	exec(t, a, `INSERT INTO api_tokens (name, token_digest) VALUES ('laptop', ?)`, models.Digest(personal))
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}}), nil)
	w := v1(h, "PUT", "/projects/shop/maintenance", personal, `{"on":true,"message":"Back at noon"}`, nil)
	got := answer(t, w)
	if w.Code != 200 || got["on"] != true || got["by"] != "token laptop" || got["message"] != "Back at noon" || got["since"] == nil {
		t.Fatalf("on: %d %s", w.Code, w.Body.String())
	}
	if routes := f.Tunnel("acct", "tun"); !strings.Contains(routes, `"hostname":"shop.svnmns.com"`) {
		t.Errorf("routes %s", routes)
	}
	// On again, a new message: on since it first was.
	again := answer(t, v1(h, "PUT", "/projects/shop/maintenance", personal, `{"on":true,"message":"Back at one"}`, nil))
	if again["since"] != got["since"] || again["message"] != "Back at one" {
		t.Errorf("on again: %v, first %v", again, got["since"])
	}
	is(t, v1(h, "PUT", "/projects/shop/maintenance", personal, `{"on":false}`, nil), 200, `{"on":false}`)
	if routes := f.Tunnel("acct", "tun"); strings.Contains(routes, "shop.svnmns.com") {
		t.Errorf("still routed %s", routes)
	}

	f.Fail("Tunnel not found")
	is(t, v1(h, "PUT", "/projects/shop/maintenance", personal, `{"on":true}`, nil), 502, `{"error":"Cloudflare said no: Tunnel not found"}`)
	var on bool
	a.DB.Read.QueryRow(`SELECT maintenance_since IS NOT NULL FROM projects WHERE name = 'shop'`).Scan(&on)
	if on {
		t.Error("the refused toggle stayed")
	}
	f.Fail("")
	is(t, v1(h, "PUT", "/projects/shop/maintenance", personal, `{"on":true,"message":"`+strings.Repeat("m", 501)+`"}`, nil), 422,
		`{"error":"Maintenance message is too long (maximum is 500 characters)"}`)
	exec(t, a, `UPDATE installations SET tunnel_id = ''`)
	is(t, v1(h, "PUT", "/projects/shop/maintenance", personal, `{"on":true}`, nil), 502, `{"error":"Cloudflare isn't connected, so there's no tunnel to route through"}`)
}

// Where a volume lives is chosen until Houston makes it.
func TestV1ChooseVolume(t *testing.T) {
	a, h := remote(t)
	exec(t, a, `UPDATE projects SET volumes = '[{"name":"data","path":"/data"}]'`)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, acknowledged_at) VALUES (1, 'nas', 'nfs', CURRENT_TIMESTAMP), (2, 'offsite', 's3', CURRENT_TIMESTAMP), (3, 'new', 'local', NULL)`)
	is(t, v1(h, "PUT", "/projects/shop/volumes/data", personal, `{"location":"nas"}`, nil), 200, `{"name":"data","path":"/data","location":"nas","placed":false}`)
	is(t, v1(h, "PATCH", "/projects/shop/volumes/data", personal, `{"location":""}`, nil), 200, `{"name":"data","path":"/data","location":null,"placed":false}`)
	is(t, v1(h, "PUT", "/projects/shop/volumes/data", personal, `{"location":"offsite"}`, nil), 422, `{"error":"offsite can't hold live volumes (backups only)"}`)
	is(t, v1(h, "PUT", "/projects/shop/volumes/data", personal, `{"location":"new"}`, nil), 422, `{"error":"new isn't set up yet"}`)
	is(t, v1(h, "PUT", "/projects/shop/volumes/data", personal, `{"location":"nope"}`, nil), 422, `{"error":"no storage location nope"}`)
	is(t, v1(h, "PUT", "/projects/shop/volumes/logs", personal, `{"location":"nas"}`, nil), 404, `{"error":"shop has no volume logs"}`)
	exec(t, a, `UPDATE project_volumes SET placed_at = CURRENT_TIMESTAMP`)
	is(t, v1(h, "PUT", "/projects/shop/volumes/data", personal, `{"location":"nas"}`, nil), 409, `{"error":"data is already on local disk; moving a volume is a later feature"}`)
}

// A project's backups go to a location of its own once set up, else the
// default.
func TestV1BackupTarget(t *testing.T) {
	a, h := remote(t)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, is_default, acknowledged_at) VALUES (1, 'nas', 'nfs', TRUE, CURRENT_TIMESTAMP), (2, 'offsite', 's3', FALSE, CURRENT_TIMESTAMP), (3, 'new', 'local', FALSE, NULL)`)
	is(t, v1(h, "PUT", "/projects/shop/backup_target", personal, `{"location":"offsite"}`, nil), 200, `{"backup_location":"offsite"}`)
	is(t, v1(h, "PATCH", "/projects/shop/backup_target", personal, `{}`, nil), 200, `{"backup_location":"nas"}`)
	is(t, v1(h, "PUT", "/projects/shop/backup_target", personal, `{"location":"new"}`, nil), 422, `{"error":"new isn't set up yet"}`)
	is(t, v1(h, "PUT", "/projects/shop/backup_target", personal, `{"location":"nope"}`, nil), 422, `{"error":"no storage location nope"}`)
}

// A backup now: a manual one already queued is the same (a double click).
func TestV1BackupNow(t *testing.T) {
	a, h := remote(t)
	is(t, v1(h, "POST", "/projects/shop/backups", personal, "", nil), 422, `{"error":"nothing deployed yet"}`)
	deploy(t, a, "shop", 1, "deploy", "go", 1, sha1, "", 0)
	is(t, v1(h, "POST", "/projects/shop/backups", personal, "", nil), 422, `{"error":"no backup storage yet (finish setup's storage step)"}`)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, is_default, acknowledged_at) VALUES (1, 'nas', 'nfs', TRUE, CURRENT_TIMESTAMP)`)
	w := v1(h, "POST", "/projects/shop/backups", personal, "", nil)
	run := answer(t, w)
	if w.Code != 202 || run["kind"] != "auto" || run["reason"] != "manual" || run["status"] != "queued" || run["deploy"] != nil {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	if again := answer(t, v1(h, "POST", "/projects/shop/backups", personal, "", nil)); again["id"] != run["id"] || pendingJobs(t, a) != 1 {
		t.Errorf("a double click: %v, %d jobs", again, pendingJobs(t, a))
	}
}

// The default storage location, once it's set up.
func TestV1DefaultStorage(t *testing.T) {
	a, h := remote(t)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, is_default, acknowledged_at, verified_at) VALUES
		(1, 'nas', 'nfs', TRUE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (2, 'offsite', 's3', FALSE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (3, 'new', 'local', FALSE, NULL, NULL)`)
	w := v1(h, "PUT", "/storage/offsite", personal, `{"default":true}`, nil)
	if got := answer(t, w); w.Code != 200 || got["name"] != "offsite" || got["default"] != true {
		t.Errorf("= %d %s", w.Code, w.Body.String())
	}
	var defaults string
	a.DB.Read.QueryRow(`SELECT group_concat(name) FROM storage_locations WHERE is_default`).Scan(&defaults)
	if defaults != "offsite" {
		t.Errorf("defaults %s", defaults)
	}
	is(t, v1(h, "PUT", "/storage/offsite", personal, `{"default":"yes"}`, nil), 422, `{"error":"send {default: true}"}`)
	is(t, v1(h, "PUT", "/storage/new", personal, `{"default":true}`, nil), 422, `{"error":"new isn't set up yet"}`)
	is(t, v1(h, "PATCH", "/storage/nope", personal, `{"default":true}`, nil), 404, `{"error":"no storage location nope"}`)
	_, _, _ = fmt.Sprint, http.StatusOK, time.Now
}

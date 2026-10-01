package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission_control/app"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare/cloudflaretest"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission_control/test/testapp"
)

// throughCloudflare is the app with setup finished, DNS host by host
// through a fake Cloudflare (zones svnmns.com and example.com).
func throughCloudflare(t *testing.T) (*app.App, http.Handler, *cloudflaretest.Fake) {
	t.Helper()
	f := cloudflaretest.New(t)
	f.Zone("zbase", "svnmns.com", "active")
	f.Zone("zcom", "example.com", "active")
	a := testapp.New(t)
	a.Cloudflare = f.URL
	exec(t, a, `INSERT INTO installations (id, base_domain, dns_mode, cloudflare_zone_id, cloudflare_account_id, tunnel_id, cloudflare_api_token, cloudflare_connected_at)
		VALUES (1, 'svnmns.com', 'per_host', 'zbase', 'acct', 'tun', ?, ?)`, crypt.Of("cf-token"), time.Now())
	return a, a.Handler(), f
}

func names(records []cloudflare.Record) string {
	var out []string
	for _, r := range records {
		out = append(out, r.Name)
	}
	return strings.Join(out, " ")
}

// Before its first GO a project's names stay where they are; after it,
// every sync points them, and a dropped domain's record goes.
func TestSyncPointsDNS(t *testing.T) {
	a, h, f := throughCloudflare(t)
	body := payload(map[string]any{"variables": []any{}})
	is(t, post(h, "POST", "/api/projects/sync", body, nil), 200, `{"project":"shop","host":"shop.svnmns.com","dns":"after_first_go",
		"domains":{"shop.example.com":{"state":"AFTER FIRST GO","reason":"pointed here once a deploy of shop is GO"}},"generation":1}`)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}, "domains": []string{"shop.svnmns.com"}}), nil)
	post(h, "POST", "/api/projects/sync", body, nil)
	if len(f.Calls()) != 0 {
		t.Errorf("Cloudflare asked before the first GO (a domain dropped and back): %v", f.Calls())
	}

	deploy(t, a, "shop", 1, "deploy", "go", 1, sha1, "", 0)
	is(t, post(h, "POST", "/api/projects/sync", body, nil), 200, `{"project":"shop","host":"shop.svnmns.com","dns":"per_host",
		"domains":{"shop.example.com":{"state":"DNS OK","reason":null}},"generation":1}`)
	if got := names(f.Records("zbase")) + " | " + names(f.Records("zcom")); got != "shop.svnmns.com | shop.example.com" {
		t.Errorf("records %s", got)
	}

	is(t, post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}, "domains": []string{"shop.svnmns.com"}}), nil), 200,
		`{"project":"shop","host":"shop.svnmns.com","dns":"per_host","domains":{},"generation":1}`)
	if got := names(f.Records("zcom")); got != "" {
		t.Errorf("the dropped domain's record is left: %s", got)
	}

	f.Fail("Rate limited")
	is(t, post(h, "POST", "/api/projects/sync", body, nil), 502, `{"error":"Cloudflare said no while pointing shop.svnmns.com at the tunnel: Rate limited"}`)
}

// A host record Houston didn't make is someone else's: the sync is NO-GO.
func TestSyncRefusesAnotherRecord(t *testing.T) {
	a, h, f := throughCloudflare(t)
	body := payload(map[string]any{"variables": []any{}})
	post(h, "POST", "/api/projects/sync", body, nil)
	deploy(t, a, "shop", 1, "deploy", "go", 1, sha1, "", 0)
	f.Record("zbase", cloudflare.Record{Type: "A", Name: "shop.svnmns.com", Content: "1.2.3.4"})
	is(t, post(h, "POST", "/api/projects/sync", body, nil), 422,
		`{"error":"NO-GO: shop.svnmns.com already exists and Houston didn't create it (no managed-by:houston comment). Remove it in Cloudflare, then deploy again."}`)
}

// A project in maintenance keeps every hostname on the page, custom domains
// a sync added included.
func TestSyncPushesMaintenanceRoutes(t *testing.T) {
	a, h, f := throughCloudflare(t)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}}), nil)
	if f.Tunnel("acct", "tun") != "" {
		t.Error("routes pushed for a project not in maintenance")
	}
	exec(t, a, `UPDATE projects SET maintenance_since = ?`, time.Now())
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}, "domains": []string{"shop.example.com", "www.example.com"}}), nil)
	if got := f.Tunnel("acct", "tun"); !strings.Contains(got, `{"hostname":"www.example.com","service":"http://mission-control:8080"}`) {
		t.Errorf("pushed %s", got)
	}
}

// A sync that goes on makes the project's volumes; one that holds doesn't
// (where they live can still be chosen), and Docker's no is the sync's.
func TestSyncPlacesVolumes(t *testing.T) {
	a := testapp.New(t)
	docker := &dockercmdtest.Fake{}
	docker.On(dockercmdtest.Fail(1, "no such volume"), "volume", "inspect")
	a.DockerCLI = docker
	exec(t, a, `INSERT INTO installations (id, base_domain, cloudflare_connected_at) VALUES (1, 'svnmns.com', ?)`, time.Now())
	h := a.Handler()
	volumes := []map[string]string{{"name": "data", "path": "/rails/storage"}}
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"volumes": volumes}), nil)
	if len(docker.Ran()) != 0 {
		t.Errorf("a HOLD made volumes: %v", docker.Ran())
	}
	secret(t, a, "shop", "SECRET_KEY_BASE", "s3cret")
	if w := post(h, "POST", "/api/projects/sync", payload(map[string]any{"volumes": volumes}), nil); w.Code != 200 ||
		strings.Join(docker.Ran(), "\n") != "volume inspect --format {{json .Options}} shop_data\nvolume create shop_data" {
		t.Errorf("= %d %s, ran %v", w.Code, w.Body.String(), docker.Ran())
	}

	docker.On(dockercmdtest.Fail(1, "disk full"), "volume", "create")
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"volumes": []map[string]string{{"name": "logs", "path": "/logs"}}}), nil)
	is(t, post(h, "POST", "/api/projects/sync", payload(map[string]any{"volumes": []map[string]string{{"name": "logs", "path": "/logs"}}}), nil), 422,
		`{"error":"couldn't create shop_logs: disk full"}`)
}

// A restore's check that catches its project up to a restore that switched
// silently points what that restore's kept compose.yml names.
func TestSyncRestoreCheckPointsAdopted(t *testing.T) {
	a, h, f := throughCloudflare(t)
	post(h, "POST", "/api/projects/sync", payload(map[string]any{"variables": []any{}, "domains": []string{"shop.svnmns.com"}}), nil)
	deploy(t, a, "shop", 1, "deploy", "go", 1, sha1, "", 0)
	switched := deploy(t, a, "shop", 2, "restore", "no_go", 2, sha1, "", time.Hour)
	exec(t, a, `UPDATE deploys SET sync_payload = ? WHERE id = ?`, payload(map[string]any{"variables": []any{}, "domains": []string{"shop.svnmns.com", "new.example.com"}}), switched)
	checking := deploy(t, a, "shop", 3, "restore", "in_flight", 3, sha1, "restore-token", 0)

	check := payload(map[string]any{"variables": []any{}, "restore_deploy": checking, "serving_generation": 2, "serving_sha": sha1})
	if w := post(h, "POST", "/api/projects/sync", check, http.Header{"X-Houston-Deploy-Token": {"restore-token"}}); w.Code != 200 {
		t.Fatalf("= %d %s", w.Code, w.Body.String())
	}
	if got := names(f.Records("zcom")); got != "new.example.com" {
		t.Errorf("the adopted domain's record: %q", got)
	}
}

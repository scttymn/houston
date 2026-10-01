package handover_test

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare/cloudflaretest"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission_control/app/services/handover"
	"github.com/scttymn/houston/mission_control/test"
)

const (
	oldTarget = "91f46ac53058:80"
	newTarget = "6a8c0ce49a59:80"
)

// service is one of kamal-proxy's, as its state file has it.
func service(name string, hosts []string, target, health string) map[string]any {
	return map[string]any{"name": name, "options": map[string]any{"hosts": hosts, "path_prefixes": []string{"/"}, "tls_enabled": false},
		"target_options": map[string]any{
			"health_check_config": map[string]any{"path": health, "port": 0, "interval": 1_000_000_000, "timeout": 5_000_000_000, "host": ""},
			"response_timeout":    30_000_000_000, "buffer_requests": true, "buffer_responses": true, "max_memory_buffer_size": 1_048_576,
			"max_request_body_size": 0, "max_response_body_size": 0, "log_request_headers": []string{"Cache-Control", "User-Agent"},
			"log_response_headers": nil, "forward_headers": true},
		"active_targets": []string{target}}
}

// both is the usual state: equip-go serving its hosts, the copy (equip) on
// its placeholder.
func both() []map[string]any {
	return []map[string]any{service("equip-go-web", []string{"equip-go.svnmns.com", "equip.svnmns.com", "equipping.com"}, oldTarget, "/up"),
		service("equip-web", []string{"equip.houston-copy.invalid"}, newTarget, "/health")}
}

type world struct {
	db     *db.DB
	docker *dockercmdtest.Fake
	cf     *cloudflaretest.Fake
	h      handover.Handover
	copy   models.ProjectCopy
	to     models.Project
}

// setUp is equip-go copied to equip, running; the two share equip.svnmns.com
// (the copy's own name) and equipping.com. Their records are equip-go's.
func setUp(t *testing.T, state []map[string]any, refused bool) *world {
	t.Helper()
	keys, _ := crypt.ParseKeys(crypt.NewKey())
	crypt.Use(keys...)
	t.Cleanup(func() { crypt.Use() })
	w := &world{db: test.DB(t), docker: &dockercmdtest.Fake{}, cf: cloudflaretest.New(t)}
	must := func(q string, args ...any) {
		if _, err := w.db.Write.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(`INSERT INTO installations (id, base_domain, cloudflare_zone_id, cloudflare_account_id, tunnel_id, cloudflare_api_token, cloudflare_connected_at)
		VALUES (1, 'svnmns.com', 'zbase', 'acct', 'tun', ?, CURRENT_TIMESTAMP)`, crypt.Of("cf-token"))
	must(`INSERT INTO projects (id, name, app_service, services, domains, health, port) VALUES
		(1, 'equip-go', 'web', '["web"]', '["equip.svnmns.com","equipping.com"]', '/up', 80),
		(2, 'equip', 'web', '["web"]', '["equipping.com"]', '/health', 80)`)
	must(`INSERT INTO project_copies (id, project_id, from_project_id, from_name, to_name, sha, requested_by, status)
		VALUES (1, 2, 1, 'equip-go', 'equip', 'aaa', 'admin', 'running')`)
	w.cf.Zone("zbase", "svnmns.com", "active")
	w.cf.Zone("zeq", "equipping.com", "active")
	w.cf.Record("zbase", cloudflare.Record{Name: "equip.svnmns.com", Type: "CNAME", Comment: "managed-by:houston project:equip-go"})
	w.cf.Record("zeq", cloudflare.Record{Name: "equipping.com", Type: "CNAME", Comment: "managed-by:houston project:equip-go"})
	raw, _ := json.Marshal(state)
	w.docker.On(dockercmdtest.OK(string(raw)), "exec", "kamal-proxy", "cat")
	if refused {
		w.docker.On(dockercmdtest.Fail(1, "Error: host settings conflict with another service"), "exec", "kamal-proxy", "sh")
	}
	q := models.New(w.db.Read)
	inst, _ := q.CurrentInstallation(context.Background())
	w.h = handover.Handover{DB: w.db, Docker: w.docker, Installation: inst, Cloudflare: w.cf.URL}
	w.load(t)
	return w
}

func (w *world) load(t *testing.T) {
	t.Helper()
	q := models.New(w.db.Read)
	var err error
	if w.copy, err = q.CopyByID(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if w.to, err = q.ProjectByID(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
}

// scripts are the shell scripts run in kamal-proxy.
func (w *world) scripts() []string {
	var out []string
	for _, c := range w.docker.Calls() {
		if len(c.Args) > 3 && slices.Equal(c.Args[:3], []string{"exec", "kamal-proxy", "sh"}) {
			out = append(out, c.Args[len(c.Args)-1])
		}
	}
	return out
}

func (w *world) comment(zone string) string {
	return w.cf.Records(zone)[0].Comment
}

func matches(t *testing.T, pattern, s string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(s) {
		t.Errorf("%q doesn't match %s", s, pattern)
	}
}

func TestForward(t *testing.T) {
	w := setUp(t, both(), false)
	moved, err := w.h.Forward(context.Background(), w.copy, w.to)
	if err != nil || strings.Join(moved, " ") != "equip.svnmns.com equipping.com" {
		t.Fatalf("= %v, %v", moved, err)
	}
	scripts := w.scripts()
	if len(scripts) != 1 {
		t.Fatalf("scripts %q", scripts)
	}
	moves := strings.Split(scripts[0], " && ")
	if len(moves) != 4 {
		t.Fatalf("moves %q", moves)
	}
	// A catch-all to the new container while the hosts move, checked as
	// its own service is.
	matches(t, `^kamal-proxy deploy houston-handover --target `+newTarget+` --health-check-path /health `, moves[0])
	if strings.Contains(moves[0], "--host") {
		t.Errorf("the catch-all has a host: %s", moves[0])
	}
	matches(t, `^kamal-proxy deploy equip-go-web --target `+oldTarget+` --host equip-go\.svnmns\.com --health-check-path /up `, moves[1])
	if regexp.MustCompile(`equipping\.com|--host equip\.svnmns`).MatchString(moves[1]) {
		t.Errorf("the old project keeps a shared host: %s", moves[1])
	}
	matches(t, `^kamal-proxy deploy equip-web --target `+newTarget+` --host equip\.svnmns\.com --host equipping\.com --health-check-path /health `, moves[2])
	if strings.Contains(moves[2], "houston-copy.invalid") {
		t.Errorf("the placeholder stays: %s", moves[2])
	}
	if moves[3] != "kamal-proxy remove houston-handover" {
		t.Errorf("last %q", moves[3])
	}
	for _, move := range moves[1:3] {
		if !strings.Contains(move, "--health-check-interval 1000ms --health-check-timeout 5000ms --target-timeout 30000ms --buffer-requests --buffer-responses") ||
			!strings.HasSuffix(move, "--log-request-header Cache-Control --log-request-header User-Agent --forward-headers=true") {
			t.Errorf("options %s", move)
		}
		if strings.Contains(move, "--force") { // it skips the health check, and answers 503s
			t.Errorf("--force: %s", move)
		}
	}
	if w.comment("zbase") != "managed-by:houston project:equip" || w.comment("zeq") != "managed-by:houston project:equip" {
		t.Errorf("records %+v %+v", w.cf.Records("zbase"), w.cf.Records("zeq"))
	}
	w.load(t)
	if strings.Join(w.copy.HandedOver.V, " ") != "equip.svnmns.com equipping.com" || !w.copy.HandedOverAt.Valid {
		t.Errorf("copy %+v", w.copy)
	}
}

// An old project left with no host of its own keeps a placeholder, never
// becoming kamal-proxy's catch-all.
func TestForwardPlaceholder(t *testing.T) {
	w := setUp(t, []map[string]any{service("equip-go-web", []string{"equip.svnmns.com", "equipping.com"}, oldTarget, "/up"),
		service("equip-web", []string{"equip.houston-copy.invalid"}, newTarget, "/up")}, false)
	if _, err := w.h.Forward(context.Background(), w.copy, w.to); err != nil {
		t.Fatal(err)
	}
	matches(t, `kamal-proxy deploy equip-go-web --target `+oldTarget+` --host equip-go\.houston-copy\.invalid `, w.scripts()[0])
}

func TestForwardNothingShared(t *testing.T) {
	w := setUp(t, []map[string]any{service("equip-go-web", []string{"equip-go.svnmns.com"}, oldTarget, "/up"),
		service("equip-web", []string{"equip.houston-copy.invalid"}, newTarget, "/up")}, false)
	w.db.Write.Exec(`UPDATE projects SET domains = '[]'`)
	w.load(t)
	moved, err := w.h.Forward(context.Background(), w.copy, w.to)
	if err != nil || len(moved) != 0 || moved == nil {
		t.Fatalf("= %#v, %v", moved, err)
	}
	moves := strings.Split(w.scripts()[0], " && ")
	if len(moves) != 1 { // only the placeholder comes off the new service
		t.Fatalf("moves %q", moves)
	}
	matches(t, `^kamal-proxy deploy equip-web --target `+newTarget+` --host equip\.svnmns\.com `, moves[0])
	if calls := w.cf.Calls(); len(calls) != 0 {
		t.Errorf("Cloudflare was asked %q", calls)
	}
}

// A move kamal-proxy refuses is put back, and nothing else is touched.
func TestForwardRefused(t *testing.T) {
	w := setUp(t, both(), true)
	_, err := w.h.Forward(context.Background(), w.copy, w.to)
	var failed handover.Failed
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "host settings conflict") {
		t.Fatalf("= %v", err)
	}
	scripts := w.scripts()
	back := scripts[len(scripts)-1]
	if len(scripts) != 2 || !strings.Contains(back, "kamal-proxy remove houston-handover") {
		t.Fatalf("scripts %q", scripts)
	}
	matches(t, `kamal-proxy deploy equip-go-web --target `+oldTarget+` --host equip-go\.svnmns\.com --host equip\.svnmns\.com --host equipping\.com `, back)
	// The copy's own service lets go first; the old one takes its hosts back.
	if strings.Index(back, "deploy equip-web") > strings.Index(back, "deploy equip-go-web") {
		t.Errorf("order %s", back)
	}
	if w.comment("zbase") != "managed-by:houston project:equip-go" || len(w.cf.Calls()) != 0 {
		t.Errorf("Cloudflare %q", w.cf.Calls())
	}
	w.load(t)
	if w.copy.HandedOverAt.Valid {
		t.Error("handed over")
	}
}

// A DNS record that can't be moved puts the hosts, and the records moved,
// back.
func TestForwardRecordRefused(t *testing.T) {
	w := setUp(t, both(), false)
	w.cf.FailCall("PATCH /zones/zeq/dns_records/rec2", "Authentication error")
	_, err := w.h.Forward(context.Background(), w.copy, w.to)
	var failed handover.Failed
	if !errors.As(err, &failed) || err.Error() != "Cloudflare said no while moving the hosts' records: Authentication error" {
		t.Fatalf("= %v", err)
	}
	scripts := w.scripts()
	if len(scripts) != 2 { // forward, then back
		t.Fatalf("scripts %q", scripts)
	}
	matches(t, `kamal-proxy deploy equip-go-web --target `+oldTarget+` --host equip-go\.svnmns\.com --host equip\.svnmns\.com --host equipping\.com `, scripts[1])
	if w.comment("zbase") != "managed-by:houston project:equip-go" {
		t.Errorf("record %+v", w.cf.Records("zbase"))
	}
	w.load(t)
	if w.copy.HandedOverAt.Valid {
		t.Error("handed over")
	}
}

// A record that isn't the old project's is left alone.
func TestForwardOthersRecord(t *testing.T) {
	w := setUp(t, both(), false)
	rec := w.cf.Records("zeq")[0]
	w.cf.Client().Patch(context.Background(), "/zones/zeq/dns_records/"+rec.ID, map[string]string{"comment": "someone else's"}, nil)
	if _, err := w.h.Forward(context.Background(), w.copy, w.to); err != nil {
		t.Fatal(err)
	}
	if w.comment("zeq") != "someone else's" || w.comment("zbase") != "managed-by:houston project:equip" {
		t.Errorf("records %+v %+v", w.cf.Records("zbase"), w.cf.Records("zeq"))
	}
}

// Undo: the hosts go back to the old project, and their records.
func TestBack(t *testing.T) {
	w := setUp(t, []map[string]any{service("equip-go-web", []string{"equip-go.svnmns.com"}, oldTarget, "/up"),
		service("equip-web", []string{"equip.svnmns.com", "equipping.com"}, newTarget, "/up")}, false)
	w.db.Write.Exec(`UPDATE project_copies SET status = 'go', handed_over = '["equip.svnmns.com","equipping.com"]', handed_over_at = CURRENT_TIMESTAMP`)
	for _, zone := range []string{"zbase", "zeq"} {
		rec := w.cf.Records(zone)[0]
		rec.Comment = "managed-by:houston project:equip"
		w.cf.Client().Patch(context.Background(), "/zones/"+zone+"/dns_records/"+rec.ID, map[string]string{"comment": rec.Comment}, nil)
	}
	w.load(t)
	if err := w.h.Back(context.Background(), w.copy); err != nil {
		t.Fatal(err)
	}
	moves := strings.Split(w.scripts()[0], " && ")
	matches(t, `^kamal-proxy deploy houston-handover --target `+oldTarget+` `, moves[0])
	matches(t, `^kamal-proxy deploy equip-web --target `+newTarget+` --host equip\.houston-copy\.invalid `, moves[1])
	matches(t, `^kamal-proxy deploy equip-go-web --target `+oldTarget+` --host equip-go\.svnmns\.com --host equip\.svnmns\.com --host equipping\.com `, moves[2])
	if w.comment("zbase") != "managed-by:houston project:equip-go" || w.comment("zeq") != "managed-by:houston project:equip-go" {
		t.Errorf("records %+v %+v", w.cf.Records("zbase"), w.cf.Records("zeq"))
	}
	w.load(t)
	if len(w.copy.HandedOver.V) != 0 || w.copy.HandedOverAt.Valid {
		t.Errorf("copy %+v", w.copy)
	}
}

// Undo refuses when the old project's container is gone.
func TestBackGone(t *testing.T) {
	w := setUp(t, []map[string]any{service("equip-web", []string{"equip.svnmns.com"}, newTarget, "/up")}, false)
	err := w.h.Back(context.Background(), w.copy)
	if err == nil || err.Error() != "equip-go-web isn't in kamal-proxy any more: its container is gone" {
		t.Errorf("= %v", err)
	}
}

// Each service is redeployed with the options Kamal gave it: what differs
// from kamal-proxy's defaults, quoted for the shell where it must be.
func TestForwardOptions(t *testing.T) {
	old := service("equip-go-web", []string{"equip-go.svnmns.com", "equip.svnmns.com"}, oldTarget, "/up")
	options := old["target_options"].(map[string]any)
	options["max_memory_buffer_size"] = 2_097_152
	options["max_request_body_size"] = 10_485_760
	options["log_request_headers"] = []string{"X-Weird Header"}
	options["forward_headers"] = false
	options["health_check_config"].(map[string]any)["port"] = 3000
	options["health_check_config"].(map[string]any)["host"] = "equip.internal"
	w := setUp(t, []map[string]any{old, service("equip-web", []string{"equip.houston-copy.invalid"}, newTarget, "/up")}, false)
	if _, err := w.h.Forward(context.Background(), w.copy, w.to); err != nil {
		t.Fatal(err)
	}
	moves := strings.Split(w.scripts()[0], " && ")
	want := "kamal-proxy deploy equip-go-web --target " + oldTarget + " --host equip-go.svnmns.com --health-check-path /up --health-check-port 3000 " +
		"--health-check-host equip.internal --health-check-interval 1000ms --health-check-timeout 5000ms --target-timeout 30000ms " +
		"--buffer-requests --buffer-responses --buffer-memory 2097152 --max-request-body 10485760 --log-request-header 'X-Weird Header' --forward-headers=false"
	if moves[1] != want {
		t.Errorf("= %s\nwant %s", moves[1], want)
	}
	if strings.Contains(moves[2], "--buffer-memory") { // kamal-proxy's default isn't repeated
		t.Errorf("%s", moves[2])
	}
}

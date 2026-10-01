package systemstatus_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare/cloudflaretest"
	"github.com/scttymn/houston/mission-control-go/app/services/systemstatus"
)

func TestTunnel(t *testing.T) {
	keys, _ := crypt.ParseKeys(crypt.NewKey())
	crypt.Use(keys...)
	t.Cleanup(func() { crypt.Use() })
	fake := cloudflaretest.New(t)
	s := &systemstatus.Status{Cloudflare: fake.URL}
	inst := models.Installation{CloudflareAccountID: "acct", TunnelID: "tun", CloudflareApiToken: crypt.Of("cf-token")}
	now := time.Now()
	ctx := context.Background()
	if got := s.Tunnel(ctx, models.Installation{}, now); got.State != "unknown" || got.Reason != "not connected to Cloudflare" {
		t.Errorf("unconnected = %+v", got)
	}
	if got := s.Tunnel(ctx, inst, now); got.State != "unknown" || got.Reason != "Tunnel not found" || got.Go() {
		t.Errorf("no tunnel = %+v", got)
	}
	fake.TunnelDetails("acct", "tun", map[string]any{"connections": []map[string]any{}})
	if got := s.Tunnel(ctx, inst, now.Add(10*time.Second)); got.State != "unknown" {
		t.Errorf("asked again within 30 s: %+v", got)
	}
	if got := s.Tunnel(ctx, inst, now.Add(31*time.Second)); got.State != "hold" || got.Go() {
		t.Errorf("no connections = %+v", got)
	}
	fake.TunnelDetails("acct", "tun", map[string]any{"connections": []map[string]any{{}, {}}})
	s.Forget()
	if got := s.Tunnel(ctx, inst, now.Add(32*time.Second)); got.State != "go" || got.Connections != 2 || !got.Go() {
		t.Errorf("connected = %+v", got)
	}
	other := inst
	other.TunnelID = "another"
	if got := s.Tunnel(ctx, other, now.Add(33*time.Second)); got.State != "unknown" {
		t.Errorf("another tunnel had this one's answer: %+v", got)
	}
}

func TestRegistry(t *testing.T) {
	status := 401
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			status = 404
		}
		w.WriteHeader(status)
	}))
	defer registry.Close()
	s := &systemstatus.Status{Registry: registry.URL}
	now := time.Now()
	if !s.RegistryUp(context.Background(), now) {
		t.Error("a 401 is up")
	}
	status = 502
	if !s.RegistryUp(context.Background(), now.Add(10*time.Second)) {
		t.Error("asked again within 30 s")
	}
	if s.RegistryUp(context.Background(), now.Add(31*time.Second)) {
		t.Error("a 502 is down")
	}
	s2 := &systemstatus.Status{Registry: "http://127.0.0.1:1"}
	if s2.RegistryUp(context.Background(), now) {
		t.Error("nothing listening is down")
	}
}

// A route answers as this install through Cloudflare; a miss is HOLD for
// the switch-over's 10 minutes, then NO-GO, and is probed again sooner.
func TestRoute(t *testing.T) {
	answer, code := "me", 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		io.WriteString(w, answer+"\n")
	}))
	defer srv.Close()
	probes := 0
	s := &systemstatus.Status{Identity: "me", PingURL: func(host string) string {
		probes++
		if host != "admin.svnmns.com" {
			t.Errorf("probed %s", host)
		}
		return srv.URL + "/ping"
	}}
	now := time.Now()
	ctx := context.Background()
	inst := models.Installation{BaseDomain: "svnmns.com"}
	if r := s.Route(ctx, inst, "admin", false, now); r != nil {
		t.Errorf("before Cloudflare = %+v", r)
	}
	inst.CloudflareConnectedAt = sql.NullTime{Time: now.Add(-time.Minute), Valid: true}
	if r := s.Route(ctx, inst, "admin", true, now); r.State != "go" || probes != 0 {
		t.Errorf("on the host = %+v, %d probes", r, probes)
	}
	if r := s.Route(ctx, inst, "admin", false, now); r.State != "go" || probes != 1 {
		t.Errorf("= %+v", r)
	}
	answer = "someone else"
	if r := s.Route(ctx, inst, "admin", false, now.Add(29*time.Second)); r.State != "go" || probes != 1 {
		t.Errorf("a go is kept 30 s: %+v", r)
	}
	if r := s.Route(ctx, inst, "admin", false, now.Add(31*time.Second)); r.State != "hold" || r.Reason != "answered 200, not from this Mission Control" {
		t.Errorf("switching over = %+v", r)
	}
	code = 530
	if r := s.Route(ctx, inst, "admin", false, now.Add(37*time.Second)); probes != 3 || r.Reason != "530 from Cloudflare: its edge is still sending admin.svnmns.com to a tunnel that isn't this one" {
		t.Errorf("a miss is probed again in 5 s: %d probes, %+v", probes, r)
	}
	inst.CloudflareConnectedAt.Time = now.Add(-11 * time.Minute)
	if r := s.Route(ctx, inst, "admin", false, now.Add(38*time.Second)); r.State != "nogo" {
		t.Errorf("after the switch-over = %+v", r)
	}
	s2 := &systemstatus.Status{PingURL: func(string) string { return "http://no-such-host.invalid/ping" }}
	if r := s2.Route(ctx, inst, "hooks", false, now); r.Reason != "can't look up hooks.svnmns.com from this server" {
		t.Errorf("= %+v", r)
	}
}

// Cloudflare's edge with no certificate for the name (a base on a
// subdomain): the reason says so, and what fixes it.
func TestRouteNoCertificate(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ErrorLog: log.New(io.Discard, "", 0)}
	go srv.Serve(tls.NewListener(ln, &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return nil, errors.New("no certificate for that name")
	}}))
	defer srv.Close()
	s := &systemstatus.Status{Identity: "me", PingURL: func(string) string { return "https://" + ln.Addr().String() + "/ping" }}
	now := time.Now()
	inst := models.Installation{BaseDomain: "next.example.com", CloudflareConnectedAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true}}
	r := s.Route(context.Background(), inst, "admin", false, now)
	if r.State != "nogo" || !strings.Contains(r.Reason, "Cloudflare refused https for admin.next.example.com") ||
		!strings.Contains(r.Reason, "needs an Advanced Certificate for *.next.example.com") {
		t.Errorf("= %+v", r)
	}
}

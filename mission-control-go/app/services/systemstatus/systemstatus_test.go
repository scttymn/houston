package systemstatus_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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

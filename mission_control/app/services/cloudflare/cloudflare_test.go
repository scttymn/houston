package cloudflare_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/houston/mission_control/app/services/cloudflare"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare/cloudflaretest"
)

// A record is found, made, changed and removed through the API.
func TestRecords(t *testing.T) {
	ctx := context.Background()
	f := cloudflaretest.New(t)
	records := cloudflare.Records{Client: f.Client(), Zone: "z1"}
	if r, err := records.Find(ctx, "shop.example.com"); r != nil || err != nil {
		t.Fatalf("none yet: %v %v", r, err)
	}
	if err := records.Point(ctx, "shop.example.com", "tun-1", nil, cloudflare.Managed+" project:shop"); err != nil {
		t.Fatal(err)
	}
	r, err := records.Find(ctx, "shop.example.com")
	if err != nil || r == nil || r.Type != "CNAME" || r.Content != "tun-1.cfargotunnel.com" || !r.Proxied || !r.IsManaged() {
		t.Fatalf("made %+v %v", r, err)
	}
	if err := records.Point(ctx, "shop.example.com", "tun-2", r, cloudflare.Managed); err != nil {
		t.Fatal(err)
	}
	if got := f.Records("z1"); len(got) != 1 || got[0].Content != "tun-2.cfargotunnel.com" || got[0].ID != r.ID {
		t.Errorf("changed %+v", got)
	}
	if err := records.Delete(ctx, *r); err != nil || len(f.Records("z1")) != 0 {
		t.Errorf("deleted: %v %+v", err, f.Records("z1"))
	}
	if (cloudflare.Record{Comment: "someone else's"}).IsManaged() {
		t.Error("another's record is Houston's")
	}
}

// Cloudflare's no is its own words; no JSON is the HTTP status; no answer
// says it couldn't reach it.
func TestErrors(t *testing.T) {
	ctx := context.Background()
	f := cloudflaretest.New(t)
	f.Fail("Authentication error")
	err := f.Client().Get(ctx, "/zones", nil, nil)
	var cf *cloudflare.Error
	if !errors.As(err, &cf) || cf.Msg != "Authentication error" || cf.Status != 400 {
		t.Errorf("= %#v", err)
	}
	bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
	defer bare.Close()
	if err := (cloudflare.Client{Base: bare.URL}).Get(ctx, "/zones", nil, nil); err == nil || err.Error() != "HTTP 502" {
		t.Errorf("no JSON: %v", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	c := cloudflare.Client{Base: slow.URL, HTTP: &http.Client{Timeout: 50 * time.Millisecond}}
	if err := c.Get(ctx, "/zones", nil, nil); !errors.As(err, &cf) || cf.Status != 0 || !strings.HasPrefix(cf.Msg, "couldn't reach Cloudflare (") {
		t.Errorf("no answer: %v", err)
	}
}

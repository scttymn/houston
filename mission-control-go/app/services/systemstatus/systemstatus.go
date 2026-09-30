// Package systemstatus is the header strip's live status (the Rails app's
// SystemStatus): whether the tunnel has connections, and whether the
// registry answers. Each probe is kept for 30 seconds and gets 2, so pages
// stay fast and Cloudflare isn't asked on every page view.
package systemstatus

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare"
)

const (
	keepFor = 30 * time.Second
	timeout = 2 * time.Second
)

// Tunnel is where the tunnel stands: "go" (connected), "hold" (no
// connections), or "unknown", with Cloudflare's reason.
type Tunnel struct {
	State       string
	Connections int
	Reason      string
}

// Go is whether the tunnel is up.
func (t Tunnel) Go() bool { return t.State == "go" }

type kept[T any] struct {
	value T
	key   string
	at    time.Time
}

// Status probes, keeping each answer a while.
type Status struct {
	// Cloudflare is its API's address ("": Cloudflare's own); Registry,
	// Houston's registry (HOUSTON_REGISTRY_URL).
	Cloudflare, Registry string
	HTTP                 *http.Client

	mu       sync.Mutex
	tunnel   kept[Tunnel]
	registry kept[bool]
}

func (s *Status) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: timeout}
}

// Tunnel is the installation's tunnel's state.
func (s *Status) Tunnel(ctx context.Context, inst models.Installation, now time.Time) Tunnel {
	token := inst.CloudflareApiToken.Reveal()
	if inst.TunnelID == "" || token == "" {
		return Tunnel{State: "unknown", Reason: "not connected to Cloudflare"}
	}
	s.mu.Lock()
	if s.tunnel.key == inst.TunnelID && now.Sub(s.tunnel.at) < keepFor {
		defer s.mu.Unlock()
		return s.tunnel.value
	}
	s.mu.Unlock()
	var t struct {
		Connections []struct{} `json:"connections"`
	}
	c := cloudflare.Client{Token: token, Base: s.Cloudflare, HTTP: s.client()}
	err := c.Get(ctx, "/accounts/"+inst.CloudflareAccountID+"/cfd_tunnel/"+inst.TunnelID, nil, &t)
	var answer Tunnel
	var cf *cloudflare.Error
	switch {
	case errors.As(err, &cf):
		answer = Tunnel{State: "unknown", Reason: cf.Msg}
	case err != nil:
		answer = Tunnel{State: "unknown", Reason: err.Error()}
	case len(t.Connections) > 0:
		answer = Tunnel{State: "go", Connections: len(t.Connections)}
	default:
		answer = Tunnel{State: "hold"}
	}
	s.mu.Lock()
	s.tunnel = kept[Tunnel]{value: answer, key: inst.TunnelID, at: now}
	s.mu.Unlock()
	return answer
}

// Forget has the next look ask Cloudflare again (the token changed).
func (s *Status) Forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tunnel = kept[Tunnel]{}
}

// RegistryUp is whether the registry answers (a 401 is up).
func (s *Status) RegistryUp(ctx context.Context, now time.Time) bool {
	s.mu.Lock()
	if !s.registry.at.IsZero() && now.Sub(s.registry.at) < keepFor {
		defer s.mu.Unlock()
		return s.registry.value
	}
	s.mu.Unlock()
	up := false
	if req, err := http.NewRequestWithContext(ctx, "GET", s.Registry+"/v2/", nil); err == nil {
		if resp, err := s.client().Do(req); err == nil {
			resp.Body.Close()
			up = resp.StatusCode < 500
		}
	}
	s.mu.Lock()
	s.registry = kept[bool]{value: up, at: now}
	s.mu.Unlock()
	return up
}

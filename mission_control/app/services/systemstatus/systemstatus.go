// Package systemstatus is the header strip's live status: whether the tunnel has connections, and whether the
// registry answers. Each probe is kept for 30 seconds and gets 2, so pages
// stay fast and Cloudflare isn't asked on every page view.
package systemstatus

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/cloudflare"
)

const (
	keepFor = 30 * time.Second
	timeout = 2 * time.Second
	// SwitchOver is how long Cloudflare gets to move a name onto the
	// tunnel after it's connected before a miss is NO-GO: its edge can
	// keep sending a name to an older record for a while.
	SwitchOver = 10 * time.Minute
	// recheckMiss is how soon a miss is probed again.
	recheckMiss = 5 * time.Second
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

	// Identity is what this Mission Control's /ping answers (app.Identity).
	Identity string
	// PingURL is where a host's /ping is ("https://<host>/ping", or a
	// test's).
	PingURL func(host string) string

	mu       sync.Mutex
	tunnel   kept[Tunnel]
	registry kept[bool]
	misses   map[string]kept[string]
}

// Route is whether <label>.<base> reaches this Mission Control through
// Cloudflare: "go", "hold" (switching over), or "nogo", with what happened.
type Route struct {
	State  string
	Reason string
}

// Route probes <label>.<base> (admin, hooks): out through Cloudflare's
// edge and back in through the tunnel, where only this install's /ping
// answers with its identity. Nil before Cloudflare is connected. onHost is
// a request on that host already: it reaches here. The probe's result is
// kept, not the state: HOLD turns into NO-GO with the clock.
func (s *Status) Route(ctx context.Context, inst models.Installation, label string, onHost bool, now time.Time) *Route {
	if !inst.CloudflareConnectedAt.Valid || inst.BaseDomain == "" {
		return nil
	}
	if onHost {
		return &Route{State: "go"}
	}
	host := label + "." + inst.BaseDomain
	s.mu.Lock()
	k, ok := s.misses[host]
	s.mu.Unlock()
	keep := keepFor
	if ok && k.value != "" {
		keep = recheckMiss
	}
	if !ok || now.Sub(k.at) >= keep {
		k = kept[string]{value: s.probe(ctx, host, inst.BaseDomain), at: now}
		s.mu.Lock()
		if s.misses == nil {
			s.misses = map[string]kept[string]{}
		}
		s.misses[host] = k
		s.mu.Unlock()
	}
	switch {
	case k.value == "":
		return &Route{State: "go"}
	case inst.CloudflareConnectedAt.Time.After(now.Add(-SwitchOver)):
		return &Route{State: "hold", Reason: k.value}
	}
	return &Route{State: "nogo", Reason: k.value}
}

// probe is "" when host answered as this install, else what happened.
func (s *Status) probe(ctx context.Context, host, base string) string {
	u := "https://" + host + "/ping"
	if s.PingURL != nil {
		u = s.PingURL(host)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err.Error()
	}
	resp, err := s.client().Do(req)
	if err != nil {
		var dns *net.DNSError
		var timeout interface{ Timeout() bool }
		var op *net.OpError
		switch {
		case errors.As(err, &dns):
			return "can't look up " + host + " from this server"
		case errors.As(err, &op) && op.Op == "remote error": // a TLS alert from the other end
			// Cloudflare's edge has no certificate for the name: its free one
			// covers a zone and one level below it, not a base on a
			// subdomain; or a new zone's isn't issued yet.
			return "Cloudflare refused https for " + host + " (" + err.Error() + "): it has no certificate for it. Its free certificate covers a zone and one level below it, so a base on a subdomain needs an Advanced Certificate for *." + base + "; a new zone's can take a few minutes"
		case errors.As(err, &timeout) && timeout.Timeout():
			return "no answer in 2 s"
		}
		return "couldn't connect to " + host + " from this server (" + err.Error() + ")"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	switch {
	case resp.StatusCode == 200 && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(string(body))), []byte(s.Identity)) == 1:
		return ""
	case resp.StatusCode == 530:
		return "530 from Cloudflare: its edge is still sending " + host + " to a tunnel that isn't this one"
	}
	return "answered " + strconv.Itoa(resp.StatusCode) + ", not from this Mission Control"
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

// Package api is Mission Control's local API for houston deploy and the
// runners (/api, not /api/v1): the runner
// token, never through the tunnel.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission_control/app/models"
)

// MaxBody is a request body's limit, but where an endpoint sets its own.
const MaxBody = 64 << 10

// Door is what every runner API request passes.
type Door struct {
	DB *db.DB
	// RunnerToken is HOUSTON_RUNNER_TOKEN: shorter than 32 characters, it
	// opens nothing.
	RunnerToken string
}

// Pipeline is the door's filters, in order: answers are JSON; through the
// tunnel, nothing; the runner token; setup finished.
func (d Door) Pipeline() web.Pipeline {
	return web.Pipeline{web.AcceptJSON, refuseTunnel, d.authenticate, d.requireSetup}
}

// refuseTunnel answers an empty 404 to a request through the tunnel:
// Cloudflare adds Cf-Ray and Cf-Connecting-Ip to every request it forwards,
// and a client can't remove them. (gantry removes a Cf-Connecting-Ip that
// didn't come from cloudflared; Cf-Ray stays.)
func refuseTunnel(w http.ResponseWriter, r *http.Request) error {
	if r.Header.Get("Cf-Ray") != "" || r.Header.Get("Cf-Connecting-Ip") != "" {
		w.WriteHeader(http.StatusNotFound)
	}
	return nil
}

func (d Door) authenticate(w http.ResponseWriter, r *http.Request) error {
	given, _ := web.BearerToken(r)
	if len(d.RunnerToken) < 32 || subtle.ConstantTimeCompare([]byte(given), []byte(d.RunnerToken)) != 1 {
		return web.Status(http.StatusUnauthorized, errors.New("the runner token is missing or wrong"))
	}
	return nil
}

func (d Door) requireSetup(w http.ResponseWriter, r *http.Request) error {
	connected, err := models.New(d.DB.Read).InstallationConnected(r.Context())
	if err != nil || !connected {
		return web.Status(http.StatusConflict, errors.New("finish setup in Mission Control first"))
	}
	return nil
}

// Body is a request's JSON object, each value still JSON: an endpoint
// checks each field's type itself, so a wrong one is its 422, not a 400.
type Body map[string]json.RawMessage

// readBody reads the request's JSON object, at most limit bytes: over it a
// 413, not a JSON object a 400.
func readBody(r *http.Request, limit int64) (Body, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, web.Status(http.StatusBadRequest, fmt.Errorf("the request's body couldn't be read: %w", err))
	}
	if int64(len(data)) > limit {
		return nil, web.Status(http.StatusRequestEntityTooLarge, fmt.Errorf("the request is larger than %d KiB", limit>>10))
	}
	var body Body
	if err := json.Unmarshal(data, &body); err != nil || body == nil {
		return nil, web.Status(http.StatusBadRequest, errors.New("the request isn't JSON"))
	}
	return body, nil
}

package api

import (
	"net/http"
	"time"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// CloudflareView is GET /api/v1/cloudflare (houston cloudflare): what
// Cloudflare has for this Houston, asked now.
func (c V1) CloudflareView(w http.ResponseWriter, r *http.Request) error {
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, c.CloudflareSettings.Fetch(r.Context(), inst, time.Now()))
}

// CloudflareToken is PUT /api/v1/cloudflare/token {token}: Houston's API
// token replaced, when the new one passes every check (422 with the checks
// when it doesn't). It's write-only: never shown.
func (c V1) CloudflareToken(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil {
		return err
	}
	token, _ := field(body, "token")
	replaced, checks := c.CloudflareSettings.ReplaceToken(r.Context(), inst, token, time.Now())
	status := http.StatusOK
	if !replaced {
		status = http.StatusUnprocessableEntity
	} else if c.SystemStatus != nil {
		c.SystemStatus.Forget()
	}
	return web.JSON(w, status, map[string]any{"replaced": replaced, "checks": checks})
}

// RepairCloudflare is POST /api/v1/cloudflare/repair: the tunnel's routes
// and Houston's records put back.
func (c V1) RepairCloudflare(w http.ResponseWriter, r *http.Request) error {
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil {
		return err
	}
	results, err := c.CloudflareSettings.Repair(r.Context(), inst, time.Now())
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"results": results})
}

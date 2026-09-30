package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// tokenKey is the personal token a /api/v1 request came with.
var tokenKey = web.NewKey[models.ApiToken]("api_token")

// V1Door is what every personal API request passes (the Rails app's
// Api::V1::BaseController): a hou_ token, and setup finished. It works
// through the tunnel, and it never answers a secret's value or a deploy
// key: those stay on the runner API, which personal tokens can't use.
type V1Door struct{ DB *db.DB }

// Pipeline is the door's filters, in order.
func (d V1Door) Pipeline() web.Pipeline {
	return web.Pipeline{web.AcceptJSON, d.authenticate, Door{DB: d.DB}.requireSetup}
}

func (d V1Door) authenticate(w http.ResponseWriter, r *http.Request) error {
	given, _ := web.BearerToken(r)
	refused := web.Status(http.StatusUnauthorized, errors.New("the API token is missing, wrong or revoked"))
	if !strings.HasPrefix(given, "hou_") {
		return refused
	}
	token, err := models.New(d.DB.Read).APITokenByDigest(r.Context(), models.Digest(given))
	if err != nil {
		return refused
	}
	now := time.Now()
	if err := models.New(d.DB.Write).MarkTokenUsed(r.Context(), models.MarkTokenUsedParams{Now: sqlTime(now), ID: token.ID,
		Before: sqlTime(now.Add(-time.Minute))}); err != nil {
		return err
	}
	web.Set(r, tokenKey, token)
	return nil
}

// V1 is the personal API's endpoints.
type V1 struct {
	Controller
	// Version is which Houston this is (app.HoustonVersion).
	Version string
}

// Me is GET /api/v1/me: which token this is, on which server, running which
// Houston (houston login checks it; houston status shows the version, and
// an update running).
func (c V1) Me(w http.ResponseWriter, r *http.Request) error {
	token, _ := web.Get(r, tokenKey)
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil {
		return err
	}
	var latest, updating *string
	if newer := models.NewerRelease(c.Version, inst.LatestRelease); newer != "" {
		latest = &newer
	}
	if running, err := models.New(c.DB.Read).RunningUpdate(r.Context()); err == nil {
		updating = &running.ToVersion
	}
	return web.JSON(w, http.StatusOK, map[string]any{"token": token.Name, "server": inst.BaseDomain, "version": c.Version,
		"latest": latest, "updating": updating})
}

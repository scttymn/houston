// Package home is the app's home page, at /.
package home

import (
	"net/http"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/shared/layout"
)

// Controller draws the home page.
type Controller struct {
	DB *db.DB
}

// Show is GET /.
func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	return web.Render(w, r, http.StatusOK, index(layout.For(r, "")))
}

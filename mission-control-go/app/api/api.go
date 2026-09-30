package api

import (
	"log/slog"

	"github.com/scttymn/gantry/db"
)

// Controller is the runner API's endpoints.
type Controller struct {
	DB  *db.DB
	Log *slog.Logger
}

package api

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission_control/app/models"
)

// Secret is GET /api/projects/{name}/secrets/{key}: one value, as plain
// text, for houston deploy to hand to Kamal. Only keys the project's
// compose.yml references.
func (c Controller) Secret(w http.ResponseWriter, r *http.Request) error {
	key := r.PathValue("key")
	q := models.New(c.DB.Read)
	project, err := q.ProjectByName(r.Context(), r.PathValue("name"))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	value := ""
	if err == nil && slices.ContainsFunc(project.Variables.V, func(v models.Variable) bool { return v.Name == key }) {
		values, err := models.Secrets(r.Context(), q, project.ID)
		if err != nil {
			return err
		}
		value = values[key]
	}
	if strings.TrimSpace(value) == "" {
		return web.Status(http.StatusNotFound, fmt.Errorf("no value for %s", key))
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, err = io.WriteString(w, value)
	return err
}

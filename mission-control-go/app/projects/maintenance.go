package projects

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// ForHost is the project host is one of the hostnames of, if any: a
// request for it reaches Mission Control only while the tunnel routes it
// here (the project's maintenance page). admin. and hooks. are Houston's
// own.
func ForHost(ctx context.Context, q *models.Queries, host string) (*models.Project, error) {
	base, err := q.InstallationBaseDomain(ctx)
	if err != nil || base == "" || host == "admin."+base || host == "hooks."+base {
		return nil, nil
	}
	projects, err := q.Projects(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		for _, h := range p.Hostnames(base) {
			if h == host {
				return &p, nil
			}
		}
	}
	return nil, nil
}

// writeMaintenance writes p's maintenance page: its own, or Houston's.
func writeMaintenance(w http.ResponseWriter, r *http.Request, p models.Project, message string, status int) error {
	if html := MaintenanceHTML(p, message); html != "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, err := io.WriteString(w, html)
		return err
	}
	return web.Render(w, r, status, MaintenancePage(p, message))
}

// ShowMaintenance answers any method on any path of a project's hostname: its
// maintenance page while it's in maintenance (503, asked again in a
// minute), a plain 404 otherwise (a stale route). Nothing else of Mission
// Control answers on these hosts: no session, no chrome.
func (c Controller) ShowMaintenance(w http.ResponseWriter, r *http.Request) error {
	p, err := ForHost(r.Context(), models.New(c.DB.Read), strings.ToLower(web.RequestHost(r)))
	if err != nil {
		return err
	}
	if p == nil || !p.MaintenanceSince.Valid {
		w.WriteHeader(http.StatusNotFound)
		return nil
	}
	w.Header().Set("Retry-After", "60")
	w.Header().Set("Cache-Control", "no-store")
	return writeMaintenance(w, r, *p, p.MaintenanceMessage, http.StatusServiceUnavailable)
}

// PreviewMaintenance is GET /projects/{name}/maintenance/preview: the page
// as the project's hostnames would show it. A repo's page may carry
// scripts: the sandbox policy keeps them off Mission Control's origin, even
// opened directly rather than in the project page's iframe.
func (c Controller) PreviewMaintenance(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	message := p.MaintenanceMessage
	if message == "" {
		message = "(your message)"
	}
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "no-store")
	return writeMaintenance(w, r, p, message, http.StatusOK)
}

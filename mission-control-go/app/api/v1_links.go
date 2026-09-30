package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/linking"
)

// StartLink is POST /api/v1/links {repo_url}: Add project's first step. A
// deploy key is made for the repo; Houston's access is checked (it can't
// read yet until the key is added).
func (c V1) StartLink(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, 64<<10)
	if err != nil {
		return err
	}
	url, _ := field(body, "repo_url")
	ctx, now := r.Context(), time.Now()
	inst, err := models.New(c.DB.Read).CurrentInstallation(ctx)
	if err != nil {
		return err
	}
	l, err := linking.Start(ctx, c.DB, inst.BaseDomain, strings.TrimSpace(url), now)
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	ok, message := linking.Check(ctx, c.Git, l)
	return web.JSON(w, http.StatusOK, map[string]any{"id": l.ID, "deploy_key": l.DeployKeyPublic, "access": map[string]any{"ok": ok, "message": message}})
}

// draft is the link the path names, or the 404 in its words.
func (c V1) draft(r *http.Request) (models.RepoLink, error) {
	l, err := models.New(c.DB.Read).LinkByID(r.Context(), web.ID(r, "id"))
	if err != nil {
		return l, web.Status(http.StatusNotFound, fmt.Errorf("no link %s (drafts last a day)", r.PathValue("id")))
	}
	return l, nil
}

// LinkAccess is POST /api/v1/links/{id}/access: can Houston read it now?
func (c V1) LinkAccess(w http.ResponseWriter, r *http.Request) error {
	l, err := c.draft(r)
	if err != nil {
		return err
	}
	ok, message := linking.Check(r.Context(), c.Git, l)
	return web.JSON(w, http.StatusOK, map[string]any{"ok": ok, "message": message})
}

// ReadLink is POST /api/v1/links/{id}/read {branch, compose_path}: the
// compose file read, and what it found.
func (c V1) ReadLink(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, 64<<10)
	if err != nil && r.ContentLength != 0 {
		return err
	}
	l, err := c.draft(r)
	if err != nil {
		return err
	}
	branch, _ := field(body, "branch")
	composePath, _ := field(body, "compose_path")
	found, problems, err := linking.Read(r.Context(), c.DB, c.Git, l, branch, composePath, time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	if found == nil {
		return web.JSON(w, http.StatusOK, map[string]any{"ok": false, "problems": problems})
	}
	return web.JSON(w, http.StatusOK, map[string]any{"ok": true, "found": found})
}

// SaveLink is POST /api/v1/links/{id}/save: the project saved from what
// was read, with where its webhook rings.
func (c V1) SaveLink(w http.ResponseWriter, r *http.Request) error {
	l, err := c.draft(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	p, err := linking.Save(ctx, c.DB, l, nil, time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	inst, err := models.New(c.DB.Read).CurrentInstallation(ctx)
	if err != nil {
		return err
	}
	var secret *string
	if !p.WebhookVerifiedAt.Valid {
		secret = orNull(p.WebhookSecret.Reveal())
	}
	c.Live.Refresh(FlightBoard, "")
	return web.JSON(w, http.StatusOK, map[string]any{"project": p.Name, "webhook_url": "https://hooks." + inst.BaseDomain + "/" + p.Name, "webhook_secret": secret})
}

// MoveRepo is PATCH or PUT /api/v1/projects/{name}/repo {repo_url}: the
// project relinked to its repo's new address.
func (c V1) MoveRepo(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	p, err := c.project(r)
	if err != nil {
		return err
	}
	url, _ := field(body, "repo_url")
	err = linking.Move(r.Context(), c.DB, c.Git, p, url, time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"repo_url": strings.TrimSpace(url)})
}

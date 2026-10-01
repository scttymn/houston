package links

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/live"
	"github.com/scttymn/gantry/sign"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/internal/mission"
	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/gitremote"
	"github.com/scttymn/houston/mission_control/app/services/linking"
	"github.com/scttymn/houston/mission_control/app/shared/layout"
)

// draftCookie holds the draft's id while the page steps through its
// forms; opening the page again, Cancel and Save all end it.
const draftCookie = "repo_link"

// Controller is Add project's.
type Controller struct {
	DB     *db.DB
	Signer sign.Signer
	Git    gitremote.Git
	Live   *live.Hub
	Refs   models.RefReader
	// Board is the flight board's live stream.
	Board string
}

// draft is the draft the cookie names, if it's still there.
func (c Controller) draft(r *http.Request) *models.RepoLink {
	id, ok := web.SignedCookie(r, c.Signer, draftCookie)
	if !ok {
		return nil
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	l, err := models.New(c.DB.Read).LinkByID(r.Context(), n)
	if err != nil {
		return nil
	}
	return &l
}

func (c Controller) keep(w http.ResponseWriter, r *http.Request, l models.RepoLink) {
	web.SetCookie(w, r, draftCookie, c.Signer.Sign(draftCookie, strconv.FormatInt(l.ID, 10)), 24*time.Hour)
}

// discard ends the draft: destroyed, not just forgotten (it holds a
// private key).
func (c Controller) discard(w http.ResponseWriter, r *http.Request) error {
	if l := c.draft(r); l != nil {
		if err := models.New(c.DB.Write).DeleteLink(r.Context(), l.ID); err != nil {
			return err
		}
	}
	web.ClearCookie(w, draftCookie)
	return nil
}

// page is Add project for the draft l (nil: none yet).
func (c Controller) page(r *http.Request, l *models.RepoLink) (Page, error) {
	ctx := r.Context()
	q := models.New(c.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Page{}, err
	}
	pg := Page{Layout: layout.For(r, "Add project · Mission Control"), Base: inst.BaseDomain, Link: l}
	if l != nil && l.Preview.Valid {
		var in mission.Inspection
		if json.Unmarshal([]byte(l.Preview.String), &in) == nil {
			pg.Found = &in
			if p, err := q.ProjectByName(ctx, in.Sync.Name); err == nil {
				pg.Existing = &p
			} else if !errors.Is(err, sql.ErrNoRows) {
				return pg, err
			}
		}
	}
	return pg, nil
}

// New is GET /link: always empty, a draft from an earlier visit isn't
// carried over.
func (c Controller) New(w http.ResponseWriter, r *http.Request) error {
	if err := c.discard(w, r); err != nil {
		return err
	}
	pg, err := c.page(r, nil)
	if err != nil {
		return err
	}
	return web.Render(w, r, http.StatusOK, NewPage(pg))
}

// Access is POST /link/access {repo_url}: a draft with a deploy key for
// the repo, and whether Houston can read it yet.
func (c Controller) Access(w http.ResponseWriter, r *http.Request) error {
	url := strings.TrimSpace(r.PostFormValue("repo_url"))
	if problem := models.RepoURLProblem(url); problem != "" {
		pg, err := c.page(r, nil)
		if err != nil {
			return err
		}
		pg.URLError, pg.EnteredURL = problem, url
		return web.Render(w, r, http.StatusUnprocessableEntity, NewPage(pg))
	}
	l := c.draft(r)
	if l == nil || l.RepoUrl != url {
		pg, err := c.page(r, nil)
		if err != nil {
			return err
		}
		started, err := linking.Start(r.Context(), c.DB, pg.Base, url, time.Now())
		if err != nil {
			return err
		}
		l = &started
	}
	c.keep(w, r, *l)
	pg, err := c.page(r, l)
	if err != nil {
		return err
	}
	ok, message := linking.Check(r.Context(), c.Git, *l)
	pg.Access = &access{OK: ok, Message: message}
	return web.Render(w, r, http.StatusOK, NewPage(pg))
}

// Read is POST /link/read {branch, compose_path}: the compose file read at
// the branch, what Houston found shown.
func (c Controller) Read(w http.ResponseWriter, r *http.Request) error {
	l := c.draft(r)
	if l == nil {
		http.Redirect(w, r, "/link", http.StatusSeeOther)
		return nil
	}
	branch, path := strings.TrimSpace(r.PostFormValue("branch")), strings.TrimSpace(r.PostFormValue("compose_path"))
	if !models.ValidBranch(branch) || !models.ValidComposePath(path) {
		l.Branch, l.ComposePath = branch, path
		pg, err := c.page(r, l)
		if err != nil {
			return err
		}
		pg.Found, pg.Existing = nil, nil
		if !models.ValidBranch(branch) {
			pg.BranchError = "isn't a branch name git accepts"
		}
		if !models.ValidComposePath(path) {
			pg.PathError = "must be a .yml or .yaml path inside the repo"
		}
		return web.Render(w, r, http.StatusUnprocessableEntity, NewPage(pg))
	}
	found, problems, err := linking.Read(r.Context(), c.DB, c.Git, *l, branch, path, time.Now())
	if err != nil {
		return err
	}
	read, err := models.New(c.DB.Read).LinkByID(r.Context(), l.ID)
	if err != nil {
		return err
	}
	pg, err := c.page(r, &read)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if found == nil {
		pg.Problems, status = problems, http.StatusUnprocessableEntity
	}
	return web.Render(w, r, status, NewPage(pg))
}

// Create is POST /link: Save, or Deploy (save, then deploy the head the
// deploy rule matches).
func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	l := c.draft(r)
	if l == nil {
		http.Redirect(w, r, "/link", http.StatusSeeOther)
		return nil
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	secrets := map[string]string{}
	for key, values := range r.PostForm {
		if name, ok := strings.CutPrefix(key, "secrets["); ok && strings.HasSuffix(name, "]") && len(values) > 0 {
			secrets[strings.TrimSuffix(name, "]")] = values[0]
		}
	}
	ctx, now := r.Context(), time.Now()
	p, err := linking.Save(ctx, c.DB, *l, secrets, now)
	var refusedSecrets linking.SecretsRefused
	var refused models.Refused
	if errors.As(err, &refusedSecrets) || errors.As(err, &refused) {
		pg, perr := c.page(r, l)
		if perr != nil {
			return perr
		}
		pg.SecretErrors, pg.SaveError = refusedSecrets.Errors, refused.Msg
		return web.Render(w, r, http.StatusUnprocessableEntity, NewPage(pg))
	}
	if err != nil {
		return err
	}
	web.ClearCookie(w, draftCookie)
	c.Live.Refresh(c.Board, "")
	flash := web.Flash{Signer: c.Signer}
	if r.PostFormValue("deploy") == "" {
		flash.Redirect(w, r, "/projects/"+p.Name, "notice", p.Name+" is linked to "+p.RepoUrl+".")
		return nil
	}
	d, err := models.QueueHead(ctx, c.DB, c.Refs, p, false, now)
	if errors.As(err, &refused) {
		flash.Redirect(w, r, "/projects/"+p.Name, "alert", p.Name+" is linked, but it can't deploy yet: "+refused.Msg)
		return nil
	}
	if err != nil {
		return err
	}
	http.Redirect(w, r, "/projects/"+p.Name+"/deploys/"+strconv.FormatInt(d.Number, 10), http.StatusSeeOther)
	return nil
}

// Cancel is DELETE /link: the draft ended, back to the board. Idempotent.
func (c Controller) Cancel(w http.ResponseWriter, r *http.Request) error {
	if err := c.discard(w, r); err != nil {
		return err
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}

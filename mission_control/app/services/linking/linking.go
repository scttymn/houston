// Package linking is Add project and relinking: a repo URL and a deploy key made for it,
// Houston's access checked, the compose file read, then the project saved
// from what was read.
package linking

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/internal/mission"
	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/gitremote"
)

// Drafts last this long.
const draftFor = 24 * time.Hour

func link(l models.RepoLink) gitremote.Link {
	return gitremote.Link{RepoURL: l.RepoUrl, DeployKey: l.DeployKeyPrivate.Reveal(), Branch: l.Branch, ComposePath: l.ComposePath}
}

// Start begins a link to url: a deploy key made for it (commented
// houston@<base>), drafts older than a day forgotten.
func Start(ctx context.Context, d *db.DB, base, url string, now time.Time) (models.RepoLink, error) {
	if problem := models.RepoURLProblem(url); problem != "" {
		return models.RepoLink{}, models.Refused{Msg: "the repo URL " + problem}
	}
	q := models.New(d.Write)
	if err := q.ForgetOldLinks(ctx, now.Add(-draftFor)); err != nil {
		return models.RepoLink{}, err
	}
	if base == "" {
		base = "houston"
	}
	private, public, err := models.NewDeployKey("houston@" + base)
	if err != nil {
		return models.RepoLink{}, err
	}
	return q.CreateLink(ctx, models.CreateLinkParams{RepoUrl: url, DeployKeyPrivate: crypt.Of(private), DeployKeyPublic: public,
		WebhookSecret: crypt.Of(models.NewToken()), Now: now})
}

// Check is whether Houston can read the link's repo, and its branch.
func Check(ctx context.Context, g gitremote.Git, l models.RepoLink) (bool, string) {
	return g.Check(ctx, link(l))
}

// Found is what a read found: the project's name, the commit, and what the
// page shows before saving.
type Found struct {
	Name      string             `json:"name"`
	Sha       string             `json:"sha"`
	Branch    string             `json:"branch"`
	Domains   []string           `json:"domains"`
	Variables []mission.Variable `json:"variables"`
}

// Read reads the link's compose file at branch (or its own) and keeps what
// was read. It's what was found, or the file's problems.
func Read(ctx context.Context, d *db.DB, g gitremote.Git, l models.RepoLink, branch, composePath string, now time.Time) (*Found, string, error) {
	if branch != "" {
		l.Branch = branch
	}
	if composePath != "" {
		l.ComposePath = composePath
	}
	var invalid []string
	if !models.ValidBranch(l.Branch) {
		invalid = append(invalid, "Branch isn't a branch name git accepts")
	}
	if !models.ValidComposePath(l.ComposePath) {
		invalid = append(invalid, "Compose path must be a .yml or .yaml path inside the repo")
	}
	if invalid != nil {
		return nil, "", models.Refused{Msg: strings.Join(invalid, " and ")}
	}
	sha, in, problems := g.Read(ctx, link(l))
	var preview sql.NullString
	if in != nil {
		b, _ := json.Marshal(in)
		preview = sql.NullString{String: string(b), Valid: true}
	}
	if err := models.New(d.Write).SetLinkRead(ctx, models.SetLinkReadParams{Branch: l.Branch, ComposePath: l.ComposePath, Preview: preview,
		PreviewSha: sha, UpdatedAt: now, ID: l.ID}); err != nil {
		return nil, "", err
	}
	if in == nil {
		return nil, problems, nil
	}
	return &Found{Name: in.Sync.Name, Sha: sha, Branch: l.Branch, Domains: in.Sync.Domains, Variables: in.Sync.Variables}, "", nil
}

// SecretsRefused are secrets typed on Add project the container can't
// receive, by name: nothing is saved.
type SecretsRefused struct{ Errors map[string]string }

func (e SecretsRefused) Error() string {
	names := make([]string, 0, len(e.Errors))
	for name := range e.Errors {
		names = append(names, name)
	}
	sort.Strings(names)
	return web.Sentence(names) + " can't be saved"
}

// Save saves the project from what the link read (a new one, or linking
// one that sync made), with secrets typed on Add project (blank ones, and
// names compose.yml doesn't list, left out), and forgets the draft.
func Save(ctx context.Context, d *db.DB, l models.RepoLink, secrets map[string]string, now time.Time) (models.Project, error) {
	refused := func(msg string) (models.Project, error) { return models.Project{}, models.Refused{Msg: msg} }
	if !l.Preview.Valid {
		return refused("Read the file first: Houston saves what it read from the repo.")
	}
	var preview struct {
		Sync map[string]json.RawMessage `json:"sync"`
	}
	json.Unmarshal([]byte(l.Preview.String), &preview)
	s, errs := models.ParseSync(preview.Sync)
	if errs != nil {
		fields := make([]string, 0, len(errs))
		for f := range errs {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		var parts []string
		for _, f := range fields {
			parts = append(parts, f+" "+strings.Join(errs[f], " and "))
		}
		return refused(strings.Join(parts, " and "))
	}
	q := models.New(d.Read)
	existing, err := q.ProjectByName(ctx, s.Name)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return models.Project{}, err
	}
	if found && existing.RepoUrl != "" && existing.RepoUrl != l.RepoUrl {
		return refused(fmt.Sprintf("%s is already linked to %s", existing.Name, existing.RepoUrl))
	}
	wanted := map[string]string{}
	problems := map[string]string{}
	for _, v := range s.Variables {
		value := secrets[v.Name]
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := models.ValidSecret(v.Name, value); err != nil {
			problems[v.Name] = strings.TrimPrefix(err.Error(), v.Name+" ")
			continue
		}
		wanted[v.Name] = value
	}
	if len(problems) > 0 {
		return models.Project{}, SecretsRefused{Errors: problems}
	}
	secret := l.WebhookSecret.Reveal()
	if found && existing.WebhookSecret.Reveal() != "" {
		secret = existing.WebhookSecret.Reveal()
	}
	if secret == "" {
		secret = models.NewToken()
	}
	var p models.Project
	err = d.Tx(ctx, func(tx *db.Tx) error {
		saved, _, err := s.Save(ctx, tx, now)
		if err != nil {
			return err
		}
		q := models.New(tx)
		if err := q.SetRepo(ctx, models.SetRepoParams{RepoUrl: l.RepoUrl, Branch: l.Branch, ComposePath: l.ComposePath, DeployKeyPrivate: l.DeployKeyPrivate,
			DeployKeyPublic: l.DeployKeyPublic, WebhookSecret: crypt.Of(secret), UpdatedAt: now, ID: saved.ID}); err != nil {
			return err
		}
		for key, value := range wanted {
			if err := q.SaveSecret(ctx, models.SaveSecretParams{ProjectID: saved.ID, Key: key, Value: crypt.Of(value), Now: now}); err != nil {
				return err
			}
		}
		if err := q.DeleteLink(ctx, l.ID); err != nil {
			return err
		}
		p, err = q.ProjectByID(ctx, saved.ID)
		return err
	})
	return p, err
}

// Move relinks p to a repo at url (renamed or moved): the same deploy key
// must read it, and its compose file must name the project.
func Move(ctx context.Context, d *db.DB, g gitremote.Git, p models.Project, url string, now time.Time) error {
	url = strings.TrimSpace(url)
	if problem := models.RepoURLProblem(url); problem != "" {
		return models.Refused{Msg: "the repo URL " + problem}
	}
	if url == p.RepoUrl {
		return models.Refused{Msg: fmt.Sprintf("%s's repo is already %s", p.Name, url)}
	}
	busy, err := models.New(d.Read).BusyDeploy(ctx, p.ID)
	if err == nil {
		return models.Refused{Msg: fmt.Sprintf("%s #%d is %s; wait for #%d", busy.Kind, busy.Number, busy.StatusWords(), busy.Number)}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	candidate := gitremote.Link{RepoURL: url, DeployKey: p.DeployKeyPrivate.Reveal(), Branch: p.Branch, ComposePath: p.ComposePath}
	if ok, message := g.Check(ctx, candidate); !ok {
		return models.Refused{Msg: url + ": " + message}
	}
	_, in, problems := g.Read(ctx, candidate)
	if in == nil {
		return models.Refused{Msg: fmt.Sprintf("couldn't read %s: %s", url, problems)}
	}
	if in.Sync.Name != p.Name {
		return models.Refused{Msg: fmt.Sprintf("%s's compose.yml there names %s, not %s", url, in.Sync.Name, p.Name)}
	}
	return models.New(d.Write).MoveRepo(ctx, models.MoveRepoParams{RepoUrl: url, UpdatedAt: now, ID: p.ID})
}

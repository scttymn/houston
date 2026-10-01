package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission_control/app/deploys"
	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/knownhosts"
)

// MaxWait is the longest a claim waits for work, in seconds.
const MaxWait = 25

// Claim is POST /api/runner/jobs/claim {runner, wait}: a long poll for the
// next queued deploy. 200 with what the runner needs to fetch, test and
// deploy it; 204 when there was nothing within wait seconds.
func (c Controller) Claim(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, MaxBody)
	if err != nil {
		return err
	}
	var runner string
	wait := int64(MaxWait)
	runnerOK := json.Unmarshal(body["runner"], &runner) == nil && models.RunnerName.MatchString(runner)
	waitOK := true
	if raw, sent := body["wait"]; sent {
		wait, err = strconv.ParseInt(string(raw), 10, 64)
		waitOK = err == nil && wait >= 0 && wait <= MaxWait
	}
	if !runnerOK || !waitOK {
		return web.Status(http.StatusUnprocessableEntity, errors.New("runner must be houston-runner-N and wait 0–25 seconds"))
	}
	ctx := r.Context()
	now := time.Now()
	if err := models.New(c.DB.Write).SeeRunner(ctx, models.SeeRunnerParams{Name: runner, Now: now}); err != nil {
		return err
	}
	deadline := now.Add(time.Duration(wait) * time.Second)
	for {
		var claimed *models.Started
		err := c.DB.Tx(ctx, func(tx *db.Tx) (err error) {
			claimed, err = models.ClaimNext(ctx, tx, runner, time.Now())
			return err
		})
		if err != nil {
			return err
		}
		if claimed != nil {
			c.cleanUpCopies(ctx, claimed.CleanUp)
		}
		if claimed != nil && claimed.Deploy.ID != 0 {
			c.Live.Refresh(FlightBoard, "")
			if err := deploys.Progress(ctx, c.Live, c.DB, claimed.Deploy, "", true); err != nil {
				c.Log.Warn("the deploy's page wasn't told", "deploy", claimed.Deploy.ID, "err", err)
			}
			job, err := c.job(ctx, *claimed)
			if err != nil {
				return err
			}
			return web.JSON(w, http.StatusOK, job)
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type jobDeploy struct {
	ID                  int64    `json:"id"`
	Number              int64    `json:"number"`
	Token               string   `json:"token"`
	Sha                 string   `json:"sha"`
	Ref                 string   `json:"ref"`
	TookOver            *int64   `json:"took_over"`
	Fresh               bool     `json:"fresh"`
	Kind                string   `json:"kind"`
	Generation          int64    `json:"generation"`
	PreviousGeneration  *int64   `json:"previous_generation"`
	PreviousAccessories []string `json:"previous_accessories"`
	PreviousVolumes     []string `json:"previous_volumes"`
	PreviousDatabases   []string `json:"previous_databases"`
	Copy                *copyJob `json:"copy,omitempty"`
}

// copyJob is a copy's deploy's: it leaves the hosts the old project serves
// for the handover, and keeps a placeholder so its service is never
// kamal-proxy's catch-all.
type copyJob struct {
	From         string   `json:"from"`
	Placeholder  string   `json:"placeholder"`
	ExcludeHosts []string `json:"exclude_hosts"`
}

type jobProject struct {
	Name        string `json:"name"`
	RepoURL     string `json:"repo_url"`
	Branch      string `json:"branch"`
	ComposePath string `json:"compose_path"`
	DeployKey   string `json:"deploy_key"`
}

type job struct {
	Deploy     jobDeploy  `json:"deploy"`
	Project    jobProject `json:"project"`
	KnownHosts []string   `json:"known_hosts"`
}

// job is what a runner needs for a claimed deploy. For a restore, the
// serving generation as the config that serves has it (a restore's check
// sync stores nothing on the project): its accessory containers, and what
// of it a snapshot holds, all of which the runner removes after the switch.
func (c Controller) job(ctx context.Context, claimed models.Started) (job, error) {
	d := claimed.Deploy
	project, err := models.New(c.DB.Read).ProjectByID(ctx, d.ProjectID)
	if err != nil {
		return job{}, err
	}
	out := job{
		Deploy: jobDeploy{ID: d.ID, Number: d.Number, Token: claimed.Token, Sha: d.Sha, Ref: d.Ref, Fresh: d.Fresh, Kind: d.Kind, Generation: d.Generation},
		Project: jobProject{Name: project.Name, RepoURL: project.RepoUrl, Branch: project.Branch, ComposePath: project.ComposePath,
			DeployKey: project.DeployKeyPrivate.Reveal()},
		KnownHosts: knownhosts.For(c.KnownHosts, project.RepoUrl),
	}
	if out.KnownHosts == nil {
		out.KnownHosts = []string{}
	}
	if claimed.TookOver > 0 {
		out.Deploy.TookOver = &claimed.TookOver
	}
	if d.Kind == "copy" {
		q := models.New(c.DB.Read)
		if cp, err := q.CopyByDeploy(ctx, sqlNumber(d.ID)); err == nil {
			shared := []string{}
			if cp.FromProjectID.Valid {
				inst, _ := q.CurrentInstallation(ctx)
				if old, err := q.ProjectByID(ctx, cp.FromProjectID.Int64); err == nil {
					theirs := old.Hostnames(inst.BaseDomain)
					for _, h := range project.Hostnames(inst.BaseDomain) {
						if slices.Contains(theirs, h) {
							shared = append(shared, h)
						}
					}
				}
			}
			out.Deploy.Copy = &copyJob{From: cp.FromName, Placeholder: models.Placeholder(project.Name), ExcludeHosts: shared}
		}
	}
	if d.Restore() {
		previous := d.Generation - 1
		serving := models.Generation{Project: project.Name, Number: previous}
		out.Deploy.PreviousGeneration = &previous
		out.Deploy.PreviousAccessories, out.Deploy.PreviousVolumes, out.Deploy.PreviousDatabases = []string{}, []string{}, []string{}
		accessories := project.Accessories()
		slices.Sort(accessories)
		for _, s := range accessories {
			out.Deploy.PreviousAccessories = append(out.Deploy.PreviousAccessories, serving.Container(s))
		}
		for _, v := range project.Volumes.V {
			out.Deploy.PreviousVolumes = append(out.Deploy.PreviousVolumes, serving.Volume(v.Name))
		}
		for _, db := range project.Databases.V {
			out.Deploy.PreviousDatabases = append(out.Deploy.PreviousDatabases, serving.Container(db.Service))
		}
	}
	return out, nil
}

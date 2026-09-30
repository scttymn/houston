package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// What the personal API shows of projects and deploys (the Rails app's
// RemoteView): no secret's value, no deploy key, no webhook secret, ever.
// Where Rails had nothing (NULL), the answer has null.

// LogChunk is the most of a deploy's log one answer carries.
const LogChunk = 256 << 10

// orNull is s, or null when it's empty.
func orNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timeOrNull(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

type deployView struct {
	Number     int64      `json:"number"`
	Kind       string     `json:"kind"`
	Fresh      bool       `json:"fresh"`
	Status     string     `json:"status"`
	Sha        string     `json:"sha"`
	Ref        string     `json:"ref"`
	Step       *string    `json:"step"`
	Error      *string    `json:"error"`
	Runner     *string    `json:"runner"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Duration   int64      `json:"duration"`
}

func viewDeploy(d models.Deploy, now time.Time) deployView {
	end := now
	if d.FinishedAt.Valid {
		end = d.FinishedAt.Time
	}
	return deployView{Number: d.Number, Kind: d.Kind, Fresh: d.Fresh, Status: d.Status, Sha: d.Sha, Ref: d.Ref, Step: orNull(d.Step),
		Error: orNull(d.Error), Runner: orNull(d.Runner), StartedAt: d.CreatedAt, FinishedAt: timeOrNull(d.FinishedAt),
		Duration: int64(end.Sub(d.CreatedAt).Seconds())}
}

type deployWithLog struct {
	deployView
	Steps   []models.StepState `json:"steps"`
	Log     string             `json:"log"`
	LogSize int                `json:"log_size"`
	LogNext int                `json:"log_next"`
}

// viewDeployWithLog is the deploy with its steps and up to LogChunk of its
// log from byte from, on whole UTF-8 characters; log_next is where to ask
// from next.
func viewDeployWithLog(d models.Deploy, from int, now time.Time) deployWithLog {
	log := d.Log
	start := max(from, 0)
	for start < len(log) && !utf8.RuneStart(log[start]) {
		start++
	}
	chunk := ""
	if start < len(log) {
		chunk = log[start:min(start+LogChunk, len(log))]
		for !utf8.ValidString(chunk) {
			chunk = chunk[:len(chunk)-1]
		}
	}
	return deployWithLog{deployView: viewDeploy(d, now), Steps: d.StepStates(), Log: chunk, LogSize: len(log), LogNext: start + len(chunk)}
}

type maintenanceView struct {
	On      bool       `json:"on"`
	Since   *time.Time `json:"since,omitempty"`
	By      *string    `json:"by,omitempty"`
	Message *string    `json:"message,omitempty"`
}

type copyProposalView struct {
	Name    string  `json:"name"`
	Sha     string  `json:"sha"`
	Deploy  int64   `json:"deploy"`
	Refusal *string `json:"refusal"`
}

type projectView struct {
	Name         string                        `json:"name"`
	Status       string                        `json:"status"`
	RunningSha   *string                       `json:"running_sha"`
	Host         string                        `json:"host"`
	Domains      map[string]models.DomainState `json:"domains"`
	LastDeploy   *deployView                   `json:"last_deploy"`
	Maintenance  maintenanceView               `json:"maintenance"`
	Deleting     any                           `json:"deleting"` // with deletions, batch 3
	CopyProposal *copyProposalView             `json:"copy_proposal"`
	Stats        any                           `json:"stats"` // with app stats, batch 3
}

type secretState struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Set      bool   `json:"set"`
}

type projectDetail struct {
	projectView
	DeployRule      any           `json:"deploy_rule"`
	Services        []string      `json:"services"`
	RepoURL         *string       `json:"repo_url"`
	Branch          *string       `json:"branch"`
	WebhookVerified bool          `json:"webhook_verified"`
	LastBackup      *backupView   `json:"last_backup"`
	BackupSchedule  string        `json:"backup_schedule"`
	TimeZone        string        `json:"time_zone"`
	Secrets         []secretState `json:"secrets"`
}

// viewProject is a project as the list shows it.
func (c Controller) viewProject(ctx context.Context, p models.Project, inst models.Installation, now time.Time) (projectView, error) {
	q := models.New(c.DB.Read)
	v := projectView{Name: p.Name, Status: "standby", Host: p.Host(inst.BaseDomain), Domains: states(p.DomainStates.V), Maintenance: maintenanceView{On: false}}
	if p.MaintenanceSince.Valid {
		v.Maintenance = maintenanceView{On: true, Since: &p.MaintenanceSince.Time, By: orNull(p.MaintenanceBy), Message: orNull(p.MaintenanceMessage)}
	}
	latest, err := q.DeploySummaries(ctx, models.DeploySummariesParams{ProjectID: p.ID, Limit: 1})
	if err != nil {
		return v, err
	}
	if len(latest) == 1 {
		d := models.Deploy(latest[0])
		dv := viewDeploy(d, now)
		v.Status, v.LastDeploy = d.Status, &dv
		if d.Status == "hold" && d.ProposedName != "" {
			refusal, err := c.nameRefusal(ctx, d.ProposedName)
			if err != nil {
				return v, err
			}
			v.CopyProposal = &copyProposalView{Name: d.ProposedName, Sha: d.Sha, Deploy: d.Number, Refusal: refusal}
		}
	}
	if deletion, err := q.HoldingDeletion(ctx, sqlNumber(p.ID)); err == nil {
		v.Deleting = c.viewDeletion(ctx, deletion, false)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	running, err := q.RunningDeploySummary(ctx, p.ID)
	if err == nil {
		v.RunningSha = &running.Sha
	} else if !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	return v, nil
}

// viewProjectDetail is one project as it's shown alone.
func (c Controller) viewProjectDetail(ctx context.Context, p models.Project, inst models.Installation, now time.Time) (projectDetail, error) {
	v, err := c.viewProject(ctx, p, inst, now)
	if err != nil {
		return projectDetail{}, err
	}
	var rule any
	if err := json.Unmarshal(p.DeployRule.V, &rule); err != nil {
		return projectDetail{}, err
	}
	d := projectDetail{projectView: v, DeployRule: rule, Services: p.Services.V, RepoURL: orNull(p.RepoUrl), Branch: orNull(p.Branch),
		WebhookVerified: p.WebhookVerifiedAt.Valid, BackupSchedule: p.BackupSchedule, TimeZone: inst.TimeZone, Secrets: []secretState{}}
	last, err := models.New(c.DB.Read).LastBackup(ctx, p.ID)
	if err == nil {
		bv := viewBackup(last, now)
		d.LastBackup = &bv
	} else if !errors.Is(err, sql.ErrNoRows) {
		return d, err
	}
	values, err := models.Secrets(ctx, models.New(c.DB.Read), p.ID)
	if err != nil {
		return d, err
	}
	for _, variable := range p.Variables.V {
		d.Secrets = append(d.Secrets, secretState{Name: variable.Name, Required: variable.Required, Set: strings.TrimSpace(values[variable.Name]) != ""})
	}
	return d, nil
}

// nameRefusal is why name can't be copied to, or nil when it can.
func (c Controller) nameRefusal(ctx context.Context, name string) (*string, error) {
	if !models.ValidName(name) {
		return orNull(name + " can't be a project's name"), nil
	}
	taken, err := models.New(c.DB.Read).ProjectExists(ctx, name)
	if err != nil || !taken {
		return nil, err
	}
	return orNull(name + " is another project"), nil
}

// storageView is a storage location as the API shows it.
type storageView struct {
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Where      string     `json:"where"`
	Live       bool       `json:"live"`
	Default    bool       `json:"default"`
	Confirmed  bool       `json:"confirmed"`
	UsedBy     int        `json:"used_by"`
	LastWrite  *time.Time `json:"last_write"`
	PrunedAt   *time.Time `json:"pruned_at"`
	PruneError *string    `json:"prune_error"`
}

func (c Controller) viewStorage(ctx context.Context, l models.StorageLocation) (storageView, error) {
	q := models.New(c.DB.Read)
	using, err := q.ProjectsUsingLocation(ctx, models.ProjectsUsingLocationParams{Location: sqlNumber(l.ID), IsDefaultAndReady: l.IsDefault && l.AcknowledgedAt.Valid})
	if err != nil {
		return storageView{}, err
	}
	v := storageView{Name: l.Name, Kind: l.Kind, Where: models.WhereItIs(l), Live: l.Kind == "nfs" || l.Kind == "local", Default: l.IsDefault,
		Confirmed: l.AcknowledgedAt.Valid, UsedBy: len(using), PrunedAt: timeOrNull(l.PrunedAt), PruneError: orNull(l.PruneError)}
	last, err := q.LocationLastWrite(ctx, l.ID)
	if err == nil {
		v.LastWrite = timeOrNull(last)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	return v, nil
}

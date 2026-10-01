package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// DeployRuleWords is what deploys the project, in words: "Every commit to
// main", "Tags matching v*".
func (p Project) DeployRuleWords() string {
	var rule struct{ On, Branch, Tags string }
	json.Unmarshal(p.DeployRule.V, &rule)
	if rule.On == "tag" {
		if rule.Tags == "" {
			rule.Tags = "v*"
		}
		return "Tags matching " + rule.Tags
	}
	if rule.Branch == "" {
		rule.Branch = "main"
	}
	return "Every commit to " + rule.Branch
}

// ServiceImage is a service's image as compose.yml names it, or "".
func (p Project) ServiceImage(name string) string { return p.Details.V.Images[name] }

// ShortSha is the commit's first 7.
func (d Deploy) ShortSha() string { return d.Sha[:min(7, len(d.Sha))] }

// Duration is how long it ran, or has run so far.
func (d Deploy) Duration(now time.Time) time.Duration {
	if d.FinishedAt.Valid {
		now = d.FinishedAt.Time
	}
	return now.Sub(d.CreatedAt)
}

// NextAt is when the project's next scheduled backup is due: today's,
// unless it ran already.
func (s Schedule) NextAt(ctx context.Context, q *Queries, projectID int64, now time.Time) (time.Time, error) {
	day := s.Today(now)
	_, err := q.ScheduledRun(ctx, ScheduledRunParams{ProjectID: projectID, ScheduledFor: sql.NullString{String: day, Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		return s.DueAt(day), nil
	}
	if err != nil {
		return time.Time{}, err
	}
	next, _ := time.ParseInLocation(time.DateOnly, day, s.Zone)
	return s.DueAt(next.AddDate(0, 0, 1).Format(time.DateOnly)), nil
}

// UpdateShownFor is how long the board says how the last update went.
const UpdateShownFor = 24 * time.Hour

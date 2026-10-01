package settings

import (
	"time"

	"github.com/scttymn/gantry/text"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/projects"
)

func orWords(s, words string) string {
	if s == "" {
		return words
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// repairState is a repair result's chip: GO for OK and DNS OK.
func repairState(state string) string {
	if state == "OK" || state == "DNS OK" {
		return "go"
	}
	return "nogo"
}

func stepWords(step string) string {
	if step == "" {
		return "…"
	}
	return " · " + step
}

func checkedWords(s Show) string {
	if !s.Inst.LatestReleaseCheckedAt.Valid {
		return ""
	}
	return " (checked " + projects.Ago(s.Inst.LatestReleaseCheckedAt.Time, s.Now) + " ago)"
}

func updateTime(u models.ServerUpdate, now time.Time) string {
	if u.FinishedAt.Valid {
		return projects.DurationWords(u.FinishedAt.Time.Sub(u.StartedAt))
	}
	return projects.MissionElapsed(now.Sub(u.StartedAt))
}

func holdsWords(l models.StorageLocation) string {
	if l.Live() {
		return "live volumes · backups"
	}
	return "backups"
}

func usedWords(n int) string {
	if n == 0 {
		return "not used yet"
	}
	return text.Count(n, "project")
}

func lastWrite(t *time.Time, now time.Time) string {
	if t == nil {
		return "—"
	}
	return projects.ShortTime(t.In(now.Location()))
}

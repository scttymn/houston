package models_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/test"
)

// A daily time in the installation's zone: due from it until the day's run
// is made; the local day is the zone's.
func TestSchedule(t *testing.T) {
	denver, _ := time.LoadLocation("America/Denver")
	s, err := models.ParseSchedule("daily 03:00", denver)
	if err != nil {
		t.Fatal(err)
	}
	d := test.DB(t)
	d.Write.Exec(`INSERT INTO storage_locations (id, name, kind) VALUES (1, 'nas', 'nfs')`)
	d.Write.Exec(`INSERT INTO projects (id, name, app_service, services, health, port) VALUES (1, 'shop', 'web', '["web"]', '/', 80)`)
	q := models.New(d.Read)
	ctx := context.Background()
	for _, c := range []struct {
		utc string
		due bool
		day string
	}{
		{"2026-09-30T08:59:00Z", false, "2026-09-30"}, // 02:59 in Denver
		{"2026-09-30T09:00:00Z", true, "2026-09-30"},  // 03:00
		{"2026-10-01T05:00:00Z", true, "2026-09-30"},  // 23:00 the same local day
	} {
		now, _ := time.Parse(time.RFC3339, c.utc)
		if due, _ := s.Due(ctx, q, 1, now); due != c.due || s.Today(now) != c.day {
			t.Errorf("%s: due %v, day %s", c.utc, due, s.Today(now))
		}
	}
	d.Write.Exec(`INSERT INTO backup_runs (project_id, location_id, kind, reason, scheduled_for, heartbeat_at) VALUES (1, 1, 'auto', 'schedule', '2026-09-30', CURRENT_TIMESTAMP)`)
	now, _ := time.Parse(time.RFC3339, "2026-09-30T10:00:00Z")
	if due, _ := s.Due(ctx, q, 1, now); due {
		t.Error("due again the same day")
	}
	if s.Words() != "Daily at 03:00 (America/Denver)" {
		t.Errorf("words %q", s.Words())
	}
	if _, err := models.ParseSchedule("weekly", denver); err == nil {
		t.Error("weekly parsed")
	}
	_ = sql.ErrNoRows
}

// The next scheduled backup is today's, unless today's ran: then tomorrow's.
func TestNextAt(t *testing.T) {
	d := test.DB(t)
	q := models.New(d.Read)
	d.Write.Exec(`INSERT INTO storage_locations (id, name, kind) VALUES (1, 'nas', 'nfs')`)
	d.Write.Exec(`INSERT INTO projects (id, name, app_service, services, health, port) VALUES (1, 'shop', 'web', '["web"]', '/', 80)`)
	denver, _ := time.LoadLocation("America/Denver")
	s, _ := models.ParseSchedule("daily 03:00", denver)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, denver)
	if next, err := s.NextAt(context.Background(), q, 1, now); err != nil || !next.Equal(time.Date(2026, 9, 30, 3, 0, 0, 0, denver)) {
		t.Errorf("= %v %v", next, err)
	}
	d.Write.Exec(`INSERT INTO backup_runs (project_id, location_id, kind, reason, scheduled_for, heartbeat_at) VALUES (1, 1, 'auto', 'schedule', '2026-09-30', CURRENT_TIMESTAMP)`)
	if next, err := s.NextAt(context.Background(), q, 1, now); err != nil || !next.Equal(time.Date(2026, 10, 1, 3, 0, 0, 0, denver)) {
		t.Errorf("after today's = %v %v", next, err)
	}
}

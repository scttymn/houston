package models

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/scttymn/gantry/db"
)

// Schedule is a project's daily backup time, in the installation's time
// zone: one scheduled run a local day; a repeated hour (the clocks going
// back) runs once, the first.
type Schedule struct {
	Hour, Minute int
	Zone         *time.Location
}

// ParseSchedule reads "daily HH:MM" in zone.
func ParseSchedule(s string, zone *time.Location) (Schedule, error) {
	m := ScheduleFormat.FindStringSubmatch(s)
	if m == nil {
		return Schedule{}, fmt.Errorf("%q isn't a backup schedule", s)
	}
	hour, _ := strconv.Atoi(m[1])
	minute, _ := strconv.Atoi(m[2])
	return Schedule{Hour: hour, Minute: minute, Zone: zone}, nil
}

// Zone is the installation's time zone (UTC if it doesn't name one).
func (i Installation) Zone() *time.Location {
	if z, err := time.LoadLocation(i.TimeZone); err == nil && ValidTimeZone(i.TimeZone) {
		return z
	}
	return time.UTC
}

// Today is now's local day, YYYY-MM-DD.
func (s Schedule) Today(now time.Time) string { return now.In(s.Zone).Format(time.DateOnly) }

// DueAt is when day's run is due.
func (s Schedule) DueAt(day string) time.Time {
	d, _ := time.ParseInLocation(time.DateOnly, day, s.Zone)
	return time.Date(d.Year(), d.Month(), d.Day(), s.Hour, s.Minute, 0, 0, s.Zone)
}

// Due is whether today's run is due at now and hasn't been made.
func (s Schedule) Due(ctx context.Context, q *Queries, projectID int64, now time.Time) (bool, error) {
	day := s.Today(now)
	if now.Before(s.DueAt(day)) {
		return false, nil
	}
	_, err := q.ScheduledRun(ctx, ScheduledRunParams{ProjectID: projectID, ScheduledFor: sql.NullString{String: day, Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// Words are the schedule for a page: "Daily at 03:00 (America/Denver)".
func (s Schedule) Words() string {
	return fmt.Sprintf("Daily at %02d:%02d (%s)", s.Hour, s.Minute, s.Zone)
}

// RequestScheduledBackup queues day's scheduled backup of p to location,
// once: the day's run is returned as it is.
func RequestScheduledBackup(ctx context.Context, tx *db.Tx, jobs Enqueuer[BackupArgs], p Project, location StorageLocation, day string, now time.Time) (BackupRun, error) {
	q := New(tx)
	scheduled := sql.NullString{String: day, Valid: true}
	run, err := q.ScheduledRun(ctx, ScheduledRunParams{ProjectID: p.ID, ScheduledFor: scheduled})
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return run, err
	}
	run, err = q.CreateScheduledRun(ctx, CreateScheduledRunParams{ProjectID: p.ID, LocationID: location.ID, ScheduledFor: scheduled, HeartbeatAt: now})
	if err != nil {
		return BackupRun{}, err
	}
	_, err = jobs.EnqueueTx(ctx, tx, BackupArgs{RunID: run.ID})
	return run, err
}

package models

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/scttymn/gantry/db"
)

// Live is a location that can hold live volumes: a share or a folder, not
// an object store.
func (l StorageLocation) Live() bool { return l.Kind == "nfs" || l.Kind == "local" }

// NoVolume is a volume the project doesn't have.
type NoVolume struct{ Project, Name string }

func (e NoVolume) Error() string { return e.Project + " has no volume " + e.Name }

// Placed is a volume made already: where it lives is settled.
type Placed struct{ Msg string }

func (e Placed) Error() string { return e.Msg }

// ChooseVolume is where one of p's volumes will live (nil: local disk),
// until Houston makes it at its first deploy; moving it after is a later
// feature.
func ChooseVolume(ctx context.Context, d *db.DB, p Project, name string, location *StorageLocation, now time.Time) (ProjectVolume, error) {
	var row ProjectVolume
	found := false
	for _, v := range p.Volumes.V {
		found = found || v.Name == name
	}
	if !found {
		return row, NoVolume{Project: p.Name, Name: name}
	}
	if location != nil {
		if !location.Live() {
			return row, Refused{fmt.Sprintf("%s can't hold live volumes (backups only)", location.Name)}
		}
		if !location.AcknowledgedAt.Valid {
			return row, Refused{fmt.Sprintf("%s isn't set up yet", location.Name)}
		}
	}
	err := d.Tx(ctx, func(tx *db.Tx) (err error) {
		q := New(tx)
		if err := q.EnsureProjectVolume(ctx, EnsureProjectVolumeParams{ProjectID: p.ID, Name: name}); err != nil {
			return err
		}
		if row, err = q.ProjectVolumeByName(ctx, ProjectVolumeByNameParams{ProjectID: p.ID, Name: name}); err != nil {
			return err
		}
		if row.PlacedAt.Valid {
			where, err := VolumeWhere(ctx, q, row)
			if err != nil {
				return err
			}
			return Placed{fmt.Sprintf("%s is already on %s; moving a volume is a later feature", name, where)}
		}
		var id sql.NullInt64
		if location != nil {
			id = sql.NullInt64{Int64: location.ID, Valid: true}
		}
		row.LocationID = id
		return q.SetVolumeLocation(ctx, SetVolumeLocationParams{LocationID: id, UpdatedAt: now, ID: row.ID})
	})
	return row, err
}

// VolumeWhere is where a volume lives, in words: its location's name, or
// local disk.
func VolumeWhere(ctx context.Context, q *Queries, row ProjectVolume) (string, error) {
	if !row.LocationID.Valid {
		return "local disk", nil
	}
	l, err := q.StorageLocationByID(ctx, row.LocationID.Int64)
	return l.Name, err
}

// ChooseBackupTarget is where p's backups go (nil: the default), and is
// that location, or nil when there's none.
func ChooseBackupTarget(ctx context.Context, d *db.DB, p Project, location *StorageLocation, now time.Time) (*StorageLocation, error) {
	var id sql.NullInt64
	if location != nil {
		if !location.AcknowledgedAt.Valid {
			return nil, Refused{fmt.Sprintf("%s isn't set up yet", location.Name)}
		}
		id = sql.NullInt64{Int64: location.ID, Valid: true}
	}
	if err := New(d.Write).SetBackupLocation(ctx, SetBackupLocationParams{BackupLocationID: id, UpdatedAt: now, ID: p.ID}); err != nil {
		return nil, err
	}
	l, err := New(d.Read).BackupLocationFor(ctx, id.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &l, err
}

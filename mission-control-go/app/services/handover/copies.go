package handover

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// lock is held by a handover, and by a copy's cancel and undo, so a cancel
// and a handover never cross (the Rails app's copy.with_lock; one Mission
// Control process serves them all).
var lock sync.Mutex

// Locked runs fn holding the copies' lock.
func Locked(fn func() error) error {
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

// Cancel ends a copy that hasn't taken hosts over: its deploy NO-GO, and
// cleanUp is the copy whose new project goes (the caller queues it).
func Cancel(ctx context.Context, d *db.DB, id int64, by string, now time.Time) (cleanUp int64, err error) {
	err = Locked(func() error {
		return d.Tx(ctx, func(tx *db.Tx) error {
			cp, err := models.New(tx).CopyByID(ctx, id)
			if err != nil {
				return err
			}
			cleanUp, err = models.CancelCopy(ctx, tx, cp, by, now)
			return err
		})
	})
	return cleanUp, err
}

// Undo undoes a copy that went, while the old project is there: the shared
// hosts go back to it, then the new project p is deleted (its backups
// kept, as any delete). Refused (models.Refused) is why it can't; the
// deletion is the caller's to queue.
func (h Handover) Undo(ctx context.Context, p models.Project, cp models.ProjectCopy, confirm, by string, now time.Time) (models.ProjectDeletion, error) {
	refuse := func(format string, args ...any) (models.ProjectDeletion, error) {
		return models.ProjectDeletion{}, models.Refused{Msg: fmt.Sprintf(format, args...)}
	}
	switch {
	case confirm != cp.ToName:
		return refuse("type %s to confirm", cp.ToName)
	case cp.Status != "go":
		return refuse("the copy to %s isn't done", cp.ToName)
	case !cp.FromProjectID.Valid:
		return refuse("%s is deleted: there's nothing to go back to", cp.FromName)
	case !cp.ProjectID.Valid:
		return refuse("%s is gone already", cp.ToName)
	}
	if err := Locked(func() error { return h.Back(ctx, cp) }); err != nil {
		var failed Failed
		if errors.As(err, &failed) {
			return refuse("the hosts didn't go back to %s: %s", cp.FromName, failed)
		}
		return models.ProjectDeletion{}, err
	}
	var deletion models.ProjectDeletion
	err := h.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		if deletion, err = models.RequestDeletion(ctx, tx, p, p.Name, false, by, now); err != nil {
			return err
		}
		return models.New(tx).MarkUndone(ctx, models.MarkUndoneParams{Now: sql.NullTime{Time: now, Valid: true}, ID: cp.ID})
	})
	var refused models.Refused
	if errors.As(err, &refused) {
		return refuse("the hosts are %s's again, but %s wasn't deleted: %s", cp.FromName, cp.ToName, refused.Msg)
	}
	return deletion, err
}

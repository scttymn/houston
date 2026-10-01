package models

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
)

// uncarriable are what Kamal's docker env files can't carry unchanged: a
// backslash, a tab, a line break or any other control character.
var uncarriable = regexp.MustCompile(`[\\\x00-\x1f\x7f]`)

// ValidSecret checks a value for key, in words: it's
// refused where the admin can still do something about it.
func ValidSecret(key, value string) error {
	var problems []string
	if strings.TrimSpace(value) == "" {
		problems = append(problems, "needs a value")
	}
	if uncarriable.MatchString(value) {
		problems = append(problems, "can't contain a backslash, line break, tab or other control character: Kamal would change it on the way to the app. Base64-encode it instead.")
	}
	if problems != nil {
		return Refused{key + " " + sentence(problems)}
	}
	return nil
}

// GeneratedSecret is a value nobody needs to know: 64 random bytes, URL-safe
// base64 (86 characters). Frameworks' keys want at least 64 bytes; any
// password takes it too.
func GeneratedSecret() string {
	b := make([]byte, 64)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// RequestManualBackup queues a backup now, to p's backup location; one
// already queued is returned as it is (a double click).
func RequestManualBackup(ctx context.Context, tx *db.Tx, jobs Enqueuer[BackupArgs], p Project, now time.Time) (BackupRun, error) {
	q := New(tx)
	if err := refuseWhileDeleting(ctx, q, p); err != nil {
		return BackupRun{}, err
	}
	served, err := q.ProjectHasServed(ctx, p.ID)
	if err != nil {
		return BackupRun{}, err
	}
	if !served {
		return BackupRun{}, Refused{NothingDeployed}
	}
	location, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupRun{}, Refused{"no backup storage yet (finish setup's storage step)"}
	}
	if err != nil {
		return BackupRun{}, err
	}
	run, err := q.QueuedManualBackup(ctx, p.ID)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return run, err
	}
	run, err = q.CreateBackupRun(ctx, CreateBackupRunParams{ProjectID: p.ID, LocationID: location.ID, Operation: "backup", Kind: "auto",
		Reason: "manual", HeartbeatAt: now})
	if err != nil {
		return BackupRun{}, err
	}
	_, err = jobs.EnqueueTx(ctx, tx, BackupArgs{RunID: run.ID})
	return run, err
}

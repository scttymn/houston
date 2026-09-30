package models

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scttymn/gantry/db"
)

// RegistryCleanupStale is how long the registry's cleanup holds its lock
// at most: after this, a Mission Control that stopped while it ran is
// forgotten.
const RegistryCleanupStale = 30 * time.Minute

// Busy is a deploy in the way: in flight, and heard from lately.
type Busy struct{ Deploy Deploy }

func (e Busy) Error() string {
	return fmt.Sprintf("deploy #%d is in flight (last heard from %s)", e.Deploy.Number, e.Deploy.HeartbeatAt.UTC().Format(time.RFC3339))
}

// ErrRegistryBusy: Houston's registry is being garbage-collected, and
// nothing may push meanwhile.
var ErrRegistryBusy = errors.New("Houston is cleaning its registry; try again in a minute")

// Invalid is a record that doesn't pass its rules, in Rails' full messages.
type Invalid []string

func (e Invalid) Error() string { return sentence(e) }

// sentence joins as Rails' to_sentence.
func sentence(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}

var shaFormat = regexp.MustCompile(`^[0-9a-f]{40}$`)

// validCommit checks a deploy's commit and ref, as the Rails model does.
func validCommit(sha, ref string) error {
	var errs Invalid
	if !shaFormat.MatchString(sha) {
		errs = append(errs, "Sha must be a full commit SHA (40 lowercase hex characters)")
	}
	if strings.TrimSpace(ref) == "" {
		errs = append(errs, "Ref can't be blank")
	} else if utf8.RuneCountInString(ref) > 255 {
		errs = append(errs, "Ref is too long (maximum is 255 characters)")
	}
	if errs != nil {
		return errs
	}
	return nil
}

// NewToken is a deploy's token: 32 random bytes, URL-safe.
func NewToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Started is a deploy houston deploy started: the row, the token only it
// holds, and the number of the silent one it took over (0: none).
type Started struct {
	Deploy   Deploy
	Token    string
	TookOver int64
	// CleanUp are the copies whose new project goes, as their deploy was
	// abandoned (the caller queues them).
	CleanUp []int64
}

// StartDeploy starts p's next deploy, in tx. A silent in-flight deploy (no
// word for StaleAfter) is finished as abandoned; a live one is Busy.
func StartDeploy(ctx context.Context, tx *db.Tx, p Project, sha, ref string, now time.Time) (Started, error) {
	q := New(tx)
	if err := refuseWhileDeleting(ctx, q, p); err != nil {
		return Started{}, err
	}
	cleaning, err := q.RegistryCleaning(ctx, sql.NullTime{Time: now.Add(-RegistryCleanupStale), Valid: true})
	if err != nil {
		return Started{}, err
	}
	if cleaning {
		return Started{}, ErrRegistryBusy
	}
	var tookOver, cleanUp int64
	current, err := q.DeployInFlight(ctx, p.ID)
	switch {
	case err == nil && current.HeartbeatAt.After(now.Add(-StaleAfter)):
		return Started{}, Busy{Deploy: current}
	case err == nil:
		if cleanUp, err = abandon(ctx, q, current, now); err != nil {
			return Started{}, err
		}
		tookOver = current.Number
	case !errors.Is(err, sql.ErrNoRows):
		return Started{}, err
	}
	if err := validCommit(sha, ref); err != nil {
		return Started{}, err
	}
	number, err := q.NextDeployNumber(ctx, p.ID)
	if err != nil {
		return Started{}, err
	}
	token := NewToken()
	d, err := q.CreateDeploy(ctx, CreateDeployParams{ProjectID: p.ID, Number: number, Kind: "deploy", Status: "in_flight", Sha: sha, Ref: ref,
		Generation: p.DataGeneration, TokenDigest: Digest(token), HeartbeatAt: now})
	if err != nil {
		return Started{}, err
	}
	started := Started{Deploy: d, Token: token, TookOver: tookOver}
	if cleanUp != 0 {
		started.CleanUp = []int64{cleanUp}
	}
	return started, nil
}

// abandon finishes a silent in-flight deploy, and settles its copy when
// it's a copy's: cleanUp is that copy when its new project goes.
func abandon(ctx context.Context, q *Queries, d Deploy, now time.Time) (cleanUp int64, err error) {
	d.Status, d.Error = "no_go", "abandoned: no word from houston deploy since "+d.HeartbeatAt.UTC().Format(time.RFC3339)
	if err := q.AbandonDeploy(ctx, AbandonDeployParams{ID: d.ID, Now: sql.NullTime{Time: now, Valid: true}, Error: d.Error}); err != nil {
		return 0, err
	}
	return SettleCopy(ctx, q, d, now)
}

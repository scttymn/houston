package models_test

import (
	"context"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app/models"
)

// C4 / C10 (TLA trace: Claim -> Tick -> Tick -> Claim). A runner claims a queued
// push; it is still fetching (no heartbeat before deploy.go:227) or its claim
// answer was lost. Past StaleAfter the next claim must still deploy that
// commit: the row goes back on the queue and is handed to the next runner,
// not recorded NO-GO and dropped. seen_refs already moved, so the poll
// will not queue it again.
func TestTLA_C10_AbandonedClaimLosesThePush(t *testing.T) {
	ctx, t0 := context.Background(), time.Now()
	d, p := shopWith(t, `{}`)
	if err := d.Tx(ctx, func(tx *db.Tx) error {
		_, err := models.QueueDeploy(ctx, tx, p, sha("a"), "refs/heads/main", false, t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	claim := func(runner string, at time.Time) (s *models.Started) {
		if err := d.Tx(ctx, func(tx *db.Tx) (err error) { s, err = models.ClaimNext(ctx, tx, runner, at); return err }); err != nil {
			t.Fatal(err)
		}
		return s
	}
	first := claim("houston-runner-1", t0)
	if first == nil || first.Deploy.ID == 0 {
		t.Fatal("nothing claimed")
	}
	// houston-runner-1 is still in git fetch; houston-runner-2 polls every second.
	second := claim("houston-runner-2", t0.Add(models.StaleAfter+time.Second))
	if second == nil || second.Deploy.ID != first.Deploy.ID || second.Deploy.Sha != sha("a") || second.Deploy.Runner != "houston-runner-2" {
		if second == nil {
			t.Errorf("the next runner got nothing; deploy #%d of %s was dropped after the first claim went stale", first.Deploy.Number, first.Deploy.Sha[:7])
			return
		}
		t.Errorf("the next runner did not get the same push of %s: id=%d sha=%s runner=%s", sha("a")[:7], second.Deploy.ID, second.Deploy.Sha, second.Deploy.Runner)
		return
	}
	if second.Token == "" || second.Token == first.Token || second.TookOver != 0 {
		t.Errorf("reclaimed deploy kept the old token or was marked taken over: token equal=%v took_over=%d", second.Token == first.Token, second.TookOver)
	}
}

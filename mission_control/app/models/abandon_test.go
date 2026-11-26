package models_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app/models"
)

func claimAt(t *testing.T, d *db.DB, runner string, at time.Time) *models.Started {
	t.Helper()
	var s *models.Started
	if err := d.Tx(context.Background(), func(tx *db.Tx) (err error) {
		s, err = models.ClaimNext(context.Background(), tx, runner, at)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func queuePush(t *testing.T, d *db.DB, p models.Project, commit, ref string, at time.Time) {
	t.Helper()
	if err := d.Tx(context.Background(), func(tx *db.Tx) error {
		_, err := models.QueueDeploy(context.Background(), tx, p, commit, ref, false, at)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func deployByID(t *testing.T, d *db.DB, id int64) models.Deploy {
	t.Helper()
	got, err := models.New(d.Read).DeployByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// C4 / C10 (TLA trace: Claim -> Tick -> Tick -> Claim). A runner claims a queued
// push; it is still fetching (no heartbeat before deploy.go:227) or its claim
// answer was lost. Past StaleAfter the next claim must still deploy that
// commit: the row goes back on the queue and is handed to the next runner,
// not recorded NO-GO and dropped. seen_refs already moved, so the poll
// will not queue it again.
func TestTLA_C10_AbandonedClaimLosesThePush(t *testing.T) {
	t0 := time.Now()
	d, p := shopWith(t, `{}`)
	queuePush(t, d, p, sha("a"), "refs/heads/main", t0)
	first := claimAt(t, d, "houston-runner-1", t0)
	if first == nil || first.Deploy.ID == 0 {
		t.Fatal("nothing claimed")
	}
	// houston-runner-1 is still in git fetch; houston-runner-2 polls every second.
	second := claimAt(t, d, "houston-runner-2", t0.Add(models.StaleAfter+time.Second))
	if second == nil || second.Deploy.ID != first.Deploy.ID || second.Deploy.Sha != sha("a") || second.Deploy.Runner != "houston-runner-2" {
		if second == nil {
			t.Errorf("the next runner got nothing; deploy #%d of %s was dropped after the first claim went stale", first.Deploy.Number, first.Deploy.Sha[:7])
			return
		}
		t.Errorf("the next runner did not get the same push of %s: id=%d sha=%s runner=%s", sha("a")[:7], second.Deploy.ID, second.Deploy.Sha, second.Deploy.Runner)
		return
	}
	if second.Token == "" || second.Token == first.Token || second.TookOver != first.Deploy.Number {
		t.Errorf("reclaimed deploy kept the old token or did not set took_over: token equal=%v took_over=%d, want #%d so the runner releases Kamal's lock", second.Token == first.Token, second.TookOver, first.Deploy.Number)
	}
	if second.Deploy.OwnedBy(first.Token) || !second.Deploy.OwnedBy(second.Token) {
		t.Errorf("the first runner's token still matches the reclaimed row, or the new token does not")
	}
	if !strings.Contains(second.Deploy.Log, "requeued: houston-runner-1 went silent before starting") {
		t.Errorf("requeued row log = %q; want a line naming the silent runner", second.Deploy.Log)
	}
}

func TestStaleClaimIsAbandonedWhenAnotherDeployIsQueued(t *testing.T) {
	t0 := time.Now()
	d, p := shopWith(t, `{}`)
	queuePush(t, d, p, sha("a"), "refs/heads/main", t0)
	first := claimAt(t, d, "houston-runner-1", t0)
	if first == nil {
		t.Fatal("nothing claimed")
	}
	queuePush(t, d, p, sha("b"), "refs/heads/main", t0.Add(time.Minute))
	second := claimAt(t, d, "houston-runner-2", t0.Add(models.StaleAfter+time.Second))
	got := deployByID(t, d, first.Deploy.ID)
	if got.Status != "no_go" || !strings.HasPrefix(got.Error, "abandoned: ") {
		t.Errorf("stale claim with a later push queued: status %s error %q; want no_go abandoned", got.Status, got.Error)
	}
	if second == nil || second.Deploy.Sha != sha("b") || second.TookOver != first.Deploy.Number {
		t.Errorf("next claim %+v; want the later push of %s, took_over #%d", second, sha("b")[:7], first.Deploy.Number)
	}
}

func TestStaleClaimThatAlreadyStartedIsAbandoned(t *testing.T) {
	t0 := time.Now()
	d, p := shopWith(t, `{}`)
	queuePush(t, d, p, sha("a"), "refs/heads/main", t0)
	first := claimAt(t, d, "houston-runner-1", t0)
	if first == nil {
		t.Fatal("nothing claimed")
	}
	if _, err := d.Write.Exec(`UPDATE deploys SET step = 'build' WHERE id = ?`, first.Deploy.ID); err != nil {
		t.Fatal(err)
	}
	second := claimAt(t, d, "houston-runner-2", t0.Add(models.StaleAfter+time.Second))
	got := deployByID(t, d, first.Deploy.ID)
	if second != nil {
		t.Errorf("next claim got deploy #%d; want nothing, the started one should have been abandoned", second.Deploy.Number)
	}
	if got.Status != "no_go" || !strings.HasPrefix(got.Error, "abandoned: ") {
		t.Errorf("started stale claim: status %s error %q; want no_go abandoned", got.Status, got.Error)
	}
}

func TestStaleRestoreClaimIsAbandoned(t *testing.T) {
	ctx, t0 := context.Background(), time.Now()
	d, p := shopWith(t, `{}`)
	if _, err := models.New(d.Write).CreateRestore(ctx, models.CreateRestoreParams{
		ProjectID: p.ID, Number: 1, Sha: sha("9"), Ref: "refs/heads/main",
		Generation: p.DataGeneration + 1, HeartbeatAt: t0, SourceSnapshotID: "snap-1",
	}); err != nil {
		t.Fatal(err)
	}
	first := claimAt(t, d, "houston-runner-1", t0)
	if first == nil || first.Deploy.Kind != "restore" {
		t.Fatalf("claimed %+v; want the restore", first)
	}
	second := claimAt(t, d, "houston-runner-2", t0.Add(models.StaleAfter+time.Second))
	got := deployByID(t, d, first.Deploy.ID)
	if second != nil {
		t.Errorf("next claim got deploy #%d kind %s; want the restore left abandoned, nothing queued", second.Deploy.Number, second.Deploy.Kind)
	}
	if got.Status != "no_go" || got.Kind != "restore" || !strings.HasPrefix(got.Error, "abandoned: ") {
		t.Errorf("stale restore: %+v; want no_go abandoned", got)
	}
}

func TestStaleCopyClaimIsAbandoned(t *testing.T) {
	ctx, t0 := context.Background(), time.Now()
	d, shop := shopWith(t, `{}`)
	store := addProject(t, d, 2, "store")
	var copy models.ProjectCopy
	if err := d.Tx(ctx, func(tx *db.Tx) error {
		q := models.New(tx)
		dep, err := q.CreateCopyDeploy(ctx, models.CreateCopyDeployParams{
			ProjectID: store.ID, Sha: sha("a"), Ref: "refs/heads/main", HeartbeatAt: t0,
		})
		if err != nil {
			return err
		}
		copy, err = q.CreateCopy(ctx, models.CreateCopyParams{
			ProjectID:     sql.NullInt64{Int64: store.ID, Valid: true},
			FromProjectID: sql.NullInt64{Int64: shop.ID, Valid: true},
			DeployID:      sql.NullInt64{Int64: dep.ID, Valid: true},
			FromName:      "shop",
			ToName:        "store",
			Sha:           sha("a"),
			RequestedBy:   "test",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	first := claimAt(t, d, "houston-runner-1", t0)
	if first == nil || first.Deploy.Kind != "copy" {
		t.Fatalf("claimed %+v; want the copy", first)
	}
	second := claimAt(t, d, "houston-runner-2", t0.Add(models.StaleAfter+time.Second))
	got := deployByID(t, d, first.Deploy.ID)
	if second != nil && second.Deploy.ID != 0 {
		t.Errorf("next claim got deploy #%d kind %s; want the copy left abandoned, not requeued", second.Deploy.Number, second.Deploy.Kind)
	}
	if got.Status != "no_go" || got.Kind != "copy" || !strings.HasPrefix(got.Error, "abandoned: ") {
		t.Errorf("stale copy deploy: %+v; want no_go abandoned", got)
	}
	settled, err := models.New(d.Read).CopyByID(ctx, copy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "no_go" || !strings.HasPrefix(settled.Error, "abandoned: ") {
		t.Errorf("copy row %+v; want no_go abandoned", settled)
	}
}

func TestStaleClaimIsAbandonedOnTheSecondSilence(t *testing.T) {
	t0 := time.Now()
	d, p := shopWith(t, `{}`)
	queuePush(t, d, p, sha("a"), "refs/heads/main", t0)
	first := claimAt(t, d, "houston-runner-1", t0)
	if first == nil {
		t.Fatal("nothing claimed")
	}
	second := claimAt(t, d, "houston-runner-2", t0.Add(models.StaleAfter+time.Second))
	if second == nil || second.Deploy.ID != first.Deploy.ID {
		t.Fatal("expected the first stale claim to be handed to the next runner")
	}
	third := claimAt(t, d, "houston-runner-3", t0.Add(2*models.StaleAfter+2*time.Second))
	got := deployByID(t, d, first.Deploy.ID)
	if third != nil {
		t.Errorf("third claim got deploy #%d; want the row abandoned after a second silence", third.Deploy.Number)
	}
	if got.Status != "no_go" || !strings.HasPrefix(got.Error, "abandoned: ") {
		t.Errorf("after a second silence: status %s error %q; want no_go abandoned", got.Status, got.Error)
	}
}

func TestStartDeployAbandonsAStaleClaim(t *testing.T) {
	ctx, t0 := context.Background(), time.Now()
	d, p := shopWith(t, `{}`)
	queuePush(t, d, p, sha("a"), "refs/heads/main", t0)
	first := claimAt(t, d, "houston-runner-1", t0)
	if first == nil {
		t.Fatal("nothing claimed")
	}
	var started models.Started
	if err := d.Tx(ctx, func(tx *db.Tx) (err error) {
		started, err = models.StartDeploy(ctx, tx, p, sha("b"), "refs/heads/main", t0.Add(models.StaleAfter+time.Second))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got := deployByID(t, d, first.Deploy.ID)
	if got.Status != "no_go" || !strings.HasPrefix(got.Error, "abandoned: ") {
		t.Errorf("stale claimed push after a hand deploy: status %s error %q; want no_go abandoned", got.Status, got.Error)
	}
	if started.Deploy.Sha != sha("b") || started.TookOver != first.Deploy.Number {
		t.Errorf("hand deploy sha=%s took_over=%d; want %s taking over #%d", started.Deploy.Sha, started.TookOver, sha("b")[:7], first.Deploy.Number)
	}
	if _, err := models.New(d.Read).QueuedDeploy(ctx, p.ID); err != sql.ErrNoRows {
		t.Errorf("queued after StartDeploy: %v; want no queued row, so the old push cannot override the hand deploy", err)
	}
}

func TestClaimNextAbandonsAStaleHandDeploy(t *testing.T) {
	ctx, t0 := context.Background(), time.Now()
	d, p := shopWith(t, `{}`)
	var started models.Started
	if err := d.Tx(ctx, func(tx *db.Tx) (err error) {
		started, err = models.StartDeploy(ctx, tx, p, sha("a"), "refs/heads/main", t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	second := claimAt(t, d, "houston-runner-1", t0.Add(models.StaleAfter+time.Second))
	got := deployByID(t, d, started.Deploy.ID)
	if second != nil {
		t.Errorf("claim got deploy #%d runner %s; want a stale hand deploy left abandoned, not handed to a runner", second.Deploy.Number, second.Deploy.Runner)
	}
	if got.Status != "no_go" || got.Runner != "" || !strings.HasPrefix(got.Error, "abandoned: ") {
		t.Errorf("stale hand deploy: status %s runner %q error %q; want no_go abandoned, no runner", got.Status, got.Runner, got.Error)
	}
}

func addProject(t *testing.T, d *db.DB, id int64, name string) models.Project {
	t.Helper()
	if _, err := d.Write.Exec(`INSERT INTO projects (id, name, app_service, services, health, port, repo_url, deploy_rule) VALUES (?, ?, 'web', '["web"]', '/', 80, 'git@x:y.git', '{}')`, id, name); err != nil {
		t.Fatal(err)
	}
	p, err := models.New(d.Read).ProjectByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRequeueClearsTheTokenOfAWaitingClaim(t *testing.T) {
	t0 := time.Now()
	d, shop := shopWith(t, `{}`)
	blog := addProject(t, d, 2, "blog")
	queuePush(t, d, shop, sha("a"), "refs/heads/main", t0)
	queuePush(t, d, blog, sha("b"), "refs/heads/main", t0)
	shopClaim := claimAt(t, d, "houston-runner-1", t0)
	blogClaim := claimAt(t, d, "houston-runner-2", t0)
	if shopClaim == nil || blogClaim == nil {
		t.Fatal("both projects should have been claimed")
	}
	next := claimAt(t, d, "houston-runner-3", t0.Add(models.StaleAfter+time.Second))
	if next == nil || next.Deploy.ID != shopClaim.Deploy.ID {
		t.Fatalf("first stale claim should have been handed over; got %+v", next)
	}
	waiting := deployByID(t, d, blogClaim.Deploy.ID)
	if waiting.Status != "queued" || waiting.Runner != "" {
		t.Errorf("blog after the same claim: status %s runner %q; want queued with the token cleared", waiting.Status, waiting.Runner)
	}
	if waiting.OwnedBy(blogClaim.Token) {
		t.Errorf("the old blog runner's token still matches the waiting row")
	}
	handed := claimAt(t, d, "houston-runner-4", t0.Add(models.StaleAfter+2*time.Second))
	if handed == nil || handed.Deploy.ID != blogClaim.Deploy.ID || !handed.Deploy.OwnedBy(handed.Token) || handed.Deploy.OwnedBy(blogClaim.Token) {
		t.Errorf("next claim %+v; want the waiting blog push with a new token", handed)
	}
}

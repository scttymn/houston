package models_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/test"
)

func sha(c string) string { return strings.Repeat(c, 40) }

var refs = map[string]string{
	"refs/heads/main": sha("a"), "refs/heads/dev": sha("b"),
	"refs/tags/v1.9": sha("c"), "refs/tags/v1.10": sha("d"), "refs/tags/v1.10^{}": sha("e"), "refs/tags/release/2": sha("f"),
}

// The refs a rule deploys: a branch's head (main by default), or the tags
// its pattern matches (v* by default; * matches a slash, as File.fnmatch),
// an annotated tag by the commit it points at.
func TestMatching(t *testing.T) {
	for rule, want := range map[string]string{
		`{}`:                              "refs/heads/main=" + sha("a"),
		`{"on":"push","branch":"dev"}`:    "refs/heads/dev=" + sha("b"),
		`{"on":"push","branch":"gone"}`:   "",
		`{"on":"tag"}`:                    "refs/tags/v1.10=" + sha("e") + " refs/tags/v1.9=" + sha("c"),
		`{"on":"tag","tags":"release*"}`:  "refs/tags/release/2=" + sha("f"),
		`{"on":"tag","tags":"v1.[0-9]"}`:  "refs/tags/v1.9=" + sha("c"),
		`{"on":"tag","tags":"v1.[!0-9]"}`: "",
		`{"on":"tag","tags":"v1.?"}`:      "refs/tags/v1.9=" + sha("c"),
	} {
		var r models.DeployRule
		json.Unmarshal([]byte(rule), &r)
		var got []string
		for ref, s := range r.Matching(refs) {
			got = append(got, ref+"="+s)
		}
		if joined := sortJoin(got); joined != want {
			t.Errorf("%s: %s, want %s", rule, joined, want)
		}
	}
}

func sortJoin(s []string) string {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return strings.Join(s, " ")
}

func shopWith(t *testing.T, rule string) (*db.DB, models.Project) {
	t.Helper()
	d := test.DB(t)
	if _, err := d.Write.Exec(`INSERT INTO projects (id, name, app_service, services, health, port, repo_url, deploy_rule) VALUES (1, 'shop', 'web', '["web"]', '/', 80, 'git@x:y.git', ?)`, rule); err != nil {
		t.Fatal(err)
	}
	p, _ := models.New(d.Read).ProjectByID(context.Background(), 1)
	return d, p
}

func reader(found map[string]string, problem string) models.RefReader {
	return func(context.Context, models.Project) (map[string]string, string) { return found, problem }
}

// A check queues a deploy of each wanted ref that moved since the last
// one, and remembers what it saw; a later push switches the queued deploy.
func TestCheckForChanges(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	d, p := shopWith(t, `{"on":"tag"}`)
	queued, err := models.CheckForChanges(ctx, d, reader(refs, ""), p, now)
	if err != nil || len(queued) != 2 || queued[0].ID != queued[1].ID {
		t.Fatalf("= %+v, %v", queued, err) // a deploy a moved ref, the same one: the second switched it
	}
	// Both tags moved, and one deploy is queued: the later ref (by name)
	// switched it, and its log says so.
	q, _ := models.New(d.Read).QueuedDeploy(ctx, 1)
	if q.Ref != "refs/tags/v1.9" || q.Sha != sha("c") || q.Number != 1 || q.Log != "A newer push switched to ccccccc (refs/tags/v1.9).\n" {
		t.Errorf("queued %+v", q)
	}
	var seen string
	d.Read.QueryRow(`SELECT seen_refs FROM projects`).Scan(&seen)
	if !strings.Contains(seen, `"refs/tags/v1.10":"`+sha("e")+`"`) {
		t.Errorf("seen %s", seen)
	}
	again, _ := models.CheckForChanges(ctx, d, reader(refs, ""), p, now)
	if len(again) != 0 {
		t.Errorf("nothing moved, queued %d", len(again))
	}

	// A restore underway: nothing queues, and what was seen stays.
	moved := map[string]string{"refs/tags/v2": sha("9")}
	if _, err := d.Write.Exec(`INSERT INTO deploys (project_id, number, kind, status, sha, ref, heartbeat_at) VALUES (1, 9, 'restore', 'in_flight', ?, 'r', ?)`, sha("1"), now); err != nil {
		t.Fatal(err)
	}
	if q, _ := models.CheckForChanges(ctx, d, reader(moved, ""), p, now); len(q) != 0 {
		t.Error("queued past a restore")
	}
	d.Read.QueryRow(`SELECT seen_refs FROM projects`).Scan(&seen)
	if strings.Contains(seen, "v2") {
		t.Errorf("seen %s", seen)
	}

	// git's no is the project's last check error.
	d2, p2 := shopWith(t, `{}`)
	models.CheckForChanges(ctx, d2, reader(nil, "fatal: repository not found"), p2, now)
	var problem string
	d2.Read.QueryRow(`SELECT last_check_error FROM projects`).Scan(&problem)
	if problem != "fatal: repository not found" {
		t.Errorf("error %q", problem)
	}
}

// One deploy queued a project: a newer push switches it and says so; a
// rebuild asked for makes it one, and a push never undoes that.
func TestQueueDeploy(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	d, p := shopWith(t, `{}`)
	queue := func(s, ref string, fresh bool) models.Deploy {
		var got models.Deploy
		err := d.Tx(ctx, func(tx *db.Tx) (err error) {
			got, err = models.QueueDeploy(ctx, tx, p, s, ref, fresh, now)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := queue(sha("a"), "refs/heads/main", false)
	if first.Number != 1 || first.Status != "queued" || first.Log != "" || first.Fresh {
		t.Errorf("first %+v", first)
	}
	second := queue(sha("b"), "refs/heads/main", false)
	rebuilt := queue(sha("b"), "refs/heads/main", true)
	pushed := queue(sha("c"), "refs/heads/main", false)
	if second.ID != first.ID || rebuilt.ID != first.ID || !rebuilt.Fresh || !pushed.Fresh ||
		pushed.Log != "A newer push switched to bbbbbbb (refs/heads/main).\nRebuild asked for: it builds without Docker's layer cache.\nA newer push switched to ccccccc (refs/heads/main).\n" {
		t.Errorf("log %q, fresh %v", pushed.Log, pushed.Fresh)
	}
}

// Deploy now: the branch's head, or the highest tag by its numbers, moved
// or not; refused while a restore is underway or when nothing matches.
func TestQueueHead(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	d, p := shopWith(t, `{"on":"tag"}`)
	got, err := models.QueueHead(ctx, d, reader(refs, ""), p, true, now)
	if err != nil || got.Ref != "refs/tags/v1.10" || got.Sha != sha("e") || !got.Fresh {
		t.Errorf("= %+v %v", got, err)
	}
	d2, p2 := shopWith(t, `{"on":"tag","tags":"nope*"}`)
	if _, err := models.QueueHead(ctx, d2, reader(refs, ""), p2, false, now); fmt.Sprint(err) != "nothing in the repo matches the deploy rule (tags matching nope*)" {
		t.Errorf("nothing: %v", err)
	}
	d.Write.Exec(`INSERT INTO deploys (project_id, number, kind, status, sha, ref, heartbeat_at) VALUES (1, 9, 'copy', 'in_flight', ?, 'r', ?)`, sha("1"), now)
	if _, err := models.QueueHead(ctx, d, reader(refs, ""), p, false, now); fmt.Sprint(err) != "a restore of shop is queued or in flight; deploy after it" {
		t.Errorf("underway: %v", err)
	}
}

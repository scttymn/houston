package models_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/test"
)

func save(t *testing.T, d *db.DB, fields string) models.Project {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fields), &raw); err != nil {
		t.Fatal(err)
	}
	s, errs := models.ParseSync(raw)
	if errs != nil {
		t.Fatal(errs)
	}
	var p models.Project
	err := d.Tx(context.Background(), func(tx *db.Tx) (err error) {
		p, _, err = s.Save(context.Background(), tx, time.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const shop = `{"name":"shop","app_service":"web","services":["web","db"],"domains":[],"variables":[],"health":"/up","port":3000}`

// Catching up to a restore that switched silently applies the compose.yml
// it kept: the project's config, and the new generation's container names.
// One that can't be applied is the adopt error, and the move forward stays.
func TestCatchUp(t *testing.T) {
	ctx := context.Background()
	d := test.DB(t)
	p := save(t, d, shop)
	kept := strings.Replace(strings.Replace(shop, `"port":3000`, `"port":4000`, 1), `["web","db"]`, `["web","db","cache"]`, 1)
	d.Write.Exec(`INSERT INTO deploys (project_id, number, kind, status, generation, sha, ref, heartbeat_at, sync_payload)
		VALUES (?, 2, 'restore', 'no_go', 2, 'abc', 'refs/restore/x', ?, ?)`, p.ID, time.Now(), kept)

	adopted, adoptErr, err := models.CatchUp(ctx, d, p, 2, "abc", time.Now())
	if err != nil || adoptErr != nil || adopted == nil || adopted.Project.Port != 4000 {
		t.Fatalf("= %+v, %v, %v", adopted, adoptErr, err)
	}
	var port, generation int
	d.Read.QueryRow(`SELECT port, data_generation FROM projects WHERE id = ?`, p.ID).Scan(&port, &generation)
	var names string
	d.Read.QueryRow(`SELECT group_concat(name, ' ') FROM (SELECT name FROM project_hosts WHERE project_id = ? ORDER BY name)`, p.ID).Scan(&names)
	if port != 4000 || generation != 2 || names != "shop shop-cache-g2 shop-db-g2" {
		t.Errorf("port %d, generation %d, names %q", port, generation, names)
	}

	// Already there: nothing moves, nothing's applied again.
	if adopted, _, err := models.CatchUp(ctx, d, p, 2, "abc", time.Now()); adopted != nil || err != nil {
		t.Errorf("again: %+v %v", adopted, err)
	}

	// A kept compose.yml another project's names clash with: not applied,
	// the generation still moves.
	other := save(t, d, strings.Replace(strings.Replace(shop, `"shop"`, `"other"`, 1), `["web","db"]`, `["web"]`, 1))
	d.Write.Exec(`INSERT INTO project_hosts (project_id, name) VALUES (?, 'shop-db-g3')`, other.ID)
	d.Write.Exec(`INSERT INTO deploys (project_id, number, kind, status, generation, sha, ref, heartbeat_at, sync_payload)
		VALUES (?, 3, 'restore', 'no_go', 3, 'def', 'refs/restore/y', ?, ?)`, p.ID, time.Now(), kept)
	adopted, adoptErr, err = models.CatchUp(ctx, d, p, 3, "def", time.Now())
	d.Read.QueryRow(`SELECT data_generation FROM projects WHERE id = ?`, p.ID).Scan(&generation)
	if err != nil || adopted != nil || adoptErr == nil || !strings.Contains(adoptErr.Error(), "shop-db-g3 belongs to project other") || generation != 3 {
		t.Errorf("a clash: %+v, %v, %v, generation %d", adopted, adoptErr, err, generation)
	}
	d.Read.QueryRow(`SELECT group_concat(name, ' ') FROM (SELECT name FROM project_hosts WHERE project_id = ? ORDER BY name)`, p.ID).Scan(&names)
	if names != "shop shop-cache-g2 shop-db-g2" {
		t.Errorf("the failed adopt left names %q", names)
	}
}

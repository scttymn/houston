package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/volumes"
)

const (
	// fillVolume empties a volume, copies the snapshot's files back, then
	// puts each SQLite copy at its path with no stale -wal/-shm/-journal.
	// Arguments: the volume, then fours of (staged file, path in the volume,
	// owner or -, mode or -).
	fillVolume = `vol="$1"; shift; find /v -mindepth 1 -delete && if [ -d "/restore/data/$vol" ]; then cp -a "/restore/data/$vol/." /v/; fi && ` +
		`while [ "$#" -gt 3 ]; do mkdir -p "$(dirname "/v/$2")" && cp "/restore/out/$1" "/v/$2" && rm -f "/v/$2-wal" "/v/$2-shm" "/v/$2-journal" && ` +
		`if [ "$3" != "-" ]; then chown "$3" "/v/$2"; else chown --reference="$(dirname "/v/$2")" "/v/$2"; fi && ` +
		`if [ "$4" != "-" ]; then chmod "$4" "/v/$2"; fi || exit 1; shift 4; done`
	pgReady = `pg_isready -U "${POSTGRES_USER:-postgres}" -d postgres`
	// Roles that already exist are fine: psql carries on past their errors.
	pgRoles = `psql -U "${POSTGRES_USER:-postgres}" -d postgres -q`
	// The snapshot's roles carry the superuser's old password; the
	// container's own POSTGRES_PASSWORD (the secret the app uses now) wins.
	pgKeepPassword = `printf '%s\n' "ALTER ROLE CURRENT_USER WITH PASSWORD :'pw';" | psql -U "${POSTGRES_USER:-postgres}" -d postgres -q -v ON_ERROR_STOP=1 -v pw="$POSTGRES_PASSWORD"`
	pgRecreate     = `dropdb -U "${POSTGRES_USER:-postgres}" --force --if-exists --maintenance-db=template1 -- "$1" && ` +
		`createdb -U "${POSTGRES_USER:-postgres}" --maintenance-db=template1 -- "$1"`
	pgRestore  = `pg_restore -U "${POSTGRES_USER:-postgres}" --no-owner --role="${POSTGRES_USER:-postgres}" -d "$1"`
	readyTries = 60
)

// stagedFile is a file a manifest may name: only what a backup writes.
var stagedFile = regexp.MustCompile(`^(postgres/[A-Za-z0-9][A-Za-z0-9_.-]*/(\d+\.dump|globals\.sql)|sqlite/\d+\.sqlite3)$`)

// manifest is a snapshot's houston.json.
type manifest struct {
	Project  string          `json:"project"`
	Sha      string          `json:"sha"`
	Volumes  []models.Volume `json:"volumes"`
	Postgres []dumped        `json:"postgres"`
	SQLite   []struct {
		Volume string          `json:"volume"`
		Path   json.RawMessage `json:"path"`
		File   json.RawMessage `json:"file"`
		UID    json.RawMessage `json:"uid"`
		GID    json.RawMessage `json:"gid"`
		Mode   json.RawMessage `json:"mode"`
	} `json:"sqlite"`
}

// restore puts a restore deploy's snapshot back into the generation it
// builds, beside the serving one: the snapshot into a staging volume, its
// manifest checked before anything is touched, then each volume emptied
// and refilled, SQLite copies in place, and each Postgres database
// recreated and restored into that generation's Postgres (the runner has
// booted it first).
func (w *run) restore(ctx context.Context) error {
	return w.steps(ctx, "restore", func(ctx context.Context) (*result, error) {
		d, err := models.New(w.DB.Read).DeployByNumber(ctx, models.DeployByNumberParams{ProjectID: w.project.ID, Number: w.run.DeployNumber.Int64})
		if err != nil {
			return nil, err
		}
		target := models.Generation{Project: w.project.Name, Number: d.Generation}
		// Defense in depth: the serving generation is never emptied or
		// dropped, whatever the restore's deploy row says.
		// A copy's new project serves nothing yet.
		if target.Number == w.project.DataGeneration && !(d.Kind == "copy" && !w.served(ctx)) {
			return nil, failed(fmt.Sprintf("refusing to restore into generation %d: generation %d is the one serving", target.Number, target.Number))
		}
		// The restore's own compose.yml's volumes, as its check sync kept
		// them: the snapshot's commit may name them differently.
		var kept struct {
			Volumes *[]models.Volume `json:"volumes"`
		}
		if d.SyncPayload.Valid {
			json.Unmarshal([]byte(d.SyncPayload.String), &kept)
		}
		if kept.Volumes == nil {
			return nil, failed("the restore's compose.yml wasn't checked (its runner is older than this Mission Control)")
		}
		place := volumes.Placement{DB: w.DB, Docker: w.Docker, Tools: w.Tools}
		var refused models.Refused
		if err := place.Place(ctx, w.project, target.Number, *kept.Volumes); errors.As(err, &refused) {
			return nil, failed(fmt.Sprintf("couldn't make generation %d's volumes: %s", target.Number, refused.Msg))
		} else if err != nil {
			return nil, err
		}
		if err := w.prepare(ctx); err != nil {
			return nil, err
		}
		env := ResticEnv(w.location)
		ran, err := w.docker(ctx, ResticArgs(w.location, env, []string{"restore", w.run.SourceSnapshotID, "--target", "/restore"}, w.container("restic"),
			[]string{w.staging() + ":/restore"}), env, nil)
		if err != nil {
			return nil, err
		}
		if !ran.OK {
			return nil, failed(fmt.Sprintf("restic restore failed (exit %d): %s", ran.Code, tail(ran.Output)))
		}
		m, err := w.readManifest(ctx, d, *kept.Volumes)
		if err != nil {
			return nil, err
		}
		for _, v := range m.Volumes {
			if err := w.fill(ctx, target, v.Name, m); err != nil {
				return nil, err
			}
		}
		for _, pg := range m.Postgres {
			if err := w.restorePostgres(ctx, target, pg); err != nil {
				return nil, err
			}
		}
		found := map[string]any{"volumes": []string{}, "sqlite": []map[string]string{}, "databases": []map[string]string{}}
		for _, v := range m.Volumes {
			found["volumes"] = append(found["volumes"].([]string), v.Name)
		}
		for _, s := range m.SQLite {
			var path string
			json.Unmarshal(s.Path, &path)
			found["sqlite"] = append(found["sqlite"].([]map[string]string), map[string]string{"volume": s.Volume, "path": path})
		}
		for _, pg := range m.Postgres {
			for _, db := range pg.Databases {
				found["databases"] = append(found["databases"].([]map[string]string), map[string]string{"service": pg.Service, "name": db.Name})
			}
		}
		return &result{found: found}, nil
	})
}

// readManifest is the snapshot's manifest, checked before anything is
// touched: Houston's, of this project at the restore's commit, naming only
// files a backup writes, and SQLite paths inside their volumes, and volumes
// the restore's compose.yml has.
func (w *run) readManifest(ctx context.Context, d models.Deploy, vols []models.Volume) (manifest, error) {
	var m manifest
	read, err := w.docker(ctx, []string{"run", "--rm", "--name", w.container("read"), "--user", "0", "-v", w.staging() + ":/restore:ro",
		"--entrypoint", "cat", w.Tools, "/restore/out/houston.json"}, nil, nil)
	if err != nil {
		return m, err
	}
	if !read.OK {
		return m, failed("couldn't read the snapshot's manifest: " + tail(read.Output))
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal([]byte(read.Output), &shape) != nil {
		return m, failed("the snapshot's manifest isn't JSON")
	}
	for _, k := range []string{"volumes", "postgres", "sqlite"} {
		if raw := shape[k]; len(raw) == 0 || raw[0] != '[' {
			return m, failed("the snapshot's manifest isn't Houston's")
		}
	}
	if json.Unmarshal([]byte(read.Output), &m) != nil {
		return m, failed("the snapshot's manifest isn't Houston's")
	}
	// A copy's snapshot is of the old project, at the commit it served.
	of, sha, whose := w.project.Name, d.Sha, "restore's"
	if d.Kind == "copy" {
		q := models.New(w.DB.Read)
		if c, err := q.CopyByDeploy(ctx, sql.NullInt64{Int64: d.ID, Valid: true}); err == nil {
			of, sha, whose = c.FromName, "", "snapshot's"
			if snapshot, err := q.BackupRunByID(ctx, c.SnapshotRunID.Int64); err == nil {
				sha = snapshot.Sha
			}
		}
	}
	if m.Project != of {
		return m, failed(fmt.Sprintf("the snapshot is of %s, not %s", m.Project, of))
	}
	if m.Sha != sha {
		return m, failed(fmt.Sprintf("the snapshot was taken at %s, not the %s %s", short(m.Sha), whose, short(sha)))
	}
	var files []json.RawMessage
	for _, pg := range m.Postgres {
		globals, _ := json.Marshal(pg.Globals)
		files = append(files, globals)
		for _, db := range pg.Databases {
			file, _ := json.Marshal(db.File)
			files = append(files, file)
		}
	}
	for _, s := range m.SQLite {
		files = append(files, s.File)
	}
	for _, raw := range files {
		var f string
		if json.Unmarshal(raw, &f) != nil || !stagedFile.MatchString(f) {
			return m, failed(fmt.Sprintf("the manifest names %s, not one Houston writes", raw))
		}
	}
	for _, s := range m.SQLite {
		var path string
		if json.Unmarshal(s.Path, &path) != nil || strings.HasPrefix(path, "/") || slices.Contains(strings.Split(path, "/"), "..") {
			return m, failed(fmt.Sprintf("the SQLite path %s leaves its volume", s.Path))
		}
	}
	for _, v := range m.Volumes {
		if !slices.ContainsFunc(vols, func(k models.Volume) bool { return k.Name == v.Name }) {
			return m, failed(fmt.Sprintf("%s has no volume %s (the snapshot's compose.yml differs)", w.project.Name, v.Name))
		}
	}
	return m, nil
}

// served is whether the project has served (a copy's new project hasn't,
// until its first GO); an error counts as served.
func (w *run) served(ctx context.Context) bool {
	served, err := models.New(w.DB.Read).ProjectHasServed(ctx, w.project.ID)
	return served || err != nil
}

func short(sha string) string { return sha[:min(7, len(sha))] }

func (w *run) fill(ctx context.Context, target models.Generation, volume string, m manifest) error {
	args := []string{"run", "--rm", "--name", w.container("fill"), "--user", "0", "-v", target.Volume(volume) + ":/v", "-v", w.staging() + ":/restore:ro",
		"--entrypoint", "sh", w.Tools, "-c", fillVolume, "sh", volume}
	for _, s := range m.SQLite {
		if s.Volume != volume {
			continue
		}
		var file, path string
		json.Unmarshal(s.File, &file)
		json.Unmarshal(s.Path, &path)
		args = append(args, file, path, owner(s.UID, s.GID), mode(s.Mode))
	}
	filled, err := w.docker(ctx, args, nil, nil)
	if err != nil {
		return err
	}
	if !filled.OK {
		return failed(fmt.Sprintf("couldn't restore the volume %s: %s", volume, tail(filled.Output)))
	}
	return nil
}

// id is a manifest's uid or gid, or -1 when it isn't one.
func id(raw json.RawMessage) int64 {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < 0 || n > 1<<31-1 {
		return -1
	}
	return n
}

// owner is a database's owner as the manifest recorded it, or "-" (an
// older snapshot, or anything but numbers): then the fill gives it its
// folder's owner.
func owner(uid, gid json.RawMessage) string {
	u, g := id(uid), id(gid)
	if u < 0 || g < 0 {
		return "-"
	}
	return fmt.Sprintf("%d:%d", u, g)
}

// mode is its permissions in octal, or "-" (then cp's).
func mode(raw json.RawMessage) string {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < 0 || n > 0o7777 {
		return "-"
	}
	return strconv.FormatInt(n, 8)
}

// feed is a staged file streamed out: a pipe's first half.
func (w *run) feed(file string) []string {
	return []string{"run", "--rm", "--name", w.container("feed"), "--user", "0", "-v", w.staging() + ":/restore:ro", "--entrypoint", "cat", w.Tools, "/restore/out/" + file}
}

func (w *run) restorePostgres(ctx context.Context, target models.Generation, pg dumped) error {
	container := target.Container(pg.Service)
	ready := false
	for i := 0; i < readyTries && !ready; i++ {
		if i > 0 {
			select {
			case <-time.After(w.ReadyEvery):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		r, err := w.docker(ctx, []string{"exec", container, "sh", "-c", pgReady}, nil, nil)
		if err != nil {
			return err
		}
		ready = r.OK
	}
	if !ready {
		return failed(fmt.Sprintf("%s's Postgres (%s) isn't ready", pg.Service, container))
	}
	roles, err := w.pipe(ctx, w.feed(pg.Globals), []string{"exec", "-i", container, "sh", "-c", pgRoles})
	if err != nil {
		return err
	}
	if !roles.OK {
		return failed(fmt.Sprintf("restoring %s's roles failed: %s", pg.Service, tail(roles.Output)))
	}
	kept, err := w.docker(ctx, []string{"exec", container, "sh", "-c", pgKeepPassword}, nil, nil)
	if err != nil {
		return err
	}
	if !kept.OK {
		return failed(fmt.Sprintf("setting %s's password back failed: %s", pg.Service, tail(kept.Output)))
	}
	for _, db := range pg.Databases {
		recreated, err := w.docker(ctx, []string{"exec", container, "sh", "-c", pgRecreate, "sh", db.Name}, nil, nil)
		if err != nil {
			return err
		}
		if !recreated.OK {
			return failed(fmt.Sprintf("recreating %s's database %q failed: %s", pg.Service, db.Name, tail(recreated.Output)))
		}
		restored, err := w.pipe(ctx, w.feed(db.File), []string{"exec", "-i", container, "sh", "-c", pgRestore, "sh", conninfo([]byte(db.Name))})
		if err != nil {
			return err
		}
		if !restored.OK {
			return failed(fmt.Sprintf("pg_restore of %s's database %q failed: %s", pg.Service, db.Name, tail(restored.Output)))
		}
	}
	return nil
}

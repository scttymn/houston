# Plan: Restore (build step 6)

Spec: https://claude.ai/artifact/R3d4fzN1u5QUP4kT88mqug (§9 Restore; §10 `POST /api/projects/:name/restore`, the Restore page; §14 "maintenance mode across stop → restore → boot; image retention and rollback after pruning").
Design: https://claude.ai/artifact/UswdZ62N4ygiKdGh1vDEPH (Restore: "Roll equip back to 18 Sep 03:00?", code now → after, data live → the snapshot, the two phases, type the name to confirm, `hou restore <id> --server`).
Builds on build step 5 (docs/plans/volumes-backups.md): each snapshot holds the app's volumes, `sqlite/<n>.sqlite3` copies, Postgres dumps and roles, and `houston.json` saying what came from where.

## Goal

Roll a project back to a snapshot, code and data together. The old code is made ready while the app keeps serving. The only downtime is a maintenance page while the data is put back. A safety snapshot is taken first, so the restore can itself be undone.

## Scope

- **A restore is a deploy of kind `restore`,** numbered with the project's deploys and shown in its history ("Report to deploy history as a restore"). It's queued, claimed by a runner, owned by its token, heartbeating, and finished GO or NO-GO, exactly like a deploy. It also has one in flight per project, so a restore and a deploy never overlap.
- **The runner does the code and Kamal; Mission Control does the data.** Restic credentials never leave Mission Control, as in step 5. The runner asks for the safety snapshot and the data restore through its API, and waits.
- **Steps** (spec §9, the design's two phases):
  - While the app keeps serving:
    1. **Prepare:** fetch the snapshot's commit; sync its compose.yml (a HOLD stops here, nothing touched); pull its image from Houston's registry, or build and push it if it's gone
    2. **Stage:** pull it onto the host
  - Maintenance page up:
    3. **Maintenance:** `kamal app maintenance`, then `kamal app stop`
    4. **Safety snapshot:** kind deploy, reason restore
    5. **Accessories:** booted or rebooted to the old compose.yml's config
    6. **Restore data:** volumes emptied and refilled; SQLite copies put in place with any `-wal`/`-shm` removed; Postgres roles applied, then each database dropped, recreated and `pg_restore`d; services that aren't backed up start empty
    7. **Start:** `kamal deploy --skip-push --version <old sha>`. The health check passes before traffic switches, and there's no release hook: the data already matches the code.
    8. **Live:** `kamal app live`
- **Boundary:** a restore always restores the whole project (code and data). A data-only or single-volume restore is "later".

## Batches

1. **The data engine, and the spike** (below):
   - Mission Control's `RestoreData`, run from a runner's request, against real restic output shapes.
   - The Kamal and git spike: maintenance across stop, restore and boot; an image pulled back from the registry after the host's copy is gone; an old commit fetched by SHA from Forgejo.
2. **Asking for a restore:**
   - the restore kind on deploys, and the snapshot it restores
   - the page (a Restore button on each snapshot, and the confirm page from the design), `/api/v1`, and `houston restore <snapshot> --confirm <name> [--follow]`
   - the preconditions: a linked repo, the snapshot found in a location the project's backups used (the snapshot list now reads all of them), nothing queued or in flight
   - while a restore is queued or in flight: pushes don't queue deploys (the next check does), and scheduled or manual backups wait
3. **The runner's restore:**
   - the claim carries the kind and snapshot; the old commit is fetched (by SHA, or the branch if the host refuses)
   - the steps above in Go, and the deploy page's restore steps
   - every failure path: before maintenance, nothing changed; after it, see Decision 1
4. **The real run:** on OrbStack, restore the spike (Postgres, NFS volume, SQLite) and equip.
   - The data is rolled back (rows, files, SQLite), and the old SHA serves.
   - The maintenance page answers during the data phase.
   - The safety snapshot exists, and restoring it goes back.
   - A restore whose image was pruned rebuilds it.

## Batch 1: The data engine, and the spike

### Spike first (spec §14): `install/test/restore-spike.sh`
On an OrbStack machine with Houston installed, using the spike fixture deployed twice (two SHAs):
- `kamal app maintenance` → kamal-proxy answers 503 with its maintenance page. `kamal app stop` → still 503, not a proxy error.
- `kamal deploy --skip-push --version <first sha>` while in maintenance → what the proxy answers after the health check (maintenance or the app). Then `kamal app live` → 200 from the old version.
- The image: `docker rmi` the host's copy of the first SHA; `docker pull 127.0.0.1:5000/spike:<sha>` gets it back. Whether `kamal deploy --skip-push` pulls it by itself.
- Forgejo: `git fetch --depth 1 <old, non-tip sha>` works, or is refused (then fall back to the branch without depth).

The notes go into this file before Batch 1's code.

### Design (short)
- **`BackupRun` gains `operation`** (`backup` | `restore`) and `source_snapshot_id`. A data restore reuses the run machinery from step 5: claim with a token, heartbeat, the 3-hour deadline, conditional finish, one running per project (so a restore never overlaps a backup of the same project), and `BackupJob` (its queue: `snapshots`, since a runner waits on it). `BackupJob` runs `RestoreData` for `operation: restore`.
- **Runner API** (the restore deploy's token, in flight, kind restore): `POST /api/deploys/:id/restore_data` → 202 with the run (one per restore deploy: a retried POST gets the same run); `GET` → the run.
  - Until Batch 2 adds the kind, the endpoint is tested against a deploy row that has the restore fields; the request path arrives with Batch 2.
- **`RestoreData`** (docker commands, secrets only in the docker process's environment; exact container names `houston-restore.<name>.<role>`, like step 5's):
  1. **Clean up** leftovers; recreate the staging volume `houston-restore.<name>`.
  2. `restic restore <snapshot> --host houston --target /restore` into staging, with the location's repository and the cache volume.
  3. **Read `houston.json` from staging** (a helper `cat`), and check it: its project is this project; every file it names is under `out/`.
     - A volume it names that the project no longer has → refused, nothing touched. (Batch 3's runner syncs the snapshot's own compose.yml first, so its volumes match.)
  4. **For each volume in the manifest:** a helper (root) mounts the volume and staging.
     - `find /v -mindepth 1 -delete`, then `cp -a /restore/data/<volume>/. /v/`
     - each SQLite copy for that volume goes to `/v/<path>`, and `/v/<path>-wal`, `-shm` and `-journal` are removed
     - Paths come from the manifest as argv, never inside a script.
  5. **For each Postgres service in the manifest:**
     - `psql -f` the roles file, with role-exists errors ignored (`ON_ERROR_STOP` off for roles only)
     - then for each database: `dropdb --force --if-exists --maintenance-db=template1 -- <name>`, then `createdb --maintenance-db=template1 -- <name>`
     - then the dump piped into `pg_restore --no-owner --role=<user> -d "dbname='<escaped>'"`
     - Any failure stops, with the service and database named.
  6. Remove the staging volume (always). Finish GO with what was restored (`found`), or NO-GO with the step and the output's tail.
- **Nothing here runs unless the restore deploy is in flight and owned.** The runner has already put the maintenance page up and stopped the app (Batch 3), so nothing writes while the data is replaced.

### Contract pin
- `POST /api/deploys/:id/restore_data` with `X-Houston-Deploy-Token`: the deploy is in flight, kind restore, with a source snapshot and location → 202 with the run. A wrong token → 403; not in flight → 409; not a restore → 422. Personal tokens → 401. Through Cloudflare → 404.
- The manifest drives everything. A manifest that's missing, isn't JSON, names another project, or names a file outside `out/` → NO-GO before anything is touched.

### Templates filled
**At-least-once:** the same as step 5's runs.
- One run per restore deploy (unique index on the source deploy number, operation restore).
- A duplicate job delivery finds the run claimed: `restic restore` runs ≤ 1 time.
- A stale run is abandoned after 2 minutes; the next claim cleans the leftovers.

**Crash gap:**
- The data restore is not atomic: a crash mid-way leaves volumes or databases partly restored.
- The runner's restore stays NO-GO, with the maintenance page up (Decision 1).
- The repair is a restore of the safety snapshot (taken before any data was touched), or of the same snapshot again: every step empties or drops before it fills, so a rerun converges.

### AC ↔ test map (Batch 1)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The engine's commands in order: cleanup, staging, restic restore (env-only secrets, cache, repository), the manifest read, per volume empty + copy, SQLite into place with -wal/-shm/-journal removed, Postgres roles then drop/create/pg_restore per database (hostile name as argv and conninfo), staging removed; GO with what was restored | `models/restore_data_test.rb` `test "a restore, step by step"` | Contract |
| 2 | A bad manifest (missing, not JSON, another project, a path outside out/) → NO-GO, and no volume or database touched | `test "the manifest is checked before anything is touched"` | Contract (hostile input) |
| 3 | A failing step (restic, a copy, pg_restore) → NO-GO naming it; staging removed | `test "a failing step stops the restore and cleans up"` | Crash & repair, Signals |
| 4 | A volume the manifest names that the project doesn't have → refused before anything is touched | `test "volumes must match"` | Preconditions |
| 5 | The runner API: 202 and one run per restore deploy (a retry gets it); the checks (403, 409, 422, 401, through Cloudflare 404) | `integration/api_restore_data_test.rb` | Authz, At-least-once |
| 6 | A duplicate job delivery → `restic restore` once; a restore run and a backup of the same project never run together | `jobs/backup_job_test.rb` | At-least-once, Concurrency |
| 7 | The migration: `operation` default backup (every step-5 run stays a backup), `source_snapshot_id`, the unique index | `models/backup_run_test.rb` | Migrate |

## Decisions (yours)
1. **When a restore fails after the maintenance page is up** (the data may be half restored), I'd **leave the maintenance page up** and finish NO-GO: "restore failed at <step>: …; the maintenance page stays up. To go back to how it was, restore the safety snapshot <id>." The alternative is restoring the safety snapshot and restarting the current version automatically, but that's a second restore that can fail the same way, and then the state is harder to explain. Leave it up, or roll back automatically?
2. **A restore needs a linked repo:** the runner fetches the snapshot's commit to build or run it. A project that was only ever deployed by hand (`houston deploy` on the server) has to be linked first (`houston link`), and the Restore button says so. OK?

# Plan: Volumes and backups (build step 5)

Spec: https://claude.ai/artifact/R3d4fzN1u5QUP4kT88mqug (§9 volumes, backups, restore; §7 deploy step 3, the pre-deploy snapshot; §10 API table and pages; §12 build order 5; §14 "restic tag filtering in forget; --keep-daily / --keep-last").
Design: https://claude.ai/artifact/UswdZ62N4ygiKdGh1vDEPH (ProjectDetail: Back up now, the Snapshots panel with Scheduled / Pre-deploy tabs, the Backup plan panel; DeployLog: step 03 "Pre-deploy snapshot"; Settings: storage).
Standing decision (build step 4): every Mission Control action gets a CLI path.

## Goal

Every deployed project's data is backed up to restic, as snapshots of code and data together: on a schedule, by Back up now (page or CLI), and right before every deploy. Old snapshots are pruned by the rules in `compose.yml`. Where each named volume lives (local disk, NFS, a host path) and where backups go are chosen in Mission Control, never in the repo. Restore is build step 6; this step makes sure everything it needs is in the snapshot.

## Scope

- **What a backup holds (spec §9, derived, nothing to list):**
  - the app's named volumes, as files
  - every SQLite database found in them (by the file header), copied with `sqlite3 .backup`; the live file and its `-wal`/`-shm` are left out of the file copy
  - every database of each Postgres service (its image name ends in `postgres`), dumped with `pg_dump -Fc`, plus `pg_dumpall --globals-only` (roles)
  - a manifest (`houston.json`) saying what came from where, for restore
  - Other services (Redis, …) are listed as "not backed up".
- **One restic snapshot per backup,** tagged `project:<name>`, `sha:<running sha>`, `kind:auto|deploy`, `reason:schedule|manual|deploy|restore`, and `deploy:<n>` on pre-deploy snapshots. The snapshot list is read from restic; Houston keeps a table of backup *runs* (queued, running, result, errors), not of snapshots.
- **Mission Control runs backups,** on its own Solid Queue queue (one at a time), with Docker: restic credentials and passwords never leave it. A runner asks Mission Control for the pre-deploy snapshot and waits for it.
- **Defer sftp** again, to "later" in the build order (it needs an SSH key managed for it; your NFS and B2 are covered). See Decisions.
- **Boundary:** restore is build step 6. Moving an existing volume to another location is spec §12 "later".

## Batches

1. **Back up now, end to end** (below): the backup engine, backup runs, and the project page's Back up now button. Local-disk volumes, the default storage location.
2. **Snapshots, retention, and the CLI:**
   - `forget` after each backup (`--keep-daily <auto>` / `--keep-last <deploy>`), `prune` once a day per location
   - the Snapshots panel (Scheduled / Pre-deploy, with sizes) and the Backup plan panel
   - `/api/v1` snapshots and backups; `houston snapshots --server`, `houston backup --server [--follow]`; last backup in `houston status`
3. **The schedule:** `backups.schedule` (daily HH:MM, in Houston's time zone, UTC by default; Decision 4) through sync; a recurring tick; once per day per project, caught up after downtime; NEXT BACKUP on the flight board; a failed backup shows as NO-GO.
4. **The pre-deploy snapshot:** the runner API (one per deploy, idempotent), deploy step "Snapshot" just before Release (Decision 2) in Go and on the deploy page, skipped with no data, its failure stops the deploy (Decision 1).
5. **Volume placement:** a location per named volume (Add project, the project page, `houston volumes --server`); sync creates new volumes there (NFS subdirectory, host path); a volume that exists elsewhere is a NO-GO, not a silent move; the SQLite-on-NFS warning; Postgres data stays on local disk.
6. **Storage locations in Settings and per-project backup targets:** list with capabilities (live volumes / backups), add (the first-run form, reused), make default, the password shown once; the project's backup target; `houston storage list|add` (secrets from stdin).
7. **The real run:** on OrbStack, the equip copy (SQLite in WAL mode) and the spike (Postgres), with an NFS server container as a location and a host path as another: Back up now, the schedule, a pre-deploy snapshot, retention, and the snapshot's contents restored by hand and opened (`sqlite3`, `pg_restore --list`).

## Batch 1: Back up now, end to end

### Spike first (spec §14)
`install/test/restic-spike.sh`: the pinned `restic/restic:0.19.1` image against a local repository, no VM. It pins what the design leans on:
- `backup --host houston --json`: the summary line's `snapshot_id` and `total_bytes_processed`
- `--tag a --tag b` on backup; `snapshots --tag project:x,kind:auto --json` is AND
- `forget --host houston --tag project:x,kind:deploy --group-by '' --keep-last 2`: keeps 2 across different paths and SHAs (the default grouping by host and paths would keep every snapshot)
- `--keep-daily` counts days, not snapshots
- `--exclude-file` with an escaped name containing `*`, `[`, `?` and `\` excludes only that file
- exit code 3 (some files unreadable) vs 0 vs 1

**Spike notes** (`install/test/restic-spike.sh`, RESTIC SPIKE PASS):
- `backup --json`'s last line is `{"message_type":"summary", …}` with a 64-hex `snapshot_id` and `total_bytes_processed`.
- `--tag project:x,kind:deploy` is AND; repeated `--tag` flags are OR. Houston always filters with the comma form.
- **Default `forget` grouping (host, paths) removed nothing** when the backed-up paths differ between snapshots, which happens whenever a volume or a database is added. So `--host houston` on every backup and `--group-by ''` on every forget are required, not nice to have.
- `--keep-daily 2` over four snapshots on three days kept the newest of each of the last two days. `--keep-last 2` kept the two newest.
- `--exclude-file` patterns escaped with `\` before `*`, `?`, `[` and `\` excluded exactly the literal names and left the look-alikes (`weXird1Y.db`, `backXslash`).
- **Exit codes:** 0 clean; **3 when some files couldn't be read** (the snapshot is still saved); 12 wrong password; 1 other failures (a missing path).
- **restic needs a writable cache directory** (it crashed without one as another user). A throwaway container also starts with an empty cache every time, so every run re-reads the repository's index (slow on B2).

**What they change in the design:**
- A persistent volume `houston-restic-cache` is mounted at `/root/.cache/restic` for every restic run: backups, forget, and the setup check.
- **Exit 3 is GO with a warning,** not NO-GO: "GO, 2 files couldn't be read: …" (restic's lines, capped at 20), and the snapshot is kept. Files that vanish mid-backup (temp files, caches) cause it on busy apps, and a NO-GO every night would teach people to ignore it. It's still shown on the run, the page, and the CLI.

### Design (short)
- **Sync learns the data** (Go `mission.RequestFor`, Rails `ProjectSync`):
  - `volumes`: the app service's named volumes, `[{name, path}]`
  - `databases`: services whose image repository (no tag or digest) ends in `postgres`, `[{service, image}]`
  - They're stored on the project, like `services`.
- **`BackupRun`** (table `backup_runs`):
  - `project_id`, `kind` (auto/deploy), `reason` (schedule/manual/deploy/restore), `deploy_number`
  - `status`: queued / running / go / no_go / skipped
  - `sha`, `location_id`, `token_digest`, `heartbeat_at`, `started_at`, `finished_at`
  - `snapshot_id`, `bytes`, `found` (JSON: the SQLite files and databases), `error`, `log` (capped at 1 MiB)
  - Partial unique indexes: one running per project; one queued manual per project.
- **`BackupRun.request!(project, reason: "manual")`:**
  - Refused if nothing is deployed ("nothing deployed yet"), or if there is no acknowledged default location.
  - A queued manual run is returned as-is (a double click queues one).
  - It enqueues `BackupJob` on the `backups` queue.
- **`BackupJob`** claims with a conditional flip, queued → running, with a token, only if no live running run exists for the project.
  - A running run silent for 2 minutes is abandoned first: NO-GO, "Mission Control stopped during the backup".
  - A job that loses the claim retries in 30 s. A second delivery of the same job finds the run already running and does nothing.
- **`Backup`** (the engine, one run; each step is a `DockerCommand`, secrets only in the docker process's environment):
  1. **Clean up:** remove leftover containers `houston-backup-<name>-*`; recreate the staging volume `houston-backup-<name>`.
  2. **Postgres:** for each database service and each non-template database, `docker exec <name>-<service> sh -c 'pg_dump -U "${POSTGRES_USER:-postgres}" -Fc -d "$1"' sh <db>`, piped into a helper container that writes `/out/postgres/<service>/<n>.dump`. Then `pg_dumpall --globals-only` the same way. Database names travel as argv, never inside the script, and the file names are Houston's numbers.
  3. **SQLite:** a helper container (Mission Control's own image, `HOUSTON_TOOLS_IMAGE`, which has `sqlite3`) mounts each app volume at `/data/<volume>` and runs `lib/backup/sqlite.sh`. It finds files starting with `SQLite format 3\0` and `.backup`s each to `/out/sqlite/<n>.sqlite3`. It writes the manifest and the exclude file, with each live file, its `-wal` and `-shm` escaped as literal patterns.
  4. **restic:** `restic backup --host houston --json` (with the `houston-restic-cache` volume) of `/data` (app volumes read-only) and `/out`, with `--exclude-file`, the tags, the default location's repository and mounts. The summary gives `snapshot_id` and bytes.
  5. **Finish and clean up:** remove the staging volume (always, on failure too). Finalize GO with the snapshot, or NO-GO with the step and the last lines of output.
  - With no volumes and no databases, the run is **skipped** ("nothing to back up") and runs no docker commands.
- **Liveness:**
  - The job heartbeats every 15 s while it runs.
  - The whole backup has a 3-hour deadline. On timeout it removes its containers (killing the docker CLI leaves them running) and finishes NO-GO "took longer than 3 hours".
  - Finalize is conditional (`WHERE status = running AND token_digest = mine`). A job that lost its run (abandoned, taken over) writes nothing. The snapshot it may have made is still a valid snapshot, and restic lists it.
- **Queue:** `config/queue.yml` gets two workers: `default` (3 threads, as now) and `backups` (1 thread). A long backup never holds up a webhook's change check.
- **Installer:** `HOUSTON_TOOLS_IMAGE: $IMAGE` in Mission Control's environment.
- **Back up now** on the project page:
  - `POST /projects/:name/backups` (admin), then back to the project with "Backing up…" or the last run's result (GO with the time and size, NO-GO with the error, or "nothing to back up").
  - Hidden when there's no acknowledged default location.

### Contract pin
- **Sync `volumes`:** an array of `{name, path}`; each name a compose volume name, each path absolute; at most 50. **`databases`:** an array of `{service, image}`, each service one of `services`. Anything else → 422 naming the field, and nothing is saved.
- **`POST /projects/:name/backups`:**
  - signed in, a known project → 302 to the project
  - signed out → sign-in
  - nothing deployed, or no storage → 302 with the reason, and no run
- **Enter preconditions** (claim): the run is `queued`, and the project has no `running` run with a heartbeat under 2 minutes old. Anything else: no claim, no docker commands.
- **Effects:**
  - One restic snapshot per GO run, and nothing else durable outside Mission Control. The staging volume and helper containers are removed on every path.
  - Durable in Mission Control: the run's status, error, found and snapshot id.

### Templates filled
**At-least-once / worker**
- Enqueue site: `BackupRun.request!` (and later the schedule, the runner API).
- Exclusive claim: conditional `update_all` queued → running with a token digest, plus the partial unique index on running per project.
- Enter allow-list: `queued`. Everything else is a no-op.
- Duplicate delivery: the second `perform` finds the run not queued and does nothing. **restic runs ≤ 1 time** (test counts `restic backup` calls).
- Process kill mid-work: the heartbeat stops. After 2 minutes, the next claim for the project abandons the run NO-GO. Leftover containers are removed by name at the next run's start.
- Mid-work, the run leaves the allow-list (abandoned by a takeover): finalize is refused, and nothing is overwritten.
- TOCTOU windows:
  - (a) claim vs claim: the conditional flip decides.
  - (b) takeover vs a slow-but-alive job: finalize is conditional on the token.
  - (c) staging reused by two runs of one project: impossible, because running is unique per project and a takeover only happens after 2 minutes of silence. The leftovers cleanup is keyed to the project, so the new run removes a stuck old container first.
- Repair when finalize is refused: none needed. The run is already final (NO-GO abandoned), and restic may list an extra snapshot, which is harmless.
- Stuck-alive (the heartbeat thread lives, docker hangs on NFS): the 3-hour deadline kills it, removes the containers, and gives NO-GO.

**Crash gap**
- Durable step 1: the restic snapshot.
- Dies before finalize: the run shows abandoned (NO-GO) after 2 minutes; the snapshot exists and is listed.
- Retry: the next Back up now or scheduled run, which starts clean.
- Must not block repair: the leftovers cleanup and the staging recreate are unconditional.

### AC ↔ test map (Batch 1)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Sync sends the app's named volumes and its Postgres services (`postgres:17`, `docker.io/library/postgres:17-alpine`, `ghcr.io/x/my-postgres@sha256:…` yes; `redis:7`, `timescale/timescaledb` no) | `internal/mission` `TestRequestForData` | Contract |
| 2 | Mission Control stores them; a relative path, a databases service not in `services`, 51 volumes, or a non-array → 422 naming the field, nothing saved | `integration/api_sync_test.rb` `test "sync records the data to back up"` | Contract |
| 3 | `request!`: queued once for a double request; refused with "nothing deployed yet" and with no acknowledged location | `models/backup_run_test.rb` `test "requesting a backup"` | Preconditions |
| 4 | Two claims of one run: one wins; a live running run holds the next one back; one silent 2 minutes is abandoned NO-GO and the next one claims | `test "claiming a run"` | Concurrency |
| 5 | **Dual delivery:** two `BackupJob#perform`s of one run → `restic backup` runs once | `jobs/backup_job_test.rb` `test "a second delivery does nothing"` | At-least-once |
| 6 | **Finalize ownership:** the run is abandoned and taken over while the job works → the old job's finish writes nothing | `test "a job that lost its run finalizes nothing"` | At-least-once |
| 7 | The engine's commands, in order: cleanup, the staging volume, pg_dump per database with the name as argv, globals, the SQLite helper, `restic backup --host houston` with the tags, volumes read-only, the exclude file, then staging removed. The password and credentials are in env only, never argv or the run's log. GO with the snapshot id and bytes | `models/backup_test.rb` `test "a backup, step by step"` | Contract, Atomicity |
| 8 | pg_dump fails → NO-GO naming the service and database, no `restic backup`, staging removed; restic fails (exit 1) → NO-GO with its last lines, staging removed; restic exit 3 → GO with the warning and the snapshot id | `test "a failing step stops the backup and cleans up"` | Crash & repair, Signals |
| 9 | No volumes and no databases → skipped, no docker commands | `test "nothing to back up"` | Preconditions |
| 10 | Timeout → containers removed by name, NO-GO "took longer than 3 hours"; the heartbeat moves while a command runs | `test "a hung backup is stopped"` | At-least-once (stuck-alive) |
| 11 | `lib/backup/sqlite.sh`, run for real (sqlite3 is in the dev image): a WAL database with rows only in `-wal`, a non-SQLite file, a file named `a'b *[x].sqlite3`, and a nested one. Each backup opens and has every row; the manifest maps numbers to the original paths; the exclude file matches exactly the live files, `-wal` and `-shm` | `test/lib/backup_sqlite_script_test.rb` `test "finds and copies SQLite databases"` | Contract (hostile input) |
| 12 | Back up now: queues one run for two clicks and shows "Backing up…", then the result; signed out → sign-in, no run; no storage → no button; nothing deployed → the reason | `controllers/project_backups_test.rb` `test "back up now"` | Authz, Preconditions |

### Deploy notes
- **Migration:** `backup_runs` plus `volumes` and `databases` on projects (JSON, default `[]`). Run down and up.
- **Old projects:** projects synced before this have `[]` until their next deploy syncs, so Back up now says "nothing to back up" until then. The page says so ("deploy once to record its data").
- **Installer:** the new environment variable. `queue.yml` changes take effect on restart.

### Agent loop checkpoints
- The spike, then red for all rows, then green; mutations on the claim flip, the finalize guard and the exclude escaping.
- scotty-review with the cold pass; then Batch 2 from this file.

### Done (Batch 1)
- **The spike:** RESTIC SPIKE PASS (notes above). It changed two things in the design: the cache volume, and exit 3 as GO with a warning.
- **Red:** the Go test didn't compile; Mission Control had 3 failures and 21 errors, every new test failing for a missing piece. **Green:** Mission Control 172 runs and the Go suite. The migration runs down and up. rubocop, gofmt and shellcheck are clean on touched files.
- **Added while building, each with a test:**
  - Row 13, `test/models/docker_command_test.rb`: `DockerCommand::Runner` for real (stdin, exit codes, a 1 s timeout → 124, a pipe carrying bytes and both commands' stderr), with a stand-in `docker` on `PATH`.
  - The job gives up after waiting as long as a backup may run (3 h 2 min) and finishes the run NO-GO. It's never left queued forever.
  - **Helpers run as root** (`--user 0`): Mission Control's image is uid 1000, but the staging volume is root's and the app's volumes are the app user's.
  - **Container names use dots** (`houston-backup.equip.restic`), and cleanup removes exact names. A prefix filter on `houston-backup-equip-` would also have removed project `equip-x`'s containers.
  - **Database names go to pg_dump as `dbname='…'`** with `\` and `'` escaped. A name with `=` would otherwise be read as connection options.
  - The SQLite finder is **Ruby** (`lib/backup/sqlite.rb`) in Mission Control's image, not shell. Names with quotes, newlines or non-UTF-8 bytes are handled, and it prints JSON.
- **A latent fixture bug:** `storage_locations.yml` stored `settings` as YAML (unquoted JSON is a YAML mapping) with the wrong keys. Nothing read it until now.
- **Mutations, each caught:**
  - the claim flip without `status: "queued"`
  - finalize without the token and status guard
  - exclude patterns unescaped
  - the conninfo unescaped
  - no staging cleanup on failure
  - the pipe dropping the first command's stderr
  - exit 3 treated as failure
  - (Go) an image's registry port read as its tag
- **scotty-review (cold pass):** four findings, all fixed:
  - **HIGH:** an unexpected exception left the run "running". Now it finishes NO-GO "Houston failed during the backup: …", cleans up, and re-raises. Red, then green.
  - **MEDIUM (stuck-alive):** a run whose Mission Control stopped showed "Backing up…" until the next claim. Now, once it's silent for 2 minutes, the page shows NO-GO "Mission Control stopped during the backup". Red, then green.
  - **LOW:** `BackupRun#owned_by?` had only a test caller, so it was cut.
  - **LOW:** the SQLite finder sorted every path in a volume in memory. It now sorts only the databases found, and caps its warnings at 20.
- **A real smoke test** of the Postgres commands against `postgres:17` (the database list, pg_dump with a quoted conninfo, globals). The engine as a whole runs for real in Batch 7.

## Decisions (yours)
1. **A failed pre-deploy snapshot stops the deploy** ("1 yes"): NO-GO "pre-deploy snapshot failed: …", and the old version keeps serving.
2. **The pre-deploy snapshot runs just before the release hook** ("move it"), after the build and the accessories, instead of the design's step 03. Writes during the build are in it, and it's still before any migration.
3. **sftp stays deferred** ("defer"), to spec §12 "later".
4. **The schedule uses Houston's configured time zone, UTC by default** ("based on whatever houston is configured to use with UTC as the default"). Batch 3 adds `time_zone` to the installation (an IANA name, default `UTC`), a Settings field, and `houston settings --server` shows it. `daily 03:00` means 03:00 there, DST included: a skipped hour runs at the next valid time, and a repeated one runs once.

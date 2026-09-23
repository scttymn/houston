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
2. **Retention and the project page:** `forget` after each backup (`--keep-daily <auto>` / `--keep-last <deploy>`), `prune` once a day per location; the Snapshots panel (Scheduled / Pre-deploy, with sizes) and the Backup plan panel. (Split from the old Batch 2, which was past the ~12-row signal.)
3. **The API and the CLI:** `/api/v1` snapshots and backups; `houston snapshots --server`, `houston backup --server [--follow]`; the last backup in `houston status`.
4. **The schedule:** `backups.schedule` (daily HH:MM, in Houston's time zone, UTC by default; Decision 4) through sync; a recurring tick; once per day per project, caught up after downtime; NEXT BACKUP on the flight board; a failed backup shows as NO-GO.
5. **The pre-deploy snapshot:** the runner API (one per deploy, idempotent), deploy step "Snapshot" after Build and before Accessories (Decision 2, refined in Batch 5) in Go and on the deploy page, skipped with no data, its failure stops the deploy (Decision 1).
6. **Volume placement:** a location per named volume (Add project, the project page, `houston volumes --server`); sync creates new volumes there (NFS subdirectory, host path); a volume that exists elsewhere is a NO-GO, not a silent move; the SQLite-on-NFS warning; Postgres data stays on local disk.
7. **Storage locations in Settings and per-project backup targets:** list with capabilities (live volumes / backups), add (the first-run form, reused), make default, the password shown once; the project's backup target; `houston storage list|add` (secrets from stdin).
8. **The real run:** on OrbStack, the equip copy (SQLite in WAL mode) and the spike (Postgres), with an NFS server container as a location and a host path as another: Back up now, the schedule, a pre-deploy snapshot, retention, and the snapshot's contents restored by hand and opened (`sqlite3`, `pg_restore --list`).

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
4. **The schedule uses Houston's configured time zone, UTC by default** ("based on whatever houston is configured to use with UTC as the default"). Batch 4 adds `time_zone` to the installation (an IANA name, default `UTC`), a Settings field (Settings › General), and `houston settings [--time-zone NAME]` shows and sets it (remote-only, like `status`). `daily 03:00` means 03:00 there, DST included: a skipped hour runs at the next valid time, and a repeated one runs once.

## Batch 2: Retention and the project page

### Design (short)
- **Sync sends `backups: {keep_auto, keep_deploy}`** from `x-houston.backups.keep` (defaults 14 / 10). They're stored on the project (`keep_auto`, `keep_deploy`, integers). Absent → the defaults; outside 1–1000 → 422.
- **Retention, after each GO backup** (in `Backup`, same run, same deadline):
  - `restic forget --host houston --tag project:<name>,kind:<kind> --group-by '' --json`, with `--keep-daily <keep_auto>` for `auto` or `--keep-last <keep_deploy>` for `deploy`
  - Never after NO-GO or skipped.
  - A failed forget leaves the run GO, with a warning ("old snapshots weren't forgotten: …"): the new snapshot is safe, and the next run tries again.
- **Prune:** `PruneJob` on the `backups` queue, so it never overlaps one of Houston's backups. It runs daily at 04:30 UTC (`recurring.yml`, production).
  - For each acknowledged location with at least one backup run, it runs `restic prune --retry-lock 30m`, with a 3-hour timeout.
  - It records `pruned_at` / `prune_error` on the location, and logs a failure. Settings shows them in Batch 7.
- **`Snapshots.for(project, location)`:** `restic snapshots --json --host houston --tag project:<name>` against the default location.
  - It returns the snapshots newest first, each with `id`, `time`, `kind`, `reason`, `deploy`, `sha` and `bytes` (from `summary.total_bytes_processed`), at most 1000.
  - Cached for 10 minutes; a GO backup clears the cache.
  - A failure raises `Snapshots::Unavailable` with restic's words, and isn't cached.
- **The project page:**
  - **Snapshots panel:** a lazy Turbo frame from `GET /projects/:name/snapshots[?kind=deploy]`, so a slow B2 listing never holds up the page. It has tabs Scheduled (`kind:auto`) and Pre-deploy (`kind:deploy`), each with its rule and "kept N / keep". The rows show when, a note ("Back up now", "before deploy #n"), the SHA and the size.
  - **Backup plan panel** (derived, read-only):
    - keep rules and storage (the default location's name)
    - volumes (name → path)
    - databases: each Postgres service · pg_dump, and each SQLite file from the last GO run · .backup
    - not backed up: the other accessories, which start empty after a restore
  - The schedule row arrives with Batch 4.

### AC ↔ test map (Batch 2)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Sync sends `backups: {keep_auto: 14, keep_deploy: 10}` by default and the file's values when set | `internal/mission` `TestRequestFor`, `TestRequestForData` | Contract |
| 2 | Mission Control stores them; absent → 14 / 10; 0, 1001, "5" → 422 naming `backups` | `integration/api_sync_test.rb` `test "sync records the keep rules"` | Contract |
| 3 | After a GO auto backup: `forget` with host, the AND tag filter, `--group-by ''` and `--keep-daily 14`. A deploy-kind run uses `--keep-last 10`. No forget after NO-GO or skipped. A failed forget → GO with the warning. The snapshots cache is cleared by a GO | `models/backup_test.rb` `test "retention after a backup"` | Contract, Signals |
| 4 | PruneJob: `restic prune --retry-lock 30m` once per used, acknowledged location; `pruned_at` set; a failure → `prune_error` and a log line, and the next location still pruned; on the `backups` queue; production's recurring entry exists | `jobs/prune_job_test.rb` `test "pruning each location"` | Signals, Scale |
| 5 | `Snapshots.for`: the command (host, `project:<name>` filter), newest first, the fields; a second call within 10 minutes runs no restic; a failure raises with restic's words and isn't cached | `models/snapshots_test.rb` `test "listing a project's snapshots"` | Contract, Scale |
| 6 | `GET /projects/:name/snapshots`: Scheduled rows by default, Pre-deploy with `kind=deploy` ("before deploy #7"), sizes, "kept 2 / 14"; restic failing → the message in the panel; no storage → "no backup storage yet"; signed out → sign-in; unknown project → 404 | `controllers/project_snapshots_test.rb` `test "the snapshots panel"` | Authz, Contract |
| 7 | The project page has the lazy frame and the Backup plan panel: keep rules, storage name, volumes, Postgres services, the last GO run's SQLite files, and "not backed up: cache" | `controllers/project_backups_test.rb` `test "the backup plan"` | Contract |

### Done (Batch 2)
- **Red:** the Go test failed; Mission Control had 1 failure and 9 errors in the new tests. **Green:** Mission Control 181 runs and the Go suite. The migration runs down and up. rubocop and gofmt are clean.
- **Test mistakes fixed on the way:**
  - The takeover test's override matched every restic call, and forget is now a second one.
  - A `?` in an `assert_select` selector is a substitution placeholder.
  - **The setup gate answers first when no storage is acknowledged.** Batch 1's "no storage → no button" assertion had passed vacuously on a redirect. Both panels' tests now assert the redirect to setup; `BackupRunTest` covers the model's own refusal.
- `Project#backup_location` is the one place that says where a project's backups go (the acknowledged default). Batch 7's per-project target changes only it.
- **Mutations, each caught:**
  - forget with restic's default grouping
  - no cache clear after a GO
  - a failed forget failing the run
  - pruning unused or unfinished locations
  - a prune failure stopping the rest
  - snapshots not cached
  - keep rules unchecked
- **scotty-review (cold pass):** one LOW finding, fixed: `Snapshots.list` capped at 1000 in restic's order (oldest first) before sorting, which would drop the newest. It now sorts, then caps, with a test. No new public API without a production caller, and no inert state. Prune failures are recorded on the location and logged (shown in Settings in Batch 7).

## Batch 3: The API and the CLI

### Design (short)
- **`/api/v1`** (personal tokens, through the tunnel):
  - `GET /projects/:name/snapshots` → `{snapshots: [{id, short_id, time, kind, reason, deploy, sha, bytes}]}`, newest first (`Snapshots.for`, cached). restic failing → 502 with its words. No backup storage → 409.
  - `POST /projects/:name/backups` → 202 with the run (Back up now; a queued one is returned as is). Refused → 422 with the reason.
  - `GET /projects/:name/backups/:id` (or `latest`) → the run. Another project's run → 404.
  - **The run's view:** `{id, status, kind, reason, deploy, sha, snapshot_id, bytes, error, queued_at, started_at, finished_at}`. A stale running run reads as `no_go` with "Mission Control stopped during the backup", as the page shows it.
  - `GET /projects/:name` gains `last_backup` (that view, or null).
- **CLI** (remote-only, like `status` and `deploys`: no `--server` flag, and `--project` or the compose file's name):
  - `houston snapshots [--json]`: one line each, newest first, as `time (UTC)  kind  note  sha  size  short id`.
  - `houston backup [--follow]`: "Queued a backup of <name> (run N)."
    - With `--follow`, it polls every 2 s until finished.
    - GO → "GO: <name> backed up: snapshot <short>, <size>" (plus the warning, if any), exit 0. Skipped → "Nothing to back up: …", exit 0. NO-GO → the error, exit 1.
    - Refused → the reason, exit 1.
  - `houston status --project <name>`: a `backup` line with GO (time, short id, size), NO-GO (the error), "running", or "none yet".

### AC ↔ test map (Batch 3)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Snapshots over the API: the shape, newest first; restic failing → 502 with its words; no storage → 409; unknown project → 404; the runner token → 401 | `integration/api_v1_backups_test.rb` `test "snapshots over the API"` | Contract, Authz |
| 2 | Back up now over the API: 202 and queued; again → the same run; nothing deployed → 422 with the reason; `backups/:id` and `latest`; another project's run → 404; a stale running run reads NO-GO | `test "backing up over the API"` | Contract, Preconditions |
| 3 | `GET /projects/:name` has `last_backup` (null before any) | `test "the project shows its last backup"` | Contract |
| 4 | The client's `Snapshots`, `BackupNow`, `Backup` hit the right paths, decode, and pass API errors through | `internal/server` `TestBackupsClient` | Contract |
| 5 | `houston snapshots`: the lines, newest first; `--json` is the API's; an API error → exit 1 with it | `internal/cli` `TestSnapshots` | Contract |
| 6 | `houston backup`: queued → exit 0; `--follow` through running to GO → exit 0 with the snapshot and size; to NO-GO → exit 1 with the error; to skipped → exit 0; refused → exit 1 with the reason | `TestBackup` | Contract, Signals |
| 7 | `houston status --project`: the backup line (GO, NO-GO, none yet) | `TestStatusShowsTheLastBackup` | Contract |

### Done (Batch 3)
- **Red:** the Go client didn't compile, and the CLI tests failed.
  - **The Rails API tests weren't run red before their code.** I wrote both before running either. Mutations stand in for that: a stale run reading as running, another project's run by id, restic failing unhandled, no `last_backup`, and create answering 200. Each was caught.
- **Green:** Mission Control 184 runs and the Go suite. rubocop and gofmt are clean.
- **Go mutations, each caught:** follow stopping at "running"; sizes in 1000s; NO-GO exiting 0.
- `postJSON` takes any 2xx now: Back up now answers 202 Accepted.
- **The CLI is remote-only, as `status` and `deploys` are:** `houston snapshots` and `houston backup`, with no `--server` flag. A NO-GO result is printed on stdout, like `deploys show`.
- **scotty-review (cold pass):** no findings to fix.
  - **Noted:** like `deploys show --follow`, `backup --follow` waits on a run stuck in "queued" for as long as it stays there. That happens only if the job worker itself is down; Ctrl-C ends it. A running run that goes silent reads as NO-GO after 2 minutes.

## Batch 4: The schedule

### Design (short)
- **Sync sends `backups.schedule`** ("daily HH:MM"). It's stored as `projects.backup_schedule` (default `daily 03:00`); anything else → 422.
- **Houston's time zone:** `installations.time_zone`, an IANA name, default `UTC`.
  - Set on **Settings › General** (`/settings/general`, a select of IANA zones). The header's Settings link goes there, and a small sub-nav joins General and API tokens.
  - Also set over `PATCH /api/v1/settings` and read with `GET /api/v1/settings` (`{base_domain, time_zone}`). The CLI is `houston settings [--time-zone NAME]`.
  - An unknown name → 422, nothing saved.
- **`BackupSchedule`** (pure, given a project, a zone and a time):
  - `due_at(date)`: HH:MM on that local date. It's `ActiveSupport::TimeZone#local`, so a time in a spring-forward gap becomes the first valid time after it, and a repeated hour picks the first.
  - `due?(now)`: `now >= due_at(today)`, and no scheduled run has `scheduled_for = today` (both in the local date).
  - `next_at(now)`: today's due time if today's hasn't run, else tomorrow's.
- **`BackupScheduleJob`,** every minute (production `recurring.yml`, `default` queue: it only queues):
  - For each project with a running deploy, something to back up, and a backup location, it queues `request!(reason: "schedule", scheduled_for: today)` when due.
  - **Once per project per local day:** a unique index on `(project_id, scheduled_for)`. A duplicate tick (two processes, a retry) hits it and is a no-op.
  - **Catch-up:** if Mission Control was down at 03:00, the first tick after it comes back runs the day's backup. A whole day missed is skipped, not doubled up.
- **The flight board:**
  - NEXT BACKUP is the soonest `next_at` across projects, in Houston's zone ("03:00 CEST"), or "—" with none.
  - A project whose last backup is NO-GO shows "backup NO-GO" under its status.
- **The project page:** the Snapshots rule reads "Daily at 03:00 (Europe/Berlin), plus Back up now…", and the Backup plan gets a SCHEDULE row.
- **The API and CLI:** `GET /projects/:name` has `backup_schedule` and `time_zone`, and `houston status` shows `schedule  daily 03:00 (Europe/Berlin)`.

### AC ↔ test map (Batch 4)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Sync sends `schedule` (default and the file's) | `internal/mission` `TestRequestFor`, `TestRequestForData` | Contract |
| 2 | Mission Control stores it; absent → `daily 03:00`; `daily 3:00`, `hourly`, `daily 24:00` → 422 | `integration/api_sync_test.rb` `test "sync records the schedule"` | Contract |
| 3 | `BackupSchedule`: `due_at` in UTC and in Europe/Berlin; New York's spring-forward gap (02:30 on 2026-03-08 → 03:30 EDT) and fall-back (01:30 on 2026-11-01 → once, the first); `due?` before and after, and after today's run; `next_at` today and tomorrow | `models/backup_schedule_test.rb` `test "when a project is due"` | Contract |
| 4 | The tick queues due projects once per local day, even ticked twice. It skips a project that's not yet due, never deployed, has nothing to back up, or has no storage. It catches up the same day, not a missed one. Production's recurring entry exists | `jobs/backup_schedule_job_test.rb` `test "the schedule tick"` | At-least-once, Preconditions |
| 5 | The database refuses a second scheduled run for a project and date | `models/backup_run_test.rb` `test "one scheduled backup per project per day"` | Concurrency |
| 6 | Settings › General: set a zone (then shown); an unknown one → 422, nothing saved; signed out → sign-in | `controllers/settings/general_controller_test.rb` `test "the time zone"` | Contract, Authz |
| 7 | `/api/v1/settings` GET and PATCH (unknown zone → 422); `houston settings` shows and sets | `integration/api_v1_settings_test.rb`, `internal/cli` `TestSettings` | Contract |
| 8 | The flight board shows NEXT BACKUP in the zone and "backup NO-GO"; the project page's schedule row and rule; `houston status` shows the schedule | `controllers/projects_controller_test.rb` `test "the next backup"`, `project_backups_test.rb`, `TestStatusShowsTheLastBackup` | Contract, Signals |

### Done (Batch 4)
- **Red:** the Go tests failed; Mission Control had 2 failures and 10 errors in the new tests. **Green:** Mission Control 194 runs and the Go suite. The migration runs down and up. rubocop and gofmt are clean.
- **Found on the way:**
  - **A test bug:** the DST test changed the schedule after building the `BackupSchedule`, which reads HH:MM when it's created.
  - **A real bug:** UTC showed as `Etc/UTC` (`tzinfo.name`). `zone.name` keeps the configured name.
- **Behaviour checked in Rails before pinning it:** `ActiveSupport::TimeZone#local` gives 02:30 on 2026-03-08 in New York as 03:30 EDT, and 01:30 on 2026-11-01 as the first (EDT).
- **Mutations, each caught:**
  - `due?` ignoring today's run
  - scheduled runs deduped like manual ones
  - the tick in UTC
  - the tick backing up never-deployed projects
  - any time zone accepted
  - the schedule unchecked in sync
  - the next backup ignoring today's run
- **scotty-review (cold pass):** nothing to fix. Two behaviours noted:
  - **A failed scheduled backup doesn't run again the same day** (once per local day, by design). It shows as "backup NO-GO" on the flight board, and Back up now is there.
  - **Changing the time zone mid-day** can make that day's scheduled backup run twice or not at all, because the local date moves.
- Settings › General is where the header's Settings link goes now, with a sub-nav to API tokens.

## Batch 5: The pre-deploy snapshot

### Decision 2, refined
The snapshot goes **after Build and before Accessories**, not just before Release. The Accessories step boots the accessories and **reboots any whose config changed**: a new Postgres image (16 → 17), new options. A snapshot after that could meet a database that won't start on its old data, and it wouldn't be the data as the old version left it. After the build is where Decision 2 wanted it: the long part is done, and nothing has touched the data yet.

### Design (short)
- **Runner API** (runner token, localhost only; the deploy's own token in `X-Houston-Deploy-Token`, like progress reports):
  - `POST /api/deploys/:id/snapshot` → 202 with the run: a `deploy`-kind run, reason `deploy`, `deploy_number`.
    - **One per deploy:** a unique index on `(project_id, deploy_number)` where reason is deploy. A retried POST gets the same run.
    - "Nothing deployed yet" (a first deploy) → 200 `{status: "skipped", error: "nothing deployed yet"}`, and no run.
    - No backup storage → 409, so the deploy stops (Decision 1).
    - A token that isn't the deploy's → 403; a deploy no longer in flight → 409. No run either way.
  - `GET /api/deploys/:id/snapshot` → that run (same checks).
- **Its own queue:** `BackupJob` for a deploy run goes on `snapshots` (2 threads). A deploy never waits behind another project's hours-long backup; it waits only for its own project's running backup (`running` stays unique per project).
- **`--retry-lock 10m`** on `restic backup` and `forget`: a snapshot can now overlap the daily prune, which holds the repository's exclusive lock.
- **Go `internal/deploy`:** step `Snapshot` after Build, before Accessories.
  - It POSTs, then polls every 2 s (`Options.SnapshotEvery`) until the run finishes.
  - GO → the log line `ok  snapshot 5c5edd4c · kind:deploy sha:<running> · 391 MB`. Skipped → `no snapshot: <why>`, and the deploy goes on.
  - NO-GO → the deploy is NO-GO "pre-deploy snapshot failed: …; the old version keeps serving". Nothing after it runs.
  - The deploy's deadline passing while it waits → NO-GO "the pre-deploy snapshot didn't finish before the deploy's deadline". Mission Control's run goes on; restic can finish the snapshot.
  - A hand `houston deploy` does the same (it has a deploy and a token).
- **Mission Control:** `Deploy::STEPS` gains `Snapshot` between Build and Accessories, so the deploy page lists it.

### AC ↔ test map (Batch 5)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | POST with the deploy's token: 202, a queued deploy-kind run with its number, on the `snapshots` queue; again → the same run, one job | `integration/api_snapshots_test.rb` `test "a deploy asks for its snapshot"` | At-least-once |
| 2 | A first deploy → skipped, no run; no storage → 409; the wrong token → 403; a finished deploy → 409; a personal token → 401; through Cloudflare (`Cf-Ray`) → refused | `test "who may ask, and when"` | Authz, Preconditions |
| 3 | GET: the run's view; after it's GO, the snapshot id and bytes | `test "a deploy asks for its snapshot"` | Contract |
| 4 | The database allows one deploy snapshot per deploy number | `models/backup_run_test.rb` `test "one snapshot per deploy"` | Concurrency |
| 5 | `restic backup` and `forget` carry `--retry-lock 10m`; a deploy run's job is on `snapshots`, and a manual one's on `backups`; `queue.yml` has the worker | `models/backup_test.rb`, `jobs/backup_job_test.rb` | Concurrency |
| 6 | `Deploy::STEPS` has Snapshot between Build and Accessories; the deploy page lists it | `models/deploy_test.rb` (step states), `deploy_pages_test.rb` | Contract |
| 7 | The mission client's `Snapshot` and `SnapshotStatus`: paths, the token header, decoding, 403/409 as errors | `internal/mission` `TestClientSnapshots` | Contract |
| 8 | Deploy: Snapshot runs after the push and before the accessories; GO logs the line and goes on; skipped logs why and goes on | `internal/deploy` `TestDeploySnapshot` | Contract |
| 9 | Deploy: a NO-GO snapshot → NO-GO with its error, and no accessory boot, release or kamal deploy; the deadline passing while it waits → NO-GO with the reason | `TestDeploySnapshotStops` | Crash & repair |

### Done (Batch 5)
- **Decision 2, refined:** the snapshot comes after Build and before Accessories, because the Accessories step can reboot a changed Postgres. See above.
- **Red:** Mission Control had 7 failures in the new tests; the Go packages didn't build. **Green:** Mission Control 198 runs and the Go suite. The migration runs down and up. rubocop, gofmt and go vet are clean.
- **Two existing tests changed on purpose:** the happy path's and a claimed deploy's mission calls now include `snapshot`.
- **Added:**
  - `internal/humanize`, so the CLI and the deploy log print sizes the same way (it was in `internal/cli`).
  - A retried POST after the snapshot is done still gets the same run. This came from the review; the first version only retried while the run was queued.
- **Mutations, each caught:**
  - Rails: no token check; no in-flight check; deploy snapshots on the backups queue; deploy snapshots deduped like manual ones; no `--retry-lock`.
  - Go: a NO-GO snapshot letting the deploy go on; the deadline read as a plain timeout; a refused request letting the deploy go on.
- **scotty-review (cold pass):** nothing to fix.
  - Ownership and "in flight" are checked before a run is created. A takeover mid-wait stops as any stop does. The wait is bounded by the deploy's deadline, and an API blip mid-wait is logged and retried until then.
  - **Noted:** a very large snapshot counts against the deploy's 30-minute deadline. Batch 8's real run will show real durations.

## Batch 6: Volume placement

### Design (short)
- **Where a volume can live:** local disk (built in; no location), or an acknowledged location of kind `nfs` or `local` (a host path): `StorageLocation#live?`. s3 and b2 hold backups only (spec §9).
- **`ProjectVolume`** (`project_id`, `name`, `location_id` null = local disk, `placed_at`; unique `(project_id, name)`). A row is made for each app volume at sync (local disk unless chosen), or earlier when chosen.
- **Choosing** is allowed until the volume is placed (`placed_at` set); after that it's fixed ("moving a volume is a later feature", spec §12):
  - Add project's Save form: a select per volume the preview found (local disk or a live location)
  - the project page's Volumes panel: the same select per volume, until it's placed
  - `PATCH /api/v1/projects/:name/volumes/:volume {location: <name> | null}`, and `houston volumes place VOLUME LOCATION` / `--local-disk`
- **Placing, at sync** (`VolumePlacement`, after `ProjectSync#save!`, before DNS; a refusal is the sync's 422, so the deploy stops before Kamal). For each app volume, `docker volume inspect --format '{{json .Options}}' <name>_<volume>`:
  - **Absent:**
    - local disk → `docker volume create <name>_<volume>`
    - `nfs` → make `volumes/<name>/<volume>` on the export (a helper container, root, with the location's `houston-storage-<location>` volume), then `docker volume create --driver local --opt type=nfs --opt o=addr=<server>,rw,nfsvers=4 --opt device=:<export>/volumes/<name>/<volume> <name>_<volume>`
    - `local` path → make `<path>/volumes/<name>/<volume>` (a helper, root, the path mounted), then `--opt type=none --opt o=bind --opt device=<path>/volumes/<name>/<volume>`
    - Then `placed_at` is set.
  - **Present and matching** (no options for local disk; the same device for a location) → `placed_at` is set; nothing is created. A crash between create and record lands here.
  - **Present elsewhere** → refused: "<name>_<volume> already exists on <where>, not <chosen>; Houston doesn't move volumes yet: choose <where>, or move it yourself". Nothing is created.
  - The helper's mkdir failing → refused with its words.
  - Postgres data (accessory volumes) isn't an app volume, so it stays where Kamal makes it: local disk.
- **The SQLite-on-NFS warning:** on the Volumes panel, next to an NFS location: "Live SQLite over a network share risks corruption". It's stronger when the last GO backup found SQLite files in that volume.
- **`GET /api/v1/projects/:name/volumes`** → `[{name, path, location, placed}]`; `houston volumes` lists them.

### AC ↔ test map (Batch 6)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Placing on local disk, NFS, a host path: the exact commands (mkdir as root through the location, then create with those options); `placed_at` set; the volume names from the project and the volume only | `models/volume_placement_test.rb` `test "placing new volumes"` | Contract |
| 2 | An existing matching volume → placed, nothing created. One elsewhere → refused with where it is, nothing created. mkdir failing → refused with its words | `test "a volume that's already somewhere"` | Crash & repair, Preconditions |
| 3 | Sync places the volumes and a refusal is its 422 (DNS untouched); volumes of an older sync without the field → none | `integration/api_sync_test.rb` `test "sync places the volumes"` | Contract |
| 4 | Only live, acknowledged locations can be chosen; a placed volume can't be changed (409); an unknown volume → 404 | `models/project_volume_test.rb`, `integration/api_v1_volumes_test.rb` | Preconditions |
| 5 | The project page: a select per unplaced volume, saving it; a placed one read-only with its location; the SQLite-on-NFS warning; signed out → sign-in | `controllers/project_volumes_test.rb` `test "choosing where a volume lives"` | Authz, Contract |
| 6 | Add project's Save: the chosen locations are stored for the new project | `controllers/project_links_test.rb` `test "choosing volume locations when saving"` | Contract |
| 7 | API: list, place by name or null, refused choices → 422/409 | `integration/api_v1_volumes_test.rb` `test "volumes over the API"` | Contract, Authz |
| 8 | CLI: `houston volumes` lists; `volumes place media unas-nfs` and `--local-disk`; errors → exit 1 | `internal/cli` `TestVolumes` | Contract |

### Done (Batch 6)
- **Red:** the Go CLI failed; Mission Control had 2 failures and 6 errors in the new tests. **Green:** Mission Control 206 runs and the Go suite. The migration runs down and up. rubocop and gofmt are clean.
- **Found on the way:**
  - **The location's own NFS volume is made sure of before the mkdir** (`ensure_volume`, from setup). `docker run -v houston-storage-<location>:/location` with that volume missing (a `docker volume prune`, say) quietly makes an empty local one. The mkdir would "succeed" into it, and the app's volume would point at a directory that doesn't exist on the NFS server.
  - **The existing sync test** that sends volumes now fakes docker. Placement would otherwise run the real docker CLI in the dev container and make a real volume on this machine.
- **Mutations, each caught:**
  - an existing volume elsewhere accepted
  - no mkdir before a location volume
  - the location's volume not ensured
  - a placed volume changeable
  - backup-only locations holding live volumes
  - sync skipping placement
  - Add project ignoring the choices
- **scotty-review (cold pass):** one LOW finding, fixed: two syncs of one project at once (a hand deploy beside a runner) could race to create a volume's row and hit its unique index as a 500. Both sites use `create_or_find_by!` now.
  - Volume names are sync-validated (no `/`, no leading `.`), so the directory path can't escape `volumes/<project>/`.
  - A volume dropped from compose.yml keeps its row and its data; the lists show the file's volumes.

## Batch 7: Storage locations in Settings, and each project's backup target

### Design (short)
- **Settings › Storage** (`/settings/storage`, in the Settings sub-nav):
  - Each location: name, type, where, what it holds ("live volumes · backups" for nfs and local, "backups" for s3 and b2), DEFAULT, used by N projects, the last backup written there, and the last prune (or its error, from Batch 2).
  - **Add** (`/settings/storage/new`): the first-run form, as a shared partial, and the same `StorageSetup` (restic init, or opening an existing repository with the saved password).
    - Then the location's page (`/settings/storage/:name`) shows the password once: copy, download, and "I saved it", as in setup, as shared partials.
    - Confirming acknowledges the location. It does **not** make it the default.
    - An unconfirmed location can't be chosen anywhere, and its password page is gone once it's confirmed.
  - **Make default** (acknowledged only): projects without their own target follow it.
- **Each project's backup target:** `projects.backup_location_id` (null = the default). `Project#backup_location` is that location if it's acknowledged, else the default.
  - It's chosen on the Backup plan panel (a select of acknowledged locations, and "Default (<name>)"). The panel says that earlier snapshots stay where they were written.
  - The snapshot list and backups follow it; each run already records its location.
- **API and CLI:**
  - `GET /api/v1/storage` → the list, **never a password or credential**. `houston storage` prints it.
  - `PATCH /api/v1/storage/:name {default: true}` → `houston storage default NAME`.
  - `PATCH /api/v1/projects/:name/backup_target {location | null}` → `houston storage use NAME` / `--default` (with `--project`).
- **Adding a location stays on the Settings page for now** (a question for you, below).

### Question (answered: "keep add in Settings only")
**Adding a storage location from the CLI or API.** Adding one generates its restic password, which has to be shown once and saved. The remote API's rule so far is "never returns a secret value"; the one exception is the webhook secret, until its first delivery. A CLI `houston storage add` would print the backup password into an agent's terminal and transcript. I've kept adding on the Settings page, and made list, make default and per-project target available from the CLI. Should the CLI also add locations (printing the password once, with a `confirm` step)?

### AC ↔ test map (Batch 7)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The Storage list: each location's type, where, holds, DEFAULT, used by, last write, prune error; the sub-nav; signed out → sign-in | `controllers/settings/storage_controller_test.rb` `test "the storage list"` | Contract, Authz |
| 2 | Add: runs restic init (fake docker) and shows the password once on its page; confirming acknowledges it (not default) and the page then redirects; bad input → 422 with the field; a name already used → 422 | `test "adding a location"` | Contract, Preconditions |
| 3 | Make default: moves the default; an unconfirmed location can't be; projects without a target now back up there | `test "making a location the default"` | Preconditions |
| 4 | A project's target: chosen on the page, used by `request!` and the snapshot list; an unconfirmed location refused; back to the default | `controllers/project_backups_test.rb` `test "choosing a project's backup target"`, `models/backup_run_test.rb` | Contract |
| 5 | API: the list (no `restic_password` or credential anywhere in the JSON), make default, a project's target, errors (404, 422) | `integration/api_v1_storage_test.rb` `test "storage over the API"` | Authz, Contract |
| 6 | CLI: `houston storage`, `storage default NAME`, `storage use NAME` / `--default` | `internal/cli` `TestStorage` | Contract |
| 7 | First-run setup still works with the shared partials | `controllers/setup/storage_controller_test.rb` (unchanged, green) | Parity |

### Done (Batch 7)
- **Red:** Mission Control had 2 failures and 4 errors in the new tests. **Green:** Mission Control 212 runs and the Go suite. The migration runs down and up. rubocop and gofmt are clean.
  - **Not verified:** the Go CLI's red. Its output was cut off in that run, so mutations stand in.
- **First-run setup and Settings share the form and the shown-once panel** (`storage/_fields`, `storage/_shown_once`). Setup's own tests pass unchanged.
- **Test mistakes fixed:** two regexes with the wrong order or case. The pages were right.
- `Project::Refused` for a target that isn't confirmed (it had borrowed `ProjectVolume::Refused`).
- **Mutations, each caught:**
  - Rails: the API listing the password; confirming making a location the default; an unconfirmed location becoming the default; the password page after confirming; a project's target ignored; an unconfirmed target accepted.
  - Go: `--default` sending a name; the list hiding DEFAULT.
- **scotty-review (cold pass):** nothing to fix.
  - The restic password shows only on an unconfirmed location's page (admin, `no-store`), and never in the API.
  - **For step 6 (restore):** after a project's target or the default changes, its earlier snapshots stay where they were written, and the page says so. Restore will need to list across the locations the project's runs used (each run records its location).
- **Decided:** adding a location stays in Settings only ("keep add in Settings only").

## Batch 8: The real run

`install/test/backups-e2e.sh` (KEEP=1 keeps the machine; EQUIP_SOURCE adds equip). A fresh OrbStack machine with Houston installed and Cloudflare marked connected (wildcard; nothing goes through the tunnel). It checks:
- **Storage:** its own `nfs-kernel-server` exporting `/srv/nfs`. Two locations made with the real `StorageSetup` (restic init): `local-backups` (a host path, the default) and `vm-nfs`.
- **The first deploy HOLDs and makes no volume.** The spike's `data` volume is then placed on `vm-nfs` with `houston volumes place`, secrets are set from the CLI, and the deploy goes on. `spike_data` is an NFS volume in `vm-nfs/volumes/spike/data`, and what the app writes lands on the export.
- **Data:**
  - Postgres with a database named `we'ird=db` and a row.
  - A SQLite database in the volume, held open with its rows only in its `-wal`.
- **Back up now:** `houston backup --follow` GO, and `houston snapshots` lists it.
- **Restored by hand with restic, then opened:**
  - the manifest names the hostile database
  - `pg_restore --list` reads the dump
  - `globals.sql` is there
  - the SQLite copy has all 3 rows
  - the live SQLite file was left out of the file copy
  - the volume's files are there
- **Retention:** three more deploys, each logging its pre-deploy snapshot; `keep.deploy: 2` leaves 2.
- **The schedule:** Europe/Berlin (`houston settings --time-zone`), with the schedule set two minutes ahead. The production tick queues it, it goes GO, and `houston status` shows it.
- **Prune:** `PruneJob` on `local-backups`, with no error.
- **equip:** GO, backed up, and Rails' SQLite databases found.

### Found while writing it (a Batch 6 fix)
**The deploy's sync placed volumes before its HOLD check.** So a first hand `houston deploy` that stops at HOLD (no secrets yet) had already made the volumes on local disk, before anyone could choose where they live. Placement now runs only on a sync that goes on to deploy: after the HOLD check. Red first (`test "sync places the volumes"`, the HOLD part), then green.

### Done (Batch 8)
- **The run:** `EQUIP_SOURCE=… install/test/backups-e2e.sh` → **BACKUPS PASS**, every check green, equip included. Every item listed for this batch above passed for real:
  - a real NFS server
  - the HOLD making no volume, then the data volume placed on NFS
  - Back up now, restored by hand with restic and opened
  - three pre-deploy snapshots with 2 kept
  - the scheduled backup in Europe/Berlin
  - prune
  - equip's four Rails SQLite databases found and backed up
  The machine was deleted afterwards.
- **Real bugs it found, each fixed with a red test first:**
  - **Mission Control didn't boot in production.** Production eager-loads `lib/`, so `lib/backup/sqlite.rb` (a script for the helper container) ran at boot and crashed. `lib/backup` is left out of autoloading now. `test/eager_load_test.rb` loads everything as production does; it was red, then green.
  - **The HOLD sync placed volumes** (above).
- **A real-world effect, noted:** SQLite on NFS stalls while the NFS server is in its grace period after a restart (NFSv4: up to 90 s). File locks wait, so a SQLite database there isn't written until the grace period ends. A backup in that window finds it as it was. A restarted NAS behaves the same way. It's one more reason for the page's SQLite-on-NFS warning.
- **The script's own mistakes, fixed across the runs:**
  - `houston storage` ran from a directory that didn't exist yet.
  - I miscounted deploy numbers: the HOLD never becomes a deploy.
  - The equip copy and key were staged in macOS's temp dir, which the machine can't see. They're under `.houston/` now, as `deploy-through-tunnel.sh` does.
  - The first wait for the `-wal` was too short for the NFS grace period.
- **Ready:** Mission Control 213 runs; the Go suite. rubocop, gofmt and shellcheck are clean on touched files.

# Plan: Restore (build step 6)

Spec: https://claude.ai/artifact/R3d4fzN1u5QUP4kT88mqug (§9 Restore; §10 `POST /api/projects/:name/restore`, the Restore page; §14 "maintenance mode across stop → restore → boot; image retention and rollback after pruning").
Design: https://claude.ai/artifact/UswdZ62N4ygiKdGh1vDEPH (Restore: "Roll equip back to 18 Sep 03:00?", code now → after, data live → the snapshot, type the name to confirm, `hou restore <id> --server`).
Builds on build step 5 (docs/plans/volumes-backups.md): each snapshot holds the app's volumes, `sqlite/<n>.sqlite3` copies, Postgres dumps and roles, and `houston.json` saying what came from where.

## Your direction (this replaces the spec's maintenance-page restore)
"Default is zero-downtime. Add the ability to show a maintenance page for apps that need to prevent user access so data is not lost. If we choose to display a maintenance page and a deploy/rollback fails, then maintenance page continues to show until admin turns it off."

## Goal

Roll a project back to a snapshot, code and data together, **with no downtime by default**:
- The snapshot's data is restored into **new storage beside the live data**, while the current version keeps serving on its own.
- The old code starts on it, passes its health check, and only then does traffic switch.
- If anything fails before the switch, the current version is still serving on untouched data, the same rule deploys already follow.

A project can instead show a **maintenance page** during deploys and restores, so nobody writes data that the restore would throw away. If the operation fails, the page stays up until an admin turns it off.

## Scope

- **Data generations.** A project's stateful storage belongs to a generation.
  - **Generation 1** is today's names: app volumes `<name>_<volume>`, accessories `<service>` (containers `<name>-<service>`). Nothing that exists today is renamed.
  - **Generation g ≥ 2** uses app volumes `<name>.g<g>_<volume>` (project names have no dots, so no clash) and accessories `<service>-g<g>` (containers `<name>-<service>-g<g>`), with their own volumes.
    - Compose service names ending in `-g<digits>` become reserved (rejected, with the reason).
  - `<SERVICE>_HOST` points at the generation's container, so the app follows its generation.
  - A restore builds generation g+1 **completely**: every accessory is new. Postgres gets the snapshot's databases; services that aren't backed up start empty, as spec §9 says, with no special case.
  - After the switch, generation g's containers and volumes are removed. The safety snapshot holds them.
  - **Deploys stay in place** on the current generation: code switches with zero downtime, and the data is the data.
- **A restore is a deploy of kind `restore`,** numbered with the project's deploys and shown in its history. It's queued, claimed by a runner, owned by its token, heartbeating, and finished GO or NO-GO. One deploy or restore is in flight per project.
- **The runner does the code and Kamal; Mission Control does the data** (restic credentials never leave it, as in step 5).
- **Restore steps, zero-downtime (the default):**
  1. **Prepare:** fetch the snapshot's commit; sync its compose.yml (a HOLD stops here); pull its image from the registry, or build and push it
  2. **Storage:** Mission Control allocates generation g+1 and makes its volumes where each volume is placed (step 5's placement)
  3. **Accessories:** boot generation g+1's accessories (empty)
  4. **Restore data** into generation g+1 (Mission Control): volumes filled, SQLite copies put in place, Postgres roles and databases restored into the new Postgres
  5. **Safety snapshot** of generation g (kind deploy, reason restore), taken as late as possible, just before the switch
  6. **Switch:** `kamal deploy --skip-push --version <old sha>` with generation g+1's config; traffic moves only after the health check passes
  7. **Commit:** the project's generation becomes g+1
  8. **Clean up:** generation g's accessories and volumes are removed (a failure here is a warning; the restore stands)
  - **A failure in steps 1–6** removes whatever generation g+1 got. Generation g keeps serving, untouched, and the restore is NO-GO.
  - **Writes made to the live app while the restore runs:** the ones after the safety snapshot and before the switch (seconds) are lost. That's what the maintenance option is for.
- **With the maintenance page:**
  - `on` comes first, before step 1, and the safety snapshot is taken right after it. With nothing writing, nothing is lost.
  - On GO the page comes down.
  - On NO-GO it **stays up** until an admin turns it off (page, API, CLI). The project shows MAINTENANCE on the flight board, with why and since when.
- **The maintenance page for deploys:** a project setting, "Show a maintenance page during deploys and restores" (off by default).
  - A deploy with it on shows the page before the pre-deploy snapshot and takes it down on GO. On NO-GO it stays up.
  - A restore's confirm page (and `houston restore --maintenance / --no-maintenance`) defaults to the setting and can override it for that restore.
- **Manual maintenance:** turn the page on or off at any time (a planned migration, an incident): the project page, `PUT /api/v1/projects/:name/maintenance`, and `houston maintenance on|off`.

## Batches

1. **Maintenance mode, and the spike** (below).
2. **Data generations:**
   - one naming function in Go (the Kamal config, `<SERVICE>_HOST`, the release hook's volumes) and one in Mission Control (placement, backups, container names claimed)
   - sync carries the generation
   - reserved `-g<n>` service names
   - generation 1 behaves exactly as today (the step 3–5 suites unchanged, plus tests at generation 2)
3. **The data engine:** Mission Control's `RestoreData` into a given generation (volumes, SQLite, Postgres into the new generation's Postgres once it's ready), from the runner's request, with the manifest checked before anything is touched.
4. **Asking for a restore:**
   - the restore kind on deploys, and its snapshot and maintenance choice
   - the Restore button on each snapshot and the confirm page (the design's, with the maintenance checkbox), `/api/v1`, and `houston restore <snapshot> --confirm <name> [--maintenance | --no-maintenance] [--follow]`
   - the preconditions: a linked repo, the snapshot in a location the project's backups used (the snapshot list reads all of them now), nothing queued or in flight
   - while a restore is queued or in flight: pushes don't queue deploys (the next check does), and backups wait (except its safety snapshot)
5. **The runner's restore:** the steps above in Go, the generation switch, cleanup, every failure path, the maintenance rules, and the deploy page's restore steps.
6. **The maintenance page during deploys:** the project setting, and the deploy flow (on before the pre-deploy snapshot, off on GO, left up on NO-GO).
7. **The real run:** on OrbStack, restore the spike (Postgres, an NFS volume, SQLite) and equip, each with and without the maintenance page. It checks:
   - the zero-downtime restore (every request through it answered by one version or the other)
   - the data rolled back
   - a failed restore leaving the current version serving
   - a failed restore with maintenance leaving the page up until turned off
   - restoring the safety snapshot to go back
   - a restore whose image was pruned

## Batch 1: Maintenance mode, and the spike

### Spike first (spec §14): `install/test/restore-spike.sh`
On an OrbStack machine with Houston installed and the spike fixture deployed twice (two SHAs):
- **Maintenance:**
  - Exactly what `kamal app maintenance` and `kamal app live` run against kamal-proxy (so Mission Control can do the same with `docker exec kamal-proxy …`), and the service name kamal-proxy knows the app by.
  - What a request gets in maintenance (status, body), including after `kamal deploy` of the other SHA while in maintenance.
  - That `live` brings it back.
- **A second generation beside the first:**
  - Accessory `db-g2` (container `spike-db-g2`) with its own volume, booted while `spike-db` serves.
  - The app, deployed with `DB_HOST=spike-db-g2`, reaches it by that name on the `kamal` network.
  - `kamal accessory remove db` removes only generation 1's container.
  - Volumes named `spike.g2_data` are valid and mountable.
- **The image:** `docker rmi` the host's copy of the first SHA; `docker pull 127.0.0.1:5000/spike:<sha>` gets it back. Whether `kamal deploy --skip-push` pulls it itself.
- **git:** `git fetch --depth 1 <old, non-tip sha>` from Forgejo works, or is refused (then the runner falls back to the branch without depth).

The notes go into this file before Batch 1's code.

### Design (short)
- **`projects`:**
  - `maintenance_since` (datetime, null = off)
  - `maintenance_reason` (string: "turned on by admin", "restore #12 failed: …", "deploy #9 failed: …")
  - `maintenance_during_changes` (bool, default false; used by Batches 5–6, but settable now)
- **`Maintenance`** (Mission Control):
  - `on!(project, reason:)` runs the kamal-proxy command the spike pins, through `DockerCommand`, then records `since` and `reason`.
  - `off!(project)` runs the resume command, then clears them.
  - A failed command → an error, nothing recorded.
  - "on" when already on keeps the first `since` and updates the reason.
  - Refused before the app was ever deployed (kamal-proxy doesn't know it): "nothing deployed yet".
- **The page:**
  - a MAINTENANCE banner (since, why, "Turn it off")
  - "Show maintenance page" (turning it on asks for confirmation: users see the page at once)
  - the setting checkbox "Show a maintenance page during deploys and restores"
- **The flight board:** a MAINTENANCE chip on the project's row.
- **API:**
  - `PUT /api/v1/projects/:name/maintenance {on: true|false}` → `{on, since, reason, during_changes}`
  - `PATCH /api/v1/projects/:name/maintenance {during_changes: bool}`
  - `GET /api/v1/projects/:name` includes `maintenance`
- **CLI:**
  - `houston maintenance` shows the state; `houston maintenance on|off` sets it
  - `houston maintenance --during-changes on|off` sets the setting
  - `houston status` shows a `maintenance` line when it's on

### AC ↔ test map (Batch 1)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | `on!` runs the spike's kamal-proxy command for the project's service, then records since and reason; `off!` resumes and clears; a failed command → error, nothing recorded; on while on keeps since | `models/maintenance_test.rb` `test "turning the maintenance page on and off"` | Contract, Atomicity |
| 2 | Refused before anything was deployed; the command never runs | `test "nothing to put in maintenance"` | Preconditions |
| 3 | The page: the banner (since, why), on (confirmed) and off, the setting saved; signed out → sign-in, nothing run | `controllers/project_maintenance_test.rb` | Authz, Contract |
| 4 | The flight board's MAINTENANCE chip | `controllers/projects_controller_test.rb` `test "a project in maintenance"` | Signals |
| 5 | API: on/off, the setting, the project view's `maintenance`; unknown project 404; the runner token 401; a failed command → 502 with its words | `integration/api_v1_maintenance_test.rb` | Authz, Contract |
| 6 | CLI: `houston maintenance` shows; `on`/`off`; `--during-changes on`; errors → exit 1; `status` shows it | `internal/cli` `TestMaintenance` | Contract |

## Decisions (yours)
1. ~~When a restore fails after the maintenance page is up~~. **Answered:** zero-downtime by default; a maintenance page is an option; after a failure with it, it stays up until an admin turns it off.
2. **A restore needs a linked repo:** the runner fetches the snapshot's commit to build or run it. A project that was only ever deployed by hand (`houston deploy` on the server) has to be linked first (`houston link`), and the Restore button says so. OK?
3. **One setting for both** deploys and restores ("Show a maintenance page during deploys and restores"), with a per-restore override on the confirm page and the CLI. Or would you rather have two separate settings?

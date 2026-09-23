# Plan: Restore (build step 6)

Spec: https://claude.ai/artifact/R3d4fzN1u5QUP4kT88mqug (§9 Restore; §10 `POST /api/projects/:name/restore`, the Restore page; §14 "maintenance mode across stop → restore → boot; image retention and rollback after pruning").
Design: https://claude.ai/artifact/UswdZ62N4ygiKdGh1vDEPH (Restore: "Roll equip back to 18 Sep 03:00?", code now → after, data live → the snapshot, type the name to confirm, `hou restore <id> --server`).
Builds on build step 5 (docs/plans/volumes-backups.md): each snapshot holds the app's volumes, `sqlite/<n>.sqlite3` copies, Postgres dumps and roles, and `houston.json` saying what came from where.

## Your direction (this replaces the spec's maintenance-page restore)
"Default is zero-downtime. Add the ability to show a maintenance page for apps that need to prevent user access so data is not lost. If we choose to display a maintenance page and a deploy/rollback fails, then maintenance page continues to show until admin turns it off."
Then: "We could simply think of a maintenance page as an out of band option for deploys. It can be displayed at any time but most appropriately during a deploy."

So the maintenance page is **an out-of-band switch the admin turns on and off**. Houston never turns it on or off by itself: not when a deploy or restore starts, succeeds or fails. It stays up until an admin turns it off, whatever happens in between.
Then: "Honestly the maintenance page could be a Houston thing. So the app could completely go down and the maintenance page would always be visible."

So **Houston serves the maintenance page itself**, from Mission Control, by routing the project's hostnames there at the tunnel. It shows even if the app, its containers and kamal-proxy are all down, and no deploy or restore can clear it: they never touch the tunnel.

## Goal

Roll a project back to a snapshot, code and data together, **with no downtime by default**:
- The snapshot's data is restored into **new storage beside the live data**, while the current version keeps serving on its own.
- The old code starts on it, passes its health check, and only then does traffic switch.
- If anything fails before the switch, the current version is still serving on untouched data, the same rule deploys already follow.

An admin can put up a **maintenance page** at any time, usually around a deploy or restore, so nobody writes data a restore would throw away. Houston leaves it exactly as the admin set it.

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
  - **Writes made to the live app while the restore runs:** the ones after the safety snapshot and before the switch (seconds) are lost. Putting the maintenance page up first prevents that.
- **The maintenance page** (out of band, the admin's switch, served by Houston):
  - On or off at any time: the project page, `PUT /api/v1/projects/:name/maintenance`, `houston maintenance on|off`. The project shows MAINTENANCE on the flight board, with who turned it on and since when.
  - **On:** the tunnel's ingress gets a rule per hostname of the project (`<name>.<base>` and its custom domains) sending it to Mission Control, before the catch-all to kamal-proxy. **Off:** the rules go. Mission Control serves the page for those hosts. It never touches kamal-proxy, the app, or its containers.
  - **Deploys and restores never change it.** They don't touch the tunnel.
  - A restore takes its safety snapshot just before the switch either way. With the page up, nothing was written after it; without it, only the seconds between snapshot and switch.

## Batches

1. **Maintenance mode, and the spike** (below).
2. **Data generations:**
   - one naming function in Go (the Kamal config, `<SERVICE>_HOST`, the release hook's volumes) and one in Mission Control (placement, backups, container names claimed)
   - sync carries the generation
   - reserved `-g<n>` service names
   - generation 1 behaves exactly as today (the step 3–5 suites unchanged, plus tests at generation 2)
3. **The data engine:** Mission Control's `RestoreData` into a given generation (volumes, SQLite, Postgres into the new generation's Postgres once it's ready), from the runner's request, with the manifest checked before anything is touched.
4. **Asking for a restore:**
   - the restore kind on deploys, and its snapshot
   - the Restore button on each snapshot and the confirm page (the design's, noting whether the maintenance page is up), `/api/v1`, and `houston restore <snapshot> --confirm <name> [--follow]`
   - the preconditions: a linked repo, the snapshot in a location the project's backups used (the snapshot list reads all of them now), nothing queued or in flight
   - while a restore is queued or in flight: pushes don't queue deploys (the next check does), and backups wait (except its safety snapshot)
5. **The runner's restore:** the steps above in Go, the generation switch, cleanup, every failure path, and the deploy page's restore steps.
6. **The real run:** on OrbStack, restore the spike (Postgres, an NFS volume, SQLite) and equip, with and without the maintenance page up. It checks:
   - the zero-downtime restore (every request through it answered by one version or the other)
   - the data rolled back
   - a failed restore leaving the current version serving
   - the maintenance page staying up through a restore, a deploy, a failed one, and the app's containers stopped, until it's turned off
   - restoring the safety snapshot to go back
   - a restore whose image was pruned

## Batch 1: Houston's maintenance page, and the spike

### Spike first (spec §14): `install/test/restore-spike.sh`
Two parts. **With the real Cloudflare account** (svnmns.com, the `orbstack.sh` setup; a `houston-maint-test` name that answers the other server's 404 first):
- Add an ingress rule for one hostname → Mission Control, and time how long until a request through Cloudflare gets Mission Control's answer (cloudflared picks up remote config).
- Remove the rule → the app again, timed the same way.
- Whether a custom domain routes by the same rule.

**On OrbStack** (for later batches, recorded now):
- **A second generation beside the first:**
  - Accessory `db-g2` (container `spike-db-g2`) with its own volume, booted while `spike-db` serves.
  - The app, deployed with `DB_HOST=spike-db-g2`, reaches it on the `kamal` network.
  - `kamal accessory remove db` removes only generation 1's container.
  - `spike.g2_data` is a valid, mountable volume name.
- **The image:** `docker rmi` the host's copy of the first SHA; `docker pull 127.0.0.1:5000/spike:<sha>` gets it back. Whether `kamal deploy --skip-push` pulls it itself.
- **git:** `git fetch --depth 1 <old, non-tip sha>` from Forgejo works, or is refused (then the runner falls back to the branch without depth).

The notes go into this file before Batch 1's code.

### Design (short)
- **`projects`:**
  - `maintenance_since` (datetime, null = off)
  - `maintenance_by` (the admin, or the API token's name)
  - `maintenance_message` (optional, at most 500 characters, shown on the page)
- **`TunnelRoutes`:** the tunnel's ingress, built from the database. It's the one place that knows the rules; `CloudflareSetup` uses it too.
  - The order: admin → Mission Control; hooks webhook paths → Mission Control; hooks otherwise → 404; **each hostname of each project in maintenance → Mission Control**; everything else → kamal-proxy.
  - `push!` PUTs the whole configuration. Two toggles at once are serialized on a lock, and each push is computed from the database after its own change is committed, so the last push always matches the database.
- **`Maintenance`:**
  - `on!(project, by:, message:)` records the state, then pushes. If Cloudflare refuses, it rolls the state back and raises with Cloudflare's words: the database never says "on" while the tunnel says off.
  - `off!(project)` does the reverse.
  - Refused when Cloudflare isn't connected (a LAN-only install has no tunnel to route).
  - Adding or removing a custom domain on a project in maintenance (at sync) pushes the routes again.
- **Mission Control serving it:**
  - For any request whose host is a hostname of a project in maintenance, it serves only `MaintenancePage`: 503, `Retry-After: 60`, `Cache-Control: no-store`.
  - The page is self-contained HTML: the project's name, "is down for maintenance", the message if any, and a meta refresh every 60 s. No Mission Control chrome and no assets.
  - Every other route on those hosts is a 404, as on `hooks.<base>`.
  - For a host that isn't in maintenance (a stale route): a plain 404.
- **The page in Mission Control:**
  - a MAINTENANCE banner (since, by whom, the message, "Turn it off")
  - "Show maintenance page" with an optional message (turning it on asks for confirmation: users see the page within seconds)
- **The flight board:** a MAINTENANCE chip.
- **API:**
  - `PUT /api/v1/projects/:name/maintenance {on: true|false, message?}` → `{on, since, by, message}`
  - `GET /api/v1/projects/:name` includes `maintenance`
- **CLI:**
  - `houston maintenance` shows the state; `houston maintenance on [--message TEXT]|off` sets it
  - `houston status` shows a `maintenance` line when it's on

### AC ↔ test map (Batch 1)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | `TunnelRoutes`: the order (admin, hooks, maintenance hosts including custom domains, catch-all); `CloudflareSetup` sends the same rules | `models/tunnel_routes_test.rb` | Contract |
| 2 | `on!` records, then pushes the rules with the project's hosts; Cloudflare refusing → the state rolled back and the error raised; `off!` removes them; not connected → refused, nothing pushed | `models/maintenance_test.rb` `test "turning the maintenance page on and off"` | Atomicity, Preconditions |
| 3 | Two projects toggled together: each push is computed after its own change, so the last one holds both | `test "two toggles at once"` | Concurrency |
| 4 | A request for a host in maintenance → 503 page with the name and message, `Retry-After`, `no-store`, no chrome; any other path on that host → the same page; sign-in, the API and webhooks on that host → 404; a host not in maintenance → 404 | `integration/maintenance_page_test.rb` | Authz, Contract (hostile input) |
| 5 | The page: the banner, on (with a message, 500 characters at most) and off; signed out → sign-in, nothing pushed | `controllers/project_maintenance_test.rb` | Authz, Contract |
| 6 | The flight board's MAINTENANCE chip | `controllers/projects_controller_test.rb` `test "a project in maintenance"` | Signals |
| 7 | A custom domain added to a project in maintenance (at sync) is routed too; one removed stops being routed | `integration/api_sync_test.rb` `test "maintenance follows the domains"` | Contract |
| 8 | API: on/off with a message, the project view's `maintenance` (by = the token's name); unknown project 404; the runner token 401; Cloudflare refusing → 502 with its words | `integration/api_v1_maintenance_test.rb` | Authz, Contract |
| 9 | CLI: `houston maintenance` shows; `on --message`/`off`; errors → exit 1; `status` shows it | `internal/cli` `TestMaintenance` | Contract |

## Decisions (yours)
1. ~~When a restore fails after the maintenance page is up~~. **Answered:** zero-downtime by default; a maintenance page is an option; after a failure with it, it stays up until an admin turns it off.
2. **A restore needs a linked repo:** the runner fetches the snapshot's commit to build or run it. A project that was only ever deployed by hand (`houston deploy` on the server) has to be linked first (`houston link`), and the Restore button says so. OK?
3. ~~One setting or two~~. **Answered:** no setting. The maintenance page is the admin's out-of-band switch, and deploys and restores never change it.

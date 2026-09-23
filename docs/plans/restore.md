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
Then: "This means style for the maintenance page should be customizable per project. Similar to how rails handles 404 pages. Sane, clean default with the ability to tweak."

So the page has **a clean default, and a project can bring its own from its repo** (like Rails' `public/404.html`), with `{{project}}` and `{{message}}` filled in.

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
  - **The page:** Houston's clean default, or the project's own (`x-houston.maintenance: public/maintenance.html`, read at sync, served as-is with `{{project}}` and `{{message}}` filled in, HTML-escaped). It can't use the app's own assets (the app may be down): self-contained, or a CDN. The project page previews it.
  - A restore takes its safety snapshot just before the switch either way. With the page up, nothing was written after it; without it, only the seconds between snapshot and switch.

- **The snapshot's commit is checked, three times** (your note: "we'd have to validate the SHA before restore. If for some reason the repo drifted or changed"). A commit's SHA is a hash of its contents, so the same SHA is the same code. Drift shows up as the commit being gone, never as different code.
  1. **When the restore is asked for:** Mission Control fetches that one commit from the linked repo (shallow, into a scratch directory, with the deploy key, as Add project's read does). If it's gone, the restore is refused before it's queued: "commit a07b2d1 isn't in <repo> any more (was history rewritten, or the repo relinked?)".
  2. **On the runner:** after the checkout, `git rev-parse HEAD` must be the snapshot's full 40-character SHA, or the restore is NO-GO before anything changes. This covers the branch fallback too.
  3. **Before the data goes back:** the manifest's `sha` must match the snapshot's `sha:` tag and the checked-out commit.
  - **Later (named, not now):** when the commit is truly gone but its image is still in Houston's registry, restore from the image and a compose.yml saved in the snapshot. That would need the compose file stored with each backup.

## Batches

1. **Maintenance mode, and the spike** (below).
2. **The page from the repo:** `x-houston.maintenance` (loaded and checked by the CLI), sent at sync and at Add project's read, stored per project, served with the placeholders filled, and the preview.
3. **Data generations:**
   - one naming function in Go (the Kamal config, `<SERVICE>_HOST`, the release hook's volumes) and one in Mission Control (placement, backups, container names claimed)
   - sync carries the generation
   - reserved `-g<n>` service names
   - generation 1 behaves exactly as today (the step 3–5 suites unchanged, plus tests at generation 2)
4. **The data engine:** Mission Control's `RestoreData` into a given generation (volumes, SQLite, Postgres into the new generation's Postgres once it's ready), from the runner's request, with the manifest checked before anything is touched (its project, its paths, and its `sha` against the snapshot's tag and the restore's commit).
5. **Asking for a restore:**
   - the restore kind on deploys, and its snapshot
   - the Restore button on each snapshot and the confirm page (the design's, noting whether the maintenance page is up), `/api/v1`, and `houston restore <snapshot> --confirm <name> [--follow]`
   - the preconditions: a linked repo, **the snapshot's commit still in it** (a shallow fetch by SHA), the snapshot in a location the project's backups used (the snapshot list reads all of them now), nothing queued or in flight
   - while a restore is queued or in flight: pushes don't queue deploys (the next check does), and backups wait (except its safety snapshot)
6. **The runner's restore:** the steps above in Go (the checkout verified against the snapshot's full SHA), the generation switch, cleanup, every failure path, and the deploy page's restore steps.
7. **The real run:** on OrbStack, restore the spike (Postgres, an NFS volume, SQLite) and equip, with and without the maintenance page up. It checks:
   - the zero-downtime restore (every request through it answered by one version or the other)
   - the data rolled back
   - a failed restore leaving the current version serving
   - the maintenance page (the default, and a project's own) staying up through a restore, a deploy, a failed one, and the app's containers stopped, until it's turned off
   - restoring the safety snapshot to go back
   - a restore whose image was pruned
   - a restore refused because its commit was force-pushed away

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
  - **Houston's default page** (a partial Batch 2's custom pages replace): clean and self-contained, with the project's name, "is down for maintenance", the message if any, the visitor's light or dark preference, and a meta refresh every 60 s. No Mission Control chrome, no Houston branding, no external assets.
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

## Batch 2: The page from the repo (titles until Batch 1 is green)
- **`x-houston.maintenance`:** a path relative to compose.yml, inside the repo. The file must exist, be at most 512 KB and be valid UTF-8; otherwise it's refused with the key path and why. `houston init` can write a starter page.
- **Sync and inspect** send its contents (`maintenance_page`); Mission Control stores them per project (absent → the default).
- **Serving:** `{{project}}` and `{{message}}` are replaced, HTML-escaped; nothing else is interpreted. 503, `Retry-After`, `no-store`.
- **Preview** on the project page: the page as it would be served, in a sandboxed iframe, without turning maintenance on.

## Decisions (yours)
1. ~~When a restore fails after the maintenance page is up~~. **Answered:** zero-downtime by default; a maintenance page is an option; after a failure with it, it stays up until an admin turns it off.
2. **A restore uses the project's linked repo.** Answered: "Every app will have a linked repo, so I assume it would use the same repo." The runner fetches the snapshot's commit from it with the project's deploy key, as a deploy does. A project without a repo (only ever deployed by hand) can't be restored until it's linked, and the Restore button says so. That's an edge, not the normal path.
3. ~~One setting or two~~. **Answered:** no setting. The maintenance page is the admin's out-of-band switch, and deploys and restores never change it.

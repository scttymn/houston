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
   - a restore refused because the project was relinked to a repo without its commit (a force-pushed commit stays fetchable: see the spike notes)

## Batch 1: Houston's maintenance page, and the spike

### Spike first (spec §14)
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

**Spike notes:**
- **git, run locally against a Forgejo 13 container:**
  - `git fetch --depth 1 --no-tags -- <repo> <old, non-tip sha>` works (protocol v2), and `checkout --detach` gives exactly that commit.
  - **A commit force-pushed away can still be fetched,** even after `git gc --prune=now` on the server, because the repository's reflog keeps it. So "the commit is gone" is rarer than feared. When a fetch by SHA succeeds, the code is exactly right: that's the point of checking the SHA.
  - The realistic drift is **a project relinked to a repo that never had the commit**, and that's what Batch 7 uses (not a force-push).
- **Cloudflare timing** is measured by Batch 1's own real stage (`install/test/maintenance-through-tunnel.sh`, a stage of `orbstack.sh`), with the real feature rather than a throwaway rule. It measures on and off, and a custom domain.
- **Kamal and the registry** (a second generation's accessory, `spike.g2_*` volumes, pulling a pruned image back): these shape Batches 3 and 6, not Batch 1. They're checked at the start of Batch 3, on OrbStack, before its code.

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

### Done (Batch 1)
- **Red:** Mission Control had 3 failures and 14 errors, all new; the Go CLI failed. **Green:** Mission Control 230 runs and the Go suite. The migration runs down and up. rubocop and gofmt are clean.
- **The real run** (`maintenance-through-tunnel.sh`, a stage of `orbstack.sh` on svnmns.com):
  - `houston maintenance on`, and Cloudflare routed the app's hostname to Houston's page with **no measurable delay** (0 s). Ten answers in a row settled within 12 s.
  - The page showed the name and the message. Mission Control's sign-in never answered on the app's hostname. The page stayed up with the app's containers stopped.
  - `off`, and the app was back, again at once.
- **Found on the way:**
  - **The race test didn't race at first.** Both toggles committed before either computed its rules, so "no lock" survived. The test now starts the second toggle only once the first push is on its way to Cloudflare. Without the lock it fails (twice in a row); with it, it passes (three times). A first version of that test deadlocked on its own signal, which was my mistake.
  - **CSRF on the page (from the review, red first):** production checks CSRF tokens and tests don't, so a POST to an app hostname would have got Rails' 422 page instead of the maintenance page. `skip_forgery_protection` fixes it: there's no session or state there. The test turns forgery protection on.
  - Setup's ingress comes from `TunnelRoutes` now; the first-run tests pass unchanged.
- **Mutations, each caught:**
  - no rollback when Cloudflare refuses
  - the push ignoring other projects
  - custom domains not routed
  - an app host not in maintenance reaching Mission Control
  - the message unescaped
  - sync never repushing
  - no lock (after the test fix)
- **Also fixed in the tunnel tests** (they had become flaky):
  - `grep -q` on `houston status` died of SIGPIPE under `pipefail` once status printed more lines.
  - The push stage now also settles `hooks/ping` from the machine, where Forgejo delivers from.
  - `deploy-through-tunnel.sh` uses the shared `settle` (its single-200 wait let a stale edge's 404 through).
- **Second full run:** 79 checks green, including the maintenance stage again. The one failure was that single-200 wait, fixed above.

## Batch 2: The page from the repo

### Design (short)
- **`x-houston.maintenance`:** a path relative to compose.yml's directory, e.g. `public/maintenance.html`. The loader (`internal/project`) reads it and refuses, with the key path and why:
  - a value that isn't a string
  - an absolute path, or one that leaves the directory (`..` after cleaning)
  - a file that's missing or not a regular file
  - one over 512 KB
  - one that isn't valid UTF-8
  The content is kept on the project (`Houston.MaintenancePage`).
- **Sync** sends `maintenance_page` (omitted when there's none). So does `houston inspect --json`, which feeds Add project's read.
  - Mission Control stores it on the project (`projects.maintenance_page`, text). Absent means the default; a string over 512 KB or not UTF-8 → 422.
  - A push that changes the file changes the page at the next deploy's sync.
- **Serving:** the project's page if it has one, else the default.
  - `{{project}}` and `{{message}}` are replaced, HTML-escaped (the message is empty without one). Nothing else is interpreted.
  - 503, `Retry-After: 60`, `Cache-Control: no-store`, as for the default.
- **Preview** (`GET /projects/:name/maintenance/preview`, admin): the page exactly as it would be served, with the message placeholder filled as "(your message)", answering 200.
  - It carries `Content-Security-Policy: sandbox`, and the project page shows it in `<iframe sandbox>`. A repo's page can have scripts, and they must never run on Mission Control's own origin, even if the preview is opened directly.
- **Named, not now:** `houston init` writing a starter page (a later init batch).

### AC ↔ test map (Batch 2)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The loader reads `maintenance` relative to compose.yml; refuses non-string, absolute, `../`, missing, a directory, >512 KB, invalid UTF-8, each naming `x-houston.maintenance` | `internal/project` `TestLoad_MaintenancePage` | Contract (hostile input) |
| 2 | Sync sends `maintenance_page`, omitted when there's none | `internal/mission` `TestRequestForData` | Contract |
| 3 | Mission Control stores it; absent → none (the default again); >512 KB or not a string → 422, nothing saved | `integration/api_sync_test.rb` `test "sync records the maintenance page"` | Contract |
| 4 | Serving the project's page with `{{project}}` and `{{message}}` filled and escaped (a `<script>` message stays text); 503 and the headers; the default without one | `integration/maintenance_page_test.rb` `test "a project's own page"` | Contract (hostile input) |
| 5 | Preview: admin only; `Content-Security-Policy: sandbox`; the project page's `<iframe sandbox>` (no `allow-scripts`, no `allow-same-origin`); the default shown without a page | `controllers/project_maintenance_test.rb` `test "previewing the page"` | Authz, Security |

### Done (Batch 2)
- **Red:** the Go packages failed to build; the Rails rows failed. **Green:** Mission Control 234 runs and the Go suite. The migration runs down and up. rubocop, gofmt and go vet are clean.
- **Found on the way, each red first:**
  - **The sync API capped bodies at 64 KiB,** so any page over about 60 KB would have made every deploy's sync fail with 413. JSON escapes `<`, `>` and `&` to six bytes each, so the largest page (512 KB of tags) is about 3 MB on the wire. The sync limit is 4 MiB now, with that worst case tested, and the old "too large" test now sends more than 4 MiB.
  - **HIGH (from the review): the loader followed symlinks out of the repo.** On a runner the checkout sits next to its deploy keys, and the page is served to anyone. Symlinks are still followed, but only to a file inside the repo: a link or a linked directory that leads outside is refused.
- **Mutations, each caught:**
  - Rails: the message unescaped in a project's page; no sandbox CSP on the preview; the page's size unchecked; the default always.
  - Go: `../` allowed; invalid UTF-8 allowed; symlinks followed out.
- **The real proof of a project's own page through the tunnel** is part of Batch 7's run.

## Batch 3: Data generations

### The Kamal checks (after the code, with the real thing)
Doing these by hand would mean rebuilding half of `houston deploy` (Kamal's secrets, its accessory boot). Instead, `install/test/generations-e2e.sh` runs an ordinary `houston deploy` on OrbStack with the project set to generation 2, and checks:
- accessory `db-g2` (container `spike-db-g2`, volume `spike.g2_pgdata`) boots while `spike-db` serves
- the app deployed with `DB_HOST=spike-db-g2` and volume `spike.g2_data` reaches it and mounts it
- `kamal accessory remove db` removes only generation 1's container
- the host's image of the first SHA removed, `docker pull 127.0.0.1:5000/spike:<sha>` gets it back, and whether `kamal deploy --skip-push` pulls it by itself

### Design (short)
- **The names** (one function in Go, `kamal.Names`, and one in Mission Control, `Generation`):

  | | generation 1 (today) | generation g ≥ 2 |
  |---|---|---|
  | app volume | `<name>_<volume>` | `<name>.g<g>_<volume>` |
  | accessory (Kamal) | `<service>` | `<service>-g<g>` |
  | its container, `<SERVICE>_HOST` | `<name>-<service>` | `<name>-<service>-g<g>` |
  | accessory volume | `<name>_<volume>` | `<name>.g<g>_<volume>` |
  | location directory | `volumes/<name>/<volume>` | `volumes/<name>.g<g>/<volume>` |

- **`projects.data_generation`** (default 1). Sync's result carries it, and `houston deploy` writes the Kamal config for it: `kamal.Target.Generation`, the release hook's volumes, and the changed-accessory reboot's names.
- **Mission Control** uses the project's generation in `VolumePlacement` (it takes a generation), `Backup` (which volumes and containers it reads), and `Project#host_names`.
  - **Container names are claimed per generation.** A project named `equip-db-g2` can't take a name generation 2 of `equip` needs, and the other way round. Batch 6 claims g+1's names when a restore allocates it.
- **The loader reserves** service names ending in `-g<digits>`: "reserved for Houston's data generations".
- **Generation 1 is unchanged:** the golden Kamal configs are byte-identical, and the step 3–5 suites pass untouched.

### AC ↔ test map (Batch 3)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Generation 1's Kamal configs are byte-identical to today's (the phoenix and rails goldens); generation 2's golden: `phoenixapp.g2_media`, accessory `db-g2`, `DB_HOST=phoenixapp-db-g2`, `phoenixapp.g2_pgdata` | `internal/kamal` `TestGenerate*` (the golden files) | Contract, Migrate |
| 2 | The release hook's volumes and the changed-accessory reboot use the generation's names | `internal/deploy` `TestDeployGeneration2` | Contract |
| 3 | Sync's result carries the generation, and the deploy uses it | `internal/mission` client test, `internal/deploy` | Contract |
| 4 | A service named `db-g2` → refused, naming the reservation | `internal/project` `TestLoad_ReservedGenerationNames` | Contract |
| 5 | Mission Control: generation 1 names as today; generation 2's for placement (the volume and its directory), backups (mounts, the Postgres container) and claimed container names | `models/generation_test.rb`, `volume_placement_test.rb`, `backup_test.rb` | Contract |
| 6 | A project named `equip-db-g2` and project `equip`'s generation 2 can't both claim `equip-db-g2` | `models/project_host_test.rb` | Concurrency |
| 7 | The migration: every existing project is generation 1 | `models/project_test.rb` | Migrate |

### Done (Batch 3)
- **Red:** Go tests failed and didn't compile; Mission Control had 1 failure and 4 errors in the new tests. **Green:** Mission Control 240 runs and the Go suite. Both migrations run down and up. rubocop, gofmt and go vet are clean.
- **Generation 1 is unchanged:** the phoenix, rails and spike goldens pass untouched. Generation 2's golden (`phoenix.g2.deploy.yml`) was generated and then reviewed line by line against generation 1's: the only differences are the names, and db's config hash (its volume changed).
- **The real check** (`install/test/generations-e2e.sh`, OrbStack): **GENERATIONS PASS.**
  - Generation 2's Postgres (`spike-db-g2`, volume `spike.g2_pgdata`) runs beside generation 1's.
  - The app's `DB_HOST` is `spike-db-g2` and resolves on the kamal network. It mounts `spike.g2_data`.
  - Removing generation 1's Postgres leaves the app serving.
  - An image removed from the host pulls back from Houston's registry.
- **Found by the real check (red first):** the first run's deploy at generation 2 went NO-GO. Its pre-deploy snapshot read the *project's* generation (2, not built yet). **A snapshot must read the serving generation**, so each deploy records the generation it ran on (`deploys.generation`, set at start and at claim), and backups read the running deploy's. That's what Batch 6's safety snapshot needs: taken while generation g+1 is being built, it has to capture g.
- **Mutations, each caught:**
  - Go: volumes ignoring the generation; the deploy ignoring sync's generation; service hosts ignoring it; no reserved names.
  - Rails: placement ignoring the generation; backups reading generation 1; claims ignoring the generation; sync not saying it.
- **Not checked, and not needed:** whether `kamal deploy --skip-push` pulls a missing image by itself. Batch 6's runner pulls it explicitly before the switch.

## Batch 4: The data engine

### Design (short)
- **`DataRun`,** a base class shared with `Backup`: the deadline (each command gets what's left of 3 hours; exit 124 stops the run), the heartbeat thread, cleanup that never raises, the guarded finish, and the output's tail. `Backup` becomes a `DataRun`; its tests pass unchanged.
- **`deploys`** gains `kind` (`deploy` | `restore`), `source_snapshot_id` and `source_location_id` (what a restore reads). A restore's `generation` is the one it builds, g+1. Batch 5 fills these from the request; here they're set directly.
- **`backup_runs`** gains `operation` (`backup` | `restore`) and `source_snapshot_id`, with one restore run per restore deploy (a unique index on the deploy number, operation restore). `BackupJob` runs `RestoreData` for a restore, on the `snapshots` queue (a runner waits on it). One running run per project still holds, so a restore never overlaps a backup of that project.
- **Runner API** (the restore deploy's token, in flight, kind restore): `POST /api/deploys/:id/restore_data` → 202 with the run (a retry gets the same run); `GET` → the run.
- **`RestoreData`** into generation g+1 (the runner has booted g+1's accessories first, Batch 6):
  1. **Place** g+1's app volumes (`VolumePlacement` for that generation; idempotent).
  2. **Clean up** leftovers (exact names `houston-restore.<name>.<role>`); recreate staging `houston-restore.<name>`.
  3. `restic restore <snapshot> --target /restore` into staging, with the source location's repository and the cache.
  4. **Read and check the manifest** (`/restore/out/houston.json`):
     - its project is this one
     - its `sha` is the restore's commit
     - every file is in Houston's own form (`postgres/<service>/<n>.dump`, `postgres/<service>/globals.sql`, `sqlite/<n>.sqlite3`)
     - every SQLite path is relative, with no `..`
     - every volume is one the project has
     Otherwise it's NO-GO, before any volume or database is touched.
  5. **Each volume** (a root helper with g+1's volume at `/v`, staging read-only): empty it (`find /v -mindepth 1 -delete`), copy the snapshot's files (`cp -a`), then put each SQLite copy at its path and remove any `-wal`, `-shm` and `-journal`. Names and paths go as argv.
  6. **Each Postgres service** (g+1's container):
     - wait for `pg_isready` (up to 60 s)
     - roles from `globals.sql` (psql without stop-on-error: roles that exist are fine)
     - then each database: `dropdb --force --if-exists` and `createdb`, with `--maintenance-db=template1` and the name after `--`, then the dump piped into `pg_restore --no-owner --role=<user> -d "dbname='<escaped>'"`
  7. Remove staging (always). GO with what was restored; NO-GO with the step and the tail.

### AC ↔ test map (Batch 4)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The commands in order: g+1's placement, cleanup, staging, restic restore (env-only secrets, cache, repo), the manifest read, per volume empty + copy + SQLite into place with -wal/-shm/-journal removed, pg_isready, roles, drop/create/pg_restore per database (the hostile name as argv and conninfo), staging removed; GO with what was restored | `models/restore_data_test.rb` `test "a restore, step by step"` | Contract |
| 2 | A bad manifest (missing, not JSON, another project, another SHA, a file outside Houston's form, `..` in a SQLite path, an unknown volume) → NO-GO, and nothing emptied or dropped | `test "the manifest is checked before anything is touched"` | Contract (hostile input) |
| 3 | restic failing, a copy failing, pg_restore failing → NO-GO naming the step; staging removed | `test "a failing step stops the restore and cleans up"` | Crash & repair, Signals |
| 4 | The runner API: 202 and one run per restore deploy (a retry gets it); a wrong token 403, not in flight 409, not a restore 422, a personal token 401, through Cloudflare 404 | `integration/api_restore_data_test.rb` | Authz, At-least-once |
| 5 | A second delivery → `restic restore` once; the job is on `snapshots` | `jobs/backup_job_test.rb` | At-least-once |
| 6 | `Backup` unchanged as a `DataRun` (its suite green); the migrations: `operation` backup, `kind` deploy for every existing row | the existing suites, `models/deploy_test.rb` | Migrate |

### Done (Batch 4)
- **Red:** Mission Control had 7 errors in the new tests. **Green:** Mission Control 248 runs and the Go suite. The migration runs down and up. rubocop is clean.
- **`DataRun`** now holds what `Backup` and `RestoreData` share. `Backup`'s suites passed unchanged after the move, including the heartbeat and unexpected-error tests.
- **Found on the way:**
  - **The superuser's password (design, before code):** restoring a snapshot's roles also restores the superuser's old password hash. If `POSTGRES_PASSWORD` was rotated since, the app couldn't connect after the switch. The restore sets it back to the container's own `POSTGRES_PASSWORD`, through psql's `:'pw'` quoting on stdin.
  - **Never into the serving generation (from the review, red first):** pointed at the serving generation by a wrong deploy row, `RestoreData` went GO. It would have emptied the live volumes and dropped the live databases. It now refuses before anything, as defense in depth for Batch 6.
  - A test assertion that could never fail (it concatenated the constant it checked) was fixed.
- **A real smoke test** against `postgres:17` and a real shell, with the exact command strings:
  - the database `we'ird=db` dropped and recreated, and restored from its dump (the row written after the dump is gone)
  - the roles applied, and the superuser's hostile password (`it's"pw$x`) still logging in after the password step
  - the fill script emptying the volume, copying the files, and putting the SQLite file at a path with a space, with its stale `-wal` removed
  - (My first attempt at the smoke test was broken by zsh's 1-based arrays; the second ran it as a bash script.)
- **Mutations, each caught:**
  - another commit's snapshot accepted
  - any staged file accepted
  - SQLite paths allowed to climb
  - restoring into the serving generation
  - the password not kept
  - restore data for a plain deploy
  - the job always backing up
- **For Batch 6:** the safety snapshot is reason `restore` (spec §9), so it needs its own one-per-restore guarantee. The existing unique index covers reason `deploy` only.

## Decisions (yours)
1. ~~When a restore fails after the maintenance page is up~~. **Answered:** zero-downtime by default; a maintenance page is an option; after a failure with it, it stays up until an admin turns it off.
2. **A restore uses the project's linked repo.** Answered: "Every app will have a linked repo, so I assume it would use the same repo." The runner fetches the snapshot's commit from it with the project's deploy key, as a deploy does. A project without a repo (only ever deployed by hand) can't be restored until it's linked, and the Restore button says so. That's an edge, not the normal path.
3. ~~One setting or two~~. **Answered:** no setting. The maintenance page is the admin's out-of-band switch, and deploys and restores never change it.

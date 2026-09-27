# Plan: Copy a project to a new name (how an app is renamed)

Spec and design: neither has it. It builds on:
- docs/plans/delete-project.md: the old app is removed with Delete, when the admin is happy
- docs/plans/restore.md: its data engine restores a snapshot into another project's volumes
- docs/plans/add-project-drafts.md: linking a project

## Your direction
The migration pattern: the old app (Rails) serves at `X` while its rewrite deploys beside it as `X-go`. When the rewrite is ready, `X` is deleted, and `X-go` should become `X`. The first real use is `valleybuiltcrossfit-go` → `valleybuiltcrossfit`:
- one app service and one `data` volume (SQLite and uploads)
- its custom domain `valleybuiltcrossfit.svnmns.com` is the new name's default host
- its GitHub repo is already renamed (`git ls-remote` on the old and new URLs gave the same `caa66a1`: GitHub forwards the old URL)

How the decision was reached:
- "The goal is as little friction as possible."
- On a rename carried out by a push: "this shouldn't be 'magical' … no ambiguity, no guessing."
- Then: "What if changing name and pushing initiated a 'copy'. Everything old is untouched, but the new gets setup as a new app. Then you could simply delete the old."
- "No maintenance, no downtime."
- On data written to the old app after the copy: "I think we allow it if the admin doesn't turn on maintenance mode. No magic."

So:
- **A new name is a new app.** Pushing a `compose.yml` whose `name:` is new *proposes* a copy. The admin clicks to make it. Nothing happens by itself.
- **The old app is untouched,** apart from the shared hosts it hands over. It keeps its data, history and backups until the admin deletes it with Delete.
- **No downtime:** the new app boots on a copy of the data and passes its health check before the hosts move to it.
- **No magic:** Houston never turns the maintenance page on or off. Anything written to the old app after the copy stays in the old app, unless the admin put its maintenance page up first.

## The flow
1. Change `name:` in `compose.yml` and push. (Optionally, put the old app's maintenance page up first, so nothing is written to it after the copy.)
2. The old app's deploy goes to HOLD, not NO-GO: "compose.yml names valleybuiltcrossfit: copy valleybuiltcrossfit-go to it". The old version keeps serving. On the board and the project page, one button: **Copy to valleybuiltcrossfit**. (CLI: `houston copy --confirm valleybuiltcrossfit-go`, run in the repo; the new name is read from `compose.yml`.)
3. The copy runs, with no downtime, and ends GO with the new app serving the shared hosts.
4. When happy, **Delete** the old app. If its maintenance page was up, that's when it comes down.

## Aborting
Your question: "So basically if I push a duplicate named app, it goes on hold. Then I can initiate the copy. What if I abort?"
1. **On HOLD, before Copy:** nothing has been made. Change `name:` back and push: the old app's next deploy goes through as usual. Until then, each push to the branch lands on the same HOLD.
2. **During the copy, before the handover:** **Cancel** (the copy's page, `houston copy --cancel`). It does what a failure at that stage does: the new app and everything made for it are removed, and the old app never stopped serving.
3. **After the copy is GO:** **Undo copy** on the new app, while the old app still exists.
   - The shared hosts go back to the old app, with the same no-gap handover in reverse (kamal-proxy and the DNS records' comment).
   - Then the new app is deleted.
   - Anything written to the new app since the handover goes with it, unless its maintenance page was up. It's the mirror of the forward case, and the admin's call.
4. **After the old app is deleted:** there's nothing to go back to; the new app is the app.
- **A new name that belongs to an existing project** is a refusal, not a proposal: the HOLD says so, and there's no Copy button.

## Evidence (what's keyed by the name)
- **The name is compose's `name:`.** A sync finds the project by it (`project_sync.rb:40`). A runner's sync must name the claimed deploy's project, or it's refused with "compose.yml names …, but deploy #n is for …" (`api/projects_controller.rb`, `claim_matches?`), which is a NO-GO today.
- **Docker names:**
  - containers `<name>-web-<sha>` (label `service=<name>`) and `<name>-<svc>` (`kamal.go:58-91`, `:250-262`)
  - volumes and folders `<name>[.g<n>]_<vol>` and `volumes/<name>/…` (`generation.rb:13-19`)
  - kamal-proxy service `<name>-web` (`kamal.go:318-328`)
- **Other names:**
  - registry `127.0.0.1:5000/<name>:<sha>` (`deploy.go:198`)
  - default host `<name>.<base>`
  - DNS comment `project:<name>` (`domain_dns.rb:15`)
  - webhook `hooks.<base>/<name>` (`webhooks_controller.rb:25`)
  - restic tag `project:<name>` (`backup.rb:92`)
- **A consistent copy of live data exists already:** a backup reads a running app (`sqlite3 .backup`, `pg_dump`, files) and a restore writes a snapshot into a generation's volumes (`restore_data.rb`). A copy is a snapshot of the old app restored into the new app's generation 1.
- **A shared host can't be both apps':**
  - kamal-proxy refuses one host for two services
  - `DomainDns#point` refuses a record commented for another project: "belongs to project …" (`domain_dns.rb:26-29`)
- **Linking a project to its repo copies nothing but the link** (`project_linking.rb`). Secrets are write-only everywhere but inside Mission Control.

## Design (short)
- **`ProjectCopy`** (a table): `from_project_id`, `project_id` (the new one, once made), `from`, `to`, `sha`, `by`, `status`, `step`, `log`, `error`, timestamps. It's a runner job of kind `copy` on the *new* project, numbered as its deploy #1.
- **Proposing (the push):** a claimed deploy whose `compose.yml` names another project:
  - is finished HOLD (not NO-GO), with the name it found and the SHA
  - if that name is free and valid, the old project shows "compose.yml names NEW: Copy to NEW"
  - if it's taken, it says so ("NEW is another project; …")
  - nothing else happens
- **`ProjectCopy.request!(from, confirm:, by:)`:**
  - `confirm == from.name`
  - the branch head's `compose.yml` (read, as Add project does) names a free, valid NEW
  - nothing of the old project's in flight; no copy to NEW already under way
  - makes the new project in one transaction:
    - `ProjectSync` of the branch head's `compose.yml`, with its container-name claims
    - the same repo link, deploy key and branch
    - the old project's webhook secret (as built: the git host keeps sending it, so the alias below keeps verifying with nothing changed there)
    - the old project's secrets (the values copied inside Mission Control), volume placement and backup target
  - then queues the copy
- **The copy (runner and Mission Control, as a restore):**
  1. **build** (runner): fetch the SHA, build and push `127.0.0.1:5000/NEW:<sha>`.
  2. **volumes** (Mission Control): NEW's volumes are placed where the old app's are (`VolumePlacement`).
  3. **data** (Mission Control), taken as late as possible:
     - a snapshot of the old app, kind `deploy`, reason `copy`, tagged for the old name (its backups are the old app's)
     - restored into NEW's generation-1 volumes and accessories: files, SQLite, Postgres into NEW's Postgres once it's up
     - the old app keeps serving throughout
  4. **deploy** (runner): `kamal accessory boot` for NEW's accessories, then `kamal deploy --skip-push` with NEW's hosts, minus the shared ones. The health check passes on NEW's container.
  5. **handover** (Mission Control): the shared hosts move from `OLD-web` to `NEW-web` with no failed request (spike notes). In one `sh -c` in the kamal-proxy container, each service's current target and Kamal's options are read from `kamal-proxy list` and its state:
     1. a catch-all `houston-handover` to NEW's container
     2. `OLD-web` redeployed without the shared hosts
     3. `NEW-web` redeployed with them
     4. the catch-all removed

     Their DNS records are then re-commented `project:NEW` (same content, no DNS change). From here the new app serves them; the old one keeps only the hosts still its own.
  6. **GO:** the new project's deploy #1.
  - **A failure in 1–4** → NO-GO. The new project is removed (with the delete steps, for its name only), and the old app is untouched, serving everything.
  - **A failure in 5** → NO-GO, with each host where it was before (the handover is per host, and reversed on failure).
- **After the copy:**
  - The old project's page says "copied to NEW (#n); delete it when you're happy", with Delete.
  - Deleting the old project removes only what's still its own. The shared hosts' routes and DNS records are the new app's now (comment `project:NEW`), so the delete's exact-comment rule leaves them alone.
  - Its webhook URL `hooks.<base>/OLD` answers for the new project once the old one is deleted. The copy stores the alias, and it yields if a project named OLD is made later.
  - With the old app's maintenance page up, the shared hosts still show the page after the handover (the tunnel sends them to Mission Control before kamal-proxy) until the admin turns it off or deletes the old app. That's the admin's cutover, by their own switch.
- **Cancel and Undo copy:**
  - **Cancel** sets the copy to cancelling. The engine stops at its next step and removes the new project.
  - **Undo copy** is a job: the reverse handover per shared host, then the new project's deletion (its backups kept with a final snapshot, as any delete).
- **UI:**
  - the HOLD notice and **Copy to NEW** button (board and project page)
  - a confirm page in the Restore dialog's style
  - the copy's page (its steps and log, with **Cancel** while it runs)
  - "copied to NEW" on the old project
  - **Undo copy** on the new project while the old one exists
- **API and CLI:**
  - `POST /api/v1/projects/:name/copy {confirm}` → 202 (the new name comes from the branch's `compose.yml`), and `GET /api/v1/copies/:id`
  - `houston copy --confirm OLD [--follow]`
  - `houston status` shows the proposal

## Batches
1. **Spike** (OrbStack, the spike fixture):
   - kamal-proxy handing a host from one service to another: a poller at 5 requests a second counts any non-200 in the handover; try `kamal-proxy deploy` of the new service with the host after removing it from the old, and in one command if it can
   - re-commenting a Cloudflare record without changing it
   - restoring a live snapshot of spike into another project's generation-1 volumes and Postgres
2. **Proposing:** a claimed deploy naming another project → HOLD with the name (instead of NO-GO), and the proposal on the board and page.
3. **The model:** `ProjectCopy.request!` and its refusals, the new project made in one transaction (secrets, placement, target, link; its own webhook secret), the webhook alias.
4. **The engine:** the steps above (build and deploy on the runner, data and handover in Mission Control), every failure path, and the old app untouched on each one.
5. **UI, API, CLI, docs:** checked at desktop and 375 px.
6. **The real run** (`install/test/copy-project.sh`): spike-go (Postgres, NFS, SQLite; domain `spike.houston.test`) copied to spike, with a poller at 5 requests a second on the shared host throughout, and every request 200. Then:
   - the data is there
   - the old app is untouched on its own host
   - Delete the old app leaves the new one serving and its DNS
   - the old webhook URL deploys the new one
   - a failed copy leaves the old app serving
7. **Release.** Then valleybuiltcrossfit-go → valleybuiltcrossfit, with you.

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | A deploy whose compose.yml names another project → HOLD with the name, old version serving; the proposal shown only for a free, valid name | `integration/api_sync_test.rb`, `controllers/projects_controller_test.rb` | Contract |
| 2 | `request!`: the wrong name typed, the branch not naming a free valid NEW, something in flight → refused, nothing made | `models/project_copy_test.rb` | Preconditions |
| 3 | The new project: NEW's compose and claims, the same link and key, its own webhook secret, the old secrets' values, placement and target; the old project unchanged | same | Contract, Blast radius |
| 4 | The copy's steps in order; the snapshot taken as late as possible; NEW's data restored into its generation 1 | `jobs/project_copy_job_test.rb`, `internal/deploy/copy_test.go` | Contract |
| 5 | The handover: kamal-proxy and DNS per shared host, reversed on failure; the old app keeps its own hosts | same | Atomicity |
| 6 | A failure before the handover: the new project removed, the old untouched | same | Fail closed |
| 7 | Deleting the old app after a copy leaves the handed-over routes and records | `jobs/delete_project_job_test.rb` | Blast radius |
| 8 | `hooks.<base>/OLD` reaches NEW once OLD is gone, and yields to a later project named OLD | `integration/webhooks_test.rb` | Contract |
| 9 | UI, API and CLI, as for deleting | controller, integration and CLI tests | Authz, Contract |
| 10 | Cancel before the handover removes the new project, with the old serving throughout; Undo copy hands the hosts back with no gap, then deletes the new project; neither is offered once the old project is gone | `models/project_copy_test.rb`, `jobs/project_copy_job_test.rb` | Atomicity, Idempotency |
| 11 | On a real server: every request 200 through the copy, the handover, and an Undo copy's handover back; the data copied; the old app untouched until deleted | `install/test/copy-project.sh` | End to end |

## Spike notes
(Batch 1, OrbStack: spike and spike-x deployed. A poller in a container on the `kamal` network asked for `spike-alias.houston.test/env/DB_HOST` as fast as curl could, about 400 a second, and each answer said which app served it: `spike-db` or `spike-x-db`.)
- **One host can't be two services':** `kamal-proxy deploy spike-x-web … --host spike-alias.houston.test` while spike-web has it → `Error: host settings conflict with another service`. So a handover must take the host from one before giving it to the other.
- **Kamal's options for a service** are in kamal-proxy's state (`~/.config/kamal-proxy/kamal-proxy.state`): `--health-check-path /up --buffer-requests --buffer-responses --log-request-header Cache-Control --log-request-header Last-Modified --log-request-header User-Agent`. A handover redeploys both services with their current target and these options, changing only the hosts.
- **Never `--force`:** five unchanged redeploys with `--force` (it skips the health check) → 3 × 503 in 4,705 requests. Without it → 0 in 4,746.
- **Take, then give, in one `sh -c` in the kamal-proxy container:** 55–61 ms. Five handovers → exactly one 404 each, in about 2,400 requests: the moment the host belongs to nobody.
- **With a temporary catch-all:**
  1. `kamal-proxy deploy houston-handover --target <new container>` (no `--host`: it serves any host no service claims)
  2. take the host from the old service
  3. give it to the new one
  4. `kamal-proxy remove houston-handover`

  Five handovers, back and forth (55–77 ms each) → **0 failures in about 12,000 requests**, each answered by the old app until the move and the new one after. For those milliseconds, a request for a host nobody serves reaches the new app instead of a 404. That's harmless, and noted.
  - The name `houston-handover` can't be a project's service: those are all `<project>-web`.
- **Found on the way:** on a fresh server, the first two deploys at once can fail. Both start kamal-proxy, and one gets `Conflict. The container name "/kamal-proxy" is already in use`. It's not this feature's; it's filed as its own task.
- **Not spiked, because they're code-level:** re-commenting a Cloudflare record (a PATCH of `comment`), and restoring a snapshot of one project into another's generation 1 (`RestoreData` names volumes from its run's project; the manifest check must accept the source project's name for a copy).

## As built (differences from the design above)
- **The webhook secret is inherited, not new** (see the design line).
- **A failed or cancelled copy's cleanup is a job (`CopyCleanupJob`) that retries.** A delete is refused while the new project's data is still being restored, which is exactly when someone presses Cancel.
- **Cancel and the handover take the copy's row lock,** so they never cross. The handover endpoint re-checks, inside the lock, that its deploy is still in flight.
- **API:**
  - `POST /api/v1/projects/:old/copy {confirm}`
  - `POST /api/v1/projects/:new/copy/cancel`
  - `POST /api/v1/projects/:new/copy/undo {confirm}` (the new name)
  - `copy_proposal` on the project's JSON

  There's no `GET /api/v1/copies/:id`: `houston copy --follow` follows the new project's deploy #1.
- **CLI:**
  - `houston copy --confirm OLD [--follow]` (the project is `--confirm`'s unless `--project` says otherwise)
  - `houston copy --cancel` and `houston copy --undo --confirm NEW` (the project is the compose file's)
  - `houston status` shows `hold      compose.yml names NEW (#n): houston copy --confirm OLD`
- **Found by the visual check:** `Project#hostnames` listed a host twice when a custom domain was also the default host, which is exactly a copy's case (`valleybuiltcrossfit.svnmns.com`). It's now unique (`test/models/project_test.rb`).

## Evidence
- **Failing first** (the reason each time):
  - the runner: `mission.Progress` had no `ProposedName`
  - the proposal page: no `new_project_copy_path`
  - `ProjectCopy`: an uninitialized constant
  - the webhook alias: no job enqueued
  - `RestoreData`: "refusing to restore into generation 1"
  - the Go copy flow: `mission.CopyJob` and `Target.ExcludeHosts` undefined
  - the CLI: `unknown flag: --confirm`
  - `ProjectCopy#cancel!` and `#undo!`: undefined
  - hostnames: listed twice
- **Mutation checks** (each change made, the test failing, then restored):

  | Broken on purpose | Caught by |
  |---|---|
  | No catch-all in the handover | 2 handover tests |
  | A failed copy's new project removed even after its handover | `api_copies_test.rb` |
  | Secrets not copied | `"asking for a copy makes the new project…"` |
  | Any manifest's project accepted | 2 restore-data tests |
  | No webhook alias | `webhooks_controller_test.rb` |
  | A push swapping the queued copy's commit | `"while the copy is under way…"` |
  | The DNS records not moved | 3 handover tests |
  | Cancel after the handover allowed | `"cancelling before the handover…"` |

  Earlier, for the runner: the security test for a compose naming another project now expects HOLD, and still checks nothing is synced or built.
- **Suites:**
  - `bin/go test ./...`: all packages ok. `gofmt -l internal cmd`: nothing.
  - `bin/rails test && bin/rubocop`: 495 runs, 0 failures (before the hostnames test); 321 files, no offenses.
- **Suites after the hostnames fix:** 496 runs, 0 failures; 322 files, no offenses; Go ok; gofmt clean.
- **`install/test/copy-project.sh`: PASS**, every check ok. A poller asked spike.houston.test about 7 times a second throughout, and each answer said which app's Postgres served it:
  - **The push:** deploy #2 held with "compose.yml names spike", `houston status` showed the proposal, and spike-go kept serving.
  - **The copy:** `houston copy --confirm spike-go --follow` exited 0. **All 190 requests answered 200** (167 by spike-go, then 23 by spike), and the last was spike's.
    - Postgres and SQLite said 'kept' in spike, with the SQLite file in `volumes/spike/data` on vm-nfs.
    - spike-go kept its own host and data.
    - kamal-proxy was left with no catch-all or placeholder.
  - **Undo:** exit 0. **All 96 requests answered 200** (spike, then spike-go), spike was deleted, and the copy was made again.
  - **Deleting spike-go:** **all 75 requests answered 200** (spike), spike-go's host is no longer served, and `hooks.houston.test/spike-go` rang spike (202).
  - **Not in the real run:** a copy failing before its handover (the new project removed, the old one serving). That's covered by `api_copies_test.rb`.
  - **Found on the way:** the first run's log check failed because the log read "handed over: spike.[secret KAMAL_REGISTRY_PASSWORD].test". The local registry's placeholder password is "houston", and the runner masks every occurrence in every deploy log. It's not this feature's: it's filed as its own task, and the run checks the handover in the database.
- **Visual check** (sample projects on a dev server, since removed): the proposal notice, the confirm page, the copy's deploy page with Cancel, and both projects' notices with Undo in the Danger zone, all at 375 px with no overflow. The confirm page's long "<NEW> GETS" label ran into its text, so it's now "GETS".

## After v0.4.12: the first real copy (valleybuiltcrossfit-go → valleybuiltcrossfit)
- **What happened:** the copy's deploy went NO-GO at its health check: `web: attempt to write a readonly database (8)`, over and over, then "target failed to become healthy within configured timeout (30s)". valleybuiltcrossfit-go kept serving, and the new project was removed, as designed.
- **Why:** the Go app runs as 65532 (`COPY --chown=65532:65532 /out/data /data`). A backup keeps each SQLite database as a `.backup` copy, written by a root helper, and leaves the live file out of the file copy. So a restore put the database back **root-owned, mode 644**: readable, but not writable by the app.
  - This was never a copy bug alone: a restore of any SQLite app that isn't root had it.
  - The real runs missed it because the spike fixture runs as root.
- **The fix:**
  - `lib/backup/sqlite.rb` records each database's `uid`, `gid` and `mode` in the manifest.
  - `RestoreData`'s `FILL` puts them back. Only numbers are accepted (anything else is `-`). An older snapshot without them gives the database its folder's owner (`chown --reference`).
  - Tests: `test/lib/backup_sqlite_script_test.rb` `"records each database's owner and permissions"`; `test/models/restore_data_test.rb` `"FILL puts databases back with their owner and permissions"` (FILL run for real, as root with GNU coreutils) and `"a manifest's owner and permissions are used only when they're numbers"`.
  - `install/test/copy-project.sh` now makes spike-go's database 65532's, mode 640, before the copy, and checks the copy's is the same.
- **Also found: the failed copy's log was lost.** Its new project, and so its deploys, were deleted, and the cause had to be read from the runner's container log. Now the copy keeps its deploy's log (`project_copies.log`), and the old project's page shows "The copy to X failed: …" with the log's last 60 lines, for a day (until a copy succeeds).
- **Mutation checks, each caught:** FILL never chowning; the manifest's owner ignored; no owner recorded; the log not kept.
- **Suites:** 500 runs, 0 failures; 323 files, no offenses.
- **`install/test/copy-project.sh`: PASS** (31 ok):
  - "the copied database is still 65532's, mode 640"
  - every request 200 through the copy (301), the undo (95) and the delete (75)
- **Then: the new project's page was a 500** (`undefined method 'first' for nil` in `_backup_run`). The page, the board and the API took a project's latest backup run as its last backup, and a copied project's only run is its data's restore: GO, with no snapshot of its own. Only runs that make a snapshot count now (`BackupRun.backups`). Test: `"a copied project whose only run is its data's restore"`, which reproduced the 500 first. Suites: 501 runs, 0 failures; no offenses.


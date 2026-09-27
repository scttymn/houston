# Plan: Delete a project

Spec and design: neither has it. This plan follows the design's Restore dialog (a typed-name confirm over the dimmed project page), as you chose.

## Your direction
"We need a way to delete an app. Similar to how github allows you to delete a repo. Choose to delete, enter the name to confirm, etc."

Then, on the choices:
- **Backups:** "Final snapshot, keep backups." Houston takes one last snapshot, then deletes everything except the backups. Re-adding a project with the same name can restore it. A checkbox on the confirm page, off by default, deletes the backups too.
- **No grace period:** "Delete right away." There's no soft delete and no restore window. The kept backups are the way back.
- **Look:** "Follow the Restore dialog." A Danger zone panel ends the project page, and the confirm page reuses the Restore dialog's style.

## Goal
One action removes a project and everything Houston made for it: its app and accessory containers, volumes and their folders on storage locations, DNS records, tunnel routes, runner checkouts, local images and its rows. It keeps its backups, plus a final snapshot, unless you ask for those gone too. The action is:
- **typed-name confirmed,** in the UI and the CLI
- **fail closed:** every check, and the final snapshot, happen before anything is removed; if any of them fails, the app keeps serving, untouched
- **resumable:** once removal has started, a failure leaves the project DELETING (never half-serving), and asking again finishes the job

## Evidence (what's there today)
- **Nothing deletes a project.** There's no `destroy` route (`config/routes.rb:66`, `:92` are `index`/`show` only) and no `Project#destroy` call. The associations are `dependent: :delete_all` (`app/models/project.rb:12-16`). The `backup_runs` and `project_volumes` foreign keys don't cascade (`db/schema.rb:220`, `:224`), so it must be `project.destroy`, never SQL.
- **What an app owns, and who makes it:**

  | Resource | Created at |
  |---|---|
  | Container-name claims | `project_sync.rb:111-115`, `deploy.rb:134-141` |
  | App containers | label `service=<name>`, `deploy.go:698-701`, `kamal.go:250-262` |
  | Accessories `<name>-<svc>[-g<n>]` | `generation.rb:16`, `kamal.go:58-91` |
  | Named volumes `<name>[.g<n>]_<vol>` | `volume_placement.rb:16-35` |
  | Volume folders `volumes/<name>[.g<n>]/<vol>` on NFS and local-path locations | `generation.rb:19`, `volume_placement.rb:53-64` |
  | Images `127.0.0.1:5000/<name>:<sha>`, label `service=<name>` | `deploy.go:198`, `:511-514` |
  | Runner checkouts `/var/lib/houston/runners/houston-runner-N/<name>` | `runner.go:121-169` |
  | DNS records commented `managed-by:houston project:<name>` | `project_sync.rb:88`, `domain_dns.rb:15` |
  | Maintenance routes | `tunnel_routes.rb:8-18` |
  | kamal-proxy's route for `<name>-web` | `kamal.go:318-328` |
  | Restic snapshots tagged `project:<name>` | `backup.rb:92` |

- **Removal code to reuse:**
  - containers and volumes, by exact name, with NFS volumes emptied first: `internal/deploy/restore.go:254-351`
  - a DNS record, only when its comment matches: `domain_dns.rb:42-51`
  - the tunnel rebuilt from the database: `TunnelRoutes.push!`
- **The typed-name pattern:**
  - `Deploy.request_restore!` refuses unless `confirm == project.name` (`deploy.rb:119`)
  - the page is `project_restores/new.html.erb:51-63`
  - the CLI is `houston restore … --confirm NAME` (`cli.go:364-376`, `backups.go:298-306`)
- **Hazards found while tracing, which this plan closes:**
  - A deleted project that's in maintenance keeps its hostnames routed to Mission Control. `AppHost` then stops matching (`app_host.rb:11`), so those hosts get Mission Control's own routes, including the sign-in page.
  - `houston deploy` (a hand run) syncs with `find_or_initialize_by` (`project_sync.rb:40`), so it could recreate a project that's being deleted.
  - A queued `BackupJob` holds a GlobalID. With `discard_on DeserializationError` off (`application_job.rb:6`), deleting its run makes that job error.
  - Kept snapshots show up for a later project with the same name only when its backup location is the one holding them (`snapshots.rb:24-27`: `locations_for` reads `backup_runs`, which go with the row).
  - kamal-proxy is shared by every app, so `kamal remove` is never used.

## Design (short)
- **`ProjectDeletion`** (a table). It's both the request and the record, like `BackupRun`:
  - `project_id`: nullified when the row goes
  - `name` and `repo_url`: kept after the row goes
  - `by`: the admin, or the token's name
  - `delete_backups` (bool)
  - `status`: queued / running / go / no_go
  - `step`, `log`, `error`
  - `snapshot_id` and `snapshot_location_id`: the final snapshot
  - `started_at`, `finished_at`, `heartbeat_at`

  A project is **deleting** while it has a deletion past the point of no return that hasn't finished GO.
- **`ProjectDeletion.request!(project, confirm:, delete_backups:, by:)`** does, in one transaction:
  - refuses unless `confirm == project.name`
  - refuses while a deploy or restore is queued or in flight, or a backup is queued or running, naming it ("deploy #12 is in flight; wait for #12")
  - refuses a second request while one is queued or running
  - an earlier deletion that failed NO-GO after the point of no return is **resumed** (re-queued), not refused

  SQLite serializes writes, so a webhook's deploy and this request can't both win.
- **While a project is deleting (or its deletion is queued or running), nothing new starts:**
  - sync is refused: 409 "equip is being deleted" (a hand `houston deploy` doesn't recreate it mid-delete), and so is linking (through `ProjectSync#save!`)
  - `Deploy.queue!`, `Deploy.start!` and `Deploy.request_restore!` refuse
  - the change check queues nothing, and Deploy now refuses
  - `claim_next!` skips it
  - `BackupRun.request!` refuses, and the schedule skips it

  Secrets, volume placement and the backup target can still be edited: nothing is created from them.
- **`DeleteProjectJob`** runs on its own `deletions` queue (one thread), so a final snapshot never waits behind, or holds up, a backup or a change check. `ProjectRemoval` does the work: it claims the deletion (queued → running), beats a heartbeat, and takes these steps, each idempotent:
  1. **check** (nothing changed yet):
     - Docker answers.
     - `HOUSTON_KAMAL_HOME` is set.
     - Cloudflare's token answers, when it's connected.
     - Unless `delete_backups`, there's backup storage for a final snapshot.
  2. **snapshot** (unless `delete_backups`, or there's nothing deployed or no data): a `BackupRun` of kind `final`, reason `delete`, run inline. Retention forgets by `kind:` tag and skips `final`, so it's never forgotten. Its id and location are kept on the deletion.
     - **The point of no return is after this step** (`removing_at`). A failure in 1–2 → NO-GO "cancelled: …": nothing was removed, the project serves as before, and asking again starts a new deletion.
  3. **routes:** `TunnelRoutes` leaves out projects being removed; pushed under the maintenance lock when the project was in maintenance.
  4. **dns:** in the base zone and the zones of its domains, records whose comment is *exactly* `managed-by:houston project:<name>` (filtered by Cloudflare, and checked again here).
  5. **containers:**
     - by the exact label `service=<name>`, then `^/<name>-release-<hex>$`
     - the accessory names it claims (every generation), except any another project claims
     - `houston-kamal-<name>`
     - `kamal-proxy remove <name>-web`

     "No such container" and "service not found" count as done.
  6. **volumes:** `<name>_…`, `<name>.g<n>_…` and its backup/restore staging volumes. Project names have no dots or underscores, so nobody else's can match. Holders are removed first; "no such volume" counts as done.
  7. **folders:** `volumes/<name>` and `volumes/<name>.g<n>` on each storage location its volumes were placed on, through the location's own volume (as `VolumePlacement` makes them).
  8. **images:** `127.0.0.1:5000/<name>:*` on the host.
  - **registry** (Batch 7): its manifests in Houston's registry, then `RegistryCleanupJob` frees their space.
  9. **files:** Kamal's `apps/<name>`, `<name>-audit.log` and `lock-<name>`, and `/var/lib/houston/runners/*/<name>`, through the tools image.
  10. **backups** (only if `delete_backups`): every snapshot tagged `project:<name>` in each location `Snapshots.locations_for` names, `restic forget <ids> --prune`.
  11. **rows:** the snapshot caches cleared, `project.destroy!`. The deletion keeps its name, repo and final snapshot (`project_id` nullified).
  - **A failure in 3–11** → NO-GO "stopped at <step>: …". The project stays deleting. Asking again (**Finish deleting**, the API, or the same CLI command) resumes it from step 3, with what it was first asked (`delete_backups` included).
- **Name reuse:** `Snapshots.locations_for` also reads the locations of the deletions with that name. So a re-added `equip` lists its final snapshot, and `houston restore <id>` rolls it back as usual (the restore rules still apply: a linked repo that has the commit).
- **What Houston can't remove:** the deploy key and webhook on the git host. The finished page says so, with the repo URL.
- **UI:**
  - **Project page:**
    - a last section, **Danger zone** (after the alphabetical ones, in the menu too), with "Delete this project" and a line on what's kept
    - while a deletion runs: a DELETING notice linking to it
    - stopped partway: a NO-GO notice with **Finish deleting**
    - cancelled (for a day): a NO-GO notice saying nothing was removed
  - **`/projects/:name/deletion/new`,** the Restore dialog's layout:
    - banner "DELETE · WHOLE PROJECT / CAN'T BE UNDONE"
    - the title "Delete equip?"
    - what goes: containers, data (volumes and where they live), domains, on the server, Mission Control
    - KEPT: the final snapshot and its location, or why there's none
    - the "Also delete its backups" checkbox
    - "Type equip to confirm"
    - the CLI hint, Cancel, and a red Delete equip
  - **`/deletions/:id`,** the update page's layout:
    - the twelve steps (DONE / RUNNING / FAILED) and the log
    - while it runs, it reloads every 5 s
    - when it's done: "equip was deleted", the final snapshot with `houston restore <id>`, and "remove the deploy key and the webhook from <repo>"
  - **Flight board:** a DELETING chip (row and card). Every change of a deletion's status or step refreshes the board.
  - **Snapshots panel:** a `final` snapshot is listed on the Pre-deploy tab as "before it was deleted", with Restore, and isn't counted against `keep_deploy`.
- **API:**
  - `DELETE /api/v1/projects/:name {confirm, delete_backups?}` → 202 `{deletion}`; a refusal → 422 `{error}`
  - `GET /api/v1/deletions/:id` → the deletion with its log
  - `GET /api/v1/projects/:name` includes `deleting` (the deletion that holds it, or null)
- **CLI:** `houston delete --confirm NAME [--delete-backups] [--follow] [--project NAME]`.
  - Without `--confirm`: exit 2, and nothing is sent.
  - `--follow` prints each step once and exits 0 on GO (with the final snapshot and the git-host reminder) or 1 on NO-GO ("run this again to finish" only past the point of no return).
  - `houston status` shows a `deleting` line.
  - `houston snapshots` labels a final snapshot "before it was deleted".
- **Installer:** Mission Control gets `HOUSTON_KAMAL_HOME=$houston_home/.kamal`.
- **Docs:** README's "Deleting a project", and docs/agents.md (a ground rule, and a reference line).

## Batches
1. **Spike** (OrbStack, with the spike project: Postgres, an NFS volume, SQLite, a custom domain). By hand, find:
   - the exact commands for kamal-proxy's route and Kamal's host state (`~houston/.kamal`: locks, env files)
   - whether removing the app containers alone leaves kamal-proxy answering 502 for the host
   - that emptying and then removing an NFS volume leaves the export's folder removable

   The notes are recorded below.
2. **The model:** the `ProjectDeletion` table, `request!` with its refusals and resume, the `final` kind and `delete` reason, "deleting" blocking sync, linking, the change check, claims, restores and backups, and `locations_for` reading deletions.
3. **The engine:** `DeleteProjectJob`'s eleven steps (`ProjectRemoval`) against `FakeDocker` and the Cloudflare stubs, every failure path, and resume.
4. **The UI:** the Danger zone, the confirm page, the deletion page and the DELETING chip. Checked visually at desktop and 375 px.
5. **API and CLI:** `DELETE /api/v1/projects/:name`, `GET /api/v1/deletions/:id`, `houston delete`, and the docs.
6. **The real run** (`install/test/delete-project.sh`, on its own OrbStack machine; Cloudflare marked connected in wildcard mode, so DNS and the tunnel are the unit tests'):
   - spike (Postgres, its data on the machine's NFS export, SQLite in it) and spike-x deployed; spike backed up, then put in maintenance.
   - **Cancelled:** the backup storage read-only → the final snapshot fails → exit 1, and nothing of spike removed.
   - **Stopped:** Kamal's folder for spike read-only → NO-GO at files. `houston status` says so, and a hand `houston deploy` and a change check are refused. The same command again → GO.
   - **Nothing of spike left:**
     - containers, volumes, the NFS folder, images, kamal-proxy's route, Kamal's files, checkouts, the row
     - its host answers kamal-proxy's 404
   - **Re-added:** it lists its final snapshot, and `houston restore` brings the data back.
   - **Deleted again** with `--delete-backups`: nothing left, generation 2 included, and no snapshot of spike in the repository.
   - spike-x is checked untouched after each stage.

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | `request!`: the wrong name → refused, nothing written, no job; a deploy or restore queued or in flight, or a backup queued or running → refused, naming it; a second request while one is active → refused | `models/project_deletion_test.rb` `"asking to delete"`, `"asking to delete, refused"` | Preconditions |
| 2 | A deletion cancelled before removal doesn't hold the project, and asking again makes a new one; one stopped during removal holds it, and asking again resumes the same one as first asked | `"a deletion cancelled before removal, then asked again"`, `"a deletion stopped during removal is resumed, not asked again"` | Idempotency |
| 3 | A deploy and a delete at once: whichever commits first wins, the other is refused inside its transaction | `"a deploy and a delete at once"` | Concurrency |
| 4 | While deleting: `start!`, `request!` (backups) and `request_restore!` refuse; a queued deploy isn't claimed (another project's is); the change check queues nothing and doesn't read the repo; `queue_head!` refuses; the schedule skips it; sync → 409 and linking refused | `"nothing new starts while a project is being deleted"`, `"the backup schedule skips a project being deleted"`, `integration/api_sync_test.rb` `"a sync waits for a deletion"` | Invariants |
| 5 | Checks or the final snapshot failing (Docker down, Cloudflare refusing, no Kamal home, no backup storage, the snapshot NO-GO) → NO-GO "cancelled", no removal command, no DNS delete, the project not deleting | `jobs/delete_project_job_test.rb` `"cancelled before anything is removed"` | Fail closed |
| 6 | The happy path's commands in order (containers by exact label and names, kamal-proxy's route, volumes by pattern, folders, images, Kamal's files and checkouts); the rows gone, the deletion GO with name, repo and final snapshot (kind `final`, reason `delete`, never forgotten); the routes pushed without its hosts; only its own DNS record deleted | `"deleting removes everything it owns"` | Contract, Blast radius |
| 7 | A project whose name starts the same (`equip-x`) is never named in any command, and keeps its container names | `"a project whose name starts the same is untouched"` | Blast radius |
| 8 | No final snapshot when nothing's deployed | `"the final snapshot is skipped when there's nothing to keep"` | Contract |
| 9 | A failure in containers, then volumes: NO-GO naming the step, the project still deleting; resuming finishes GO with no second final snapshot, treating "no such container/volume" and "service not found" as done; a failure at dns, images, files or folders stops there with the row kept | `"stopped partway, then resumed"`, `"each removal step stops the deletion when it fails"` | Idempotency, Atomicity |
| 10 | `delete_backups`: no final snapshot; every snapshot forgotten (`--prune`) in each location the backups used; a restic failure → NO-GO at backups | `"deleting the backups too"` | Contract |
| 11 | A job for a deletion that isn't queued does nothing | `"a job for a deletion that isn't queued does nothing"` | Concurrency |
| 12 | A re-added project with the same name reads its final snapshot's location (not another name's) | `models/snapshots_test.rb` `"a deleted project's snapshots"` | Contract |
| 13 | A project being removed has no maintenance routes (still has them while its final snapshot is taken) | `models/tunnel_routes_test.rb` `"a project being removed has no routes"` | Authz |
| 14 | The UI: the Danger zone (last in the menu); the confirm page (what goes, what's kept, the checkbox, the CLI hint); the wrong name or a busy project → 422 with the reason; the right name → the deletion page (by, delete_backups); done → what's kept, how to restore, the git-host reminder; the project page while deleting, stopped (Finish deleting resumes) and cancelled; signed out → sign-in, nothing queued | `controllers/project_deletions_test.rb`, `controllers/project_pages_test.rb` | Authz, Contract |
| 15 | The board shows DELETING (row and card), only for that project; a deletion refreshes the board | `controllers/projects_controller_test.rb` `"a project being deleted"`, `"a deletion refreshes the board"` | Contract |
| 16 | A final snapshot is on the Pre-deploy tab as "before it was deleted", restorable, and not counted | `controllers/project_snapshots_test.rb` `"a deleted project's final snapshot"`; `internal/cli/delete_test.go` `TestSnapshotNoteFinal` | Contract |
| 17 | API: DELETE with the wrong name → 422, no job; right → 202 and the deletion, by the token; again → 422; `deleting` on the project; GET the deletion after the project is gone; an unknown project → 404; no token → 401 | `integration/api_v1_deletions_test.rb` | Authz, Contract |
| 18 | CLI: no `--confirm` → exit 2, nothing sent; `--delete-backups` sent; `--follow` prints each step once, exit 0 on GO with the snapshot and the git-host reminder, exit 1 on NO-GO; a 422 → its words; `houston status` shows `deleting` | `internal/cli/delete_test.go` | Contract |
| 19 | On a real server: a cancel changes nothing; a stop leaves it deleting (status says so, a hand deploy and a change check are refused); the same command finishes it; nothing of spike is left and spike-x is untouched; re-added, its final snapshot restores the data; `--delete-backups` leaves no snapshot | `install/test/delete-project.sh` | End to end |

Each test was seen failing for the right reason first (a missing constant, route or command; the Danger zone missing from the page), then made to pass. Then the code was broken on purpose to prove the tests catch it (Evidence).

## Spike notes
(Batch 1, by hand on `install/test/delete-project.sh`'s machine with `SETUP_ONLY=1 KEEP=1`: spike and spike-x deployed, spike's `data` on the machine's NFS export.)
- **What spike left:**
  - Containers `spike-web-<sha>` (label `service=spike`) and `spike-db` (label `service=spike-db`, so the app's label never matches an accessory).
  - Volumes `spike_data` and `spike_pgdata`. Postgres mounts only its named volume, with no anonymous one.
  - Images `127.0.0.1:5000/spike:<sha>` and `:latest`.
  - kamal-proxy's service `spike-web` (hosts `spike.houston.test,spike-alias.houston.test`).
  - `/srv/nfs/volumes/spike/data`.
  - A checkout on `houston-runner-1` only.
  - **Kamal's host state, which the plan missed:** `~houston/.kamal/apps/spike/env/{roles/web.env,accessories/db.env}`, mode 600, holding the app's **secret values** (`DATABASE_URL=…`), plus `~houston/.kamal/spike-audit.log`. Deleting must remove both. Mission Control doesn't know houston's home, so the installer now passes `HOUSTON_KAMAL_HOME` (`$houston_home/.kamal`). A server that hasn't rerun the installer is refused at the check step ("run the installer once more"), as Update does with its runner image.
- **kamal-proxy:**
  - With the app containers removed and the route left, `spike.houston.test` answers **502**.
  - `docker exec kamal-proxy kamal-proxy remove spike-web` → exit 0, and both hosts answer **404** (an unknown host's answer). spike-x stays 200.
  - A second remove → exit 1, `Error: service not found`, which counts as done.
- **Volumes and folders:**
  - `docker volume rm spike_data spike_pgdata` works once the containers are gone. A second run → exit 1, `no such volume`, which counts as done.
  - The folder goes through the location's own volume (`houston-storage-vm-nfs`, as `VolumePlacement` makes folders): `rm -rf` of `volumes/spike` and `volumes/spike.g<n>`, with the name passed as `$1`, never in the script. It's idempotent: a second run exits 0.
  - This deletes the data where it lives, so restore's "empty each volume first" isn't needed. A local-disk volume's data goes with `docker volume rm`.
- **Kamal files and checkouts:** one `rm -rf` in the tools image with `~houston/.kamal` and `/var/lib/houston/runners` mounted. Idempotent.
- **Images:** `docker images --filter reference='127.0.0.1:5000/spike:*'` lists exactly spike's tags (spike-x's aren't matched), and `docker rmi -f` removes them.
- **spike-x afterwards:** it answers 200 through kamal-proxy, its Postgres answers, and its containers, volumes, Kamal files and checkout are all there.

## Batch 7: the registry's copies

### Your direction
"Houston should garbage collect automatically if an app is removed."

### Spike (registry 3.1.2, locally)
- **Deletes are off by default.** `REGISTRY_STORAGE_DELETE_ENABLED=true` turns them on.
- **An unknown digest answers 404 either way,** so the API can't tell whether deletes are on. The check reads the registry container's environment instead (`docker inspect`).
- **Finding and deleting an app's images:**
  - `GET /v2/<name>/tags/list` lists its tags, or 404 `NAME_UNKNOWN` when it has none.
  - `HEAD /v2/<name>/manifests/<tag>`, with the manifest media types in `Accept`, gives `Docker-Content-Digest`.
  - `DELETE /v2/<name>/manifests/<digest>` → 202, and every tag on that digest goes (`sha1` and `latest` together). A second DELETE → 404.
- **`registry garbage-collect /etc/distribution/config.yml`** (in the registry container) freed the deleted app's blobs: 42 MB → 22 MB. The other app's shared base layer was kept, and it still pulled and ran.
- **Distribution's rule:** garbage collection must not run during a push, or it can delete a layer being uploaded. Only deploys and restores push (runners, or a hand `houston deploy`).

### Design (short)
- **Installer:** the `registry` service gets `REGISTRY_STORAGE_DELETE_ENABLED: "true"`. A server has it once it has run this installer, the same condition as `HOUSTON_KAMAL_HOME`.
- **The check step** also requires the registry container to have deletes on; otherwise it cancels with "run the installer once more".
- **A new removal step, `registry`,** after `images`: for each of `<name>`'s tags, find the digest and DELETE it. A 404 counts as done; any other answer stops the deletion there. Then `RegistryCleanupJob` is queued with the deletion.
- **`RegistryCleanup`,** the garbage collection:
  - **One at a time, never during a push.**
    - `Installation#registry_cleanup_since` is the lock. It's taken in a transaction only while no deploy is in flight; stale after 30 minutes.
    - While it's held, `claim_next!` hands out nothing and `Deploy.start!` refuses (409, "Houston is cleaning its registry; try again in a minute").
  - **With the lock:** find the registry container (Compose service `registry` in Mission Control's own Compose project), run `registry garbage-collect` (30 minutes at most), and always let the lock go.
  - **`RegistryCleanupJob`:** on the `deletions` queue. While a deploy is in flight it waits and tries again, every 30 s for up to 6 hours. It appends how it went to the deletion's log ("registry space freed", or why not). A failure never changes the deletion's GO: the app is already gone, and the next deletion's cleanup collects everything unreferenced.

### AC ↔ test map (Batch 7)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 20 | The check cancels when the registry doesn't allow deletes | `jobs/delete_project_job_test.rb` `"cancelled before anything is removed"` | Fail closed |
| 21 | The registry step: each tag's digest deleted once; 404s done; a repository with no tags done; another answer → stopped at registry; the cleanup queued after GO | `"deleting removes everything it owns"`, `"the registry's copies"` | Contract, Idempotency |
| 22 | The cleanup: refused while a deploy is in flight (nothing run); takes the lock, runs garbage-collect in the registry container, lets go (even when it fails); a stale lock is taken over | `models/registry_cleanup_test.rb` | Concurrency, Atomicity |
| 23 | While it runs: `claim_next!` hands out nothing; `Deploy.start!` refuses (the API: 409) | `models/registry_cleanup_test.rb`, `integration/api_deploys_test.rb` | Invariants |
| 24 | The job waits while busy, and records the result in the deletion's log | `jobs/registry_cleanup_job_test.rb` | Contract |
| 25 | On a real server: the app's tags gone from the registry, its blobs freed (size down), spike-x's image still pulls | `install/test/delete-project.sh` | End to end |

## Evidence
- **Failing first:**
  - `project_deletion_test.rb`: `NameError: uninitialized constant ProjectDeletion`.
  - `project_deletions_test.rb`: `undefined method 'project_deletion_path'`, and the page's last menu item was "Volumes".
  - `delete_test.go`: `unknown flag: --confirm`.
  - `project_snapshots_test.rb`: 1 snapshot shown where 2 were expected (a `final` snapshot was on no tab).
  - `projects_controller_test.rb`: no DELETING chip, and no board refresh when a deletion is created (the status column's default isn't a change).
- **Mutation checks** (each change made, the named test failing, then restored):

  | Broken on purpose | Caught by |
  |---|---|
  | `Deploy.queue!`/`start!` not refusing | `"a deploy and a delete at once"`, `"nothing new starts…"` |
  | `claim_next!` claiming a deleting project's deploy | `"nothing new starts…"` (line 107) |
  | DNS deleted without the exact comment check | 6 tests in `delete_project_job_test.rb` (the `equip-x` record's DELETE isn't stubbed) |
  | `TunnelRoutes` keeping a project being removed | `"a project being removed has no routes"`, `"deleting removes everything it owns"` |
  | `Snapshots.locations_for` ignoring deletions | `"a deleted project's snapshots"` |
  | Retention forgetting `final` snapshots | `"deleting removes everything it owns"` |
  | Removing before the final snapshot | `"deleting removes everything it owns"`, `"cancelled before anything is removed"`, `"stopped partway, then resumed"` |
  | The volume pattern loosened to a prefix | `"deleting removes everything it owns"`, `"a project whose name starts the same is untouched"` |
  | The checkbox ignored (UI), or `delete_backups` ignored (API) | `"deleting, the wrong name and then the right one"`, `api_v1_deletions_test.rb` |
  | The board's DELETING set empty | `"a project being deleted"` |
  | `deleting` left out of the project's JSON | `api_v1_deletions_test.rb` |
  | `houston delete` sending without `--confirm` | `TestDelete` ("no --confirm: exit 0, 1 requests") |

- **Suites:**
  - `bin/go test ./...`: 10 packages ok. `gofmt -l internal cmd` prints nothing.
  - `bin/rails test && bin/rubocop`: 449 runs, 4320 assertions, 0 failures; 299 files, no offenses.
  - `install/test/install-version.sh`, `install-bind.sh` and `install-ssh-key.sh`: PASS.
- **Visual check** (a dev server on :3050, a sample project, since removed):
  - the Danger zone, the confirm page and the deletion page at desktop and 375 px
  - `scrollWidth == clientWidth` (375) on each, with no element past the right edge
- **`install/test/delete-project.sh`: PASS**, every check ok:
  - **Cancelled:** exit 1, and nothing of spike removed.
  - **Stopped:** at files. `houston status` says so, a hand deploy is refused, the host answers 404.
  - **Finished:** the same command → GO, with final snapshot `8c97e4f9` kept in local-backups. Nothing of spike is left, and spike-x is untouched.
  - **Re-added:** `houston restore 8c97e4f9` brings back 'kept' in Postgres and SQLite (NFS, generation 2).
  - **`--delete-backups`:** nothing left, and 0 snapshots of spike in local-backups.
- **Batch 7 (the registry):**
  - **Failing first:** `uninitialized constant Registry`, `RegistryCleanup` and `RegistryCleanupJob`. `claim_next!` handed out a deploy, and `Deploy.start!` answered 201, while the lock was held.
  - **Mutation checks, each caught:**

    | Broken on purpose | Caught by |
    |---|---|
    | Collecting while a deploy is in flight | `"never while an image may be pushed"`, `"waits while an image may be pushed"` |
    | Claims during a cleanup | `"while it runs, no image is pushed"` |
    | Hand deploys during a cleanup | same, and `api_deploys_test.rb` `"no deploy starts while the registry is cleaned"` |
    | The lock never let go | 3 tests in `registry_cleanup_test.rb` |
    | A stale lock never taken over | `"one at a time; a stale lock is taken over"` |
    | No check that the registry allows deletes | `"cancelled before anything is removed"` |
    | No registry step | `"deleting removes everything it owns"`, `"each removal step stops…"` |

  - **Suites:** `bin/rails test && bin/rubocop`: 460 runs, 4391 assertions, 0 failures; 305 files, no offenses.
  - **`install/test/delete-project.sh`: PASS** (42 ok):
    - spike has no tags left in the registry
    - "registry space freed (1 blobs and 0 manifests eligible for deletion)"
    - the registry shrank, 2072 KB → 2064 KB
    - every blob of spike-x's image still answers 200

    The fixtures share all but their config blob, so the freed space is small by design: shared layers are kept.
  - **Found on the way:** re-adding a project after its daily backup time queues that backup at once, so a delete asked for straight away is refused ("a backup of spike is queued; wait for it"). That's the intended refusal; the run now waits for the backup.

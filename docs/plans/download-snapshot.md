# Plan: Download a snapshot

Direction (2026-09-28): "can we build a db export feature into houston so that I can download a backup from the UI?", then "could we just have a zip with db and files? … if we had images and other things in the backup … it would be weird to only include a few things."
Builds on docs/plans/volumes-backups.md (what a snapshot holds) and docs/plans/restore.md (how a snapshot is found and trusted).
Standing rule: every Mission Control action gets a CLI path and an API route.

## Goal

From any snapshot on a project's page, **Download** gives one zip of everything the snapshot holds: the app's volumes as files (uploads, images, anything else), each SQLite database as a consistent copy, each Postgres dump and its roles, and `houston.json`, which says what came from where. The same file comes from `houston snapshots download <id>`. Nothing about the running app is touched: the file is read out of restic.

## Evidence

- A snapshot holds two trees (`mission_control/app/models/backup.rb:91-97`):
  - `/data/<volume>/…`: each named volume's files. The live SQLite files and their `-wal`, `-shm` and `-journal` files are left out by `--exclude-file` (`lib/backup/sqlite.rb`), so no half-written database is in it.
  - `/out/`: `sqlite/<n>.sqlite3` (`sqlite3 .backup` copies), `postgres/<service>/<n>.dump` (`pg_dump -Fc`), `postgres/<service>/globals.sql`, `houston.json` (`backup.rb:83`) mapping each number to its volume, path or database, and `.houston/exclude`.
  - A project with no volumes has only `/out`.
- A snapshot is trusted only after it's found in `Snapshots.for(project, location)`, and a location only if it's in `Snapshots.locations_for(project)` (`app/models/deploy.rb:145-149`, `app/controllers/project_restores_controller.rb:20`). **This is the authz check.** Repositories are shared between projects, so a raw snapshot id could belong to another project.
- restic runs through `StorageLocation#restic_args` / `#restic_env`, with secrets only in docker's environment (`app/models/storage_location.rb:83-96`), pinned at `restic/restic:0.19.1`.
- Streaming exists for logs (`app/controllers/api/v1/logs_controller.rb`, `ActionController::Live` plus `DockerCommand.stream`). But `Runner#stream` uses `popen2e`, which **mixes stderr into the bytes**, takes no env (restic needs its password), and ends the response cleanly even when docker failed.
- Puma's threads are `RAILS_MAX_THREADS`: 3 by default (`config/puma.rb`), and **8 on a server** (the installer sets it; `install/test/orbstack.sh` checks it). *(Corrected in Batch 3: this said 3.)* A download holds one for as long as it runs, and runners' heartbeats and the pages need the rest. With volumes included, a download can run for minutes.
- The snapshot's size is already known: `Snapshot#bytes`, from restic's `total_bytes_processed`, shown on each row.
- The Snapshots panel lists the project's current `backup_location` only; its rows already link Restore with `snapshot` and `location` (`app/views/project_snapshots/index.html.erb`).
- `FakeDocker` records `call`, `pipe` and `stream` (`test/support/fake_docker.rb`).

## Scope

- **The whole snapshot, as one zip:** `data/` (the volumes) and `out/` (the databases and `houston.json`), as restic stores them. (Decision 1, answered.)
- **From an existing snapshot.** For today's data: Back up now, then Download.
- **One download at a time, server-wide.** (Decision 2.)

## Batches

1. **Download from the project page, end to end** (below): the spike, `SnapshotExport`, `DockerCommand#download`, the web route, and the snapshot row's link.
2. **The API and the CLI:** `GET /api/v1/projects/:name/snapshots/:id/download?location=`; `houston snapshots download SNAPSHOT [--location NAME] [-o FILE]`. The CLI writes `<file>.part`, checks the zip opens, then renames it; it never overwrites a file that's there.
3. **The real run:** on OrbStack, with the equip copy (SQLite in WAL mode, plus an image in a volume) and the spike (Postgres, including the hostile database name). Download from the page and from the CLI, on port 3000 and through the tunnel. Then open the files (`sqlite3 … 'pragma integrity_check'`, `pg_restore --list`, and the image's checksum against the live one). Also: cut a download off mid-way, and start two at once.

## Batch 1: Download from the project page

### Spike first
`install/test/restic-dump-spike.sh`, against a local repository with a `/data` and an `/out` tree, like `restic-spike.sh`. It pins:
- `restic dump --archive zip <id> /`: both trees are in it, the entries' names (`data/…`, `out/…`), and whether they're compressed. An empty directory and a symlink in a volume come out as what (kept, skipped)?
- **Zip64:** a sparse 4.5 GiB file in `/data` comes out whole, and `unzip -t` passes. Volumes can pass 4 GiB where databases rarely did.
- If restic's zip can't do either, the design switches to `--archive tar` piped through `gzip` in the same container, with a `.tar.gz` file.
- A snapshot id that doesn't exist, a wrong password (12), and a locked repository without and with `--retry-lock 30s`. The exit codes, and that **a failure writes nothing to stdout**.
- `--name houston-export` twice at once: the second exits 125 with "is already in use" before starting restic.
- **The mid-stream break:** Puma (the Gemfile's version) serving a plain Rack body whose `each` raises after a few chunks. curl must exit 18 ("transfer closed with outstanding read data"), not 0. If it exits 0, the file would look complete to a browser, and the design needs another way to break the connection.

**Spike notes** (`install/test/restic-dump-spike.sh`, RESTIC DUMP SPIKE PASS):
- `dump --archive zip <id> /` holds both trees as `data/…` and `out/…`, with empty folders and symlinks (as symlinks) kept. Entries are deflated, and a file comes out byte for byte.
- **Zip64 works:** a 4.5 GiB file came out whole, and `unzip -t` passed. No tar.gz fallback is needed.
- **Failures write nothing to stdout:** an unknown snapshot (1), a path not in it (1), a wrong password (12).
- **Except a lock, unless `--quiet`.** A repository locked by prune exits **11**, and restic writes "repo already locked, waiting up to 0s for the lock" **to stdout**. With `--retry-lock`, that line would sit at the front of the zip and break it. `--quiet` keeps stdout to the zip alone, and `--quiet --retry-lock 30s` waited out a released lock with a clean zip.
- `docker run --name` taken → 125, "is already in use", before the container starts. `docker inspect -f '{{.Created}}'` gives its start time.
- **Puma 8.0.2 breaks a response whose body raises:** curl exits 56 (a bad chunk), not 0. A browser marks the download failed.

**What they change in the design:**
- The command gets `--quiet`.
- **The first bytes must be a zip's** (`PK\x03\x04`). Anything else is `Failed` ("restic didn't send a zip: …"), and nothing is sent. Whatever restic prints next time can't become a download.
- Exit 11 reads "the repository is busy (pruning?); try again in a few minutes".

### Design (short)
- **`DockerCommand::Runner#download(args, env, timeout:)`**, a sibling of `stream`: `popen3`, so stdout carries only the file's bytes and stderr is collected on its own. It blocks until the first chunk or EOF.
  - EOF first → `Result(success: false, output: stderr, code:)`. Nothing has been sent, so the caller can still answer with an error.
  - Bytes first → a `Download` body. `each` yields the first chunk, then the rest. At EOF it raises `DockerCommand::Broken` (with stderr's last lines) if docker exited non-zero. `close` stops docker if it's still running.
  - `FakeDocker#download` records the call and yields scripted chunks, a scripted failure before the first byte, or a scripted break after N chunks.
- **`SnapshotExport`** (one download; both controllers use it):
  - `SnapshotExport.open(project, location_name:, snapshot:, by:)`:
    1. The location must be in `Snapshots.locations_for(project)`, and the snapshot (its full or short id) must be in `Snapshots.for(project, location)`. Otherwise → `NotFound`.
    2. `Snapshots::Unavailable` → `Failed` ("can't read <location>'s snapshots: …").
    3. It runs `timeout 10800 docker run --rm --name houston-export …restic_args… dump --quiet --retry-lock 30s --archive zip <full id> /`, with `restic_env` as the environment.
    4. On exit 125 with "already in use": if the existing `houston-export` was created more than 3 h 10 m ago, it is removed (`docker rm -f`) and the download is tried once more. Otherwise → `Busy` ("another download started at 14:02 is still running").
    5. Any other failure before the first byte → `Failed` with restic's last lines. First bytes that aren't a zip's → `Failed`, and docker is stopped. A locked repository (exit 11) reads "the repository is busy (pruning?); try again in a few minutes".
  - It returns `[body, filename]`. The filename is `<project>-<YYYYMMDD-HHMM>Z-<short_id>.zip`.
  - `close` runs `docker rm -f houston-export` if docker hadn't finished (the client went away), and logs one line.
  - **Log lines (Signals):** `download: <by> <project> <short_id> from <location>: started`, then `finished, <bytes>`, `stopped by the client after <bytes>`, or `broke after <bytes>: <reason>`. The log never includes the password or the repository's credentials.
- **Web:** `GET /projects/:project_name/snapshots/:id/download?location=NAME` (`ProjectSnapshotsController#download`, signed in).
  - It answers with a plain Rack body, not `ActionController::Live`, so a failure mid-stream breaks the connection (the spike pins this).
  - Headers: `Content-Type: application/zip`, `Content-Disposition: attachment; filename="…"`, `Cache-Control: no-store` (which also keeps `Rack::ETag` from buffering the body), and `X-Accel-Buffering: no`. There's no `Content-Length`, because the zip's size isn't known until it's written, so the browser shows bytes received rather than a percentage.
  - `NotFound` → 404. `Busy` and `Failed` → a redirect to the project page with the reason as an alert. Nothing has been sent at that point, so the redirect works.
- **The snapshot row** gets a **Download** link next to Restore, with `data-turbo="false"` and the snapshot's size in its title ("Download everything in this snapshot, about 1.2 GB"). It gets **no** `download` attribute: with one, a refusal's redirect would save the project page's HTML as a file.
- **The panel's footnote** says what's in the zip: "Each snapshot holds code and data together. Download gives its files (`data/`) and databases (`out/`, with `houston.json` saying which is which)."

### Contract pin
- **`GET /projects/:project_name/snapshots/:id/download`:**
  - `id`: a full or short snapshot id, compared exactly. `location`: a storage location's name.
  - Signed in → the zip, 200. Signed out → sign-in, and no docker command runs.
  - Unknown project, unknown location, a location the project never used, or a snapshot not in its list (**another project's snapshot in the same repository included**) → 404, and no `dump` runs.
  - Busy, the listing failing, or restic failing before the first byte → 302 to the project with the alert.
- **Preconditions (allow-list):** the snapshot is listed for this project in this location, and no `houston-export` container is younger than 3 h 10 m. Nothing else starts a download.
- **Effects:** there's nothing durable. One restic container runs, named `houston-export`, removed by `--rm`, and by `docker rm -f` when the client leaves or on a later stale takeover. Log lines only.

### Templates filled
**Concurrency (two downloads at once)**
- Writer A and B: two requests, which may come from different Puma threads or processes.
- Ordering: Docker's container name is the lock. `docker run --name houston-export` is atomic, and the loser gets 125 before restic starts.
- Bad interleaving: A finishes while B checks. B's run either wins (A's container is gone) or gets 125, so there's no double slot.
- Stale takeover: A's container is older than the 3-hour deadline (it's stuck on NFS, and Mission Control restarted, so nothing will stop it). B removes it and tries once. A younger container is never removed.
- Nothing runs under a hold; the name is the hold.

**Crash gap**
- Mission Control dies mid-download: the docker CLI dies with it. restic writes into a closed pipe and exits (SIGPIPE), and `--rm` removes the container. If it's hung instead, the next download's stale check removes it after 3 h 10 m.
- Nothing durable is written, so there's nothing to repair.

**Stuck-alive:** restic hangs mid-read (a stalled B2 or NFS). The `timeout 10800` ends the docker CLI; `close` removes the container; the connection breaks and it's logged "broke after …".

### AC ↔ test map (Batch 1)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 0 | The spike's pins (both trees, names and compression, zip64, exit codes, silent stdout on failure with `--quiet`, 125 on the name, a broken body isn't a clean end) | `install/test/restic-dump-spike.sh` prints RESTIC DUMP SPIKE PASS | Contract, Scale |
| 1 | `Runner#download`: stdout's bytes only (stderr kept out); EOF first → failure Result with stderr; exit ≠ 0 after bytes → `each` raises `Broken`; `close` mid-way stops the process. Run against a stand-in `docker` script on PATH | `test/models/docker_command_test.rb` `test "download keeps stderr out and breaks on failure"` | Contract, Signals |
| 2 | The command: `timeout 10800`, `--rm --name houston-export`, `dump --quiet --retry-lock 30s --archive zip <full id> /`, the location's repo mounts; the password is in env and not in argv; a short id resolves to the full one | `test/models/snapshot_export_test.rb` `test "the download's command"` | Contract |
| 3 | Only the project's own snapshots: an unknown location, a location never used, a snapshot id listed for another project in the same repository → `NotFound`, no `dump`; the listing failing → `Failed` with its words | `test "only the project's own snapshots"` | Authz, Preconditions |
| 4 | Before the first byte: restic failing (exit 1, 12, 11 locked) → `Failed` with its lines and nothing yielded; first bytes that aren't `PK\x03\x04` → `Failed`, docker stopped; 125 "already in use" with a young container → `Busy` with its start time, and nothing removed; with a container older than 3 h 10 m → removed, one retry, bytes | `test "a download that can't start"` | Concurrency, Crash & repair |
| 5 | The body: the chunks in order; a break after N → raises, and logs "broke after"; the client leaving → `docker rm -f houston-export` and "stopped by the client"; a clean end → "finished, <bytes>". No log line has the password | `test "a download's body and its log"` | Signals, Crash & repair |
| 6 | The page's download: 200, `application/zip`, the attachment filename, `no-store`, and the body is the bytes | `test/controllers/project_snapshots_test.rb` `test "downloading a snapshot"` | Contract |
| 7 | Its refusals: signed out → sign-in with no docker call; unknown snapshot or location → 404; Busy / Failed → redirect to the project with the alert | `test "a download that's refused"` | Authz, Signals |
| 8 | Each snapshot row links Download with its location, `data-turbo="false"`, the size in its title, and no `download` attribute; the footnote names `data/` and `out/`. Visual check: the row at 375 px has no overflow | `test "each snapshot links its download"` + screenshot | Contract, Whole batch |

### Done (Batch 1)
- **Spike:** `install/test/restic-dump-spike.sh` → RESTIC DUMP SPIKE PASS. It changed two things (above): `--quiet`, and the zip-header check.
- **Red:** 8 new tests failed for the missing runner method, `SnapshotExport`, the route and the link. **Green:** `bin/rails test test/models/docker_command_test.rb test/models/snapshot_export_test.rb test/controllers/project_snapshots_test.rb` → 16 runs, 0 failures. With the other `restic_args` callers' tests (snapshots, backup, restore data): 38 runs, 0 failures.
- **Test mistakes fixed on the way:** the fake Docker was swapped out before `close` ran; a listing cached earlier in the same test hid the failing one. And `Content-Length` in an integration test comes from the harness, which buffers the body. The real check is that there's no ETag (`Rack::ETag` digests only a body with `to_ary`, and this one has none), plus curl in the real run.
- **Mutations, each caught:** no `--quiet`; any location; a prefix id match; no zip-header check; always taking over the name; `close` leaving docker; a broken body ending cleanly; the `download` attribute; not-found redirecting.
- **Visual check:** the project page rendered with two snapshots. At 375 px, the page is 375 wide (no overflow) and each row ends at 358 px, with Download and Restore side by side. On desktop, the size column stays.
- **scotty-review (cold pass):** three findings, each with a failing test first, then fixed:
  - **MEDIUM:** cleanup removed `houston-export` **by name**. A client leaving just after its own container ended could kill the next download, and two stale takeovers could hit each other's fresh container. Now each download labels its container `houston.export=<token>`, and removes a container only by the id it inspected, and only when the token is its own (stale: by id).
  - **MEDIUM:** `Download#close` waited without limit, so a docker CLI that ignored TERM could hold one of Puma's three threads forever. It now runs in its own process group: TERM to the group, KILL after `STOP_WAIT` (5 s), and every wait is bounded.
  - **LOW:** the refusal alert carried the raw id, and a long one could overflow the flash cookie (a 500). It's truncated to 64.
  - Mutations for each fix were caught: removing any holder, stale removal by name, no KILL, no process group, the raw id.

### Agent loop checkpoints
- The spike runs first; if it changes the design (zip vs tar.gz, the break), the plan gets updated before any tests are written.
- Red tests for rows 1–8 → the RED map is reported, and the work continues.
- Batch 1 green → scotty-review (cold pass).
- After Batch 3: scotty-review again if the tip moved, then the full Rails suite, `bin/go test ./...`, rubocop, and gofmt.

## Batch 2: The API and the CLI

### Design (short)
- **`GET /api/v1/projects/:name/snapshots/:id/download[?location=NAME]`** (`Api::V1::SnapshotsController#download`, personal tokens):
  - `location` defaults to the project's backup location, as restores' does. `SnapshotExport.open(…, by: "token <name>")`, the same headers and plain body as the page.
  - No project, or `NotFound` → 404; no location given and no backup storage → 409; `Busy` → 409; `Failed` → 502. Each is `{error}`, and nothing has been sent before it.
- **Go client:** `DownloadSnapshot(ctx, project, snapshot, location) (SnapshotDownload, error)` → `{Filename, Body}`, with no client timeout (like `Logs`). A non-200 → the API's error.
  - **The filename is the server's, sanitised:** its base name only. Empty, `.` or `..` → `<project>-<snapshot>.zip`. A server can't write outside the directory.
- **`houston snapshots download SNAPSHOT [--location NAME] [-o FILE] [--project NAME]`:**
  - It writes `<file>.part` (truncating a leftover), then checks the zip opens (`archive/zip`, which reads its central directory: a cut-off zip has none).
  - Then it links `.part` to the file, which fails if the file exists, and removes `.part`. **It never overwrites a file.** An existing file is refused before any byte is written; closing the response stops the server's restic.
  - "Downloaded snapshot 5c5edd4c of equip to equip-20260921-0300Z-5c5edd4c.zip (1.2 GB)." A refusal, a break, or a zip that doesn't open → exit 1 with the reason, and no file and no `.part` are left.

### AC ↔ test map (Batch 2)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The API download: 200, the zip's headers and bytes; the default location; `?location=`; the log says `token agent` | `test/integration/api_v1_snapshot_downloads_test.rb` `test "downloading a snapshot over the API"` | Contract |
| 2 | Refused: a bad token → 401 with no docker call; unknown project, snapshot or location → 404; no storage → 409; busy → 409; restic failing → 502; each `{error}` | `test "a download over the API that's refused"` | Authz, Contract, Signals |
| 3 | The client: the path and query, the filename (sanitised: `../../x.zip` → `x.zip`; none → the fallback), the bytes; an API error passes through | `internal/server` `TestDownloadSnapshot` | Contract |
| 4 | The CLI: the file under the server's name, or `-o`; no `.part` left; `--location` sent | `internal/cli` `TestSnapshotsDownload` | Contract |
| 5 | The CLI refuses: an existing file → exit 1, untouched; the server refusing → exit 1 with its words, no file; a zip that doesn't open (cut off) → exit 1, "incomplete", no file and no `.part` | `TestSnapshotsDownloadRefused` | Crash & repair, Signals |
| 6 | A file of that name appearing mid-download is never overwritten, and the whole download is kept as `.part`, said so; a drive without hard links (link fails) → checked, then renamed, the same promise | `TestSnapshotsDownloadRefused`, `TestSnapshotsDownloadWithoutHardLinks` | Concurrency, Crash & repair |

### Done (Batch 2)
- **Red:** the API route was missing (404), the Go client didn't compile, and the CLI had no `download` command. **Green:** `bin/rails test test/integration/api_v1_snapshot_downloads_test.rb` → 2 runs, 0 failures; `bin/go test ./internal/server/ ./internal/cli/ -run 'TestDownloadSnapshot|TestSnapshotsDownload|TestSnapshots$'` → ok. gofmt is clean.
- The four response headers moved into `SnapshotExport#headers`, since both controllers set them.
- **Found while testing (row 6):**
  - Swapping `os.Link` for `os.Rename` wasn't caught at first. Go's test server had buffered the whole small response, so the file existed before the CLI saw the headers. The test now flushes the first bytes and waits.
  - The link fails on drives without hard links (exFAT, some network shares). That would have thrown away a finished download, so it falls back to check-then-rename.
  - A file that appears mid-download keeps the finished zip as `.part` rather than deleting it.
- **Mutations, each caught:** the server's filename used as a path; no zip check; `.part` left behind; rename instead of link; `--location` dropped; the fallback without its check.
- The saved file is created `0600`: a snapshot holds the app's data.


## Batch 3: The real run

### Done (Batch 3, port 3000)
- **The run:** `EQUIP_SOURCE=~/code/equip install/test/download-e2e.sh` on a fresh OrbStack machine (ubuntu:noble, Houston installed from this tree). Every check was green except one, a mistake in the script: it expected the size as "1.x GB", and the CLI prints "1 GB". The pattern was fixed and that check rerun on the kept machine: `Downloaded snapshot b4358fce of spike to /tmp/dl/recheck.zip (1 GB).`, and `unzip -t` passed. What it proved:
  - **The CLI's zip:** it's whole (`unzip -t`), and holds `data/data/uploads/photo.jpg`, `data/data/big.bin`, `out/houston.json`, `out/sqlite/1.sqlite3` and `out/postgres/db/globals.sql`. The live SQLite file and its `-wal` aren't in it.
  - **What's inside:** the photo comes out byte for byte. The SQLite copy passes `integrity_check` with all 3 rows, which were only in the `-wal`. `pg_restore --list` reads `we'ird=db`'s dump with table `t`'s data, and `houston.json` names it. A second download to the same file is refused, and the file is untouched.
  - **The page:** the row links Download. The response is an attachment named `spike-…-cbda7174.zip`, chunked with no `Content-Length`, and its zip is **byte for byte the CLI's**.
  - **Streaming:** the first byte arrived after 0.9 s, and all 1,079,075,176 bytes after 20.5 s. **Mission Control grew 61 MiB** while 1 GiB passed through it.
  - **One at a time:** a second download from the CLI → "another download started at 16:37 UTC is still running; try again when it's done". From the page → back to the project with the reason. Neither left a file.
  - **The client leaves:** `houston-export` is gone, it's logged "stopped by the client", and the next download runs.
  - **Broken mid-way** (`docker kill houston-export`): curl exits **18**, so a browser marks the download failed. It's logged "broke after …", and what arrived doesn't open as a zip. Each log line says who: the admin, or the token.
  - **equip (a copy):** deployed GO, backed up, and downloaded. Its 4 SQLite copies pass `integrity_check`, and `houston.json` maps `production.sqlite3`.
- **Through the tunnel:** `install/test/download-through-tunnel.sh`, a stage of `install/test/orbstack.sh` after the other tunnel stages. It backs up and downloads `houston-spike-test` through `https://admin.<base>`, and checks that a download broken on the server reaches curl on this Mac as a failed transfer through Cloudflare.
  - **Run 2026-09-28 (`install/test/orbstack.sh ubuntu:noble`): not reached.** The Cloudflare preflight refused, as it should: `cloudflare-check.env` points at `svnmns.com`, and "admin.svnmns.com answers as a live Houston: a test run would take over its tunnel (houston-svnmns) and delete its records". Step 2 and every tunnel stage were skipped, and nothing on Cloudflare was touched. The install, setup step 1 and rerun checks passed. **Blocked on:** a zone of its own for tunnel tests. Until then, a download through Cloudflare, and a break reaching the client as a failed transfer, are unproven. The CLI's zip check covers a cut-off download either way; a browser relies on Cloudflare passing the break on.

## Later (named, not built)
- **More than one download at a time**, which needs more Puma threads first.
- **Resuming a cut-off download** (Range requests). A zip streamed from restic has no fixed bytes to resume from; it would need the zip written to disk first.

## Decisions (yours)
1. ~~Databases only~~. **Answered:** the whole snapshot, files and databases, in one zip.
2. **One download at a time, server-wide.** Recommended: Puma has 8 threads on a server (the plan first said 3), and a download of a snapshot with images can hold one for many minutes. Two downloads would leave one thread for pages, the API and runners' heartbeats. The second download gets "another download started at 14:02 is still running".
3. **From a snapshot, not a fresh live export.** Recommended: it reuses the consistent copies backups already make and never touches the app. For "now": Back up now, then Download.

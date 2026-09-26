# Plan: update the server from Mission Control

## Your direction
- "We need to get it so I don't need to be home to update the server." Until now an update was `ssh` to the server, then the installer with `sudo`, so it needed the home network. Mission Control is reachable from anywhere (admin.<base domain>, through the tunnel), so the update starts there.
- This is the "later" named in [update-available.md](update-available.md): `houston update`, or a button, with a host-side helper that runs the installer.

## Evidence
- The board already knows when a release is newer: `LatestRelease` (every 6 hours), plus `UpdateNotice.newer` (mission_control/app/models/update_notice.rb:8).
- Mission Control already restarts itself through a helper: `PortSwitch.set!` (mission_control/app/models/port_switch.rb:17) runs a one-off container from the runner image with the Docker socket. That container recreates Mission Control from its own compose.yml, and it outlives Mission Control's restart.
- Mission Control has the host's Docker socket (install/install.sh:449). That's root on the host already, so a helper that runs the installer adds no power it doesn't have.
- The runner image (`docker:29-cli`, Alpine) has busybox's `nsenter`. `nsenter -t 1 -m -u -i -n` from a `--privileged --pid=host` container (already in the host's PID namespace) runs a command in the host's namespaces as root. That's `sudo sh` on the host, with the host's own curl, systemd and packages.
- The installer recreates the runners (install/install.sh:533), so a deploy in flight is abandoned. That's why the update refuses while one is queued or in flight, and why claims stop while it runs (`Deploy.claim_next!`, deploy.rb:156).
- The installer is fail-closed before it changes anything (a failed pull changes nothing). A failure after compose is rewritten can leave Mission Control down. Remotely, nobody could then fix it, hence the rollback below.

## Design (short)
- **`ServerUpdate`**, a table:
  - Columns: `to_version`, `from_version`, `status` (running, go, rolled_back, no_go), `started_at`, `finished_at`, `log`.
  - A unique index on the running one makes it the lock: two clicks start one update.
  - The table also serves as the history.
- **`ServerUpdate.start!(version = latest)`:**
  - Refused, with nothing changed, unless:
    - this server runs a release
    - `version` is a release tag newer than it
    - Mission Control was started by the installer, which its compose labels show (the installer's directory comes from them, shared with PortSwitch)
    - the runner image is known
    - no update is running
    - no deploy, restore or backup is queued or running
  - It saves the running row *first*, then re-checks deploys and backups. That catches a claim that landed between the check and the save; the row is then removed and the update refused.
  - Then it starts the helper `houston-update`:
    - The runner image, `--privileged --pid=host`, and a small script ([update-helper.sh](../../mission_control/lib/update-helper.sh)).
    - The script uses `nsenter` into the host (mount, UTS, IPC and network namespaces) to download that release's own `install.sh` and run it with `HOUSTON_VERSION`, and with the installer's directory and runner count.
    - It runs with the host's `timeout 20m`, under a clean environment, as `sudo` would give it.
    - **If that fails, it reinstalls the version the server ran** and exits 3. It exits 0 on success and 1 if both fail.
    - It isn't removed when it ends (its full log stays readable with `docker logs houston-update`). The next start removes a finished one.
    - It's not part of the compose project, so the installer's `--remove-orphans` doesn't touch it.
- **Deploys and backups wait:** while an update runs, `Deploy.claim_next!` hands out nothing and `BackupRun.claim!` says busy (its job tries again). Queued ones run after the update.
- **`ServerUpdateJob`**, every minute (a no-op unless one is running), inspects `houston-update`:
  - Running: it waits. Past 20 minutes it logs a warning each minute, and the board says it's taking long. The script's timeouts end it within about 40 minutes.
  - Exited 0 while this Mission Control runs the new version: GO.
  - Exited 3: rolled back.
  - Anything else, or the helper gone: NO-GO.
  - It keeps the log's last 20 lines and finishes the row only while it's still running, so the result is written once. That also lets deploys go again.
  - If Docker can't answer, it logs that and tries again next minute.
- **The flight board** reads only the database:
  - The update note gets an **Update to v0.4.3** button, with a confirm step. The command stays for anyone who'd rather SSH.
  - While it runs: "Updating to v0.4.3 since 22:51. Mission Control restarts on the way; deploys and backups wait."
  - For a day after, the result: "Updated to v0.4.3", or the failure with its log lines.
- **CLI-first:**
  - `POST /api/v1/update` (`{"version": "v0.4.3"}`, default the latest known) and `GET /api/v1/update` (the running version, the latest, and the last update).
  - `houston update [vX.Y.Z]` starts it and follows it to its result through Mission Control's restart, trying again for up to 30 minutes while the server doesn't answer. It exits 0 on GO and 1 otherwise.
  - `houston status` says when one is running.

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The helper runs with the release's installer script, the versions, directory and runner count, privileged in the host's PID namespace | `server_update_test.rb` `test "start runs the helper for that release"` | Contract |
| 2 | Refused, with no row and no helper, for: not a release; not newer; a bad tag; no latest known; not installed by the installer; no runner image; the helper didn't start | `server_update_test.rb` `test "refused before anything changes"` | Contract, Preconditions |
| 3 | Refused while an update runs (two starts, one row) | `server_update_test.rb` `test "one update at a time"` | Concurrency |
| 4 | Refused while a deploy, restore or backup is queued or running; a claim landing between the check and the save is caught, and the row removed | `server_update_test.rb` `test "refused while a deploy or backup is busy, even one claimed meanwhile"` | Concurrency |
| 5 | While an update runs, no deploy is claimed and a backup is busy; afterwards both go | `server_update_test.rb` `test "deploys and backups wait for the update"` | At-least-once |
| 6 | The job records each outcome once (GO, rolled back, NO-GO, the helper gone), with the log's last lines, and refreshes the board once; Docker not answering changes nothing | `server_update_job_test.rb` `test "each outcome is recorded once"` | Crash & repair, Signals |
| 7 | Past 20 minutes: logged, and the board says so | `server_update_job_test.rb` `test "a long update says so"` | Stuck-alive |
| 8 | The helper script: success exits 0; a failed install puts the old version back and exits 3; both failing exits 1; each install gets a clean environment and a timeout | `update_helper_script_test.rb` (fake `nsenter` and `curl`) | Crash & repair |
| 9 | The board: the button with its confirm on the note; running and finished states; nothing about it on a current server | `projects_controller_test.rb` `test "the flight board updates the server"` | Contract |
| 10 | Signed in only, and a token for the API; refusals say why | `server_updates_controller_test.rb`, `api_v1_update_test.rb` | Authz |
| 11 | `houston update` starts it, rides out the restart, prints the result, and exits by it; `houston status` shows one running | Go `TestUpdate`, `TestStatusSaysWhenUpdating` | Contract |
| 12 | On a real host (Ubuntu, systemd, Docker): the helper, started as ServerUpdate starts it, runs the downloaded installer on the host as root with only what it's given, reaching the host's Docker and systemd (exit 0); for a release that doesn't exist it installs the old version again (exit 3). A stand-in installer takes the release's place (below) | `install/test/update-e2e.sh` (an OrbStack machine) | Parity |
| 13 | Live: after v0.4.3 is on the server by hand, the next release updates from the board through admin.<base domain> | the live check, recorded here | Parity |

## Evidence
- **Tests:**
  - Rails: 399 runs, 0 failures; rubocop clean.
  - Go: `bin/go test ./...` passes; gofmt clean.
  - The new tests: `server_update_test.rb`, `server_update_job_test.rb`, `update_helper_script_test.rb`, `server_updates_controller_test.rb`, `api_v1_update_test.rb`, the board's `test "the flight board updates the server"`, and Go `TestUpdate`, `TestUpdateRefusedAndTimedOut`, `TestStatusSaysWhenUpdating`.
- **Mutation check:** 21 in Rails and 6 in Go, each caught by a test. They include:
  - claims or backups not held while it runs
  - no check after saving
  - the row left behind on a refusal
  - no release check, no newer check, no installer check
  - the directory hardcoded
  - GO without the new version running
  - a rollback counted as GO
  - the result written twice
  - Docker trouble counted as a result
  - no 20-minute note
  - results shown forever
  - the script's rollback, clean environment, timeout and exit codes
  - the button shown while one runs
  - the API taking any `version`
  - the CLI following the wrong update, repeating the restart note, calling a failure GO, or dropping `version`
  - `houston status` not saying it's updating
- **The mutations it found:**
  - Refusing on the first busy check was redundant (the check after saving refuses the same), so it was cut.
  - The "not a release" message and the result written twice by two checks at once weren't pinned by a test; now they are.
- **On a real host** (`install/test/update-e2e.sh`, 2026-09-25): all 10 checks passed.
  - Why a stand-in installer: the release images are amd64 only, OrbStack's amd64 machines can't run containers ("unable to apply cgroup configuration"), and an arm64 machine can't pull the images.
  - So the host's `curl` is wrapped to hand out a stand-in installer.
  - The real installer's own run is covered by the live check (13). It uses nothing a clean environment takes away (`grep` for `SUDO_`, `USER`, `TERM`: none).
- **Visual check** (a throwaway Mission Control on port 3040), at 800 px and 375 px, with no overflow:
  - the note with **Update to v0.4.3** and the command
  - updating, and taking long
  - rolled back, with its log
- **The live check (13), first half, 2026-09-25:**
  - v0.4.3 released green, and the server was updated to it by hand after an idle check. Mission Control, both runners and all three CLIs run v0.4.3, pinned by digest.
  - `settle_server_update` is scheduled every minute, and Mission Control knows the runner image.
  - The four apps and admin/up answered 10 × 200 each.
  - `houston update` from the laptop reached the new API and was refused, with nothing started: no newer release yet.
  - The second half, an update from the board, waits for the next release.
- **Fixed in passing:** at 375 px, a pre-flight check's error with a long URL in it overflowed by 12 px. It now wraps.

## Batch 2: Houston's page, instead of banners (2026-09-26)
Your direction:
- "Instead of the massive banner we have now, I think having a refresh icon next to the version would be good… a small update link… After update, the new version would be displayed."
- "It would be nice to have some indicator of how the deploy is going."
- "What if clicking on the version took you to a deploy log page that looked like the app deploy/logs but for the server. It's also where we could allow the user to manually check for new version."

What changed:
- **The flight board:**
  - The version in its eyebrow leads to Houston's page (`/update`).
  - A small pill beside it says what's up, and leads there too: "v0.4.5 available", "Updating to v0.4.5 · Pulling Houston v0.4.5" (refreshing every 10 seconds), or a red "Update to v0.4.5 failed" for a day.
  - The UPDATE banner, with its command to copy, is gone, and so are the GO and NO-GO update banners.
- **Houston's page**, laid out like a deploy's:
  - The version, the latest release and when it was checked, with release notes.
  - **Check now**, which asks GitHub right away and says what it found in a toast.
  - **Update to vX.Y.Z**, with a confirm step.
  - The installer's steps: DONE, RUNNING, or FAILED at the step that broke. After a rollback, the old version's own steps are done.
  - The log in the deploy page's panel: LIVE, Follow and Copy log, with a refresh every 5 seconds while the update runs.
  - The last 10 updates, each opening its own log (`?id=`).
- **Progress:**
  - While an update runs, `ServerUpdateJob` follows it every 5 seconds, starting from `start!` (its queued jobs outlive Mission Control's restart). It saves the helper's log and its latest `==>` step, and refreshes the board when the step changes.
  - The helper's rollback lines are `==>` steps now.
  - The log kept is the helper's last 500 lines, not 20.
- **Toasts:** the check and a started or refused update say so in a toast on Houston's page. The board never showed flash messages, so "Updating to…" had gone nowhere.
- **The refresh controller** fetches before it refreshes, so while Mission Control restarts, a Cloudflare error page can't replace the page and stop the refreshing. Tried by stopping a server under an open page: the page stayed.
- **CLI-first:**
  - `houston update --check` (`POST /api/v1/update/check`) does what **Check now** does.
  - `houston update` prints each step as it goes.
  - `GET /api/v1/update` returns the step.
- **Cut:** `LatestRelease.update_command`, which had no caller once the banner went.

Evidence:
- Rails: 407 runs, 0 failures; rubocop clean. Go: `bin/go test ./...` passes; gofmt clean.
- New or rewritten tests:
  - `ServerUpdatesControllerTest` ("Houston's page: the version, checking, and updating", "Houston's page follows an update's log", check and sign-in)
  - `ServerUpdateJobTest` ("a running update says what it's doing", "while one runs, it's checked every 5 seconds")
  - `ProjectsControllerTest` (the version link and its pills)
  - `ApiV1UpdateTest` (check, step)
  - Go `TestUpdateCheck`, `TestUpdatePrintsSteps`
- Mutation check: 17, each caught.
  - Among them: the refresh on every log change, the log not saved, the failed-step rules, the follow chain, the Update button while one runs, `?id=`, the failed pill, the check's wording, the step in the API, and the CLI's step printing.
  - One survived at first because the test's "older" update had the newer id. Now `?id=` is tested against the other update.
  - One condition, preferring the running update over the newest, was redundant and was cut: the running one is always the newest.
- Visual check (a throwaway Mission Control) at 800, 1280 and 375 px, with no overflow:
  - the board's pills
  - Houston's page while updating, and with v0.4.5 out
  - the toast from a real check against GitHub

## Batch 3: in Settings (2026-09-26)
Your direction: "instead of clicking on the version, let's move this to settings. Create a deploy log table in settings. In the top right, let's have a button to check for updates. If an update is available, it should display an update button. Clicking on the log would take you to a log screen."

What changed:
- **Settings › Houston**, a section between Cloudflare and Port 3000:
  - Its head shows the running version, **Check for updates**, and **Update to vX.Y.Z** when one is out (not while one runs).
  - A line says what's known: out (with release notes), the latest, or updating with its step (refreshing every 10 seconds).
  - Below, the updates, in the deploy history's rows: #, version, from, when (or how it failed), GO / NO-GO / UPDATING, how long, and **Log**.
- **An update's log page** (`/settings/updates/:id`) is Houston's page from Batch 2, cut down to one update, laid out like a deploy's:
  - "Update #N", from → to, how long it took, and the steps.
  - The live log.
  - Crumbs back to Settings › Houston.
- **Toasts:** check results and a started or refused update are toasts (`flash[:toast]`) in Settings and on the log page. A started update opens its log page.
- **The flight board:** the version is plain text again. The pills stay: "available" leads to Settings › Houston, and "updating" and "failed" lead to that update's log.
- **Gone:** `/update` and `ServerUpdatesController`, now `Settings::UpdatesController`.

Evidence:
- Rails: 405 runs, 0 failures; rubocop clean. Go unchanged, and it passes.
- `test/controllers/settings/updates_controller_test.rb`:
  - the section with its buttons and rows
  - while one runs
  - the log page, with the failed-step rules
  - check with its toasts
  - update opens its log, refused says why
  - signed out
- The board's tests follow the pills' new links, and the Settings menu test lists Houston.
- Mutation check: 8, each caught. Among them: the Update button while one runs, the section's and log page's refresh, the rollback row's words, the check's NO-GO toast, a started update opening its log, and the pills' links.
- Visual check (a throwaway Mission Control) at 1280 and 375 px: the section with three updates (GO, a rollback, GO), and a rollback's log page.
  - At 375 px the section's buttons overflowed by 12 px. They wrap now.

- **Renamed:** the section is **Settings › Releases** (`#releases`), your call ("that makes a lot more sense"). It sits after Port 3000, in the menu's alphabetical order. Live check: the server updated itself v0.4.5 → v0.4.6 from Houston's page (update #3, GO in 23 s).

- **Settings › Port 3000 is Settings › Security** (`#security`), your call, so other security settings can join it. Port 3000 is its first group, under a "PORT 3000" subheading (`#port`, so old links still land). The installer's messages and the docs say Settings › Security. `install/test/install-bind.sh` and `install-version.sh` pass.

- **Check for updates refreshes in place** (your ask: it "reloads the page and causes it to scroll to the top").
  - The cause: the redirect back to `/settings#releases` goes through `fetch`, which drops the `#releases`.
  - The fix: Settings refreshes with a Turbo morph and keeps the scroll, and the Check form submits as a replace visit, which Turbo 8 treats as a page refresh.
  - Checked by clicking it from JavaScript with Releases 20 px from the top of the window: afterwards it was still 20 px from the top, and the scroll was unchanged (936). The page didn't reload (a marker on `window` survived), and the toast showed.
  - (The browser tool's own click scrolls a button to the middle of the window first, which looked like a jump.)
  - Update keeps a normal visit, so opening an update's log adds to the history and Back works.

- **The menu keeps its place too** (your report: checking "clears the selected navigation"). The morph puts the Settings menu back as the server sent it, with nothing marked, and the menu's controller survives the morph without marking it again. It now marks the section in view again on `turbo:morph`. Checked in a browser: Releases chosen, then Check for updates, and Releases stays marked. With the old controller, the same steps left nothing marked.

## Deploy notes
- The server gets this with one more update by hand (v0.4.3). After that, updates start from the board or `houston update`.

## Later (named, not built)
- Automatic updates when the server is idle (a Settings choice, off by default).

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

## Deploy notes
- The server gets this with one more update by hand (v0.4.3). After that, updates start from the board or `houston update`.

## Later (named, not built)
- Automatic updates when the server is idle (a Settings choice, off by default).

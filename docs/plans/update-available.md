# Plan: "update available" on the flight board

## Your direction
- "Build the update available note on the flight board" (option 1 of three; a one-command or automatic update are later).

## Goal
When the server runs a release and a newer one is out, the flight board says so, with the release's notes and the exact command to update. `houston status` says it too. Nothing updates by itself.

## Design (short)
- **`LatestRelease.check!`**, a recurring job every 6 hours:
  - It reads `GET https://api.github.com/repos/<HOUSTON_REPO>/releases/latest`, the repo defaulting to `scttymn/houston` as in the installer. GitHub leaves prereleases out of "latest".
  - It keeps `latest_release`, `latest_release_url` and `latest_release_checked_at` on `Installation`.
  - Only a well-formed tag and an https URL on github.com are kept.
  - A failure (network, rate limit, bad JSON) is logged, and the last known release stays.
  - A new release refreshes open flight boards live.
- **Never at render time:** the board reads only the database, so GitHub being slow or down never slows the board.
- **`UpdateNotice.for(current, latest)`:** a note only when the current version is a release (`vX.Y.Z`) and the latest is newer by version number (v0.1.10 > v0.1.9). A server built from a checkout (`source …`) or `dev` gets no note.
- **The board:** a notice above the projects. "UPDATE: v0.1.1 is out. This server runs v0.1.0." It links to the release notes and shows the update command with a Copy button:
  `curl -fsSL https://github.com/<repo>/releases/latest/download/install.sh | sudo HOUSTON_VERSION=v0.1.1 sh`
- **CLI-first:** `/api/v1/me` returns `latest` (the newer release, or null), and `houston status` prints "Houston v0.1.0 at svnmns.com (v0.1.1 available)".

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The check stores the latest release's tag and URL | `latest_release_test.rb` `test "the check keeps the latest release"` | Contract |
| 2 | A malformed tag, a URL off github.com, a failure or a timeout keeps the last known release, and is logged | `latest_release_test.rb` `test "a bad answer keeps what we knew"` | Contract, Signals |
| 3 | A new release refreshes the flight board; the same one again doesn't | `latest_release_test.rb` `test "a new release refreshes the board"` | Scale |
| 4 | A note only for a release behind a newer one, compared by version number | `update_notice_test.rb` `test "only a release behind a newer one"` | Contract |
| 5 | The board shows the note with the notes link, the command and Copy; none when current | `projects_controller_test.rb` `test "the flight board says when an update is out"` | Contract |
| 6 | `/api/v1/me` returns `latest`, and `houston status` prints it | `api_v1_auth_test.rb`; Go `TestStatusPrintsServerVersion` (extended) | Contract |
| 7 | The job is scheduled every 6 hours | `latest_release_test.rb` `test "the release check is scheduled"` | Whole batch |
| 8 | Live: once the server runs a release with this, its check reaches GitHub and stores the latest; the note appears when a newer release comes out | the live check, recorded here | Parity |

## Evidence
- **Tests:** Rails 324 runs, 0 failures (rubocop clean); Go passes (gofmt clean).
- **Mutation check.** Each of these fails a test:
  - versions compared as text
  - an older release counted as newer
  - any release URL accepted
  - a refresh on every check
  - a failed check not logged
- **Visual check** (the rendered board): the note sits under the header, the command has its own line with Copy beside it, and nothing overflows from 320 to 1280 px.

- **The live check (8), 2026-09-24:**
  - `v0.2.0` (3dc7788) released green, and the production server updated to it with the README's command.
  - Mission Control and both runners run `…:v0.2.0`, and the CLI on the host and both runners says `v0.2.0`.
  - The migration ran, and `check_latest_release` is scheduled "every 6 hours".
  - The check, run once by hand on the server, reached GitHub and stored `v0.2.0` (its release page, 22:18:59 UTC). No note, since it runs the latest.
  - The flight board reads "FLIGHT BOARD · SVNMNS.COM · V0.2.0" with no update note, and `houston status` says "Houston v0.2.0 at svnmns.com".
  - The four apps each answered 10 × 200.
  - The note itself shows when the next release comes out (within 6 hours).

## Later (named, not built)
- `houston update --server`, or a button (a host-side helper that runs the installer).
- Automatic updates, when the server is idle.

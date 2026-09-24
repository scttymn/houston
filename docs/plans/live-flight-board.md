# Plan: the flight board updates live

## Your direction
- "It would be cool if the flight board updated in real time." The flight board showed equip as QUEUED until a reload, though its deploy had already gone GO.

## Goal
The Projects page (the flight board) redraws by itself when something it shows changes: a deploy is queued, starts, moves a step or finishes; a backup run starts or finishes; a project is linked; maintenance goes on or off. No reload, and no polling.

## What's there (evidence)
- A deploy's page already updates live, through Turbo Streams over Solid Cable (`DeployBroadcast`, `turbo_stream_from @deploy`).
- The flight board already asks Turbo to redraw in place when it refreshes (`turbo_refreshes_with method: :morph, scroll: :preserve`). It only refreshes today while a HOLD banner polls.
- The log is appended with `update_all` (`Deploy#append_log`), so log chunks trigger no callbacks. Heartbeats do: every report saves the deploy.

## Design (short)
- The board subscribes to one stream, `flight_board` (`turbo_stream_from :flight_board`).
- When something it shows changes, Mission Control sends that stream a **refresh**, not the markup. Each open board then re-fetches itself and morphs in place: the same page, the same data rules, and scroll kept. Turbo debounces bursts of refreshes into one.
- **Refresh, on change:**
  - a deploy is queued, or its step or status changes (not a log chunk, not a heartbeat)
  - a backup run's status changes
  - a project is created, linked or removed, or its maintenance changes
- One place sends it: `FlightBoard.refresh!`, called after the commit by the code that makes those changes. Sending it is best effort: a failure is logged and never fails the change.

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The board subscribes to `flight_board` and morphs on refresh | `projects_controller_test.rb` `test "the flight board listens for changes"` | Contract |
| 2 | Queueing a deploy (a push, Check for changes, the Deploy button) refreshes the board | `flight_board_test.rb` `test "a queued deploy refreshes the board"` | Contract |
| 2b | A deploy started from the CLI (created in flight, the status column's default) refreshes it too | `flight_board_test.rb` `test "a deploy started from the CLI refreshes the board"` | Whole batch |
| 3 | A runner's step or status report refreshes it; a log-only report or heartbeat doesn't | `flight_board_test.rb` `test "step and status refresh; log chunks don't"` | Scale |
| 4 | A backup run's start and finish refresh it; its heartbeats don't | `flight_board_test.rb` `test "backup runs refresh on status changes"` | Scale |
| 5 | Linking a project, and maintenance on or off, refresh it | `flight_board_test.rb` `test "projects and maintenance refresh the board"` | Contract |
| 6 | A failed broadcast is logged, and the change it followed still succeeds | `flight_board_test.rb` `test "a failed refresh never fails the change"` | Signals |
| 7 | Checked in a browser: two tabs, a deploy in one changes the board in the other | the live check on the production server, recorded here | Parity |

## Evidence
- The suite: 310 runs, 0 failures; rubocop clean.
- Mutation check: each of 13 mutants (removing a trigger, the rescue or the subscription, or refreshing on every save) fails a test. One mutant survived at first: dropping "a new deploy" didn't matter, because every new deploy sets its commit. That condition was cut, and the CLI start test was added (2b).
- The live check (7), on the production server (4a2a6f1), 2026-09-24: a board open in the user's browser subscribed to `flight_board` ("Turbo::StreamsChannel is streaming from flight_board"). A refresh sent at 17:25:11 UTC (Solid Cable message on `flight_board`) was followed in the same second by that browser's `GET "/"`, the board re-fetching itself without a reload.
- Then a visible change: with the board open in the browser pane (a marker set on the page, to prove no reload), a manual snapshot of equip was requested on the server (backup run #11). Within 10 seconds the board's Last backup for equip went from "24 Sep 11:31 · deploy · 321 KB" to "24 Sep 12:26 · auto · 321 KB", and the marker was still there.
- Then a real deploy: merging equip's PR #8 (CI actions) queued deploy #5 (216aa0e) at 12:30:15 CDT. The user watched the board go QUEUED → IN FLIGHT ("2772f2f → 216aa0e", "Deploying") → GO without a reload, in the pane too (marker kept). equip's /up answered 200 on 10 of 10 checks afterwards.

## Later (named, not built)
- The **"Deploying · T+00:52"** clock ticks only when the board refreshes (each step). A per-second tick would be a small Stimulus timer.
- **The project page** could listen the same way (its Deploy history and Snapshots).

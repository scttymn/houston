# Rebuild: a deploy that builds from scratch

## Direction
From the gantry work on Equip (2026-09-29): after the server's Proxmox VM moved to the `host` CPU type, redeploying Equip made no AVIF copies. Its build steps were Docker layer-cache hits from the builds before the change, as an empty commit changes no file. "sounds like an opportunity for a gantry rebuild command that starts fresh" … "or a houston rebuild --server" … "1. absolutely" (a Rebuild button in Mission Control too) "2. you can release and I can update mission control."

## Evidence
- The deploy builds the production image with `docker build --target production …` (internal/deploy/deploy.go:562), Docker's layer cache on: a commit that changes no file reuses every layer.
- Step 00 runs `houston -f <compose> test` (internal/deploy/deploy.go:373), which builds the test image with `compose run --build` (internal/cli/test.go:54), the layer cache on too.
- On the server: Equip's images for `4d9f7f9`, `02f4539` and `1935e0f` were all created 22:03:12, before the VM's restart at 22:21 (`docker images` there).
- Deploy now is `ChangeCheck#queue_head!` → `Deploy.queue!` (mission_control/app/models/change_check.rb:38, deploy.rb:121), from the web (DeploysController#create) and the API (Api::V1::DeploysController#create); a runner claims it (Api::RunnerJobsController#claim → `job`).

## Design
A rebuild is a deploy with `fresh` set, carried from the request to the runner:
- `deploys.fresh` (boolean, default false). `Deploy.queue!(…, fresh:)`: a new deploy takes it; a deploy already queued becomes fresh when a rebuild is asked for, and stays so when a push moves it to a newer commit (a push never undoes a rebuild asked for).
- `ChangeCheck#queue_head!(fresh: false)`; the web's Deploy and the API take `fresh`. The project page gets a Rebuild button beside Deploy. A fresh deploy's name is "Rebuild" (its page, its history row, `houston deploys`).
- The runner's job carries `fresh`. A fresh deploy builds both images with `--no-cache`: step 00 runs `houston test --no-cache` (which builds the test service with `compose build --no-cache` before running), and the production build passes `--no-cache`. BuildKit's cache mounts (`RUN --mount=type=cache`) are kept, as they are by `--no-cache`: a gantry app's resized pictures are still only made where missing.
- CLI: `houston rebuild`, as `houston deploy` (on the server, as houston; `--server` from anywhere, with `--project` and `--follow`). `houston test --no-cache` for step 00, and for a person.
- The API's deploy (RemoteView.deploy) says `fresh`.

## Acceptance criteria → tests
| Criterion | Test |
| --- | --- |
| A rebuild queues a fresh deploy; one already queued becomes fresh, and stays fresh when a push moves it | `DeployTest` "a rebuild queues a fresh deploy" |
| The API's POST deploys takes `fresh`, and its deploy says `fresh` | `ApiV1ActionsTest` "rebuild queues the head, fresh" |
| The web's Deploy takes `fresh`; the project page has Rebuild; a fresh deploy's name is Rebuild | `DeployPagesTest` "Rebuild queues a fresh deploy, named Rebuild" |
| The runner's job carries `fresh` | `ApiClaimsTest` "a rebuild's job says fresh" |
| A fresh deploy builds with `--no-cache`, and runs `houston test --no-cache`; another doesn't | `deploy.TestFreshBuild` |
| `houston test --no-cache` builds with `--no-cache` first, and a failed build fails the tests | `cli.TestTestNoCache` |
| `houston rebuild --server` asks for a fresh deploy; `houston deploy --server` doesn't | `cli.TestRebuildServer` |
| `houston rebuild` on the server deploys fresh | `cli.TestRebuildLocal` |

## Evidence (2026-09-29)
- Tests first: the Rails ones failed for the right reasons (no `fresh?`, `queue!` taking no `fresh`, no Rebuild button, the API ignoring `fresh`), then passed. Go: `TestFreshBuild`, `TestTestNoCache`, `TestRebuildServer`, `TestRebuildLocal`.
- Suites: `bin/go test ./...`, gofmt clean; Mission Control 524 runs, 0 failures, rubocop clean.
- Mutation check: all 18 caught (9 in Go: the image's and the tests' `--no-cache`, fresh from the claim and from the command, `houston test --no-cache`'s build and a failed one, the request's body both ways, the rebuild command; 9 in Rails: a new deploy's fresh, a push undoing it, a queued deploy not becoming fresh, the name, the API, the web, `ChangeCheck`, the claim, the button).
- Visual check (a throwaway Mission Control on port 3040, a project with three deploys, one a rebuild): the project page at 1280 px shows Rebuild beside Deploy and "main · Rebuild" in the history, no overflow; a rebuild's page is "Rebuild #2". **Found by it:** at 375 px the second button pushed the Deploy history head 10 px past the page; the head now wraps, as the settings sections' do, and at 1280 it stays on one line.

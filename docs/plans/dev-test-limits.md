# Plan: production's resource limits only where production's image runs

## Direction
- "I just don't get why a container needs to be big enough for a build. The output can be much smaller than the build requires."
- "I shouldn't have to have an artificially large app just to placate builds."
- Weighing a separate, uncapped builder beside a capped dev container against dropping the limits in dev: "sounds like option 1 is better for dev since it runs with different preferences anyway." `houston dev --production` keeps the limits, so production's image is still checked under them locally.

## Evidence
- **`houston dev` inherits production's limits.** `DevOverride` (`internal/variant/dev.go:31`) sets only the build target and the route, so `deploy.resources.limits` from compose.yml applies to the dev container.
  - valleybuiltcrossfit's app is capped at `cpus: "0.5"`, `memory: 384M`, sized for its production binary (10–17 MiB, and a photo resize peaking near 300 MB).
  - Its dev stage runs `templ generate --watch --cmd="go run /app/cmd/web"`. On this laptop (60 GiB), the compile was killed: `modernc.org/sqlite/lib: …/compile: signal: killed`, and the kernel logged `Memory cgroup out of memory: Killed process 51416 (compile)`, anon-rss 287800kB, in the app container's cgroup.
- **`houston test` inherits them too.** `TestOverride` (`internal/variant/dev.go:68`) sets the test target, resets ports and keeps named volumes. The limits pass through, locally and on the server, where step 00 of a deploy runs `houston test` (`internal/deploy/deploy.go:371`).
  - valleybuiltcrossfit's deploys #3 and #4 failed that way. Its commit `b5787bf` works around it: the test stage compiles every package's tests at build time, and `bin/run-tests` runs the binaries inside the limit.
- **Deploys don't read the overrides.** Kamal's config takes the limits from compose.yml directly (`limits`, `internal/kamal/kamal.go:558`; `TestResourceLimits`, `internal/kamal/kamal_test.go:172`). Image builds run in BuildKit, outside any container's limits.
- **`houston dev --production`** builds the production target (`ProductionOverride`, `internal/variant/dev.go:148`), which leaves the limits in place.
- **Compose can remove them in an override.** Checked with Docker Compose 5.5.1: `deploy: { resources: { limits: !reset {} } }` in an override leaves `deploy: {}` in `docker compose config`, with no limits. The other services' limits are untouched.
- Houston allows only `deploy.resources.limits` (`cpus`, `memory`) under `deploy` (`docs/plans/cli-local.md`, allowed service keys), so resetting `limits` removes everything the key can set.

## Design
- **`houston dev`** (the dev target): the app service's `deploy.resources.limits` is reset, when compose.yml sets it.
- **`houston test`** (the test target): the same, locally and on the server.
- **Kept:**
  - `houston dev --production`: the production image under production's limits.
  - Deploys: unchanged.
  - Accessories (databases, caches) in dev and test: they run the same image as in production, so their limits still describe them.
- The reset appears only when the app has limits, so an app without them gets the same override as today.
- **Docs:** README's resource-limits line says the limits apply on the server and in `houston dev --production`, not to `houston dev` or `houston test`, where the app's container also builds and runs its tests.
- **Trade-off:** a test run on the server is no longer capped, so a heavy suite competes with live apps while it runs. Runners take one deploy at a time each. A Houston-wide ceiling for test runs is later work, if it's needed.
- **Out of scope:** reverting valleybuiltcrossfit's precompiled tests (`b5787bf`). It still works, and is the app's own choice.

## AC ↔ test
| # | Acceptance criterion | Test |
|---|---|---|
| 1 | The dev override resets the app's limits when compose.yml sets them, both when routed by name and with `--ports` | `internal/variant` `TestDevOverride_ForcesDevTarget`, `TestDevOverrideRoutesByName` |
| 2 | The test override resets the app's limits when compose.yml sets them | `internal/variant` `TestTestOverride_Phoenix` |
| 3 | An app without limits gets no reset; an accessory's limits are never reset, in dev or test | `internal/variant` `TestOverridesLeaveOtherLimits` |
| 4 | `houston dev --production` keeps the app's limits | `internal/variant` `TestProductionOverride` (phoenix case, whose app has limits) |
| 5 | Real Docker: the dev and test containers have no memory limit, and the `--production` one has compose.yml's | checked by hand, recorded here |
| 6 | valleybuiltcrossfit's `houston dev` builds and serves at `http://valleybuiltcrossfit.localhost` with no manual `docker update` | checked by hand, recorded here |

## Evidence
- **Tests first:** `TestDevOverride_ForcesDevTarget`, `TestDevOverrideRoutesByName` and `TestTestOverride_Phoenix` failed on the missing `limits: !reset {}` before the change. `TestOverridesLeaveOtherLimits` and the `--production` case passed, as guards against resetting too much.
- **Suites:** `bin/go test ./...` ok, gofmt clean, `bin/test-integration` ok.
- **Mutation check.** All 8 were caught:
  - never resetting; dev skipping it; test skipping it
  - test resetting every service; each service's own limits reset (a database's too)
  - resetting when the app has no limits
  - `--production` resetting too
  - (a first try at "resetting without limits" only failed to compile, which proves nothing, so it was redone as valid code)
- **Checked by hand on this laptop (Omarchy, rootless Docker 29.7.2, Compose 5.5.1), with valleybuiltcrossfit:**
  - `houston dev` recreated the app container with `Memory=0 NanoCpus=0`, and it answered 200 at `http://valleybuiltcrossfit.localhost`.
  - A from-scratch compile inside it (`GOCACHE=/tmp/fresh-cache go build ./cmd/web`, sqlite included) took 13 s. Under the 384M limit the same compile was killed.
  - `houston test` passed. Compose resolves the test override's app to `deploy: {}`.
  - `houston dev --production` ran with `Memory=402653184 NanoCpus=500000000` (384M, half a CPU), and answered 200. Its project and volume were removed afterwards.

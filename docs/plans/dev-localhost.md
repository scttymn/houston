# Plan: `houston dev` at `<name>.localhost`, with no ports to think about

## Direction
- "Instead of fighting over ports on my localhost and worrying about collisions, I was thinking we could support `<appname>.localhost` for development work … `houston dev` in equip would launch the app at `equip.localhost` instead of localhost:3000."
- "I don't want to have to even have a hosts port configuration or think about ports during dev work. It should run in a docker container and be accessible via `*.localhost`."
- "an nginx/caddy whatever container that answers to `*.localhost`. This could be something like `equip.localhost` or `feature1.equip.localhost` (for working on a feature branch) … Running `houston dev` will register the new houston app with the service."
- A branch's own database: "def preferred. a copy would be fantastic so we don't have to worry about missing data."
- Databases: "I use sqlite and postgres typically. We can start with those two." The server's snapshots already copy both consistently (`sqlite3 .backup`; `pg_dump` plus roles, `docs/plans/volumes-backups.md`). MySQL, MariaDB and other engines on the server are later work, named below. A dev branch copy pauses main's containers while it copies, which is consistent for SQLite and Postgres (and for others) without engine-specific code.

## Evidence
- **Resolution:** macOS resolves `*.localhost` to this machine (`dscacheutil -q host -a name equip.localhost` gives localhost). Browsers do so on every OS. On Linux, systemd-resolved does it too.
- **Rails allows `.localhost` in development by default** (`ALLOWED_HOSTS_IN_DEVELOPMENT = [".localhost", ".test", …]`, actionpack 8.1 `host_authorization.rb:23`). Other stacks' host checks are the app's to configure; Houston doesn't know frameworks.
- **Houston needs a published port today:**
  - `appPort` (`internal/project/project.go:598`) wants exactly one `ports:` entry, or `x-houston.app_port`.
  - `announceWhenUp` (`internal/cli/dev.go:225`) builds its URL from the published port.
  - The dev override (`internal/variant/dev.go:20`) only sets the build target, so published ports collide between projects.
- **equip** publishes `127.0.0.1:3000:3000`. Its `x-houston.app_port: 80` is production's port, where dev's rails server is on 3000. So there are two ports: the one the dev container listens on, and production's.
- **Compose has `expose:`:** a container's port, published to no host. It isn't in Houston's allowed service keys yet (`allowedServiceKeys`, `project.go:331`).
- Port 80 is free on this Mac (OrbStack). kamal-proxy is what production runs in front of apps.

## Goal
`houston dev` serves the app at `http://<name>.localhost`, and a branch at `http://<branch>.<name>.localhost`, with any number of projects and branches side by side. A branch starts with a copy of the main instance's data. `compose.yml` names only the port the app listens on in its container (`expose`), and no host ports.

## Design
- **The app's ports:**
  - The **dev port** is `expose`'s one entry, or else the target of `ports`' one entry (existing files keep working).
  - The **production port** is still `x-houston.app_port`, defaulting to the dev port.
  - `expose` becomes an allowed service key. `ports` is optional.
- **`houston-dev-proxy`:** one kamal-proxy container (pinned by digest, like the server's images), published on `127.0.0.1:${HOUSTON_DEV_PORT:-80}` and attached to a Docker network `houston-dev`.
  - `houston dev` creates the network and starts the proxy if they're missing, and restarts the proxy if it's stopped.
  - If the port is taken, it stops **before starting the app**, and says so: set `HOUSTON_DEV_PORT=8080` and the URL becomes `equip.localhost:8080`.
- **The dev override:**
  - Every service's published ports are reset, as `houston test` already does. `--ports` keeps compose's ports, for a tool that needs one.
  - The app service joins `houston-dev` with the alias `<name>-dev`, or `<name>-production` with `--production`.
- **The route:** once `compose up` has started, Houston runs `kamal-proxy deploy <name> --target <alias>:<port> --host <name>.localhost --health-check-path <health>` in the proxy.
  - kamal-proxy waits for the health path, so the route switches only to a healthy app. Houston then prints `houston: equip is up at http://equip.localhost`.
  - It waits up to 10 minutes. If the app never passes, the app keeps running (so you can read its logs) and Houston says the route isn't set.
- **On exit** (Ctrl-C, or compose ending), Houston removes the route. A crash leaves at most a stale route, which the next `houston dev` replaces.
- **Instances, one per branch:**
  - The **instance** is the git branch. The deploy rule's branch (else `main` or `master`) is the main instance: `equip.localhost`, Compose project `equip`, as today.
  - Any other branch is `<slug>.equip.localhost` and Compose project `equip-<slug>`. The slug is lowercase `[a-z0-9-]`, `/` and `_` become `-`, and it's cut to fit a 63-character DNS label.
  - `--as <name>` chooses it. `--production` appends `-production` to the name (`equip-production.localhost`, `feature1.equip-production.localhost`).
  - Not a git checkout, or a detached HEAD: the main instance.
  - A route already held by another checkout's running containers: Houston says so and suggests `--as`, rather than taking it over silently.
- **A branch's data is a copy of main's:**
  - On an instance's first run (none of its named volumes exist yet), each of the main instance's named volumes (`equip_storage`) is copied into the branch's (`equip-feature1_storage`), before `compose up`.
  - If the main instance's containers are running, they're paused (`docker compose -p equip pause`) for the copy and unpaused after, even when the copy fails. The copy is then crash-consistent, which Postgres, MySQL and SQLite all recover from on start.
  - The copy runs in a helper container (`cp -a`). Bind mounts aren't copied: they're the checkout itself.
  - If main has no volumes yet, the branch starts empty, and Houston says so.
  - Later runs reuse the branch's own volumes. `--fresh` copies main's again, replacing the branch's, after it names the volumes it's about to replace.
- **`houston init`** writes `expose: ["<port>"]` for a new app service, not `ports`.
- **Docs:**
  - The README and docs/agents.md say to reach the app at `<name>.localhost`, drop "if the laptop's port is taken, publish another", and explain `HOUSTON_DEV_PORT` and `--ports`.
  - "Things that bit us" gains: allow `<name>.localhost` in your framework's development host check (Rails already does), and an app that builds absolute URLs from a configured host needs `<name>.localhost` there.
- **Out of scope, named for later:**
  - Other services by name (`<service>.<name>.localhost`, such as a mail catcher's web UI).
  - HTTPS locally.
  - A database client on the laptop reaching a dev database (`--ports`, or `docker compose exec`).
  - Server snapshots of MySQL and MariaDB (`mysqldump --single-transaction`, like the Postgres path), and a brief pause for other engines' volumes.

## Batches
1. **Ports:** `expose`, the dev port, and `houston init` (rows 1, 7).
2. **The override and instances:** ports reset, the `houston-dev` network and alias, and names from the branch (rows 2, 9).
3. **The proxy and routes:** start or reuse it, port taken, register and announce, clean up, unhealthy, name in use (rows 3–6, 11).
4. **Branch data:** copying main's volumes, the pause and unpause, `--fresh` (row 10).
5. **Real Docker and docs:** the integration tests (rows 8, 12), the README and docs/agents.md, the check by hand (row 13).

## AC ↔ test
| # | Acceptance criterion | Test |
|---|---|---|
| 1 | `expose` is allowed; the dev port comes from `expose`, or from `ports`' target; the production port from `app_port`, defaulting to the dev port; no port at all is a clear problem | `internal/project` `TestAppPorts` |
| 2 | The dev override resets every service's published ports, and joins the app to `houston-dev` with its alias; `--ports` keeps them; `--production` gets its own alias | `internal/variant` `TestDevOverrideRoutesByName` |
| 3 | `houston dev` creates the network and proxy when missing, starts a stopped proxy, reuses a running one | `internal/cli` `TestDevStartsTheProxy` |
| 4 | Port 80 taken: nothing starts, the message names `HOUSTON_DEV_PORT`; `HOUSTON_DEV_PORT` moves the proxy and the URL | `internal/cli` `TestDevPortTaken` |
| 5 | The route is registered with the right target, host and health path; the URL is announced once it's up; the route is removed on exit | `internal/cli` `TestDevRoutesAndCleansUp` |
| 6 | An app that never passes its health path keeps running, and the message says the route isn't set | `internal/cli` `TestDevUnhealthyApp` |
| 7 | `houston init` writes `expose`, not `ports` | `internal/cli` init tests |
| 8 | Real Docker: two fixture apps at once, each answers at its own `<name>.localhost` through the proxy, with no host port of their own; after exit, the route is gone | `bin/test-integration` `TestDevLocalhostIntegration` |
| 9 | The instance comes from the branch (main vs other), with slugging and the 63-character cut; `--as` overrides; `--production` appends `-production`; no git or detached HEAD gives main | `internal/cli` `TestDevInstanceName` |
| 10 | First run of a branch copies each of main's named volumes; main's running containers are paused and unpaused (also on a failed copy); an existing branch volume isn't touched; main with no volumes starts empty, with a note; `--fresh` replaces after naming them | `internal/cli` `TestDevCopiesMainsData` |
| 11 | A name held by another checkout's running containers: a message and `--as`, not a silent takeover | `internal/cli` `TestDevNameInUse` |
| 12 | Real Docker: a branch instance starts with main's data (a row written in main is there) and diverges (a write in the branch isn't in main) | `bin/test-integration` `TestDevBranchCopyIntegration` |
| 13 | Checked by hand with equip: `houston dev` at `http://equip.localhost` in a browser, next to estherpictures | recorded here |

## Evidence
- **Tests first:** every new test failed first for the right reason, including:
  - `DevPort` undefined
  - `Route` undefined
  - `devInstance` undefined
  - an init template that still published a port
  - the fake's non-nil empty slice, which made `--fresh` think the branch was running (a real bug, fixed)
- **Unit suites:** `bin/go test ./...` ok, gofmt clean.
- **Integration (real Docker, `bin/test-integration`):**
  - `TestDevLocalhostIntegration`: two apps through one proxy, each at its own name, with no host port. Stopping one removed its route and left the other answering.
  - `TestDevBranchCopyIntegration`: the branch saw main's row, its write didn't reach main, and main wasn't left paused.
  - `TestDevIntegration_BuildsServesAndStops` still passes.
  - **Found stale:** `TestInit_DefaultsRun` looked for the `--production` app in `<name>`, but `--production` has had its own project (`<name>-production`) since cb12bd3, five hours after the test was written. Fixed.
- **Mutation check.** All 22 were caught:
  - The proxy: no network create; start never called; image not compared; the half-made container left; the port-taken message missed.
  - Ports: expose ignored; ports never reset.
  - Names: main detection by `main` only; no 63-character cut; the alias not the host.
  - Routes: the route kept after exit; the 403 check off; name-in-use off.
  - Branch data: unpause dropped; pause skipped; the branch's own data overwritten; no undo on a failed copy; `--fresh` while running; compose's volume label missing; `--fresh` on main; no copy at all.
- **Checked by hand on this Mac (OrbStack), with a copy of equip named `houston-equip-test`:**
  - `houston dev` printed "up at http://houston-equip-test.localhost", and `/up` answered 200 from the Mac, with no port in compose.yml.
  - **Found by the check, and fixed:** kamal-proxy's health check sends the *target's* name as Host. With an alias of `<project>.houston`, Rails blocked it ("Blocked hosts: houston-equip-test.houston:3000"), so the route never went live. Checked by hand that Docker's DNS resolves a `.localhost` alias inside the proxy (200). The alias is now the host itself.
  - A `feature1` worktree: "copying houston-equip-test's data … (storage, paused meanwhile)". Rails refused `feature1.houston-equip-test.localhost`: its `.localhost` rule allows one label (`SUBDOMAIN_REGEX = /(?:[a-z0-9-]+\.)/`, actionpack 8.1). Scotty chose to keep two-level names ("it's flexible and works for both"). So `houston dev` now checks the health path itself after 30 s, and says "answered 403 Forbidden: the app refuses that name" with the host to allow. That appeared on the real run. With `config.hosts << ".houston-equip-test.localhost"` in the copy, main and the branch both answered 200 side by side, and both rendered equip in a browser.
  - Everything the check made (both instances' containers and volumes, the copies) was removed afterwards. houston-dev-proxy stays, as designed.

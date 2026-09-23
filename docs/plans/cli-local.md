# Plan: Houston CLI, local only (build step 1)

Spec v3: https://claude.ai/artifact/R3d4fzN1u5QUP4kT88mqug (sections 3–6). The producer contract is the spec's section 4 (compose.yml + `x-houston`), and Docker Compose itself for everything outside `x-houston`. Divergences from Compose are listed under **Parity**.

## Goal

A single Go binary, `houston` (`hou` is a symlink), that loads a project's `compose.yml` + `x-houston`, rejects what Houston can't run, and runs the project locally: `dev`, `test`, `console`, `logs`, and `init` (Rails). The only dependency is Docker with Compose.

## Scope

- Go module `github.com/sevenmoons/houston` at the repo root. Mission Control (Rails) goes in `mission_control/` later.
  - `cmd/houston/`: `main`, which only wires up cobra
  - `internal/project/`: loads and validates compose.yml + `x-houston` → `Project`
  - `internal/variant/`: builds the dev/test compose variants from a `Project` (pure functions)
  - `internal/docker/`: the only package that runs `docker` (os/exec)
  - `internal/cli/`: commands, turning errors into exit codes
- Deps: `github.com/spf13/cobra`, `github.com/compose-spec/compose-go/v2` (Docker's own loader; it brings `yaml.v3`).
- **Toolchain runs in Docker:**
  - `Dockerfile` stages: `base` (`golang:1.25` + Docker CLI + compose plugin from the official images) → `dev` → `release` (cross-compiles darwin/linux × arm64/amd64 with CGO off; Batch 6).
  - `compose.yml` service `cli`: builds the `dev` target, mounts the repo at `${PWD}:${PWD}` with `working_dir: ${PWD}`, mounts `/var/run/docker.sock`, and uses named volumes for `/go/pkg/mod` and `/root/.cache/go-build`. This is Houston's own dev tooling, not a Houston project, so it has no `x-houston`.
  - Wrappers: `bin/go …`, `bin/test` (`go test ./...`), `bin/test-integration` (`go test -tags integration ./...`).
  - `mise.toml` (`go = "1.25"`) is only for editor tooling.
  - Check at execute time: a compose plugin that matches the host's Compose v5.1.2, and OrbStack's socket reachable from inside `cli`.
- **Boundary:** `--server` isn't registered in this step. It arrives with Mission Control (build step 3).

## Batch 1: loading the compose file

One surface: `project.Load(path string) (*project.Project, error)`, which every command calls first. The file is hostile input: typos, compose features the server can't run, and copy-paste from other setups.

### Design (short)

1. Read the file (up to 1 MiB).
2. Hand it to compose-go with **interpolation off**, environment resolution off, and no override files, so the model keeps `${VAR}` exactly as written. **The process environment and `.env` never change what Load returns.** compose-go's schema check rejects unknown compose keys (e.g. `enviroment`).
3. Extract referenced variables from the raw YAML with compose-go's template package: name + required/optional.
4. Run Houston's checks on the model and the `x-houston` extension, collecting **all** problems as `Problem{Path, Msg}`.
5. Return `*Project` or `*project.Errors`, never both. `Error()` renders one line per problem, sorted by path: `compose.yml: services.app.network_mode: Houston can't run this on the server`.

A compose-go failure (bad YAML, schema) comes back as a single problem with compose-go's own message, because it stops at its first error. Houston's own checks always report everything.

`Project` exposes only what Batch 1 computes and later batches read: `Name`, `File`, `AppService`, `AppPort`, `Variables []Variable{Name, Required, Kind}` (Kind = `Secret` | `ServiceHost`), `Houston` (the parsed `x-houston`), and the compose-go model (for building variants).

### Contract pin: `project.Load`

**Input:** a path. The file exists, is ≤ 1 MiB, and is a single valid compose document (compose-go schema).
**Authz / preconditions:** none. It's a pure read.
**Output:** `*Project` or `*Errors`.

| Check | Rule (literal) |
|---|---|
| `name` | required (a top-level key, not the folder default); `^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`; not `admin` / `hooks` |
| App service | exactly one service has `build`. Zero → "no service has `build:`; Houston deploys the one you build". Two or more → "only one built service per project for now (a worker role is planned)" |
| App port | `x-houston.port` if set (int 1–65535); else the container port of the app's single `ports` entry; zero or several entries without `x-houston.port` → error |
| Allowed service keys | `image`, `build` (app only), `environment`, `volumes`, `ports`, `depends_on`, `command`, `healthcheck`, `restart`, `deploy.resources.limits` (`cpus`, `memory`). Any other set key → "Houston can't run `services.<svc>.<key>` on the server" |
| `env_file` | rejected: "write `NAME: ${NAME}` under environment so Houston knows the app needs NAME" |
| Top-level keys | `name`, `services`, `volumes`, `x-houston` only. `networks`, `secrets`, `configs`, `include`, other `x-*` → rejected by path |
| Bind mounts | allowed on the app (dev only); rejected on other services: "would disappear on the server" |
| Named volume use | every named volume a service mounts is declared under top-level `volumes`, with no `driver`/`driver_opts`/`external` (location is chosen in Mission Control) |
| `x-houston` | required; unknown keys rejected; `health` required, starts with `/`, no whitespace |
| `x-houston.domains` | lowercase hostnames, ≥ 2 labels, labels `[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?`, no `*`/scheme/port/path, unique |
| `x-houston.deploy` | `on` ∈ {`commit`, `tag`} default `commit`; `branch` default `main` (no whitespace, no `..`, no leading `-`); `tags` default `v*` |
| `x-houston.commands` | keys ⊆ {`console`, `test`}; `console` is a string or `{dev, server}` with both set; `test` is a string only; no empty strings |
| `x-houston.hooks` | keys ⊆ {`release`, `post_deploy`}; non-empty strings |
| `x-houston.backups` | `schedule` `^daily ([01][0-9]\|2[0-3]):[0-5][0-9]$` default `daily 03:00`; `keep.auto`/`keep.deploy` int 1–1000, default 14/10; `storage` → rejected: "pick the backup target on the project page in Mission Control" |
| Variables | `${V}`, `$V`, `${V:?msg}`, `${V?msg}` → required; `${V:-d}`, `${V-d}` → optional; `$$` isn't a variable; a name referenced both ways counts as required |
| Service hosts | a variable named `<SVC>_HOST`, where `upper(replace(svc, "-", "_"))` is a service in the file, is kind `ServiceHost` (not a secret). It must be written with a default (`${DB_HOST:-db}`), or it's rejected with that exact fix, so plain compose still works. Two services mapping to the same name (`my-db`, `my_db`) is an error |

### Adversarial AC

1. The spec's PhoenixApp file loads: name, app `app`, port 4000, health, domains, commands (console dev/server), hooks, backups; variables `POSTGRES_PASSWORD` + `SECRET_KEY_BASE` (required secrets, deduplicated across services) and `DB_HOST`, `CACHE_HOST` (ServiceHost, not secrets).
2. The spec's RailsApp file loads with defaults: deploy `commit`/`main`/`v*`, backups `daily 03:00` 14/10, port 3000.
3. Setting `POSTGRES_PASSWORD=x` in the process env, or writing a `.env` next to the file, doesn't change what Load returns (the variables stay references, and no values appear anywhere in `Project`).
4. File problems each give one clear problem: missing (suggests `houston init`), empty, not a mapping, malformed YAML, > 1 MiB, a compose schema error (`enviroment`), no `x-houston` block.
5. Name: missing, `Equip`, `my_app`, `-app`, 64 chars, `admin`, `hooks` are rejected; `equip`, `a`, `my-app` are accepted.
6. App service and port: zero or two built services, two `ports` without `x-houston.port`, and no `ports` without `x-houston.port` are rejected; `x-houston.port` overrides `ports`.
7. Unsupported compose: `network_mode`, `privileged`, `profiles`, `env_file`, top-level `secrets`/`networks`/`include`, `build` on a second service, a bind mount on `db`, an anonymous volume (`- /data`), a volume with `driver_opts`, `deploy.replicas`, `extends`, an undeclared named volume. Each is rejected with its path.
8. `x-houston` rules: one invalid and one valid case per row of the table, including `backups.storage` rejected with the project-page hint. Service hosts: `${DB_HOST}` with no default is rejected with the fix; `${DB_HOST:-db}` with no `db` service is an ordinary optional variable; `my-db` + `my_db` collide.
9. All problems are reported together: a file with 4 independent Houston problems produces one error with 4 lines, sorted by path.
10. Variables are classified per use (added at execute: compose-go's `ExtractVariables` merges uses and can't tell `${V}` from `${V:-}`, so Houston scans compose's `$`/`${…}` syntax itself (it has to walk nested braces, which a single regex can't do)). A variable is optional only if every use has a default.

### Lens run (Batch 1)

| Lens | Applies? | Where |
|---|---|---|
| Contract | yes | AC 1–8 |
| Signals | yes | AC 9 (every problem, and never a partial Project) |
| Scale | yes | 1 MiB cap (AC 4) |
| Parity | yes | compose-go is Docker's own parser; the divergences are below |
| Whole batch | yes | one surface, 7 unhappy rows |
| Authz, Preconditions, Crash, Concurrency, At-least-once, Atomicity, Re-entry, Migrate | no | pure read, no state |

### Parity with Docker Compose (divergences, deliberate)

- `name` is required and stricter (no underscores, since it becomes a subdomain).
- Only the supported subset. Everything else is rejected, never ignored.
- One file, with no `compose.override.yml` merge. Batch 2 warns when an override file exists.
- Interpolation is deferred to run time (dev: `.env`; server: Mission Control). Load itself never interpolates.

### AC ↔ test map (Batch 1), `internal/project/load_test.go`

| AC | Test | Lens |
|---|---|---|
| 1 | `TestLoad_PhoenixScenario` (fixture `testdata/phoenixapp.compose.yml`) | Contract |
| 2 | `TestLoad_RailsScenarioDefaults` (fixture `testdata/railsapp.compose.yml`) | Contract |
| 3 | `TestLoad_IgnoresEnvironmentAndDotEnv` | Contract |
| 4 | `TestLoad_FileProblems` (table) | Contract, Scale |
| 5 | `TestLoad_NameRules` (table) | Contract |
| 6 | `TestLoad_AppServiceAndPort` (table) | Contract |
| 7 | `TestLoad_UnsupportedCompose` (table) | Contract, Parity |
| 8 | `TestLoad_XHoustonRules` (table) | Contract |
| 9 | `TestLoad_ReportsAllProblemsSortedByPath` | Signals |
| 10 | `TestLoad_VariableKinds` (table: `${V}`, `$V`, `${V:?m}`, `${V:-d}`, `${V:-}`, `${V-d}`, `$$V`, nested `${A:-${B}}`, same name required + optional → required, variables inside `x-houston` ignored) | Contract |

Command: `bin/go test ./internal/project/ -run TestLoad -v`

Batch 1 also sets up the toolchain (Dockerfile `base`/`dev`, `compose.yml`, `bin/` wrappers), because the tests run through it. Check: `bin/go version` prints go1.25, and `docker compose run --rm cli docker version` reaches the host daemon.

## Batch 2: `houston dev`

Batch 1 is green and committed (`1c991a3`). Batch 2 is the first real command: `houston dev [-f compose.yml]`.

### Design (short)

- **A small override file instead of a rewritten copy.** Houston writes `.houston/compose.dev.yml` containing only what it changes, and runs
  `docker compose --project-directory <dir> -f <compose.yml> -f <dir>/.houston/compose.dev.yml up --build`.
  Relative paths (`.:/app`, `build: .`) keep resolving from the project directory, just as with plain `docker compose`. Passing `-f` explicitly also stops Compose from auto-merging a user's `compose.override.yml`, which matches the one-file rule.
- In dev, Houston changes only the app's build target:
  ```yaml
  # Generated by houston from compose.yml. Do not edit.
  services:
    <app>:
      build:
        target: dev
  ```
- `.env` is left to Compose (it reads `<dir>/.env` itself). Houston only reads it to warn about names. It uses compose-go's own `dotenv` parser, so Houston and Compose agree on what the file says.
- `up --build` so Dockerfile edits apply. The command runs attached, and Houston's exit code is Compose's.
- On Ctrl-C, the terminal sends SIGINT to both Houston and Compose. Houston ignores SIGINT while Compose runs and waits for it to stop the containers, so the prompt doesn't come back while containers are still shutting down.

Packages:
- `internal/variant`: `DevOverride(p *project.Project) []byte` (pure).
- `internal/docker`: `Runner` interface: `LookPath`, `Output(args…)` for preflight, `Run(dir, args…) (exitCode, error)` with stdio attached and SIGINT ignored while the child runs. The real implementation uses os/exec. Tests use a fake.
- `internal/cli`: `Main(args []string, stdout, stderr io.Writer, d docker.Runner) int`. `cmd/houston/main.go` is just `os.Exit(cli.Main(...))`.

### Contract pin: `houston dev`

| Input / state | Result |
|---|---|
| `-f PATH` (default `compose.yml`); project dir = dir of PATH | — |
| Load problems | print every problem to stderr, exit **2**; no Docker calls, nothing written |
| Unknown flag (incl. `--server`, `--production` for now) | usage error, exit **2** |
| `docker` not on PATH | "Docker isn't installed…", exit **1**, nothing written |
| `docker version` can't reach the server | "Docker isn't running (start OrbStack or Docker Desktop)", exit **1**, nothing written |
| `docker compose version` fails | "Houston needs Docker Compose v2 or later", exit **1**, nothing written |
| `.env` unparseable | "`.env`: <parser message>", exit **2**, no `up` |
| `.env` missing, or a **required Secret** missing/blank in it | warning to stderr listing the names (never values), then continue. ServiceHost and optional variables are never listed |
| `compose.override.yml` / `.yaml` present | warning: "Houston ignores compose.override.yml", then continue |
| `.houston` can't be created or written (e.g. it's a file) | "can't write .houston/compose.dev.yml: …", exit **1**, no `up` |
| All good | override written atomically (temp + rename, identical bytes on every run) → `up --build` → exit with Compose's code |

### AC ↔ test map (Batch 2)

| # | AC | Test | Lens |
|---|---|---|---|
| 1 | Override for the PhoenixApp fixture is exactly the pinned bytes; two calls give the same bytes | `internal/variant/dev_test.go` `TestDevOverride_ForcesDevTarget` | Contract, Re-entry |
| 2 | App named `web` → `services.web.build.target: dev` | `TestDevOverride_UsesAppServiceName` | Contract |
| 3 | Happy path: preflight → file written → exact `docker compose … up --build` argv run in the project dir; Compose exits 3 → `houston dev` exits 3 | `internal/cli/dev_test.go` `TestDev_RunsComposeWithOverride` | Contract |
| 4 | Invalid compose file → exit 2, problems on stderr, zero Docker calls, no `.houston/` | `TestDev_InvalidConfigExits2WithoutDocker` | Preconditions |
| 5 | Docker not installed / not running / no Compose v2 → exit 1 with the message, no file, no `up` | `TestDev_DockerPreflight` (table) | Preconditions |
| 6 | Missing `.env`; blank and missing required secrets; ServiceHost and optional not listed; a value in `.env` never appears in output; still runs `up` | `TestDev_WarnsAboutVariables` (table) | Signals |
| 7 | Malformed `.env` → exit 2 naming `.env`, no `up` | `TestDev_BadDotEnvExits2` | Contract |
| 8 | `compose.override.yml` present → warning, and it isn't in the `-f` list | `TestDev_IgnoresOverrideFile` | Parity, Signals |
| 9 | Stale override replaced; second run writes identical bytes; `.houston` is a file → exit 1, no `up` | `TestDev_GeneratedFile` (table) | Atomicity, Re-entry |
| 10 | `-f sub/compose.yml` → project dir `sub/`; unknown flag and `--server` → exit 2 | `TestCLI_FileFlagAndUsageErrors` | Contract, Honest surface |
| 11 | **Real Docker:** a busybox fixture project (Dockerfile with `base`/`dev`/`test`/`production` stages, app + `db` service). `houston dev` builds the **dev** stage (a marker file reads `dev`), the bind mount is live (a file written on the host is visible in the app container), `${DB_HOST:-db}` reaches the `db` service, and SIGINT to `houston` stops the containers and exits within 30 s. Checked with `docker compose exec`, so no host ports are involved | `internal/cli/dev_integration_test.go` `TestDevIntegration_BuildsServesAndStops` (`//go:build integration`) | Crash/signals, Parity |

Commands: `bin/go test ./internal/variant/ ./internal/cli/` for rows 1–10; `bin/test-integration -run TestDevIntegration` for row 11.

### Lens run (Batch 2)

| Lens | Where |
|---|---|
| Contract | 1–3, 7, 10 |
| Preconditions | 4, 5: every failure before `up` writes nothing |
| Atomicity / Re-entry | 9: atomic write, identical reruns |
| Crash / signals | 11: Ctrl-C leaves no running containers and doesn't return early |
| Signals (ops) | 6, 8: warnings name the problem and never leak values |
| Honest surface | 10: `--server` and `--production` aren't registered until they work (Batch 6 / build step 3) |
| Parity | 8, 11: behaves like plain `docker compose` except for the documented differences |
| Authz, Concurrency, At-least-once, Migrate | N/A: single local process, no shared state |

### Added during execute (review of Batch 2)
- `-p <name>` is always passed. Otherwise Compose lets `COMPOSE_PROJECT_NAME` from the shell or `.env` rename the containers, which would break `console`/`logs` in Batch 4 (row 3 now sets a stray `COMPOSE_PROJECT_NAME`).
- `.houston/.gitignore` (`*`) is written with every generated file, so `.houston/` is never committed even without `houston init` (row 9).
- Row 11 now also asserts that nothing is left in houston's process group when it exits. Without that check, a mutation that dropped the Ctrl-C handling still passed, because busybox stops so fast.

### Real-app check (equip copy, Batch 2)
A copy of `~/code/equip` (Rails 8.1, SQLite) with hand-added `dev`/`test`/`production` stage names and a `compose.yml`: `/up` returned 200 after 78 s (first build), a file written on the host was served with no rebuild, and Ctrl-C stopped gracefully (exit 130, no processes or containers left). For Batch 5, Rails' generated Dockerfile needs its final stage named `production`, plus `dev` (development env, full bundle, `db:prepare && rails server -b 0.0.0.0`) and `test` stages.

### Check at execute time
- compose-go `dotenv` API: `dotenv.ReadFile(path, lookup)` with a lookup that returns nothing, so `.env` is parsed without reading the environment (done).
- `docker compose up` exit code after Ctrl-C (130 vs 0), which the integration test pins as whatever Compose returns.

## Batch 3: `houston test`

Batch 2 is green and committed (`1eed6bf`). Batch 3: `houston test [-f compose.yml]` runs `x-houston.commands.test` in a throwaway copy of the project and exits with the test's exit code. This is the same command a runner will use on the server before every deploy (build step 4), so it must not depend on anything on the laptop.

### Design (short)

- **Throwaway project.** Compose project name `<name>-test-<8 random hex>`. Its containers, network, and named volumes are new and separate from `houston dev`'s, and two test runs at once never collide.
- **Override with Compose's own reset tags** (checked against the host's Compose before writing this plan). `.houston/compose.test.yml`:
  ```yaml
  # Generated by houston. Do not edit.
  services:
    app:
      build:
        target: test
      ports: !reset []
      volumes: !override        # named volumes only; bind mounts are dev-only
        - media:/media
    db:
      ports: !reset []
  ```
  `ports: !reset []` goes on every service. When the app has no named volumes: `volumes: !reset []`.
- **Environment is throwaway, not the laptop's.**
  - `--env-file /dev/null`: Compose doesn't read `.env`.
  - The child environment is Houston's own minus every variable the compose file references. Each **required secret** gets a random value (the same value everywhere in one run, new every run).
  - Service hosts and optional variables stay unset, so their defaults apply.
  - Everything else (`PATH`, `DOCKER_HOST`, …) passes through.
- **Run, then always tear down:**
  1. `docker compose -p <P> --project-directory <dir> --env-file /dev/null -f <compose.yml> -f <dir>/.houston/compose.test.yml run --rm --build <app> sh -c '<commands.test>'`
  2. `… down -v --rmi local --remove-orphans` with the same `-p` and files, **every time**: pass, fail, docker error, or Ctrl-C (caught as in `dev`, so the run stops first and teardown still happens).
- **Exit code** = the test command's. Teardown is secondary: if it fails, Houston warns with the exact `docker compose -p <P> down -v` command to finish cleanup, and still returns the test's code.
- **No `commands.test`** → "no x-houston.commands.test; nothing to run", exit 0, no Docker calls (spec: tests pass when undefined).
- `docker.Runner.Run` gains an `env []string` argument (nil = inherit). `dev` passes nil.
- Readiness is the compose file's job (`healthcheck` + `depends_on: condition: service_healthy`). Houston doesn't inject healthchecks. `houston init` (Batch 5) writes them for Postgres.

### Crash-gap template

```text
Durable step 1 (primary): the test run (containers, network, volumes of project <P>)
Dies before: teardown (houston SIGKILLed, machine sleeps, docker daemon restarts)
Retry / redelivery: the next `houston test` uses a new <P>, so it's unaffected by leftovers
Must still accomplish: leftovers are findable and removable
Paths that must not block repair: none; leftovers are only resources, not state Houston reads
After primary: teardown is best-effort — failure warns (with the exact command) and never changes the exit code
Named defer: automatic sweeping of stale <name>-test-* projects belongs to the server runners (build step 4), where leaks would pile up; on a laptop the printed command is the repair
```

### Contract pin: `houston test`

| Input / state | Result |
|---|---|
| Load problems | stderr, exit **2**, no Docker calls |
| No `commands.test` | message, exit **0**, no Docker calls, nothing written |
| Docker preflight fails | same messages as `dev`, exit **1**, no run |
| `.houston` unwritable | exit **1**, no run, no teardown needed |
| Run finishes with code N | teardown, exit **N** |
| `docker` itself can't start the run | teardown, exit **1** |
| Teardown fails | warning with `docker compose -p <P> down -v`, exit unchanged |

### AC ↔ test map (Batch 3)

| # | AC | Test | Lens |
|---|---|---|---|
| 1 | PhoenixApp → exact override bytes (target `test`, app named volumes via `!override`, `ports: !reset []` on every service); same bytes on a second call | `internal/variant/test_test.go` `TestTestOverride_Phoenix` | Contract, Re-entry |
| 2 | App with only a bind mount → `volumes: !reset []` | `TestTestOverride_NoNamedVolumes` | Contract |
| 3 | Exact `run` argv (`-p` matches `^phoenixapp-test-[0-9a-f]{8}$`, `--env-file /dev/null`, `sh -c "mix test"`), then exact `down` argv with the same `-p`. Exit = run's code (3). Two runs get different project names | `internal/cli/test_test.go` `TestTest_RunsInThrowawayProject` | Contract, Concurrency |
| 4 | Child env: required secrets are random, non-empty, and differ across runs. A shell value for a referenced variable (`SECRET_KEY_BASE=from-shell`) is replaced, not passed. `DB_HOST` and optional variables are absent even if set in the shell. `PATH` passes through. A `.env` with values changes nothing and gives no warnings | `TestTest_EnvironmentIsThrowaway` | Contract, Parity |
| 5 | No `commands.test` → exit 0, message, zero Docker calls, no `.houston/` | `TestTest_NoTestCommand` | Preconditions |
| 6 | Invalid file → exit 2, no Docker. Preflight failures → exit 1, no run (table) | `TestTest_InvalidConfigAndPreflight` | Preconditions |
| 7 | The run errors (docker couldn't start it) → teardown still runs, exit 1. Teardown fails → warning names the project and the command, exit = the test's code | `TestTest_TeardownAlwaysRuns` | Crash & repair, Signals |
| 8 | `.houston/compose.test.yml` written atomically next to `.gitignore`. `.houston` is a file → exit 1, no run, no teardown | `TestTest_GeneratedFile` | Atomicity |
| 9 | **Real Docker** (busybox fixture gains a named volume, `APP_SECRET: ${APP_SECRET}`, and `commands.test`): a passing test sees the `test` stage, `${DB_HOST:-db}` reaches `db`, `APP_SECRET` is set, and no bind mount → exit 0. `exit 7` → exit 7. Ctrl-C during `sleep 300` → exit within 30 s. After each: no containers, volumes, or networks left for the test project, and no `.env` value was used | `internal/cli/test_integration_test.go` `TestTestIntegration_PassFailInterruptCleanup` | Crash, Parity, Signals |

| 10 | **Added at execute** (found while writing row 9): Compose interpolates `x-houston` too. A bare `$DB_URL` in a command becomes blank (with a warning on every `docker compose` run), and `$$` means a literal `$`. So in `x-houston.commands.*` and `hooks.*`, Load rejects bare `$NAME`/`${…}` ("write `$$DB_URL`; compose reads this file too…") and stores the command with `$$` → `$`, exactly what compose would see. `$(…)` stays as written, as in compose | `internal/project/load_test.go` `TestLoad_XHoustonDollarEscaping` | Parity, Contract |

| 11 | **Added in review:** a compose file that mounts `${HOME}/.ssh` (or uses `${DOCKER_CONTEXT}`) doesn't make `houston test` give docker a random `HOME`/`DOCKER_*`. Docker's own variables (`HOME`, `PATH`, `TMPDIR`, `DOCKER_HOST`, `DOCKER_CONTEXT`, `DOCKER_CONFIG`, `DOCKER_CERT_PATH`, `DOCKER_TLS_VERIFY`) always pass through | `TestTest_KeepsDockersOwnEnvironment` | Contract, Config |
| 12 | **Added in review (Batch 1 bug):** Load accepted only literal bind sources. Without interpolation, compose-go took `${HOME}/.ssh:/root/.ssh` for a named volume and rejected it as undefined. A short-syntax source that starts with a variable is now a bind mount (as compose decides after interpolation), with the text kept as written | `internal/project/load_test.go` `TestLoad_VariableLedBindMounts` | Contract, Parity |

Commands: `bin/go test ./internal/variant/ ./internal/cli/ ./internal/project/` (rows 1–8, 10–12); `bin/test-integration -run TestTestIntegration` (row 9). Batch 2's rows stay green (the `Run` signature change touches `dev`).

### Review of Batch 3: deferred, with a home
- **A test that hangs forever** blocks `houston test` forever. On a laptop, Ctrl-C handles it (and tears down). On the server, a hung job would hold a runner slot, so a **job timeout belongs to the runners (build step 4)**.
- **Machine variables** like `${HOME}` in a dev bind mount load as required secrets. `houston test` now leaves Docker's own variables alone (row 11), but on the server Mission Control would ask for a `HOME` value. **Decide how machine variables are classified when the server reads the file (build step 3).**
- A named volume chosen by a variable (`${VOL}:/data`) is now read as a bind mount (row 12), because a variable-led source is almost always a host path (`${PWD}`, `${HOME}/…`). Houston needs literal names for data volumes anyway, since their location is picked in Mission Control.

### Lens run (Batch 3)

| Lens | Where |
|---|---|
| Contract | 1–4 |
| Preconditions | 5, 6: nothing runs or is written before the checks pass |
| Crash & repair | 7, 9 + crash-gap template: teardown on every path; SIGKILL leftovers have a named home |
| Concurrency | 3: random project name per run, so parallel runs (two terminals, two runners) share nothing |
| Atomicity | 8 |
| Signals | 7: teardown failure is loud and actionable, never silent |
| Parity | 4, 9: laptop and runner get the same environment; `.env` and shell can't leak in |
| Honest surface | `Run`'s new `env` argument is used by `test`; `dev` passes nil |
| Authz, At-least-once, Migrate | N/A |

### Check at execute time
- `docker compose run` exit code when the container is stopped by Ctrl-C (the test only pins "exits within 30 s, nothing left").
- `down --rmi local` removes only the throwaway app image (tagged under `<P>`), never `houston dev`'s image.

## Batch 4: `houston console` and `houston logs [-f]`

Batch 3 is green and committed (`6aacd5e`). Batch 4 covers the two local commands that reach into what `houston dev` started. `--server` versions come with Mission Control (build step 3).

### Design (short)

- **Find the app container by Compose's labels, not by compose files.** `houston dev` pins the project name (`-p <name>`), so the app container is always labelled `com.docker.compose.project=<name>`, `com.docker.compose.service=<app>`, `com.docker.compose.oneoff=False`. The last label excludes `docker compose run` containers. `houston test` containers carry `<name>-test-…` and never match.
  - console: `docker ps -q --filter …` (running only)
  - logs: `docker ps -a -q --filter …` (a stopped app still has logs)
  - Docker lists newest first, and Houston takes the first ID.
  This needs no `.houston/` files and works even if the compose file changed since `dev` started.
- **`houston console`** runs `docker exec -i [-t] <id> sh -c '<commands.console.dev>'`. `-t` only when Houston's stdin is a terminal, so `echo 'User.count' | houston console` works. The exit code is the console's. The command is already `$$`-unescaped by Load.
- **`houston logs [-f]`** runs `docker logs [--follow] <id>`. The exit code is Docker's (Ctrl-C during `-f` is caught as in `dev`, so Docker exits first).
- Terminal detection: `os.Stdin.Stat()` is a character device (no new dependency), behind a package variable the tests override.

### Contract pin

| Input / state | `console` | `logs [-f]` |
|---|---|---|
| Load problems | exit **2**, no Docker | same |
| No `commands.console` | "no x-houston.commands.console", exit **2**, no Docker | — |
| Docker preflight fails | exit **1** (same messages as `dev`) | same |
| No running app container | "`<name>` isn't running; start it with `houston dev`", exit **1**, no exec | — |
| No app container at all | — | "no app container for `<name>`; start it with `houston dev`", exit **1** |
| Found | `exec -i[t] <id> sh -c <dev cmd>`, exit = console's | `logs [--follow] <id>`, exit = Docker's |
| `--server`, `--tail`, other flags | usage error, exit **2** | same |

### AC ↔ test map (Batch 4), `internal/cli/console_test.go`, `logs_test.go`

| # | AC | Test | Lens |
|---|---|---|---|
| 1 | Exact `ps` query (project, service, oneoff=False, running only), then `exec -it <id> sh -c "iex -S mix"`. Exit = console's (4) | `TestConsole_ExecsInRunningApp` | Contract |
| 2 | Stdin not a terminal → `exec -i` (no `-t`) | `TestConsole_PipedInputSkipsTTY` | Contract |
| 3 | `{dev, server}` console runs `dev`. `$$NODE` in the file reaches exec as `$NODE` | `TestConsole_UsesDevCommand` | Contract, Parity |
| 4 | App not running (empty `ps`) → exit 1 with the message, no exec | `TestConsole_AppNotRunning` | Preconditions, Signals |
| 5 | No `commands.console` → exit 2, zero Docker calls | `TestConsole_NoConsoleCommand` | Preconditions |
| 6 | Exact `ps -a` query, then `logs <id>`; `-f` → `logs --follow <id>`; exit passthrough | `TestLogs_ShowsAppLogs` | Contract |
| 7 | No container at all → exit 1 with the message, no `logs` | `TestLogs_NoContainer` | Preconditions, Signals |
| 8 | Several IDs → the first (newest) is used, for both commands | `TestConsoleAndLogs_UseNewestContainer` | Contract |
| 9 | Both commands: invalid file → 2 with no Docker; preflight failures → 1 with no exec/logs; `--server` → 2 | `TestConsoleAndLogs_StopEarly` (table) | Preconditions, Honest surface |
| 10 | **Real Docker:** with `houston dev` running the busybox fixture, `houston logs` shows the app's startup line, `houston console` (piped stdin) prints `dev` and passes through a failing console's exit code, and `houston logs -f` exits within 10 s of Ctrl-C with nothing left in its process group. After `dev` stops, `console` exits 1 with "isn't running" and `logs` still shows the stopped app's output | `internal/cli/console_integration_test.go` `TestConsoleLogsIntegration` | Parity, Crash/signals |

Commands: `bin/go test ./internal/cli/` (rows 1–9); `bin/test-integration -run TestConsoleLogsIntegration` (row 10). The fixture's dev `CMD` gains a startup line and `httpd -v`, plus `commands.console: cat /stage`.

### Lens run (Batch 4)

| Lens | Where |
|---|---|
| Contract | 1–3, 6, 8 |
| Preconditions | 4, 5, 7, 9: nothing is exec'd against a missing container |
| Signals | 4, 7: the message says what to run |
| Parity | 3, 10: same project name as `dev` (`-p`), same `$$` rule |
| Honest surface | 9: no `--server` or `--tail` until they do something |
| Crash / signals | 10: Ctrl-C on `logs -f` leaves nothing running |
| Authz, Concurrency, At-least-once, Atomicity, Migrate | N/A: read-only against local Docker, nothing written |

### Added during execute (Batch 4)
- **`-f` clash:** Batch 2 made `-f/--file` a flag on every command, which left no `-f` for `logs -f` (follow, per the spec). Resolved as Compose does: `--file` is a root flag given before the command (`houston -f x dev`), and `logs -f` is follow. Batch 2/3 tests moved the flag; `TestCLI_FileFlagAndUsageErrors` checks that `dev -f x` is a usage error. With cobra's `TraverseChildren`, root had to reject unknown commands itself.
- The review left one low-severity note: when `docker ps` fails after the preflight passed, the message is Docker's bare "exit status 1". It goes in the Batch 6 polish.

### Check at execute time
- `com.docker.compose.oneoff` label values (`False`/`True`) on current Compose.

## Later batches (titles only)

2. **`houston dev`**: fully specified below.
3. **`houston test`**: fully specified below.
4. **`houston console` / `houston logs [-f]`**: fully specified below.
5. **`houston init` (Rails)**: detection, prompts, writing or extending `compose.yml` (SQLite `storage` volume, or Postgres when `pg` is in the Gemfile), Dockerfile stages, `.env` template, `.gitignore`, never overwriting silently.
6. **`houston dev --production` + packaging**: `hou` symlink, release builds, laptop install script. Decide whether to keep cobra's built-in `completion` command (shell completions) or disable it; it's untested surface today. Also: the `cli` container runs as root, so on a Linux host files it writes (go.sum, generated code) come out root-owned. That's fine on macOS/OrbStack, and should be fixed before Linux CI or runners use this image (found in the Batch 1 review).

## Agent loop checkpoints

- Red: write all 10 Batch-1 tests, run them, report the red map, then keep going.
- Green: all 10 pass, shown with command output. Then scotty-review on the tip (cold pass).
- Ready: scotty-review again if the tip moved; `bin/test`; `bin/go vet` + `docker compose run --rm cli gofmt -l` on the touched files.

## Decisions made (say if any are wrong)

- Exit codes: `0` success; `2` usage or config error; `1` any other Houston failure; `test`/`console` pass through the child's exit code.
- The project root is the directory containing the compose file. `-f/--file` defaults to `compose.yml` and goes **before the command**, like `docker compose -f x up`: `houston -f other.yml dev`. After `logs`, `-f` means follow, like `docker compose logs -f` (decided in Batch 4, when the two clashed).
- `houston test` ignores `.env` and gives every referenced variable a random throwaway value.
- Precise compose-go calls (loader options, template variable extraction) are confirmed against the library at execute time. If an option doesn't exist as assumed, the plan's contract stays and only the mechanism changes.

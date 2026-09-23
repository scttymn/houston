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

## Later batches (titles only)

2. **`houston dev`**: fully specified below.
3. **`houston test`**: test variant (target `test`, no bind mounts or ports, fresh volumes, random values for variables), throwaway project `<name>-test-<hex>`, `run --rm`, exit-code passthrough, teardown on success, failure, and SIGINT.
4. **`houston console` / `houston logs [-f]`**: find the app container by compose labels, TTY only when stdin is a TTY, and clear errors when nothing is running.
5. **`houston init` (Rails)**: detection, prompts, writing or extending `compose.yml` (SQLite `storage` volume, or Postgres when `pg` is in the Gemfile), Dockerfile stages, `.env` template, `.gitignore`, never overwriting silently.
6. **`houston dev --production` + packaging**: `hou` symlink, release builds, laptop install script. Decide whether to keep cobra's built-in `completion` command (shell completions) or disable it; it's untested surface today. Also: the `cli` container runs as root, so on a Linux host files it writes (go.sum, generated code) come out root-owned. That's fine on macOS/OrbStack, and should be fixed before Linux CI or runners use this image (found in the Batch 1 review).

## Agent loop checkpoints

- Red: write all 10 Batch-1 tests, run them, report the red map, then keep going.
- Green: all 10 pass, shown with command output. Then scotty-review on the tip (cold pass).
- Ready: scotty-review again if the tip moved; `bin/test`; `bin/go vet` + `docker compose run --rm cli gofmt -l` on the touched files.

## Decisions made (say if any are wrong)

- Exit codes: `0` success; `2` usage or config error; `1` any other Houston failure; `test`/`console` pass through the child's exit code.
- The project root is the directory containing the compose file. `-f/--file` defaults to `compose.yml`.
- `houston test` ignores `.env` and gives every referenced variable a random throwaway value.
- Precise compose-go calls (loader options, template variable extraction) are confirmed against the library at execute time. If an option doesn't exist as assumed, the plan's contract stays and only the mechanism changes.

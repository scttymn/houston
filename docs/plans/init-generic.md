# Plan: build step 7, a generic `houston init`

## Your direction
- "I want a truly generic setup and not something tailored specifically to every framework under the sun. That's fragile. Heck, what if we were deploying static html?!"
- "The init tool should upsert docker config with all the defaults necessary to deploy with Houston. This means that the user will need to make changes based on their project."
- "We assume docker is a prerequisite since the idea is also local dev in docker too."

This replaces the spec's "more stacks for `init`" (§12, step 7) and its stack presets (§4). Houston knows no frameworks at all: not in `init`, not anywhere.

## Goal
`houston init` in any folder upserts what Houston needs to run and deploy it, with generic defaults. It works as written for static HTML; for anything else, the developer changes the lines that matter to their project.
- **Dockerfile:** `base`, `dev`, `test` and `production` stages. The default is a tiny web server (busybox `httpd`) serving the folder on port 8080.
- **`compose.yml`:** `name`, one `app` service built from the Dockerfile, the port, the bind mount for dev.
- **`x-houston`:** `health: /`, plus every other key as a commented example (`commands`, `hooks`, `domains`, `deploy`, `backups`), so what can be set is in front of the developer.
- **`.env`:** a line for every variable the compose file references. **`.gitignore`:** `/.env`.

## The rule: upsert
- **Missing → written with the defaults.**
- **Present → only what Houston needs and can't find is added.** Nothing the developer wrote is replaced.
- **Dockerfile:**
  - Missing: the default is written.
  - Present:
    - An unnamed final stage is named `production`; a named one is kept.
    - A missing `production` becomes `FROM <final stage> AS production`.
    - A missing `dev` or `test` becomes `FROM production AS dev` / `AS test`, appended with a comment saying to make them the project's own.
    - Other lines stay byte for byte.
- **`compose.yml`:**
  - Missing: the default is written. The name is the folder's, prompted for on a terminal, as today.
  - Present:
    - A missing `x-houston` block is appended.
    - A present block gets only the keys Houston requires that it lacks: `health`, and `app_port` when the app has no single `ports` entry. Comments and formatting stay: lines are appended, and the file isn't re-marshalled.
    - Problems `init` can't fix without guessing are listed by name, and nothing is written. That covers no service with `build:` (which one is the app?), `network_mode`, `env_file`, and the other rejections `project.Parse` already makes.
- **`.env` and `.gitignore`:** missing lines appended, as today.
- **All or nothing per run:** every change is planned and the resulting compose file checked with `project.Parse` before anything is written. A rerun finishes an interrupted run, and says "already set up" when nothing is missing.

## Scope
- The generic `init` above.
- **Removed:** `internal/stack` (Rails' Dockerfile rewrite, the Rails compose and `x-houston`, and the Rails detection). A Rails app now gets the generic upsert like everything else. Its `rails-expected` goldens are replaced by the generic ones.
- **`x-houston.port` becomes `x-houston.app_port`:** the port the app listens on inside its container, which kamal-proxy routes to and health-checks. It's Kamal's own name for it (`proxy.app_port`), and `port` next to compose's `ports:` didn't say which side it meant.
  - There's no alias: `port` is an unknown key whose error says "`port` is now `app_port`" (pre-release; only this session's test copies used it).
  - The resolved port Houston reports (Mission Control's sync payload, `houston inspect --json`) stays `port`: that's Houston's own API, not the compose key.
  - Touched: `internal/project/houston.go` (the key, the unknown-key list), `project.go` ("…or set x-houston.app_port"), `variant/dev.go` (a comment), `load_test.go`.
- **The spec is updated:**
  - §3: stack knowledge no longer lives in `init`.
  - §4: the presets table goes, `houston init` writes generic defaults, and `port` → `app_port` in the key table and the examples.
  - §5: the `init` row.
  - §12: step 7.
- **Cut:** framework detection of any kind, and commands or hooks filled in per stack. They're shown as commented examples instead.

## Batches
1. **The generic upsert** (fully specified below).
2. **The real run:**
   - A static HTML folder goes through `init`, `dev`, `dev --production`, and a deploy on a fresh OrbStack Houston serving its `index.html` with no edits.
   - A copy of equip, then a copy of estherpictures_com: `init` upserts into their own Dockerfiles, then the edits a developer would make are made in the copies and written down (they become the migration notes for step 8).

## Batch 1: The generic upsert

### Evidence (what's there)
- **`internal/cli/init.go`:**
  - `runInit` plans `change`s, checks the compose file with `project.Parse`, and writes with `writeAtomic`.
  - `planCompose` appends a missing `x-houston` block; `planDotEnv` and `planGitignore` already upsert; `askName` prompts for the name.
  - Rails-specific parts: `stack.DetectRails`, the Dockerfile rewrite, `RailsCompose`, the Rails `x-houston`.
- **`internal/stack/rails.go`:** the only stack code. It goes. Its stage-finding (`fromRE`, the `stage` list) moves into `init`'s generic Dockerfile upsert.
- **`internal/cli/testdata/devapp`:** a busybox `httpd` app with `base`, `dev`, `test` and `production` on 8080. The default Dockerfile is this shape.
- **`internal/project`:** `Parse`, the port rule (the container side of one `ports` entry, or `x-houston.port`, renamed here), and the rejections by key path. `houston.go:87` parses `port`; `project.go:456` names it in the error.

### Design (short)
- **`init.go`**, with the Dockerfile upsert in its own small file (`init_dockerfile.go`: find the stages, add what's missing):
  - `planDockerfile`
  - `planCompose`: the default, or an append, or gap lines inside the block
  - `planDotEnv`, `planGitignore` (unchanged)
- **The default Dockerfile:**
  ```dockerfile
  # Houston builds the stage it needs: dev (houston dev), test (houston test),
  # production (deploys). These defaults serve this folder as a static site;
  # change them for your project.
  FROM busybox:1.36 AS base
  WORKDIR /app

  FROM base AS dev
  CMD ["httpd", "-f", "-v", "-p", "8080", "-h", "/app"]

  FROM base AS test
  COPY . /app

  FROM base AS production
  COPY . /app
  CMD ["httpd", "-f", "-p", "8080", "-h", "/app"]
  ```
- **The default `compose.yml`:**
  ```yaml
  name: <folder>

  services:
    app:
      build: { context: ., target: dev }
      ports: ["8080:8080"]
      volumes:
        - .:/app

  x-houston:
    health: /
    # app_port: 8080                # the port the app listens on, when it has no single `ports` entry
    # domains: [example.com]
    # deploy: { on: commit, branch: main }
    # commands:
    #   console: sh
    #   test: "true"
    # hooks:
    #   release: ./migrate
    # backups: { schedule: "daily 03:00", keep: { auto: 14, deploy: 10 } }
  ```
  With no `commands.test`, `houston test` passes without running anything (spec §4).
- **The gap lines** in an existing `x-houston` block go right after its first line, at the block's indentation. A flow-style block (`x-houston: { … }`) can't take lines, so `init` names the keys to add and writes nothing.

### Contract pin
- **Input:** the folder of the compose file (`-f` as today). There's a name prompt on a terminal only when `compose.yml` is missing. `init --server` → exit 2.
- **Preconditions:** any folder. There is no detection and nothing is refused for what the app is.
- **Refused (exit 1, nothing written), each with its specific message:**
  - an existing compose file with problems `init` can't fix without guessing
  - an existing Dockerfile it can't read, or one with no `FROM`
  - a flow-style `x-houston` block missing required keys
- **Effects:** only these six files (the five above and `.dockerignore`). Each is written atomically, after every change is planned and checked. A summary line is printed per file written, then "Next: edit the defaults for your project, then houston dev". Nothing else is touched.
- **Exit codes:** 0 when set up or already set up; 1 when refused; 2 for usage.

### Adversarial AC
- **Nothing the developer wrote is replaced:**
  - a changed `health`
  - their own stages and extra services
  - their `.env` values
  - comments in `x-houston`
- **A second run changes nothing, byte for byte.** A run cut short → the rerun writes only the rest.
- **An existing compose file `init` can't fix** → every problem listed, and every file left as it was.
- **An existing Dockerfile:**
  - The final stage named something else (`runner`) → it's kept, and `production` is added from it.
  - Every other line stays, comments included.

### Crash gap
- **Durable step 1 (primary):** the first file written (the order: Dockerfile, `compose.yml`, `.env`, `.gitignore`).
- **Dies before:** the next file.
- **Retry:** `houston init` again.
- **Must still accomplish:** every missing piece. Each file is planned from what's on disk now.
- **Must not block repair:** there's no "already initialized" flag. "Already set up" is reported only when every plan is empty.

(`init` is local, with one writer, so no concurrency or at-least-once templates apply.)

### AC ↔ test map (Batch 1), `internal/cli/init_test.go`
Fixtures: `testdata/static` (just an `index.html`), `testdata/rails` (kept, as an app with its own Dockerfile), and goldens in `testdata/init-expected`.

| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | A folder with only `index.html` → the exact default Dockerfile and `compose.yml`, `.gitignore` = `/.env`, no `.env` (nothing referenced), the summary lines, exit 0. The compose file passes `project.Load` with `AppPort` 8080 and health `/` | `TestInit_EmptyFolder` | Contract |
| 2 | An existing Dockerfile: unnamed final → named `production`, `dev`/`test` appended from it; final named `runner` → `FROM runner AS production` added; all four stages present → untouched; every other line byte-identical in each case; no `FROM` → refused, nothing written | `TestInit_UpsertsTheDockerfile` (table) | Contract, Preconditions |
| 3 | An existing `compose.yml` without `x-houston` → the default block appended, the original bytes a prefix of the result. With `x-houston` lacking `health` → one `health: /` line added inside the block, its comments and other keys unchanged. A changed `health: /healthz` → kept | `TestInit_UpsertsXHouston` | Atomicity, Contract |
| 4 | Port: an app with no `ports` or several → `app_port: 8080` added to `x-houston`; exactly one → nothing added | `TestInit_AddsTheAppPortOnlyWhenNeeded` | Contract |
| 4a | The rename: `x-houston.app_port` is read as the port (overriding `ports`, required with zero or several, 1–65535), and `x-houston.port` is refused with "`port` is now `app_port`". The existing port subtests in `load_test.go` move to `app_port` | `TestLoad` port subtests, `TestAppPortRenamed` | Contract, Honest surface |
| 5 | Can't fix without guessing: no service with `build:`, two with it, `network_mode`, `env_file`, a flow-style block missing `health` → each problem named, exit 1, every file byte-identical | `TestInit_RefusesWhatItCantFix` (table) | Preconditions, Atomicity |
| 6 | Rerun → "already set up", every file byte-identical. Cut short (only the Dockerfile written) → the rerun writes only the rest | `TestInit_IdempotentAndResumes` | Crash & repair, Re-entry |
| 7 | `.env`: a compose file referencing `${SECRET_KEY_BASE}` and `${DB_HOST:-db}` → `.env` gets `SECRET_KEY_BASE=` only; existing values untouched; missing names appended. `.gitignore`: created, appended, or left when it already ignores `.env` | `TestInit_EnvAndGitignore` | Contract |
| 8 | A Rails app (its own Dockerfile, no compose) gets exactly the generic upsert: stages added from its Dockerfile and the default compose file, with no Rails-specific line anywhere | `TestInit_RailsGetsTheGenericSetup` | Honest surface, Parity |
| 9 | `internal/stack` is gone; no package imports it; `init`'s help says "Set up this folder for Houston"; the old `TestInit_Rails*` tests are replaced by these | build and grep in the review, and `TestInit_Help` | Honest surface |
| 10 | `-f sub/compose.yml init` sets up `sub/`; `init --server` → exit 2; a name typed at the prompt, and `Bad_Name` refused with nothing written | `TestInit_FileFlagNameAndUsage` | Contract |
| 11 | **The default runs:** in a temp folder with only `index.html`, `houston init` then `houston dev` → `http://localhost:8080/` 200 with the file; `houston test` → exit 0; `houston dev --production` → 200 | `TestInit_DefaultsRun` (tagged `integration`, run by `bin/test-integration`) | Parity |

Commands: `bin/go test ./internal/cli/ ./internal/project/ -run 'TestInit|TestLoad|TestAppPort'` (rows 1–10, 4a), `bin/test-integration -run TestInit_DefaultsRun` (row 11).

### Review round 1 (scotty-review, cold pass): what's added to Batch 1
- **The Dockerfile is the one compose builds.** It's `build.context` / `build.dockerfile` of the app service, as deploy reads it; not always `./Dockerfile`. So the compose file is planned first. A remote context (a git or HTTP URL) is refused: `init` only completes files in this folder.
- **Instructions are read as Docker reads them:**
  - lines ending in the escape character (`\`, or what `# escape=` sets) are one instruction
  - heredoc bodies (`RUN <<EOF … EOF`) aren't instructions
  - a UTF-8 BOM and CRLF line endings are kept, and added lines use the file's ending
  - naming a stage whose `FROM` spans lines puts `AS production` at the end of its last line

  The result is read again before it's written: if it doesn't have `dev`, `test` and `production`, nothing is written.
- **A sibling compose file isn't shadowed.** With `compose.yml` missing, a `compose.yaml`, `docker-compose.yml` or `docker-compose.yaml` in the folder is named, and nothing is written. `-f <that file>` upserts it instead.
- **`.dockerignore` is a sixth upserted file.** The default production image copies the folder and serves it, so `.git`, `.env` and `.houston` are excluded:
  - missing: written with those three
  - present: only what it doesn't already exclude is appended (`/.git/` counts for `.git`, `.env*` for `.env`)
- **`x-houston: ~` / `null`** is refused as not a mapping, like any other non-mapping. Only an empty `x-houston:` gets lines under it.
- **The rename, everywhere:** `mission_control/compose.yml` has `port: 80`, so it moves to `app_port: 80`. A test loads every `compose.yml` in the repo outside `testdata`, so a stale key can't hide again. The `EQUIP_SOURCE` copies the e2e scripts use were made by the old init: the scratchpad copy is updated, and Batch 2 regenerates it.

| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| R1 | `build: ./web` (with `web/Dockerfile`) → `web/Dockerfile` completed, no root Dockerfile; `dockerfile: Dockerfile.prod` → that file; a git-URL context → refused, nothing written | `TestInit_CompletesTheDockerfileComposeBuilds` | Contract |
| R2 | A `FROM` continued on the next line (named there, or unnamed); a `# escape=` backtick file; a heredoc containing `FROM`; a BOM; CRLF → each completed correctly (the goldens), and an unrecognizable result → refused | `TestInit_ReadsInstructionsAsDockerDoes` (table) | Contract, Atomicity |
| R3 | `docker-compose.yml` or `compose.yaml` present with no `compose.yml` → named, nothing written; `-f docker-compose.yml` → upserted | `TestInit_DoesntShadowAnotherComposeFile` | Preconditions |
| R4 | `.dockerignore`: written for an empty folder; an existing one with `/.git/` and `.env*` gets only `.houston`; one with all three is untouched | `TestInit_Dockerignore` | Contract |
| R5 | Gap-fill shapes: `x-houston: # note`, an anchor, a quoted key, no final newline, CRLF → the line added in place; `x-houston: ~` → refused, nothing written | `TestInit_UpsertsXHouston` (more cases) | Contract |
| R6 | Every `compose.yml` in the repo outside `testdata` loads (Mission Control's included) | `TestRepoComposeFilesLoad` | Honest surface |
| R7 | The note: two blank optional variables → one plural line; one set in the shell → not named | `TestDevProduction_RunsCompose` (more cases) | Signals |
| R8 | The defaults run: with a `.env` in the folder, `GET /.env` and `GET /.git/HEAD` → 404 from the production image | `TestInit_DefaultsRun` | Parity |

### Done (Batch 1)
- **Red first:**
  - rows 1–10 and 4a: 8 `TestInit_*` tests, `TestInit_Help`, `TestInit_FileFlagNameAndUsage`, the 4 `app_port` subtests and `TestAppPortRenamed`
  - row 11: `init` refused non-Rails folders
- **Green:**
  - the Go suite, and the integration suite (`TestInit_DefaultsRun` in 23 s: `init` → `dev` serves the page → `test` → `dev --production` serves it, with `/.env` and `/.git/HEAD` 404)
  - gofmt clean
- **Stack knowledge removed everywhere, not only from `init`:** the full suite found `houston dev --production`'s `RAILS_MASTER_KEY` note. It's now generic: blank optional variables (`${V:-}`, the new `Variable.BlankDefault`) are named, since a production image may need what dev falls back without. The `${MODE:-dev}` kind isn't named.
- **The default's host port:** this Mac already had 8080 taken (another project's container). The integration test publishes a random host port, and the app still listens on 8080 in its container.
- **scotty-review, round 1: request changes.** Three HIGH and two MEDIUM, all fixed test-first (see "Review round 1" above):
  - HIGH: the Dockerfile compose actually builds
  - HIGH: Mission Control's own `compose.yml` still had `port:`, so a repo-wide load test now guards it
  - HIGH: a `FROM` continued over lines could be corrupted, or yield a deploy of the wrong image
  - MEDIUM: a sibling `docker-compose.yml` was shadowed by a stub
  - MEDIUM: the default image served `.git/` and `.env`, so `.dockerignore` is now upserted
- **scotty-review, round 2: approve with findings.** One MEDIUM (a comment inside a continued `FROM` ended the join) and four LOW:
  - a variable, bare-GitHub or `..` build context
  - `<Dockerfile>.dockerignore`
  - `houston dev` serving the mounted folder to the network, so the default now publishes on `127.0.0.1`
  - mixed line endings rewritten

  All fixed test-first.
- **Mutations (each caught):**
  - the stage name, production from the final stage, dev and test, case-insensitive names, `FROM` flags, the no-FROM refusal, the final newline
  - the health gap-fill; `app_port` always or never; the flow-style refusal; indentation; the appended block's `app_port`; the port count; the compose check before writing; the default Dockerfile
  - the old `port` key; `BlankDefault` for real defaults or required variables; the note in plain dev
  - continuations, the escape directive, heredocs, BOM, CRLF, mixed endings, the re-read
  - the build context and dockerfile; remote, variable and outside contexts; sibling compose files; `.dockerignore` written, appended, equivalent patterns, per-Dockerfile
  - null `x-houston`; CRLF gap lines; the local-only port
  - Five needed better tests first: case-insensitive names, flags on the final stage, the appended `app_port`, a required variable that's also `${V:-}`, the re-read (an unterminated heredoc).
  - Two lines turned out inert and were cut, not tested around: re-adding the escape character after a comment inside a continuation (the loop's flag already carries it), and a redundant clause in the added-stages note's condition.
- **The spec** (version 13): §00 "Generic init", §03, §04 (the presets table replaced by what `init` writes, `app_port`), §05, §12 step 7, §13 (the PHP question dropped).
- **For Batch 2:** the `EQUIP_SOURCE` copy the e2e scripts use was made by the old Rails init. The scratchpad copy's `port: 80` is now `app_port: 80`; Batch 2 regenerates it with the generic init.

### Agent loop checkpoints
- The red tests go in first → the RED map is reported → implement to green.
- Batch 1 green → scotty-review with a cold pass → Done notes → the spec update → commit.
- Batch 2 is expanded from this file when it starts.

## Open questions
None blocking. Two defaults to change if you'd rather:
- **The app port:** 8080 (unprivileged, and what the dev fixture already uses).
- **The health path:** `/`, because a static site answers it. Apps whose `/` redirects change it; Rails and Laravel apps would set their built-in `/up`.

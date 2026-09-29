# Plan: `houston exec`, and `houston init --name`

## Direction
- gantry (the Go web framework) runs an app's commands in its local container by default: "I was thinking of having this work in a local docker container by default. This would make using houston a lot easier and gantry and houston are parts of the same ecosystem." "Under the hood, that could just run houston." (2026-09-29, gantry's `docs/plans/mission-control.md`, G0)
- `gantry db migrate` becomes `houston exec myapp db migrate`, and `gantry new` runs `houston init` for Houston's files. Order: "houston, then gantry. I assume the changes to houston won't break anything already working."

## Evidence
- **Houston runs a command in the app only through `console`.** `runConsole` (`internal/cli/console.go:14`) runs `commands.console` with `sh -c` in the running app container, and refuses when nothing's running. There's no way to run an arbitrary command, and none when the app is stopped.
- **`console` finds the main instance only.** `appContainer` (`internal/cli/console.go:71`) filters on `com.docker.compose.project=<name>`, but a branch's `houston dev` runs as `<name>-<branch>` (`devNaming`, `internal/cli/devname.go:171`). `console` and `logs` are left as they are here (see Out of scope).
- **A one-off container can't reuse the dev override as it is.** `DevOverride` (`internal/variant/dev.go:32`) puts the app on the external `houston-dev` network, which exists only while the dev proxy is set up; `compose run` fails when it's missing. Its `ports: !reset` on every service does what a one-off needs: no dependency binds a host port.
- **`houston test` already runs a one-off** (`internal/cli/test.go:22`): `compose run --rm` in a throwaway project. `exec` needs the dev project instead, since the point is the dev data (`gantry db migrate` changes the database `houston dev` serves).
- **`init` asks for the name only on a terminal** (`askName`, `internal/cli/init.go:281`); without one, it takes the folder's name. A tool running `init` (gantry) whose app name isn't the folder's name has no way to say it.

## Design
**`houston exec CMD [ARGS...]`:** runs a command in this checkout's dev instance, the one `houston dev` runs here (the main one, the branch's, or the remembered `--as`).
- **Running:** `docker exec -i [-t] <app container> CMD ARGS...`, in the instance's project (`<name>`, or `<name>-<branch>`). Arguments go as argv, not through a shell. `-t` only when stdin is a terminal, as `console`.
- **Not running:** a one-off app container in the same Compose project, so the same image, code mounts, volumes (data) and `.env` (or the main checkout's, as `houston dev`): `docker compose -p <project> [--env-file ...] --project-directory <dir> -f compose.yml -f .houston/compose.exec.yml run --rm [-T] <app> CMD ARGS...`.
  - `.houston/compose.exec.yml` is the dev override without the route: the dev target, the app's limits reset (as dev), every service's ports reset, and no `houston-dev` network. It's its own file, so a running `houston dev`'s `compose.dev.yml` is never rewritten.
  - A branch that has never run starts from a copy of main's data first, as `houston dev` does.
  - Compose starts the app's dependencies (a database) for the run. If nothing of the project was running before, they're stopped afterwards, so `exec` leaves things as it found them.
  - The image is the dev image `houston dev` built; Compose builds it if there's none.
- **Exit code:** the command's. Flags after `CMD` are the command's, not Houston's (`houston exec ls -la`).
- **No command:** usage error (exit 2).

**`houston init --name NAME`:** the project name, without asking. It's checked like a typed one. It applies when `init` creates compose.yml; when compose.yml exists and names another project, `init` refuses (exit 2) before changing anything.

**Docs:** README's local commands and `docs/agents.md` gain `houston exec`; README's `init` section gains `--name`.

**Out of scope:** `console` and `logs` finding a branch's instance (they look up the main one; worth its own change, since it changes which container they reach); `exec --server`.

## AC ↔ test
| # | Acceptance criterion | Test |
|---|---|---|
| 1 | Running app: `docker exec -i -t <id> CMD ARGS` on a terminal, no `-t` when piped; the command's exit code | `TestExec_RunsInRunningApp`, `TestExec_PipedInputSkipsTTY` |
| 2 | Flags after the command are passed to it | `TestExec_PassesFlagsThrough` |
| 3 | On a branch, the lookup is the branch's project | `TestExec_BranchInstance` |
| 4 | Not running: `compose run --rm` in the instance's project with the exec override, `-T` when piped, the command's exit code | `TestExec_OneOffWhenNotRunning` |
| 5 | The one-off's dependencies are stopped afterwards when nothing was running, and left alone when something was | `TestExec_OneOffStopsWhatItStarted`, `TestExec_OneOffLeavesRunningServices` |
| 6 | A branch's one-off starts from a copy of main's data | `TestExec_BranchOneOffCopiesMainData` |
| 7 | A worktree without `.env` uses the main checkout's | `TestExec_OneOffUsesMainCheckoutEnv` |
| 8 | No command: exit 2, nothing run | `TestExec_NeedsACommand` |
| 9 | The exec override: dev target, ports reset everywhere, no dev network, limits reset as dev's | `internal/variant` `TestExecOverride` |
| 10 | `init --name` creates compose.yml with that name without asking, even on a terminal; a bad name is exit 2; an existing compose.yml with another name is exit 2 and nothing changes | `TestInit_NameFlag`, `TestInit_NameFlagInvalid`, `TestInit_NameFlagConflicts` |
| 11 | Everything already working is unchanged: the existing suites pass, `console` and `logs` untouched | `bin/go test ./...`, `bin/test-integration` |
| 12 | Real Docker: `exec` in the running dev app, and in a one-off after it stops (same data, dependencies stopped after) | `TestConsoleLogsIntegration` (extended) |

## Evidence
- **Tests first:** all 11 `exec` and `init --name` tests failed for the right reason before the change (`unknown command "exec"`, `unknown shorthand flag: 'l' in -la`, `unknown flag: --name`), and `internal/variant` didn't build (`undefined: ExecOverride`). `TestExec_NeedsACommand` passed already (an unknown command is exit 2 too) and is kept as a guard.
- **Suites:** `bin/go test ./...` ok in every package, gofmt clean.
- **Mutation check.** All 14 were caught:
  - `exec`: no `-t` on a terminal; `-t` always; flags parsed as Houston's; the main instance's project on a branch; a one-off without `-T`; never stopping what it started; always stopping; no copy of main's data; ignoring the main checkout's `.env`; writing `compose.dev.yml` instead of `compose.exec.yml`
  - the exec override keeping ports; keeping the app's limits
  - `init`: `--name` ignored; no check against an existing compose.yml's name
  - (the two stop mutations first only failed to compile, an unused variable, which proves nothing, so they were redone as valid code)
- **Real Docker** (rootless Docker 29.7.2, Compose 5.5.1; the test container given the rootless socket by an uncommitted `compose.override.yml`): `TestConsoleLogsIntegration` passed with `exec` in the running app (`cat /stage` is `dev`, a file written to `/data`), then after `houston dev` stopped, as a one-off: the same file read back, the command's exit code 5 passed through, and neither the app nor the database left running.
- **Full integration suite** (`bin/test-integration`, the same setup): everything passed but `TestDevLocalhostIntegration`, on a first run against a daemon that had never pulled `busybox:1.37`. Its `throughProxy` reads the page with `CombinedOutput`, so the pull's progress was read as the answer. Run again with the image there, it passed. That's the test's own fragility on a fresh daemon, not this change; worth a fix of its own (read stdout only).

# Plan: `houston exec --server`

## Direction
- Gantry's first-install password setup needs a one-time code that only the deployed app can print: `/app task auth:setup-code`, run inside its container. Gantry's `docs/auth.md` already tells operators to run `houston exec --server /app task auth:setup-code`, and says Houston's remote exec is "currently in development". No Houston release has it, so today the only way is SSH plus `docker exec` by hand (found deploying svnmns to the v0.5.8 server, 2026-11-20).
- `docs/plans/exec-and-init-name.md` left `exec --server` out of scope.

## Evidence
- **`console --server` already does most of it** (`internal/cli/console_server.go`): `remote()` for the client and project, `client.SSHTarget(os.Getenv)` for `houston@<LAN address>` (`houston login --ssh` or `HOUSTON_SSH`), the container lookup `docker ps -q --filter label=service=<name> --filter label=role=web | head -n1`, `singleQuote`, and `runSSH`, which tests replace.
- **It always asks for a TTY** (`ssh -t`, `docker exec -it`), so piped or scripted use gets a pseudo-terminal's output (CRLFs, no clean EOF on stdin).
- **It interpolates the project name unquoted** into the filter. A compose file's name is a DNS label, but `--project` is free text.
- **With no app container running**, the lookup is empty and `docker exec ""` fails with Docker's own unhelpful message.
- **`exec` takes the command with `SetInterspersed(false)`** (`internal/cli/cli.go`), so Houston's flags must come before CMD and everything after is the command's.

## Design
**`houston exec --server [--project NAME] CMD [ARGS...]`:** the command in the project's running app container on the server.
- **The project:** `--project`, else the compose file's name (`remote()`, as the other `--server` commands). Neither → exit 2.
- **The remote shell command**, over `ssh <target>`:
  `c="$(docker ps -q --filter '<label=service=NAME>' --filter label=role=web | head -n1)"; [ -n "$c" ] || { echo '<NAME isn't running on the server>' >&2; exit 1; }; exec docker exec -i [-t] "$c" 'CMD' 'ARG'...`
  - Every word (CMD, each ARG, the filter, the message) is single-quoted for the remote shell, so no argument can run anything.
  - `exec` hands the exit code straight back through ssh, which `runSSH` passes through.
- **A terminal only when stdin is one** (`stdinIsTerminal`, as the local `exec`): `ssh -t` and `docker exec -i -t`; otherwise `ssh -T` and `docker exec -i`, so it pipes and scripts.
- **Output:** ssh writes straight to Houston's stdout and stderr. Houston prints nothing of its own and logs nothing: the main use is a one-time credential.
- **No SSH target:** exit 2 with the same message as `console --server` (`houston login --ssh …`, `ssh-copy-id …`), naming `exec --server`.
- **`--project` without `--server`:** exit 2. A local exec runs in the folder's project; silently ignoring the flag could run the command in the wrong one.
- **Shared with `console --server`:** both call `runInServerApp(client, name, what, tty, argv, stderr)`. Console keeps `tty = true` and runs `sh -c <console command>`, so it also gains the quoted name and the "isn't running" message. Its TTY behaviour is unchanged (see Out of scope).

**Docs:** `houston exec --help` (usage line with the flags before CMD, a long description, examples), README's login and "What's running" sections, `docs/agents.md`'s deploy step.

**Out of scope:**
- `console --server` asking for a TTY only on a terminal, as the local `console` does. A one-line change in its call, but it changes a working interactive command.
- Reaching the server from outside the LAN (`cloudflared access ssh`), as for `console --server` (cli-server.md, Batch 5's decision).

## AC ↔ test
| # | Acceptance criterion | Test |
|---|---|---|
| 1 | On a terminal: `ssh -t <target>` with `docker exec -i -t "$c" 'CMD' 'ARG'…`, the command's exit code passed through, nothing printed by Houston | `TestExecServer_RunsInServerApp` |
| 2 | Piped: `ssh -T` and `docker exec -i`, no `-t` | `TestExecServer_PipedInputSkipsTTY` |
| 3 | The project from the compose file; flags after CMD (`-la`, `--server`) are the command's | `TestExecServer_ProjectFromComposeFile` |
| 4 | A real shell running the remote command hands docker every argument intact (quotes, `$(…)`, backticks, spaces, `\`, empty, `-n`, `*`) and a hostile project name, and nothing runs | `TestExecServer_ArgumentsReachTheContainerIntact` |
| 5 | No app container on the server: exit 1, `houston: <name> isn't running on the server` on stderr, no `docker exec` | `TestExecServer_AppNotRunningOnServer` |
| 6 | No SSH target: exit 2, the login and `ssh-copy-id` hint naming `exec --server`, no ssh | `TestExecServer_NoSSHTarget` |
| 7 | No project, no command, `--project` without `--server`: exit 2, nothing run | `TestExecServer_Usage` |
| 8 | `console --server` unchanged but for the shared helper: `ssh -t`, `sh -c` with the hostile console command quoted, its exit code | `TestConsoleServer` (expectation updated) |

## Evidence
- **Tests first:** the 7 new tests failed for the right reason before the change (`unknown flag: --server`, `unknown flag: --project`).
- **Suites:** `bin/go test ./...` ok in every package; gofmt clean.
- **Mutation check.** All 9 were caught: arguments unquoted; a TTY always; never a TTY; the exit code dropped; the project name unquoted; no "isn't running" check; `--project` without `--server` allowed; `--project` ignored; no SSH-target check.
- **Not run against a real server here.** The remote command was run by a real `sh` against a fake `docker` (AC 4, 5); a real run is `houston exec --server --project svnmns /app task auth:setup-code` from the LAN.

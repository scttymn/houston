# Plan: `houston exec --server`

## Direction
- Gantry's first-install password setup needs a one-time code that only the deployed app can print: `/app task auth:setup-code`, run inside its container. Gantry's `docs/auth.md` tells operators to run `houston exec --server /app task auth:setup-code` (found deploying svnmns to the v0.5.8 server, 2026-11-20).
- **v0.5.9 shipped it over SSH** (PR #2), as `console --server` does: `houston login --ssh`, `ssh-copy-id`, LAN only.
- **Then, through the API instead:** "Right now we have houston login and it doesn't need a server address. houston exec --server should be able to work as simply." (2026-11-21). So `exec --server` needs only the saved login, and works from anywhere, as `status` and `logs --server` do. Non-interactive is fine for now.
- `docs/plans/exec-and-init-name.md` left `exec --server` out of scope.

## Evidence
- **`logs --server` already crosses the API from anywhere** (`mission_control/app/api/v1_logs.go`): behind `V1Door` (a `hou_` token, setup finished), it finds the newest running app container with `docker ps --filter label=service=<name> --filter label=role=web` and streams `docker logs` with a flushing writer. The CLI side is `server.Client.Logs` (`internal/server/server.go`), with no client timeout.
- **`dockercmd.Stream` merges stdout and stderr** into one writer (`app/services/dockercmd/dockercmd.go`). A command's stdout should stay apart, so `houston exec --server cat x > file` gets only the file.
- **An HTTP trailer can't carry the exit code**: a proxy on the way (Cloudflare's tunnel) may drop trailers. Frames in the body cross any proxy that passes a body.
- **An old server's answer to an unknown route** is the router's own 404 (`web.Router`'s catch-all: HTML, or `{"error":...}` to a client asking for JSON). The route's own 404s (`no project nope`, `shop isn't running`) look the same, so a header tells them apart.
- **Mission Control logs a failed request's error** (`web.Router.Fail`: method, path, err), never a body. The route's errors never hold output.

## Design
**`POST /api/v1/projects/{name}/exec`** `{"command": ["/app", "task", "auth:setup-code"]}`, behind `V1Door`:
- `docker exec <newest app container> <command...>`: the argv as given, no shell, no `-i`, no `-t`. A deadline of an hour; a client that goes away cancels it.
- **The answer** is `200`, `Content-Type: application/vnd.houston.exec-stream`, `Cache-Control: no-store`. Its body is frames (`internal/execstream`, shared by both ends): a byte of kind (1 stdout, 2 stderr, 3 exit), four bytes of big-endian length, the bytes. Each frame is flushed as it's written. The exit frame (the code, in decimal) comes last; a stream without one was cut off. When docker itself can't run, the stderr frame says so and the code is 1.
- **Refusals**, before anything runs: no or a wrong token → 401; no project → 404 `no project <name>`; nothing running → 404 `<name> isn't running`; not JSON, or `command` not a list of strings → 400; an empty list or program → 422.
- **`Houston-Exec: 1`** on every answer of the route, its errors too.
- **The output is never logged or kept:** it goes to the answer and nowhere else. No log line, deploy log or database row holds it.
- **`dockercmd.StreamApart`** is `Stream` with stdout and stderr to their own writers; the fake answers it with `OnApart(stdout, stderr, code)`.

**`houston exec --server [--project NAME] CMD [ARGS...]`:**
- `remote()`'s client and project (`--project`, else the compose file's name; neither → exit 2). `server.Client.Exec` sends the argv as a JSON list, never joined.
- Frames to Houston's stdout and stderr as they come; the exit code is the command's. Houston prints nothing of its own on success.
- Exit 1 with a message: not logged in (`remote()`'s), a refused token (`run houston login`), a 404 or 405 without `Houston-Exec` ("this server's Houston doesn't have exec --server yet; update it: houston update"), the route's own errors (`svnmns isn't running`), an answer that isn't frames (a login page in the way), a cut-off stream.
- `--project` without `--server` → exit 2, as before.
- **No SSH** any more for `exec --server`. `console --server` keeps its SSH as it is (it's interactive). `houston login --ssh` is for `console --server` only again.

**Docs:** `houston exec --help` (through Mission Control, no stdin or terminal, an hour at most, never logged), README's "What's running" and login sections, `docs/agents.md`'s deploy step.

**Out of scope:**
- Interactive `exec --server` (stdin, a terminal): it would need a two-way stream (a WebSocket, say) through the tunnel. `console --server` stays the interactive way, over SSH on the LAN.
- `console --server` through the API: the same, once there's a two-way stream.

## AC ↔ test
| # | Acceptance criterion | Test |
|---|---|---|
| 1 | The route runs the argv as an array (`docker exec <newest> /app task 'auth:setup-code; touch /pwned' '$(id)' 'a b' ''`), an hour's deadline; stdout and stderr back apart in frames, then the exit code; the content type, `Houston-Exec` and `no-store` | `mission_control/app/api` `TestV1Exec`, `TestV1Exec_Succeeds` |
| 2 | Refusals before anything runs: 401 without or with a wrong token; 404 no project, 404 not running; 400 not JSON or a string command; 422 an empty command or program; `Houston-Exec` on each but the 401s | `TestV1Exec_Refusals` |
| 3 | The output (stdout and stderr) is in no log Mission Control writes, at any level, and in no table of the database | `TestV1Exec_NeverLogsOrStoresTheOutput` |
| 4 | The CLI's client against the real route over HTTP: argv there, output and code back; an unknown project's message; the real router without the route → `ErrNoExec` | `TestV1Exec_WithTheCLIClient` |
| 5 | Frames on the wire byte for byte, flushed each; big output split; a round trip; a cut-off stream and nonsense refused | `internal/execstream` `TestWriterFrames`, `TestRoundTrip`, `TestReadCutOff`, `TestReadRefusesNonsense` |
| 6 | `StreamApart`: stdout and stderr apart, the exit code, the deadline | `dockercmd` `TestStreamApart` |
| 7 | CLI: argv sent as a list, the token sent, output to stdout and stderr apart with nothing of Houston's, the command's exit code; no SSH, with no SSH target set | `internal/cli` `TestExecServer_RunsThroughTheAPI` |
| 8 | CLI: the project from the compose file; flags after CMD are the command's | `TestExecServer_ProjectFromComposeFile` |
| 9 | CLI: exit 1 and the message for an old server (HTML 404), not running, a refused token, a cut-off stream, an answer that isn't frames; and when not logged in | `TestExecServer_Failures`, `TestExecServer_NotLoggedIn` |
| 10 | CLI: no project, no command, `--project` without `--server` → exit 2, nothing asked | `TestExecServer_Usage` |
| 11 | `console --server` unchanged | `TestConsoleServer` |

## Evidence
- **Tests first:** the route's tests failed on the missing route (the router's HTML 404), and the CLI's on the SSH path still in place (`exec --server connects over SSH on the LAN`).
- **Suites:** `bin/go test ./...` ok in every package; Mission Control's own (`houston test` in `mission_control/`, which `gantry test` runs: `go vet ./... && go test ./...` in its test image); gofmt clean.
- **Mutation check.** All 12 were caught, and a no-op control survived: the argv shell-joined (`sh -c`); stdout logged at debug; the exit code dropped on the server; `Houston-Exec` set only after the project lookup; no deadline; an empty program allowed; an old server's 404 not recognised; a non-frames answer accepted; stderr merged into stdout; the CLI's exit code dropped; `--project` ignored; a cut-off stream read as success.
- **Not run against a real server here.** On the server, after the next release: `houston exec --server --project svnmns /app task auth:setup-code` from anywhere, with only `houston login`.

### Before: the SSH version (v0.5.9)
`ssh -t|-T houston@<LAN> 'c="$(docker ps …)"; … exec docker exec -i [-t] "$c" '\''CMD'\'' …'`, every word single-quoted, a TTY only on a terminal. It was proven with a real `sh` against a fake `docker` and 9 caught mutations. `console --server` still uses that helper (`runInServerApp`), with its quoted project name and "isn't running on the server" message.

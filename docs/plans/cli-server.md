# Plan: The CLI against the server (build step 4b)

Spec v11: https://claude.ai/artifact/R3d4fzN1u5QUP4kT88mqug (§5 CLI reference and `--server`; §10 API table, Settings › API tokens, security notes).
Your decision (build step 4): "It makes this always work from the cli (agentic interactions)". Every Mission Control action gets a CLI path, usable by an agent without a browser.

## Goal

From a laptop (or an agent), with a named API token: see status, deploys and their logs; set secrets; trigger and follow a deploy to its result; link a repo; read app logs; open a console. Same verbs as local, pointed at the server with `--server`.

## Scope

- **Settings › API tokens:** create a named token (shown once), list with last use, revoke.
- **`/api/v1`** (personal tokens, through the tunnel), next to the runner's local API, which is unchanged:

| | runner token | personal token |
|---|---|---|
| `/api/projects/sync`, `/api/projects/:name/secrets/:key` (values), `/api/deploys`, `/api/runner/jobs/claim` | yes, never through the tunnel | **no** |
| `/api/v1/…` (status, deploys, logs, secret names and writes, link, deploy, webhook) | **no** | yes, through the tunnel |

  **Personal tokens never read a secret value or a deploy key.** Secrets stay write-only, as on the page. The one value the remote API returns is the **webhook secret, under the page's rule**: only until its first verified delivery, because it has to be pasted into the git host.
- **CLI:**
  - `houston login` / `logout` (a saved server)
  - `HOUSTON_SERVER` + `HOUSTON_API_TOKEN` for agents (no file needed)
  - optional Cloudflare Access service-token headers (`HOUSTON_ACCESS_CLIENT_ID` / `_SECRET`, or saved with login), for when Access guards `admin.<base>`
- **The project** for a `--server` command: `--project <name>`, else the `name` in `./compose.yml` (or `-f`).
- **Defer:** `snapshots` / `backup` / `restore --server` (build steps 5 and 6: their API arrives with them); console through `cloudflared access ssh` from outside the LAN (a later batch here only if you want it; see Open questions).

## Batches

1. **API tokens and `houston login`** (below).
2. **Reading:**
   - `houston status --server`: projects, or one project's status, running SHA, domains with states, last deploy
   - `houston deploys --server`: history
   - `houston deploys show <n> --server [--follow]`: steps and log; `--follow` polls until it's finished, exit 0 on GO, 1 on NO-GO
3. **Secrets:** `houston secrets list | set NAME | unset NAME | generate NAME --server`. The value comes from stdin, never argv. Uncarriable values are refused with the server's message. `list` shows names, required/optional and set/unset only.
4. **Actions:**
   - `houston deploy --server [--follow]`: check for changes, then queue the newest matching commit and print its number; with `--follow`, stream its log to the result, exit code as above
   - `houston link --server <repo-url> [--branch] [--file] [--wait]`: prints the deploy key; `--wait` polls access until GO, then reads and saves, and prints the webhook URL and secret
   - `houston webhook --server [--rotate]`
5. **App logs and console:**
   - `houston logs --server [-f]`: Mission Control streams the running app container's `docker logs`
   - `houston console --server`: runs `commands.console` (server form) in the running app container, interactively over SSH to `houston@<server>`. `houston login` records the SSH target (LAN) and says how to authorize your key.
6. **An agent's run on svnmns.com:** from the Mac, with only `HOUSTON_SERVER` and `HOUSTON_API_TOKEN`, a script links a Forgejo repo, sets secrets, `deploy --follow`s to GO, reads `status` and `logs`, and a failing push gives exit 1 with the reason. No browser.

## Batch 1: API tokens and `houston login`

### Design (short)
- **`ApiToken`** (`name` unique, 1–50 chars; `token_digest` SHA-256; `last_used_at`; timestamps).
  - The token is `hou_` + 32 random bytes (base64url), shown once, on the page right after creation, and never again.
  - Revoke deletes the row.
- **Settings › API tokens** (`/settings/tokens`, admin only): create (name), a list (name, created, last used), revoke. A **Settings** link joins the header nav.
- **`Api::V1::BaseController < ActionController::API`:**
  - `Authorization: Bearer hou_…` → a digest lookup (compared in constant time). Anything else is 401 (the runner token included).
  - Allowed through the tunnel: no `Cf-Ray` refusal here, since this is the remote API.
  - 409 until setup is finished.
  - `last_used_at` is updated at most once a minute.
- **The runner API stays as it is:** it compares only `HOUSTON_RUNNER_TOKEN`, so a personal token is a 401 there. A test pins this.
- **`GET /api/v1/me`** → `{token: <name>, server: <base domain>}`, for login to check against.
- **CLI `internal/server`** (a client like `internal/mission`, for `/api/v1`):
  - **Config resolution:** `HOUSTON_SERVER` + `HOUSTON_API_TOKEN` (both required together), else `~/.config/houston/server.json` `{url, token, access_client_id, access_client_secret}`.
  - With Access credentials, every request carries `CF-Access-Client-Id` / `CF-Access-Client-Secret`.
  - A 401 → "the API token was refused (revoked?); run houston login".
- **`houston login [url]`:**
  - The URL comes from the argument, else a prompt.
  - The token comes from `HOUSTON_API_TOKEN`, else stdin: a hidden prompt on a terminal, or a line on a pipe.
  - Optional `--access-client-id` / `--access-client-secret`.
  - It calls `/api/v1/me`, then writes the config (dir 0700, file 0600, atomic) and prints "Logged in to <server> as token <name>". It writes nothing if `me` fails.
- **`houston logout`** deletes the config.

### Contract pin
- `POST /settings/tokens {name}` → the page with the token once. A blank, a duplicate, or > 50 chars → 422. Signed out → sign-in.
- `DELETE /settings/tokens/:id` → gone; a later request with it → 401.
- `GET /api/v1/me` → 200 `{token, server}` / 401 / 409 (setup).

### AC ↔ test map (Batch 1)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Create shows `hou_…` once (≥ 47 chars); the list afterwards never contains it; only the digest is stored; a blank, duplicate or 51-char name → 422 | `controllers/settings/tokens_controller_test.rb` `test "a token is shown once"` | Contract |
| 2 | Revoke deletes it; its next API call → 401 | `test "revoking a token"` | Authz |
| 3 | Signed out: the token pages → sign-in, nothing created | `test "tokens need the admin"` | Authz |
| 4 | `/api/v1/me` with a token → 200 `{token, server}`, **with `Cf-Ray` too** (through the tunnel is fine); `last_used_at` set, and not rewritten within a minute | `integration/api_v1_auth_test.rb` `test "a personal token reaches the remote API"` | Contract |
| 5 | `/api/v1/me` with no token, a wrong one, a revoked one, or **the runner token** → 401; before setup → 409 | `test "the remote API takes only personal tokens"` | Authz, Preconditions |
| 6 | **A personal token on the runner API** (sync, a secret value, claim) → 401 | `test "personal tokens can't use the runner API"` | Authz |
| 7 | CLI config: env beats the file; env needs both variables; Access headers sent when configured; a 401 → the "run houston login" message | `internal/server` `TestConfigAndHeaders` (httptest) | Contract |
| 8 | `houston login <url>` with the token on stdin: calls `me`; writes the config at 0600 in a 0700 dir; prints the token's name and server | `internal/cli` `TestLogin` | Contract |
| 9 | Login with a refused token → exit 1, no file written; `houston logout` removes it | `TestLoginRefused` | Crash & repair |

### Agent loop checkpoints
- Red for all rows, then green; scotty-review with the cold pass; the next batch from this file.
- Ready: the full Go and Rails suites, lint on touched files, and Batch 6's run.

### Done (Batch 1)
- **Red:** the Rails tests failed to load (no `Settings` namespace), and the Go tests didn't compile. **Green:** Mission Control 136 runs and the Go suite (9 packages). The migration runs down and up; rubocop and gofmt are clean.
- **Mutations, each caught:** the runner token accepted on `/api/v1`; `last_used_at` written on every request; a refused token saved by login; half an env config (`HOUSTON_SERVER` without the token) accepted.
- A **Settings** link is in the header nav; Settings › API tokens is its first page.

## Batch 2: Reading (status, deploys, a deploy's log)

### Design (short)
- **API** (`/api/v1`, personal tokens):
  - `GET /projects` → each project's name, status (queued / in_flight / go / no_go / standby), running SHA, host, domain states, and last deploy.
  - `GET /projects/:name` → the same, plus deploy rule, services, repo and branch, webhook verified or not, and the secrets summary (name, required, set).
  - `GET /projects/:name/deploys?page=` → 20 per page, newest first.
  - `GET /projects/:name/deploys/:number?log_from=<byte>` → the deploy, its steps, `log` from that byte (at most 256 KiB, cut on a character boundary) and `log_size`.
  - Unknown → 404.
  - A shared serializer never emits secret values, the deploy key, or the webhook secret. A test scans every response for them.
- **CLI:**
  - `houston status [--project] [--json]`: one project (from `--project`, else `./compose.yml`'s name), or every project when there's no compose file here.
  - `houston deploys [--project] [--json]`
  - `houston deploys show [n] [--follow] [--json]`: the latest when no n. `--follow` polls every 2 s with `log_from`, prints only new log text, waits through queued, and ends with GO (exit 0) or NO-GO and its reason (exit 1). Without `--follow`, a finished deploy's exit code says the same.
  - These are server-only commands, so they need no `--server`.
- **Project resolution** (shared by every remote command from here on): `--project`, else the compose file's `name` (a file that doesn't load is exit 2 with its problems), else "say which project: --project <name>" (exit 2).

### AC ↔ test map (Batch 2)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | `GET /projects`: statuses, running SHA, domain states, last deploy | `integration/api_v1_read_test.rb` `test "projects"` | Contract |
| 2 | `GET /projects/:name`: facts and the secrets summary; unknown → 404 | `test "a project"` | Contract |
| 3 | No response (all four endpoints) contains a secret's value, the deploy key or the webhook secret | `test "reading never reveals secrets"` | Authz |
| 4 | Deploys: newest first, 20 per page; unknown project → 404 | `test "deploys"` | Contract, Scale |
| 5 | A deploy: steps, `log` from `log_from`, `log_size`; past the end → empty; a chunk ≤ 256 KiB on a UTF-8 boundary; unknown number → 404 | `test "a deploy and its log"` | Contract, Scale |
| 6 | The client decodes all four from Mission Control's exact JSON; 404 → "no project x" / "no deploy #n" | `internal/server` `TestReading` | Contract |
| 7 | `status`: one project (compose name) vs all (no compose file); `--project` wins; `--json` prints the API's JSON | `internal/cli` `TestStatus` | Contract |
| 8 | `deploys show --follow`: prints each byte of the log once across polls, waits through queued, exits 0 on GO and 1 on NO-GO with the reason | `TestDeploysFollow` | Contract, Signals |
| 9 | No `--project` and no compose file for `deploys` → exit 2 naming `--project`; a broken compose file → exit 2 with its problems | `TestProjectResolution` | Preconditions |

### Done (Batch 2)
- **Red:** 4 of the Rails tests failed (the fifth, "never reveals", passed trivially against 404s until the endpoints existed), and the Go tests didn't compile. **Green:** Mission Control 141 runs and the Go suite. Rubocop and gofmt are clean.
- **Mutations, each caught:** the webhook secret in a response; `--follow` re-reading the whole log; NO-GO exiting 0; a chunk cut mid-character; a chunk starting mid-character.
- **A test gap closed by a mutation:** "a chunk cut mid-character" first survived. The fixture's 256 KiB boundary landed on a character boundary by luck (all two-byte `é` from an even offset). With one ASCII byte in front, the boundary falls inside an `é`, and both boundary rules are now caught.
- The API returns `log_next`, the byte to ask from next, so a client never re-reads or skips a byte, even when a chunk is trimmed to whole characters.

## Batch 3: Secrets

### Design (short)
- **API** (`/api/v1/projects/:name/secrets`):
  - `GET` → `[{name, required, set, updated_at}]`, names only
  - `PUT /:key {value}` → 200 `{name, set}`; blank or uncarriable → 422 with the model's message; a key the file doesn't reference → 404
  - `DELETE /:key` → 200
  - `POST /:key/generate` → 200 `{name, set}`
  - The value never appears in any response, and `value` is in the log filter (build step 4's review).
- **CLI:**
  - `houston secrets list [--project]`
  - `houston secrets set NAME [--project]`: the value comes from stdin, never argv. On a terminal it's a hidden prompt; on a pipe it's all of stdin, minus one trailing newline.
  - `houston secrets unset NAME`
  - `houston secrets generate NAME`
  - Nothing prints a value.

### AC ↔ test map (Batch 3)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | List: names, required, set, never a value | `integration/api_v1_secrets_test.rb` `test "listing"` | Authz |
| 2 | Set: stored (encrypted); the response has no value; a backslash → 422 with the base64 hint; blank → 422; unreferenced key → 404 | `test "setting"` | Contract |
| 3 | Unset removes it; generate stores ≥ 43 characters, not in the response | `test "unsetting and generating"` | Contract |
| 4 | The client and CLI: `set` reads the value from a pipe (one trailing newline dropped) and sends it as JSON; nothing prints it; argv has no value; `list` prints set/unset; the server's 422 message is shown and exits 1 | `internal/cli` `TestSecrets` | Contract, Signals |

### Done (Batch 3)
- **Red:** 3 Rails tests and the Go test failed. **Green:** Mission Control 144 runs and the Go suite; rubocop and gofmt are clean.
- **Mutations, each caught:** the value in the API's response (2 failures); any key settable; the CLI printing the value.
- A value given as an argument (`secrets set NAME value`) is a usage error, so values never land in shell history or `ps`.

## Batch 4: Actions (deploy, link, webhook)

### Design (short)
- **`POST /api/v1/projects/:name/deploys`** → **queues the current head** of what the deploy rule matches (the branch head; for tags, the highest tag by version order), moved or not, then updates `seen_refs`.
  - This is the page's "Deploy" and an agent's "deploy now". A plain check for changes finds nothing after a HOLD is fixed, because the commit didn't move.
  - Response: `{number, sha, ref, status}` (a queued one coalesces as always).
  - Unlinked → 422 "link the repo first"; ls-remote fails → 502 with git's line.
- **Links** (`/api/v1/links`), the Add project steps over the same models (`RepoLink`, `GitRemote`, `ProjectLinking`):
  - `POST /links {repo_url}` → a draft with its key, and the access check run at once: `{id, deploy_key, access: {ok, message}}`
  - `POST /links/:id/access` → the check again
  - `POST /links/:id/read {branch, compose_path}` → `{ok, found: {…}}` or `{ok: false, problems}`
  - `POST /links/:id/save` → `{project, webhook_url, webhook_secret}`
  - Input rules as the page's (hostile URLs are a 422 before git runs).
- **Webhook:** `GET /api/v1/projects/:name/webhook` → `{url, verified, secret}` and `POST …/webhook/rotate` → a new secret.
  - **The webhook secret is shown under the page's rule:** only until its first verified delivery (`secret: null` after). It's the one value this API returns, because you must paste it into the git host.
  - Project reads (`RemoteView`) still never include it.
- **CLI:**
  - `houston deploy --server [--project] [--follow]`: queues and prints `#n sha7 ref`. `--follow` reuses `deploys show --follow` (exit 0 GO, 1 NO-GO).
  - `houston link <repo-url> [--branch] [--file] [--wait]` and `houston link --continue <id>`:
    - It creates the draft and prints the deploy key.
    - Access OK → read → save → it prints the project, webhook URL and secret.
    - Access not OK: without `--wait`, exit 1 with "add the key, then: `houston link --continue <id>`". With `--wait`, it checks every 5 s for up to 10 minutes.
    - Read problems → exit 2, printed as the CLI prints them.
  - `houston webhook [--project] [--rotate]`

### AC ↔ test map (Batch 4)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Deploy queues the branch head even when `seen_refs` already has it; the tag rule picks `v1.10` over `v1.9`; a second call coalesces; unlinked → 422; ls-remote failure → 502 | `integration/api_v1_actions_test.rb` `test "deploy queues the head"` | Contract, Re-entry |
| 2 | Links: create (the key, access), a hostile URL → 422 with no git, access again, read (found / problems), save → a project with its link; saving without a read → 422 | `test "linking over the API"` | Contract, Authz |
| 3 | Webhook: the secret until verified, then null; rotate → a new secret, shown again | `test "the webhook"` | Authz |
| 4 | CLI `deploy --server --follow`: POST, then follows #n to its result; exit code 0 / 1 | `internal/cli` `TestDeployServer` | Contract |
| 5 | CLI `link`: access OK → saved, printing the webhook URL and secret; access denied → exit 1 with `--continue <id>`; `--continue` resumes the same draft; `--wait` polls until OK; read problems → exit 2 | `TestLink` | Contract, Crash & repair |
| 6 | CLI `webhook` prints the URL and secret, or "verified"; `--rotate` prints the new one | `TestWebhook` | Contract |

### Done (Batch 4)
- **Red:** 3 Rails tests failed, and the Go tests didn't compile. **Green:** Mission Control 147 runs and the Go suite; rubocop and gofmt are clean.
- **Mutations, each caught:** tags picked in string order (`v1.9` over `v1.10`); the webhook secret shown after its first delivery.
- **Cleanup:** the link-save response has its own type (`LinkSaved`) instead of borrowing `Webhook`'s fields.

## Batch 5: App logs and console

### Design (short)
- **`GET /api/v1/projects/:name/logs?tail=<1–10000, default 200>&follow=1`:** Mission Control finds the running app container (`docker ps --filter label=service=<name> --filter label=role=web`, the newest) and **streams** `docker logs --timestamps --tail N [--follow]` as `text/plain` (`ActionController::Live`).
  - No running container → 404 "<name> isn't running".
  - A follow stops when the client goes away (the write fails, and the docker process is killed), and after at most an hour.
  - `DockerCommand` gains `stream(*args) { |chunk| }`, with FakeDocker to match.
- **`houston logs --server [-f] [--tail N] [--project]`:** copies that stream to stdout. `houston logs` without `--server` is the local one, as before.
- **`houston console --server`** (LAN-only, your decision):
  - `commands.console.server` from the local compose file (a string console serves both).
  - It runs `ssh -t <target> 'docker exec -it "$(docker ps -q --filter label=service=<name> --filter label=role=web | head -n1)" sh -c '"'"'<command>'"'"''` and passes the exit code through.
  - The target: `HOUSTON_SSH`, else `houston login --ssh houston@<LAN IP>` (saved with the server).
  - With no target → exit 2 saying how to set one and how to authorize your key (`ssh-copy-id houston@<server>`).
  - The project name and command are quoted for the remote shell.

### AC ↔ test map (Batch 5)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Logs stream the docker output of the newest running app container, with `--tail 200` by default, `--follow` when asked, and `--timestamps` | `integration/api_v1_logs_test.rb` `test "app logs stream"` | Contract |
| 2 | Not running → 404; `tail` of 0, 10001 or `x` → 422; an unknown project → 404; no token → 401 | `test "logs are guarded"` | Contract, Authz |
| 3 | CLI `logs --server -f --tail 50` asks with `follow=1&tail=50` and copies the stream to stdout; without `--server`, the local path is unchanged | `internal/cli` `TestLogsServer` | Contract |
| 4 | CLI `console --server`: `ssh -t <target>` with the remote command, the name and a hostile console command (`echo 'hi'; id`) quoted, the exit code passed through; `HOUSTON_SSH` beats the saved one | `TestConsoleServer` | Contract (hostile input) |
| 5 | No SSH target → exit 2 with `houston login --ssh` and `ssh-copy-id`; no console command → exit 2; `login --ssh` saves the target | same | Preconditions |

### Done (Batch 5)
- **Red:** 2 Rails tests failed, and the Go tests didn't compile. **Green:** Mission Control 149 runs and the Go suite; rubocop and gofmt are clean.
- **An old test retired:** build step 1 pinned `logs --server` / `--tail` as unknown flags until they existed. Now that they do, the test checks that `--server` without a login exits 1 pointing at `houston login`, and never touches docker.
- **Mutations, each caught:** the console command unquoted for the remote shell; `follow` never sent; any `tail` accepted.
- **The real proof** (a real log stream through Cloudflare, a real console over SSH) is Batch 6.

### Decisions (from you)
1. **`console --server` is LAN-only for now** ("LAN-only is fine for now"). Reaching it through `cloudflared access ssh` from anywhere else is a later addition.

### Open questions (answered)
1. **`console --server` from outside the LAN.** The plan uses SSH to the server's LAN address (the installer already runs sshd, and you'd authorize your key for the houston user). From anywhere else it would need `cloudflared access ssh` and an SSH route on the tunnel. Is LAN-only fine for now?

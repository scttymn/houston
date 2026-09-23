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

  **Personal tokens never read a secret value or a deploy key.** Secrets stay write-only, as on the page.
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

### Decisions (from you)
1. **`console --server` is LAN-only for now** ("LAN-only is fine for now"). Reaching it through `cloudflared access ssh` from anywhere else is a later addition.

### Open questions (answered)
1. **`console --server` from outside the LAN.** The plan uses SSH to the server's LAN address (the installer already runs sshd, and you'd authorize your key for the houston user). From anywhere else it would need `cloudflared access ssh` and an SSH route on the tunnel. Is LAN-only fine for now?

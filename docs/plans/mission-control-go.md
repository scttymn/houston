# Plan: Mission Control in Go

## Direction
- "OK gantry needs features necessary to rebuild Houston's Mission Control." (2026-09-28) MC is gantry's reference app: a side-by-side rewrite with no regressions, then retire the Rails one. Port order: API + auth, jobs + leases, backups and restore, pages + live updates, setup flow.
- gantry's side is done (its `docs/plans/mission-control.md`, G0–G3, gantry v0.9.2). "let's start on mission control" (2026-09-30).
- "conceptually rails but ideal golang"; the target: one Go process, memory flat while it streams.

## Evidence (the Rails app, 2026-09-30; paths under `mission_control/`)
**The contract others depend on**
- Runner API, `/api/*` (`config/routes.rb:36-48`, `app/controllers/api/`): 12 endpoints. Bearer `HOUSTON_RUNNER_TOKEN` (constant-time, ≥ 32 chars), refused with an empty 404 when `Cf-Ray`/`Cf-Connecting-Ip` is present (`api/base_controller.rb:12-22`), 409 until Cloudflare is connected. A per-deploy token (`X-Houston-Deploy-Token`, SHA-256 in `deploys.token_digest`) fences a deploy's writes. The claim is a 25 s long poll (`api/runner_jobs_controller.rb:7-25`). The Go side is `internal/mission` (typed; `SyncRequest` is also the output of `houston inspect --json`, which MC runs).
- Personal API, `/api/v1/*` (routes 50-94): 43 endpoints, bearer `hou_…` (SHA-256 in `api_tokens`), JSON views in `app/models/remote_view.rb`. The Go side is `internal/server`; `--json` prints some bodies raw, so their full shape is user-visible. Streams: app logs (`api/v1/logs_controller.rb`, `docker logs`, chunked text) and a snapshot zip (`app/models/snapshot_export.rb`, `restic dump`, first bytes checked before answering).
- Webhooks on `hooks.<base>` (`webhooks_controller.rb:11-48`): HMAC-SHA256 or token headers from GitHub/Gitea/Forgejo/GitLab, per-minute rate limits, 202.
- Project hostnames answer only the maintenance page (`app/constraints/app_host.rb`); `/up`, `/ping` (`Installation.identity`, derived from `SECRET_KEY_BASE`).
- Quirks to keep (the Go clients rely on them): the secret endpoint answers `text/plain`; several v1 flags must be the JSON literal `true`; a snapshot with nothing deployed answers 200 `skipped`, a real one 202.

**Data** (`db/schema.rb`, 39 migrations)
- 18 app tables in `storage/production.sqlite3`; Solid Queue, Cache and Cable each in their own file.
- 11 partial unique indexes are the app's locks: one in-flight and one queued deploy per project, one running backup, one scheduled backup a day, one running server update, one active copy or deletion.
- Leases: claim by compare-and-swap with a token, heartbeat, takeover after 2 minutes of silence, finish fenced by the token (`deploy.rb:102-216,278-311`, `backup_run.rb:46-145`, `project_removal.rb:59-70`).
- Encrypted at rest with Active Record encryption (non-deterministic, keys from `AR_ENCRYPTION_*` in the server's `.env`): Cloudflare API token and tunnel token, deploy keys, webhook secrets, secrets' values, storage credentials and restic passwords.
- Sessions: a signed `session_id` cookie over a `sessions` row, bound to whether it came through the tunnel (`Cf-Ray`), 2 weeks idle, 30 days in all. Passwords: bcrypt.

**Work in the background** (`app/jobs`, `config/recurring.yml`, `config/queue.yml`): backups and restores (queues `backups`/`snapshots`, retry while busy for up to 3 h), change checks (one per project at a time), deletions, copy cleanup, prune (daily), registry clean-up, latest release (6-hourly), backup schedule (every minute), polling for changes (10 minutes), server update settling (every minute). They run docker, restic (in `restic/restic` containers), git over ssh (60 s timeouts), and call Cloudflare's and GitHub's APIs (10 s timeouts).

**Pages**: about 30 (flight board, project, deploy, snapshots frame, restore/copy/delete confirmations, add project, settings with its sections, updates, setup's three steps, sign-in), one stylesheet (66 KB, no build), 9 self-hosted woff2 fonts, 7 Stimulus controllers, live updates on the flight board (a refresh on change) and the deploy page (log appends, status and steps replaced), and polling on the update and deletion pages.

## Design
`mission-control-go/` in Houston's module, on gantry (latest), importing `internal/` for what the CLI already has (`mission`/`server` types, `project`, `kamal`, `docker`, `humanize`).

**Decisions** (yours, 2026-09-30)
1. **Data: its own schema, moved once.** The Go version has Go-shaped tables and gantry's encryption (`crypt.String`); at the switch, a one-time move reads the Rails database (Rails' encryption format decrypted with the server's `AR_ENCRYPTION_*` keys) and writes the new one. Everyone signs in again. Going back to Rails is restoring the snapshot taken before the switch. Each batch that adds tables adds their part of the move, tested on a database the Rails app made (as Esther's move from Phoenix).
2. **Side by side: parity checks and a read-only shadow.** Locally, both versions run on the same data (the Go one on the moved copy) and a parity check sends each the same API requests and compares the answers, and the pages' structure. On the server, before the switch, the Go version runs read-only on a moved copy of the data (jobs off) at a LAN address, to look at.

**Batches**, each a slice that's tested (tests first, a parity row per endpoint, mutation check) and merged before the next:
0. **Foundation**: gantry up to date; the Rails schema as sqlc's schema; Rails' encryption in Go, checked against values the Rails app encrypted; configuration from the same `.env`; `/up`, `/ping`, hosts (admin, hooks, project hostnames), the forwarded-headers rules.
1. **The runner API** (C1–C12) and webhooks, with the leases they need (deploy start, queue, claim, report, takeover). The Go runner and `houston deploy` pass against it unchanged.
2. **The personal API** (D1–D43), reads first, then writes, streams last (logs, snapshot zip). The CLI's tests pass against it.
3. **Jobs and their leases**: backups, restores, copies, deletions, change checks, schedules, server update settling, prune, registry clean-up, latest release, on gantry's jobs.
4. **Pages and live updates**: sign-in and sessions first, then the flight board, project, deploy, settings and the rest, the same stylesheet and controllers; visual checks at 1280 and 375 px against the Rails version.
5. **Setup and the switch**: the setup code and first-run steps, the installer running the Go image, and the switch itself (and its way back).

## Acceptance criteria → tests
Filled per batch when it starts (a row per endpoint or behaviour, each with its test and its parity check).

### Batch 0: foundation
| Criterion | Test |
| --- | --- |
| mission-control-go is on gantry's latest release, with what `gantry new` gives an app since v0.8.0 (the assets build step, the build cache) | the app's tests; `gantry test` |
| Rails' encryption is read in Go: values the Rails app encrypted (its dev keys, short and long values, which Rails compresses) decrypt to what went in; a wrong key or a changed byte is an error, not garbage | `railsdata.TestDecrypt` on vectors made by `bin/rails runner` |
| The move's frame: it opens a Rails-made database read-only and writes a new one through the app's migrations; the installation (base domain, time zone, Cloudflare ids, tokens re-encrypted with gantry's keys) moves | `move.TestInstallation` on a Rails-made fixture |
| Hosts: `hooks.<base>` answers only `GET /ping` and `POST /<name>` (else an empty 404); a project's hostname answers only its maintenance page (503) or an empty 404; everything else is Mission Control | `app.TestHosts` |
| Where a request comes from: the client's address from `Cf-Connecting-Ip` only when the peer is the tunnel (cloudflared), from `X-Forwarded-For` only when the peer is loopback (Thruster); https only through the tunnel (`Cf-Ray`); forwarding headers from anyone else ignored | `app.TestForwarded` |
| `GET /up` 200; `GET /ping` the installation's identity, plain text, on any host but a project's | `app.TestUpAndPing` |

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
| mission-control-go is on gantry's latest release, with what `gantry new` gives an app since v0.8.0 (the assets build step, the build cache) | the app's tests; `gantry test` (done, v0.10.0) |
| Rails' encryption is read in Go: values the Rails app encrypted (its dev keys, short and long values, which Rails compresses) decrypt to what went in; a wrong key or a changed byte is an error, not garbage | `move.TestDecrypt`, `TestDecryptRefuses` on vectors made by `bin/rails runner` (done) |
| The move's frame: it opens a Rails-made database read-only and writes a new one through the app's migrations; the installation (base domain, time zone, Cloudflare ids, tokens re-encrypted with gantry's keys) moves | `move.TestInstallation`, `TestMoveRefuses` on a Rails-made fixture (`testdata/rails_fixture.rb`) (done) |
| Hosts: `hooks.<base>` answers only `GET /ping` and `POST /<name>` (else an empty 404); everything else is Mission Control. (A project's hostname, its maintenance page or an empty 404, comes with the projects table in batch 1.) | `app.TestHosts` (done) |
| Where a request comes from: the client's address from `Cf-Connecting-Ip` only when the peer is the tunnel (cloudflared, by name: gantry v0.10.0's `Proxies.Names`); https only through the tunnel; forwarding headers from anyone else ignored, and a forwarded host from anyone (`Proxies.ForwardedHost` off). No Thruster in front of the Go version, so no loopback rule. | `app.TestForwarded` (done) |
| `GET /up` 200; `GET /ping` the installation's identity, plain text, on any host but a project's; the identity is the Rails app's for the same `SECRET_KEY_BASE` | `app.TestUpAndPing` (done) |

### Batch 1: the runner API and webhooks
In slices, each merged before the next; each slice adds its tables' part of the move (tested on `testdata/rails_fixture.rb`'s database) and its rows of the parity check.

| Slice | Criterion | Test |
| --- | --- | --- |
| 1a | The door (`api/base_controller.rb`): through the tunnel (`Cf-Ray`, or `Cf-Connecting-Ip` from cloudflared) an empty 404; the runner token as a bearer, constant-time, refused below 32 characters (401 JSON); 409 until Cloudflare is connected; a body over its limit 413, one that isn't JSON 400 | `api.TestDoor` (done) |
| 1b | Projects and sync: the payload validated as `ProjectSync#validate` does (a table of each rule), the project saved with its container names claimed (another project's name refused, nothing saved), required secrets missing is HOLD (422 with `missing`), reserved names, a restore in flight 409, a runner's sync must match its claimed deploy (token, project) | `api.TestSync*`, `models.TestParseSync*`, `TestCatchUp`; `move.TestProjects` (done; a project being deleted is refused once deletions move, batch 3) |
| 1c | Cloudflare and volumes for sync (volumes made where they were chosen to live, through Docker, checked and never moved): per-host DNS (a record Houston didn't make refused), custom domains' states, dropped domains removed, a never-served project waits for its first GO, maintenance routes pushed; against a fake Cloudflare API | `cloudflare.Test*`, `dns.Test*`, `volumes.TestPlace*`, `api.TestSyncPointsDNS`, `TestSyncRefusesAnotherRecord`, `TestSyncPushesMaintenanceRoutes`, `TestSyncPlacesVolumes`, `TestSyncRestoreCheckPointsAdopted` (done; the routes leave out projects being removed once deletions move, batch 3) |
| 1d | Secrets: one value as `text/plain`, only for a key compose.yml names; else 404 JSON | `api.TestSecret`; `move.TestProjects` (done) |
| 1e | Deploys: start (the next number, a live one 409 with its number, a silent one taken over, the registry cleaning 409); progress (token, still in flight, the report's rules, the log capped at 4 MiB in 256 KiB chunks, the flight board and deploy page told); a restore's switch moves the generation forward once and applies its compose.yml; the first GO points DNS | `api.TestStartDeploy*`, `TestReport*`; `move.TestProjects` (done; the deploy page's status and steps are replaced live with the pages, batch 4, and a copy's deploy settles its copy with copies, 1g) |
| 1f | The claim: a 25 s long poll, the oldest claimable queued deploy (none while the server updates or the registry cleans, a project with a live deploy or a deletion held back), silent ones finished first, the job's JSON (restore's previous generation, copy's hosts, known_hosts), the runner seen | `api.TestClaim*`, `knownhosts.TestFor`; `move.TestProjects` (done; held back while the server updates and for a project being deleted once those move, batch 3; `known_hosts` read in Go, hashed or plain, rather than through ssh-keygen) |
| 1g | What a deploy asks for while in flight: its snapshot (one per deploy, `skipped` when nothing is deployed), a restore's data, a copy's data and handover: rows and requests made here, their work run by batch 3's jobs | `api.TestSnapshots*`, `TestRestoreData`; `move.TestBackupRuns` (done for snapshots and a restore's data, whose runs are queued with the backup job's in one transaction; the job's work is batch 3. A copy's data and handover go with copies, batch 3: they run restic, Docker and kamal-proxy) |
| 1h | Webhooks: verified by HMAC (GitHub, Gitea, Forgejo) or token (GitLab, Houston), constant-time; unverified and unknown alike an empty 404, counted per address and name (30 a minute, checked before the body is read); verified ones 60 a minute per project; 202 and a change check queued | `api.TestWebhook*` (done; a webhook to a deleted project's copy rings the copy once copies move, batch 3, and the change check's work is batch 3's) |
| 1i | Parity: a script runs both versions on the same moved data and sends each the same runner API requests, comparing status and body (tokens and times by shape, null as "") | `mission-control-go/bin/parity` (done: 29 steps, none differ; 2 known until batch 3, the Rails app's backup job having run) |

**Found along the way, for later batches**
- Batch 5: the production image can't be `FROM scratch`: Mission Control runs the docker CLI (volumes, restic, logs), git over ssh, and its own image's `mkdir` for volume directories (`HOUSTON_TOOLS_IMAGE`). The tunnel's Mission Control address (`HOUSTON_MISSION_CONTROL_URL`, default `http://mission-control:8080`) must match the container the installer runs.
- Batch 5: the switch carries the Rails app's `storage/known_hosts` to the Go app's `HOUSTON_KNOWN_HOSTS` (default `DATA_DIR/known_hosts`).
- The parity check compares answers as the Go clients decode them: the Go tables keep `''` where Rails kept NULL, so a field like a project's branch can be `""` where Rails answered `null`.

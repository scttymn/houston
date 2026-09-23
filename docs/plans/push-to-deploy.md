# Plan: Push to deploy (build step 4)

Spec v11: https://claude.ai/artifact/R3d4fzN1u5QUP4kT88mqug (§7 linking a project, webhook handling, the deploy job; §8 custom domains; §10 status, pages, API, security notes; §12 build step 4; §14 webhook headers and `git ls-remote` with a deploy key from Mission Control's container).
Design: https://claude.ai/artifact/UswdZ62N4ygiKdGh1vDEPH (AddProject, DeployLog, Main boards). The design wins over the spec where they differ, except where the spec's follow-ups (§13) supersede it: `compose.yml` replaces "houston.yml", the runner is `houston-runner-1`, and "Run tests" is step 00.

## Goal

Link a repo in Mission Control, and every push the deploy rule matches is deployed by Houston's runners: the webhook rings, Mission Control reads the refs itself, and a runner tests and deploys the new commit, with the log live on the deploy page. Custom domains get their DNS.

## Scope

- **Add project:** repo URL, a generated deploy key, an access check, the branch and compose path, a preview of what Houston found (read with the CLI's own parser), and Save.
- **Webhooks:** `POST hooks.<base>/<name>` verified by an HMAC or token header, then "check for changes" (`git ls-remote` against the deploy rule), queueing a deploy of each moved ref. One queued deploy per project, switched to the newest SHA.
- **Runners:** `houston runner` in the `houston-runner-1…n` containers claims a job, fetches the commit, runs `houston test` (step 00) and `houston deploy`.
- **Live deploy page** (Turbo Streams over Solid Cable) and **custom-domain DNS** (§8 states).
- **Defer:**
  - **CLI personal API tokens and `--server` commands** (`secrets`, `status`, `logs`, `console`): build step 4b, right after this one (your decision).
  - **Volume placement and backups:** build step 5.
- **Boundary:** the local API's runner token and the "never through the tunnel" rule (build step 3) stay as they are. Webhooks are the only thing served on `hooks.<base>`.

## Batches

1. **Link a repo** (below): `houston inspect`, Mission Control's git access with a deploy key, reading `compose.yml`, the preview, and Save.
2. **Webhooks and check for changes:**
   - `hooks.<base>` locked down to `POST /<name>` (and the reachability `GET /ping`)
   - signature verification for GitHub, Gitea/Forgejo, GitLab and Houston headers; rate limiting
   - the webhook secret shown until the first verified delivery, then rotatable
   - `git ls-remote` against the deploy rule; queued deploys coalescing to the newest SHA
   - the Check for changes button, and the `hooks.<base>` probe on the flight board
3. **Claiming queued deploys** (Mission Control): the claim API (long poll), one runner per deploy, stale takeover at claim, the runner registry and RUNNERS n/m, and "Test" as step 00.
4. **`houston runner`** (Go): claim → fetch with the deploy key (Mission Control's recorded host keys only) → step 00 `houston test` → `houston deploy` on the claimed deploy; backoff when Mission Control is away; `houston deploy` refusing to run as anyone but the houston user.
5. **Runner containers:** the image, two runners in the installer (same-path workspace mounts, the host's CLI, houston's uid, the socket), sweeping stale test projects; proven on a VM (Check for changes → a runner deploys from Forgejo).
6. **Live deploy page:** log and steps streamed.
7. **Custom-domain DNS:** zones found by suffix, proxied CNAMEs with `managed-by:houston project:<name>`, the states WILDCARD / DNS OK / DNS PENDING / ZONE NOT IN CLOUDFLARE YET, and owned records no project uses removed.
8. **Real run on svnmns.com:** a push to Forgejo → a webhook through Cloudflare → a runner deploys it; a custom domain in a zone that isn't in Cloudflare shows its state.

## Batch 1: Link a repo

### Design (short)
- **`houston inspect [--json]`** (new CLI command) loads the compose file with the same loader as every other command. On success it prints JSON:
  - `sync`: exactly the request `houston deploy` sends. `deploy.syncRequest` moves to `mission.RequestFor(p)`, used by both.
  - `preview`: services with their images, the app's port, health, CPU/memory limits, `commands.test` present or not, and the backup schedule and named volumes.
  - With problems: exit 2, and the loader's problem lines on stderr, as `houston dev` prints them.
- **Mission Control reads the file with that binary:**
  - The installer mounts the host's `/usr/local/bin/houston` read-only into the mission-control container, which is the same architecture.
  - The production image gains `git` and `openssh-client`.
  - One parser for compose everywhere, and Mission Control never interprets `compose.yml` itself.
- **`RepoLink`** (a draft; Save turns it into the project's link):
  - `repo_url`, `branch` (default `main`), `compose_path` (default `compose.yml`), and an ed25519 key pair from `ssh-keygen`: the private key encrypted, the public key shown.
  - Drafts older than a day are deleted when a new one is made.
- **`GitRemote`** (a runner seam like `DockerCommand`) runs git with:
  - `GIT_SSH_COMMAND="ssh -i <0600 temp key> -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=<storage>/known_hosts"`
  - `GIT_TERMINAL_PROMPT=0`, `GIT_ALLOW_PROTOCOL=ssh:https`, a 60 s timeout, and `--` before the URL
  - **Trust on first use:** the first contact records the host key in `storage/known_hosts` (on the persistent volume). A changed key fails, saying so.
  - **Check access:** `git ls-remote --heads -- <url>`, then the branch must exist.
  - **Read:** `git clone --depth 1 --single-branch --branch <b> --no-tags --filter=blob:none --no-checkout -- <url> <tmp>`, then `git -C <tmp> checkout HEAD -- <compose_path>`, then `houston -f <tmp>/<path> inspect --json`. The temp dir and key are removed in `ensure`.
- **Inputs are refused before git sees them:**
  - URLs other than `ssh://`, `https://`, or scp-style `user@host:path`, including `file://`, `ext::`, local paths, and anything starting with `-`
  - branch names git's `check-ref-format --branch` rejects
  - compose paths that are absolute, contain `..`, or aren't `.yml`/`.yaml`
- **Pages** (the design's AddProject board, sections 01 and 02; secrets are set on the project page after Save, as built in step 3):
  - `GET /projects/new`: the repo URL, and a generated key once a URL is entered.
  - Check access → GO "Houston can read the repo", or NO-GO with git's first error line (the key's path scrubbed).
  - Branch and compose path, then Read → "What Houston found `<branch> @ <sha7>`": name → `<name>.<base>`, domains, app, services, deploys (+ "tests run first" when `commands.test` is set), backups (schedule and volumes as the file says).
  - Problems are listed as the CLI prints them, and Save stays disabled.
  - **Save:**
    - `ProjectSync` validates and claims the container names (step 3's code, so name clashes and the unique index apply). Then it stores the link (repo, branch, compose path, deploy key) and a webhook secret (32 random bytes, encrypted) for Batch 2.
    - A project `houston deploy` already registered, with no link yet, is linked. One that's already linked → "already linked to <repo>".
    - The Deploy button and the webhook section (§7 step 5) arrive with Batches 2–3; the page doesn't show them before they work.
- **Authz:** every page requires the signed-in admin (the default).
- **CLI counterpart (lands in build step 4b):** linking is one model (`RepoLink` → `ProjectSync.save!` + link), so the page is a thin layer over it. Step 4b's token-authenticated API and `houston link --server <repo-url> [--branch] [--file]` call the same model.

### Contract pin
- **`houston inspect --json`:** exit 0 with JSON `{sync: SyncRequest, preview: {…}}`; exit 2 with problems on stderr; exit 2 when the file is missing.
- **`POST /projects/new/access`** `{repo_url}` → the page with GO / NO-GO. Invalid URL → 422 with the field error, and git isn't run.
- **`POST /projects/new/read`** `{branch, compose_path}` → the preview, or the problems. Invalid → 422; git and houston aren't run.
- **`POST /projects`** (Save) → redirect to the project page. Clash → 422 naming the owner. Already linked → 422. No read preview in the session's draft → 422 "read the file first".

### Crash-gap template (Save)
```text
Durable step 1 (primary): the project + its hosts + its link, one transaction (ProjectSync.save! extended)
Dies before: nothing follows in Batch 1 (DNS happens at the first deploy's sync, as in step 3)
Retry: Save again → the project exists and is linked to this repo → idempotent success (not "already linked")
```

### AC ↔ test map (Batch 1)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | `houston inspect --json`: `sync` equals `mission.RequestFor(p)`, and `preview` has services/images, port, health, limits, test, backups; problems → exit 2 with the loader's lines; missing file → exit 2 | `internal/cli/inspect_test.go` `TestInspect` (Phoenix fixture golden + a broken file) | Contract, Parity |
| 2 | `houston deploy` sends `mission.RequestFor(p)` (the moved function), unchanged | `internal/deploy` `TestDeployHappyPath` (existing, still green) + `internal/mission` `TestRequestFor` | Parity |
| 3 | A new draft has an ed25519 key: the public key starts `ssh-ed25519 `, the private key is encrypted at rest (raw column ≠ PEM), and drafts older than a day are deleted | `models/repo_link_test.rb` | Contract |
| 4 | Hostile inputs refused before git runs (table): `file:///etc`, `ext::sh -c id`, `/srv/repo`, `-oProxyCommand=id`, `https://x/y.git --upload-pack=id`; branch `-x` / `a..b`; compose path `/etc/passwd`, `../x.yml`, `compose.txt` → 422 with the field; FakeGit saw no calls | `controllers/project_links_test.rb` `test "hostile repo input never reaches git"` | Contract (hostile input) |
| 5 | Check access: FakeGit is called with `ls-remote --heads -- <url>` and the env (IdentitiesOnly, BatchMode, accept-new, the known_hosts path, `GIT_ALLOW_PROTOCOL`); exit 0 with the branch → GO; the branch missing → NO-GO naming it; exit 128 → NO-GO with git's first line; the temp key file is gone afterwards | `test "checking access to the repo"` | Contract, Signals |
| 6 | Read: clone + checkout of just the compose path + `houston inspect`; the preview shows every §7 fact from the fixture JSON; problems are shown and Save is disabled; the temp dir is removed even when inspect fails | `test "reading compose.yml"` | Contract, Crash & repair |
| 7 | Save: creates the project (facts from `sync`) and its link and webhook secret (encrypted); a container-name clash → 422 naming the owner, nothing saved; a project registered by `houston deploy` → linked; linked to another repo → 422; Save twice → idempotent | `test "saving links the project"` | Contract, Re-entry, Concurrency (reuses step 3's index) |
| 8 | Signed out: every link page and action → sign-in; git never runs | `test "linking needs the admin"` | Authz |
| 9 | Save without a read preview → 422 "read the file first" | `test "save needs a read preview"` | Preconditions |
| 10 | **Real (VM):** in Mission Control's container, `ls-remote` of a public HTTPS repo works; over SSH to a Forgejo container in the VM with the generated deploy key: the first contact records the host key; a replaced host key → NO-GO "host key changed"; the whole flow on a real repo reads its `compose.yml` | `install/test/link-repo.sh` | Parity (§14) |

### Deploy notes
- The installer adds the read-only `houston` mount to mission-control, and the image rebuild adds `git` / `openssh-client`. A rerun on an existing server picks up both.
- New tables: `repo_links`, plus link columns on `projects` (`repo_url`, `branch`, `compose_path`, `deploy_key_private` (encrypted), `deploy_key_public`, `webhook_secret` (encrypted), `webhook_verified_at`). The migration runs down and up.

### Agent loop checkpoints
- Red: all Batch 1 rows fail for the missing code. Report the RED map, then continue.
- Batch 1 green → scotty-review with the cold pass → Batch 2, expanded from this file.
- Ready: scotty-review over the whole branch, the full Go and Rails suites, lint on touched files, and the real run.

### Done (Batch 1)
- **Red:** rows 1–9 failed for the missing code (the compile failures in Go; 9 errors in Mission Control). **Green:** the Go suite and Mission Control (102 runs), `go vet`, and rubocop on the 13 touched Ruby files.
- **Found on the way:**
  - `repo_link.rb` needed `require "open3"`.
  - "Read the file first" only rendered when a draft existed. It now shows without one.
  - My `make_project` test helper created projects that owned no container names, so a clash test passed a save it should have refused. Real projects always claim their names (sync, linking), and the helper now does too.
- **Mutations, each caught:** any repo URL accepted; a compose path climbing out with `..`; no `--` before the URL; a linked project re-linked to another repo; `StrictHostKeyChecking=no`.
- **Real (row 10), `install/test/link-repo.sh`: LINK PASS, 11 checks.** Ubuntu 24.04, Mission Control's production container, Forgejo 13 on its network, the Add project pages driven over HTTP as the admin:
  - Before the key is added → NO-GO, "Permission denied (publickey)"; the page shows the generated key.
  - Once added as a read-only deploy key → GO, and Forgejo's host key is recorded on first use (`ssh-keygen -F` finds it in `storage/known_hosts`; Debian hashes the entries).
  - Read → `houston inspect` (the host's CLI mounted read-only) shows "spike → spike.houston.test, db · postgres:17"; Save links the project, with hosts `spike`, `spike-db`.
  - A public repo over HTTPS (`github.com/basecamp/kamal`) → GO.
  - Forgejo recreated with new host keys → NO-GO, "host key changed".
  - The first run failed every POST with a CSRF 422, for a script reason. Rails' tokens are per form, and my helper took the header's sign-out form token. The script now takes the token from the form it posts to.

## Batch 2: Webhooks and check for changes

### Design (short)
- **`hooks.<base>` serves only two routes:** `POST /<name>` (the webhook) and `GET /ping` (the reachability probe; Mission Control's identity, as for `admin.`). Everything else on that host is a 404, before any other route.
  - This closes a gap: the tunnel lets any `^/[a-z0-9-]+$` path through to Mission Control, so `hooks.<base>/session` reached the sign-in page.
  - Routes on the admin host and the LAN address are unchanged.
- **`WebhooksController < ActionController::API`** (no cookies or CSRF):
  - Reads the raw body up to 5 MiB (more → 413).
  - Rate limited to 30 per minute per project name (429).
  - Accepts any one of:
    - `X-Hub-Signature-256: sha256=<hex>` (GitHub, and also Gitea/Forgejo)
    - `X-Gitea-Signature` / `X-Forgejo-Signature: <hex>`, the HMAC-SHA256 of the body with the project's webhook secret
    - `X-Gitlab-Token` / `X-Houston-Token`, equal to the secret
  - All comparisons are constant-time.
  - An unknown project, an unlinked one, or a bad or missing signature all get the same empty 404, so the endpoint never says which.
  - Verified → 202 (empty). `webhook_verified_at` is set on the first one, and `CheckForChangesJob` is enqueued. **The payload is never read**: it's a doorbell.
- **`ChangeCheck`** (the job, and the Check for changes button):
  - `GitRemote.refs(project)` runs `git ls-remote --heads --tags` with the project's deploy key and the same guards as Batch 1.
  - The refs the deploy rule matches (`commit` → `refs/heads/<branch>`; `tag` → `refs/tags/*` matching the glob, `^{}` peeled refs used for tags) are compared with `projects.seen_refs`.
  - Each moved or new ref queues a deploy of its SHA. `seen_refs` is updated in the same transaction.
  - A failed ls-remote queues nothing and records `last_check_error`, shown on the project page.
- **The queue:** `Deploy` gains a `queued` status and a partial unique index (one queued per project).
  - Queueing while one is queued **switches that one to the newest SHA and ref**, with a log line saying so. It's still one record, and it keeps its number.
  - Queueing while one is in flight creates a queued one.
  - Runners claim queued deploys in Batch 3. Until then, the page shows QUEUED.
  - `houston deploy` by hand is unchanged: it ignores the queue, and one in flight per project still holds.
- **Project page → Connect pushes** (the design's section 04):
  - the webhook URL `https://hooks.<base>/<name>`
  - the secret shown in full until the first verified delivery, then masked with **Rotate secret** (a new secret, shown again until its first delivery)
  - "Send push events as application/json"
  - a Check for changes button
  - the `hooks.<base>` route state
- **`SystemStatus`** generalizes the admin route probe to `route(host)`; the empty board's pre-flight gets a "Route to hooks.<base>" row.

### Concurrency template (queueing)
```text
Writer A: ChangeCheck from a webhook      Writer B: ChangeCheck from the button (or a second webhook)
Ordering / lock: IMMEDIATE transaction (Rails 8.1 SQLite) + partial unique index on deploys(project_id) WHERE status='queued'
Bad interleaving: both see no queued deploy, both insert
Expected end state: one queued deploy with the newest SHA; seen_refs updated once per moved ref
Re-check under the lock: find-or-update the queued row and write seen_refs in the same transaction
Work under the lock: only deploys + projects rows; git ls-remote runs before the transaction
```

### AC ↔ test map (Batch 2)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | On `hooks.<base>`: `GET /session/new`, `GET /`, `POST /link`, `GET /projects/x`, `GET /up` → 404; `GET /ping` → the identity; the admin host is unchanged | `integration/hooks_host_test.rb` | Authz |
| 2 | Signatures (table): GitHub `sha256=`, Forgejo, Gitea, GitLab token, Houston token → 202, empty, job enqueued. Wrong HMAC, HMAC of another body, a missing header, an unknown project, an unlinked project → 404, empty, no job | `controllers/webhooks_controller_test.rb` `test "only a signed push rings the doorbell"` | Authz, Contract |
| 3 | The first verified delivery sets `webhook_verified_at`; later ones don't move it | `test "the first delivery is remembered"` | Signals |
| 4 | Body > 5 MiB → 413, no job; the 31st in a minute → 429 | `test "webhooks are bounded"` | Scale |
| 5 | ChangeCheck, commit rule: the branch moved → one queued deploy (SHA, ref); no move → nothing; other branches ignored; `seen_refs` updated | `models/change_check_test.rb` `test "a moved branch queues its commit"` | Contract |
| 6 | Tag rule: a new tag matching the glob → queued (peeled SHA); non-matching tags ignored | `test "a new tag queues its commit"` | Contract |
| 7 | Coalescing: a second move while queued → the same record switched to the newest SHA (count 1, log line); while in flight → a new queued one | `test "one queued deploy, always the newest"` | At-least-once |
| 8 | The partial unique index allows one queued deploy per project | `models/deploy_test.rb` `test "one queued deploy per project"` | Concurrency |
| 9 | ls-remote fails → nothing queued; `last_check_error` recorded and shown | `test "a failed check queues nothing"` | Signals |
| 10 | Project page: URL, secret visible until verified then masked; Rotate → new secret visible and `verified_at` cleared; Check for changes runs the check | `controllers/project_pages_test.rb` `test "connect pushes"` | Contract |
| 11 | `SystemStatus.route("hooks…")`: GO / HOLD / NO-GO as the admin route; the pre-flight row | `models/system_status_admin_route_test.rb` (generalized) | Signals |

### Done (Batch 2)
- **Red:** all 11 rows failed (4 failures, 7 errors). **Green:** 114 runs. Rubocop is clean on the 22 touched files, and the migration runs down and up.
- **Found on the way:**
  - The rate limiter's `by:` read `params`, which made Rails parse the body as JSON, so an oversized or malformed body errored before the doorbell ran. The controller reads the name from the path only, with `wrap_parameters false`. The payload is never parsed.
  - The oversized request counts toward the rate limit, which is right. The test clears the limiter between its two parts.
- **Mutations, each caught:** the hooks host serving other routes; any signature accepted; `seen_refs` never saved; no coalescing (the unique index raises); annotated tags deploying the tag object instead of the commit; `webhook_verified_at` moving on every push.
- **Visual check:** a linked project's page, with Connect pushes (the URL, the secret until the first push, Check for changes, Rotate secret) and a QUEUED deploy, at 1440 px. A queued deploy's duration shows "—".
- **Queued deploys wait for Batch 3's runners.** Until then, the page shows them as QUEUED.

## Batch 3: Claiming queued deploys

### Design (short)
- **`POST /api/runner/jobs/claim`** `{runner, wait}` (runner token, never through the tunnel, as in step 3's API).
  - `runner` must match `^houston-runner-\d+$`. `wait` is seconds, 0–25, default 25: a long poll that checks every second.
  - In one IMMEDIATE transaction:
    - Finish any stale in-flight deploy (no heartbeat for 2 minutes) as abandoned, as `Deploy.start!` does.
    - Pick the oldest queued deploy whose project has no in-flight deploy.
    - Flip it to `in_flight` with a fresh token (digest stored), `heartbeat_at` now, and `runner` set, using `update_all … WHERE status = 'queued'`. It's claimed only if that updated one row.
  - **Response 200:**
    - `deploy {id, number, token, sha, ref, took_over}`
    - `project {name, repo_url, branch, compose_path, deploy_key}`
    - `known_hosts`: the lines Mission Control recorded for the repo's host (`ssh-keygen -F`), empty for HTTPS
  - Nothing to claim within `wait` → 204.
- **The runner registry:** `runners (name unique, last_seen_at)`, touched on every claim call.
  - The header strip shows **RUNNERS n/m**: n seen in the last 60 s, m = `HOUSTON_RUNNERS` (the installer sets it; unset → the item isn't shown).
- **`Deploy::STEPS`** gains **Test** first ("Run tests" as step 00, spec §13). `houston runner` reports it; a hand `houston deploy` skips it.
- **The deploy key over the local API:** the runner token already reads secrets there. The key goes only to a claim, which proves the runner token, and never through the tunnel.

### At-least-once / ownership template (claim)
```text
Enqueue site: Deploy.queue! (ChangeCheck)
Exclusive claim: UPDATE deploys SET status='in_flight', token_digest=… WHERE id=? AND status='queued' (1 row = ours), inside BEGIN IMMEDIATE
Enter preconditions: status queued, and the project has no fresh in_flight deploy (stale ones are finished first, same transaction)
Duplicate delivery: two runners claim at once → each deploy is handed out once (row count check)
Process kill mid-work: heartbeat stops → the next claim finishes it as abandoned after 2 min and hands the queued one out with took_over (Kamal's lock released by the runner)
Ownership at finalize: unchanged from step 3 (token + in_flight checked in the writing transaction)
Stuck-alive: houston deploy's deadline; the runner's own deadline for step 00 (Batch 4)
```

### AC ↔ test map (Batch 3)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | A claim hands out the oldest queued deploy: 200 with deploy (token ≥ 43 chars, digest stored), project (repo, branch, compose path, deploy key) and known_hosts; the deploy is in flight with `runner` and `heartbeat_at` | `integration/api_claims_test.rb` `test "a runner claims the oldest queued deploy"` | Contract |
| 2 | Nothing queued → 204 after `wait` (0 in tests); a project with a fresh in-flight deploy keeps its queued one back while another project's is handed out | `test "nothing to claim"` | Preconditions |
| 3 | A stale in-flight deploy is finished as abandoned, and its project's queued deploy is handed out with `took_over` | `test "a silent runner's deploy is taken over at claim"` | Crash & repair |
| 4 | The flip is conditional: `Deploy.claim!` on a deploy that stopped being queued (claimed by another runner between the pick and the update) returns nil and changes nothing | `models/deploy_test.rb` `test "a deploy is claimed once"` | Concurrency (TOCTOU) |
| 5 | A bad runner name, `wait` > 25 or negative → 422; no token → 401; `Cf-Ray` → 404; nothing claimed | `test "claims are guarded"` | Authz, Contract |
| 6 | Runners are recorded; the strip shows RUNNERS 1/2 with one seen in the last minute and `HOUSTON_RUNNERS=2`; no item when unset | `controllers/projects_controller_test.rb` `test "the header counts runners"` | Signals |
| 7 | known_hosts: an SSH repo's host lines from `storage/known_hosts`; HTTPS → empty | `models/git_remote_test.rb` `test "the recorded host keys for a repo"` | Contract |
| 8 | Steps: Test is step 00; a claimed deploy at Test shows Test current and the rest pending | `controllers/deploy_pages_test.rb` (extended) | Contract |

### Done (Batch 3)
- **Red:** all 8 rows failed (9 tests). **Green:** 122 runs. The migration runs down and up, and rubocop is clean on the touched files.
- **Mutations, each caught:** the flip without its `status = 'queued'` condition (row 4); a busy project's queued deploy handed out (row 2); silent deploys never taken over at claim (row 3); every recorded host key handed to a runner (row 7).
- **A choice made on the way:** a deploy run by hand has no runner and never runs step 00, so its page shows Test as **SKIPPED**, not DONE.
- **Deploy note for Batch 5:** a claim long-polls for up to 25 s on a Puma thread. Rails' default is 3 threads (`RAILS_MAX_THREADS`), so two idle runners would hold two of them, and the UI and webhooks would share the last. The installer must raise `RAILS_MAX_THREADS`; the SQLite pool follows it (`database.yml`).

### Decisions (from you)
1. **The real run's git host:** a Forgejo container in the test VM ("keeps things easily testable"). Its webhook still goes out through Cloudflare to `hooks.svnmns.com`.
2. **CLI API tokens and `--server` commands become build step 4b**, right after this: "It makes this always work from the cli (agentic interactions)." So every Mission Control action should have a CLI path.

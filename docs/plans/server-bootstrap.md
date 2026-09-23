# Plan: Server bootstrap (build step 2)

Spec v10: https://claude.ai/artifact/R3d4fzN1u5QUP4kT88mqug (§8 Cloudflare, §9 storage, §10 Mission Control, §11 server install).
Design: https://claude.ai/artifact/UswdZ62N4ygiKdGh1vDEPH (boards SetupAdmin, SetupCloudflare, SetupStorage, ProjectsEmpty, and the shared header). The design wins over the spec where they differ.

## Goal

A fresh Ubuntu/Debian server goes from `curl … | sh` to a signed-in admin at `admin.<base>`, with a Cloudflare tunnel, a `*.<base>` wildcard, and a default backup location. That is spec §11, first run steps 1–3, and the empty Projects landing.

## Scope

- **Mission Control** is a Rails 8.1 app (Ruby 3.4, SQLite, Solid Queue, Propshaft, importmap, Turbo) in `mission_control/`.
- **It's developed with Houston itself.** `houston init` sets it up, then `houston -f mission_control/compose.yml dev` runs it and `… test` runs its suite. No Ruby on the Mac; `rails new` runs once inside `ruby:3.4`.
- **Fonts are self-hosted** (IBM Plex Sans/Mono and Barlow Condensed, SIL OFL) under `app/assets/fonts`, not Google Fonts. Mission Control is an admin tool on your own server, and it shouldn't tell Google every time you open it.
- **Boundary:** projects, deploys, secrets UI and Settings are later build steps. Batch 5's empty landing only shows what setup created.

## Batches

1. **Mission Control skeleton + first-run step 1** (admin login with a setup code). Fully specified below.
2. **First-run step 2: Cloudflare** (token check, remotely managed tunnel, ingress for `admin`/`hooks`/catch-all, `*.<base>` wildcard with `managed-by:houston`, cloudflared started).
3. **First-run step 3: default storage** (nfs/local/s3/b2/sftp, test write, `restic init`, password shown once, must be acknowledged).
4. **Installer** (`install.sh` for Ubuntu LTS / Debian stable: Docker, `houston` user and local SSH key, the server compose file with Mission Control, cloudflared, `localhost:5000` registry and runner placeholders, LAN URL + setup code; a rerun repairs).
5. **Empty Projects landing** (the "setup complete, bookmark `admin.<base>`" banner, pre-flight check, header system strip for tunnel and registry).

Later batches are titles only until Batch 1 is green.

## Batch 1: Mission Control skeleton + admin setup

### Design (short)

- **Generate** with `rails new mission_control --database=sqlite3 --skip-kamal --skip-action-mailbox --skip-action-text --skip-jbuilder --skip-ci --skip-devcontainer`. Kamal is skipped because Mission Control is updated by the installer, never by Kamal (§11). Then `bin/rails generate authentication`, then `houston init` on it.
- **Single admin.** The generator's `User` (email + password) and `Session`. Password reset is removed: there's no mail, and the password is changed in Settings (a later step).
- **Setup code, owned by Mission Control:**
  - `bin/rails houston:setup_code` prints a fresh code like `K7QM-2XHD` (Crockford-style alphabet, no 0/O/1/I) and stores **only its bcrypt digest**, replacing any previous code.
  - Once an admin exists, it prints "setup is complete" and exits 1 without creating a code.
  - The installer (Batch 4) runs this task inside the container and prints the result next to the LAN URL, so "re-run the installer for a new code" (design) works by construction.
- **Setup gate:** while no admin exists, every page except `/setup` and assets redirects to `/setup`. Once an admin exists, `/setup` redirects to sign-in, or to `/` when signed in.
- **Claiming the setup, exactly once:**
  1. Validate the form (email format; password ≥ 12 characters and matches its confirmation; code matches the digest).
  2. In one transaction, delete the setup-code row by id and require exactly 1 row deleted, then create the user and the session.
  3. If a concurrent request already consumed the code, 0 rows are deleted, so reject. If the user save fails, the transaction rolls back and the code stays valid.
  SQLite serializes writes, so two valid submissions can't both win.
- **Rate limit:** `rate_limit to: 10, within: 3.minutes` on `POST /setup` (Rails 8 built-in), like the generator's sign-in.
- **Pages follow the design's SetupAdmin board:**
  - the "STEP 01 OF 03" header
  - the fields: setup code, email, password + confirm
  - the "at least 12 characters" hint
  - the "Why a setup code" aside with the installer output
  After step 1, it redirects to `/`. The stub `/` page says "Setup continues with Cloudflare" until Batch 2 adds that step.
- The shared layout (dark header, logo mark, Projects/Settings nav) is built now with the design's tokens (paper `#F3EFE4`, ink `#1A1D24`, signal red `#C8321F`). The system strip waits for Batch 5.

### Contract pin

| Surface | Allowed state | Result |
|---|---|---|
| any page (not `/setup`, not assets) | no admin | redirect to `/setup` |
| `GET /setup` | no admin | form |
| `GET/POST /setup` | admin exists | redirect (sign-in, or `/` when signed in); no user created |
| `POST /setup` | no admin, code valid, fields valid, code row still there | admin + session created, code row gone, redirect `/` |
| `POST /setup` | wrong code / invalid fields | 422, the form with the error, **no user, code still valid** |
| `POST /setup` | code row already consumed (lost the race) | 422 "setup was already completed", no second user |
| `POST /setup` | 11th attempt in 3 min | 429 |
| `houston:setup_code` | no admin | prints the new code, stores its digest only, invalidates the old one |
| `houston:setup_code` | admin exists | "setup is complete", exit 1, nothing stored |
| `/passwords/*` | — | 404 (reset removed) |
| any page | admin exists, not signed in | redirect to sign-in |

### Concurrency template

```text
Writer A: POST /setup with the valid code
Writer B: POST /setup with the valid code (second browser or a replayed request)
Ordering / lock: DELETE the setup_code row by id; exactly one request deletes 1 row (SQLite serializes writes)
Bad interleaving: both read "no admin" and a valid code, both create a user → two admins
Expected end state: exactly 1 user; the loser gets 422 and nothing is created
Re-check under the write: the delete count is the check, made inside the same transaction as the user insert
Work under the lock: only the delete and the inserts (bcrypt runs before the transaction)
```

### AC ↔ test map (Batch 1), `mission_control/test/…`, run with `houston -f mission_control/compose.yml test`

| # | AC | Test | Lens |
|---|---|---|---|
| 1 | With no admin, `/` and `/session/new` redirect to `/setup`; assets and `/up` are served; unknown paths stay 404 (changed at execute: routing runs before the gate, and a catch-all redirect would hide real 404s later) | `integration/setup_gate_test.rb` `test "every page leads to setup until an admin exists"` | Preconditions |
| 2 | `GET /setup` shows the step-1 form (code, email, password, confirmation) | `controllers/setup_controller_test.rb` `test "shows the admin form"` | Contract |
| 3 | A valid `POST` creates the one admin, signs in, consumes the code, redirects to `/` | `test "creates the admin and signs in"` | Contract |
| 4 | Wrong code → 422, no user, the same code still works afterwards | `test "a wrong code creates nothing and keeps the code valid"` | Authz |
| 5 | Invalid email / password under 12 / mismatch / blanks → 422 with the message, code not consumed | `test "invalid details create nothing"` | Contract |
| 6 | Once an admin exists, `GET`/`POST /setup` redirect and create no user, even with a code minted by stubbing | `test "setup is closed once an admin exists"` | Preconditions |
| 7 | **Race:** the code row is deleted between rendering the form and submitting (another request won) → 422, still exactly 1 user | `test "losing the race to another setup creates nothing"` | Concurrency |
| 8 | The 11th `POST /setup` within 3 minutes → 429 | `test "setup attempts are rate limited"` | Scale |
| 9 | `houston:setup_code` with no admin: prints a code matching `\A[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}\z`, stores a digest and not the code, and the previous code stops working. With an admin: "setup is complete", exit 1, no row | `tasks/setup_code_test.rb` (2 tests) | Contract, Re-entry |
| 10 | After setup, signed out: `/` → sign-in; a wrong password is rejected; the right one signs in; sign-out works | `integration/sign_in_test.rb` | Authz |
| 11 | `/passwords/new` → 404 | `integration/sign_in_test.rb` `test "password reset is gone"` | Honest surface |
| 12 | **Real run:** `houston init` on the generated app; `houston -f mission_control/compose.yml dev`; get a code with `houston … console` running the task; complete setup in the in-app browser; the design's step-1 page shows with self-hosted fonts (no request to fonts.googleapis.com); `houston … test` runs the suite green | manual check (screenshots + command output) | Parity |

### Lens run (Batch 1)
| Lens | Where |
|---|---|
| Contract | 2, 3, 5, 9 |
| Authz | 4, 10: the code is the only way in before an admin exists; afterwards, the session is |
| Preconditions | 1, 6: the setup gate and closed setup, as a literal allow-list |
| Concurrency | 7 + template: the delete count is the lock |
| Scale / abuse | 8: rate limit on guessing the code |
| Honest surface | 11: no password reset without mail |
| Crash & repair | the transaction rolls back and the code survives a failed save (4, 5); the installer can always mint a new code (9) |
| At-least-once, Migrate | N/A in Batch 1 (the first migrations are new tables) |

### Done (Batch 1)
- Generated in `ruby:3.4` (Rails 8.1.3.1), set up with `houston init` (name `mission-control`, `port: 80` from `EXPOSE 80`), and developed only through `houston dev`/`test`/`console`.
- **Found while generating:**
  - `rails new --skip-git` writes no `.gitignore`, so Rails' standard ignores were added (`/config/*.key`, logs, tmp, storage).
  - `bundle install` picked **json 3.0.2**, which breaks ActiveSupport 8.1.3.1's signed cookies (`JSON.parse` changed its arguments). Pinned `json ~> 2.21`, the version equip runs.
  - The first `houston test` refused with pending migrations until one `houston dev` wrote `db/schema.rb`. That's normal Rails behaviour.
- **Red → green:** 14 red, then 20 green, then 21 with a guard. Row 1 changed at execute: unknown paths stay 404 instead of redirecting. A mutation that skipped the race check was caught by row 7 alone.
- **Review:** `resource :session` exposed unused edit/show/update routes. It's now limited to new/create/destroy. The guard test didn't go red first, because Rails already 404s routes with no action.
- **Real run (row 12):** `houston dev`, then `houston console` running `bin/rails houston:setup_code` printed `NYWJ-J9U8`. The step-1 page rendered as designed in the in-app browser, with **no font or Google requests**. The form was submitted with curl (I don't type credentials into browsers): a wrong code got 422 with "Setup code doesn't match…", and the real code (lower case, no dash) got 302 to `/` and the home page. `/setup` is closed afterwards, and the task says "setup is complete" and exits 1.
- **Fonts:** not added yet. They need downloading (Fontsource packages, SIL OFL), which waits for your OK. Until then there's a system-font fallback.

## Batch 2: first-run step 2, Cloudflare

### Design (short)
- **Setup state:** a singleton `Installation` record holds `base_domain`, the Cloudflare account/zone/tunnel ids, `cloudflare_api_token` and `tunnel_token` (**Active Record encryption**), and `cloudflare_connected_at`. After the admin exists, a signed-in admin is sent to the next unfinished step (`/setup/cloudflare`; Batch 3 adds storage). Sign-out always works.
- **Encryption keys come from the environment** (`AR_ENCRYPTION_*`). Dev and test use fixed dev-only keys. Production refuses to boot without real ones, which the installer generates (Batch 4). Nothing depends on `config/master.key`.
- **Token check, reads only, before anything is created** (the design's TOKEN CHECK list; each row GO or NO-GO with a reason):
  1. `GET /accounts`: exactly one account. None, or a 401, means the token isn't valid. Several means "make a token for just one account".
  2. `GET /accounts/{id}/cfd_tunnel?per_page=1`: Tunnel access.
  3. `GET /zones?name=<base>`, then `GET /zones/{zone}/dns_records?per_page=1`: DNS access on the base domain.
  Edit rights can't be read without writing, so they're proven by the create steps, whose errors are shown as-is.
- **Create, every step idempotent,** so a rerun finishes what failed:
  1. **Tunnel** `houston-<first label of base>`: reuse one with that name (`GET …/cfd_tunnel?name=…&is_deleted=false`), else `POST …/cfd_tunnel {name, config_src: "cloudflare"}`.
  2. **Tunnel token:** `GET …/cfd_tunnel/{id}/token`.
  3. **Ingress:** `PUT …/cfd_tunnel/{id}/configurations`, in this order:
     - `admin.<base>` → Mission Control
     - `hooks.<base>` with `path ^/[a-z0-9-]+$` → Mission Control
     - `hooks.<base>` → `http_status:404`
     - everything else → kamal-proxy
     The service URLs default to `http://mission-control:80` and `http://kamal-proxy:80`, with env overrides for Batch 4.
  4. **Wildcard:** `*.<base>` CNAME → `<tunnel>.cfargotunnel.com`, proxied, comment `managed-by:houston`. An existing record with that comment is updated in place. **A record without it is never touched:** that's a NO-GO asking you to remove it.
  5. Mark it connected and redirect. Starting cloudflared on the server with the token is Batch 4 (installer).
- **HTTP:** `Cloudflare::Client` on Net::HTTP (10 s timeouts). Tests use WebMock with exact method, path, auth header and body expectations, and no real network.
- **Page:** the design's SetupCloudflare board, with a live "What Houston will create" aside for the entered domain. No "Back": step 1 can't be redone once the admin exists.

### Crash-gap template
```text
Durable steps: tunnel (Cloudflare) → token stored → ingress (Cloudflare) → DNS record (Cloudflare) → connected_at (DB)
Dies between any two: Cloudflare has some of it; Mission Control isn't marked connected
Retry: submit step 2 again
Must still accomplish: everything, reusing what exists (tunnel by name, DNS record by managed-by comment)
Must not block repair: no "tunnel already exists" failure; reuse it
Never: modify or delete a DNS record without the managed-by:houston comment
```

### AC ↔ test map (Batch 2), `mission_control/test/…`
| # | AC | Test | Lens |
|---|---|---|---|
| 1 | After step 1, signed-in pages lead to `/setup/cloudflare` until connected; sign-out still works; not signed in → sign-in | `integration/setup_gate_test.rb` `test "the admin is taken to the Cloudflare step"` | Preconditions, Authz |
| 2 | `GET /setup/cloudflare` shows the step-2 form and the aside | `controllers/setup/cloudflare_controller_test.rb` `test "shows the Cloudflare form"` | Contract |
| 3 | Happy path: the exact requests (tunnel POST body, ingress PUT body, DNS POST body with the comment); tokens stored encrypted (raw column ≠ token); connected; redirect `/` | `test "creates the tunnel, ingress and wildcard"` | Contract, Atomicity |
| 4 | Check failures (401, no account, two accounts, tunnel 403, zone missing) → 422 with the NO-GO row, **no write requests** | `test "a failed token check creates nothing"` (table) | Preconditions, Signals |
| 5 | Invalid base domain → 422, no requests | `test "the base domain must be a domain"` | Contract |
| 6 | Rerun with the tunnel and our DNS record already there → no tunnel POST, the DNS record PATCHed to this tunnel, connected | `test "a rerun reuses what exists"` | Crash & repair, Re-entry |
| 7 | A `*.<base>` record without `managed-by:houston` → NO-GO naming it, no PATCH/DELETE of it, not connected | `test "never touches a DNS record Houston didn't create"` | Contract |
| 8 | Cloudflare 500 on the ingress PUT → 422 with Cloudflare's message, not connected; a rerun completes | `test "a Cloudflare error mid-way can be retried"` | Crash & repair, Signals |
| 9 | Connected → `/setup/cloudflare` redirects to `/` | `test "the step closes once connected"` | Preconditions |
| 10 | **Real Cloudflare (needs your token and a base domain):** the same flow against the real API; the tunnel, config and `*.<base>` record exist with the comment | manual check | Parity |

### Done (Batch 2)
- Red: the suite failed to load (`Setup`, `Installation` and the client didn't exist). Green: 30 runs, 0 failures. Request bodies are compared as parsed JSON (WebMock hashes), so key order isn't pinned.
- Mutations: patching any existing `*.<base>` record was caught only by row 7; skipping the "all checks GO" gate was caught only by row 4.
- Review: tokens encrypted at rest (asserted on the raw columns), not echoed back in forms, and filtered from logs by Rails' default `filter_parameters` (`token`). No required changes.
- **Row 10 (real Cloudflare) is pending your token and base domain.**

### Lens run (Batch 2)
| Lens | Where |
|---|---|
| Contract | 2, 3, 5, 7 |
| Preconditions / Authz | 1, 4, 9 |
| Crash & repair / Re-entry | 6, 8 + template |
| Signals | 4, 8: every failure names what's wrong and what to do |
| Atomicity | 3: secrets are encrypted before they're stored |
| Parity | 10: the real API (spec §14 lists this as a must-verify) |

## Batch 3: first-run step 3, default backup storage

### Design (short)
- **`StorageLocation`:** name (`^[a-z][a-z0-9-]{0,62}$`, unique), kind, settings (JSON), credentials (JSON, **encrypted**), `restic_password` (**encrypted**), `verified_at`, `acknowledged_at`, `default`. Capabilities: `nfs` and `local` hold live volumes *and* backups; `s3` and `b2` hold backups only (spec §9).
- **Kinds in this batch:** `nfs` (server + export path), `local` (an absolute host path), `s3` (endpoint, bucket, access key, secret), `b2` (bucket, key id, application key). **Defer `sftp`** to build step 5 (storage in Settings): restic over SFTP needs an SSH key managed for it. The radio group shows the four kinds that work.
- **restic runs in Docker** (`restic/restic`, pinned version), through a `DockerCommand` runner (Open3). Tests use a recording fake.
  - `nfs`: `docker volume create --driver local --opt type=nfs --opt o=addr=<server>,rw,nfsvers=4 --opt device=:<export> houston-storage-<name>` (inspected first, so reruns reuse it), mounted at `/repo`.
  - `local`: `-v <path>:/repo`.
  - `s3`: `RESTIC_REPOSITORY=s3:<endpoint>/<bucket>/houston`.
  - `b2`: `RESTIC_REPOSITORY=b2:<bucket>:houston`.
  - **Secrets never go in argv:** `-e NAME` without a value takes it from the docker CLI's own environment (`RESTIC_PASSWORD`, `AWS_*`, `B2_*`).
- **Crash-safe order:**
  1. Save the location with a newly generated password (unverified).
  2. `restic init`.
  3. If it answers "already exists", open the repository with the saved password (`restic cat config`).
  4. Mark it verified.
  A rerun reuses the saved password, so a crash between steps never leaves a repository nobody can open. A repository that exists but won't open with our password is a NO-GO, and Houston never deletes it.
- **Pages (design SetupStorage):** the type radios and per-kind fields, then the test-write result ("GO Wrote a test file. Backups are ready to go here."). Then **SHOWN ONCE: Save this password now**, with the password, Copy (Stimulus + clipboard), Download .txt, and the "I saved it somewhere off this server" checkbox, which **Finish setup** requires.
  - Finishing sets `acknowledged_at` and makes the location the default. After that, no route shows the password again.
  - Before finishing, reloading shows it again. It's the same setup session, and losing it to a reload would be worse.
- **Mission Control needs Docker:** the Docker CLI goes into its image (copied from `docker:29-cli`, like the CLI's toolchain). Its dev compose mounts `/var/run/docker.sock`, a dev-only bind mount that `houston test` drops.
- `Setup.next_step`: Cloudflare → storage (no acknowledged default location) → done.

### Crash-gap template
```text
Durable step 1 (primary): StorageLocation row with the encrypted password (unverified)
Dies before: restic init / verification / acknowledgement
Retry: submit step 3 again with the same name → same row, same password
Must still accomplish: init (or open the existing repository with the saved password), verify, then ask to save the password
Paths that must not block repair: "repository already exists" is success when our password opens it
Never: delete or re-init a repository; show the password after acknowledgement
```

### AC ↔ test map (Batch 3), `mission_control/test/…`
| # | AC | Test | Lens |
|---|---|---|---|
| 1 | Cloudflare connected, no default storage → pages lead to `/setup/storage` | `integration/setup_gate_test.rb` `test "the admin is taken to the storage step"` | Preconditions |
| 2 | The form offers nfs/local/s3/b2 with each kind's fields; no sftp | `controllers/setup/storage_controller_test.rb` `test "shows the storage form"` | Contract, Honest surface |
| 3 | NFS happy path: the exact docker calls (volume inspect → create with NFS opts → restic init with `-e RESTIC_PASSWORD`); the password is **not in any argv**; the location is saved encrypted; the password page shows it with the checkbox | `test "an nfs location is created and initialized"` | Contract, Atomicity |
| 4 | local / s3 / b2: the exact mounts and `RESTIC_REPOSITORY`; credentials only via env, never argv | `test "each kind gets its repository and credentials"` (table) | Contract |
| 5 | Invalid input (name, server, relative path, missing bucket/keys) → 422, no docker calls | `test "invalid details run nothing"` (table) | Preconditions |
| 6 | `restic init` fails (permission denied) → 422 NO-GO with restic's message; a rerun with the same name reuses the saved password; "already exists" plus `cat config` OK → verified | `test "a failed init can be retried with the same password"` | Crash & repair |
| 7 | "Already exists" but `cat config` fails (wrong password) → NO-GO, nothing deleted, not verified | `test "never takes over a repository it can't open"` | Contract |
| 8 | Finish without the checkbox → 422. With it → default + acknowledged, setup complete, redirect `/`; afterwards the password page and download → redirect/404 | `test "finishing needs the password saved and closes the step"` | Preconditions |
| 9 | Download .txt before finishing → `text/plain` attachment with the password; before verification → 404 | `test "the password can be downloaded until setup finishes"` | Contract |
| 10 | **Real run:** a `local` location on this Mac through the real Docker socket; `restic init` creates a repository in a temp folder; the page shows the password; Copy works in the browser; Finish lands on `/` | manual check | Parity |

### Done (Batch 3)
- Red: 39 errors (the fixtures referenced a missing table). Green: 39 runs. One test bug fixed: the gate test's `setup` deletes users, so it creates its own admin.
- Mutation: always generating a new password (instead of reusing the saved one) was caught only by the retry test.
- **Real run (row 10):** with the Docker socket mounted in dev and Cloudflare marked connected in the *dev* database (the real Cloudflare check is still pending), a `local` location at a temp folder on this Mac made Mission Control run `restic init` through Docker. The repository appeared on disk (`config`, `data`, `keys`…) and **opened independently** with the page's password. The `.txt` download matched the page. Finish without the box → 422 with the message; with it → `/`, and the location is default + acknowledged, `Setup.next_step` → nil, download closed. The forms were driven with curl; Copy wasn't exercised in a real browser (no browser session without typing the password).
- **Review, fixed test-first:** the password page and download now send `Cache-Control: no-store`, and the "doesn't open it" message includes restic's own words (so a network error isn't passed off as a wrong password).
- **Deferred:** a way to pick a different location before finishing (today: finish, then change it in Settings, build step 5); an NFS version choice (fixed at `nfsvers=4`); a real NFS check against your UNAS.

## Batch 5: the empty Projects landing and system status

(Done before Batch 4, which needs your OK to create OrbStack test machines. Nothing in it depends on the installer.)

### Design (short)
- **`/`, once setup is complete (design ProjectsEmpty):**
  - "FLIGHT BOARD · <BASE>" and the stat strip (projects 0, in flight 0, no-go 0, next backup —).
  - The empty state with the two paths. **A**'s copy becomes "repo has an x-houston block", and its Add project button shows as unavailable ("Coming soon") until build step 4 adds the page. **B** has the `houston init` commands with Copy.
  - The pre-flight check.
- **LAN banner:** "Setup complete. Mission Control now lives at admin.<base>…" shows unless the request's host is `admin.<base>`.
- **Pre-flight check:**
  - Admin login (GO).
  - **Tunnel `houston-<label>`:** GO "N connections to Cloudflare", HOLD "cloudflared isn't connected yet", or a "can't check" state with Cloudflare's reason.
  - **`*.<base>`:** GO.
  - **Backup storage `<name>`:** GO, "default · password saved".
  - **Cloudflare Access:** OPTIONAL.
- **Header system strip** on signed-in pages: `TUNNEL` (GO when there are connections), `REGISTRY` (GO when `GET <registry>/v2/` answers, default `http://registry:5000`), and the UTC time. **RUNNERS stays out until runners exist** (build step 4).
- **Status is cached for 30 s** (`Rails.cache`), so the Cloudflare API isn't called on every page view. Every probe has a 2 s timeout.

### AC ↔ test map (Batch 5)
| # | AC | Test | Lens |
|---|---|---|---|
| 1 | The home page shows the flight board, the zero stats, both paths (A with an unavailable Add project, B with the commands) | `controllers/projects_controller_test.rb` `test "the empty flight board"` | Contract, Honest surface |
| 2 | The LAN banner shows on `localhost` and is hidden on `admin.svnmns.com` | `test "the banner points LAN visitors at admin.<base>"` | Contract |
| 3 | The tunnel row: 4 connections → GO "4 connections to Cloudflare"; none → HOLD; Cloudflare error → "Can't check" with the reason | `test "the pre-flight tunnel row"` (table) | Signals |
| 4 | The header strip: TUNNEL/REGISTRY GO or NO-GO from the probes; no RUNNERS | `test "the header shows tunnel and registry status"` | Signals, Honest surface |
| 5 | Two page views within 30 s call Cloudflare once | `test "status checks are cached"` | Scale |

### Done (Batch 5)
- Red: 5 failures. Green: 44 runs. Two test fixes: the eyebrow is upper case (pattern made case-insensitive), and the registry probe now has a default stub in `test_helper`, because WebMock blocks real connections suite-wide.
- Real render in dev (curl, signed in): strip "TUNNEL NO-GO REGISTRY NO-GO", pre-flight tunnel "? Can't check: not connected to Cloudflare" (dev has no real tunnel), storage "mac-local · Default · password saved", the LAN banner pointing at admin.<base>. RUNNERS isn't shown.

### Open questions (for later batches, not blocking Batch 1)
1. **Real Cloudflare checks (Batch 2):** automated tests stub the API with contract expectations, but spec §14 needs a real run. Can I use a Cloudflare API token and a base domain you pick (svnmns.com, or a spare domain)?
2. **Installer testing (Batch 4):** I'd test `install.sh` in a throwaway OrbStack Linux machine (Ubuntu, then Debian). OK to create and delete those?
3. **Where Mission Control's image and the CLI binaries are published** (GitHub/Forgejo registry and releases). The installer needs a place to pull from. Until then, it builds the image locally on the server.

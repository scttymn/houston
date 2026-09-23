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

### Open questions (for later batches, not blocking Batch 1)
1. **Real Cloudflare checks (Batch 2):** automated tests stub the API with contract expectations, but spec §14 needs a real run. Can I use a Cloudflare API token and a base domain you pick (svnmns.com, or a spare domain)?
2. **Installer testing (Batch 4):** I'd test `install.sh` in a throwaway OrbStack Linux machine (Ubuntu, then Debian). OK to create and delete those?
3. **Where Mission Control's image and the CLI binaries are published** (GitHub/Forgejo registry and releases). The installer needs a place to pull from. Until then, it builds the image locally on the server.

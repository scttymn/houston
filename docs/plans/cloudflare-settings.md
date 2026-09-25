# Plan: Cloudflare in Settings — see the tunnel, replace the token, repair

## Your direction
- "Cloudflare settings are read only. What if we want to make changes? There's also no way to view tunnels. It just shows 'Go' and 4."
- Keep host-by-host DNS: "more portable. I could spin up another box to do hosting too."

## What's there (evidence)
- **Settings › Cloudflare** (`settings/pages/show.html.erb`) shows:
  - the base domain
  - the DNS mode
  - the tunnel as GO plus a connection count (`SystemStatus.tunnel`, which counts `connections`)
  - three ingress lines of **fixed text**, not read from Cloudflare
- **Cloudflare can only be changed in first-run setup** (`CloudflareSetup`). Its token checks are read-only: the account, tunnel access, and DNS on the base zone.
- **There's no way to replace the token.** When estherpictures.com needed DNS access, the token had to be widened in Cloudflare's dashboard. A new token couldn't have been given to Houston at all.
- **The tools exist:** `TunnelRoutes.push!` sends the routes built from the database, `DomainDns#point` points a domain (touching only its own project's records), and `Cloudflare::Records.managed?`.

## Goal
Settings shows what Cloudflare actually has: the tunnel and each connection, the live routes, and Houston's DNS records. It can replace the API token (checked before it's saved), and it can repair routes and records. Each has a CLI path.

## Design (short)
- **Viewing: `CloudflareView.fetch`,** read-only, loaded **after** the page, in a lazy Turbo Frame. A slow or failing Cloudflare only fills that panel with a reason; the page itself never waits. It shows:
  - **The tunnel:** its name, its ID (with Copy), its status and when it was created, and **each connection:** the data centre (`colo_name`), cloudflared's version, the origin address and when it connected.
  - **The live routes:** the tunnel's configuration from Cloudflare, one row per rule, marked **DRIFT** when it differs from `TunnelRoutes.rules` (the database's view, maintenance routes included), with a note that Repair puts it back.
  - **Houston's DNS records:** every record whose comment starts `managed-by:houston`, in every zone the token can see. Each shows its name, its project (or `admin`/`hooks`), proxied or not, and whether it points at **this** server's tunnel. A record pointing at another tunnel is shown as another server's: host by host lets two Houston servers share a domain.
- **Replace the token: `CloudflareToken.replace(token)`.** It runs every check before saving:
  - it sees the installation's account (the same account ID)
  - it has tunnel access, and sees this tunnel
  - it has DNS on the base zone
  - it has DNS on every project domain's zone
  
  All pass: the token is saved (encrypted), and the old one is gone. Any fail: the checks are shown, and **the old token stays**. The checks are read-only, as in setup, so they can't prove edit rights. The page says which permissions to grant.
- **Repair: `CloudflareRepair.run`.** It pushes the routes (`TunnelRoutes.push!`), then points every project's names (its `<name>.<base>` in host-by-host mode, and its domains) through `DomainDns`, which never touches a record without that project's comment. It reports one result per item. It's idempotent, so running it twice changes nothing more.
- **CLI-first:**
  - `houston cloudflare` shows the same view (`--json`), from `GET /api/v1/cloudflare`
  - `houston cloudflare token` reads the new token from stdin (a hidden prompt on a terminal), via `PUT /api/v1/cloudflare/token`
  - `houston cloudflare repair` runs `POST /api/v1/cloudflare/repair`
- **Out of scope (named):** more than one base domain (below), and switching the DNS mode. You're keeping host by host.

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The view lists the tunnel and each connection (colo, version, origin, since) | `cloudflare_view_test.rb` `test "the tunnel and its connections"` | Contract |
| 2 | Live routes are shown, and a difference from the database's rules is DRIFT | `cloudflare_view_test.rb` `test "live routes, and drift"` | Contract |
| 3 | Managed records across zones, each with its project and whether it's this tunnel's; others' records aren't listed | `cloudflare_view_test.rb` `test "Houston's records, and another server's"` | Contract, Authz |
| 4 | Cloudflare failing or slow shows a reason in the panel; the settings page renders without calling Cloudflare | `cloudflare_view_test.rb` `test "a failing Cloudflare is a reason, not an error"`; `settings` test `test "the page doesn't wait on Cloudflare"` | Signals, Scale |
| 5 | A good token replaces the old one, encrypted | `cloudflare_token_test.rb` `test "a token that passes every check replaces the old one"` | Contract |
| 6 | A token missing any check (another account, no tunnel access, no DNS on the base zone or a project domain's zone, rejected) is refused, and the old token stays | `cloudflare_token_test.rb` `test "any failed check keeps the old token"` | Preconditions |
| 7 | The token is never shown or logged: the form is a password field, the value never reaches a log, the API is write-only | `cloudflare_token_test.rb` `test "the token is never shown or logged"` | Authz |
| 8 | Repair pushes the routes, and points each project's names and domains; it's idempotent, and a foreign record is reported, not touched | `cloudflare_repair_test.rb` `test "repair puts routes and records back"` | Crash & repair |
| 9 | Settings needs the admin, and the API needs a token; signed out, nothing changes | `settings` and `api_v1` tests | Authz |
| 10 | CLI: `houston cloudflare` (and `--json`), `cloudflare token` from stdin only, `cloudflare repair` | Go `TestCloudflare*` | Contract |
| 11 | Checked in a browser, and live on the server: the tunnel's 4 connections are listed with their data centres | the live check, recorded here | Parity |

## Evidence
- **Tests:** Rails 340 runs, 0 failures (rubocop clean); Go passes (gofmt clean).
  - The models: `cloudflare_view_test.rb`, `cloudflare_token_test.rb`, `cloudflare_repair_test.rb`.
  - The page: `settings/cloudflare_controller_test.rb`.
  - The API: `api_v1_cloudflare_test.rb`.
  - The CLI: `cloudflare_test.go`.
- **Mutation check.** Each of these fails a test:
  - saving despite a failed check
  - skipping project domains' zones
  - accepting any account
  - overwriting a foreign record in Repair
  - pointing never-deployed projects
  - no drift detection
  - one failing part breaking the whole view
  - every record counted as this server's
- **Found on the way:**
  - `LatestReleaseTest` (v0.2.0) used Turbo's broadcast test helper without including it, so it passed only by test order. It now includes it.
  - The Settings page's "Add storage location" button overflowed at 320 px. Section headings now wrap.
- **Visual check** (Settings rendered with the live panel inserted, after a Repair):
  - the tunnel and its 4 connections
  - the routes, with the drifted one and the missing one flagged
  - Houston's records, with a second server's app marked "another server"
  - Repair's results, the token field and the Repair button
  - No overflow from 320 to 1280 px.

- **The live check (11), 2026-09-24:**
  - v0.3.0 (edf1f2f) released green. The server updated with the README's version-free `curl …/install.sh | sudo sh`: it found "The latest release is v0.3.0", and Mission Control and both runners run `…:v0.3.0`.
  - `houston cloudflare` from the laptop and Settings › Cloudflare in the browser both show the tunnel `houston-svnmns` (healthy), its **4 connections (DFW16, MCI01, MCI03, DFW15**, cloudflared 2026.9.1), and the 4 routes with no drift.
  - Houston's 7 records all point at this server: admin, hooks, equip, valleybuiltcrossfit, estherpictures (.svnmns.com), and estherpictures.com and its www.
  - The four apps each answered 10 × 200.

## Later (named, not built)
- **Several base domains, rather than changing the one.** Your idea: the server serves several (`svnmns.com`, `example.dev`, …), each with its own zone, admin and hooks names, and each project picks one (defaulting to the first). Moving to a new domain becomes gradual: add it, move projects one at a time with both names working, and retire the old one when nothing uses it. It also lets one server host separate groups of apps. The token check and Repair here already work zone by zone, so they carry over.
  - Changing the one base domain in place touches every webhook URL, every CLI login, the admin sign-in, Cloudflare Access, and apps that name their own host (valleybuiltcrossfit's `APP_HOST`, estherpictures' `check_origin`).
- Switch the DNS mode (wildcard vs host by host).

## Rethink (2026-09-25, after v0.4.0)
- **Direction:** "the cloudflare section … is a bit of a mess". It started empty and cached nothing. "Copy ID" didn't say what it copied. Routes and records were headless sub-tables. The token field was one long box with a dangling button. Later: the help should list the permissions, with "More" for the details and a link to Cloudflare, and name no one's domains (only the base domain configured here).
- **Design:**
  - `CloudflareView.fetch` keeps a complete answer in Rails.cache (solid_cache, so it survives restarts), keyed by tunnel. A failed check shows its failed parts from the last good answer and never replaces it.
  - `last` and `stale?` (older than 5 minutes). Settings renders the last answer at once, and the frame asks again (lazy src) only when it's old. "Check now" reloads the frame.
  - A new token and Repair `forget` the kept answer.
  - The panel: labelled tunnel facts (Name, Created, Tunnel ID with "Copy"), then headed tables. Connections: Data center, cloudflared, From, Connected. Routes: Hostname, Path, Goes to, in words, with the address in small print. Records: Name, For, Points at, Proxy, with Houston's own names first and each project's together. At 600 px and below, rows stack with their labels.
  - **Token row:** what's needed, "More" (each permission and why, "All zones" is simplest, the configured base domain, Cloudflare's API Tokens page), then the field and "Check and replace". No other buttons.
  - **Repair row:** what it does, one button, and results as a table.
  - The dead `.ingress` CSS is removed.
- **Tests** (`settings/cloudflare_controller_test`, `cloudflare_view_test`, `cloudflare_token_test`):
  - shown at once and refreshed when old
  - a good answer kept over a failed check, and per tunnel
  - every table's headings and rows in words
  - the token row: its one button, a link, and no domain but the configured base
  - the repair row and its table
  - the kept answer forgotten after a new token and after Repair
- **Mutation check:** all 11 caught.
- **Visual check:** the production image, seeded with placeholder domains, at 1280 and 375 px. Phone rows first split the small-print address into the label column; fixed by wrapping the cell.

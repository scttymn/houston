# Plan: build step 8, the personal projects move to Houston

## Your direction
- "I'm going to be replacing coolify with houston, so I assume I'll need to create a new VM on my ProxMox box for Houston."
- "For the record, we're going to start with my self-hosted personal projects: equip, valleybuildcrossfit, estherpictures."
- equip: "there is not data on equip. Easy :D"
- Cutover: "a short maintenance window" per app, so no write is lost.
- Some domains are outside your Cloudflare account.
- "valleybuilt.svnmns.com is what we will host locally. It won't be on production values until it's ready." So valleybuiltcrossfit.com stays where it is, and nothing moves from Porkbun.

## Goal
equip, valleybuilt and estherpictures run on a production Houston, on a new Proxmox VM, and deploy on push. Coolify stops serving them. Each moves with its data (where it has any), its domains, and a way back until it's proven. ha_web and valley_coffee_co wait for a later round.

## What's there (evidence)
| | equip | valleybuilt | estherpictures |
|---|---|---|---|
| Stack | Rails 8.1, SQLite, Solid Queue | Rails, SQLite, Solid Queue | Phoenix 1.8, SQLite (`ecto_sqlite3`) |
| Runs on | Coolify | Coolify (to confirm) | Coolify (its Dockerfile says so) |
| Data | none | SQLite and Active Storage files (local disk) | SQLite, and uploads under `/opt/estherpictures/data` |
| Git | `git@github.com:scttymn/equip.git` (moved from the LAN Forgejo; `main` tracks `github/main`) | `git.svnmns.com/scttymn/valley-built-crossfit` | `git.svnmns.com/EstherPictures/estherpictures.com` |
| Domains | `equip.svnmns.com`, served today by the wildcard `*.svnmns.com` → Coolify's tunnel (no record of its own) | `valleybuilt.svnmns.com`, the same way (it answers 200 today). Its links default to `valleybuiltcrossfit.com` (`APP_HOST`), so the compose file sets `APP_HOST: valleybuilt.svnmns.com`. valleybuiltcrossfit.com (Porkbun) isn't this app yet | `estherpictures.com` and `www.`, **in your Cloudflare account** |
| Houston setup | done on a copy (migration notes in `init-generic.md`) | not yet | done on a copy (migration notes, including two app edits) |

Cloudflare: the token sees 14 zones, including `svnmns.com` and `estherpictures.com`. `*.svnmns.com` is Coolify's tunnel, unmanaged by Houston.

## Three things the evidence forces
1. **Production and the tests can't share a base domain.** The tunnel is named after the base domain (`houston-svnmns`), and setup reuses a tunnel with that name. So a test run on `svnmns.com` would:
   - take over production's tunnel, including the route for `admin.svnmns.com`
   - then, in its cleanup, delete that tunnel and every `managed-by:houston` record, taking the apps offline

   The tests move to a zone of their own (decision 1). The test scripts also get a guard: they refuse a base domain whose `admin.<base>` already answers as a Houston.
2. **The cutover is DNS, record by record.** Houston's first deploy of a project creates its record (`equip.svnmns.com`, or a custom domain's CNAME), which beats Coolify's wildcard. Cloudflare's edges then switch over a few minutes, as the tests showed. So, for an app with data:
   - the old copy stops taking writes first
   - the data is copied across while it's stopped
   - Houston's deploy switches the DNS
   - the way back is to delete Houston's record and restart the old app
3. **Two of the three are only a DNS switch on `svnmns.com`.** equip and valleybuilt are served today through the wildcard, so Houston's first deploy of each takes the name over. Only estherpictures moves a domain of its own, and its zone is already in Cloudflare.

## Batches
0. **Prerequisites.** Your steps and one code change:
   - **The test domain** (decision 1): `cloudflare-check.env` points at it, and the tunnel-stage scripts refuse a base domain with a live Houston (a red test first: a stub `admin.<base>` answering `/up`).
   - **The Houston VM (yours):** on Proxmox. Debian 12 or Ubuntu 24.04 is simplest; any apt, dnf or pacman distro works. 4 vCPU, 8 GB RAM and 100 GB disk are enough for three small apps plus builds. It needs a fixed LAN address, a route to GitHub and `git.svnmns.com`, and it must *not* be the Coolify host.
   - **Backup storage** (decision 4).
1. **Houston itself, and equip** (fully specified below).
2. **Bringing data in** (decision 3): how an app's current SQLite files and uploads land in its Houston volumes during its window. The same method serves every app with data, ha_web and valley_coffee_co included.
3. **estherpictures:**
   - its repo gets the setup from the migration notes, including its two app edits (dev bind address, esbuild's `NODE_PATH`)
   - a staging deploy at `estherpictures.svnmns.com` with a copy of its data
   - then the window: Coolify stopped, the final data copy, `domains: [estherpictures.com, www.estherpictures.com]` deployed (Houston makes the CNAMEs), checks, done
4. **valleybuilt** (moved on 2026-09-24; its repo is `git@github.com:scttymn/valleybuiltcrossfit.git`, and it started fresh with no data brought across):
   - The project is named `valleybuiltcrossfit` on purpose (the full domain), so it's served at `valleybuiltcrossfit.svnmns.com`, with `APP_HOST` the same. `valleybuilt.svnmns.com` was Coolify's name, and it's gone with Coolify's copy.
   - Solid Queue runs inside Puma from the production image's `ENV`, since development has no queue database.
   - Deploy #1 (3b76cdd) went GO, with 10 × 200 on `/up`; 43 jobs ran on Solid Queue. Coolify's valleybuilt is stopped.
   - Still to do: the GitHub webhook and a first backup.

   The plan as written:
   - its repo gets the setup (like equip's: Rails, SQLite, Solid Queue), with `APP_HOST: valleybuilt.svnmns.com`
   - its data comes in (decision 5)
   - its first Houston deploy takes over `valleybuilt.svnmns.com` from the wildcard, as equip's does
5. **Afterwards:**
   - the three apps' Coolify deployments are removed once each has served from Houston for a week
   - Coolify's wildcard `*.svnmns.com` stays until ha_web and valley_coffee_co move too (the next round)
   - then publishing images (see the memory note: after step 8)

## Batch 1: Houston itself, and equip

### Steps
1. **Install Houston on the VM** (README §1–2):
   - `git clone` this repo, then `sudo HOUSTON_SOURCE=… install/install.sh`
   - first run: the admin login, Cloudflare with base domain `svnmns.com` and your token, and backup storage
   - Houston finds `*.svnmns.com` taken (Coolify's) and uses **host-by-host DNS**, as the tests on `svnmns.com` did
   - check: `https://admin.svnmns.com` signs in; the header shows TUNNEL, RUNNERS 2/2 and REGISTRY; Coolify's apps still answer
2. **The CLI on your laptop:** `bin/install`, `houston login https://admin.svnmns.com`.
3. **equip's repo gets its Houston setup** (decision 2): `houston init` in it, plus the edits from the migration notes:
   - real `dev` and `test` stages
   - the compose file: port 3000 on `127.0.0.1`, `RAILS_MASTER_KEY: ${RAILS_MASTER_KEY:-}` (optional: `houston test` fills required variables with random values, which Rails can't decrypt with; so it's set before the first deploy, not held for), `storage:/rails/storage`
   - `x-houston`: `health: /up`, `app_port: 80`, the console, test and release commands
   - `houston dev`, `houston test` and `houston dev --production` pass locally
4. **Link and deploy:**
   - `houston link git@github.com:scttymn/equip.git --wait`; add the deploy key to the GitHub repo (read-only)
   - `houston secrets set RAILS_MASTER_KEY --project equip < config/master.key`
   - `houston deploy --server --follow --project equip`
5. **The switch:** the first GO creates `equip.svnmns.com` → Houston's tunnel, beating the wildcard. There's no data, so there's no window. Coolify's copy can keep answering until the edges have switched.
   - **Found on the real run:** deploy #1 went NO-GO (`RAILS_MASTER_KEY` wasn't set yet, and an optional secret doesn't HOLD), but its sync had already pointed the name. So `equip.svnmns.com` answered 502 until deploy #2. Now a project that has never served keeps its names where they are until its first GO, and a failed first deploy leaves the old host answering. Tested in `api_sync_test.rb` `test "a project's first deploy points its names only once it's GO"`.
6. **Checks** (all done on 2026-09-24; equip has moved):
   - deploy #2 GO; `equip.svnmns.com/up` 10 × 200 in a row
   - the GitHub webhook: the ping got 202, and a push to `main` (8d9f56d) deployed on its own as #3, GO with its tests
   - the first backup: GO, snapshot 708c4477, 321 KB, with its four SQLite files
   - Coolify's equip stopped. Its "Docker cleanup" was left off, so its image stays for the way back. `equip.svnmns.com/up` still 10 × 200, from Houston.

   The steps as planned:
   - `https://equip.svnmns.com/up` answers 200, ten in a row, from Houston
   - a push to the repo, with the webhook added in GitHub (push events, JSON, the secret), deploys on its own
   - `houston backup --follow --project equip` is GO
   - then stop equip in Coolify
7. **The way back** (until step 6 passes): delete the `equip.svnmns.com` record (`managed-by:houston`) in Cloudflare, and the wildcard sends traffic to Coolify's copy again.

### AC ↔ check map (Batch 1)
It's an operation on your machines, so the checks are live, recorded in the transcript.

| # | Acceptance criterion | Check | Lens |
|---|---|---|---|
| 1 | The test scripts refuse a base domain with a live Houston, and the tests run on their own zone | a red test with a stub `admin.<base>`; the next `orbstack.sh` run on the new zone | Preconditions |
| 2 | Houston installs on the VM and finishes first run on `svnmns.com` in host-by-host mode; Coolify's apps keep answering | the installer's output; the header strip; `curl` to each current app | Parity |
| 3 | equip's Houston setup passes locally (`dev`, `test`, `dev --production`) | the transcript | Parity |
| 4 | equip deploys GO on the VM, and `equip.svnmns.com` settles on Houston (10 × 200 in a row) | `houston deploy --follow`; `settle.sh`'s check against the real name | Contract |
| 5 | Push to deploy works from GitHub | a commit, the webhook, a GO deploy | Contract |
| 6 | A backup of equip is GO on the chosen storage | `houston backup --follow` | Contract |
| 7 | The way back is written down and possible until Coolify's copy is removed | the runbook in this plan | Crash & repair |

## Later (named, not built)
- **Rename a storage location** from Settings › Storage, and with `houston storage rename <old> <new>` (CLI-first). Found at first run: the production VM's local-path location is named `unas`, which the real UNAS over NFS will want. A name is only a label, since projects and volumes point at the location's row, not its name. So a rename changes what Settings, `houston storage`, `houston volumes` and the snapshot lists show, and nothing on disk or in restic. The usual rules apply: lowercase letters, digits and dashes, and unique.

## Decisions (yours)
1. **The test domain.** I'd keep `svnmns.com` for production, where `equip.svnmns.com` and your git host already live, and give the tests one of your other zones. Which one can be the throwaway (records created and deleted by every test run)? For example `mostlyseriousgames.com`, `featureflow.app` or `trailbound.app`, if nothing serves on it.
2. **The apps' own repos.** The Houston setup (the compose file, `x-houston`, Dockerfile stages, and estherpictures' two app edits) has to live in each app's repo. May I commit it there, on a branch named `houston`, and push it for you to merge? Or would you rather apply it yourself from the migration notes?
3. **Bringing data in** (Batch 2), for estherpictures (and valleybuilt, if decision 5 says so) and the apps after them:
   - **(a) A runbook:** in each app's window, copy the files from Coolify's volume into Houston's volume by hand, using documented `docker` commands. It needs no new code, but it's a manual step, repeated for each app with data.
   - **(b) `houston import`:** turn the files into a Houston snapshot (the same manifest a backup writes), then restore it with the existing restore, which is zero-downtime and already proven. It's new code with tests, and every later migration gets it for free. It also fits "every action has a CLI path".

   I'd pick (b).
4. **Backup storage for production.** The UNAS over NFS, B2 offsite, a disk on the VM, or a mix?
5. **valleybuilt's data.** It's in development, so does its current data on Coolify need to come across, or can it start fresh on Houston, like equip?

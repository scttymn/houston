# Plan: security fixes from the 2026-09-24 audit

The findings: [security-audit-2026-09-24.md](security-audit-2026-09-24.md). Kept private until v0.4.0 shipped them.

## Direction
- "Since this is public now. We should do a security audit."
- "Go ahead through all the batches and only stop if you need feedback from me." Close port 3000 after setup.
- Pushing and releasing wait for the user's OK. Everything ships together.

## Batch 1: repo containment (C1, H1, H2)

### Design
- **C1:** under `build:`, only `context`, `dockerfile`, `target` and `args` are allowed, as paths inside the repo: relative, with no `..`, `~`, URL or `@`. Every build arg has its value in the file. `Project.BuildPaths` resolves symlinks for the context and the Dockerfile, and refuses anything outside the checkout. `deploy` builds with it, and `houston test` checks it before building. Only one service may build (`checkApp`), so this covers every build.
- **H1, on the runner:** the claimed job's project must be the compose file's `name:`. If not, nothing is synced or built.
- **H1, on the server:** the runner's sync carries `claimed_deploy` and the deploy's token. Mission Control needs the claim to exist (else 422) and the token to be the claim's (else 403). The claim's project must be the payload's name (else 422, with nothing changed).
- **H2:** `houston test` passes only the Docker variables from the environment (HOME, PATH, TMPDIR, DOCKER_*), so `HOUSTON_TOKEN` never reaches a build. Bare build args are refused at load time.

### AC ↔ test
| # | AC | Test |
|---|---|---|
| 1 | build keys limited; paths stay in the repo; bare args refused | `TestBuildStaysInTheRepo` |
| 2 | symlinked context/Dockerfile out, missing context, dangling Dockerfile link: refused | `TestBuildPathsStayInsideTheCheckout` |
| 3 | deploy refuses a build resolving outside | `TestDeployRefusesABuildOutsideTheCheckout` |
| 4 | runner refuses a compose naming another project | `TestDeployRefusesAComposeForAnotherProject` |
| 5 | a claimed sync carries the claim and its token | `TestClaimedDeploySyncCarriesTheClaim`, `TestRestoreSyncCarriesItsToken` |
| 6 | server: wrong project 422 (nothing changed), no/wrong token 403, unknown claim 422, own claim syncs | `api_sync_test.rb` "a claimed deploy's sync is for its own project" |
| 7 | test env keeps only Docker's variables | `TestTestEnvKeepsOnlyWhatDockerNeeds` |

### Evidence
- The new Rails test failed first: a claim for `other` synced as `equip` (200).
- Suites: Go ok, gofmt clean. Rails: 341 runs, 0 failures. Rubocop clean.
- Mutation check. Each of these fails a test:
  - skipping the token check (403 expected)
  - skipping the name check (200 instead of 422)
  - `testEnv` passing every variable
  - no token header on a claimed sync
  - no `..` check
  - no containment after EvalSymlinks
  - the runner's name check removed
  - dangling symlinks allowed. This one survived at first, so its test was added.
- Later (named): per-deploy tokens instead of one runner token. That would bind unclaimed syncs and secret reads too.

## Batch 2: port 3000 and signing in (H3, M1, L1, L2)

### Evidence
- `install.sh` published `"3000:80"`. `force_ssl` and `assume_ssl` are off. Sessions were `cookies.signed.permanent`, and a LAN-made session worked at admin.<base>.
- **Measured** (Thruster from the v0.3.0 image, in front of a header-echo server):
  - Thruster *appends* its TCP peer to `X-Forwarded-For` ("6.6.6.6, 192.168.215.1").
  - It passes a client's `X-Forwarded-Host` and `X-Forwarded-Proto` through untouched, and sets them only when they're missing.
  - So `request.host` could be chosen (L1), and so could `remote_ip`: Rails puts XFF and `Client-Ip` entries ahead of REMOTE_ADDR (M1; the auth agent's late correction agrees).
- Setup counts as done for the installer at `Installation.connected?`: admin.<base> exists, and the storage step can finish there.

### Design
- **`ForwardedHeaders` middleware (first in the stack):**
  - The peer is XFF's last entry when REMOTE_ADDR is loopback (Thruster), else REMOTE_ADDR.
  - `Cf-Connecting-Ip` is the client only when the peer is cloudflared (Docker DNS, cached 60 s; a failed lookup trusts nobody).
  - It drops XFF, `Client-Ip`, `X-Forwarded-Host`/`Port`/`Server` and `Forwarded`. It drops the scheme headers unless the request came through the tunnel.
- **Sign-in:** 10 per address per 3 minutes, as before, plus 50 per 3 minutes from all addresses together (`EVERYONE`).
- **Sessions:** a `tunnel` column (from Cf-Ray, which a client can't strip) must match the request. They end after `IDLE` (2 weeks unused) or `LIFETIME` (30 days). Use is written at most hourly, and the cookie expires with the lifetime. Sessions from before the release count as made on the network, so they need one fresh sign-in at admin.<base>.
- **Installer:**
  - `choose_bind`:
    - `HOUSTON_BIND` if set (IPv4 only, checked before anything changes)
    - with no compose.yml, 0.0.0.0
    - otherwise, a one-off `compose run … rails runner 'exit(Installation.connected? ? 0 : 3)'`: 0 gives 127.0.0.1, 3 gives 0.0.0.0, and anything else gives 127.0.0.1 plus a note on how to open it
  - The probe uses the bound address. The report names admin.<base> and `ssh -L`.
- **Flight board:** a HOLD notice while `PortExposure.open?`, once Cloudflare is connected. It comes from the container's own `docker inspect` PortBindings, cached per container hostname, and includes the rerun command.
- **README:** "the only way in" corrected; close port 3000, `ssh -L`, `HOUSTON_BIND`, and the session lifetime documented. `docs/agents.md` step 1 gets the rerun.
- **Not done:** `config.hosts` against DNS rebinding. Once port 3000 is loopback-only, rebinding needs a browser on the server itself. Settings › Sessions (list and revoke) is named for later.

### AC ↔ test
| # | AC | Test |
|---|---|---|
| 1 | hooks.<base> ignores X-Forwarded-Host and Forwarded | `forwarded_headers_test` "a client's forwarding headers don't move it off hooks.<base>" |
| 2 | XFF and Client-Ip can't choose the rate limit's key | "on the network, X-Forwarded-For can't choose the rate limit's key" |
| 3 | tunnel visitors get their own counts; Cf-Connecting-Ip only from cloudflared | "through the tunnel, each visitor has its own count…" |
| 4 | a cap across all addresses | "failed sign-ins have a cap across every address" |
| 5 | https only from the tunnel; the session records the visitor's address | "https counts only from the tunnel" |
| 6 | LAN and tunnel sessions don't cross | `session_lifetime_test` (two tests) |
| 7 | idle and absolute expiry; ended sessions are deleted | "a session ends after two weeks unused, and a month after sign-in" |
| 8 | the cookie expires with the session | "the cookie lasts as long as the session can" |
| 9 | flight board HOLD while open; not when closed, unknown or before connected; read once | `projects_controller_test` "…close port 3000…" |
| 10 | installer: first install open, connected rerun closed, unfinished open, unknown closed and says so, HOUSTON_BIND chooses and is validated, probe, report | `install/test/install-bind.sh` |
| 11 | real stack: bind, rerun closes it, PortExposure, cloudflared DNS, spoofed headers through the real Thruster | `install/test/bind-e2e.sh` (OrbStack) |

### Evidence
- **Tests first:** every new test failed first for the right reason: no middleware, no `Session::IDLE`, no notice, no `choose_bind`.
- **Suites:** Rails 351 runs, 0 failures, rubocop clean. `install-bind.sh` and `install-version.sh` pass. shellcheck is clean.
- **Mutation check.** Each of these fails a test:
  - keeping X-Forwarded-Host
  - keeping Client-Ip
  - XFF's first entry instead of its last
  - trusting Cf-Connecting-Ip from anyone
  - keeping the scheme headers
  - dropping the scheme headers whenever the lookup fails
  - no global cap
  - no tunnel match on sessions
  - no idle limit
  - use never written down
  - ended sessions kept
  - a 20-year cookie
  - 127.0.0.1 counted as open
  - no cache
- **Found by the mutation check, and fixed:**
  - The 30-day limit survived its test. A new cookie carries its own expiry (Rails signs `exp` into it), so the test now uses a pre-release, 20-year cookie, which is what the server-side limit is for.
  - The flight board's `connected?` guard was dead: an unconnected install never reaches the flight board. It's removed, along with its vacuous test.
- **Real stack (`bind-e2e.sh`, OrbStack Debian, installed from this checkout):**
  - A first install is open on 192.168.139.x:3000, and says to rerun.
  - `PortExposure.open?` is true inside the container.
  - Docker's DNS lookup works from Mission Control.
  - A spoofed X-Forwarded-Host on hooks.<base> gets 404 through the real Thruster.
  - Once connected, the rerun binds 127.0.0.1: the network address is refused, loopback answers, and the report names `ssh -L`. `PortExposure` then says closed.
  - `HOUSTON_BIND=0.0.0.0` opens it again.
- **Sign-in limit, the production image run locally:**
  - v0.3.0, rotating X-Forwarded-For: 11 of 11 attempts accepted as "Try another…", so no limit.
  - This branch: 10, then "Try again later." (also with a rotating Client-Ip).
  - The e2e's own sign-in check was broken by `curl -X POST -L`, which re-POSTs on the redirect. Fixed; it's rerun with batch 3.
- **Visual check:** the dev flight board (PortExposure really true there, since dev publishes 3000:3000) shows the HOLD notice with the copyable command. No overflow at 375 px.
- **Production data** (checked by reading the Cloudflare test file's BASE_DOMAIN only): `orbstack.sh`'s Cloudflare stage runs against svnmns.com, the production domain. It would repoint admin.svnmns.com, so it wasn't run. `bind-e2e.sh` marks setup connected inside the VM instead.
- **bind-e2e.sh, final run:** BIND E2E PASS, all 13 checks, including the sign-in limit through the real Thruster after the curl fix.

## Batch 3: the release pipeline (M2, M3) and SECURITY.md

### Design
- **Workflow:**
  - Every action is pinned by commit, with its version in a comment. The major tags were checked to point at the same commits as v7.0.1, v4.4.1, v4.6.0 and v7.4.0.
  - `persist-credentials: false` on every checkout.
  - The images job outputs both digests. The release job writes `IMAGES` (`ghcr.io/<owner>/houston-*:<tag>@sha256:…`) and fails if a digest is missing. SHA256SUMS covers IMAGES. Values go through `env:`, not inline expressions.
  - The test job also runs `install-bind.sh`.
- **Dependabot:** github-actions only, weekly, grouped, with a 7-day cooldown.
- **Installer:**
  - `release_images` fetches IMAGES and checks it against SHA256SUMS. Each line must be exactly the expected image at `@sha256:<64 hex>`, or nothing is pulled.
  - A release without IMAGES (v0.3.0 and older) is pulled by tag, and the installer says so.
- **Third-party images are pinned by tag and index digest (multi-arch):**
  - cloudflared 2026.9.1
  - Kamal v2.12.0: install.sh and `deploy.KamalImage`, one value checked by a test
  - registry 3.1.2 (was the floating `registry:3`)
  - restic 0.19.1
- **Measured:** Docker 29 runs a `repo:tag@digest` image with Compose `pull_policy: never` after pulling it that way (as the runners do).
- **SECURITY.md:** private reporting, supported versions, and the trust model, including the unauthenticated local registry (L8).
- **Later (named):** build provenance attestations, which need `id-token: write`. Immutable releases, the tag ruleset and 2FA are GitHub settings for Scotty. Kamal's own kamal-proxy image isn't pinned by Houston.

### AC ↔ test
| # | AC | Test |
|---|---|---|
| 1 | Kamal, cloudflared and registry pinned by digest; installer and deploy agree on Kamal | Go `TestThirdPartyImagesArePinnedByDigest` |
| 2 | restic pinned by digest | `storage_location_test` "restic's image is pinned by digest" |
| 3 | pulled by IMAGES' digests; compose names them | `install-version.sh` |
| 4 | IMAGES tampered, naming others, without a digest, or with a malformed one: refused, nothing pulled | `install-version.sh` |
| 5 | a release from before IMAGES: by tag, and said | `install-version.sh` |
| 6 | the workflow has no zizmor findings | `zizmor --offline` (clean) |

### Evidence
- Each test failed first: Go named `REGISTRY_IMAGE="registry:3"`, the restic test named "restic/restic:0.19.1", and install-version got pulls by tag.
- **Suites:** Go ok, gofmt clean. Rails 352 runs, rubocop clean. install-version and install-bind pass. shellcheck has no new findings.
- **Mutation check.** Each of these fails a test:
  - skipping the IMAGES checksum
  - skipping `by_digest`
  - skipping `release_images`
  - a mismatched Kamal between installer and deploy
  - a loose digest pattern. This one survived at first, so the malformed-digest case was added.
- bind-e2e ran on the pinned third-party images and passed.

## Batch 4: the lows, and M4

### Design (each with its evidence)
- **L3:**
  - The deploy reporter masks each secret value of 6+ characters, longest first, as `[secret NAME]` in what goes to Mission Control. Shorter values would hide ordinary text. Multi-line values are already refused at deploy (`CarrierValue`), so there's no per-line masking.
  - An unfinished last line waits for the next report, so a secret split across writes is still masked. The runner's own terminal output is unchanged.
  - `filter_parameters` gains `:log, :error, :code`.
- **L4:** the runner writes the deploy key and known_hosts into `os.MkdirTemp("", "houston-fetch-")` and removes it when fetch returns. It also removes the `<workspace>/.keys` older runners left.
- **L5:** the release hook's env file is `os.CreateTemp("", …)`, private, and removed after. A value can't go through `-e` from the docker CLI's own environment: an app variable named PATH or DOCKER_HOST would change the CLI itself.
- **L6:** `authorize_key` writes houston's own key as `from="127.0.0.1,::1",no-agent-forwarding,no-X11-forwarding`, replaces an older plain line once, and keeps other keys. Port forwarding and pty stay allowed (Kamal).
- **L7:** after the token login, EXIT, INT and TERM traps log out. They're cleared after the normal logout.
- **L8:** documented in SECURITY.md.
- **L9:** `project.GeneratedDir` makes `.houston/…` as plain directories and refuses a symlink or a file. Deploy (`writeKamalFiles`), `houston test` and `houston dev` all use it. `writeFile` removes whatever was at the path and creates the file with O_EXCL, so a committed file symlink isn't followed.
  - **Demonstrated before the fix:** a deploy overwrote an outside `authorized_keys` with its Kamal config.
- **L10:**
  - compose-go runs with `SkipExtends`. When compose-go rejects a file that uses something Houston refuses anyway, Houston's refusal is reported.
  - `readFile` refuses a compose file that's a symlink. Git won't check out a path past a symlinked directory.
- **L11:** `StorageSetup::ABSOLUTE` is anchored with `\z` and excludes control characters.
- **L12:** Add project doesn't show a project's secret once its pushes arrive. Saving still keeps it.
- **M4:**
  - Webhooks count requests that don't verify per client address (30 a minute), checked before the body is read. Verified ones count per project (60 a minute).
  - `PollForChangesJob` runs every 10 minutes, only for projects whose webhook has arrived.
  - `ChangeCheck#run` reloads the project inside its IMMEDIATE transaction, so a webhook and the poll can't queue the same commit twice.

### AC ↔ test
| # | AC | Test |
|---|---|---|
| 1 | secret values masked, longest first; short values kept; split across reports | `TestDeployLogHidesSecretValues`, `TestReporterHidesASecretSplitAcrossReports` |
| 2 | log/error/code filtered | `parameter_filter_test` |
| 3 | the deploy key only during the fetch, outside the workspace; older keys removed | `TestRunnerFetchesTheClaimedCommit` |
| 4 | release.env outside the checkout, private, removed | `TestDeployHappyPath` |
| 5 | authorized_keys restricted, upgraded once, others kept, 600 | `install-ssh-key.sh` |
| 6 | an interrupted token install logs out | `install-version.sh` (TERM mid-pull) |
| 7 | .houston symlinks and files refused, and a file symlink not followed, by deploy, test and dev | `TestGeneratedDirIsAPlainDirectory`, `TestDeployWritesNothingThroughSymlinks`, `TestTest_RefusesASymlinkedHoustonDir` |
| 8 | extends refused without opening the file; a symlinked compose.yml refused | `TestExtendsNeverOpensTheOtherFile` (FIFO), the `extends another file` case, `TestLoadRefusesASymlinkedComposeFile` |
| 9 | storage paths anchored | `storage_setup_test` |
| 10 | a verified secret isn't shown on relink, and Save keeps it | `project_links_test` "linking again doesn't show a verified webhook secret" |
| 11 | junk limited per address without blocking real pushes; verified limited per project | `webhooks_controller_test` (two tests) |
| 12 | polling covers pushed projects only, on schedule; a stale check doesn't queue twice | `poll_for_changes_job_test`, `change_check_test` |

### Evidence
- Every new test failed first for the right reason. Suites: Go ok, gofmt clean, Rails 360 runs, rubocop clean, the three installer tests pass, shellcheck clean.
- The mutation check caught all of these:
  - Go: no hide, no hold-back, masking short values, shortest first, env file in the checkout, following a file symlink, no GeneratedDir check, no symlinked-compose check, no SkipExtends, no key cleanup, no legacy `.keys` removal, dev and test bypassing GeneratedDir
  - Rails: no reload, polling everything, a wrong schedule, no pre-body check, a global key, no verified cap, a shared verified key, junk not counted, the filter reduced, an unanchored regex, the view branch
  - installer: no `from=`, no replacement, no TERM trap, no EXIT trap
- **Found by the mutation check, and fixed:**
  - SkipExtends survived, because Houston's refusal was reported either way. A FIFO test now proves the file is never opened.
  - L12's first version made `ProjectLinking#webhook_secret` return nil on a verified relink, so **Save would have rotated the secret and broken the repo's webhook**. The mutation survived because no test saved a relink. Now the view hides the secret, `webhook_secret` keeps returning it, and the test saves and checks the secret is unchanged.

## Batch 5: the cold review's findings
An independent review of all four commits found one HIGH, two MEDIUM and five LOW issues. All are fixed, test-first, and each fix passed the mutation check.
- **HIGH:**
  - **The problem:** Kamal's working directory is `.houston/kamal` in the checkout. Kamal runs `.kamal/hooks/*` from it and reads `.kamal/secrets-common`. A repo committing those got code run with the Docker socket, houston's SSH key and every secret. The L9 fix refused only symlinks.
  - **Fix:** every deploy removes `.houston/kamal` (RemoveAll doesn't follow symlinks) and writes it fresh.
  - **Test:** `TestKamalSeesOnlyWhatHoustonGenerated`. It failed first: Kamal saw pre-connect, secrets-common and deploy.extra.yml.
- **MEDIUM, cable:** `ApplicationCable::Connection` skipped the tunnel match and expiry. Now `Session.resume(id, tunnel:)` serves pages and the cable both. Test: `connection_test` (3 tests).
- **MEDIUM, lockout:** the global sign-in cap counted every POST, so anyone could lock everyone out. Now it counts only failed attempts, only through the tunnel. The server itself (ssh -L) is never globally capped. Tests: the two cap tests.
- **LOW:**
  - **Puma's scheme:** Puma sets `rack.url_scheme` from X-Forwarded-Proto before middleware. The middleware now resets it (and HTTPS) without Cf-Ray. Test: the `https` test with `rack.url_scheme` set. `sign_in_test` now sends Cf-Ray for its https case, as Cloudflare does.
  - **Webhooks:** junk now counts per address *and* path, so a GitHub repo pointed at another path can't hold back GitHub's deliveries to a real project.
  - **Change checks:** `CheckForChangesJob` has `limits_concurrency to: 1` per project, so an older ls-remote can't switch the queued deploy back.
  - **PortExposure:** an unknown answer (docker failing) isn't cached (`skip_nil`).
  - **Compose path, bigger than reported:** the deploy treats compose.yml's folder as the checkout, so a compose_path through a symlinked folder would move the "checkout" and undo C1's containment. The runner now refuses a compose_path with `..` or with any folder on the way that's a symlink (NO-GO). Mission Control's read is already safe: git won't check out a path under a folder that's a symlink in the tree.
- **Documented:**
  - `HOUSTON_BIND` must be given on every run (README).
  - The runner's own output on the server isn't masked (SECURITY.md).
- **Evidence:**
  - Go ok, gofmt clean. Rails 365 runs, rubocop clean.
  - The mutation check caught all 12, including RemoveAll off, IsLocal off, the symlink walk off, `find_by` in cable, no tunnel match, the global cap on everything, not counting failures, counting successes, no url_scheme reset, a per-IP sender, concurrency 2, and caching nil.

## Batch 6: port 3000 closes by itself after setup (Scotty's direction)
- **Direction:** "it's odd to force a reinstall to close a port… it should be open only until initial setup is complete."
- **Design:**
  - compose.yml binds `${HOUSTON_BIND:-127.0.0.1}:3000:80`, so its default is closed. The installer's `compose()` passes the run's chosen bind: 0.0.0.0 before setup, 127.0.0.1 after.
  - `ClosePortJob` runs every minute. `PortClosing.due?` requires all of these:
    - no `HOUSTON_BIND_CHOSEN`
    - a runner image
    - default storage ready
    - PortExposure open
    - `admin.<base>` answering this install (the probe is nil until connected), so closing never strands the admin
  - `close!` reads its own compose labels (config file, working directory). It starts `houston-close-port` from the runner image (Mission Control's image has no compose plugin), with docker.sock and the install dir mounted read-only. That container waits 5 s, then runs `docker compose up -d --no-deps mission-control` without HOUSTON_BIND, which gives 127.0.0.1.
  - The fixed helper name makes a second run fail harmlessly. compose.yml is never rewritten.
  - The notice no longer says to rerun: it says the port closes by itself. `UpdateNotice.release?` was dead after that change and is removed.
  - The README, docs/agents.md, SECURITY.md and the installer's report are updated.
- **Tests:**
  - `port_closing_test` covers the exact docker run, 7 not-due cases and the schedule.
  - The `projects_controller_test` notice test is updated.
  - `install-bind.sh` covers the compose default, compose run with the chosen bind, and the environment passed on.
  - `bind-e2e.sh` does a real self-recreate, a rerun that stays closed, and a chosen bind that's kept.
- **Mutation check:**
  - Caught: every due? condition, the labels guard, the view guard, the schedule, the compose default, compose() not passing the bind, and HOUSTON_BIND_CHOSEN not passed.
  - `Installation.connected?` survived, because the admin probe already implies it, so it was removed.
- **A flaky test, fixed:** one full-suite run failed once, not reproducible in four seeds. The likely cause is fixed-window counters straddling a boundary, so the four window tests now `freeze_time`.

## Batch 7: port 3000 is a setting, not automatic (Scotty's direction)
- **Direction:**
  - "That sounds almost fragile."
  - "why is it bad for the app to listen on port 3000?" We agreed the risk is real mainly on public servers, where Docker bypasses ufw.
  - "have a houston setting in /settings that shows if it's open or closed and allow the user to close it" / "that's way better".
- **Design:**
  - `installations.port_open` (default true, on existing and new installs) is the saved choice.
  - `PortSwitch.set!(open:)` starts the runner-image helper (`houston-port-3000`) with `-e HOUSTON_BIND=0.0.0.0|127.0.0.1`, which runs `docker compose up -d --no-deps mission-control`. The choice is saved only once the helper started. It's refused, with nothing saved, when there are no compose labels, no runner image, or the helper fails.
  - `PortExposure.address` reports what it's bound to now.
  - **Settings › Port 3000:** a state pill (OPEN/CLOSED, or "can't tell"), Close/Open, and a note about public servers. Closing it from port 3000 redirects to `https://admin.<base>/settings#port` first.
  - **API and CLI:** `GET/PUT /api/v1/port`, and `houston port [open|close] [--json]`.
  - **Installer:** `choose_bind` asks the installed Mission Control: exit 0 means open, 4 means closed, anything else means 127.0.0.1 plus a note. An older Mission Control (no column) counts as open. HOUSTON_BIND overrides for one run.
  - **Removed:** ClosePortJob, the recurring entry, PortClosing's due?, the flight board notice (the flight board is back to its pre-branch state), and HOUSTON_BIND_CHOSEN.
- **Tests:**
  - `port_switch_test`: close, open, 3 refusals, the default.
  - `settings/port_controller_test`: the states, redirecting to admin from port 3000, the tunnel notice, a refusal, signed out.
  - `api_v1_port_test`: show/close, refused, a bad request, no token.
  - The Settings menu test.
  - Go `TestPortShow` and `TestPortOpenAndClose`.
  - `install-bind.sh`: saved open, saved closed, can't tell, and overrides.
  - `bind-e2e.sh`: a real close, a rerun that keeps it, a real open, a rerun that keeps it, a one-run override. BIND E2E PASS.
- **Mutation check:** all 10 guards caught (Rails 8, installer 2).
- **Visual check:** the dev Settings page with the real OPEN state, at desktop width and at 375 px, with no overflow.
- **Suites:** Go ok, gofmt clean, Rails 376 runs, rubocop clean, the three installer tests pass.

## Release (v0.4.0, 2026-09-25)
- **History:** before pushing, the unpushed history was tidied. The port-3000 auto-close commit was folded into the Settings one, and the headers commit was retitled. The final tree was checked identical (`git diff`: 0 lines).
- **Workflow:** tag v0.4.0 went green in 1m22s with the pinned actions. The release's `IMAGES` checks against its SHA256SUMS, and both digests match ghcr.io.
- **The server updated** with the usual `curl …/install.sh | sudo sh`, idle beforehand (0 deploys, 0 backups in flight).
  - It kept port 3000 open, as promised: v0.3.0 had no saved choice, which counts as open.
  - Every service runs by digest: Mission Control and both runners at v0.4.0, cloudflared 2026.9.1, registry 3.1.2. Both runners' CLIs say v0.4.0.
  - `PortExposure.address` is 0.0.0.0 and `port_open` is true. Mission Control finds cloudflared at 172.19.0.2.
- **Checks after the update:**
  - equip, valleybuiltcrossfit, estherpictures(.svnmns.com) and estherpictures.com each answered 10/10.
  - admin.svnmns.com/up answers 200, and so does http://192.168.0.56:3000/up.
  - The laptop CLI v0.4.0 (checksum OK) shows `houston status` and `houston port` (OPEN).

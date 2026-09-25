# Houston security audit, 2026-09-24

Published with the fixes, in v0.4.0: [security-fixes.md](security-fixes.md) says how each was fixed and tested.

Scope: the repo at 9788937..b934207 (v0.3.0). Automated: brakeman, bundler-audit, importmap audit, govulncheck, gitleaks (history), zizmor (workflow). Manual: four read-only reviews (sign-in and access; running commands; secrets and data; installer, host and release pipeline). The critical and high findings were re-verified against the code before this report.

## Automated
- **No known vulnerabilities** in the gems, the JavaScript packages or the Go modules. **No secrets** in the git history.
- **brakeman:** one medium warning, `permit!` on Add project's secrets. It's **not exploitable**: the values are sliced to the compose file's own variable names.
- **zizmor:** 7 actions pinned by tag (high), and 3 `actions/checkout` steps that keep credentials (low).

## Findings, by severity

### Critical
**C1. The build context and Dockerfile path can leave the checkout.** Who: anyone who can push to a linked repo.
- **Where:** `internal/deploy/deploy.go:490-508` joins `build.context` and `build.dockerfile` with no containment. `project.go`'s key allowlist allows `build:` without checking what's inside it, and compose-go runs with `ResolvePaths=false`.
- **What it allows:** a pushed compose file can set `context: /home/houston/.ssh`, or `../..`, or a symlink, and copy the runner's SSH key or other projects' deploy keys into an image. The houston key is in `authorized_keys`, and houston is in the docker group, so that means **root on the host**. `build.additional_contexts`, `build.ssh` and `build.network` are unchecked too. The test step (`compose run --build`) is exposed the same way.
- **Fix:**
  - Under `build:`, allow only a relative `context` and `dockerfile`, plus `target` and `args`.
  - Resolve symlinks, and refuse anything outside the checkout. Do the same in the test override.

### High
**H1. A repo can sync and deploy as another project.** Who: a repo pusher.
- **Where:** `api/projects_controller.rb#sync` then `ProjectSync#save!` does `Project.find_or_initialize_by(name: payload["name"])`. Only restores check the claimed job against the name.
- **What it allows:** pushing `name: victim` rewrites victim's config, deletes victim's dropped domain records, reads victim's secrets through the runner's secrets endpoint, and deploys the attacker's image as victim.
- **Fix:**
  - The runner passes the claimed deploy's ID and token, and the server requires `deploy.project.name == payload.name`, for syncs and for secret reads.
  - The runner also checks the compose `name:` against the job before it starts.

**H2. The runner token leaks into test builds.** Who: a repo pusher.
- **Where:** `deploy.go:347` runs `houston test` with the runner's environment (`env=nil`), which includes `HOUSTON_TOKEN`. `testEnv` strips only `${…}`-referenced names, so a bare `build.args: [HOUSTON_TOKEN]` is filled in from the environment.
- **What it allows:** with the token, `/api/runner/jobs/claim` hands out other projects' deploy keys, `/api/projects/*/secrets/*` hands out their secrets, and sync can rewrite any project, all over port 3000.
- **Fix:**
  - Give `houston test` an allow-listed environment: PATH, HOME and DOCKER_*.
  - Refuse bare build args.
  - Later, use per-deploy tokens instead of one all-powerful runner token.

**H3 (high on a public-IP VPS, medium on a LAN). Mission Control stays open on 0.0.0.0:3000 after setup, over plain HTTP.**
- **Where:** `install/install.sh:379` (`"3000:80"`). `force_ssl` and `assume_ssl` are off. Sessions are `cookies.signed.permanent` (20 years), and a session made on the LAN also works at `admin.<base>`.
- **What it allows:**
  - On a LAN, sniffing the password or cookie.
  - On a VPS, the whole admin is internet-facing over HTTP. Docker-published ports skip ufw.
- **Docs:** the README claims "a Cloudflare Tunnel is the only way in".
- **Fix:**
  - Bind to 127.0.0.1 after setup (`HOUSTON_BIND`), and reach it with `ssh -L` when needed.
  - Refuse sessions created over HTTP on HTTPS requests.
  - Correct the README.

### Medium
- **M1. Sign-in and setup rate limits are keyed on `request.remote_ip`.** From a private address, `X-Forwarded-For` spoofs it, so a LAN host or any app container gets unlimited guesses. There's no global cap. Fix: key on `Cf-Connecting-Ip` when `Cf-Ray` is present and on `remote_addr` otherwise, add a global failed-sign-in cap, and set `trusted_proxies` to the Docker network.
- **M2. The release workflow isn't hardened.** Actions are pinned by tag in jobs with `packages: write` and `contents: write`, and checkout persists credentials. Fix: pin every action by SHA, set `persist-credentials: false`, and add Dependabot for `github-actions`.
- **M3. Image and binary authenticity.** Houston's and third-party images (kamal, cloudflared, registry:3, restic) are pulled by tag, and `SHA256SUMS` proves integrity only. Proportionate fixes:
  - Digest-pin third-party images.
  - Publish image digests in the release and pull by digest.
  - Turn on immutable releases and a tag ruleset (settings in GitHub).
  - Add build provenance attestations.
  - Hardware-key 2FA on the account.
- **M4. Anyone on the internet can block push-to-deploy.** The webhook rate limit is keyed by project name and counts unverified requests. More than 30 junk POSTs a minute gets real pushes a 429, and nothing polls as a fallback. Fix: limit by IP before verifying, don't count verified requests, and poll refs occasionally.

### Low
- **L1. `hooks.<base>` constraints trust `X-Forwarded-Host`, and `config.hosts` is unset.** Admin routes can be reached on the hooks or app hostnames, which gets around Access or WAF rules on `admin.<base>`. A session or token is still needed. Also DNS rebinding on port 3000. Fix: match on the raw Host header, and set `config.hosts`.
- **L2. Sessions never expire, and can't be listed or revoked.** Fix: idle and absolute timeouts, and a sessions list in Settings.
- **L3. Hook output can put secret values into deploy logs,** which API tokens can read. Also `log` isn't in `filter_parameters`. Fix: the runner masks secret values, and add `:log` and `:error` to the filter.
- **L4. Deploy keys stay on disk** in the runner workspace, for every project ever fetched. Fix: use a temp dir and remove it after the fetch.
- **L5. `release.env`** (all secret values) can outlive a crash inside the checkout. Fix: pipe it on stdin, or write it outside the checkout.
- **L6. The houston SSH key has no restrictions,** though Kamal only uses it on localhost. Fix: `from="127.0.0.1,::1",no-agent-forwarding,…`.
- **L7. The installer's token path:** an interrupted install leaves the token in `/root/.docker/config.json`. The repo is public now, so drop the token path, or add a `trap`.
- **L8. The local registry has no auth** (`127.0.0.1:5000`). Fine on one server; document it.
- **L9. Generated files can be written through a `.houston` symlink the repo commits.** Fix: `Lstat`, and refuse it.
- **L10. compose-go follows `extends:`** before Houston rejects it (`SkipExtends` isn't set). A compose file that's a symlink is read in Mission Control. Fix: set `SkipExtends`, and refuse a symlinked compose path.
- **L11. The storage path regex** isn't `\z`-anchored (hardening; only admins can set it).
- **L12. Add project shows an already-verified webhook secret** when a repo is re-linked (admin only). Fix: hide it once it's verified.

### Info
- `setup[code]` isn't in `filter_parameters`. Fix: add `:code`.
- There's no CSP. `X-Frame-Options` defaults cover clickjacking.
- API tokens have no scopes or expiry: a token is effectively an admin.
- There's no `SECURITY.md` and no Dependabot. Fix: turn on private vulnerability reporting and add a `SECURITY.md`.
- The public tree names production details (hostnames, the NFS address) in the plans and tests. That's reconnaissance value only.
- **Trust model to document:** anyone who can push to a linked repo's deploy branch runs code on the host's Docker. Once C1 and H1–H2 are fixed, that should be contained to *their* project, but it's still code on the server.

## Checked and fine (selected)
- **Passwords and setup:** bcrypt passwords with constant-time lookup, no session fixation, CSRF on. The setup code is 40 bits, bcrypt-hashed, single-use and closed after setup.
- **API tokens:** 256 bits, stored as SHA-256.
- **Runner API:** `secure_compare` on a token of 32 or more characters, and it refuses tunnel traffic.
- **What APIs return:** no endpoint returns a secret, deploy key, Cloudflare token or storage credential. The webhook secret is shown only until the first verified push. Webhook HMAC uses `secure_compare`.
- **Encryption:** all sensitive columns are encrypted. `.env` is 0600, with 48 random bytes per key.
- **Running commands:** every Ruby process call uses argv (no shell strings). Git uses `--` and `GIT_ALLOW_PROTOCOL=ssh:https`, with strict host-key checking on the runner. Restic credentials go through the environment, never argv.
- **XSS:** the only `html_safe` uses are the static SVG and a project's own maintenance HTML (with escaped placeholders), which is never served on admin or hooks. Deploy logs are escaped.
- **The installer's token handling:** only on stdin, and tested. There are no privileged containers.

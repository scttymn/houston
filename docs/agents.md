# Setting up an app with Houston: a guide for agents

You're an AI agent helping someone deploy their app with Houston. This page is the order of work, what to check at each step, and where to stop and hand over to the human. The [README](../README.md) has the same material as a narrative for people. To work on Houston's own code, read [AGENTS.md](../AGENTS.md).

Houston deploys apps described by a Docker Compose file with an `x-houston` block. Everything goes through one CLI, `houston`, which works locally and, with `--server`, against the person's Houston server (Mission Control).

## Ground rules
- **Stop and ask the human** at every step marked **HUMAN** below. Those need their accounts, their judgment, or a secret you shouldn't handle.
- **Never put a secret on a command line, in a file you commit, or in your output.** Secrets go to `houston secrets set NAME` on stdin. `houston secrets generate NAME` makes a random one nobody sees. If a command prints a secret (`houston webhook` shows the webhook secret until the first push), pass it straight on, and don't repeat it.
- **Don't touch DNS records** that Houston didn't create. Houston marks its own with `managed-by:houston`, and refuses the rest. If a domain has an old record, the human removes it.
- **Deploys to a live app, restores and maintenance mode are the human's call.** Say what you're about to do and wait for a yes. `houston restore` also needs `--confirm <project name>`.
- **Check, don't assume.** Every step below has a check. Exit codes: `0` success, `1` the work failed, `2` bad flags or a bad compose file. `--follow` commands exit `0` on GO and `1` on NO-GO. `status`, `deploys`, `snapshots` and `inspect` take `--json`.

## 0. Find out where things stand
```sh
houston --version           # is the CLI installed?
houston status              # logged in? which server and version? which projects exist?
ls compose.yml Dockerfile   # in the app's repo: is it set up for Houston already?
```
- **No CLI:** go to step 2.
- **"not logged in to a server":** go to step 2's login.
- **No server at all:** step 1.
- **The app is already listed in `houston status`:** skip to step 5, or step 6 to add a domain.

## 1. The server (only if there isn't one)
**HUMAN:** they need a Linux server (apt, dnf or pacman), a Cloudflare account with a domain, and somewhere for backups. On the server, as root:
```sh
curl -fsSL https://github.com/scttymn/houston/releases/latest/download/install.sh | sudo sh
```
- That installs the latest release (`sudo HOUSTON_VERSION=<tag> sh` pins one). The installer checks everything before changing anything, and ends with a URL and a setup code.
- **HUMAN: first run, in the browser:**
  1. The admin login.
  2. A Cloudflare API token (Account › Cloudflare Tunnel › Edit, and Zone › DNS › Edit on the domain).
  3. Backup storage, and a restic password shown once, which they must save.
- **Check:** `https://admin.<their domain>/up` answers 200.
- Use `https://admin.<domain>` from here on. Port 3000 stays open to the network (plain HTTP) until someone closes it. **HUMAN:** on a server with a public address, they should: Settings › Port 3000, or `houston port close` with their OK. Mission Control restarts for a few seconds.

## 2. The CLI, and logging in
Download the CLI (`darwin` or `linux`, `arm64` or `amd64`), and check its checksum. This is the latest release. If the server is pinned to an older one, use `releases/download/<tag>/…` for both files, and match `houston status`'s version.
```sh
curl -fsSLo houston https://github.com/scttymn/houston/releases/latest/download/houston-darwin-arm64
curl -fsSL https://github.com/scttymn/houston/releases/latest/download/SHA256SUMS | grep " houston-darwin-arm64$" | shasum -a 256 -c
install -m 755 houston ~/.local/bin/houston
```
- **HUMAN:** they create an API token in Mission Control (Settings › API tokens), then run `houston login https://admin.<domain>` themselves and paste it. The input is hidden.
- In CI or an agent sandbox, `HOUSTON_SERVER` and `HOUSTON_API_TOKEN` in the environment work instead of logging in. Let the human set them.
- **Check:** `houston status` prints "Houston <version> at <domain>".

## 3. Prepare the app (in its repo)
```sh
houston init
```
`init` adds only what's missing: Dockerfile stages `dev`, `test` and `production`, a `compose.yml` with an `x-houston` block, `.dockerignore` entries, and a `.env` for local secrets. Then **edit its defaults**. Houston knows no frameworks, so this is where the app's own knowledge goes:

- **The project's name:** `compose.yml`'s top-level `name:`. `init` takes it from the folder name, so fix it. It becomes `<name>.<domain>`, and can't be `admin` or `hooks`.
- **The Dockerfile:**
  - `dev` gets the dev toolchain and a command serving the mounted code, bound to `0.0.0.0` (a server bound to 127.0.0.1 is unreachable from outside its container).
  - `test` copies the code and has what the tests need.
  - `production` is the existing final stage.
  - Keep compiled dependencies out of the mounted folder.
- **The `app` service:**
  - The published port: check the Dockerfile's `EXPOSE`, since `init` may guess wrong. If the laptop's port is taken, publish another (`127.0.0.1:4001:4000`).
  - The variables: `${NAME}` when the server must have it, `${NAME:-}` when dev can do without.
  - Non-secret settings as literals (a canonical host, for example).
  - A named volume for every path holding data (databases, uploads).
- **`x-houston`:**
  - `health`: a path answering 200 with no login and no database. Add a tiny `/up` route if the app has none, and make it answer on any host, ahead of any redirect.
  - `commands.test`, `commands.console`.
  - `hooks.release`: migrations, run before traffic switches.
  - `app_port`, if production listens elsewhere than dev.
- **`.env`:** `init` creates it with **blank** values. Fill in local values: generate random ones, and don't ask the human to paste secrets into chat. `.env` is gitignored, and it's for local runs only.

Then check it the way the server will run it:
```sh
houston inspect                # what Houston reads; exit 2 names any problem in compose.yml
houston test                   # exits with the test command's code
houston dev --production       # the real production image, locally
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:<published port><health path>   # must be 200
```
Stop `houston dev --production` afterwards (`docker compose -p <name>-production down`). Before committing, **read "Things that bit us"** below, and check each one.

Commit the changes on a branch, and open a PR for the human to review and merge.

## 4. Link it to the server
**HUMAN** merges the PR first, since Houston reads the repo's default branch. Then:
```sh
houston link <repo git URL> --wait
```
- `link` prints a **read-only deploy key**. **HUMAN** adds it to the repo (GitHub: Settings › Deploy keys, read-only), or you do it with their OK: `gh repo deploy-key add <file> --repo <owner/repo>`. Public repos can skip the key. `--wait` carries on once Houston can read the repo.
- **The webhook:** `houston webhook` prints the URL and a secret. Add both to the repo's webhook settings, sending push events as JSON. With the human's OK you can do it with `gh api`, passing the secret on stdin (never as an argument). GitHub's first delivery, a ping, should answer 2xx.
- **Secrets:**
  ```sh
  houston secrets list --project <name>
  houston secrets set NAME --project <name> < file    # or from a pipe; never an argument
  houston secrets generate NAME --project <name>      # passwords and keys nobody needs to see
  ```
  Required secrets without a value hold deploys (HOLD). Ask the human for values only they have (API keys for other services), and have them set those themselves.

## 5. Deploy
With the human's OK:
```sh
houston deploy --server --follow --project <name>   # exit 0 on GO, 1 on NO-GO
houston status --project <name>                     # GO, the running commit, domains, the last backup
houston logs --server --project <name>
```
- After this, every push to the deploy branch deploys by itself.
- **A deploy:** a snapshot of the running version, the image built, the release hook, then the switch, only once the new version answers its health check. So a NO-GO leaves the old version serving.
- **After a NO-GO:** `houston deploys show --project <name>` prints the steps and the log. Fix it, push, and it deploys again.
- **Check:** `https://<name>.<domain><health path>` answers 200.

## 6. A custom domain (optional)
1. Add it to `x-houston.domains`, for example `[example.com, www.example.com]`. The app should redirect www to the apex itself, if that's what they want.
2. **HUMAN, before deploying:**
   - The domain's zone must be in the same Cloudflare account.
   - Houston's Cloudflare token needs Zone › DNS › Edit on that zone. A token scoped to the base domain only can see the zone but not change it. The human widens it in Cloudflare, or makes a new one and sets it with `houston cloudflare token` (stdin), which checks every zone before replacing the old token.
   - If the domain already has records pointing elsewhere (the old host), the human deletes them when they're ready to switch. Houston won't.
3. Push. `houston status` shows each domain's state:
   - `DNS OK`: pointed at Houston.
   - `DNS PENDING`: the zone's nameservers aren't on Cloudflare yet.
   - `AFTER FIRST GO`: pointed once the first deploy is GO.
   - `ZONE NOT IN CLOUDFLARE YET`.
   - `NO-GO`, with the reason, for example a record Houston didn't create.
   - `CAN'T CHECK`: Cloudflare refused or failed, often because the token lacks access to that zone.
   - `WILDCARD`: a name under the base domain, served by Houston's `*.<base>` record.

## 7. Moving an existing app's data in (optional)
There's no import command yet. The steps that worked, each with the human's OK:
1. `houston backup --follow`: a safety snapshot.
2. `houston maintenance on`: the app's names show a maintenance page.
3. Stop the old app. Copy its data into the Houston app's volume on the server: SQLite with `sqlite3 .backup` (never a live file copy), and uploads as they are, owned by the uid the app runs as.
4. Compare row counts and run `PRAGMA integrity_check` on both sides. Then start the app, and turn `houston maintenance off`.
5. `houston backup --follow` again, a snapshot with the real data.

## Things that bit us (check these)
- **`houston test` "passes" with no tests:** a `.dockerignore` excluding `test/` keeps the tests out of the test image. Check the test output says tests ran.
- **Flaky tests fail deploys at random,** because every deploy runs them. Run `houston test` several times, and look for tests sharing files or leaving state behind.
- **Phoenix:**
  - Behind Cloudflare, `check_origin: :conn` refuses every browser, since the app sees http on its own port. List the hosts instead: `check_origin: ["//example.com", "//<name>.<domain>"]`.
  - `SECRET_KEY_BASE` must be at least 64 bytes.
- **Rails:** `RAILS_MASTER_KEY` should be `${RAILS_MASTER_KEY:-}`, optional, because `houston test` gives required variables random values, and Rails can't decrypt with a random key. Set it on the server before the first deploy.
- **The health path must not redirect.** Put it ahead of www or HTTPS redirects, since the check hits the container directly over http.
- **Ports:** `init` may guess the wrong port, and the laptop's port may already be taken. Publishing another locally changes nothing on the server.
- **A gem that needs a system library** (like `ruby-vips`) may break CI jobs that just boot the app. `require: false` lets the framework load it lazily.

## Reference
- **Commands:** `houston --help`, and `houston <command> --help` for any command.
- **Exit codes:** `0` done, `1` failed, `2` usage or a bad compose file. `--follow`: `0` GO, `1` NO-GO.
- **Machine-readable:** `--json` on `status`, `deploys`, `deploys show`, `snapshots`, `inspect` and `cloudflare`.
- **Cloudflare:** `houston cloudflare` (the tunnel, its routes, Houston's records), and `houston cloudflare repair` after anyone changed the tunnel or records by hand (exit 1 if something couldn't be put back).
- **Port 3000:** `houston port` says whether Mission Control's port 3000 is open to the network (`--json`); `houston port close` and `houston port open` change it, with the human's OK (Mission Control restarts for a few seconds).
- **Status words:** GO, QUEUED, IN FLIGHT, NO-GO (the previous version still serves), HOLD (something blocks the next deploy, usually a secret with no value), STANDBY (linked, never deployed).

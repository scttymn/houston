<p align="center"><img src="mission_control/app/assets/images/patch.svg" width="280" alt="Houston Mission Control"></p>

# Houston

A small self-hosted deploy orchestrator. Each project is one Docker Compose file with an `x-houston` block. One CLI works the same on your laptop and against the server. A web admin, **Mission Control**, handles Cloudflare, secrets, deploys, backups and restores.

Houston ties proven tools together rather than reinventing them: Docker runs everything, Compose describes the app, Kamal deploys it, restic backs it up, and a Cloudflare Tunnel is how the internet reaches it. It knows no frameworks. A static site, Rails, Phoenix or anything else that builds a Docker image works the same way.

- [`docs/agents.md`](docs/agents.md): for an AI agent setting up an app with Houston (the steps, the checks, and where to stop for you). Agents working on Houston itself read [`AGENTS.md`](AGENTS.md).
- [`docs/plans/`](docs/plans): how each part was built, with what was tested and found.
- [Releases](https://github.com/scttymn/houston/releases): the CLI for macOS and Linux, and the installer. Mission Control's and the runner's images are on ghcr.io, and the installer pulls them by the digests each release lists.
- [`SECURITY.md`](SECURITY.md): how to report a vulnerability, and what Houston trusts.

## What you need

- **A Linux server that installs packages with apt, dnf or pacman:** Debian, Ubuntu and their derivatives; Fedora and the RHEL family (RHEL, Rocky, Alma, CentOS Stream); Arch and its derivatives. The installer adds what's missing (Docker, git, an SSH server) with that package manager. SELinux must be permissive for now.
- **An address your browser can reach** for first-run setup, before the Cloudflare tunnel exists: on your LAN, over a VPN, or a VPS's public IP (the setup code guards the page). After setup, everything goes through Cloudflare, and rerunning the installer closes that address.
- **A Cloudflare account with a domain** (the *base domain*). Apps are served at `<name>.<base>`, and Mission Control at `admin.<base>`.
- **Docker with Compose on your laptop.** That's all the CLI needs.
- **Somewhere for backups:** a path on the server, an NFS export, or S3, B2 or SFTP.

## 1. Install the server

```sh
curl -fsSL https://github.com/scttymn/houston/releases/latest/download/install.sh | sudo sh
```

That installs the latest release. To pin one from [Releases](https://github.com/scttymn/houston/releases), use `sudo HOUSTON_VERSION=<tag> sh`.

The installer:
- checks for apt, dnf or pacman (and stops, changing nothing, if there's none, or if SELinux is enforcing)
- installs Docker, git and an SSH server if they're missing, and starts them. Docker comes from Docker's own repository on apt and dnf, and from Arch's packages on pacman
- creates a `houston` user, and checks it can SSH to the server (Kamal deploys over SSH)
- generates Mission Control's secrets in `/opt/houston/.env`, which you must keep: losing them locks away every encrypted secret
- pulls that version's Mission Control and runner images, and installs its CLI, checked against the release's `SHA256SUMS`. It checks the version exists before changing anything
- starts everything from `/opt/houston/compose.yml`: Mission Control, a registry on `localhost:5000`, and two runners

It ends with a URL and a one-time **setup code**:

```
Houston vX.Y.Z is running.
Finish setup at  http://<server address>:3000
Setup code       XXXX-XXXX
```

**To update,** run the same command again: it installs the latest release (or the `HOUSTON_VERSION` you give). Rerunning the installer also repairs, and it keeps the secrets. It restarts the runners so they use the new CLI; a deploy in flight at that moment is abandoned (NO-GO, the old version keeps serving). `HOUSTON_RUNNERS=3` changes how many runners there are.

The flight board's header and `houston status` say which version is running. Mission Control checks GitHub for a newer release every 6 hours. When one is out, "vX.Y.Z available" appears beside the version, and `houston status` adds "(vX.Y.Z available)". Nothing updates by itself.

**Or update from anywhere:** in **Settings › Houston**, **Check for updates** asks GitHub now (`houston update --check`), and **Update to vX.Y.Z** updates (`houston update`, which follows it to the end): Mission Control runs that release's installer on the server for you. Each update is listed there, with a log page like a deploy's: its steps and its log. It waits until no deploy, restore or backup is running, and holds new ones until it's done. Mission Control restarts on the way, then the board shows the new version (or, if it failed, a red "Update to vX.Y.Z failed" beside it that leads to the log). If the new version doesn't install, the server goes back to the one it ran. The first version with the button has to be installed with the command above.

**From a checkout** (working on Houston): `sudo HOUSTON_SOURCE=/path/to/houston sh install/install.sh` builds everything there, instead of pulling a release. The flight board then says `SOURCE <commit>`. Set one of `HOUSTON_VERSION` and `HOUSTON_SOURCE`, not both.

## 2. First run (in the browser)

Open the URL. The three steps:

1. **Create the admin login** with the setup code.
2. **Connect Cloudflare:** the base domain, and an API token with **Account · Cloudflare Tunnel · Edit** and **Zone · DNS · Edit** on that domain. Houston makes a tunnel with these routes:
   - `admin.<base>` → Mission Control
   - `hooks.<base>` → Mission Control, webhook paths only
   - everything else → your apps

   For the apps' names, it makes a proxied `*.<base>` record commented `managed-by:houston`. If another server already has `*.<base>`, it leaves that alone and gives each app its own `<name>.<base>` record at its first deploy instead. Then it connects the server. It never touches a DNS record without that comment.
3. **Default backup storage:** a location, a test write, and a restic password shown **once**. Save the password before you continue: without it the backups can't be read.

Mission Control is then at `https://admin.<base>`. Cloudflare Access in front of it is optional and recommended. Keep `hooks.<base>` outside Access.

**Port 3000 stays open until you close it.** It's plain HTTP, open to your network, which is handy at home and risky on a server with a public address: Docker publishes it straight past the server's firewall. **Settings › Port 3000** shows whether it's open, and closes or opens it (Mission Control restarts for a few seconds; so does `houston port close` or `houston port open`). Closed, it's bound to `127.0.0.1`, so Mission Control answers only at `admin.<base>` and on the server itself. Updates keep your choice. If you need Mission Control without Cloudflare while it's closed, go through SSH:

```sh
ssh -L 3000:127.0.0.1:3000 you@<server>    # then open http://localhost:3000
```

`HOUSTON_BIND=<IPv4 address>` on the installer chooses where port 3000 listens for that run, overriding the saved choice: a VPN address, for example. Give it on every run. Sign-ins end after two weeks unused, and after 30 days at most. A sign-in made on port 3000 doesn't work at `admin.<base>`, and the other way round.

## 3. Install the CLI (your laptop)

Download the latest (`darwin` or `linux`, `arm64` or `amd64`). For a pinned server, use the same release: `releases/download/<tag>/…`.

```sh
curl -fsSLo ~/.local/bin/houston https://github.com/scttymn/houston/releases/latest/download/houston-darwin-arm64
chmod +x ~/.local/bin/houston && ln -sf houston ~/.local/bin/hou
houston --version
```

From a checkout, `bin/install` builds it for this machine into `~/.local/bin` instead, and `bin/release` builds all four into `dist/`.

Then make an API token in Mission Control (**Settings › API tokens**) and log in:

```sh
houston login https://admin.<base>      # asks for the token (hidden)
```

If Cloudflare Access guards `admin.<base>`, add `--access-client-id` and `--access-client-secret` (an Access service token). For `houston console --server`, add `--ssh houston@<server address>`.

## 4. Set up a project

In the project's folder:

```sh
houston init
```

`init` adds what Houston needs and changes nothing that's already there:

| File | When it's missing | When it's there |
|---|---|---|
| `Dockerfile` (the one `build:` names) | Stages `base`, `dev`, `test` and `production`, serving the folder as a static site on port 8080 | An unnamed final stage is named `production`, and a missing `production`, `dev` or `test` is added, each starting as a copy of production |
| `compose.yml` | One `app` service built from the Dockerfile, exposing the port it listens on (`expose: ["8080"]`, no host port), the folder mounted for dev, and an `x-houston` block | A missing `x-houston` block is added; an existing one gets only the required keys it lacks |
| `.dockerignore` | `.git`, `.env`, `.houston` and the build files | Whichever of `.git`, `.env` and `.houston` it doesn't already exclude |
| `.env` / `.gitignore` | One line per variable the compose file references / `/.env` | Missing lines added |

A static site deploys as `init` writes it. For anything else, **edit the defaults for your project**:

- **The `dev` and `test` stages:** your dev tools and dev dependencies, and a command that serves the code `houston dev` mounts. Keep compiled dependencies out of the mounted folder.
- **The `app` service:**
  - the port it listens on in its container (`expose`), with no host port: `houston dev` serves it by name
  - the variables the app needs (`${NAME}` when the server must have it, `${NAME:-}` when dev can do without)
  - named volumes for data
  - other services (Postgres, Redis) with their images and volumes
- **`x-houston`:**
  - `health`: a path that answers 200 without a login
  - `app_port`: the port production listens on, when it isn't the one in `expose`
  - `commands.console`, `commands.test`
  - `hooks.release` (migrations, run before traffic switches)
  - `domains`, `deploy` (which branch or tag deploys), `backups`
- **Service hostnames:** write `${DB_HOST:-db}` wherever the app names a service. Plain Compose uses `db`; on the server, Houston sets it to that service's container.
- **Resource limits:** `deploy: { resources: { limits: { cpus: "2", memory: 2g } } }` on any service (the app or a database) caps it on the server as in `houston dev`, so apps sharing a server can't starve each other. Changing a database's limits restarts its container once at the next deploy; its data is kept.

Then check it the way the server will run it:

```sh
houston dev                  # the dev stage, code mounted, at http://<name>.localhost
houston test                 # commands.test in a throwaway copy
houston dev --production     # the production image, locally, at http://<name>-production.localhost
houston init                 # again: "already set up" when nothing's missing
```

**No ports in dev.** `houston dev` runs one small proxy container, `houston-dev-proxy` (kamal-proxy, as on the server), on port 80. Each `houston dev` registers its app there by name, so any number of projects run side by side with no host ports to pick or collide.
- **Branches get their own copy.** On another branch (a git worktree, say), the app is at `http://<branch>.<name>.localhost`, as its own Compose project with its own volumes. On its first run, those start as a copy of main's data, taken with main paused for the seconds the copy lasts, so nothing a branch does touches main. `houston dev --fresh` copies main's data again. `--as <name>` names an instance yourself.
- **A branch's name has two levels**, and some frameworks' development host checks allow only one under `.localhost` (Rails, for one). If the app refuses it, `houston dev` says so; allow `.<name>.localhost` in the app's development settings. For Rails: `config.hosts << ".<name>.localhost"`.
- `HOUSTON_DEV_PORT=8080` moves the proxy off port 80 (the app is then at `<name>.localhost:8080`), and `--ports` also publishes `compose.yml`'s `ports`, for a tool that needs one.

`docker compose up` still works on its own, since the file is plain Compose.

**What Houston requires of a project:**
- a Dockerfile with `dev`, `test` and `production` stages
- a `compose.yml` with a top-level `name` (a DNS label; not `admin` or `hooks`), exactly one service with `build:`, and an `x-houston` block with `health`
- an app that listens on one port and answers 200 on the health path

`houston inspect` shows what Houston reads from the file.

## 5. Add it to Houston

Push the project to any git host that can send a webhook, then:

```sh
houston link git@github.com:you/app.git --wait
```

`link` prints a **read-only deploy key**. Add it to the repo, and `--wait` carries on once Houston can read the repo. `link` then reads `compose.yml`, lists the secrets it needs, and saves the project. It prints the **webhook URL** (`https://hooks.<base>/<name>`) and a **secret**, which is shown until the first push arrives. Add both to the repo's webhook settings, sending push events as JSON.

The same flow is **Add project** in Mission Control.

Set the secrets. Values are write-only and never taken from the command line:

```sh
houston secrets list --project app
houston secrets set RAILS_MASTER_KEY --project app < config/master.key    # stdin, or a hidden prompt
houston secrets generate POSTGRES_PASSWORD --project app                 # a random value nobody sees
```

Then deploy:

```sh
houston deploy --server --follow --project app
```

## Day to day

- **Push to deploy.** A push rings the webhook. Mission Control reads the repo's refs itself, and a runner fetches the commit, runs `commands.test` (step 00), then deploys. Once a project's pushes arrive, Mission Control also checks its refs every 10 minutes, so a lost webhook only delays a deploy. Traffic moves only once the new version passes its health check, so a failed deploy leaves the old version serving.
- **A deploy:**
  1. a snapshot of the running version (data and code)
  2. the image built at the commit
  3. accessories booted
  4. the release hook
  5. the switch
  6. the `post_deploy` hook
- **What's running:** `houston status`, `houston deploys --project app`, `houston logs --server`, `houston console --server`.
- **By hand on the server:** as the `houston` user, `houston deploy` in a clean checkout.
- **Status words:** GO, QUEUED, IN FLIGHT, NO-GO (the previous version still serves), HOLD (something blocks the next deploy, such as a secret with no value), STANDBY (linked, never deployed).

## Custom domains

Every app is served at `<name>.<base>`. To serve it on its own domain too, list it in the compose file:

```yaml
x-houston:
  domains: [example.com, www.example.com]
```

On the next deploy, Houston points each one at its tunnel with a proxied CNAME (an apex works through Cloudflare's CNAME flattening), commented `managed-by:houston project:<name>`. For that:
- **The domain's zone must be in the same Cloudflare account.**
- **Houston's Cloudflare token needs Zone › DNS › Edit on that zone,** not only on the base domain. With a token scoped to the base domain alone, Houston can see the zone but not change it, and the domain shows CAN'T CHECK. Widen the token in Cloudflare, or give Houston a new one in **Settings › Cloudflare** (or `houston cloudflare token`, from stdin): it's checked against every zone before it replaces the old one.
- **An existing record for the name** (the old host's) is never overwritten: the domain shows NO-GO until you delete that record in Cloudflare, then deploy again. So you choose the moment it switches.

`houston status --project app` shows each domain's state: DNS OK, DNS PENDING (the nameservers aren't on Cloudflare yet), ZONE NOT IN CLOUDFLARE YET, or NO-GO with the reason. Redirecting `www` to the apex is up to the app.

## Backups and restore

- **Volumes and storage:**
  - Where a volume lives is chosen per project: `houston volumes place <volume> <location> --project app` (or `--local-disk`, the default) before its first deploy, or on the project page.
  - Storage locations are added in **Settings › Storage**.
  - `houston storage use <location> --project app` picks where a project backs up.
- **What's backed up:** the app's named volumes, SQLite databases found in them (copied safely while open), and every Postgres database (dumped). Other services' data isn't backed up, and the project page says so.
- **When:** daily at 03:00 (`x-houston.backups`), before every deploy, and on demand:

```sh
houston backup --follow --project app
houston snapshots --project app
houston restore <snapshot> --confirm app --follow
```

- **A restore puts back code and data together, with no downtime:**
  1. the snapshot's commit and data are prepared beside the running version
  2. a safety snapshot of the running version is taken (the way back)
  3. traffic switches
  4. the old data is removed

  A failed restore leaves the running version untouched.

## Cloudflare

**Settings › Cloudflare** shows the tunnel and each of its connections (the data centre, cloudflared's version, since when), the tunnel's live routes (marked DRIFT where they differ from Houston's), and Houston's DNS records in every zone, each with its project and whether it points at this server or another one. From there you can replace the API token (checked first; the old one stays if anything fails) and **Repair**, which pushes the routes and re-points Houston's own records.

```sh
houston cloudflare                 # the same view (--json)
houston cloudflare token < token   # a new API token, from stdin
houston cloudflare repair          # exit 1 if anything couldn't be put back
```

## Maintenance page

```sh
houston maintenance on --message "Back at 10:00" --project app
houston maintenance off --project app
```

The page is served by Mission Control, routed at the tunnel. It stays up whatever the app is doing, and deploys and restores never turn it off. A project can use its own page with `x-houston.maintenance: path/to/page.html`, where `{{project}}` and `{{message}}` are filled in.

## Working on Houston

The Go CLI is in `cmd/` and `internal/`; Mission Control (Rails 8, SQLite) is in `mission_control/`. Everything builds and runs in Docker:

```sh
bin/go test ./...                    # Go tests in the toolchain container
bin/test-integration                 # the same, plus the ones that drive real Docker
houston -f mission_control/compose.yml test    # Mission Control's suite, run by Houston itself
```

**Releasing:** push a tag like `vX.Y.Z`. `.github/workflows/release.yml` runs the tests (Go, Mission Control, the installer), then publishes both images at that tag and a GitHub Release with the CLI binaries, `install.sh` and `SHA256SUMS`. Nothing is published unless the tests pass.

`install/test/install-version.sh` checks installing a release, against a fake GitHub, in a container. The other real runs are in `install/test/`. Each creates a throwaway OrbStack machine and installs Houston on it:
- `orbstack.sh ubuntu:noble` runs the whole install, including the real Cloudflare tunnel when `mission_control/.houston/cloudflare-check.env` exists.
- `deploy-e2e.sh`, `backups-e2e.sh`, `restore-e2e.sh`, `init-e2e.sh` and others cover the rest.

## License

MIT, copyright Seven Moons LLC. See [LICENSE](LICENSE).

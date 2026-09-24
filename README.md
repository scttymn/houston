# Houston

A small self-hosted deploy orchestrator. Each project is one Docker Compose file with an `x-houston` block. One CLI works the same on your laptop and against the server. A web admin, **Mission Control**, handles Cloudflare, secrets, deploys, backups and restores.

Houston ties proven tools together rather than reinventing them: Docker runs everything, Compose describes the app, Kamal deploys it, restic backs it up, and a Cloudflare Tunnel is the only way in. It knows no frameworks. A static site, Rails, Phoenix or anything else that builds a Docker image works the same way.

- [The spec](https://claude.ai/artifact/R3d4fzN1u5QUP4kT88mqug): what Houston is and why.
- [`docs/plans/`](docs/plans): how each part was built, with what was tested and found.

## What you need

- **A Linux server that installs packages with apt, dnf or pacman:** Debian, Ubuntu and their derivatives; Fedora and the RHEL family (RHEL, Rocky, Alma, CentOS Stream); Arch and its derivatives. The installer adds what's missing (Docker, git, an SSH server) with that package manager. SELinux must be permissive for now.
- **An address your browser can reach** for first-run setup, before the Cloudflare tunnel exists: on your LAN, over a VPN, or a VPS's public IP (the setup code guards the page). After setup, everything goes through Cloudflare.
- **A Cloudflare account with a domain** (the *base domain*). Apps are served at `<name>.<base>`, and Mission Control at `admin.<base>`.
- **Docker with Compose on your laptop.** That's all the CLI needs.
- **Somewhere for backups:** a path on the server, an NFS export, or S3, B2 or SFTP.

## 1. Install the server

Images aren't published yet, so the server builds Houston from a checkout of this repo:

```sh
git clone https://github.com/scttymn/houston.git ~/houston
sudo HOUSTON_SOURCE=$HOME/houston sh ~/houston/install/install.sh
```

The installer:
- checks for apt, dnf or pacman (and stops, changing nothing, if there's none, or if SELinux is enforcing)
- installs Docker, git and an SSH server if they're missing, and starts them. Docker comes from Docker's own repository on apt and dnf, and from Arch's packages on pacman
- creates a `houston` user, and checks it can SSH to the server (Kamal deploys over SSH)
- generates Mission Control's secrets in `/opt/houston/.env`, which you must keep: losing them locks away every encrypted secret
- builds Mission Control, the CLI and the runner image
- starts everything from `/opt/houston/compose.yml`: Mission Control, a registry on `localhost:5000`, and two runners

It ends with a URL and a one-time **setup code**:

```
Houston is running.
Finish setup at  http://<server address>:3000
Setup code       XXXX-XXXX
```

Rerunning the installer repairs and updates, and keeps the secrets. `HOUSTON_RUNNERS=3` changes how many runners there are.

## 2. First run (in the browser)

Open the URL. The three steps:

1. **Create the admin login** with the setup code.
2. **Connect Cloudflare:** the base domain, and an API token with **Account · Cloudflare Tunnel · Edit** and **Zone · DNS · Edit** on that domain. Houston makes a tunnel with these routes:
   - `admin.<base>` → Mission Control
   - `hooks.<base>` → Mission Control, webhook paths only
   - everything else → your apps

   It also makes a proxied `*.<base>` record commented `managed-by:houston`, then connects the server. It never touches a DNS record without that comment.
3. **Default backup storage:** a location, a test write, and a restic password shown **once**. Save the password before you continue: without it the backups can't be read.

Mission Control is then at `https://admin.<base>`. Cloudflare Access in front of it is optional and recommended. Keep `hooks.<base>` outside Access.

## 3. Install the CLI (your laptop)

```sh
git clone https://github.com/scttymn/houston.git && cd houston
bin/install            # builds houston for this machine into ~/.local/bin, plus a `hou` symlink
```

`bin/release` builds all four platforms (macOS or Linux, arm64 or amd64) into `dist/`.

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
| `compose.yml` | One `app` service built from the Dockerfile, published on `127.0.0.1:8080`, the folder mounted for dev, and an `x-houston` block | A missing `x-houston` block is added; an existing one gets only the required keys it lacks |
| `.dockerignore` | `.git`, `.env`, `.houston` and the build files | Whichever of `.git`, `.env` and `.houston` it doesn't already exclude |
| `.env` / `.gitignore` | One line per variable the compose file references / `/.env` | Missing lines added |

A static site deploys as `init` writes it. For anything else, **edit the defaults for your project**:

- **The `dev` and `test` stages:** your dev tools and dev dependencies, and a command that serves the code `houston dev` mounts. Keep compiled dependencies out of the mounted folder.
- **The `app` service:**
  - your port
  - the variables the app needs (`${NAME}` when the server must have it, `${NAME:-}` when dev can do without)
  - named volumes for data
  - other services (Postgres, Redis) with their images and volumes
- **`x-houston`:**
  - `health`: a path that answers 200 without a login
  - `app_port`: the port production listens on, when it isn't the one in `ports`
  - `commands.console`, `commands.test`
  - `hooks.release` (migrations, run before traffic switches)
  - `domains`, `deploy` (which branch or tag deploys), `backups`
- **Service hostnames:** write `${DB_HOST:-db}` wherever the app names a service. Plain Compose uses `db`; on the server, Houston sets it to that service's container.

Then check it the way the server will run it:

```sh
houston dev                  # the dev stage, code mounted
houston test                 # commands.test in a throwaway copy
houston dev --production     # the production image, locally, with volumes of its own
houston init                 # again: "already set up" when nothing's missing
```

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

- **Push to deploy.** A push rings the webhook. Mission Control reads the repo's refs itself, and a runner fetches the commit, runs `commands.test` (step 00), then deploys. Traffic moves only once the new version passes its health check, so a failed deploy leaves the old version serving.
- **A deploy:**
  1. a snapshot of the running version (data and code)
  2. the image built at the commit
  3. accessories booted
  4. the release hook
  5. the switch
  6. the `post_deploy` hook
- **What's running:** `houston status`, `houston deploys --project app`, `houston logs --server`, `houston console --server`.
- **By hand on the server:** as the `houston` user, `houston deploy` in a clean checkout.
- **Status words:** GO, IN FLIGHT, NO-GO (the previous version still serves), HOLD (something blocks the next deploy, such as a secret with no value).

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

The real runs are in `install/test/`. Each creates a throwaway OrbStack machine and installs Houston on it:
- `orbstack.sh ubuntu:noble` runs the whole install, including the real Cloudflare tunnel when `mission_control/.houston/cloudflare-check.env` exists.
- `deploy-e2e.sh`, `backups-e2e.sh`, `restore-e2e.sh`, `init-e2e.sh` and others cover the rest.

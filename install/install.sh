#!/bin/sh
# Houston server installer. Run as root on a Linux that installs packages
# with apt (Debian, Ubuntu and their derivatives), dnf (Fedora and the RHEL
# family) or pacman (Arch and its derivatives):
#
#   curl -fsSL <install-url>/install.sh | sudo sh
#
# Until Mission Control's image is published, it's built from a Houston
# checkout on the server: HOUSTON_SOURCE=/path/to/houston sh install.sh
#
# Rerunning repairs and updates. Generated secrets are kept.
set -eu

HOUSTON_DIR="${HOUSTON_DIR:-/opt/houston}"
HOUSTON_SOURCE="${HOUSTON_SOURCE:-}"
# A release (docs/plans/releases.md): its images from the registry and its CLI
# from the GitHub Release. The token is only needed while the repo is private.
HOUSTON_VERSION="${HOUSTON_VERSION:-}"
HOUSTON_REPO="${HOUSTON_REPO:-scttymn/houston}"
HOUSTON_GITHUB_API="${HOUSTON_GITHUB_API:-https://api.github.com}"
HOUSTON_GITHUB_TOKEN="${HOUSTON_GITHUB_TOKEN:-}"
HOUSTON_REGISTRY="${HOUSTON_REGISTRY:-ghcr.io}"
IMAGE="houston/mission-control:local"
# Other projects' images, pinned by digest: a moved tag can't change what
# runs. KAMAL_IMAGE is the one houston deploy runs (internal/deploy).
CLOUDFLARED_IMAGE="cloudflare/cloudflared:2026.9.1@sha256:b269e8abd07a5bf6f3f4be65d5050b2174eca89c56a0241a8ff32a16aec454e4"
KAMAL_IMAGE="ghcr.io/basecamp/kamal:v2.12.0@sha256:7b5be276aa17bbe122887f6a1ff12865f4848111989699b9786083be95d98415"
REGISTRY_IMAGE="registry:3.1.2@sha256:c87f33837722a100572e95d7dc4bf539fc42cf68202b13c3bc03c0ff54c3a649"
RUNNER_IMAGE="houston/runner:local"
RUNNERS="${HOUSTON_RUNNERS:-2}"
# Where Mission Control's port 3000 listens (an IPv4 address). Unset: open to
# the network until Cloudflare is connected, then 127.0.0.1 (choose_bind).
HOUSTON_BIND="${HOUSTON_BIND:-}"

say() { printf '%s\n' "$*"; }
compose() { docker compose -f "$HOUSTON_DIR/compose.yml" "$@"; }
step() { printf '==> %s\n' "$*"; }
fail() { printf 'houston install: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

# Everything is checked before anything is changed.
check_system() {
  [ "$(id -u)" = 0 ] || fail "run it as root (for example: sudo sh install.sh)"
  if has apt-get; then pm=apt
  elif has dnf; then pm=dnf
  elif has pacman; then pm=pacman
  else fail "Houston installs its prerequisites with apt, dnf or pacman; this system has none of them"
  fi
  ID="" ID_LIKE="" VERSION_CODENAME="" UBUNTU_CODENAME=""
  # shellcheck disable=SC1091
  [ -r /etc/os-release ] && . /etc/os-release
  case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac
  has useradd || fail "no useradd on this system; install your distro's shadow package, then run this again"
  if has getenforce && [ "$(getenforce)" = Enforcing ]; then
    fail "SELinux is enforcing, and Houston doesn't label its containers for SELinux yet: set it to permissive (setenforce 0, and SELINUX=permissive in /etc/selinux/config), then run this again"
  fi
  check_release
  check_bind
}

check_bind() {
  [ -z "$HOUSTON_BIND" ] && return 0
  printf '%s' "$HOUSTON_BIND" | grep -Eqx '([0-9]{1,3}\.){3}[0-9]{1,3}' ||
    fail "HOUSTON_BIND must be an IPv4 address, like 127.0.0.1 or 0.0.0.0 (not $HOUSTON_BIND)"
}

# check_release: a release (HOUSTON_VERSION, or the latest when it's unset) or
# a checkout (HOUSTON_SOURCE), not both. A release must exist before anything
# is changed.
check_release() {
  [ -n "$HOUSTON_VERSION" ] && [ -n "$HOUSTON_SOURCE" ] && fail "set HOUSTON_VERSION or HOUSTON_SOURCE, not both"
  if [ -n "$HOUSTON_SOURCE" ]; then
    [ -f "$HOUSTON_SOURCE/mission_control/Dockerfile" ] || fail "HOUSTON_SOURCE=$HOUSTON_SOURCE has no mission_control/Dockerfile"
    return 0
  fi
  has curl || fail "installing a release needs curl"
  # Neither set: the latest release (GitHub's "latest" leaves prereleases out).
  if [ -z "$HOUSTON_VERSION" ]; then
    latest_json=$(github "$HOUSTON_GITHUB_API/repos/$HOUSTON_REPO/releases/latest") || latest_json=""
    # shellcheck disable=SC2020 # each of , { and } becomes a newline, on purpose
    HOUSTON_VERSION=$(printf '%s' "$latest_json" | tr ',{}' '\n\n\n' | sed 's/^ *//; s/": *"/":"/' | sed -n 's/^"tag_name":"\([^"]*\)"$/\1/p' | head -1)
    [ -n "$HOUSTON_VERSION" ] ||
      fail "couldn't find Houston's latest release (set HOUSTON_VERSION to one, or HOUSTON_SOURCE to a checkout)"
    step "The latest release is $HOUSTON_VERSION"
  fi
  printf '%s' "$HOUSTON_VERSION" | grep -Eq '^v[0-9]+[.][0-9]+[.][0-9]+(-[0-9A-Za-z.]+)?$' ||
    fail "HOUSTON_VERSION must be a release tag like v0.1.0"
  release_json=$(github "$HOUSTON_GITHUB_API/repos/$HOUSTON_REPO/releases/tags/$HOUSTON_VERSION") ||
    fail "$HOUSTON_VERSION isn't a Houston release (or the repo is private: set HOUSTON_GITHUB_TOKEN to a token that can read it)"
  owner=${HOUSTON_REPO%%/*}
  IMAGE="$HOUSTON_REGISTRY/$owner/houston-mission-control:$HOUSTON_VERSION"
  RUNNER_IMAGE="$HOUSTON_REGISTRY/$owner/houston-runner:$HOUSTON_VERSION"
}

# github <url> [accept]: a GitHub API request, with the token when there is
# one. The token goes to curl on stdin, never on its command line.
github() {
  if [ -n "$HOUSTON_GITHUB_TOKEN" ]; then
    printf 'header = "Authorization: Bearer %s"\n' "$HOUSTON_GITHUB_TOKEN" |
      curl -fsSL -K - -H "Accept: ${2:-application/vnd.github+json}" "$1"
  else
    curl -fsSL -H "Accept: ${2:-application/vnd.github+json}" "$1"
  fi
}

# asset_url <name>: the API URL of the release's asset called name.
asset_url() {
  # shellcheck disable=SC2020 # each of , { and } becomes a newline, on purpose
  printf '%s' "$release_json" | tr ',{}' '\n\n\n' | sed 's/^ *//; s/": *"/":"/' | awk -v want="\"name\":\"$1\"" '
    /^"url":"[^"]*\/releases\/assets\/[0-9]+"$/ { u = $0; sub(/^"url":"/, "", u); sub(/"$/, "", u) }
    $0 == want && u != "" { print u; exit }'
}

# fetch_asset <name> <dest>: downloads one of the release's assets.
fetch_asset() {
  url=$(asset_url "$1")
  [ -n "$url" ] || fail "release $HOUSTON_VERSION has no $1"
  github "$url" application/octet-stream >"$2" || fail "couldn't download $1 of $HOUSTON_VERSION"
}

# release_images: IMAGE and RUNNER_IMAGE by digest, from the release's
# IMAGES, checked against its SHA256SUMS. A release from before IMAGES
# (v0.3.0 and older) is pulled by tag.
release_images() {
  if [ -z "$(asset_url IMAGES)" ]; then
    step "$HOUSTON_VERSION lists no image digests; pulling by tag"
    return 0
  fi
  dir=$(mktemp -d)
  fetch_asset SHA256SUMS "$dir/SHA256SUMS"
  fetch_asset IMAGES "$dir/IMAGES"
  if ! (cd "$dir" && grep " IMAGES\$" SHA256SUMS | sha256sum -c --status); then
    rm -rf "$dir"
    fail "IMAGES doesn't match the release's SHA256SUMS; nothing was pulled"
  fi
  mc=$(sed -n 1p "$dir/IMAGES")
  runner=$(sed -n 2p "$dir/IMAGES")
  rm -rf "$dir"
  by_digest "$mc" "$IMAGE"
  by_digest "$runner" "$RUNNER_IMAGE"
  IMAGE=$mc RUNNER_IMAGE=$runner
}

# by_digest <ref> <image>: ref must be image@sha256:<digest>.
by_digest() {
  printf '%s' "$1" | grep -Eqx "$(printf '%s' "$2" | sed 's/[.]/[.]/g')@sha256:[0-9a-f]{64}" ||
    fail "IMAGES names $1, not $2 by digest; nothing was pulled"
}

# installed_version: what the flight board will say.
installed_version() { if [ -n "$HOUSTON_VERSION" ]; then printf '%s' "$HOUSTON_VERSION"; else printf 'source %.7s' "$(source_sha)"; fi; }

# source_sha: the checkout's commit, for the version a source build shows.
source_sha() { git -C "$HOUSTON_SOURCE" rev-parse HEAD 2>/dev/null || echo unknown; }

# pm_install installs packages with the system's package manager, quietly,
# and on failure stops with the command to run by hand (dnf -q prints
# nothing at all for a package it can't find).
pm_install() {
  case "$pm" in
    apt) set -- env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" ;;
    dnf) set -- dnf install -y -q "$@" ;;
    # Arch installs packages only as part of a full upgrade.
    pacman) set -- pacman -Syu --noconfirm --needed "$@" ;;
  esac
  "$@" >/dev/null || fail "this failed: $*; run it by hand to see why, then run this again"
}

# Docker from Docker's own repository on apt and dnf; Arch packages it. A
# repository is only written once it's known to exist for this system, and
# is removed again if installing from it fails: a broken one would break
# every later update.
install_docker_packages() {
  case "$pm" in
    apt)
      # Docker publishes for debian and ubuntu; a derivative uses its parent's
      # release (Mint and Pop!_OS say UBUNTU_CODENAME, LMDE DEBIAN_CODENAME).
      distro=$ID codename=$VERSION_CODENAME
      case "$ID" in debian | ubuntu) ;; *)
        case " $ID_LIKE " in
          *" ubuntu "*) distro=ubuntu codename=${UBUNTU_CODENAME:-$VERSION_CODENAME} ;;
          *" debian "*) distro=debian codename=${DEBIAN_CODENAME:-$VERSION_CODENAME} ;;
          *) codename="" ;;
        esac ;;
      esac
      apt-get update -qq
      pm_install ca-certificates curl
      if [ -z "$codename" ] || ! curl -fsI "https://download.docker.com/linux/$distro/dists/$codename/Release" >/dev/null; then
        fail "Docker doesn't publish packages for ${PRETTY_NAME:-this system} (${distro:-?} ${codename:-?}): install Docker with Compose yourself, then run this again"
      fi
      install -m 0755 -d /etc/apt/keyrings
      curl -fsSL "https://download.docker.com/linux/$distro/gpg" -o /etc/apt/keyrings/docker.asc
      chmod a+r /etc/apt/keyrings/docker.asc
      printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' \
        "$(dpkg --print-architecture)" "$distro" "$codename" >/etc/apt/sources.list.d/docker.list
      if ! apt-get update -qq || ! (pm_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin); then
        rm -f /etc/apt/sources.list.d/docker.list
        fail "installing Docker from Docker's repository failed; install Docker with Compose yourself, then run this again"
      fi
      ;;
    dnf)
      # Fedora has its own repository and RHEL its own; the rest of the RHEL
      # family (Rocky, Alma, CentOS Stream: ID_LIKE names rhel or centos) uses
      # CentOS's. Fedora's derivatives (Amazon Linux: ID_LIKE=fedora) aren't
      # Fedora releases, so Docker has nothing for them.
      case "$ID" in
        fedora) repo=fedora ;;
        rhel) repo=rhel ;;
        *) case " $ID_LIKE " in
             *" rhel "* | *" centos "*) repo=centos ;;
             *) fail "Docker doesn't publish packages for ${PRETTY_NAME:-this system}: install Docker with Compose yourself, then run this again" ;;
           esac ;;
      esac
      url="https://download.docker.com/linux/$repo/docker-ce.repo"
      if dnf --version 2>/dev/null | grep -q '^dnf5'; then
        pm_install dnf5-plugins
        dnf config-manager addrepo --overwrite --from-repofile="$url" >/dev/null
      else
        pm_install dnf-plugins-core
        dnf config-manager --add-repo "$url" >/dev/null
      fi
      if ! (pm_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin); then
        rm -f /etc/yum.repos.d/docker-ce.repo
        fail "installing Docker from Docker's $repo repository failed; install Docker with Compose yourself, then run this again"
      fi
      ;;
    pacman) pm_install docker docker-compose docker-buildx ;;
  esac
}

install_prereqs() {
  if has docker && docker compose version >/dev/null 2>&1; then
    step "Docker is installed"
  else
    step "Installing Docker (with $pm)"
    install_docker_packages
  fi
  systemctl enable --now docker >/dev/null 2>&1 || true
  if ! has docker || ! docker compose version >/dev/null 2>&1; then
    fail "Docker with Compose isn't working after installing it; see: docker compose version"
  fi
  # The daemon, not only the client: a full upgrade (pacman -Syu) can replace
  # the running kernel, and its modules with it, until a reboot.
  if ! docker info >/dev/null 2>&1; then
    [ -d "/usr/lib/modules/$(uname -r)" ] || fail "the kernel was upgraded while installing, and Docker can't start until this server reboots: reboot, then run this again"
    fail "the Docker daemon isn't running; see: systemctl status docker"
  fi
  has curl || { step "Installing curl"; pm_install curl; }
  has git || { step "Installing git (houston deploy reads the checkout)"; pm_install git; }
  if ! has sshd && [ ! -x /usr/sbin/sshd ]; then
    step "Installing OpenSSH server (Kamal deploys to this host over SSH)"
    case "$pm" in apt | dnf) pm_install openssh-server ;; pacman) pm_install openssh ;; esac
  fi
  # The client too, for the check that houston can SSH here (dnf's server
  # package doesn't bring it).
  if ! has ssh; then
    case "$pm" in apt) pm_install openssh-client ;; dnf) pm_install openssh-clients ;; pacman) pm_install openssh ;; esac
  fi
  # Kamal SSHes to this host as houston, so the server must be running; not
  # every distro starts it on install. Debian names the service ssh.
  systemctl enable --now ssh >/dev/null 2>&1 || systemctl enable --now sshd >/dev/null 2>&1 || true
}

# Kamal reaches this host as houston over SSH: prove it before going on.
# This host's key isn't pinned here (Kamal doesn't read houston's known_hosts
# either), so a server whose host keys changed still passes.
check_ssh() {
  home=$(getent passwd houston | cut -d: -f6)
  if ! out=$(su houston -c "ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -i '$home/.ssh/id_ed25519' houston@127.0.0.1 true" 2>&1); then
    fail "houston can't SSH to this host, which Kamal needs: $out (is the SSH server running and allowing key logins?)"
  fi
}

# The address to finish setup at: the first of this host's addresses.
host_address() {
  hostname -I 2>/dev/null | awk '{print $1}' | grep . && return
  ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit }}'
}

create_user() {
  if ! id houston >/dev/null 2>&1; then
    step "Creating the houston user"
    useradd --create-home --shell "$(command -v bash || echo /bin/sh)" houston
  fi
  usermod -aG docker houston
  home=$(getent passwd houston | cut -d: -f6)
  install -d -m 700 -o houston -g houston "$home/.ssh"
  if [ ! -f "$home/.ssh/id_ed25519" ]; then
    su houston -c "ssh-keygen -q -t ed25519 -N '' -C houston@$(uname -n) -f '$home/.ssh/id_ed25519'"
  fi
  touch "$home/.ssh/authorized_keys"
  if ! grep -qF "$(cat "$home/.ssh/id_ed25519.pub")" "$home/.ssh/authorized_keys"; then
    cat "$home/.ssh/id_ed25519.pub" >>"$home/.ssh/authorized_keys"
  fi
  chown houston:houston "$home/.ssh/authorized_keys"
  chmod 600 "$home/.ssh/authorized_keys"
}

random() { head -c 48 /dev/urandom | base64 | tr -d '\n/+='; }

write_env() {
  install -d -m 755 "$HOUSTON_DIR"
  if [ -f "$HOUSTON_DIR/.env" ]; then
    step "Keeping the existing secrets in $HOUSTON_DIR/.env"
  else
    step "Generating secrets in $HOUSTON_DIR/.env"
    umask 077
    {
      say "# Generated by the Houston installer. Losing these locks away every encrypted secret."
      say "SECRET_KEY_BASE=$(random)$(random)"
      say "AR_ENCRYPTION_PRIMARY_KEY=$(random)"
      say "AR_ENCRYPTION_DETERMINISTIC_KEY=$(random)"
      say "AR_ENCRYPTION_KEY_DERIVATION_SALT=$(random)"
    } >"$HOUSTON_DIR/.env"
    umask 022
  fi
  # Installs from before build step 3 have no runner token yet.
  if ! grep -q '^HOUSTON_RUNNER_TOKEN=' "$HOUSTON_DIR/.env"; then
    say "HOUSTON_RUNNER_TOKEN=$(random)" >>"$HOUSTON_DIR/.env"
  fi
  chmod 600 "$HOUSTON_DIR/.env"
}

# houston deploy, run as the houston user, reads the runner token from here.
write_runner_token() {
  home=$(getent passwd houston | cut -d: -f6)
  install -d -m 700 -o houston -g houston "$home/.config" "$home/.config/houston"
  umask 077
  sed -n 's/^HOUSTON_RUNNER_TOKEN=//p' "$HOUSTON_DIR/.env" >"$home/.config/houston/runner-token"
  umask 022
  chown houston:houston "$home/.config/houston/runner-token"
  chmod 600 "$home/.config/houston/runner-token"
}

# The houston CLI: the release's binary, checked against its SHA256SUMS, or
# built from the checkout. A mismatch installs nothing.
install_cli() {
  out=$(mktemp -d)
  if [ -n "$HOUSTON_VERSION" ]; then
    step "Installing the houston CLI $HOUSTON_VERSION"
    fetch_asset SHA256SUMS "$out/SHA256SUMS"
    fetch_asset "houston-linux-$arch" "$out/houston-linux-$arch"
    if ! (cd "$out" && grep " houston-linux-$arch\$" SHA256SUMS | sha256sum -c --status); then
      rm -rf "$out"
      fail "houston-linux-$arch doesn't match the release's SHA256SUMS; nothing was installed"
    fi
  else
    step "Building the houston CLI from $HOUSTON_SOURCE"
    sha=$(source_sha)
    docker build --quiet --target release --build-arg VERSION="source-$(printf '%.7s' "$sha")" --output "type=local,dest=$out" "$HOUSTON_SOURCE" >/dev/null
  fi
  install -m 755 "$out/houston-linux-$arch" /usr/local/bin/houston
  ln -sf houston /usr/local/bin/hou
  rm -rf "$out"
  docker pull --quiet "$KAMAL_IMAGE" >/dev/null
}

# One service per runner. Paths the runner hands to the host's docker (its
# workspace, houston's ~/.ssh) are mounted at the same path.
runner_services() {
  i=1
  while [ "$i" -le "$RUNNERS" ]; do
    name="houston-runner-$i"
    cat <<RUNNER
  $name:
    image: $RUNNER_IMAGE
    pull_policy: never
    restart: unless-stopped
    user: "$houston_uid:$houston_gid"
    group_add:
      - "$docker_gid"
    environment:
      HOME: $houston_home
      HOUSTON_URL: http://mission-control:80
      HOUSTON_TOKEN: \${HOUSTON_RUNNER_TOKEN}
      # Only ~/.ssh is mounted under HOME, so docker keeps its state in the workspace.
      DOCKER_CONFIG: /var/lib/houston/runners/$name/.docker
    command: ["houston", "runner", "--name", "$name", "--workspace", "/var/lib/houston/runners/$name"]
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /usr/local/bin/houston:/usr/local/bin/houston:ro
      - /etc/passwd:/etc/passwd:ro
      - /etc/group:/etc/group:ro
      - $houston_home/.ssh:$houston_home/.ssh:ro
      - /var/lib/houston/runners/$name:/var/lib/houston/runners/$name

RUNNER
    i=$((i + 1))
  done
}

# choose_bind: where port 3000 listens. A first install opens it to the
# network, for setup in a browser. Once Cloudflare is connected, admin.<base>
# is the way in, so a rerun binds it to 127.0.0.1 (reach it with ssh -L). A
# rerun that can't ask the installed Mission Control binds 127.0.0.1 too, and
# the report says how to open it. HOUSTON_BIND chooses.
choose_bind() {
  bind_unknown=""
  if [ -n "$HOUSTON_BIND" ]; then
    bind=$HOUSTON_BIND
  elif [ ! -f "$HOUSTON_DIR/compose.yml" ]; then
    bind=0.0.0.0
  else
    connected=0
    compose run --rm --no-deps -T mission-control bin/rails runner 'exit(Installation.connected? ? 0 : 3)' >/dev/null 2>&1 || connected=$?
    case "$connected" in
      0) bind=127.0.0.1 ;;
      3) bind=0.0.0.0 ;;
      *) bind=127.0.0.1 bind_unknown=1 ;;
    esac
  fi
}

# probe_address: where to check Mission Control answers.
probe_address() { if [ "$bind" = 0.0.0.0 ]; then echo 127.0.0.1; else echo "$bind"; fi; }

write_compose() {
  docker_gid=$(getent group docker | cut -d: -f3)
  houston_uid=$(id -u houston)
  houston_gid=$(id -g houston)
  houston_home=$(getent passwd houston | cut -d: -f6)
  step "Writing $HOUSTON_DIR/compose.yml"
  cat >"$HOUSTON_DIR/compose.yml" <<EOF
# Written by the Houston installer; rerunning it rewrites this file.
name: houston

services:
  mission-control:
    image: $IMAGE
    restart: unless-stopped
    env_file: .env
    environment:
      RAILS_ENV: production
      SOLID_QUEUE_IN_PUMA: "1"
      HOUSTON_TUNNEL_TOKEN_PATH: /houston/tunnel-token
      HOUSTON_RUNNERS: "$RUNNERS"
      HOUSTON_TOOLS_IMAGE: $IMAGE
      # Each runner's claim long-polls on a Puma thread; leave plenty for the UI and webhooks.
      RAILS_MAX_THREADS: "8"
    ports:
      - "$bind:3000:80"
    group_add:
      - "$docker_gid"
    volumes:
      - mission-control-storage:/rails/storage
      - houston-config:/houston
      - /var/run/docker.sock:/var/run/docker.sock
      # Mission Control reads linked repos' compose files with the CLI's own parser.
      - /usr/local/bin/houston:/usr/local/bin/houston:ro

  # Waits for the token Mission Control writes in setup step 2, then connects.
  cloudflared:
    image: $CLOUDFLARED_IMAGE
    restart: unless-stopped
    user: "1000:1000"
    command: ["tunnel", "--no-autoupdate", "run", "--token-file", "/houston/tunnel-token"]
    volumes:
      - houston-config:/houston:ro
    networks:
      - default
      - kamal

  registry:
    image: $REGISTRY_IMAGE
    restart: unless-stopped
    ports:
      - "127.0.0.1:5000:5000"
    volumes:
      - registry-data:/var/lib/registry

$(runner_services)
volumes:
  mission-control-storage:
  houston-config:
  registry-data:

networks:
  kamal:
    external: true
EOF
}

# fetch_images: the release's images, or ones built from the checkout. Right
# after Docker is installed and before anything is written, so a failed pull
# or build changes nothing.
fetch_images() {
  if [ -n "$HOUSTON_VERSION" ]; then
    release_images
    step "Pulling Houston $HOUSTON_VERSION"
    if [ -n "$HOUSTON_GITHUB_TOKEN" ]; then
      printf '%s' "$HOUSTON_GITHUB_TOKEN" | docker login "$HOUSTON_REGISTRY" -u houston --password-stdin >/dev/null 2>&1 ||
        fail "couldn't log in to $HOUSTON_REGISTRY with HOUSTON_GITHUB_TOKEN (it needs read:packages)"
    fi
    pulled=1
    docker pull --quiet "$IMAGE" >/dev/null && docker pull --quiet "$RUNNER_IMAGE" >/dev/null || pulled=0
    # The token is only for this install: it isn't left in Docker's config.
    [ -z "$HOUSTON_GITHUB_TOKEN" ] || docker logout "$HOUSTON_REGISTRY" >/dev/null 2>&1 || true
    [ "$pulled" = 1 ] || fail "couldn't pull $IMAGE and $RUNNER_IMAGE"
  else
    sha=$(source_sha)
    step "Building Mission Control from $HOUSTON_SOURCE"
    docker build --quiet --target production --build-arg HOUSTON_SOURCE_SHA="$sha" -t "$IMAGE" "$HOUSTON_SOURCE/mission_control" >/dev/null
    step "Building the runner image"
    docker build --quiet -t "$RUNNER_IMAGE" -f "$HOUSTON_SOURCE/install/runner.Dockerfile" "$HOUSTON_SOURCE/install" >/dev/null
  fi
}

start() {
  docker network inspect kamal >/dev/null 2>&1 || docker network create kamal >/dev/null
  i=1
  while [ "$i" -le "$RUNNERS" ]; do
    install -d -m 750 -o houston -g houston /var/lib/houston /var/lib/houston/runners "/var/lib/houston/runners/houston-runner-$i"
    i=$((i + 1))
  done
  compose create --quiet-pull >/dev/null 2>&1 || compose create
  # Mission Control (uid 1000) writes the tunnel token for cloudflared here.
  docker run --rm --user root --entrypoint chown -v houston_houston-config:/houston "$IMAGE" 1000:1000 /houston
  step "Starting Houston"
  compose up -d --remove-orphans >/dev/null 2>&1 || compose up -d --remove-orphans

  # The runners run the host's houston CLI, mounted as a file, and a running
  # container keeps the binary it started with. So they're recreated to pick
  # up this one. A deploy in flight on one is abandoned: NO-GO, and the old
  # version keeps serving.
  runners=""
  i=1
  while [ "$i" -le "$RUNNERS" ]; do
    runners="$runners houston-runner-$i"
    i=$((i + 1))
  done
  # shellcheck disable=SC2086 # one word per runner
  compose up -d --no-deps --force-recreate $runners >/dev/null 2>&1 || compose up -d --no-deps --force-recreate $runners

  tries=0
  until curl -fs -o /dev/null "http://$(probe_address):3000/up"; do
    tries=$((tries + 1))
    [ "$tries" -lt 90 ] || fail "Mission Control didn't come up; see: docker compose -f $HOUSTON_DIR/compose.yml logs mission-control"
    sleep 2
  done
}

report() {
  address=$(host_address || true)
  if [ "$bind" = 0.0.0.0 ]; then url="http://${address:-<this server>}:3000"; else url="http://$bind:3000"; fi
  if code=$(compose exec -T mission-control bin/rails houston:setup_code 2>/dev/null); then
    say ""
    say "Houston $(installed_version) is running."
    say "Finish setup at  $url"
    say "Setup code       $code"
    [ "$bind" = 0.0.0.0 ] && say "When setup is done, run this installer again: it closes port 3000 to the network."
  else
    say ""
    say "Houston $(installed_version) is running. Setup is already complete. Sign in at https://admin.<your base domain>."
    if [ "$bind" = 0.0.0.0 ]; then
      say "Port 3000 is open to your network, over plain HTTP (HOUSTON_BIND=0.0.0.0)."
      return 0
    fi
  fi
  if [ "$bind" = 127.0.0.1 ]; then
    say "Port 3000 answers only on this server. From another machine: ssh -L 3000:127.0.0.1:3000 <you>@${address:-<this server>}, then open http://localhost:3000."
  fi
  if [ -n "${bind_unknown:-}" ]; then
    say "Couldn't ask Mission Control whether setup is done, so port 3000 is closed to the network. To open it for setup, run this installer again with HOUSTON_BIND=0.0.0.0."
  fi
}

# HOUSTON_INSTALL_LIB=1 loads the functions without running them (tests).
[ -n "${HOUSTON_INSTALL_LIB:-}" ] && return 0 2>/dev/null

check_system
install_prereqs
fetch_images
create_user
check_ssh
write_env
write_runner_token
choose_bind
write_compose
install_cli
start
report

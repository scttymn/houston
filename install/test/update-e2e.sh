#!/usr/bin/env bash
# The houston-update helper on a real host (docs/plans/update-from-mission-control.md,
# row 12): a fresh OrbStack machine (Ubuntu, systemd, Docker) runs
# mission_control/lib/update-helper.sh the way ServerUpdate starts it, from
# the runner's base image. The release images are amd64 only and OrbStack's
# amd64 machines can't run containers, so a stand-in installer takes the
# release's place: the host's curl is wrapped to hand it out (and to 404 for
# v0.0.99, a release that doesn't exist). It writes down what it was run
# with, as whom and where, and checks it can use the host's Docker, as the
# real installer does.
#
#   install/test/update-e2e.sh          # KEEP=1 keeps the machine
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

name="houston-update-e2e"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
# The runner image is FROM this (install/runner.Dockerfile); busybox's nsenter is the one used.
image="docker:29-cli"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$name" -u root "$@"; }

cleanup() { if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi; }
trap cleanup EXIT

# helper TO FROM: runs houston-update as ServerUpdate.start! does (with a
# secret in the container's environment that mustn't reach the installer)
# and prints its exit code once it has ended.
helper() {
  vm docker rm houston-update >/dev/null 2>&1
  vm docker run -d --name houston-update --privileged --pid=host --user 0 \
    -e "HOUSTON_UPDATE_TO=$1" -e "HOUSTON_UPDATE_FROM=$2" -e HOUSTON_REPO=scttymn/houston \
    -e HOUSTON_DIR=/opt/houston -e HOUSTON_RUNNERS=2 -e SECRET_KEY_BASE=leaked \
    --entrypoint sh "$image" -c "$(cat "$repo/mission_control/lib/update-helper.sh")" >/dev/null || { echo start-failed; return; }
  vm docker wait houston-update
}
installed() { vm cat "/root/installed-$1" 2>/dev/null; }

echo "== creating $name with Docker"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null
vm sh -c 'DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io curl >/dev/null 2>&1' || { echo "couldn't install Docker"; exit 1; }
vm docker pull -q "$image" >/dev/null

# The stand-in: /usr/local/bin comes first on the PATH the helper gives the host.
vm sh -c 'cat > /usr/local/bin/curl' <<'CURL'
#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift 2 ;; -*) shift ;; *) url=$1; shift ;; esac; done
case "$url" in https://github.com/scttymn/houston/releases/download/v0.0.99/install.sh) exit 22 ;; https://github.com/scttymn/houston/releases/download/*/install.sh) ;; *) exit 2 ;; esac
cat > "$out" <<'INSTALLER'
f=/root/installed-$HOUSTON_VERSION
echo "version=$HOUSTON_VERSION repo=$HOUSTON_REPO dir=$HOUSTON_DIR runners=$HOUSTON_RUNNERS home=$HOME uid=$(id -u) host=$(hostname) secret=${SECRET_KEY_BASE:-none}" > "$f"
docker ps -q --filter name=houston-update | grep -q . && echo "host docker: the helper is running" >> "$f"
systemctl is-system-running >/dev/null 2>&1; [ $? -le 1 ] && echo "host systemd: answers" >> "$f"
INSTALLER
CURL
vm chmod 755 /usr/local/bin/curl
host=$(vm hostname)

echo "== the helper installs v0.4.3 on the host"
code=$(helper v0.4.3 v0.4.2)
[ "$code" = 0 ] && ok "the helper exited 0" || { bad "the helper exited $code"; vm docker logs --tail 30 houston-update; }
got=$(installed v0.4.3)
[ "$(head -1 <<<"$got")" = "version=v0.4.3 repo=scttymn/houston dir=/opt/houston runners=2 home=/root uid=0 host=$host secret=none" ] &&
  ok "the installer ran on the host, as root, with only what it's given" || bad "the installer saw: $got"
grep -q "host docker: the helper is running" <<<"$got" && ok "it reaches the host's Docker" || bad "no host Docker: $got"
grep -q "host systemd: answers" <<<"$got" && ok "it reaches the host's systemd" || bad "no host systemd: $got"
[ -z "$(installed v0.4.2)" ] && ok "nothing else was installed" || bad "v0.4.2 was installed too"
vm docker logs houston-update 2>&1 | grep -q 'installing v0.4.3' && ok "its log says what it installed" || bad "its log"

echo "== asked for a release that doesn't exist, it puts v0.4.2 back"
code=$(helper v0.0.99 v0.4.2)
[ "$code" = 3 ] && ok "the helper exited 3 (rolled back)" || { bad "the helper exited $code"; vm docker logs --tail 30 houston-update; }
installed v0.4.2 | grep -q '^version=v0.4.2 ' && ok "v0.4.2 was installed again" || bad "v0.4.2: $(installed v0.4.2)"
vm docker logs houston-update 2>&1 | grep -q "v0.0.99 didn't install; putting v0.4.2 back" && ok "its log says so" || bad "its log"

echo
[ "$failures" = 0 ] && echo "update-e2e: all passed" || echo "update-e2e: $failures failed"
[ "$failures" = 0 ]

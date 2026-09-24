#!/usr/bin/env bash
# The installer's refusals (docs/plans/install-package-managers.md, row 5),
# each on a fresh OrbStack machine: it stops before changing anything.
#   - Alpine: none of apt, dnf and pacman.
#   - Fedora with SELinux enforcing. OrbStack's kernel doesn't run SELinux,
#     so a getenforce stub that says Enforcing stands in for it.
#   - Kali and Devuan: apt, but releases Docker doesn't publish for. The
#     installer checks the repository exists before writing it (curl may be
#     installed for that), and leaves no Docker repository behind.
#
#   install/test/install-refusals.sh
# shellcheck disable=SC2015 # ok/bad return 0
set -uo pipefail

repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
machines=()
cleanup() { for m in "${machines[@]}"; do orb delete -f "$m" >/dev/null 2>&1 || true; done; }
trap cleanup EXIT

# refused <machine> <want in the message>: runs the installer, which must
# exit 1 with that message, having installed nothing and written nothing.
refused() {
  local m=$1 want=$2 out code
  out=$(orb -m "$m" -u root env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" 2>&1); code=$?
  [ "$code" = 1 ] && printf '%s' "$out" | grep -qF "$want" && ok "$m: exit 1, \"$(printf '%s' "$out" | tail -1)\"" || bad "$m: exit $code: $out"
  orb -m "$m" -u root sh -c '[ ! -e /opt/houston ] && ! id houston >/dev/null 2>&1 && ! command -v docker >/dev/null' &&
    ok "$m: nothing installed, no houston user, no /opt/houston" || bad "$m: something was changed"
}

echo "== alpine: no apt, dnf or pacman"
m=houston-refuse-alpine; machines+=("$m")
orb delete -f "$m" >/dev/null 2>&1 || true
orb create alpine "$m" >/dev/null
refused "$m" "Houston installs its prerequisites with apt, dnf or pacman; this system has none of them"

echo "== fedora with SELinux enforcing (simulated)"
m=houston-refuse-selinux; machines+=("$m")
orb delete -f "$m" >/dev/null 2>&1 || true
orb create fedora "$m" >/dev/null
orb -m "$m" -u root sh -c 'printf "#!/bin/sh\necho Enforcing\n" > /usr/local/sbin/getenforce && chmod 755 /usr/local/sbin/getenforce'
refused "$m" "SELinux is enforcing"

for distro in kali devuan; do
  echo "== $distro: apt, a release Docker doesn't publish for"
  m=houston-refuse-$distro; machines+=("$m")
  orb delete -f "$m" >/dev/null 2>&1 || true
  orb create "$distro" "$m" >/dev/null
  out=$(orb -m "$m" -u root env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" 2>&1); code=$?
  [ "$code" = 1 ] && printf '%s' "$out" | grep -qF "Docker doesn't publish packages for" && ok "$m: exit 1, \"$(printf '%s' "$out" | tail -1)\"" || bad "$m: exit $code: $out"
  orb -m "$m" -u root sh -c '[ ! -e /etc/apt/sources.list.d/docker.list ] && [ ! -e /opt/houston ] && ! id houston >/dev/null 2>&1 && ! command -v docker >/dev/null' &&
    ok "$m: no Docker repository left behind, no Docker, no houston user, no /opt/houston" || bad "$m: something was left"
done

echo
if [ "$failures" -eq 0 ]; then echo "REFUSALS PASS"; else echo "REFUSALS: $failures failure(s)"; exit 1; fi

#!/usr/bin/env bash
# Where Mission Control's port 3000 listens (docs/plans/security-fixes.md,
# H3), in a Debian container: install.sh loaded as a library
# (HOUSTON_INSTALL_LIB=1), with docker stubbed.
#   - a first install opens it to the network, for setup in a browser
#   - a rerun once Cloudflare is connected binds it to 127.0.0.1
#   - a rerun before that keeps it open, so setup can finish
#   - a rerun that can't tell binds 127.0.0.1, and says how to open it
#   - HOUSTON_BIND chooses; anything but an IPv4 address is refused before
#     anything changes
#   - the report says where to sign in, and how to reach port 3000 with ssh
#
#   install/test/install-bind.sh
set -uo pipefail

repo="$(cd "$(dirname "$0")/../.." && pwd)"
exec docker run --rm -i -v "$repo/install:/install:ro" debian:bookworm-slim bash -s <<'IN_CONTAINER'
set -uo pipefail
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }

# docker: logs its arguments. "compose ... run ... rails runner" (the setup
# check) exits with $CONNECTED; "compose ... exec ... setup_code" prints a
# code only when $SETUP_CODE is set.
mkdir -p /stub /fake && cat > /stub/docker <<'SH' && chmod 755 /stub/docker
#!/bin/sh
echo "docker $*" >> /fake/docker.log
case "$*" in
  *"rails runner"*) exit "${CONNECTED:-1}" ;;
  *houston:setup_code*) [ -n "${SETUP_CODE:-}" ] && echo "$SETUP_CODE" && exit 0; exit 1 ;;
esac
exit 0
SH
cat > /stub/getent <<'SH' && chmod 755 /stub/getent
#!/bin/sh
case "$1" in group) echo "docker:x:999:" ;; passwd) echo "houston:x:1001:1001::/home/houston:/bin/sh" ;; esac
SH
cat > /stub/id <<'SH' && chmod 755 /stub/id
#!/bin/sh
echo 1001
SH
export PATH="/stub:$PATH"

# lib <env...>: install.sh's functions in a fresh subshell, with LIB_CMD.
lib() { env HOUSTON_INSTALL_LIB=1 HOUSTON_DIR=/opt/houston "$@" sh -c '. /install/install.sh; '"$LIB_CMD"; }
ports() { sed -n '/^  mission-control:/,/^  [a-z]/p' /opt/houston/compose.yml | grep -A1 '^    ports:' | tail -1 | tr -d ' "-'; }

bound() { # bound <want> <what> <env...>
  local want=$1 what=$2; shift 2
  : > /fake/docker.log
  out=$(LIB_CMD='choose_bind && write_compose' lib "$@" 2>&1); code=$?
  [ "$code" = 0 ] && [ "$(ports)" = "$want" ] && ok "$what: $want" || bad "$what: wanted $want, got exit $code, ports $(ports): $out"
}

echo "== a first install: open for setup"
rm -rf /opt/houston; mkdir -p /opt/houston
bound "0.0.0.0:3000:80" "no compose.yml yet"
grep -q "rails runner" /fake/docker.log && bad "checked setup with no install to ask" || ok "didn't ask a Mission Control that isn't there"

echo "== reruns"
bound "127.0.0.1:3000:80" "Cloudflare connected" CONNECTED=0
grep -q "compose -f /opt/houston/compose.yml run --rm --no-deps -T mission-control bin/rails runner" /fake/docker.log &&
  ok "asked the installed Mission Control" || bad "setup check: $(cat /fake/docker.log)"
bound "0.0.0.0:3000:80" "setup not finished" CONNECTED=3
bound "127.0.0.1:3000:80" "can't tell" CONNECTED=1
out=$(LIB_CMD='choose_bind && echo "unknown=${bind_unknown:-}"' lib CONNECTED=1 2>&1)
printf '%s' "$out" | grep -qx "unknown=1" && ok "can't tell: the report will say so" || bad "can't tell: $out"

echo "== HOUSTON_BIND chooses"
bound "0.0.0.0:3000:80" "HOUSTON_BIND=0.0.0.0, connected" CONNECTED=0 HOUSTON_BIND=0.0.0.0
bound "100.64.1.2:3000:80" "HOUSTON_BIND=100.64.1.2" CONNECTED=0 HOUSTON_BIND=100.64.1.2
rm -f /opt/houston/compose.yml
bound "127.0.0.1:3000:80" "HOUSTON_BIND=127.0.0.1 on a first install" HOUSTON_BIND=127.0.0.1
for b in "0.0.0.0:80" "localhost" "::" "1.2.3" "1.2.3.4; rm -rf /"; do
  out=$(LIB_CMD='check_bind; echo "not refused"' lib HOUSTON_BIND="$b" 2>&1); code=$?
  [ "$code" = 1 ] && printf '%s' "$out" | grep -qF "HOUSTON_BIND must be an IPv4 address" && ok "refused HOUSTON_BIND=$b" || bad "HOUSTON_BIND=$b: exit $code: $out"
done

echo "== the probe after starting asks where it listens"
out=$(LIB_CMD='bind=100.64.1.2; probe_address' lib 2>&1); [ "$out" = 100.64.1.2 ] && ok "a chosen address is probed there" || bad "probe: $out"
out=$(LIB_CMD='bind=0.0.0.0; probe_address' lib 2>&1); [ "$out" = 127.0.0.1 ] && ok "everywhere is probed on 127.0.0.1" || bad "probe: $out"

echo "== the report"
report() { LIB_CMD='host_address() { echo 192.168.0.56; }; installed_version() { echo v0.4.0; }; '"$1"'; report' lib "${@:2}" 2>&1; }
out=$(report 'bind=0.0.0.0' SETUP_CODE=ABCD-EFGH)
printf '%s' "$out" | grep -qF "Finish setup at  http://192.168.0.56:3000" && printf '%s' "$out" | grep -qF "run this installer again" &&
  ok "setup: the LAN address, and to rerun once it's done" || bad "setup report: $out"
out=$(report 'bind=127.0.0.1')
printf '%s' "$out" | grep -qF "Sign in at https://admin.<your base domain>" && printf '%s' "$out" | grep -qF "ssh -L 3000:127.0.0.1:3000" &&
  ! printf '%s' "$out" | grep -qF "192.168.0.56:3000" && ok "closed: admin.<base>, and ssh -L for port 3000" || bad "closed report: $out"
out=$(report 'bind=127.0.0.1; bind_unknown=1')
printf '%s' "$out" | grep -qF "HOUSTON_BIND=0.0.0.0" && ok "can't tell: says how to open it" || bad "unknown report: $out"
out=$(report 'bind=0.0.0.0')
printf '%s' "$out" | grep -qF "Port 3000 is open to your network" && ok "open after setup: says so" || bad "open report: $out"

echo
if [ "$failures" -eq 0 ]; then echo "INSTALL BIND PASS"; else echo "INSTALL BIND: $failures failure(s)"; exit 1; fi
IN_CONTAINER

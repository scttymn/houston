#!/usr/bin/env bash
# Port 3000 and what Mission Control believes about a request
# (docs/plans/security-fixes.md, H3, M1, L1), on a throwaway OrbStack
# machine with the real installer, Thruster and Docker. Cloudflare isn't
# called: setup is marked connected inside the machine, which is what the
# installer asks.
#   - a first install opens port 3000 to the network; Mission Control sees
#     that from its own container
#   - Settings › Port 3000 closes it (PortSwitch, what the button calls):
#     Mission Control recreates itself bound to 127.0.0.1, compose.yml as it
#     was; a rerun keeps it closed, and says how to reach it
#   - opened again the same way, a rerun keeps it open
#   - HOUSTON_BIND overrides the saved choice for one run
#   - a spoofed X-Forwarded-For or Client-Ip doesn't escape the sign-in limit
#   - a spoofed X-Forwarded-Host doesn't take hooks.<base> to the admin
#   - Mission Control looks up addresses in Docker's DNS (as it does cloudflared's)
#
#   install/test/bind-e2e.sh            (KEEP=1 keeps the machine)
# shellcheck disable=SC2016,SC2015 # single-quoted commands run inside the machine; ok/bad return 0
set -uo pipefail

name="houston-bind-test"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
vm() { orb -m "$name" -u root "$@"; }
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
check() { local what="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi; }
cleanup() { if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi; }
trap cleanup EXIT
mc() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1" 2>/dev/null | tail -1; }

echo "== creating $name"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create debian:bookworm "$name" >/dev/null

echo "== first install"
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" > /tmp/houston-bind-1.log 2>&1 || { bad "first install: $(tail -5 /tmp/houston-bind-1.log)"; exit 1; }
ip=$(vm sh -c "hostname -I | awk '{print \$1}'")
check "port 3000 answers on the network ($ip)" vm curl -fsS -o /dev/null "http://$ip:3000/up"
grep -qF "Settings › Port 3000" /tmp/houston-bind-1.log && ok "the report says where to close port 3000" || bad "report: $(tail -4 /tmp/houston-bind-1.log)"
[ "$(mc 'p PortExposure.open?')" = true ] && ok "Mission Control sees port 3000 open, from its own container" || bad "PortExposure.open? said $(mc 'p PortExposure.open?')"
# cloudflared restarts until setup gives it a token, and Docker's DNS names
# only running containers, so the lookup is proven on the registry.
addresses=$(mc 'ENV["HOUSTON_TUNNEL_HOST"] = "registry"; p ForwardedHeaders.lookup')
[[ "$addresses" =~ ^\[\"[0-9.]+\"\]$ ]] && ok "Mission Control looks up a service in Docker's DNS: $addresses" || bad "lookup: $addresses"

echo "== what Mission Control believes, through the real Thruster"
mc 'Installation.current.update!(base_domain: "houston.test"); User.create!(email_address: "a@houston.test", password: SecureRandom.hex(16))' >/dev/null
code=$(vm curl -s -o /dev/null -w '%{http_code}' -H "Host: hooks.houston.test" -H "X-Forwarded-Host: admin.houston.test" "http://127.0.0.1:3000/sign-in")
[ "$code" = 404 ] && ok "hooks.<base> with a spoofed X-Forwarded-Host: 404" || bad "hooks.<base> with X-Forwarded-Host answered $code"
# 11 failed sign-ins, each claiming another address: the 11th is limited.
read -r -d '' attempts <<'IN_VM' || true
for i in $(seq 1 11); do
  jar=$(mktemp)
  t=$(curl -s -c "$jar" -b "$jar" http://127.0.0.1:3000/sign-in | sed -n 's/.*name="authenticity_token" value="\([^"]*\)".*/\1/p' | head -1)
  curl -s -L -c "$jar" -b "$jar" -H "X-Forwarded-For: 6.6.6.$i" -H "Client-Ip: 7.7.7.$i" http://127.0.0.1:3000/session \
    --data-urlencode "authenticity_token=$t" --data-urlencode email_address=a@houston.test --data-urlencode password=wrong |
    grep -oE 'Try again later\.|Try another email address or password\.' | head -1
  rm -f "$jar"
done
IN_VM
alerts=$(vm bash -c "$attempts")
[ "$(printf '%s\n' "$alerts" | tail -1)" = "Try again later." ] && [ "$(printf '%s\n' "$alerts" | grep -c "Try another")" = 10 ] &&
  ok "spoofed X-Forwarded-For and Client-Ip still hit the sign-in limit" || bad "sign-in alerts: $alerts"

echo "== Settings › Port 3000: close it"
mc 'Installation.current.update!(cloudflare_connected_at: Time.current)' >/dev/null
before=$(vm docker compose -f /opt/houston/compose.yml ps -q mission-control)
mc 'PortSwitch.set!(open: false)' >/dev/null
closed() { ! vm curl -fsS -o /dev/null --max-time 5 "http://$ip:3000/up"; }
for _ in $(seq 1 60); do closed && vm curl -fsS -o /dev/null "http://127.0.0.1:3000/up" 2>/dev/null && break; sleep 2; done
check "port 3000 is closed to the network" closed
check "Mission Control answers on 127.0.0.1:3000" vm curl -fsS -o /dev/null "http://127.0.0.1:3000/up"
after=$(vm docker compose -f /opt/houston/compose.yml ps -q mission-control)
[ -n "$after" ] && [ "$after" != "$before" ] && ok "Mission Control was recreated" || bad "container: before $before, after $after"
vm grep -qF '"${HOUSTON_BIND:-127.0.0.1}:3000:80"' /opt/houston/compose.yml && ok "compose.yml is unchanged" || bad "compose.yml: $(vm grep -n 3000 /opt/houston/compose.yml)"
check "the helper container is gone" sh -c "! orb -m $name -u root docker ps -a --format '{{.Names}}' | grep -qx houston-port-3000"
[ "$(mc 'p [PortExposure.address, Installation.current.port_open]')" = '["127.0.0.1", false]' ] && ok "Mission Control sees it closed, and the choice is saved" || bad "state: $(mc 'p [PortExposure.address, Installation.current.port_open]')"

echo "== a rerun keeps it closed"
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" > /tmp/houston-bind-2.log 2>&1 || bad "rerun: $(tail -5 /tmp/houston-bind-2.log)"
check "port 3000 is still closed to the network" closed
check "Mission Control answers on 127.0.0.1:3000" vm curl -fsS -o /dev/null "http://127.0.0.1:3000/up"
grep -qF "ssh -L 3000:127.0.0.1:3000" /tmp/houston-bind-2.log && ok "the report says how to reach it" || bad "report: $(tail -4 /tmp/houston-bind-2.log)"

echo "== open it again, and a rerun keeps it open"
mc 'PortSwitch.set!(open: true)' >/dev/null
for _ in $(seq 1 60); do vm curl -fsS -o /dev/null --max-time 5 "http://$ip:3000/up" 2>/dev/null && break; sleep 2; done
check "port 3000 answers on the network again" vm curl -fsS -o /dev/null "http://$ip:3000/up"
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" > /tmp/houston-bind-3.log 2>&1 || bad "rerun: $(tail -5 /tmp/houston-bind-3.log)"
check "still open after a rerun" vm curl -fsS -o /dev/null "http://$ip:3000/up"

echo "== HOUSTON_BIND overrides it for one run"
vm env HOUSTON_SOURCE="$repo" HOUSTON_BIND=127.0.0.1 sh "$repo/install/install.sh" > /tmp/houston-bind-4.log 2>&1 || bad "rerun with HOUSTON_BIND: $(tail -5 /tmp/houston-bind-4.log)"
check "closed for that run" closed
[ "$(mc 'p Installation.current.port_open')" = true ] && ok "the saved choice is still open" || bad "saved: $(mc 'p Installation.current.port_open')"

echo
if [ "$failures" -eq 0 ]; then echo "BIND E2E PASS"; else echo "BIND E2E: $failures failure(s)"; exit 1; fi

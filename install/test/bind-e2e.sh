#!/usr/bin/env bash
# Port 3000 and what Mission Control believes about a request
# (docs/plans/security-fixes.md, H3, M1, L1), on a throwaway OrbStack
# machine with the real installer, Thruster and Docker. Cloudflare isn't
# called: setup is marked connected inside the machine, which is what the
# installer asks.
#   - a first install opens port 3000 to the network; Mission Control sees
#     that from its own container
#   - once connected, a rerun binds it to 127.0.0.1, and says how to reach it
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
grep -qF "run this installer again" /tmp/houston-bind-1.log && ok "the report says to rerun once setup is done" || bad "report: $(tail -4 /tmp/houston-bind-1.log)"
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

echo "== connected: the rerun closes port 3000"
mc 'Installation.current.update!(cloudflare_connected_at: Time.current)' >/dev/null
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" > /tmp/houston-bind-2.log 2>&1 || bad "rerun: $(tail -5 /tmp/houston-bind-2.log)"
closed() { ! vm curl -fsS -o /dev/null --max-time 5 "http://$ip:3000/up"; }
check "port 3000 is closed to the network" closed
check "Mission Control answers on 127.0.0.1:3000" vm curl -fsS -o /dev/null "http://127.0.0.1:3000/up"
vm grep -q '"127.0.0.1:3000:80"' /opt/houston/compose.yml && ok "compose.yml binds 127.0.0.1:3000" || bad "compose.yml: $(vm grep -n 3000 /opt/houston/compose.yml)"
grep -qF "ssh -L 3000:127.0.0.1:3000" /tmp/houston-bind-2.log && ok "the report says how to reach it" || bad "report: $(tail -4 /tmp/houston-bind-2.log)"
[ "$(mc 'p PortExposure.open?')" = false ] && ok "Mission Control sees it closed" || bad "PortExposure.open? said $(mc 'p PortExposure.open?')"

echo "== HOUSTON_BIND=0.0.0.0 opens it again"
vm env HOUSTON_SOURCE="$repo" HOUSTON_BIND=0.0.0.0 sh "$repo/install/install.sh" > /tmp/houston-bind-3.log 2>&1 || bad "rerun with HOUSTON_BIND: $(tail -5 /tmp/houston-bind-3.log)"
check "port 3000 answers on the network again" vm curl -fsS -o /dev/null "http://$ip:3000/up"

echo
if [ "$failures" -eq 0 ]; then echo "BIND E2E PASS"; else echo "BIND E2E: $failures failure(s)"; exit 1; fi

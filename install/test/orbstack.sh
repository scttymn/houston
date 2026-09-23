#!/usr/bin/env bash
# Installs Houston on a throwaway OrbStack Linux machine and checks the result.
#
#   install/test/orbstack.sh ubuntu:noble
#   install/test/orbstack.sh debian:bookworm
#
# KEEP=1 keeps the machine afterwards. If mission_control/.houston/cloudflare-check.env
# exists (CLOUDFLARE_API_TOKEN, BASE_DOMAIN), setup step 2 runs against the real
# Cloudflare API from inside the machine; the token is never printed. Afterwards
# the test tunnel and Houston's wildcard record are deleted, unless
# KEEP_CLOUDFLARE=1 (the real install reuses them).
# shellcheck disable=SC2016 # single-quoted commands run inside the machine, not here
set -euo pipefail

distro="${1:?usage: $0 ubuntu:noble|debian:bookworm}"
name="houston-test-${distro//[:.]/-}"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
# A fresh admin password per run: while the machine is up, admin.<base> can
# reach it through the tunnel.
admin_password="test-$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"

vm() { orb -m "$name" -u root "$@"; }
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
check() { local what="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi; }

cf_created=""
cleanup() {
  if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi
  if [ -n "$cf_created" ] && [ "${KEEP_CLOUDFLARE:-}" != 1 ]; then
    echo "== removing the test tunnel and Houston's wildcard (KEEP_CLOUDFLARE=1 keeps them)"
    "$repo/install/test/cloudflare.sh" cleanup || true
  fi
}
trap cleanup EXIT

echo "== creating $name ($distro)"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create "$distro" "$name" >/dev/null

echo "== first install"
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" | tee /tmp/houston-install-1.log
code=$(sed -n 's/^Setup code *//p' /tmp/houston-install-1.log)
ip=$(vm hostname -I | awk '{print $1}')

echo "== checks"
check "docker and compose installed" vm docker compose version
check "houston user is in the docker group" vm sh -c 'id -nG houston | grep -qw docker'
check "houston's key is authorized for local SSH" vm sh -c 'grep -qF "$(cat ~houston/.ssh/id_ed25519.pub)" ~houston/.ssh/authorized_keys'
check ".env is mode 600" vm sh -c '[ "$(stat -c %a /opt/houston/.env)" = 600 ]'
check ".env has the 4 generated secrets" vm sh -c '[ "$(grep -cE "^(SECRET_KEY_BASE|AR_ENCRYPTION_[A-Z_]+)=.{40,}" /opt/houston/.env)" = 4 ]'
for svc in mission-control registry cloudflared; do
  check "$svc container exists and isn't stopped" vm sh -c "docker compose -f /opt/houston/compose.yml ps -a --format '{{.Service}} {{.State}}' | grep -E '^$svc (running|restarting)'"
done
check "registry listens on 127.0.0.1:5000 only" vm sh -c 'ss -ltn | grep ":5000 " | grep -q "127.0.0.1:5000" && ! ss -ltn | grep ":5000 " | grep -qvE "127.0.0.1:5000"'
check "Mission Control answers on the LAN address ($ip)" vm curl -fsS -o /dev/null "http://$ip:3000/up"
if [[ "$code" =~ ^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$ ]]; then ok "printed a setup code ($code)"; else bad "printed a setup code (got '$code')"; fi

echo "== setup step 1 over plain HTTP, with the printed code"
jar=/tmp/houston-jar
token() { vm curl -s -c "$jar" -b "$jar" "http://$ip:3000$1" | grep -o 'name="authenticity_token" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"$//'; }
step1=$(vm curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -c "$jar" -b "$jar" -X POST "http://$ip:3000/setup" \
  --data-urlencode "authenticity_token=$(token /setup)" --data-urlencode "setup[code]=$code" \
  --data-urlencode "setup[email_address]=admin@example.com" \
  --data-urlencode "setup[password]=$admin_password" --data-urlencode "setup[password_confirmation]=$admin_password")
if [ "$step1" = "302 http://$ip:3000/" ]; then ok "step 1 created the admin over HTTP ($step1)"; else bad "step 1 over HTTP (got $step1)"; fi
next=$(vm curl -s -o /dev/null -w '%{redirect_url}' -b "$jar" "http://$ip:3000/")
if [ "$next" = "http://$ip:3000/setup/cloudflare" ]; then ok "signed in; next is the Cloudflare step"; else bad "signed in; next step (got $next)"; fi

cf_file="$repo/mission_control/.houston/cloudflare-check.env"
if [ -f "$cf_file" ]; then
  echo "== setup step 2 against the real Cloudflare API (token read from a file, never printed)"
  base=$(sed -n 's/^BASE_DOMAIN=//p' "$cf_file")
  "$repo/install/test/cloudflare.sh" preflight
  cf_created=yes
  vm sh -c "umask 077; sed -n 's/^CLOUDFLARE_API_TOKEN=//p' '$cf_file' | tr -d '\r\n' > /tmp/cf-token"
  step2=$(vm curl -s -o /tmp/houston-step2.html -w '%{http_code} %{redirect_url}' -c "$jar" -b "$jar" -X POST "http://$ip:3000/setup/cloudflare" \
    --data-urlencode "authenticity_token=$(token /setup/cloudflare)" --data-urlencode "cloudflare[base_domain]=$base" \
    --data-urlencode "cloudflare[api_token]@/tmp/cf-token")
  vm rm -f /tmp/cf-token
  if [ "$step2" = "302 http://$ip:3000/" ]; then
    ok "step 2 created the tunnel, ingress and *.$base"
    connected=""
    for _ in $(seq 1 30); do
      if vm sh -c 'docker compose -f /opt/houston/compose.yml logs cloudflared 2>&1 | grep -q "Registered tunnel connection"'; then connected=yes; break; fi
      sleep 2
    done
    if [ -n "$connected" ]; then ok "cloudflared picked up the token and connected"; else bad "cloudflared connected"; fi
  else
    bad "step 2 (got $step2)"
    vm sh -c "grep -oE 'NO-GO</span><span>[^<]*' /tmp/houston-step2.html | sed 's/.*<span>/    /'" || true
  fi
else
  echo "== (no $cf_file; skipping the real Cloudflare stage)"
fi

echo "== rerun (repair / update)"
env_before=$(vm sha256sum /opt/houston/.env)
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" | tee /tmp/houston-install-2.log
env_after=$(vm sha256sum /opt/houston/.env)
if [ "$env_before" = "$env_after" ]; then ok ".env kept byte-for-byte"; else bad ".env changed on rerun"; fi
if grep -q "Setup is already complete" /tmp/houston-install-2.log; then ok "rerun says setup is already complete"; else bad "rerun output"; fi
check "Mission Control still answers after the rerun" vm curl -fsS -o /dev/null "http://$ip:3000/up"

echo
if [ "$failures" -eq 0 ]; then echo "PASS ($distro)"; else echo "FAIL: $failures check(s) ($distro)"; exit 1; fi

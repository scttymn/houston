#!/usr/bin/env bash
# Installs Houston on a throwaway OrbStack Linux machine and checks the result.
#
#   install/test/orbstack.sh ubuntu:noble      # apt
#   install/test/orbstack.sh debian:bookworm
#   install/test/orbstack.sh fedora            # dnf
#   install/test/orbstack.sh rocky:9
#   install/test/orbstack.sh arch              # pacman
#
# KEEP=1 keeps the machine afterwards. If mission_control/.houston/cloudflare-check.env
# exists (CLOUDFLARE_API_TOKEN, BASE_DOMAIN), setup step 2 runs against the real
# Cloudflare API from inside the machine; the token is never printed. Afterwards
# the test tunnel and the DNS records Houston manages are deleted, unless
# KEEP_CLOUDFLARE=1 (the real install reuses them).
# shellcheck disable=SC2016 # single-quoted commands run inside the machine, not here
set -euo pipefail

distro="${1:?usage: $0 <orb distro[:version]>, e.g. ubuntu:noble, fedora, rocky:9, arch}"
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
    echo "== removing the test tunnel and the records Houston manages (KEEP_CLOUDFLARE=1 keeps them)"
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
# The machine's address (not every distro ships hostname -I).
ip=$(vm sh -c "hostname -I 2>/dev/null | awk '{print \$1}' | grep . || ip -4 route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if (\$i == \"src\") { print \$(i + 1); exit }}'")

echo "== checks"
check "docker and compose installed" vm docker compose version
check "houston user is in the docker group" vm sh -c 'id -nG houston | grep -qw docker'
check "houston's key is authorized for local SSH" vm sh -c 'grep -qF "$(cat ~houston/.ssh/id_ed25519.pub)" ~houston/.ssh/authorized_keys'
check ".env is mode 600" vm sh -c '[ "$(stat -c %a /opt/houston/.env)" = 600 ]'
check ".env has the 5 generated secrets" vm sh -c '[ "$(grep -cE "^(SECRET_KEY_BASE|AR_ENCRYPTION_[A-Z_]+|HOUSTON_RUNNER_TOKEN)=.{43,}" /opt/houston/.env)" = 5 ]'
check "houston has the runner token (0600, its own)" vm sh -c 'f=~houston/.config/houston/runner-token; [ "$(stat -c "%a %U" $f)" = "600 houston" ] && [ "$(cat $f)" = "$(sed -n "s/^HOUSTON_RUNNER_TOKEN=//p" /opt/houston/.env)" ]'
check "git is installed" vm git --version
check "the houston CLI is installed" vm sh -c 'houston --version && hou --version'
check "Kamal's image is pulled" vm docker image inspect ghcr.io/basecamp/kamal:v2.12.0
for svc in mission-control registry cloudflared houston-runner-1 houston-runner-2; do
  check "$svc container exists and isn't stopped" vm sh -c "docker compose -f /opt/houston/compose.yml ps -a --format '{{.Service}} {{.State}}' | grep -E '^$svc (running|restarting)'"
done
check "runners run as the houston user" vm sh -c '[ "$(docker compose -f /opt/houston/compose.yml exec -T houston-runner-1 id -u)" = "$(id -u houston)" ]'
check "runners have git, ssh and the houston CLI" vm docker compose -f /opt/houston/compose.yml exec -T houston-runner-1 sh -c 'git --version && ssh -V 2>&1 && houston --version && docker compose version'
check "Mission Control expects 2 runners and has threads for them" vm sh -c 'docker compose -f /opt/houston/compose.yml exec -T mission-control env | grep -qx HOUSTON_RUNNERS=2 && docker compose -f /opt/houston/compose.yml exec -T mission-control env | grep -qx RAILS_MAX_THREADS=8'
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
if [ -f "$cf_file" ] && ! "$repo/install/test/cloudflare.sh" preflight; then
  bad "Cloudflare preflight (step 2 skipped; see above)"
elif [ -f "$cf_file" ]; then
  echo "== setup step 2 against the real Cloudflare API (token read from a file, never printed)"
  base=$(sed -n 's/^BASE_DOMAIN=//p' "$cf_file")
  cf_created=yes
  vm sh -c "umask 077; sed -n 's/^CLOUDFLARE_API_TOKEN=//p' '$cf_file' | tr -d '\r\n' > /tmp/cf-token"
  step2=$(vm curl -s -o /tmp/houston-step2.html -w '%{http_code} %{redirect_url}' -c "$jar" -b "$jar" -X POST "http://$ip:3000/setup/cloudflare" \
    --data-urlencode "authenticity_token=$(token /setup/cloudflare)" --data-urlencode "cloudflare[base_domain]=$base" \
    --data-urlencode "cloudflare[api_token]@/tmp/cf-token")
  vm rm -f /tmp/cf-token
  if [ "$step2" = "302 http://$ip:3000/" ]; then
    ok "step 2 created the tunnel, its routes and DNS"
    connected=""
    for _ in $(seq 1 30); do
      if vm sh -c 'docker compose -f /opt/houston/compose.yml logs cloudflared 2>&1 | grep -q "Registered tunnel connection"'; then connected=yes; break; fi
      sleep 2
    done
    if [ -n "$connected" ]; then ok "cloudflared picked up the token and connected"; else bad "cloudflared connected"; fi
    # From here on the test goes through Cloudflare's edge, not the VM's LAN address.
    # Cloudflare's edge can take minutes to move a name off an existing wildcard onto a new
    # explicit record (seen on svnmns.com), so each name gets up to 5 minutes.
    through() {
      local want="$1" url="$2" got="" start=$SECONDS
      for _ in $(seq 1 60); do
        got=$(curl -s -o /dev/null -w '%{http_code}' "$url") && [ "$got" = "$want" ] && break
        sleep 5
      done
      if [ "$got" = "$want" ]; then ok "$url answers $got through the tunnel (after $((SECONDS - start))s)"; else bad "$url answered $got through the tunnel after 5 minutes (wanted $want)"; fi
    }

    echo "== setup step 3: a local backup location in the machine"
    step3=$(vm curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -c "$jar" -b "$jar" -X POST "http://$ip:3000/setup/storage" \
      --data-urlencode "authenticity_token=$(token /setup/storage)" --data-urlencode "storage[kind]=local" \
      --data-urlencode "storage[name]=vm-local" --data-urlencode "storage[local_path]=/srv/houston-backups")
    if [ "$step3" = "302 http://$ip:3000/setup/storage" ]; then ok "step 3 initialized a restic repository"; else bad "step 3 (got $step3)"; fi
    check "the repository is on the machine's disk" vm test -f /srv/houston-backups/config
    finish=$(vm curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -c "$jar" -b "$jar" -X POST "http://$ip:3000/setup/storage/finish" \
      --data-urlencode "authenticity_token=$(token /setup/storage)" --data-urlencode "saved=1")
    if [ "$finish" = "302 http://$ip:3000/" ]; then ok "setup finished; the flight board is next"; else bad "finishing setup (got $finish)"; fi

    # Mission Control's own verdict on admin.<base>: it GETs https://admin.<base>/ping
    # through Cloudflare and expects this install's identity back.
    route="" first="" start=$SECONDS
    for _ in $(seq 1 60); do
      route=$(vm curl -s -b "$jar" "http://$ip:3000/" | grep -o 'data-check="admin-route" data-state="[a-z]*"' | sed 's/.*data-state="//;s/"$//' || true)
      first="${first:-$route}"
      [ "$route" = go ] && break
      sleep 5
    done
    if [ "$route" = go ]; then ok "the flight board says admin.$base reaches this Mission Control (after $((SECONDS - start))s; first seen: $first)"; else bad "the flight board's admin.$base route is '$route' after 5 minutes"; fi
    echo "== deploys through the tunnel"
    "$repo/install/test/deploy-through-tunnel.sh" "$name" "$base" || failures=$((failures + 1))
    echo "== push to deploy through the tunnel"
    "$repo/install/test/push-through-tunnel.sh" "$name" "$base" || failures=$((failures + 1))
    echo "== an agent on this Mac, with only HOUSTON_SERVER and HOUSTON_API_TOKEN"
    "$repo/install/test/agent-through-tunnel.sh" "$name" "$base" || failures=$((failures + 1))
    echo "== Houston's maintenance page, through the tunnel"
    "$repo/install/test/maintenance-through-tunnel.sh" "$name" "$base" || failures=$((failures + 1))
    # hooks.<base> answers only the webhook and /ping (build step 4); /up is a 404 there.
    through 200 "https://hooks.$base/ping"
    through 404 "https://hooks.$base/up"
    # Mission Control would redirect this; the hooks hostname only passes /<slug>, so the tunnel answers 404.
    through 404 "https://hooks.$base/setup/cloudflare"
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

#!/usr/bin/env bash
# Build step 6, Batch 1's real run, a stage of install/test/orbstack.sh after
# the agent stage (its houston-agent-test app is serving): Houston's own
# maintenance page, routed at the real tunnel. Times how long Cloudflare
# takes to route the app's hostname to Mission Control and back, and shows
# the page stays up with the app's containers stopped.
#
#   install/test/maintenance-through-tunnel.sh <machine> <base domain>
# shellcheck disable=SC2015 # ok/bad return 0
set -uo pipefail

machine="$1" base="$2"
app="houston-agent-test"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$machine" -u root "$@"; }
code_of() { curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$1"; }
# shellcheck source=install/test/settle.sh
. "$repo/install/test/settle.sh"

token=$(vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner 'puts ApiToken.issue!("maintenance").first' 2>/dev/null | tail -1)
[ -n "$token" ] || { bad "making an API token"; exit 1; }
houston() { (cd "$(mktemp -d)" && HOUSTON_SERVER="https://admin.$base" HOUSTON_API_TOKEN="$token" "$repo/.houston/houston-mac" "$@"); }
url="https://$app.$base"

# Seconds until the answer's code is want (up to 5 minutes).
until_code() {
  local want="$1" start=$SECONDS
  for _ in $(seq 1 300); do [ "$(code_of "$url/up")" = "$want" ] && break; sleep 1; done
  echo $((SECONDS - start))
}

settle 200 "$url/up"
out=$(houston maintenance on --message "Back by 10:00" --project "$app" 2>&1)
printf '%s' "$out" | grep -q "shows a maintenance page" && ok "houston maintenance on" || bad "on: $out"
took=$(until_code 503)
[ "$(code_of "$url/up")" = 503 ] && ok "Cloudflare routed $app.$base to Houston's page after ${took}s" || bad "no maintenance page after ${took}s"
settle 503 "$url/up"
body=$(curl -s --max-time 10 "$url/")
printf '%s' "$body" | grep -q "is down for maintenance" && printf '%s' "$body" | grep -q "Back by 10:00" && ok "the page: the name and the message" || bad "page: $(printf '%s' "$body" | head -c 300)"
[ "$(code_of "$url/session/new")" = 503 ] && ! curl -s --max-time 10 "$url/session/new" | grep -qi "sign in" && ok "Mission Control's sign-in never answers on the app's hostname" || bad "sign-in on the app host"

vm sh -c "docker ps -q --filter label=service=$app | xargs -r docker stop" >/dev/null
settle 503 "$url/up"
curl -s --max-time 10 "$url/" | grep -q "is down for maintenance" && ok "the page stays up with the app's containers stopped" || bad "the app's containers stopped: no page"
vm sh -c "docker ps -aq --filter label=service=$app | xargs -r docker start" >/dev/null

out=$(houston maintenance off --project "$app" 2>&1)
printf '%s' "$out" | grep -q "no maintenance page" && ok "houston maintenance off" || bad "off: $out"
took=$(until_code 200)
[ "$(code_of "$url/up")" = 200 ] && ok "Cloudflare routed $app.$base back to the app after ${took}s" || bad "the app didn't come back after ${took}s"
settle 200 "$url/up"

exit "$((failures > 0))"

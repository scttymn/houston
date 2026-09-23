#!/usr/bin/env bash
# Build step 3, Batch 6: houston deploy end to end on a fresh Houston server
# in an OrbStack machine, with a real Mission Control (Cloudflare is marked
# connected in wildcard mode; the real tunnel is Batch 8's run). Deploys the
# spike fixture (internal/kamal/testdata/spike) plus release/post_deploy hooks.
#
#   install/test/deploy-e2e.sh          # KEEP=1 keeps the machine
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/note return 0
set -uo pipefail

name="houston-deploy-e2e"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }

vm() { orb -m "$name" -u root "$@"; }
as_houston() { orb -m "$name" -u houston bash -lc "$1"; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T "$@"; }
through_proxy() { vm docker run --rm --network kamal curlimages/curl:8.16.0 -s -o "${3:-/dev/stdout}" -w "${4:-}" -H "Host: $1" "http://kamal-proxy$2"; }

cleanup() { if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi; }
trap cleanup EXIT

echo "== creating $name and installing Houston"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-e2e-install.log 2>&1 || { tail -20 /tmp/houston-e2e-install.log; exit 1; }
rails mission-control bin/rails runner 'Installation.current.update!(base_domain: "houston.test", cloudflare_connected_at: Time.current, dns_mode: "wildcard")' >/dev/null
vm docker pull -q curlimages/curl:8.16.0 >/dev/null

echo "== a checkout of the spike fixture, with hooks"
as_houston "rm -rf ~/spike && cp -r '$repo/internal/kamal/testdata/spike' ~/spike && cd ~/spike &&
  printf '  hooks:\n    release: date >> /data/released\n    post_deploy: touch /www/post_deploy\n' >> compose.yml &&
  git init -q -b main && git add -A && git -c user.name=e2e -c user.email=e2e@houston.test commit -qm 'spike'"
commit() { as_houston "cd ~/spike && $1 && git add -A && git -c user.name=e2e -c user.email=e2e@houston.test commit -qm '$2' && git rev-parse HEAD"; }
deploy() { as_houston 'cd ~/spike && houston deploy' >"$1" 2>&1; }

echo "== HOLD: no secrets yet"
deploy /tmp/e2e-1.log; code=$?
if [ "$code" = 1 ] && grep -q 'HOLD' /tmp/e2e-1.log && grep -q 'HOSTILE' /tmp/e2e-1.log && grep -q 'POSTGRES_PASSWORD' /tmp/e2e-1.log; then
  ok "the first deploy holds, naming HOSTILE and POSTGRES_PASSWORD"
else bad "HOLD (exit $code): $(tail -3 /tmp/e2e-1.log)"; fi

hostile='$(touch /workdir/pwned) `id` $HOME ${HOME} $$HOME '\''single'\'' "double" <%= 1 + 1 %> café'
vm env HOSTILE_VALUE="$hostile" docker compose -f /opt/houston/compose.yml exec -T -e HOSTILE_VALUE mission-control bin/rails runner '
  p = Project.find_by!(name: "spike")
  p.secrets.create!(key: "HOSTILE", value: ENV.fetch("HOSTILE_VALUE"))
  p.secrets.create!(key: "POSTGRES_PASSWORD", value: "spike-pw")' >/dev/null

echo "== GO"
first=$(as_houston 'cd ~/spike && git rev-parse HEAD')
deploy /tmp/e2e-2.log; code=$?
[ "$code" = 0 ] && ok "deploy exits 0" || { bad "deploy (exit $code)"; tail -40 /tmp/e2e-2.log; }
check_env() {
  local var="$1" want="$2" got
  got=$(through_proxy spike.houston.test "/env/$var"; printf x); got="${got%x}"
  [ "$got" = "$want"$'\n' ] && ok "$var arrives byte-exact" || bad "$var: got $(printf %q "$got")"
}
check_env HOSTILE "$hostile"
check_env DATABASE_URL "postgres://postgres:spike-pw@spike-db/spike"
check_env DB_HOST spike-db
check_env KAMAL_VERSION "$first"
through_proxy spike.houston.test /env/lookup | grep -q Address && ok "spike-db resolves from the app" || bad "lookup"
[ -n "$(vm docker run --rm -v spike_data:/d busybox:1.37 cat /d/released 2>/dev/null)" ] && ok "the release hook wrote to the data volume" || bad "release hook"
[ "$(through_proxy spike.houston.test /post_deploy /dev/null '%{http_code}')" = 200 ] && ok "post_deploy ran in the new container" || bad "post_deploy"
as_houston 'cd ~/spike && git status --porcelain' | grep -q . && bad "the checkout is dirty after a deploy" || ok "the checkout stays clean (.houston ignores itself)"

echo "== a zero-downtime redeploy"
second=$(commit 'echo two > version.txt' 'two')
vm docker run -d --rm --name e2e-poller --network kamal curlimages/curl:8.16.0 sh -c \
  'while :; do curl -s -o /dev/null -w "%{http_code}\n" -H "Host: spike.houston.test" http://kamal-proxy/up; sleep 0.1; done' >/dev/null
deploy /tmp/e2e-3.log; code=$?
codes=$(vm docker logs e2e-poller 2>/dev/null); vm docker rm -f e2e-poller >/dev/null
total=$(printf '%s\n' "$codes" | grep -c .); non200=$(printf '%s\n' "$codes" | grep -vc '^200$')
[ "$code" = 0 ] && [ "$non200" = 0 ] && ok "redeployed; $total requests during it, all 200" || bad "redeploy exit $code, $non200/$total not 200"
check_env KAMAL_VERSION "$second"

echo "== a failing release hook"
commit "sed -i 's|release: date >> /data/released|release: exit 3|' compose.yml" 'broken release' >/dev/null
deploy /tmp/e2e-4.log; code=$?
[ "$code" = 1 ] && grep -q 'release hook failed (exit 3)' /tmp/e2e-4.log && ok "NO-GO: release hook failed (exit 3)" || bad "failing release (exit $code): $(tail -3 /tmp/e2e-4.log)"
check_env KAMAL_VERSION "$second"

echo "== two deploys at once"
commit "sed -i 's|release: exit 3|release: date >> /data/released|' compose.yml" 'fixed release' >/dev/null
as_houston 'cd ~/spike && { (houston deploy >/tmp/e2e-a.log 2>&1; echo $? >/tmp/e2e-a.code) & (sleep 0.2; houston deploy >/tmp/e2e-b.log 2>&1; echo $? >/tmp/e2e-b.code) & wait; }'
codes=$(as_houston 'cat /tmp/e2e-a.code /tmp/e2e-b.code' | sort | tr '\n' ' ')
[ "$codes" = "0 1 " ] && as_houston 'cat /tmp/e2e-a.log /tmp/e2e-b.log' | grep -q 'in flight' && ok "one ran, one was refused as in flight" || bad "concurrent deploys: exits $codes"

echo "== Mission Control's records"
records=$(rails mission-control bin/rails runner 'puts Project.find_by!(name: "spike").deploys.order(:number).map { |d| [d.number, d.status, d.error.to_s[0, 40], d.log.include?("output") || d.log.include?("Deploy #")].join("|") }')
printf '%s\n' "$records" | sed 's/^/    /'
[ "$(printf '%s\n' "$records" | cut -d'|' -f1,2 | tr '\n' ' ')" = "1|go 2|go 3|no_go 4|go " ] && ok "deploys #1–#4: go, go, no_go, go" || bad "records"
printf '%s\n' "$records" | grep -q '^3|no_go|release hook failed (exit 3)' && ok "#3 says why" || bad "#3's error"

echo
if [ "$failures" -eq 0 ]; then echo "E2E PASS"; else echo "E2E: $failures failure(s)"; exit 1; fi

#!/usr/bin/env bash
# The deploy stage of install/test/orbstack.sh, on a machine whose Mission
# Control is set up with the real Cloudflare tunnel (host-by-host mode on a
# shared base domain). Deploys the spike fixture as houston-spike-test and,
# with EQUIP_SOURCE (a Houston-ready copy of equip; never your repo), equip as
# houston-equip-test, then checks both through Cloudflare from this Mac.
#
#   install/test/deploy-through-tunnel.sh <machine> <base domain>
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

machine="$1" base="$2"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
stage="$repo/.houston/e2e" # .houston ignores itself; the machine sees /Users
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$machine" -u root "$@"; }
as_houston() { orb -m "$machine" -u houston bash -lc "$1"; }
runner() { vm sh -c "$1"; } # a shell in the machine, for Mission Control's rails runner
gitc='git -c user.name=houston-test -c user.email=test@houston.invalid'
code_of() { curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$1"; }
# Cloudflare's edge can take a while to move a name off the other server's
# wildcard onto the new record, so every first look waits up to 5 minutes.
wait_for() {
  local want="$1" url="$2" got="" start=$SECONDS
  for _ in $(seq 1 60); do got=$(code_of "$url"); [ "$got" = "$want" ] && break; sleep 5; done
  [ "$got" = "$want" ] && ok "$url answers $got through Cloudflare (after $((SECONDS - start))s)" || bad "$url answered $got after 5 minutes (wanted $want)"
}

# Never deploy over a name something already answers on: equip.svnmns.com is
# a live app on the other server, which is why the test names are these.
for app in houston-spike-test houston-equip-test; do
  got=$(code_of "https://$app.$base/")
  if [ "$got" != 404 ]; then
    bad "https://$app.$base/ answers $got before the test (wanted the other server's 404); not deploying over it"
    exit 1
  fi
done
ok "houston-spike-test and houston-equip-test are unused (the other server's 404)"

echo "== spike as houston-spike-test"
as_houston "rm -rf ~/spike && cp -r '$repo/internal/kamal/testdata/spike' ~/spike && cd ~/spike &&
  sed -i 's/^name: spike\$/name: houston-spike-test/' compose.yml && git init -q -b main && git add -A && $gitc commit -qm spike"
as_houston 'cd ~/spike && houston deploy' >/tmp/houston-spike-1.log 2>&1
grep -q 'HOLD' /tmp/houston-spike-1.log && ok "the first deploy holds for HOSTILE and POSTGRES_PASSWORD" || bad "HOLD: $(tail -2 /tmp/houston-spike-1.log)"
hostile='$(touch /workdir/pwned) `id` $HOME ${HOME} $$HOME '\''single'\'' "double" <%= 1 + 1 %> café'
vm env HOSTILE_VALUE="$hostile" docker compose -f /opt/houston/compose.yml exec -T -e HOSTILE_VALUE mission-control bin/rails runner '
  p = Project.find_by!(name: "houston-spike-test")
  p.secrets.create!(key: "HOSTILE", value: ENV.fetch("HOSTILE_VALUE"))
  p.secrets.create!(key: "POSTGRES_PASSWORD", value: SecureRandom.hex(16))' >/dev/null
if as_houston 'cd ~/spike && houston deploy' >/tmp/houston-spike-2.log 2>&1; then ok "GO"; else bad "deploy"; tail -20 /tmp/houston-spike-2.log; fi
wait_for 200 "https://houston-spike-test.$base/up"
got=$(curl -s --max-time 10 "https://houston-spike-test.$base/env/HOSTILE"; printf x); got="${got%x}"
[ "$got" = "$hostile"$'\n' ] && ok "HOSTILE arrives byte-exact through Cloudflare" || bad "HOSTILE through Cloudflare: $(printf %q "$got")"

[ -n "${EQUIP_SOURCE:-}" ] || { echo "  (no EQUIP_SOURCE; skipping equip)"; exit "$((failures > 0))"; }
echo "== equip (a copy) as houston-equip-test"
rm -rf "$stage" && mkdir -p "$stage/equip"
tar -C "$EQUIP_SOURCE" --exclude=./.houston --exclude=./.kamal --exclude=./.git --exclude=./.claude --exclude=./tmp --exclude=./log \
  --exclude=./storage --exclude=./node_modules --exclude=./.env --exclude=./config/master.key -cf - . | tar -C "$stage/equip" -xf -
# What git keeps of the excluded folders: their .keep files. Without
# storage/.keep the image has no /rails/storage, the volume's mount point is
# root's, and Rails (uid 1000) can't open its SQLite database.
for keep in storage/.keep tmp/.keep log/.keep tmp/pids/.keep tmp/storage/.keep; do
  if [ -f "$EQUIP_SOURCE/$keep" ]; then mkdir -p "$stage/equip/$(dirname "$keep")" && cp "$EQUIP_SOURCE/$keep" "$stage/equip/$keep"; fi
done
(umask 077 && cp "$EQUIP_SOURCE/config/master.key" "$stage/master.key")
as_houston "rm -rf ~/equip && cp -r '$stage/equip' ~/equip && cd ~/equip &&
  sed -i -e 's/^name: equip\$/name: houston-equip-test/' -e 's/\${RAILS_MASTER_KEY:-}/\${RAILS_MASTER_KEY}/' compose.yml &&
  git init -q -b main && git add -A && $gitc commit -qm equip"
as_houston 'cd ~/equip && houston deploy' >/tmp/houston-equip-1.log 2>&1
grep -q 'HOLD' /tmp/houston-equip-1.log && ok "the first deploy holds for RAILS_MASTER_KEY" || bad "HOLD: $(tail -2 /tmp/houston-equip-1.log)"
# The key goes from the staged file straight into rails runner's environment.
runner "V=\$(cat '$stage/master.key') docker compose -f /opt/houston/compose.yml exec -T -e V mission-control bin/rails runner 'Project.find_by!(name: \"houston-equip-test\").secrets.create!(key: \"RAILS_MASTER_KEY\", value: ENV.fetch(\"V\"))'" >/dev/null
rm -f "$stage/master.key"
if as_houston 'cd ~/equip && houston deploy' >/tmp/houston-equip-2.log 2>&1; then ok "GO"; else bad "deploy"; tail -30 /tmp/houston-equip-2.log; fi
wait_for 200 "https://houston-equip-test.$base/up"

echo "== equip redeploy, polled through Cloudflare"
as_houston "cd ~/equip && date > .houston-test-marker && git add -A && $gitc commit -qm redeploy" >/dev/null
(while :; do code_of "https://houston-equip-test.$base/up"; echo; sleep 0.5; done) >/tmp/houston-equip-poll.log 2>/dev/null &
poller=$!
as_houston 'cd ~/equip && houston deploy' >/tmp/houston-equip-3.log 2>&1; code=$?
sleep 2; kill "$poller" 2>/dev/null; wait "$poller" 2>/dev/null
total=$(grep -c . /tmp/houston-equip-poll.log); non200=$(grep -vc '^200$' /tmp/houston-equip-poll.log)
[ "$code" = 0 ] && [ "$non200" = 0 ] && ok "redeployed; $total requests through Cloudflare during it, all 200" || bad "redeploy exit $code; $non200 of $total not 200: $(sort /tmp/houston-equip-poll.log | uniq -c | tr '\n' ' ')"

echo "== equip with a broken health path"
as_houston "cd ~/equip && sed -i 's|health: /up|health: /not-a-health-check|' compose.yml && git add -A && $gitc commit -qm 'broken health'" >/dev/null
as_houston 'cd ~/equip && houston deploy' >/tmp/houston-equip-4.log 2>&1; code=$?
[ "$code" = 1 ] && grep -q 'kamal deploy failed' /tmp/houston-equip-4.log && ok "NO-GO: kamal deploy failed" || bad "broken health (exit $code): $(tail -3 /tmp/houston-equip-4.log)"
[ "$(code_of "https://houston-equip-test.$base/up")" = 200 ] && ok "the previous version still answers 200" || bad "the previous version stopped answering"

records=$(vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner '
  Project.order(:name).each { |p| puts "#{p.name}: " + p.deploys.order(:number).map { |d| "##{d.number} #{d.status}" }.join(", ") }')
printf '%s\n' "$records" | sed 's/^/    /'
exit "$((failures > 0))"

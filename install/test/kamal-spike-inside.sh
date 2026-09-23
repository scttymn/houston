#!/usr/bin/env bash
# Runs inside the spike's machine as root; see kamal-spike.sh.
# shellcheck disable=SC2016,SC2015 # quoted strings expand in containers; ok/note always return 0
set -uo pipefail

repo="$1"
fixture="$repo/internal/kamal/testdata"
work=/tmp/spike-deploy
kamal_image="ghcr.io/basecamp/kamal:v2.12.0"
failures=0

ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
note() { printf '  note  %s\n' "$*"; }

# The secret values Mission Control would supply. HOSTILE tries every way a
# value could turn into a command in the Kamal container: if one works, a file
# appears in the working directory, which is mounted from this machine.
export HOUSTON_S_KAMAL_REGISTRY_PASSWORD="not-checked"
export HOUSTON_S_POSTGRES_PASSWORD="spike-pw"
export HOUSTON_S_APP__DATABASE_URL="postgres://postgres:spike-pw@spike-db/spike"
hostile='$(touch /workdir/pwned-dollar) `touch /workdir/pwned-backtick` $HOME ${HOME} $$HOME '\''single'\'' "double" <%= 1 + 1 %> café'
# What houston deploy passes for it (kamal.CarrierValue; TestCarrierValueSpikeFixture
# pins this string): a backslash before each $ that isn't $(.
export HOUSTON_S_HOSTILE='$(touch /workdir/pwned-dollar) `touch /workdir/pwned-backtick` \$HOME \${HOME} \$\$HOME '\''single'\'' "double" <%= 1 + 1 %> café'

kamal() {
  docker run --rm --name houston-kamal --network host \
    -v "$work:/workdir" -v /var/run/docker.sock:/var/run/docker.sock -v /home/houston/.ssh:/ssh:ro \
    -e HOUSTON_S_KAMAL_REGISTRY_PASSWORD -e HOUSTON_S_POSTGRES_PASSWORD -e HOUSTON_S_APP__DATABASE_URL -e HOUSTON_S_HOSTILE \
    "$kamal_image" "$@"
}
# A request through kamal-proxy from the kamal network, as cloudflared makes it.
through_proxy() { docker run --rm --network kamal curlimages/curl:8.16.0 -s -H "Host: $1" "http://kamal-proxy$2"; }

rm -rf "$work" && mkdir -p "$work/config" "$work/.kamal"
cp "$fixture/spike.deploy.yml" "$work/config/deploy.yml"
cp "$fixture/spike.secrets" "$work/.kamal/secrets"

echo "== image: built and pushed by Houston, not Kamal"
# Kamal only deploys images labelled service=<name>; its own builder adds it.
docker build -q --target production --label service=spike -t spike-build "$fixture/spike" >/dev/null
for v in v1 v2 v3; do
  docker tag spike-build "127.0.0.1:5000/spike:$v" && docker push -q "127.0.0.1:5000/spike:$v" >/dev/null
done
docker pull -q "$kamal_image" >/dev/null
docker pull -q curlimages/curl:8.16.0 >/dev/null

echo "== accessories, then the first deploy"
# Kamal's working directory isn't a git checkout, so every command gets --version.
if kamal accessory boot all --version v1 >/tmp/spike-accessory-1.log 2>&1 && docker ps --format '{{.Names}}' | grep -qx spike-db; then
  ok "accessory spike-db booted"
else
  tail -30 /tmp/spike-accessory-1.log; bad "accessory boot"
fi
if kamal deploy --skip-push --version v1 >/tmp/spike-deploy-1.log 2>&1; then ok "kamal deploy v1"; else tail -40 /tmp/spike-deploy-1.log; bad "kamal deploy v1"; fi

echo "== S1 registry at 127.0.0.1:5000 with a dummy login"
if docker ps -a --format '{{.Names}}' | grep -q kamal-docker-registry; then bad "S1 Kamal started its own registry"; else ok "S1 no kamal-docker-registry"; fi
if grep -q 'docker login 127.0.0.1:5000' /tmp/spike-deploy-1.log; then note "S1 Kamal ran docker login against Houston's registry"; fi
docker ps --format '{{.Image}}' | grep -q '^127.0.0.1:5000/spike:v1$' && ok "S1 the app runs 127.0.0.1:5000/spike:v1" || bad "S1 app image"

echo "== S2 SSH houston@127.0.0.1"
grep -qiE 'host key|known_hosts|fingerprint' /tmp/spike-deploy-1.log && note "S2 host-key lines: $(grep -iE 'host key|known_hosts|fingerprint' /tmp/spike-deploy-1.log | head -2 | tr '\n' ' ')" || ok "S2 no host-key prompt or warning"

echo "== S3/S4 values arrive byte-exact"
check_env() {
  local var="$1" want="$2" got
  got=$(through_proxy spike.houston.test "/env/$var"; printf x)
  got="${got%x}"
  if [ "$got" = "$want"$'\n' ]; then ok "$var byte-exact"; else bad "$var: got $(printf %q "$got") want $(printf %q "$want")"; fi
}
check_env HOSTILE "$hostile"
check_env ERB '<%= 1 + 1 %>'
check_env PLAIN 'a: b - c # {x} café'
check_env DOLLAR 'cost $5'
check_env DB_HOST spike-db
check_env DATABASE_URL "$HOUSTON_S_APP__DATABASE_URL"
if ls "$work"/pwned-* >/dev/null 2>&1; then bad "S3 a secret value ran a command: $(ls "$work"/pwned-*)"; else ok "S3 no secret value ran as a command"; fi

echo "== S5 the accessory resolves from the app"
through_proxy spike.houston.test /env/lookup | grep -q 'Address' && ok "S5 spike-db resolves" || { bad "S5 lookup"; through_proxy spike.houston.test /env/lookup; }

echo "== S6 both hosts route; kamal-proxy takes no host ports"
[ "$(through_proxy spike.houston.test /up)" = ok ] && ok "S6 spike.houston.test" || bad "S6 spike.houston.test"
[ "$(through_proxy spike-alias.houston.test /up)" = ok ] && ok "S6 spike-alias.houston.test" || bad "S6 spike-alias.houston.test"
if ss -ltn | grep -qE ':(80|443) '; then bad "S6 host ports: $(ss -ltn | grep -E ':(80|443) ')"; else ok "S6 nothing listens on host ports 80/443"; fi

echo "== S7 zero-downtime second deploy"
docker run -d --rm --name spike-poller --network kamal curlimages/curl:8.16.0 sh -c \
  'while :; do curl -s -o /dev/null -w "%{http_code}\n" -H "Host: spike.houston.test" http://kamal-proxy/up; sleep 0.1; done' >/dev/null
sleep 2
if kamal deploy --skip-push --version v2 >/tmp/spike-deploy-2.log 2>&1; then ok "kamal deploy v2"; else tail -30 /tmp/spike-deploy-2.log; bad "kamal deploy v2"; fi
sleep 2
codes=$(docker logs spike-poller 2>/dev/null); docker rm -f spike-poller >/dev/null
total=$(printf '%s\n' "$codes" | grep -c .); non200=$(printf '%s\n' "$codes" | grep -vc '^200$')
[ "$non200" = 0 ] && ok "S7 $total requests during the deploy, all 200" || bad "S7 $non200 of $total requests weren't 200: $(printf '%s\n' "$codes" | sort | uniq -c | tr '\n' ' ')"
docker ps --format '{{.Image}}' | grep -q '^127.0.0.1:5000/spike:v2$' && ok "S7 v2 is running" || bad "S7 v2 running"

echo "== S8 a one-off container of the new image reaches the accessory"
docker run --rm --network kamal 127.0.0.1:5000/spike:v3 nslookup spike-db >/dev/null 2>&1 && ok "S8 one-off v3 container resolves spike-db" || bad "S8 one-off lookup"

echo "== S9 a deploy killed mid-way"
kamal deploy --skip-push --version v3 >/tmp/spike-deploy-3.log 2>&1 &
sleep 4; docker kill houston-kamal >/dev/null 2>&1; wait 2>/dev/null
note "S9 killed after: $(grep -vE '^\s*$' /tmp/spike-deploy-3.log | tail -1)"
kamal deploy --skip-push --version v3 >/tmp/spike-deploy-4.log 2>&1; rc=$?
note "S9 next deploy exit $rc: $(grep -iE 'lock' /tmp/spike-deploy-4.log | head -3 | tr '\n' ' ')"
if [ "$rc" != 0 ]; then
  kamal lock release --version v3 >/tmp/spike-lock.log 2>&1 && note "S9 kamal lock release: ok"
  kamal deploy --skip-push --version v3 >/tmp/spike-deploy-5.log 2>&1 && ok "S9 deploy works after the lock is released" || { tail -20 /tmp/spike-deploy-5.log; bad "S9 deploy after release"; }
else
  ok "S9 no stale lock after the kill"
fi

echo "== S10 booting accessories again"
kamal accessory boot all --version v3 >/tmp/spike-accessory-2.log 2>&1; rc=$?
note "S10 exit $rc: $(grep -iE 'error|already|conflict|running' /tmp/spike-accessory-2.log | head -2 | tr '\n' ' ')"

echo
if [ "$failures" -eq 0 ]; then echo "SPIKE PASS"; else echo "SPIKE: $failures failure(s)"; exit 1; fi

#!/usr/bin/env bash
# Build step 4's real run, a stage of install/test/orbstack.sh: on a machine
# whose Mission Control has the real Cloudflare tunnel, a Forgejo repo's own
# webhook (to https://hooks.<base>, out through Cloudflare and back in)
# makes a runner test and deploy each push.
#
#   install/test/push-through-tunnel.sh <machine> <base domain>
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

machine="$1" base="$2"
app="houston-push-test"
elsewhere="$app.example.invalid" # a custom domain in no Cloudflare zone
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$machine" -u root "$@"; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1"; }
code_of() { curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$1"; }
wait_for() {
  local want="$1" url="$2" got="" start=$SECONDS
  for _ in $(seq 1 60); do got=$(code_of "$url"); [ "$got" = "$want" ] && break; sleep 5; done
  [ "$got" = "$want" ] && ok "$url answers $got through Cloudflare (after $((SECONDS - start))s)" || bad "$url answered $got after 5 minutes (wanted $want)"
}
wait_for_deploy() { # number → "status|runner|error"
  local got=""
  for _ in $(seq 1 90); do
    got=$(rails "d = Project.find_by(name: '$app')&.deploys&.find_by(number: $1); puts d ? [d.status, d.runner, d.error].join('|') : 'none'" 2>/dev/null | tail -1)
    case "$got" in go* | no_go*) break ;; esac
    sleep 5
  done
  printf '%s' "$got"
}

got=$(code_of "https://$app.$base/")
[ "$got" = 404 ] || { bad "https://$app.$base/ answers $got before the test (wanted the other server's 404); not deploying over it"; exit 1; }
ok "$app.$base is unused (the other server's 404)"

echo "== Forgejo in the machine, with a webhook to https://hooks.$base/$app"
pw="fj-$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
vm docker run -d --name forgejo --network houston_default -p 127.0.0.1:3001:3000 \
  -e FORGEJO__security__INSTALL_LOCK=true -e FORGEJO__server__SSH_DOMAIN=forgejo -e FORGEJO__server__SSH_PORT=22 \
  -e FORGEJO__webhook__ALLOWED_HOST_LIST=external codeberg.org/forgejo/forgejo:13 >/dev/null
for _ in $(seq 1 60); do vm curl -fs -o /dev/null http://127.0.0.1:3001/api/v1/version && break; sleep 2; done
vm docker exec -u git forgejo forgejo admin user create --username houston --password "$pw" --email houston@test.invalid --admin --must-change-password=false >/dev/null
fj() { vm curl -s -u "houston:$pw" -H 'Content-Type: application/json' "http://127.0.0.1:3001/api/v1$1" "${@:2}"; }
fj /user/repos -X POST -d "{\"name\":\"$app\",\"private\":true,\"default_branch\":\"main\"}" >/dev/null
gitc='git -c user.name=t -c user.email=t@test.invalid'
vm sh -c "rm -rf /tmp/$app && cp -r '$repo/internal/kamal/testdata/spike' /tmp/$app && cd /tmp/$app &&
  sed -i -e 's/^name: spike\$/name: $app/' -e 's/domains: \\[spike-alias.houston.test\\]/domains: [$elsewhere]/' compose.yml &&
  printf '  commands: { test: \"test -x /entry.sh\" }\n' >> compose.yml &&
  git init -q -b main && git add -A && $gitc commit -qm first && git remote add origin 'http://houston:$pw@127.0.0.1:3001/houston/$app.git' && git push -q origin main"
push() { vm sh -c "cd /tmp/$app && $1 && git add -A && $gitc commit -qm '$2' && git push -q origin main && git rev-parse HEAD"; }

echo "== linked in Mission Control (its models, as Add project does)"
key=$(rails "puts RepoLink.start!('git@forgejo:houston/$app.git').deploy_key_public")
fj "/repos/houston/$app/keys" -X POST -d "{\"title\":\"houston\",\"key\":\"$key\",\"read_only\":true}" >/dev/null
secret=$(rails '
  link = RepoLink.last
  raise GitRemote.check(link).message unless GitRemote.check(link).ok
  read = GitRemote.read(link)
  raise read.problems unless read.ok
  link.update!(preview: read.inspection, preview_sha: read.sha)
  project = ProjectLinking.new(link).save!
  project.secrets.create!(key: "HOSTILE", value: "pushed through the tunnel")
  project.secrets.create!(key: "POSTGRES_PASSWORD", value: SecureRandom.hex(16))
  puts project.webhook_secret' 2>&1 | tail -1)
[ ${#secret} -ge 43 ] && ok "linked; secrets set" || { bad "linking: $secret"; exit 1; }
fj "/repos/houston/$app/hooks" -X POST -d "{\"type\":\"forgejo\",\"active\":true,\"events\":[\"push\"],
  \"config\":{\"url\":\"https://hooks.$base/$app\",\"content_type\":\"json\",\"secret\":\"$secret\"}}" >/dev/null
ok "Forgejo's webhook points at https://hooks.$base/$app"

echo "== a push → Forgejo's webhook through Cloudflare → a runner deploys it"
first=$(push 'echo one > version.txt' 'one')
result=$(wait_for_deploy 1)
case "$result" in go\|houston-runner-*) ok "deploy #1 GO, run by ${result#go|}" ;; *) bad "deploy #1: $result"; rails "puts Project.find_by(name: '$app')&.deploys&.last&.log.to_s" 2>/dev/null | tail -20 | sed 's/^/      /' ;; esac
[ -n "$(rails "puts Project.find_by!(name: '$app').webhook_verified_at" 2>/dev/null | tail -1)" ] && ok "the webhook's first delivery through the tunnel was verified" || bad "webhook never verified"
wait_for 200 "https://$app.$base/up"
[ "$(curl -s --max-time 10 "https://$app.$base/env/KAMAL_VERSION")" = "$first" ] && ok "https://$app.$base serves $first" || bad "serving the wrong version"
state=$(rails "puts Project.find_by!(name: '$app').domain_states.dig('$elsewhere', 'state')" 2>/dev/null | tail -1)
[ "$state" = "ZONE NOT IN CLOUDFLARE YET" ] && ok "$elsewhere: ZONE NOT IN CLOUDFLARE YET" || bad "$elsewhere: $state"

echo "== the next push, polled through Cloudflare"
(while :; do code_of "https://$app.$base/up"; echo; sleep 0.5; done) >/tmp/houston-push-poll.log 2>/dev/null &
poller=$!
second=$(push 'echo two > version.txt' 'two')
result=$(wait_for_deploy 2)
sleep 2; kill "$poller" 2>/dev/null; wait "$poller" 2>/dev/null
total=$(grep -c . /tmp/houston-push-poll.log); non200=$(grep -vc '^200$' /tmp/houston-push-poll.log)
[ "${result%%|*}" = go ] && [ "$non200" = 0 ] && ok "deploy #2 GO; $total requests through Cloudflare during it, all 200" || bad "deploy #2 $result; $non200 of $total not 200"
[ "$(curl -s --max-time 10 "https://$app.$base/env/KAMAL_VERSION")" = "$second" ] && ok "https://$app.$base serves $second" || bad "serving the wrong version after #2"

exit "$((failures > 0))"

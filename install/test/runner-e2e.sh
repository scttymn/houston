#!/usr/bin/env bash
# Build step 4, Batch 5: queued deploys run by houston-runner-N on a fresh
# Houston server (OrbStack machine). A Forgejo container is the git host;
# Cloudflare is marked connected in wildcard mode (houston.test). Linking,
# secrets and checks are driven with rails runner (the pages are covered by
# link-repo.sh); webhooks are signed POSTs to hooks.houston.test.
#
#   install/test/runner-e2e.sh          # KEEP=1 keeps the machine
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

name="houston-runner-e2e"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$name" -u root "$@"; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1"; }
through_proxy() { vm docker run --rm --network kamal curlimages/curl:8.16.0 -s -H "Host: spike.houston.test" "http://kamal-proxy$1"; }

cleanup() { if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi; }
trap cleanup EXIT

echo "== creating $name and installing Houston"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-runner-install.log 2>&1 || { tail -20 /tmp/houston-runner-install.log; exit 1; }
rails 'Installation.current.update!(base_domain: "houston.test", cloudflare_connected_at: Time.current, dns_mode: "wildcard")' >/dev/null
vm docker pull -q curlimages/curl:8.16.0 >/dev/null

echo "== Forgejo with the spike fixture (plus commands.test)"
pw="fj-$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
vm docker run -d --name forgejo --network houston_default -p 127.0.0.1:3001:3000 \
  -e FORGEJO__security__INSTALL_LOCK=true -e FORGEJO__server__SSH_DOMAIN=forgejo -e FORGEJO__server__SSH_PORT=22 \
  codeberg.org/forgejo/forgejo:13 >/dev/null
for _ in $(seq 1 60); do vm curl -fs -o /dev/null http://127.0.0.1:3001/api/v1/version && break; sleep 2; done
vm docker exec -u git forgejo forgejo admin user create --username houston --password "$pw" --email houston@test.invalid --admin --must-change-password=false >/dev/null
fj() { vm curl -s -u "houston:$pw" -H 'Content-Type: application/json' "http://127.0.0.1:3001/api/v1$1" "${@:2}"; }
fj /user/repos -X POST -d '{"name":"spike","private":true,"default_branch":"main"}' >/dev/null
gitc='git -c user.name=t -c user.email=t@test.invalid'
vm sh -c "rm -rf /tmp/spike && cp -r '$repo/internal/kamal/testdata/spike' /tmp/spike && cd /tmp/spike &&
  printf '  commands: { test: \"test -x /entry.sh\" }\n' >> compose.yml && git init -q -b main && git add -A && $gitc commit -qm spike &&
  git remote add origin 'http://houston:$pw@127.0.0.1:3001/houston/spike.git' && git push -q origin main"
push() { vm sh -c "cd /tmp/spike && $1 && git add -A && $gitc commit -qm '$2' && git push -q origin main && git rev-parse HEAD"; }
first=$(vm sh -c 'cd /tmp/spike && git rev-parse HEAD')

echo "== linking it, as Add project would"
key=$(rails 'puts RepoLink.start!("git@forgejo:houston/spike.git").deploy_key_public')
fj /repos/houston/spike/keys -X POST -d "{\"title\":\"houston\",\"key\":\"$key\",\"read_only\":true}" >/dev/null
secret=$(rails '
  link = RepoLink.last
  raise "no access: #{GitRemote.check(link).message}" unless GitRemote.check(link).ok
  read = GitRemote.read(link)
  raise "read: #{read.problems}" unless read.ok
  link.update!(preview: read.inspection, preview_sha: read.sha)
  project = ProjectLinking.new(link).save!
  project.secrets.create!(key: "HOSTILE", value: "hello from a runner")
  project.secrets.create!(key: "POSTGRES_PASSWORD", value: SecureRandom.hex(16))
  puts project.webhook_secret' 2>&1 | tail -1)
[ ${#secret} -ge 43 ] && ok "linked; secrets set" || bad "linking: $secret"

wait_for_deploy() { # number, then prints "status|runner|error"
  local got=""
  for _ in $(seq 1 90); do
    got=$(rails "d = Project.find_by!(name: 'spike').deploys.find_by(number: $1); puts d ? [d.status, d.runner, d.error].join('|') : 'none'" 2>/dev/null | tail -1)
    case "$got" in go* | no_go*) break ;; esac
    sleep 5
  done
  printf '%s' "$got"
}

echo "== Check for changes → a runner deploys it"
rails 'ChangeCheck.new(Project.find_by!(name: "spike")).run' >/dev/null
result=$(wait_for_deploy 1)
case "$result" in go\|houston-runner-*) ok "deploy #1 GO, run by ${result#go|}" ;; *) bad "deploy #1: $result"; rails 'puts Project.find_by!(name: "spike").deploys.find_by!(number: 1).log' 2>/dev/null | tail -25 | sed 's/^/      /' ;; esac
rails 'puts Project.find_by!(name: "spike").deploys.find_by!(number: 1).log' 2>/dev/null | grep -qE 'spike-test-[0-9a-f]{8}' && ok "step 00 ran the tests (houston test's project in the log)" || bad "no test run in the log"
[ "$(through_proxy /env/KAMAL_VERSION)" = "$first" ] && ok "kamal-proxy serves $first" || bad "serving: $(through_proxy /env/KAMAL_VERSION)"
[ "$(through_proxy /env/HOSTILE)" = "hello from a runner" ] && ok "secrets reached the app" || bad "HOSTILE"
[ "$(rails 'puts Runner.live_count' 2>/dev/null | tail -1)" = 2 ] && ok "RUNNERS 2/2 polling" || bad "runners live: $(rails 'puts Runner.live_count' | tail -1)"

ring() { vm curl -s -o /dev/null -w '%{http_code}' -H "Host: hooks.houston.test" -H "X-Houston-Token: $secret" -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:3000/spike; }

echo "== a push and a webhook → a runner deploys the new commit"
second=$(push 'echo two > version.txt' 'two')
[ "$(ring)" = 202 ] && ok "the signed webhook is accepted" || bad "webhook"
result=$(wait_for_deploy 2)
[ "${result%%|*}" = go ] && ok "deploy #2 GO" || bad "deploy #2: $result"
[ "$(through_proxy /env/KAMAL_VERSION)" = "$second" ] && ok "kamal-proxy serves $second" || bad "serving: $(through_proxy /env/KAMAL_VERSION)"

echo "== tests that fail stop the deploy"
push "sed -i 's|test -x /entry.sh|exit 3|' compose.yml" 'failing tests' >/dev/null
ring >/dev/null
result=$(wait_for_deploy 3)
case "$result" in no_go*"tests failed (exit 3)"*) ok "deploy #3 NO-GO: tests failed (exit 3)" ;; *) bad "deploy #3: $result" ;; esac
[ "$(through_proxy /env/KAMAL_VERSION)" = "$second" ] && ok "$second still serving" || bad "serving: $(through_proxy /env/KAMAL_VERSION)"

echo
if [ "$failures" -eq 0 ]; then echo "RUNNER PASS"; else echo "RUNNER: $failures failure(s)"; exit 1; fi

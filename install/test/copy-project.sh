#!/usr/bin/env bash
# Copying a project to a new name, for real (docs/plans/copy-project.md,
# Batch 6), on a fresh Houston server in an OrbStack machine (Cloudflare
# marked connected in wildcard mode; DNS moves are the unit tests'). A
# Forgejo container is the git host. spike-go (the spike fixture: Postgres,
# its data volume on the machine's NFS export, SQLite in it) serves its own
# host and spike.houston.test, the new name's default host. A poller asks
# spike.houston.test five times a second throughout, and every answer must
# be a 200, from spike-go until the handover and from spike after.
#
#   install/test/copy-project.sh           # KEEP=1 keeps the machine
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

name="houston-copy-e2e"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$name" -u root "$@" </dev/null; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1" 2>/dev/null | tail -1; }
through_proxy() { vm docker run --rm --network kamal curlimages/curl:8.16.0 -s -H "Host: $1.houston.test" "http://kamal-proxy$2"; }
psql_in() { vm docker exec "$1" psql -qtA -U postgres -d postgres -c "$2" 2>&1; }
sqlite_in() { vm docker run --rm --user 0 -v "$1:/data" --entrypoint sqlite3 houston/mission-control:local /data/app.sqlite3 "$2" 2>&1; }

cleanup() {
  if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

echo "== creating $name and installing Houston"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null
orb -m "$name" -u root env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-copy-install.log 2>&1 || { tail -20 /tmp/houston-copy-install.log; exit 1; }
rails 'Installation.current.update!(base_domain: "houston.test", cloudflare_connected_at: Time.current, dns_mode: "wildcard")' >/dev/null
vm docker pull -q curlimages/curl:8.16.0 >/dev/null

echo "== an NFS export, and backup storage"
vm sh -c 'DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nfs-kernel-server >/dev/null 2>&1 && mkdir -p /srv/nfs &&
  echo "/srv/nfs *(rw,sync,no_subtree_check,no_root_squash)" > /etc/exports && exportfs -ra && systemctl restart nfs-kernel-server' &&
  ok "nfs-kernel-server exports /srv/nfs" || bad "the NFS server"
ip=$(vm hostname -I | awk '{print $1}')
made=$(rails "
  s = StorageSetup.new(kind: 'local', name: 'local-backups', local_path: '/srv/houston-backups')
  abort(s.errors.full_messages.to_sentence + s.checks.map(&:label).join) unless s.save
  s.location.update!(acknowledged_at: Time.current, default: true)
  n = StorageSetup.new(kind: 'nfs', name: 'vm-nfs', nfs_server: '$ip', nfs_export: '/srv/nfs')
  abort(n.errors.full_messages.to_sentence + n.checks.map(&:label).join) unless n.save
  n.location.update!(acknowledged_at: Time.current)
  puts 'made'")
[ "$made" = made ] && ok "local-backups (the default) and vm-nfs" || bad "storage: $made"
token=$(rails 'puts ApiToken.issue!("e2e").first')
hou() { orb -m "$name" -u houston env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" bash -lc "houston $*" </dev/null; }

echo "== Forgejo with spike-go, linked and deployed"
pw="fj-$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
vm docker run -d --name forgejo --network houston_default -p 127.0.0.1:3001:3000 \
  -e FORGEJO__security__INSTALL_LOCK=true -e FORGEJO__server__SSH_DOMAIN=forgejo -e FORGEJO__server__SSH_PORT=22 \
  codeberg.org/forgejo/forgejo:13 >/dev/null
for _ in $(seq 1 60); do vm curl -fs -o /dev/null http://127.0.0.1:3001/api/v1/version && break; sleep 2; done
vm docker exec -u git forgejo forgejo admin user create --username houston --password "$pw" --email houston@test.invalid --admin --must-change-password=false >/dev/null
fj() { vm curl -s -u "houston:$pw" -H 'Content-Type: application/json' "http://127.0.0.1:3001/api/v1$1" "${@:2}"; }
gitc='git -c user.name=t -c user.email=t@test.invalid'
fj /user/repos -X POST -d '{"name":"spike","private":true,"default_branch":"main"}' >/dev/null
vm sh -c "rm -rf /tmp/spike && cp -r '$repo/internal/kamal/testdata/spike' /tmp/spike && cd /tmp/spike &&
  sed -i 's/^name: spike$/name: spike-go/; s/domains: \[spike-alias.houston.test\]/domains: [spike.houston.test]/' compose.yml &&
  git init -q -b main && git add -A && $gitc commit -qm spike-go &&
  git remote add origin 'http://houston:$pw@127.0.0.1:3001/houston/spike.git' && git push -q origin main"
key=$(rails "puts RepoLink.start!('git@forgejo:houston/spike.git').deploy_key_public")
fj /repos/houston/spike/keys -X POST -d "{\"title\":\"houston\",\"key\":\"$key\",\"read_only\":true}" >/dev/null
linked=$(rails "
  link = RepoLink.last
  read = GitRemote.read(link)
  raise \"read: #{read.problems}\" unless read.ok
  link.update!(preview: read.inspection, preview_sha: read.sha)
  project = ProjectLinking.new(link).save!
  project.secrets.create!(key: 'HOSTILE', value: 'x')
  project.secrets.create!(key: 'POSTGRES_PASSWORD', value: SecureRandom.hex(16))
  puts 'linked'")
[ "$linked" = linked ] && ok "spike-go linked; secrets set" || bad "linking: $linked"
hou volumes place data vm-nfs --project spike-go | grep -q 'data will be placed on vm-nfs' && ok "its data volume goes on vm-nfs" || bad "volumes place"

wait_for_deploy() { # project, number; prints "status|error"
  local got=""
  for _ in $(seq 1 120); do
    got=$(rails "d = Project.find_by(name: '$1')&.deploys&.find_by(number: $2); puts d ? [d.status, d.error.to_s.tr(\"\\n\", ' ')].join('|') : 'none'")
    case "$got" in go* | no_go* | hold*) break ;; esac
    sleep 5
  done
  printf '%s' "$got"
}
rails 'ChangeCheck.new(Project.find_by!(name: "spike-go")).run' >/dev/null
result=$(wait_for_deploy spike-go 1)
[ "${result%%|*}" = go ] && ok "spike-go deploy #1 GO" || bad "spike-go deploy #1: $result"
psql_in spike-go-db "create table t (v text); insert into t values ('kept')" >/dev/null
for _ in $(seq 1 30); do sqlite_in spike-go_data "create table t (v); insert into t values ('kept')" >/dev/null && break; sleep 5; done
[ "$(through_proxy spike /env/DB_HOST)" = spike-go-db ] && [ "$(through_proxy spike-go /env/DB_HOST)" = spike-go-db ] &&
  ok "spike-go serves spike-go.houston.test and spike.houston.test" || bad "before: $(through_proxy spike /env/DB_HOST)"

# Five requests a second (and more) at spike.houston.test, each line "<code> <db host>".
poll_start() {
  vm docker rm -f poller >/dev/null 2>&1
  vm docker run -d --name poller --network kamal --entrypoint sh curlimages/curl:8.16.0 -c \
    'while true; do c=$(curl -s --max-time 5 -o /tmp/v -w "%{http_code}" -H "Host: spike.houston.test" http://kamal-proxy/env/DB_HOST); echo "$c $(cat /tmp/v 2>/dev/null)"; sleep 0.1; done' >/dev/null
}
poll_check() { # what: the db host the last answer must name
  local lines failed
  lines=$(vm docker logs poller 2>&1)
  vm docker rm -f poller >/dev/null
  failed=$(printf '%s\n' "$lines" | grep -vc '^200 ')
  [ "$failed" = 0 ] && ok "all $(printf '%s\n' "$lines" | wc -l | tr -d ' ') requests to spike.houston.test answered 200 ($(printf '%s\n' "$lines" | sort | uniq -c | tr -s ' ' | tr '\n' ','))" ||
    bad "$failed of $(printf '%s\n' "$lines" | wc -l | tr -d ' ') requests failed: $(printf '%s\n' "$lines" | grep -v '^200 ' | sort | uniq -c | head -5)"
  [ "$(printf '%s\n' "$lines" | tail -1)" = "200 $1" ] && ok "and the last one was answered by $1's app" || bad "last: $(printf '%s\n' "$lines" | tail -1)"
}

echo "== a push naming spike holds, and proposes the copy"
vm sh -c "cd /tmp/spike && sed -i 's/^name: spike-go$/name: spike/' compose.yml && $gitc commit -qam 'name: spike' && git push -q origin main"
rails 'ChangeCheck.new(Project.find_by!(name: "spike-go")).run' >/dev/null
result=$(wait_for_deploy spike-go 2)
case "$result" in hold*'compose.yml names "spike"'*) ok "spike-go deploy #2 HOLD: compose.yml names spike" ;; *) bad "deploy #2: $result" ;; esac
hou status --project spike-go | grep -q '^hold      compose.yml names spike (#2): houston copy --confirm spike-go' && ok "houston status shows the proposal" || bad "status: $(hou status --project spike-go)"
[ "$(through_proxy spike /env/DB_HOST)" = spike-go-db ] && ok "spike-go keeps serving" || bad "serving: $(through_proxy spike /env/DB_HOST)"

echo "== the copy, with no failed request"
poll_start
sleep 2
out=$(hou copy --confirm spike-go --follow 2>&1); code=$?
sleep 2
poll_check spike-db
[ "$code" = 0 ] && printf '%s' "$out" | grep -q 'Copying spike-go to spike: deploy #1 of spike.' && ok "houston copy --follow: exit 0" || bad "copy: exit $code: $(printf '%s' "$out" | tail -15)"
log=$(vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "print Project.find_by!(name: 'spike').deploys.find_by!(number: 1).log" 2>/dev/null)
# (The log masks the local registry's placeholder password, "houston", even
# inside host names: a separate task. So the handover is checked in the row.)
for step in 'ok  data copied from spike-go' 'ok  handed over: spike\.' 'GO: spike is serving'; do
  printf '%s' "$log" | grep -q "$step" && ok "log: $step" || bad "log lacks: $step"
done
[ "$(rails 'puts ProjectCopy.order(:id).last.handed_over.join(",")')" = spike.houston.test ] && ok "the copy handed over spike.houston.test" || bad "handed over: $(rails 'puts ProjectCopy.order(:id).last.handed_over.inspect')"
[ "$(psql_in spike-db 'select v from t')" = kept ] && [ "$(sqlite_in spike_data 'select v from t')" = kept ] && ok "spike's Postgres and SQLite (on NFS) say 'kept'" || bad "copied data: $(psql_in spike-db 'select v from t') / $(sqlite_in spike_data 'select v from t')"
vm test -f /srv/nfs/volumes/spike/data/app.sqlite3 && ok "spike's data is in volumes/spike/data on vm-nfs" || bad "no volumes/spike/data/app.sqlite3 on vm-nfs"
[ "$(through_proxy spike-go /env/DB_HOST)" = spike-go-db ] && [ "$(psql_in spike-go-db 'select v from t')" = kept ] && ok "spike-go still serves its own host, its data untouched" || bad "spike-go after the copy"
routes=$(vm docker exec kamal-proxy kamal-proxy list | sed 's/\x1b\[[0-9;]*m//g')
printf '%s\n' "$routes" | grep -qE '^spike-web +spike\.houston\.test ' && printf '%s\n' "$routes" | grep -qE '^spike-go-web +spike-go\.houston\.test ' &&
  ! printf '%s\n' "$routes" | grep -q 'houston-handover\|houston-copy.invalid' && ok "kamal-proxy: spike has spike.houston.test, spike-go its own; no catch-all or placeholder left" || bad "routes: $routes"

echo "== undo, with no failed request, then the copy again"
poll_start
sleep 2
out=$(hou copy --undo --confirm spike --project spike 2>&1); code=$?
for _ in $(seq 1 60); do [ "$(rails 'puts ProjectDeletion.where(name: "spike").order(:id).last&.status')" = go ] && break; sleep 5; done
sleep 2
poll_check spike-go-db
[ "$code" = 0 ] && printf '%s' "$out" | grep -q "The hosts are spike-go's again" && ok "houston copy --undo: exit 0" || bad "undo: exit $code: $out"
[ "$(rails 'puts Project.exists?(name: "spike")')" = false ] && ok "spike is deleted" || bad "spike is still there"
out=$(hou copy --confirm spike-go --follow 2>&1); code=$?
[ "$code" = 0 ] && ok "copied again: exit 0" || bad "copy again: exit $code: $(printf '%s' "$out" | tail -8)"

echo "== spike-go deleted: spike keeps serving, and the old webhook URL rings it"
poll_start
sleep 2
out=$(hou delete --confirm spike-go --project spike-go --follow 2>&1); code=$?
sleep 2
poll_check spike-db
[ "$code" = 0 ] && ok "houston delete spike-go: exit 0" || bad "delete: exit $code: $out"
[ "$(through_proxy spike-go /env/DB_HOST)" != spike-go-db ] && ok "spike-go.houston.test isn't served any more" || bad "spike-go.houston.test still served"
secret=$(rails 'puts Project.find_by!(name: "spike").webhook_secret')
body='{"ref":"refs/heads/main"}'
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$secret" | awk '{print $NF}')
rang=$(vm curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Host: hooks.houston.test' -H 'Content-Type: application/json' -H "X-Forgejo-Signature: $sig" --data "$body" http://127.0.0.1:3000/spike-go)
[ "$rang" = 202 ] && ok "hooks.houston.test/spike-go rings spike (202)" || bad "the old webhook URL answered $rang"

echo
if [ "$failures" = 0 ]; then echo "PASS"; else echo "FAIL ($failures)"; exit 1; fi

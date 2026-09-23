#!/usr/bin/env bash
# Build step 6, Batch 7: restores for real, on a fresh Houston server in an
# OrbStack machine (Cloudflare marked connected in wildcard mode; nothing
# goes through the tunnel). A Forgejo container is the git host; runners
# deploy and restore. The spike fixture (Postgres, a data volume placed on
# the machine's own NFS export, a SQLite database in it) is backed up,
# changed, and restored while a poller asks for the app five times a second.
# Then: a restore that fails (its snapshot gone), Houston's maintenance page
# up through a restore, going back with the safety snapshot while the image
# was pruned from the registry, and a restore refused once the project is
# relinked to a repo without the commit.
#
# The maintenance flag is set in the database: turning it on pushes tunnel
# routes to Cloudflare, which maintenance-through-tunnel.sh covers.
#
# With EQUIP_SOURCE, equip too (restore-e2e-equip.sh): SQLite restored at the
# commit that serves.
#
#   install/test/restore-e2e.sh          # KEEP=1 keeps the machine
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

name="houston-restore-e2e"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$name" -u root "$@"; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1" 2>/dev/null | tail -1; }
through_proxy() { vm docker run --rm --network kamal curlimages/curl:8.16.0 -s -H "Host: spike.houston.test" "http://kamal-proxy$1"; }
psql_in() { vm docker exec "$1" psql -qtA -U postgres -d postgres -c "$2" 2>&1; }
sqlite_in() { vm docker run --rm --user 0 -v "$1:/data" --entrypoint sqlite3 houston/mission-control:local /data/app.sqlite3 "$2" 2>&1; }

keydir="" # a private copy of a master key while it's in use (restore-e2e-equip.sh): never left behind
cleanup() {
  [ -n "$keydir" ] && rm -rf "$keydir"
  if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

echo "== creating $name and installing Houston"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-restore-install.log 2>&1 || { tail -20 /tmp/houston-restore-install.log; exit 1; }
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
hou() { orb -m "$name" -u houston env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" bash -lc "houston $*"; }

echo "== Forgejo with the spike, linked"
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
  git init -q -b main && git add -A && $gitc commit -qm spike &&
  git remote add origin 'http://houston:$pw@127.0.0.1:3001/houston/spike.git' && git push -q origin main"
push() { vm sh -c "cd /tmp/spike && $1 && git add -A && $gitc commit -qm '$2' && git push -q origin main && git rev-parse HEAD"; }
first=$(vm sh -c 'cd /tmp/spike && git rev-parse HEAD')
key=$(rails 'puts RepoLink.start!("git@forgejo:houston/spike.git").deploy_key_public')
fj /repos/houston/spike/keys -X POST -d "{\"title\":\"houston\",\"key\":\"$key\",\"read_only\":true}" >/dev/null
linked=$(rails '
  link = RepoLink.last
  raise "no access: #{GitRemote.check(link).message}" unless GitRemote.check(link).ok
  read = GitRemote.read(link)
  raise "read: #{read.problems}" unless read.ok
  link.update!(preview: read.inspection, preview_sha: read.sha)
  project = ProjectLinking.new(link).save!
  project.secrets.create!(key: "HOSTILE", value: "restored")
  project.secrets.create!(key: "POSTGRES_PASSWORD", value: SecureRandom.hex(16))
  puts "linked"')
[ "$linked" = linked ] && ok "linked; secrets set" || bad "linking: $linked"
hou volumes place data vm-nfs --project spike | grep -q 'data will be placed on vm-nfs' && ok "the data volume goes on vm-nfs" || bad "volumes place"

wait_for_deploy() { # number, then prints "status|error"
  local got=""
  for _ in $(seq 1 120); do
    got=$(rails "d = Project.find_by!(name: 'spike').deploys.find_by(number: $1); puts d ? [d.status, d.error.to_s.tr(\"\\n\", ' ')].join('|') : 'none'")
    case "$got" in go* | no_go*) break ;; esac
    sleep 5
  done
  printf '%s' "$got"
}
# The whole log (rails keeps only the last line).
log_of() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "print Project.find_by!(name: 'spike').deploys.find_by!(number: $1).log" 2>/dev/null; }
generation() { rails 'puts Project.find_by!(name: "spike").data_generation'; }

echo "== deploy #1 (a runner), and its data: 'one'"
rails 'ChangeCheck.new(Project.find_by!(name: "spike")).run' >/dev/null
result=$(wait_for_deploy 1)
[ "${result%%|*}" = go ] && ok "deploy #1 GO" || bad "deploy #1: $result"
psql_in spike-db "create table t (v text); insert into t values ('one')" >/dev/null
# SQLite locks the file; a freshly restarted NFS server blocks locks for its
# grace period (NFSv4: up to 90 s).
for _ in $(seq 1 30); do sqlite_in spike_data "create table t (v); insert into t values ('one')" >/dev/null && break; sleep 5; done
web() { vm sh -c 'docker ps -q --filter label=service=spike --filter label=role=web | head -n1'; }
vm docker exec "$(web)" sh -c 'echo one > /data/probe'
[ "$(psql_in spike-db 'select v from t')" = one ] && [ "$(sqlite_in spike_data 'select v from t')" = one ] &&
  ok "Postgres and SQLite (on NFS) say 'one'" || bad "data one: $(psql_in spike-db 'select v from t') / $(sqlite_in spike_data 'select v from t')"

out=$(hou backup --follow --project spike 2>&1)
printf '%s' "$out" | grep -q '^GO: spike backed up' && ok "Back up now: GO" || bad "backup: $out"
s1=$(hou snapshots --project spike | awk '/Back up now/ {print $NF; exit}')
[ -n "$s1" ] && ok "snapshot $s1 (at ${first:0:7}, data 'one')" || bad "no snapshot: $(hou snapshots --project spike)"

echo "== deploy #2 (a push), and its data: 'two'"
second=$(push 'echo two > version.txt' 'two')
rails 'ChangeCheck.new(Project.find_by!(name: "spike")).run' >/dev/null
result=$(wait_for_deploy 2)
[ "${result%%|*}" = go ] && ok "deploy #2 GO" || bad "deploy #2: $result"
psql_in spike-db "update t set v = 'two'" >/dev/null
sqlite_in spike_data "update t set v = 'two'" >/dev/null
vm docker exec "$(web)" sh -c 'echo two > /data/probe'
[ "$(through_proxy /env/KAMAL_VERSION)" = "$second" ] && ok "serving ${second:0:7}, data 'two'" || bad "serving: $(through_proxy /env/KAMAL_VERSION)"

# Five requests a second through kamal-proxy, each line "<code> <version>".
poll_start() {
  vm docker rm -f poller >/dev/null 2>&1
  vm docker run -d --name poller --network kamal --entrypoint sh curlimages/curl:8.16.0 -c \
    'while true; do v=$(curl -s --max-time 5 -o /tmp/v -w "%{http_code}" -H "Host: spike.houston.test" http://kamal-proxy/env/KAMAL_VERSION); echo "$v $(cat /tmp/v 2>/dev/null)"; sleep 0.2; done' >/dev/null
}
poll_check() { # the version it must end on
  local lines failed
  lines=$(vm docker logs poller 2>&1)
  vm docker rm -f poller >/dev/null
  failed=$(printf '%s\n' "$lines" | grep -vc '^200 ')
  [ "$failed" = 0 ] && ok "all $(printf '%s\n' "$lines" | wc -l | tr -d ' ') requests during it answered 200" || bad "$failed of $(printf '%s\n' "$lines" | wc -l) requests failed: $(printf '%s\n' "$lines" | grep -v '^200 ' | sort | uniq -c | head -5)"
  [ "$(printf '%s\n' "$lines" | tail -1)" = "200 $1" ] && ok "and the last one was ${1:0:7}" || bad "last: $(printf '%s\n' "$lines" | tail -1)"
}

echo "== restore #3: snapshot $s1, zero-downtime"
poll_start
sleep 2
out=$(hou restore "$s1" --confirm spike --project spike --follow 2>&1); code=$?
poll_check "$first"
[ "$code" = 0 ] && printf '%s' "$out" | grep -q 'Queued restore #3' && ok "houston restore --follow: exit 0" || bad "restore: exit $code: $(printf '%s' "$out" | tail -15)"
log=$(log_of 3)
for step in 'into data generation 2' 'ok  data restored into generation 2' 'ok  snapshot [0-9a-f]\{8\} · kind:deploy' 'Removing generation 1.' 'GO: spike is serving'; do
  printf '%s' "$log" | grep -q "$step" && ok "log: $step" || bad "log lacks: $step"
done
[ "$(generation)" = 2 ] && ok "spike's data generation is 2" || bad "generation: $(generation)"
[ "$(through_proxy /env/KAMAL_VERSION)" = "$first" ] && [ "$(through_proxy /env/DB_HOST)" = spike-db-g2 ] && ok "serving ${first:0:7} on spike-db-g2" || bad "serving: $(through_proxy /env/KAMAL_VERSION) $(through_proxy /env/DB_HOST)"
[ "$(psql_in spike-db-g2 'select v from t')" = one ] && ok "Postgres says 'one' again" || bad "Postgres: $(psql_in spike-db-g2 'select v from t')"
[ "$(sqlite_in spike.g2_data 'select v from t')" = one ] && ok "SQLite says 'one' again" || bad "SQLite: $(sqlite_in spike.g2_data 'select v from t')"
[ "$(vm docker exec "$(web)" cat /data/probe)" = one ] && ok "the volume's files are back" || bad "probe: $(vm docker exec "$(web)" cat /data/probe)"
vm docker volume inspect --format '{{json .Options}}' spike.g2_data | grep -q ':/srv/nfs/volumes/spike.g2/data"' && ok "spike.g2_data is on vm-nfs, in volumes/spike.g2/data" || bad "spike.g2_data: $(vm docker volume inspect --format '{{json .Options}}' spike.g2_data)"
vm docker ps -a --format '{{.Names}}' | grep -qx spike-db && bad "generation 1's Postgres is still there" || ok "generation 1's Postgres is gone"
gone=$(vm sh -c 'docker volume ls -q | grep -xE "spike_(data|pgdata)"')
[ -z "$gone" ] && ok "generation 1's volumes are gone" || bad "left: $gone"
[ -z "$(vm ls -A /srv/nfs/volumes/spike/data)" ] && ok "and generation 1's directory on vm-nfs is emptied, not just forgotten" || bad "left on vm-nfs: $(vm ls -A /srv/nfs/volumes/spike/data)"
safety=$(hou snapshots --project spike | awk '/before a restore/ {print $NF; exit}')
[ -n "$safety" ] && ok "the safety snapshot $safety is listed as 'before a restore'" || bad "no safety snapshot: $(hou snapshots --project spike)"
hou deploys --project spike | grep -q "refs/restore/$s1" && ok "houston deploys shows the restore by its ref" || bad "deploys: $(hou deploys --project spike)"

echo "== restore #4 fails: its snapshot is gone by the time the data is restored"
# Asked for as the CLI would, then its snapshot swapped for one that isn't
# there (as if pruned since), before a runner gets to the data.
queued=$(rails "p = Project.find_by!(name: 'spike')
  d = Deploy.request_restore!(p, snapshot: '$s1', location: StorageLocation.find_by!(name: 'local-backups'), confirm: 'spike')
  d.update!(source_snapshot_id: 'f' * 64); puts d.number")
[ "$queued" = 4 ] && ok "restore #4 queued" || bad "queued: $queued"
poll_start
result=$(wait_for_deploy 4)
poll_check "$first"
case "$result" in no_go*"restore failed at its data: restic restore failed"*"the old version keeps serving"*) ok "restore #4 NO-GO at its data" ;; *) bad "restore #4: $result" ;; esac
[ "$(generation)" = 2 ] && ok "the data generation is still 2" || bad "generation: $(generation)"
left=$(vm sh -c 'docker ps -a --format "{{.Names}}"; docker volume ls -q' | grep -E 'g3')
[ -z "$left" ] && ok "generation 3 was removed" || bad "left of generation 3: $left"
[ "$(psql_in spike-db-g2 'select v from t')" = one ] && ok "generation 2's data untouched" || bad "g2: $(psql_in spike-db-g2 'select v from t')"

echo "== the maintenance page stays up through a restore; going back with the safety snapshot, its image pruned"
rails 'Project.find_by!(name: "spike").update!(maintenance_since: Time.current, maintenance_by: "e2e")' >/dev/null
page() { vm curl -s -o /dev/null -w '%{http_code}' -H 'Host: spike.houston.test' http://127.0.0.1:3000/; }
[ "$(page)" = 503 ] && ok "Mission Control answers spike.houston.test with its maintenance page (503)" || bad "page: $(page)"
registry=$(vm docker ps -q --filter ancestor=registry:3 | head -n1)
vm docker exec "$registry" rm -rf "/var/lib/registry/docker/registry/v2/repositories/spike/_manifests/tags/$second"
vm docker rmi -f "127.0.0.1:5000/spike:$second" >/dev/null 2>&1
vm docker pull -q "127.0.0.1:5000/spike:$second" >/dev/null 2>&1 && bad "the registry still has ${second:0:7}" || ok "${second:0:7}'s image is gone from the host and the registry"
poll_start
vm docker rm -f pager >/dev/null 2>&1
vm docker run -d --name pager --network houston_default --entrypoint sh curlimages/curl:8.16.0 -c \
  'while true; do curl -s --max-time 5 -o /dev/null -w "%{http_code}\n" -H "Host: spike.houston.test" http://mission-control:3000/; sleep 0.5; done' >/dev/null
out=$(hou restore "$safety" --confirm spike --project spike --follow 2>&1); code=$?
poll_check "$second"
pages=$(vm docker logs pager 2>&1); vm docker rm -f pager >/dev/null
[ -n "$pages" ] && [ "$(printf '%s\n' "$pages" | grep -vc '^503$')" = 0 ] && ok "the maintenance page answered all $(printf '%s\n' "$pages" | wc -l | tr -d ' ') requests during it" || bad "the page during the restore: $(printf '%s\n' "$pages" | sort | uniq -c)"
[ "$code" = 0 ] && ok "restore #5 (the safety snapshot): GO" || bad "restore #5: exit $code: $(printf '%s' "$out" | tail -15)"
log_of 5 | grep -q "isn't in Houston's registry any more; building it again" && ok "the pruned image was built again" || bad "no rebuild in the log"
[ "$(page)" = 503 ] && ok "the maintenance page is still up after it" || bad "page after: $(page)"
[ "$(rails 'puts Project.find_by!(name: "spike").maintenance?')" = true ] && ok "and maintenance is still on: a restore never changes it" || bad "maintenance flag"
[ "$(generation)" = 3 ] && [ "$(through_proxy /env/KAMAL_VERSION)" = "$second" ] && ok "generation 3, serving ${second:0:7}" || bad "generation $(generation), serving $(through_proxy /env/KAMAL_VERSION)"
[ "$(psql_in spike-db-g3 'select v from t')" = two ] && [ "$(sqlite_in spike.g3_data 'select v from t')" = two ] && ok "the data is 'two' again: back where it was" || bad "data: $(psql_in spike-db-g3 'select v from t') / $(sqlite_in spike.g3_data 'select v from t')"
vm docker ps -a --format '{{.Names}}' | grep -qx spike-db-g2 && bad "generation 2's Postgres is still there" || ok "generation 2 removed"
rails 'Project.find_by!(name: "spike").update!(maintenance_since: nil, maintenance_by: nil)' >/dev/null
[ "$(page)" = 404 ] && ok "maintenance off: Mission Control no longer answers for the app" || bad "page off: $(page)"

echo "== relinked to a repo without the commit: refused"
fj /user/repos -X POST -d '{"name":"spike-other","private":true,"default_branch":"main"}' >/dev/null
vm sh -c "rm -rf /tmp/other && mkdir /tmp/other && cd /tmp/other && echo other > README && git init -q -b main && git add -A && $gitc commit -qm other &&
  git remote add origin 'http://houston:$pw@127.0.0.1:3001/houston/spike-other.git' && git push -q origin main"
fj /repos/houston/spike-other/keys -X POST -d "{\"title\":\"houston\",\"key\":\"$key\",\"read_only\":true}" >/dev/null
rails 'Project.find_by!(name: "spike").update!(repo_url: "git@forgejo:houston/spike-other.git")' >/dev/null
out=$(hou restore "$s1" --confirm spike --project spike 2>&1); code=$?
[ "$code" != 0 ] && printf '%s' "$out" | grep -q "commit ${first:0:7} isn't in git@forgejo:houston/spike-other.git any more" && ok "refused: $(printf '%s' "$out" | head -1)" || bad "relinked: exit $code: $out"
[ "$(rails 'puts Project.find_by!(name: "spike").deploys.count')" = 5 ] && ok "nothing was queued" || bad "deploys: $(rails 'puts Project.find_by!(name: "spike").deploys.count')"

# With EQUIP_SOURCE (a Houston-ready copy of equip, never your repo): equip too.
if [ -n "${EQUIP_SOURCE:-}" ]; then
  # shellcheck source=install/test/restore-e2e-equip.sh
  . "$(dirname "$0")/restore-e2e-equip.sh"
fi

echo
if [ "$failures" -eq 0 ]; then echo "RESTORE PASS"; else echo "RESTORE: $failures failure(s)"; exit 1; fi

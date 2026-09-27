#!/usr/bin/env bash
# Deleting a project, for real (docs/plans/delete-project.md, Batch 6), on a
# fresh Houston server in an OrbStack machine (Cloudflare marked connected in
# wildcard mode; DNS and the tunnel are covered by the unit tests). A Forgejo
# container is the git host. Two projects from the spike fixture are
# deployed: spike (Postgres, its data volume on the machine's own NFS export,
# a SQLite database in it) and spike-x, whose name starts the same and which
# nothing may touch.
#
#   install/test/delete-project.sh           # KEEP=1 keeps the machine
#   SETUP_ONLY=1 KEEP=1 install/test/delete-project.sh   # stop once both are deployed
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

name="houston-delete-e2e"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$name" -u root "$@"; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1" 2>/dev/null | tail -1; }
through_proxy() { vm docker run --rm --network kamal curlimages/curl:8.16.0 -s -o /dev/null -w '%{http_code}' -H "Host: $1.houston.test" "http://kamal-proxy/up"; }
psql_in() { vm docker exec "$1" psql -qtA -U postgres -d postgres -c "$2" 2>&1; }
sqlite_in() { vm docker run --rm --user 0 -v "$1:/data" --entrypoint sqlite3 houston/mission-control:local /data/app.sqlite3 "$2" 2>&1; }

cleanup() {
  if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

echo "== creating $name and installing Houston"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-delete-install.log 2>&1 || { tail -20 /tmp/houston-delete-install.log; exit 1; }
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

echo "== Forgejo with spike and spike-x"
pw="fj-$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
vm docker run -d --name forgejo --network houston_default -p 127.0.0.1:3001:3000 \
  -e FORGEJO__security__INSTALL_LOCK=true -e FORGEJO__server__SSH_DOMAIN=forgejo -e FORGEJO__server__SSH_PORT=22 \
  codeberg.org/forgejo/forgejo:13 >/dev/null
for _ in $(seq 1 60); do vm curl -fs -o /dev/null http://127.0.0.1:3001/api/v1/version && break; sleep 2; done
vm docker exec -u git forgejo forgejo admin user create --username houston --password "$pw" --email houston@test.invalid --admin --must-change-password=false >/dev/null
fj() { vm curl -s -u "houston:$pw" -H 'Content-Type: application/json' "http://127.0.0.1:3001/api/v1$1" "${@:2}"; }
gitc='git -c user.name=t -c user.email=t@test.invalid'

# Pushes the fixture as project $1, then links it: prints "linked".
link() {
  local p="$1"
  fj /user/repos -X POST -d "{\"name\":\"$p\",\"private\":true,\"default_branch\":\"main\"}" >/dev/null
  vm sh -c "rm -rf /tmp/$p && cp -r '$repo/internal/kamal/testdata/spike' /tmp/$p && cd /tmp/$p &&
    sed -i 's/^name: spike$/name: $p/; s/spike-alias/$p-alias/' compose.yml &&
    git init -q -b main && git add -A && $gitc commit -qm $p &&
    git remote add origin 'http://houston:$pw@127.0.0.1:3001/houston/$p.git' && git push -q origin main"
  link_project "$p"
}
# Links project $1 to its repo, as Add project does, with a new deploy key.
link_project() {
  local p="$1" key
  key=$(rails "puts RepoLink.start!('git@forgejo:houston/$p.git').deploy_key_public")
  fj "/repos/houston/$p/keys" -X POST -d "{\"title\":\"houston-$(date +%s)\",\"key\":\"$key\",\"read_only\":true}" >/dev/null
  rails "
    link = RepoLink.last
    raise \"no access: #{GitRemote.check(link).message}\" unless GitRemote.check(link).ok
    read = GitRemote.read(link)
    raise \"read: #{read.problems}\" unless read.ok
    link.update!(preview: read.inspection, preview_sha: read.sha)
    project = ProjectLinking.new(link).save!
    project.secrets.create!(key: 'HOSTILE', value: 'x')
    project.secrets.create!(key: 'POSTGRES_PASSWORD', value: SecureRandom.hex(16))
    puts 'linked'"
}
wait_for_deploy() { # project, number; prints "status|error"
  local got=""
  for _ in $(seq 1 120); do
    got=$(rails "d = Project.find_by!(name: '$1').deploys.find_by(number: $2); puts d ? [d.status, d.error.to_s.tr(\"\\n\", ' ')].join('|') : 'none'")
    case "$got" in go* | no_go*) break ;; esac
    sleep 5
  done
  printf '%s' "$got"
}

for p in spike spike-x; do
  [ "$(link "$p")" = linked ] && ok "$p linked; secrets set" || bad "linking $p"
done
hou volumes place data vm-nfs --project spike | grep -q 'data will be placed on vm-nfs' && ok "spike's data volume goes on vm-nfs" || bad "volumes place"

echo "== deploy both"
for p in spike spike-x; do
  rails "ChangeCheck.new(Project.find_by!(name: '$p')).run" >/dev/null
done
for p in spike spike-x; do
  result=$(wait_for_deploy "$p" 1)
  [ "${result%%|*}" = go ] && ok "$p deploy #1 GO" || bad "$p deploy #1: $result"
done
psql_in spike-db "create table t (v text); insert into t values ('kept')" >/dev/null
for _ in $(seq 1 30); do sqlite_in spike_data "create table t (v); insert into t values ('kept')" >/dev/null && break; sleep 5; done
[ "$(psql_in spike-db 'select v from t')" = kept ] && [ "$(sqlite_in spike_data 'select v from t')" = kept ] &&
  ok "spike's Postgres and SQLite (on NFS) say 'kept'" || bad "data: $(psql_in spike-db 'select v from t') / $(sqlite_in spike_data 'select v from t')"
for p in spike spike-x; do
  [ "$(through_proxy "$p")" = 200 ] && ok "$p answers through kamal-proxy" || bad "$p through kamal-proxy: $(through_proxy "$p")"
done

if [ "${SETUP_ONLY:-}" = 1 ]; then
  echo "set up (SETUP_ONLY); token: $token"
  exit "$((failures > 0))"
fi

# What's left of spike on the server, one line per thing (empty: all gone).
leftovers() {
  vm sh -c '
    docker ps -a --format "container {{.Names}}" | grep -E "^container spike-(web-|db|release-)|^container houston-kamal-spike$"
    docker volume ls -q | grep -E "^spike(\.g[0-9]+)?_" | sed "s/^/volume /"
    docker images --format "image {{.Repository}}:{{.Tag}}" | grep -E "^image 127\.0\.0\.1:5000/spike:"
    docker exec kamal-proxy kamal-proxy list | grep -Eo "spike-web\b" | sed "s/^/route /"
    for p in /srv/nfs/volumes/spike /srv/nfs/volumes/spike.g2 /home/houston/.kamal/apps/spike /home/houston/.kamal/spike-audit.log /var/lib/houston/runners/*/spike; do [ ! -e "$p" ] || echo "file $p"; done' 2>/dev/null
}
spike_x_intact() {
  [ "$(through_proxy spike-x)" = 200 ] && [ "$(psql_in spike-x-db 'select 1')" = 1 ] &&
    [ "$(vm sh -c 'docker volume ls -q | grep -cE "^spike-x_"')" = 2 ] && vm test -d /var/lib/houston/runners/houston-runner-1/spike-x -o -d /var/lib/houston/runners/houston-runner-2/spike-x &&
    vm test -d /home/houston/.kamal/apps/spike-x && [ "$(rails 'puts Project.find_by(name: "spike-x")&.name')" = spike-x ]
}
registry_kb() { vm sh -c 'docker exec "$(docker ps -q --filter label=com.docker.compose.service=registry | head -n1)" du -sk /var/lib/registry' | awk '{print $1}'; }
registry_tags() { vm curl -s "http://127.0.0.1:5000/v2/$1/tags/list"; }
deletion() { rails "d = ProjectDeletion.where(name: 'spike').order(:id).last; puts [d.status, d.step, d.error.to_s.tr(\"\\n\", ' ')].join('|')"; }

echo "== a backup first, and spike in maintenance"
out=$(hou backup --follow --project spike 2>&1)
printf '%s' "$out" | grep -q '^GO: spike backed up' && ok "Back up now: GO" || bad "backup: $out"
rails 'Project.find_by!(name: "spike").update!(maintenance_since: Time.current, maintenance_by: "e2e")' >/dev/null

echo "== cancelled: the backup storage is read-only when the final snapshot is taken"
vm sh -c 'mount --bind /srv/houston-backups /srv/houston-backups && mount -o remount,bind,ro /srv/houston-backups'
out=$(hou delete --confirm spike --project spike --follow 2>&1); code=$?
vm umount /srv/houston-backups
[ "$code" = 1 ] && printf '%s' "$out" | grep -q 'NO-GO: deleting spike cancelled: the final snapshot failed' && ok "houston delete: exit 1, cancelled at the final snapshot" || bad "cancel: exit $code: $out"
printf '%s' "$out" | grep -q 'run this again' && bad "a cancel says to run it again" || ok "and it doesn't say to run it again (nothing was removed)"
[ "$(through_proxy spike)" = 200 ] && [ "$(psql_in spike-db 'select v from t')" = kept ] && ok "spike still serves, its data untouched" || bad "spike after the cancel: $(through_proxy spike)"
[ "$(leftovers | grep -c .)" -ge 5 ] && ok "nothing of spike was removed" || bad "removed during a cancel; left: $(leftovers | tr '\n' ' ')"
hou status --project spike | grep -q '^deleting' && bad "status says deleting after a cancel" || ok "houston status doesn't say deleting"

echo "== stopped partway: Kamal's folder for spike is read-only"
vm sh -c 'mount --bind /home/houston/.kamal/apps/spike /home/houston/.kamal/apps/spike && mount -o remount,bind,ro /home/houston/.kamal/apps/spike'
out=$(hou delete --confirm spike --project spike --follow 2>&1); code=$?
[ "$code" = 1 ] && printf '%s' "$out" | grep -q 'NO-GO: deleting spike stopped at files' && printf '%s' "$out" | grep -q 'run this again to finish' &&
  ok "houston delete: exit 1, stopped at files" || bad "stop: exit $code: $out"
[ "$(deletion | cut -d'|' -f1-2)" = "no_go|files" ] && ok "the deletion is NO-GO at files" || bad "deletion: $(deletion)"
hou status --project spike | grep -q '^deleting  NO-GO: stopped at files' && ok "houston status says so, and how to finish" || bad "status: $(hou status --project spike)"
[ "$(through_proxy spike)" = 404 ] && ok "spike's host answers kamal-proxy's 404: its containers and route are gone" || bad "spike's host: $(through_proxy spike)"
vm sh -c 'rm -rf /home/houston/spike-hand && cp -r /tmp/spike /home/houston/spike-hand && chown -R houston:houston /home/houston/spike-hand'
out=$(orb -m "$name" -u houston bash -lc "cd ~/spike-hand && houston deploy" 2>&1)
printf '%s' "$out" | grep -q 'spike is being deleted' && ok "a hand houston deploy is refused meanwhile" || bad "hand deploy: $out"
rails 'ChangeCheck.new(Project.find_by!(name: "spike")).run' >/dev/null
[ "$(rails 'puts Project.find_by!(name: "spike").deploys.where(status: %w[queued in_flight]).count')" = 0 ] && ok "and a change check queues nothing" || bad "a deploy was queued"
spike_x_intact && ok "spike-x untouched" || bad "spike-x after the stop"
vm umount /home/houston/.kamal/apps/spike

echo "== finished: the same command again"
before_kb=$(registry_kb)
out=$(hou delete --confirm spike --project spike --follow 2>&1); code=$?
[ "$code" = 0 ] && printf '%s' "$out" | grep -q 'GO: spike is deleted. Its final snapshot [0-9a-f]\{8\} is kept in local-backups.' &&
  printf '%s' "$out" | grep -q 'Remove the deploy key and the webhook from git@forgejo:houston/spike.git' && ok "houston delete: exit 0, GO, the final snapshot kept" || bad "finish: exit $code: $out"
left=$(leftovers)
[ -z "$left" ] && ok "nothing of spike is left: containers, volumes, NFS folder, images, route, Kamal's files, checkouts" || bad "left: $(printf '%s' "$left" | tr '\n' ' ')"
[ "$(rails 'puts Project.exists?(name: "spike")')" = false ] && ok "spike is gone from Mission Control" || bad "spike's row is still there"
[ "$(through_proxy spike)" = 404 ] && ok "spike's host answers kamal-proxy's 404" || bad "spike's host: $(through_proxy spike)"
spike_x_intact && ok "spike-x untouched: serving, its data, volumes, checkout, Kamal's files, row" || bad "spike-x after the deletion"
case "$(registry_tags spike)" in *NAME_UNKNOWN* | *'"tags":[]'* | *'"tags":null'*) ok "spike has no tags left in Houston's registry" ;; *) bad "registry tags: $(registry_tags spike)" ;; esac
for _ in $(seq 1 30); do rails "puts ProjectDeletion.where(name: 'spike').order(:id).last.log" | grep -q 'registry space' && break; sleep 2; done
freed=$(vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "print ProjectDeletion.where(name: 'spike').order(:id).last.log" 2>/dev/null | grep 'registry space')
case "$freed" in "ok  registry space freed"*) ok "the registry was garbage-collected: $freed" ;; *) bad "registry cleanup: ${freed:-nothing logged}" ;; esac
after_kb=$(registry_kb)
[ "$after_kb" -lt "$before_kb" ] && ok "the registry shrank: ${before_kb} KB → ${after_kb} KB" || bad "the registry didn't shrink: ${before_kb} KB → ${after_kb} KB"
# Every blob spike-x's image needs is still in the registry (shared layers included).
missing=$(vm sh -c 'A="Accept: application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json"
  tag=$(curl -s http://127.0.0.1:5000/v2/spike-x/tags/list | grep -o "[0-9a-f]\{40\}" | head -n1)
  digests=$(curl -s -H "$A" http://127.0.0.1:5000/v2/spike-x/manifests/$tag | grep -o "sha256:[0-9a-f]\{64\}" | sort -u)
  [ -n "$digests" ] || { echo "no manifest"; exit; }
  for d in $digests; do [ "$(curl -s -o /dev/null -w "%{http_code}" -I http://127.0.0.1:5000/v2/spike-x/blobs/$d)" = 200 ] || echo "$d"; done')
[ -z "$missing" ] && ok "every blob of spike-x's image is still in the registry" || bad "spike-x's image lost: $missing"

echo "== added again, and its final snapshot restored"
[ "$(link_project spike)" = linked ] && ok "spike linked again" || bad "relinking spike"
hou volumes place data vm-nfs --project spike | grep -q 'data will be placed on vm-nfs' && ok "its data volume goes on vm-nfs" || bad "volumes place"
rails 'ChangeCheck.new(Project.find_by!(name: "spike")).run' >/dev/null
result=$(wait_for_deploy spike 1)
[ "${result%%|*}" = go ] && ok "the new spike: deploy #1 GO, empty" || bad "new spike deploy #1: $result"
final=$(hou snapshots --project spike | awk '/before it was deleted/ {print $NF; exit}')
[ -n "$final" ] && ok "houston snapshots lists the final snapshot $final" || bad "no final snapshot: $(hou snapshots --project spike)"
out=$(hou restore "$final" --confirm spike --project spike --follow 2>&1); code=$?
[ "$code" = 0 ] && ok "houston restore $final: exit 0" || bad "restore: exit $code: $(printf '%s' "$out" | tail -8)"
[ "$(psql_in spike-db-g2 'select v from t')" = kept ] && [ "$(sqlite_in spike.g2_data 'select v from t')" = kept ] &&
  ok "Postgres and SQLite say 'kept' again" || bad "restored data: $(psql_in spike-db-g2 'select v from t') / $(sqlite_in spike.g2_data 'select v from t')"

echo "== deleted again, with its backups"
# Added again after 03:00 UTC, spike's daily backup is due at once; a delete
# asked for meanwhile is refused ("wait for it"), so wait for it.
for _ in $(seq 1 60); do [ "$(rails 'puts BackupRun.joins(:project).where(projects: { name: "spike" }, status: %w[queued running]).count')" = 0 ] && break; sleep 5; done
out=$(hou delete --confirm spike --project spike --delete-backups --follow 2>&1); code=$?
[ "$code" = 0 ] && printf '%s' "$out" | grep -q 'its backups go too' && ok "houston delete --delete-backups: exit 0" || bad "delete --delete-backups: exit $code: $out"
left=$(leftovers)
[ -z "$left" ] && ok "nothing of spike is left, generation 2 included" || bad "left: $(printf '%s' "$left" | tr '\n' ' ')"
count=$(rails 'l = StorageLocation.find_by!(name: "local-backups"); r = DockerCommand.run(*l.restic_args("snapshots", "--json", "--tag", "project:spike"), env: l.restic_env); puts r.success ? JSON.parse(r.output).size : "failed: #{r.output.lines.last}"')
[ "$count" = 0 ] && ok "no snapshot of spike is left in local-backups" || bad "snapshots of spike left: $count"
spike_x_intact && ok "spike-x still untouched" || bad "spike-x at the end"

echo
if [ "$failures" = 0 ]; then echo "PASS"; else echo "FAIL ($failures)"; exit 1; fi

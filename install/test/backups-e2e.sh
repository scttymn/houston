#!/usr/bin/env bash
# Build step 5, Batch 8: volumes and backups for real, on a fresh Houston
# server in an OrbStack machine (Cloudflare marked connected in wildcard mode;
# nothing here goes through the tunnel). The machine runs its own NFS server.
# Storage: a host path (the default) and that NFS export. The spike fixture
# (Postgres, a data volume placed on NFS, a SQLite database held open in WAL
# mode) is deployed by hand, backed up now, deployed again (pre-deploy
# snapshots, retention), backed up on its schedule, and pruned; a snapshot is
# restored with restic by hand and opened. With EQUIP_SOURCE (a Houston-ready
# copy of equip, never your repo), equip's SQLite databases too.
#
#   install/test/backups-e2e.sh          # KEEP=1 keeps the machine
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

name="houston-backups-e2e"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$name" -u root "$@"; }
as_houston() { orb -m "$name" -u houston bash -lc "$1"; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1" 2>/dev/null | tail -1; }
gitc='git -c user.name=e2e -c user.email=e2e@houston.test'

keydir="" # a private copy of a master key while it's in use: never left behind
cleanup() {
  [ -n "$keydir" ] && rm -rf "$keydir"
  if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

echo "== creating $name and installing Houston"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-backups-install.log 2>&1 || { tail -20 /tmp/houston-backups-install.log; exit 1; }
rails 'Installation.current.update!(base_domain: "houston.test", cloudflare_connected_at: Time.current, dns_mode: "wildcard")' >/dev/null

echo "== an NFS server in the machine, and two storage locations"
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
[ "$made" = made ] && ok "local-backups (the default) and vm-nfs: restic repositories made for real" || bad "storage: $made"
token=$(rails 'puts ApiToken.issue!("e2e").first')
hou() { orb -m "$name" -u houston env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" bash -lc "cd ~/spike 2>/dev/null; houston $*"; }
hou storage | grep -q '^vm-nfs .*live volumes · backups' && ok "houston storage lists vm-nfs as holding live volumes" || bad "houston storage: $(hou storage)"

echo "== the spike: HOLD first, then its data volume on NFS"
as_houston "rm -rf ~/spike && cp -r '$repo/internal/kamal/testdata/spike' ~/spike && cd ~/spike &&
  printf '  backups: { keep: { auto: 3, deploy: 2 } }\n' >> compose.yml && git init -q -b main && git add -A && $gitc commit -qm spike"
as_houston 'cd ~/spike && houston deploy' >/tmp/backups-e2e-1.log 2>&1
grep -q HOLD /tmp/backups-e2e-1.log && ok "the first deploy holds for its secrets" || bad "HOLD: $(tail -2 /tmp/backups-e2e-1.log)"
vm docker volume inspect spike_data >/dev/null 2>&1 && bad "a HOLD made spike_data" || ok "a HOLD made no volume, so its place can still be chosen"
hou volumes place data vm-nfs | grep -q 'data will be placed on vm-nfs' && ok "houston volumes place data vm-nfs" || bad "volumes place"
printf 'backed up' | hou secrets set HOSTILE >/dev/null && hou secrets generate POSTGRES_PASSWORD >/dev/null && ok "secrets set from the CLI" || bad "secrets"

as_houston 'cd ~/spike && houston deploy' >/tmp/backups-e2e-first.log 2>&1 && ok "deploy #1 GO" || { bad "deploy #1"; tail -20 /tmp/backups-e2e-first.log; }
grep -q 'no snapshot: nothing deployed yet' /tmp/backups-e2e-first.log && ok "no pre-deploy snapshot on the first real deploy" || bad "first snapshot: $(grep -i snapshot /tmp/backups-e2e-first.log)"
vm docker volume inspect --format '{{json .Options}}' spike_data | grep -q "\"device\":\":/srv/nfs/volumes/spike/data\"" && ok "spike_data is an NFS volume in vm-nfs/volumes/spike/data" || bad "spike_data: $(vm docker volume inspect --format '{{json .Options}}' spike_data)"
web() { vm sh -c 'docker ps -q --filter label=service=spike --filter label=role=web | head -n1'; }
vm docker exec "$(web)" sh -c 'echo probe > /data/probe' && [ "$(vm cat /srv/nfs/volumes/spike/data/probe)" = probe ] &&
  ok "what the app writes to /data lands on the NFS export" || bad "the probe didn't reach /srv/nfs"

echo "== data to back up: Postgres with a hostile database name, SQLite held open in WAL mode"
vm docker exec spike-db psql -q -U postgres -c "create database \"we'ird=db\"" &&
  vm docker exec spike-db psql -q -U postgres -d "dbname='we\\'ird=db'" -c "create table t (n int); insert into t values (42)" &&
  ok "Postgres: a database named we'ird=db, with a row" || bad "Postgres data"
vm docker run -d --name wal-holder --user 0 -v spike_data:/data --entrypoint sh houston/mission-control:local -c \
  '(echo "pragma journal_mode=wal; pragma wal_autocheckpoint=0; create table t (n); insert into t values (1),(2),(3);"; sleep 3600) | sqlite3 /data/app.sqlite3' >/dev/null
# SQLite locks the file; a freshly restarted NFS server blocks locks for its
# grace period (NFSv4: up to 90 s), so this can take a while.
for _ in $(seq 1 150); do vm test -s /srv/nfs/volumes/spike/data/app.sqlite3-wal && break; sleep 1; done
vm test -s /srv/nfs/volumes/spike/data/app.sqlite3-wal && ok "app.sqlite3 is open, its rows in its -wal" || bad "no -wal"

echo "== Back up now"
out=$(hou backup --follow 2>&1); code=$?
[ "$code" = 0 ] && printf '%s' "$out" | grep -q '^GO: spike backed up: snapshot' && ok "houston backup --follow: GO" || bad "backup: exit $code: $out"
hou snapshots | grep -q ' auto .*Back up now' && ok "houston snapshots lists it" || bad "snapshots: $(hou snapshots)"

echo "== the snapshot, restored by hand with restic"
pw=$(rails 'print StorageLocation.find_by!(name: "local-backups").restic_password')
vm rm -rf /tmp/restore
vm env RESTIC_PASSWORD="$pw" docker run --rm -e RESTIC_PASSWORD -e RESTIC_REPOSITORY=/repo -v /srv/houston-backups:/repo -v /tmp/restore:/restore \
  restic/restic:0.19.1 restore latest --host houston --tag project:spike,kind:auto --target /restore >/dev/null 2>&1 && ok "restic restored the latest snapshot" || bad "restore"
unset pw
vm grep -q "we'ird=db" /tmp/restore/out/houston.json && ok "the manifest names the hostile database" || bad "manifest"
dumps=$(vm sh -c 'for f in /tmp/restore/out/postgres/db/*.dump; do docker run --rm -v /tmp/restore:/tmp/restore postgres:17 pg_restore --list "$f"; done' | grep -c 'TABLE DATA public t ')
[ "$dumps" -ge 1 ] && ok "pg_restore --list reads the dump with table t's data" || bad "pg_restore: $dumps"
vm test -s /tmp/restore/out/postgres/db/globals.sql && ok "globals.sql (roles) is there" || bad "no globals.sql"
rows=$(vm docker run --rm --user 0 -v /tmp/restore:/r --entrypoint sqlite3 houston/mission-control:local /r/out/sqlite/1.sqlite3 'select count(*) from t')
[ "$rows" = 3 ] && ok "the SQLite copy opens with all 3 rows (they were only in the -wal)" || bad "sqlite copy: $rows"
vm test -f /tmp/restore/data/data/app.sqlite3 && bad "the live SQLite file was copied too" || ok "the live SQLite file and its -wal were left out of the file copy"
[ "$(vm cat /tmp/restore/data/data/probe)" = probe ] && ok "the volume's files are in the snapshot" || bad "no probe in the snapshot"

echo "== deploys #2-#4 (the HOLD was never a deploy): a pre-deploy snapshot each, the last 2 kept"
for n in 2 3 4; do
  as_houston "cd ~/spike && echo $n > version.txt && git add -A && $gitc commit -qm v$n && houston deploy" >"/tmp/backups-e2e-$n.log" 2>&1 || { bad "deploy #$n"; tail -20 "/tmp/backups-e2e-$n.log"; }
done
grep -q 'ok  snapshot [0-9a-f]\{8\} · kind:deploy' /tmp/backups-e2e-4.log && ok "deploy #4 logged its pre-deploy snapshot" || bad "no snapshot line: $(grep -i snapshot /tmp/backups-e2e-4.log)"
deploy_snaps=$(hou snapshots | grep -c ' deploy .*before deploy #')
[ "$deploy_snaps" = 2 ] && ok "3 pre-deploy snapshots, 2 kept (keep.deploy: 2)" || bad "pre-deploy snapshots kept: $deploy_snaps"
hou snapshots | grep -q 'before deploy #4' && ok "the newest is before deploy #4" || bad "$(hou snapshots)"

echo "== the schedule, in Europe/Berlin"
hou settings --time-zone Europe/Berlin | grep -q 'Europe/Berlin' && ok "houston settings --time-zone Europe/Berlin" || bad "settings"
rails "p = Project.find_by!(name: 'spike'); t = Time.current.in_time_zone('Europe/Berlin') + 2.minutes; p.update!(backup_schedule: format('daily %02d:%02d', t.hour, t.min))" >/dev/null
for _ in $(seq 1 40); do [ "$(rails "puts BackupRun.where(reason: 'schedule').pick(:status)")" = go ] && break; sleep 10; done
[ "$(rails "puts BackupRun.where(reason: 'schedule').pick(:status)")" = go ] && ok "the scheduled backup ran and is GO" || bad "scheduled: $(rails "puts BackupRun.where(reason: 'schedule').pluck(:status, :error).inspect")"
hou status --project spike | grep -q '^schedule  daily [0-9:]* (Europe/Berlin)$' && ok "houston status shows the schedule" || bad "status: $(hou status --project spike)"
hou snapshots | grep -q ' auto .*daily' && ok "houston snapshots lists the daily one" || bad "no daily snapshot"

echo "== prune"
pruned=$(rails 'PruneJob.perform_now; l = StorageLocation.find_by!(name: "local-backups"); puts [ l.pruned_at.present?, l.prune_error ].inspect')
[ "$pruned" = "[true, nil]" ] && ok "prune on local-backups" || bad "prune: $pruned"

if [ -n "${EQUIP_SOURCE:-}" ]; then
  echo "== equip (a copy): Rails' SQLite databases"
  mkdir -p "$repo/.houston"
  stage="$(mktemp -d "$repo/.houston/e2e.XXXXXX")" # .houston ignores itself; the machine sees /Users
  tar -C "$EQUIP_SOURCE" --exclude=./.houston --exclude=./.kamal --exclude=./.git --exclude=./.claude --exclude=./tmp --exclude=./log \
    --exclude=./storage --exclude=./node_modules --exclude=./.env --exclude=./config/master.key -cf - . | tar -C "$stage" -xf -
  for keep in storage/.keep tmp/.keep log/.keep; do
    if [ -f "$EQUIP_SOURCE/$keep" ]; then mkdir -p "$stage/$(dirname "$keep")" && cp "$EQUIP_SOURCE/$keep" "$stage/$keep"; fi
  done
  as_houston "rm -rf ~/equip && cp -r '$stage' ~/equip && cd ~/equip &&
    sed -i -e 's/^name: equip\$/name: houston-equip-test/' -e 's/\${RAILS_MASTER_KEY:-}/\${RAILS_MASTER_KEY}/' compose.yml &&
    git init -q -b main && git add -A && $gitc commit -qm equip"
  rm -rf "$stage"
  as_houston 'cd ~/equip && houston deploy' >/tmp/backups-e2e-equip-1.log 2>&1
  # The key goes from a private temp dir straight into houston secrets set's
  # stdin, as root in the machine (it can read a file only this Mac user can).
  keydir=$(mktemp -d "$repo/.houston/key.XXXXXX")
  (umask 077 && cp "$EQUIP_SOURCE/config/master.key" "$keydir/master.key")
  vm env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" sh -c \
    "houston secrets set RAILS_MASTER_KEY --project houston-equip-test < '$keydir/master.key'" >/dev/null && ok "RAILS_MASTER_KEY set from stdin" || bad "RAILS_MASTER_KEY"
  rm -rf "$keydir"; keydir=""
  as_houston 'cd ~/equip && houston deploy' >/tmp/backups-e2e-equip-2.log 2>&1 && ok "equip GO" || { bad "equip deploy"; tail -20 /tmp/backups-e2e-equip-2.log; }
  out=$(orb -m "$name" -u houston env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" bash -lc "cd ~/equip && houston backup --follow" 2>&1)
  printf '%s' "$out" | grep -q '^GO: houston-equip-test backed up' && ok "equip backed up" || bad "equip backup: $out"
  found=$(rails 'puts BackupRun.joins(:project).where(projects: { name: "houston-equip-test" }, status: "go").last.found["sqlite"].map { |f| f["path"] }.sort.join(" ")')
  printf '%s' "$found" | grep -q 'production.sqlite3' && ok "equip's SQLite databases found: $found" || bad "found: $found"
fi

echo
if [ "$failures" -eq 0 ]; then echo "BACKUPS PASS"; else echo "BACKUPS: $failures failure(s)"; exit 1; fi

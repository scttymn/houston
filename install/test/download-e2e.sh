#!/usr/bin/env bash
# docs/plans/download-snapshot.md, Batch 3: downloading a snapshot for real,
# on a fresh Houston server in an OrbStack machine, over port 3000 (the
# tunnel's stage is download-through-tunnel.sh). The spike fixture has
# Postgres (a hostile database name), a SQLite database held open in WAL
# mode, a photo, and 1 GiB of random bytes, so a download takes long enough
# to watch. It's downloaded from the page and from the CLI and opened; then
# a streaming check, two at once, a client that leaves, and a download
# broken mid-way. With EQUIP_SOURCE (a Houston-ready copy of equip, never
# your repo), equip's SQLite databases too.
#
#   install/test/download-e2e.sh          # KEEP=1 keeps the machine
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

name="houston-download-e2e"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$name" -u root "$@"; }
as_houston() { orb -m "$name" -u houston bash -lc "$1"; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1" 2>/dev/null | tail -1; }
mc_log() { vm docker compose -f /opt/houston/compose.yml logs --no-log-prefix mission-control 2>/dev/null | grep 'download: '; }
gitc='git -c user.name=e2e -c user.email=e2e@houston.test'

keydir=""
cleanup() {
  [ -n "$keydir" ] && rm -rf "$keydir"
  if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

echo "== creating $name and installing Houston"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-download-install.log 2>&1 || { tail -20 /tmp/houston-download-install.log; exit 1; }
vm sh -c 'DEBIAN_FRONTEND=noninteractive apt-get install -y -qq unzip >/dev/null 2>&1'
admin_password="dl-$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
made=$(rails "
  Installation.current.update!(base_domain: 'houston.test', cloudflare_connected_at: Time.current, dns_mode: 'wildcard')
  User.create!(email_address: 'admin@example.com', password: '$admin_password')
  s = StorageSetup.new(kind: 'local', name: 'local-backups', local_path: '/srv/houston-backups')
  abort(s.errors.full_messages.to_sentence + s.checks.map(&:label).join) unless s.save
  s.location.update!(acknowledged_at: Time.current, default: true)
  puts 'made'")
[ "$made" = made ] && ok "an admin, and local-backups (the default)" || { bad "setup: $made"; exit 1; }
token=$(rails 'puts ApiToken.issue!("e2e").first')
hou() { orb -m "$name" -u houston env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" bash -lc "cd ~/spike 2>/dev/null; houston $*"; }

echo "== the spike, deployed"
as_houston "rm -rf ~/spike && cp -r '$repo/internal/kamal/testdata/spike' ~/spike && cd ~/spike && git init -q -b main && git add -A && $gitc commit -qm spike"
as_houston 'cd ~/spike && houston deploy' >/tmp/download-e2e-1.log 2>&1
printf 'downloaded' | hou secrets set HOSTILE >/dev/null && hou secrets generate POSTGRES_PASSWORD >/dev/null && ok "secrets set" || bad "secrets"
as_houston 'cd ~/spike && houston deploy' >/tmp/download-e2e-2.log 2>&1 && ok "deploy GO" || { bad "deploy"; tail -20 /tmp/download-e2e-2.log; exit 1; }
web=$(vm sh -c 'docker ps -q --filter label=service=spike --filter label=role=web | head -n1')

echo "== data: Postgres with a hostile name, SQLite open in WAL mode, a photo, 1 GiB of random bytes"
vm docker exec spike-db psql -q -U postgres -c "create database \"we'ird=db\"" &&
  vm docker exec spike-db psql -q -U postgres -d "dbname='we\\'ird=db'" -c "create table t (n int); insert into t values (42)" &&
  ok "Postgres: we'ird=db, with a row" || bad "Postgres data"
vm docker run -d --name wal-holder --user 0 -v spike_data:/data --entrypoint sh houston/mission-control:local -c \
  '(echo "pragma journal_mode=wal; pragma wal_autocheckpoint=0; create table t (n); insert into t values (1),(2),(3);"; sleep 3600) | sqlite3 /data/app.sqlite3' >/dev/null
for _ in $(seq 1 30); do vm docker run --rm -v spike_data:/data busybox test -s /data/app.sqlite3-wal && break; sleep 1; done
vm docker exec "$web" sh -c 'mkdir -p /data/uploads && head -c 5000000 /dev/urandom > /data/uploads/photo.jpg && head -c 1073741824 /dev/urandom > /data/big.bin' &&
  ok "a photo in uploads/, and 1 GiB in big.bin" || bad "files"
photo_sum=$(vm docker exec "$web" sha256sum /data/uploads/photo.jpg | cut -d' ' -f1)

out=$(hou backup --follow 2>&1)
printf '%s' "$out" | grep -q '^GO: spike backed up' && ok "houston backup --follow: GO" || { bad "backup: $out"; exit 1; }
id=$(hou snapshots | head -n1 | awk '{print $NF}')
[[ "$id" =~ ^[0-9a-f]{8}$ ]] && ok "the snapshot: $id" || { bad "no snapshot id: $(hou snapshots)"; exit 1; }

echo "== houston snapshots download"
vm sh -c 'rm -rf /tmp/dl && mkdir -p /tmp/dl && chown houston /tmp/dl'
out=$(hou snapshots download "$id" -o /tmp/dl/cli.zip 2>&1); code=$?
[ "$code" = 0 ] && printf '%s' "$out" | grep -q "^Downloaded snapshot $id of spike to /tmp/dl/cli.zip (1\(\.[0-9]\)\{0,1\} GB)\.$" && ok "$out" || bad "download: exit $code: $out"
vm unzip -tq /tmp/dl/cli.zip >/dev/null 2>&1 && ok "unzip -t: the zip is whole" || bad "unzip -t failed"
listing=$(vm unzip -Z1 /tmp/dl/cli.zip)
for want in data/data/uploads/photo.jpg data/data/big.bin out/houston.json out/sqlite/1.sqlite3 out/postgres/db/globals.sql; do
  grep -qx "$want" <<<"$listing" && ok "it holds $want" || bad "no $want in: $(head -20 <<<"$listing")"
done
grep -qx 'data/data/app.sqlite3' <<<"$listing" && bad "the live SQLite file is in it" || ok "the live SQLite file and its -wal aren't (out/ has the safe copy)"
vm sh -c 'rm -rf /tmp/dl/x && mkdir /tmp/dl/x && cd /tmp/dl/x && unzip -q ../cli.zip'
[ "$(vm sha256sum /tmp/dl/x/data/data/uploads/photo.jpg | cut -d' ' -f1)" = "$photo_sum" ] && ok "the photo comes out byte for byte" || bad "the photo differs"
rows=$(vm docker run --rm --user 0 -v /tmp/dl/x:/x --entrypoint sqlite3 houston/mission-control:local /x/out/sqlite/1.sqlite3 'pragma integrity_check; select count(*) from t;' | tr '\n' ' ')
[ "$rows" = "ok 3 " ] && ok "the SQLite copy: integrity ok, all 3 rows (they were only in the -wal)" || bad "sqlite: $rows"
tables=$(vm sh -c 'for f in /tmp/dl/x/out/postgres/db/*.dump; do docker run --rm -v /tmp/dl/x:/tmp/dl/x postgres:17 pg_restore --list "$f"; done' | grep -c 'TABLE DATA public t ')
[ "$tables" -ge 1 ] && ok "pg_restore --list reads we'ird=db's dump, table t's data in it" || bad "pg_restore: $tables"
vm grep -q "we'ird=db" /tmp/dl/x/out/houston.json && ok "houston.json names the hostile database" || bad "manifest"
out=$(hou snapshots download "$id" -o /tmp/dl/cli.zip 2>&1); code=$?
[ "$code" = 1 ] && printf '%s' "$out" | grep -q 'already exists' && ok "again to the same file: refused, and it's untouched" || bad "overwrite: exit $code: $out"

echo "== from the page"
jar=/tmp/dl/jar
vm sh -c "rm -f $jar; t=\$(curl -s -c $jar -b $jar http://127.0.0.1:3000/sign-in | grep -o 'name=\"authenticity_token\" value=\"[^\"]*\"' | head -1 | sed 's/.*value=\"//;s/\"\$//');
  curl -s -o /dev/null -c $jar -b $jar -X POST http://127.0.0.1:3000/session --data-urlencode authenticity_token=\$t \
    --data-urlencode email_address=admin@example.com --data-urlencode password='$admin_password'"
frame=$(vm curl -s -b "$jar" "http://127.0.0.1:3000/projects/spike/snapshots")
grep -q "href=\"/projects/spike/snapshots/$id/download?location=local-backups\"" <<<"$frame" && ok "the snapshot's row links Download" || bad "no Download link"
link="http://127.0.0.1:3000/projects/spike/snapshots/$id/download?location=local-backups"
head=$(vm curl -s -D - -o /tmp/dl/page.zip -b "$jar" "$link" | tr -d '\r')
grep -qi '^content-disposition: attachment; filename="spike-[0-9]\{8\}-[0-9]\{4\}Z-'"$id"'.zip"' <<<"$head" && ok "an attachment named spike-…-$id.zip" || bad "headers: $head"
grep -qi '^transfer-encoding: chunked' <<<"$head" && ! grep -qi '^content-length' <<<"$head" && ok "chunked, with no Content-Length: streamed, not read whole first" || bad "not streamed: $head"
vm unzip -tq /tmp/dl/page.zip >/dev/null 2>&1 && ok "the page's zip is whole" || bad "the page's zip"
vm cmp -s /tmp/dl/page.zip /tmp/dl/cli.zip && ok "and byte for byte the CLI's" || ok "(the page's zip differs from the CLI's in bytes; both whole)"
vm rm -f /tmp/dl/page.zip /tmp/dl/cli.zip

echo "== it streams: first bytes at once, Mission Control's memory flat"
mc=$(vm docker compose -f /opt/houston/compose.yml ps -q mission-control)
# Mission Control's memory in bytes (docker stats says "312.5MiB / 7.7GiB").
mem() { vm docker stats --no-stream --format '{{.MemUsage}}' "$mc" | awk '{ v = $1 + 0; u = $1; gsub(/[0-9.]/, "", u)
  m = (u == "KiB") ? 1024 : (u == "MiB") ? 1048576 : (u == "GiB") ? 1073741824 : 1; printf "%d\n", v * m }'; }
before=$(mem)
vm sh -c "curl -s -o /dev/null -w '%{time_starttransfer} %{time_total} %{size_download}' --limit-rate 50M -b $jar '$link' > /tmp/dl/timing" &
slow=$!
peak=0
for _ in $(seq 1 20); do m=$(mem); [ "$m" -gt "$peak" ] && peak=$m; sleep 1; done
wait $slow
read -r first total size < <(vm cat /tmp/dl/timing)
awk -v f="$first" -v t="$total" 'BEGIN { exit !(f < 10 && t > 3 * f) }' && ok "first byte after ${first}s, the whole ${size} bytes after ${total}s" || bad "timing: first $first, total $total"
grow=$(( (peak - before) / 1048576 ))
[ "$grow" -lt 200 ] && ok "Mission Control grew ${grow} MiB while 1 GiB passed through it" || bad "Mission Control grew ${grow} MiB"

echo "== one at a time"
vm sh -c "curl -s -o /dev/null --limit-rate 2M -b $jar '$link'" &
first_dl=$!
for _ in $(seq 1 30); do vm docker ps -q --filter name=^houston-export\$ | grep -q . && break; sleep 0.5; done
out=$(hou snapshots download "$id" -o /tmp/dl/second.zip 2>&1); code=$?
[ "$code" = 1 ] && printf '%s' "$out" | grep -q 'another download started at' && ok "a second (CLI) is refused: $out" || bad "second: exit $code: $out"
second=$(vm curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -b "$jar" "$link")
[ "$second" = "302 http://127.0.0.1:3000/projects/spike" ] && ok "a second (page) goes back to the project, with the reason" || bad "page second: $second"
vm test -e /tmp/dl/second.zip -o -e /tmp/dl/second.zip.part && bad "the refused download left a file" || ok "the refused download left no file"

echo "== the client leaves"
vm pkill -f -- "--limit-rate 2M" ; wait $first_dl 2>/dev/null
for _ in $(seq 1 20); do vm docker ps -aq --filter name=^houston-export\$ | grep -q . || break; sleep 0.5; done
vm docker ps -aq --filter name=^houston-export\$ | grep -q . && bad "houston-export is still there" || ok "houston-export is gone"
mc_log | grep -q "spike $id from local-backups: stopped by the client after" && ok "logged: stopped by the client" || bad "log: $(mc_log | tail -3)"
out=$(hou snapshots download "$id" -o /tmp/dl/after.zip 2>&1) && ok "the next download runs" || bad "after: $out"
vm rm -f /tmp/dl/after.zip

echo "== broken mid-way"
vm sh -c "curl -s -o /tmp/dl/broken.zip --limit-rate 2M -b $jar '$link'; echo \$? > /tmp/dl/broken.code" &
broken_dl=$!
for _ in $(seq 1 30); do vm docker ps -q --filter name=^houston-export\$ | grep -q . && break; sleep 0.5; done
sleep 2
vm docker kill houston-export >/dev/null
wait $broken_dl
code=$(vm cat /tmp/dl/broken.code)
[ "$code" != 0 ] && ok "curl exits $code: the browser would mark it failed, not done" || bad "a broken download ended cleanly"
mc_log | grep -q "spike $id from local-backups: broke after" && ok "logged: broke after …" || bad "log: $(mc_log | tail -3)"
vm unzip -tq /tmp/dl/broken.zip >/dev/null 2>&1 && bad "the broken zip opens" || ok "and what arrived isn't a zip that opens"
mc_log | grep -q 'download: admin@example.com spike' && mc_log | grep -q 'download: token e2e spike' && ok "each line says who: the admin, or the token" || bad "who"

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
  as_houston 'cd ~/equip && houston deploy' >/tmp/download-e2e-equip-1.log 2>&1
  keydir=$(mktemp -d "$repo/.houston/key.XXXXXX")
  (umask 077 && cp "$EQUIP_SOURCE/config/master.key" "$keydir/master.key")
  vm env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" sh -c \
    "houston secrets set RAILS_MASTER_KEY --project houston-equip-test < '$keydir/master.key'" >/dev/null && ok "RAILS_MASTER_KEY set from stdin" || bad "RAILS_MASTER_KEY"
  rm -rf "$keydir"; keydir=""
  as_houston 'cd ~/equip && houston deploy' >/tmp/download-e2e-equip-2.log 2>&1 && ok "equip GO" || { bad "equip deploy"; tail -20 /tmp/download-e2e-equip-2.log; }
  equip() { orb -m "$name" -u houston env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" bash -lc "cd ~/equip && houston $*"; }
  out=$(equip backup --follow 2>&1)
  printf '%s' "$out" | grep -q '^GO: houston-equip-test backed up' && ok "equip backed up" || bad "equip backup: $out"
  eid=$(equip snapshots | head -n1 | awk '{print $NF}')
  equip snapshots download "$eid" -o /tmp/dl/equip.zip >/dev/null 2>&1 && ok "equip's snapshot downloaded" || bad "equip download"
  vm sh -c 'rm -rf /tmp/dl/e && mkdir /tmp/dl/e && cd /tmp/dl/e && unzip -q ../equip.zip'
  checked=$(vm sh -c 'for f in /tmp/dl/e/out/sqlite/*.sqlite3; do docker run --rm --user 0 -v /tmp/dl/e:/tmp/dl/e --entrypoint sqlite3 houston/mission-control:local "$f" "pragma integrity_check"; done' | sort | uniq -c | tr -s ' ')
  sqlite_count=$(vm sh -c 'ls /tmp/dl/e/out/sqlite/*.sqlite3 | wc -l')
  [ "$sqlite_count" -ge 1 ] && [ "$checked" = " $sqlite_count ok" ] && ok "equip's $sqlite_count SQLite copies pass integrity_check" || bad "equip sqlite: $checked ($sqlite_count)"
  vm grep -q 'production.sqlite3' /tmp/dl/e/out/houston.json && ok "houston.json maps production.sqlite3" || bad "equip manifest"
fi

echo
if [ "$failures" -eq 0 ]; then echo "DOWNLOAD PASS"; else echo "DOWNLOAD: $failures failure(s)"; exit 1; fi

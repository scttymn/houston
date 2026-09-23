#!/usr/bin/env bash
# Build step 6, Batch 3: data generations with the real Kamal, on a fresh
# Houston server in an OrbStack machine (wildcard mode; nothing through the
# tunnel). The spike fixture is deployed at generation 1, then the project is
# set to generation 2 (as a restore will, in Batch 6) and deployed again with
# an ordinary houston deploy. Checks what the restore's zero-downtime plan
# leans on: a second generation's accessories beside the first, the app
# reaching them by name, its own volumes, and an image pulled back from the
# registry after the host's copy is gone.
#
#   install/test/generations-e2e.sh          # KEEP=1 keeps the machine
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

name="houston-generations-e2e"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$name" -u root "$@"; }
as_houston() { orb -m "$name" -u houston bash -lc "$1"; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1" 2>/dev/null | tail -1; }
through_proxy() { vm docker run --rm --network kamal curlimages/curl:8.16.0 -s -H "Host: spike.houston.test" "http://kamal-proxy$1"; }
gitc='git -c user.name=e2e -c user.email=e2e@houston.test'

cleanup() { if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi; }
trap cleanup EXIT

echo "== creating $name and installing Houston"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-generations-install.log 2>&1 || { tail -20 /tmp/houston-generations-install.log; exit 1; }
rails 'Installation.current.update!(base_domain: "houston.test", cloudflare_connected_at: Time.current, dns_mode: "wildcard")' >/dev/null
made=$(rails "s = StorageSetup.new(kind: 'local', name: 'local-backups', local_path: '/srv/houston-backups')
  abort(s.errors.full_messages.to_sentence + s.checks.map(&:label).join) unless s.save
  s.location.update!(acknowledged_at: Time.current, default: true); puts 'made'")
[ "$made" = made ] && ok "backup storage (every deploy takes a pre-deploy snapshot)" || bad "storage: $made"
vm docker pull -q curlimages/curl:8.16.0 >/dev/null

echo "== generation 1"
as_houston "rm -rf ~/spike && cp -r '$repo/internal/kamal/testdata/spike' ~/spike && cd ~/spike && git init -q -b main && git add -A && $gitc commit -qm spike"
as_houston 'cd ~/spike && houston deploy' >/dev/null 2>&1 # HOLD: records the project
rails 'p = Project.find_by!(name: "spike"); p.secrets.create!(key: "HOSTILE", value: "gen"); p.secrets.create!(key: "POSTGRES_PASSWORD", value: "spike-pw")' >/dev/null
first=$(as_houston 'cd ~/spike && git rev-parse HEAD')
as_houston 'cd ~/spike && houston deploy' >/tmp/generations-1.log 2>&1 && ok "deploy at generation 1: GO" || { bad "deploy 1"; tail -20 /tmp/generations-1.log; }
[ "$(through_proxy /env/DB_HOST)" = spike-db ] && ok "DB_HOST is spike-db" || bad "DB_HOST: $(through_proxy /env/DB_HOST)"

echo "== generation 2, beside it"
rails 'Project.find_by!(name: "spike").update!(data_generation: 2)' >/dev/null
as_houston "cd ~/spike && echo two > version.txt && git add -A && $gitc commit -qm two"
as_houston 'cd ~/spike && houston deploy' >/tmp/generations-2.log 2>&1 && ok "deploy at generation 2: GO" || { bad "deploy 2"; tail -30 /tmp/generations-2.log; }
running=$(vm docker ps --format '{{.Names}}' | grep -E '^spike-db(-g2)?$' | sort | tr '\n' ' ')
[ "$running" = "spike-db spike-db-g2 " ] && ok "generation 2's Postgres (spike-db-g2) runs beside generation 1's" || bad "accessories: $running"
[ "$(through_proxy /env/DB_HOST)" = spike-db-g2 ] && ok "the app's DB_HOST is spike-db-g2" || bad "DB_HOST: $(through_proxy /env/DB_HOST)"
through_proxy /env/lookup | grep -q 'spike-db-g2' && ! through_proxy /env/lookup | grep -q 'lookup failed' && ok "and it resolves on the kamal network" || bad "lookup: $(through_proxy /env/lookup)"
web=$(vm sh -c 'docker ps -q --filter label=service=spike --filter label=role=web | head -n1')
vm docker inspect --format '{{range .Mounts}}{{.Name}} {{end}}' "$web" | grep -qw 'spike.g2_data' && ok "the app mounts spike.g2_data" || bad "mounts: $(vm docker inspect --format '{{range .Mounts}}{{.Name}} {{end}}' "$web")"
vm docker volume inspect spike.g2_pgdata >/dev/null 2>&1 && ok "spike.g2_pgdata is generation 2's Postgres volume" || bad "no spike.g2_pgdata"

vm docker rm -f spike-db >/dev/null
[ "$(through_proxy /up)" = ok ] && ok "generation 1's Postgres removed: the app still serves" || bad "after removing spike-db: $(through_proxy /up)"

echo "== an image pulled back from the registry"
vm docker rmi -f "127.0.0.1:5000/spike:$first" >/dev/null 2>&1
vm docker image inspect "127.0.0.1:5000/spike:$first" >/dev/null 2>&1 && bad "the host still has it" || ok "the host's copy of $first is gone"
vm docker pull -q "127.0.0.1:5000/spike:$first" >/dev/null && ok "docker pull gets it back from Houston's registry" || bad "pull"

echo
if [ "$failures" -eq 0 ]; then echo "GENERATIONS PASS"; else echo "GENERATIONS: $failures failure(s)"; exit 1; fi

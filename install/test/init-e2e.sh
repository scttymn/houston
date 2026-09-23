#!/usr/bin/env bash
# Build step 7, Batch 2: projects set up by the generic `houston init`,
# deployed by hand on a fresh Houston server in an OrbStack machine (wildcard
# mode; nothing through the tunnel).
#   - A static site: `houston init` and nothing else. It must deploy as
#     written, and never serve .git or .env.
#   - With EQUIP_SOURCE and ESTHER_SOURCE (copies set up by `houston init`
#     plus the edits their developer would make; never your repos): a Rails
#     app and a Phoenix app, each deployed GO and answering.
#
#   install/test/init-e2e.sh          # KEEP=1 keeps the machine
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

name="houston-init-e2e"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$name" -u root "$@"; }
as_houston() { orb -m "$name" -u houston bash -lc "$1"; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1" 2>/dev/null | tail -1; }
code_at() { vm docker run --rm --network kamal curlimages/curl:8.16.0 -s -o /dev/null -w '%{http_code}' -H "Host: $1.houston.test" "http://kamal-proxy$2"; }
body_at() { vm docker run --rm --network kamal curlimages/curl:8.16.0 -s -H "Host: $1.houston.test" "http://kamal-proxy$2"; }
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
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-init-install.log 2>&1 || { tail -20 /tmp/houston-init-install.log; exit 1; }
rails 'Installation.current.update!(base_domain: "houston.test", cloudflare_connected_at: Time.current, dns_mode: "wildcard")' >/dev/null
vm docker pull -q curlimages/curl:8.16.0 >/dev/null
token=$(rails 'puts ApiToken.issue!("e2e").first')
hou() { orb -m "$name" -u houston env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" bash -lc "houston $*"; }

echo "== a static site: houston init, nothing else"
as_houston "rm -rf ~/site && mkdir ~/site && cd ~/site &&
  printf '<!doctype html>\n<title>houston static test</title>\n<p>static site served by Houston</p>\n' > index.html &&
  mv ~/site ~/houston-static-test && cd ~/houston-static-test && git init -q -b main && houston init" >/tmp/init-e2e-static-init.log 2>&1 &&
  ok "houston init: $(tr '\n' ' ' </tmp/init-e2e-static-init.log | head -c 160)" || bad "init: $(cat /tmp/init-e2e-static-init.log)"
# What a real folder has beside the site. .env is gitignored, so the checkout
# stays clean and it's still in the build context: .dockerignore must keep it out.
as_houston "cd ~/houston-static-test && echo SECRET=do-not-serve > .env && $gitc add -A && $gitc commit -qm site"
as_houston 'cd ~/houston-static-test && houston deploy' >/tmp/init-e2e-static.log 2>&1 && ok "deploy GO, as init wrote it" || { bad "static deploy"; tail -20 /tmp/init-e2e-static.log; }
body_at houston-static-test / | grep -q "static site served by Houston" && ok "kamal-proxy serves index.html (health / passed)" || bad "GET /: $(body_at houston-static-test /)"
for path in /.env /.git/HEAD /compose.yml /Dockerfile; do
  [ "$(code_at houston-static-test "$path")" = 404 ] && ok "GET $path → 404" || bad "GET $path → $(code_at houston-static-test "$path")"
done

# Stages a copy into ~/<project> as the houston user, with fresh history.
stage() { # source, project
  local tmp
  mkdir -p "$repo/.houston"
  tmp="$(mktemp -d "$repo/.houston/e2e.XXXXXX")" # .houston ignores itself; the machine sees /Users
  tar -C "$1" --exclude=./.git --exclude=./.env --exclude=./config/master.key --exclude=./.houston -cf - . | tar -C "$tmp" -xf -
  as_houston "rm -rf ~/$2 && cp -r '$tmp' ~/$2 && cd ~/$2 && git init -q -b main && $gitc add -A && $gitc commit -qm '$2'"
  rm -rf "$tmp"
}

if [ -n "${EQUIP_SOURCE:-}" ]; then
  echo "== equip (Rails, SQLite): init's completed Dockerfile plus its edits"
  stage "$EQUIP_SOURCE" houston-equip-test
  # A fresh checkout has no .env: init writes its template, and nothing else.
  out=$(as_houston 'cd ~/houston-equip-test && houston init' 2>&1)
  [ "$(printf '%s\n' "$out" | grep -vc '^created .env\|^Next:')" = 0 ] && ok "houston init on the checkout: only .env ($(printf '%s' "$out" | head -1))" || bad "init changed more than .env: $out"
  as_houston 'cd ~/houston-equip-test && houston deploy' >/tmp/init-e2e-equip-hold.log 2>&1
  grep -q "HOLD: RAILS_MASTER_KEY" /tmp/init-e2e-equip-hold.log && ok "HOLD for RAILS_MASTER_KEY" || bad "hold: $(tail -3 /tmp/init-e2e-equip-hold.log)"
  # The key goes from a private temp dir straight into houston secrets set's
  # stdin, as root in the machine; it's never printed.
  keydir=$(mktemp -d "$repo/.houston/key.XXXXXX")
  (umask 077 && cp "$EQUIP_SOURCE/config/master.key" "$keydir/master.key")
  vm env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" sh -c \
    "houston secrets set RAILS_MASTER_KEY --project houston-equip-test < '$keydir/master.key'" >/dev/null && ok "RAILS_MASTER_KEY set from stdin" || bad "RAILS_MASTER_KEY"
  rm -rf "$keydir"; keydir=""
  as_houston 'cd ~/houston-equip-test && houston deploy' >/tmp/init-e2e-equip.log 2>&1 && ok "equip deploy GO" || { bad "equip deploy"; tail -20 /tmp/init-e2e-equip.log; }
  [ "$(code_at houston-equip-test /up)" = 200 ] && ok "equip /up → 200" || bad "equip /up → $(code_at houston-equip-test /up)"
fi

if [ -n "${ESTHER_SOURCE:-}" ]; then
  echo "== estherpictures (Phoenix, SQLite): init's completed Dockerfile plus its edits"
  stage "$ESTHER_SOURCE" houston-esther-test
  # A fresh checkout has no .env: init writes its template, and nothing else.
  out=$(as_houston 'cd ~/houston-esther-test && houston init' 2>&1)
  [ "$(printf '%s\n' "$out" | grep -vc '^created .env\|^Next:')" = 0 ] && ok "houston init on the checkout: only .env ($(printf '%s' "$out" | head -1))" || bad "init changed more than .env: $out"
  as_houston 'cd ~/houston-esther-test && houston deploy' >/tmp/init-e2e-esther-hold.log 2>&1
  grep -q "HOLD: PHX_HOST, SECRET_KEY_BASE" /tmp/init-e2e-esther-hold.log && ok "HOLD for PHX_HOST and SECRET_KEY_BASE" || bad "hold: $(tail -3 /tmp/init-e2e-esther-hold.log)"
  hou secrets generate SECRET_KEY_BASE --project houston-esther-test >/dev/null && ok "SECRET_KEY_BASE generated (long enough for Phoenix)" || bad "generate"
  printf 'houston-esther-test.houston.test' | hou secrets set PHX_HOST --project houston-esther-test >/dev/null && ok "PHX_HOST set" || bad "PHX_HOST"
  as_houston 'cd ~/houston-esther-test && houston deploy' >/tmp/init-e2e-esther.log 2>&1 && ok "estherpictures deploy GO (release hook: bin/migrate)" || { bad "esther deploy"; tail -20 /tmp/init-e2e-esther.log; }
  body_at houston-esther-test / | grep -q "<title[^>]*>Esther Pictures" && ok "its home page answers" || bad "home: $(code_at houston-esther-test /)"
  vm docker volume inspect houston-esther-test_data >/dev/null 2>&1 && ok "its data volume (SQLite and uploads) exists" || bad "no data volume"
fi

echo
if [ "$failures" -eq 0 ]; then echo "INIT PASS"; else echo "INIT: $failures failure(s)"; exit 1; fi

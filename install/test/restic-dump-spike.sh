#!/usr/bin/env bash
# The download spike (docs/plans/download-snapshot.md, Batch 1): pins what
# a snapshot download leans on. restic's zip of a whole snapshot, its
# failures, and Docker's container name as a one-at-a-time lock. Needs only
# Docker; changes nothing outside its own volumes and containers.
#
#   install/test/restic-dump-spike.sh
# shellcheck disable=SC2015 # ok/bad return 0
set -uo pipefail

image="restic/restic:0.19.1"
repo="houston-spike-dump-repo" data="houston-spike-dump-data" out="houston-spike-dump-out"
work=$(mktemp -d)
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
export RESTIC_PASSWORD="spike-$RANDOM-$RANDOM" RESTIC_REPOSITORY=/repo
restic() { docker run --rm -i -e RESTIC_PASSWORD -e RESTIC_REPOSITORY -v "$repo:/repo" -v "$data:/data:ro" -v "$out:/out:ro" "$image" "$@"; }
in_volumes() { docker run --rm -v "$data:/data" -v "$out:/out" --entrypoint sh "$image" -c "$1"; }
locks() { docker run --rm -v "$repo:/repo" --entrypoint sh "$image" -c 'ls /repo/locks | wc -l' | tr -d ' '; }

cleanup() {
  docker rm -f houston-spike-lock houston-spike-export >/dev/null 2>&1 || true
  docker volume rm -f "$repo" "$data" "$out" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
cleanup; work=$(mktemp -d)
for v in "$repo" "$data" "$out"; do docker volume create "$v" >/dev/null; done
docker pull -q "$image" >/dev/null
restic init >/dev/null 2>&1 || bad "init"

echo "== a whole snapshot as a zip"
in_volumes 'mkdir -p /data/app/empty /data/app/img /out/sqlite && head -c 200000 /dev/urandom > /data/app/img/a.jpg &&
  ln -s img/a.jpg /data/app/link && yes hello | head -c 300000 > /out/sqlite/1.sqlite3 && echo "{}" > /out/houston.json'
id=$(restic backup --quiet --host houston --json /data /out | grep '"summary"' | python3 -c 'import json,sys; print(json.load(sys.stdin)["snapshot_id"])')
restic dump --quiet --archive zip "$id" / > "$work/whole.zip"; code=$?
names=$(unzip -Z1 "$work/whole.zip" | sort | tr '\n' ' ')
[ "$code" = 0 ] && [ "$names" = "data/ data/app/ data/app/empty/ data/app/img/ data/app/img/a.jpg data/app/link out/ out/houston.json out/sqlite/ out/sqlite/1.sqlite3 " ] &&
  ok "dump / gives both trees as data/… and out/…, empty folders too" || bad "names ($code): $names"
listing=$(unzip -Z "$work/whole.zip")  # captured: grep -q would SIGPIPE unzip under pipefail
grep -q '^lrwx.*data/app/link$' <<<"$listing" && ok "a symlink stays a symlink" || bad "symlink: $(grep link <<<"$listing")"
grep -q 'Defl:N.*out/sqlite/1.sqlite3' <<<"$(unzip -v "$work/whole.zip")" && ok "entries are deflated" || bad "not deflated"
grep -q 'P   K 003 004' <<<"$(head -c 4 "$work/whole.zip" | od -An -c)" && ok "it starts with a zip header" || bad "header: $(head -c 8 "$work/whole.zip" | od -An -c)"
unzip -p "$work/whole.zip" data/app/img/a.jpg | cmp -s - <(docker run --rm -v "$data:/data" --entrypoint cat "$image" /data/app/img/a.jpg) &&
  ok "a file comes out byte for byte" || bad "a.jpg differs"

echo "== zip64: a file past 4 GiB"
in_volumes 'truncate -s 4608M /data/app/big.bin && echo end >> /data/app/big.bin'
big=$(restic backup --quiet --host houston --json /data /out | grep '"summary"' | python3 -c 'import json,sys; print(json.load(sys.stdin)["snapshot_id"])')
restic dump --quiet --archive zip "$big" / > "$work/big.zip"
unzip -tq "$work/big.zip" >/dev/null 2>&1 && grep -q '^4831838212 .*data/app/big.bin$' <<<"$(unzip -v "$work/big.zip")" &&
  ok "a 4.5 GiB file comes out whole (unzip -t passes)" || bad "zip64: $(unzip -v "$work/big.zip" | grep big.bin)"
rm -f "$work/big.zip"

echo "== failures write nothing to stdout"
fails() { # label, expected code, restic args…
  local label="$1" want="$2"; shift 2
  restic "$@" > "$work/f" 2> "$work/e"; local code=$?
  [ "$code" = "$want" ] && [ ! -s "$work/f" ] && ok "$label: $code, nothing on stdout ($(tail -1 "$work/e" | cut -c1-70))" ||
    bad "$label: $code, $(wc -c < "$work/f") bytes on stdout: $(head -c 80 "$work/f")"
}
fails "an unknown snapshot" 1 dump --quiet --archive zip deadbeef /
fails "a path not in it" 1 dump --quiet --archive zip "$id" /nope
RESTIC_PASSWORD=wrong fails "a wrong password" 12 dump --quiet --archive zip "$id" /

echo "== a repository locked by prune"
# A download-limited prune holds its exclusive lock for a few seconds.
docker run -d --name houston-spike-lock -e RESTIC_PASSWORD -e RESTIC_REPOSITORY -v "$repo:/repo" "$image" prune --limit-download 1 >/dev/null
for _ in $(seq 50); do [ "$(locks)" -gt 0 ] && break; sleep 0.1; done
restic dump --archive zip "$id" / > "$work/f" 2>/dev/null; code=$?
[ "$code" = 11 ] && grep -q 'repo already locked' "$work/f" && ok "without --quiet: 11, and 'repo already locked' is written to STDOUT (so --quiet is required)" ||
  bad "unquiet lock: $code, stdout: $(head -c 80 "$work/f")"
fails "with --quiet, locked" 11 dump --quiet --archive zip "$id" /
( sleep 2; docker rm -f houston-spike-lock >/dev/null; restic unlock --remove-all >/dev/null 2>&1 ) &
restic dump --quiet --retry-lock 30s --archive zip "$id" / > "$work/retried.zip" 2>/dev/null; code=$?; wait
[ "$code" = 0 ] && unzip -tq "$work/retried.zip" >/dev/null 2>&1 && ok "--quiet --retry-lock waits out the lock and the zip is clean" || bad "retry: $code"

echo "== the container name is a lock"
docker run -d --name houston-spike-export --entrypoint sleep "$image" 30 >/dev/null
docker run --rm --name houston-spike-export --entrypoint echo "$image" hi >/dev/null 2>"$work/e"; code=$?
[ "$code" = 125 ] && grep -q 'is already in use' "$work/e" && ok "a second run with the name: 125, 'is already in use', before it starts" || bad "name: $code $(cat "$work/e")"
created=$(docker inspect -f '{{.Created}}' houston-spike-export)
[[ "$created" =~ ^20[0-9-]+T[0-9:.]+Z$ ]] && ok "docker inspect gives its start time ($created)" || bad "created: $created"
docker rm -f houston-spike-export >/dev/null

echo
if [ "$failures" = 0 ]; then echo "RESTIC DUMP SPIKE PASS"; else echo "RESTIC DUMP SPIKE FAIL ($failures)"; exit 1; fi

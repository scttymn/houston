#!/usr/bin/env bash
# Build step 5's restic spike (spec §14): pins the restic behaviour Houston's
# backups lean on, with the pinned image against throwaway volumes. Needs only
# Docker; changes nothing outside its own volumes.
#
#   install/test/restic-spike.sh
# shellcheck disable=SC2015 # ok/bad return 0
set -uo pipefail

image="restic/restic:0.19.1"
repo="houston-spike-restic-repo" data="houston-spike-restic-data"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
export RESTIC_PASSWORD="spike-$RANDOM-$RANDOM" RESTIC_REPOSITORY=/repo
restic() { docker run --rm -i -e RESTIC_PASSWORD -e RESTIC_REPOSITORY -v "$repo:/repo" -v "$data:/data" "$image" "$@"; }
restic_as() { local user="$1"; shift; docker run --rm -i --user "$user" -e HOME=/tmp -e RESTIC_PASSWORD -e RESTIC_REPOSITORY -v "$repo:/repo" -v "$data:/data" "$image" "$@"; }
in_data() { docker run --rm -v "$data:/data" --entrypoint sh "$image" -c "$1"; }
count() { restic snapshots --json "$@" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))'; }

cleanup() { docker volume rm -f "$repo" "$data" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
docker volume create "$repo" >/dev/null && docker volume create "$data" >/dev/null
docker pull -q "$image" >/dev/null

echo "== init and the backup summary"
restic init >/dev/null 2>&1 && ok "init" || bad "init"
in_data 'mkdir -p /data/app /data/out && echo hello > /data/app/a.txt && echo dump > /data/out/d.dump'
summary=$(restic backup --host houston --json --tag project:x --tag kind:deploy --tag sha:1 /data/app | grep '"message_type":"summary"')
printf '%s' "$summary" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert len(d["snapshot_id"])==64 and d["total_bytes_processed"]>0, d; print("  ok    summary: snapshot_id", d["snapshot_id"][:8], "total_bytes_processed", d["total_bytes_processed"])' || bad "summary: $summary"

echo "== tags: comma is AND, repeated --tag is OR"
restic backup --host houston --json --tag project:x --tag kind:deploy --tag sha:2 /data/app /data/out >/dev/null
restic backup --host houston --json --tag project:x --tag kind:deploy --tag sha:3 /data/out >/dev/null
restic backup --host houston --json --tag project:y --tag kind:deploy --tag sha:9 /data/app >/dev/null
restic backup --host houston --json --tag project:x --tag kind:auto --tag sha:3 /data/app >/dev/null
[ "$(count --tag project:x,kind:deploy)" = 3 ] && ok "--tag project:x,kind:deploy: the 3 that have both" || bad "AND: $(count --tag project:x,kind:deploy)"
[ "$(count --tag project:x --tag kind:deploy)" = 5 ] && ok "--tag project:x --tag kind:deploy: either one (5)" || bad "OR: $(count --tag project:x --tag kind:deploy)"

echo "== forget: grouping"
removed=$(restic forget --dry-run --json --host houston --tag project:x,kind:deploy --keep-last 2 | python3 -c 'import json,sys; print(sum(len(g.get("remove") or []) for g in json.load(sys.stdin)))')
[ "$removed" = 0 ] && ok "default grouping (host, paths): keep-last 2 removes nothing when the paths differ" || bad "default grouping removed $removed"
restic forget --json --host houston --tag project:x,kind:deploy --group-by '' --keep-last 2 >/dev/null
[ "$(count --tag project:x,kind:deploy)" = 2 ] && ok "--group-by '' --keep-last 2: 2 kept across different paths" || bad "keep-last: $(count --tag project:x,kind:deploy)"
[ "$(count --tag project:y)" = 1 ] && [ "$(count --tag project:x,kind:auto)" = 1 ] && ok "the tag filter left other projects and kinds alone" || bad "forget touched others"
newest=$(restic snapshots --json --tag project:x,kind:deploy | python3 -c 'import json,sys; print(sorted(t for s in json.load(sys.stdin) for t in s["tags"] if t.startswith("sha:")))')
[ "$newest" = "['sha:2', 'sha:3']" ] && ok "the newest two stayed ($newest)" || bad "kept $newest"

echo "== forget: --keep-daily counts days"
for t in "2026-09-20 03:00:00" "2026-09-20 11:20:00" "2026-09-21 03:00:00" "2026-09-22 03:00:00"; do
  restic backup --host houston --json --tag project:z --tag kind:auto --time "$t" /data/app >/dev/null
done
restic forget --json --host houston --tag project:z,kind:auto --group-by '' --keep-daily 2 >/dev/null
days=$(restic snapshots --json --tag project:z | python3 -c 'import json,sys; print(sorted(s["time"][:10] for s in json.load(sys.stdin)))')
[ "$days" = "['2026-09-21', '2026-09-22']" ] && ok "--keep-daily 2 of 4 snapshots over 3 days: the last 2 days ($days)" || bad "keep-daily: $days"

echo "== --exclude-file with literal names"
in_data 'cd /data/app && echo live > "we*ird[1]?.db" && echo other > weXird1Y.db && echo s > "back\slash" && echo t > backXslash && echo w > "we*ird[1]?.db-wal"'
printf '%s\n' '/data/app/we\*ird\[1\]\?.db' '/data/app/we\*ird\[1\]\?.db-wal' '/data/app/back\\slash' > /tmp/houston-spike-exclude
docker run --rm -i -e RESTIC_PASSWORD -e RESTIC_REPOSITORY -v "$repo:/repo" -v "$data:/data" -v /tmp/houston-spike-exclude:/exclude:ro "$image" \
  backup --host houston --json --tag project:e --exclude-file /exclude /data/app >/dev/null
files=$(restic ls --json latest --tag project:e | python3 -c 'import json,sys
names=[json.loads(l).get("name") for l in sys.stdin if "\"struct_type\":\"node\"" in l]
print(sorted(n for n in names if n))')
[ "$files" = "['a.txt', 'app', 'backXslash', 'data', 'weXird1Y.db']" ] && ok "escaped patterns excluded exactly the three literal names ($files)" || bad "exclude: $files"
rm -f /tmp/houston-spike-exclude

echo "== exit codes"
restic backup --host houston --tag project:c /data/app >/dev/null 2>&1; code=$?
[ "$code" = 0 ] && ok "a clean backup: 0" || bad "clean backup: $code"
in_data 'echo secret > /data/app/unreadable && chmod 000 /data/app/unreadable'
docker run --rm -v "$repo:/repo" --entrypoint sh "$image" -c 'chmod -R a+rwX /repo'
restic_as 65534 backup --host houston --tag project:c /data/app >/dev/null 2>&1; code=$?
[ "$code" = 3 ] && ok "a file it can't read: 3 (the snapshot is made, incomplete)" || bad "unreadable: $code"
RESTIC_PASSWORD=wrong restic snapshots >/dev/null 2>&1; code=$?
[ "$code" = 12 ] && ok "a wrong password: 12" || bad "wrong password: $code"
restic backup --host houston /data/nothing-here >/dev/null 2>&1; code=$?
[ "$code" = 1 ] && ok "a missing path: 1" || bad "missing path: $code"

echo
if [ "$failures" -eq 0 ]; then echo "RESTIC SPIKE PASS"; else echo "RESTIC SPIKE: $failures failure(s)"; exit 1; fi

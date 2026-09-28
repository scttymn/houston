#!/usr/bin/env bash
# docs/plans/download-snapshot.md, Batch 3: downloading a snapshot through
# Cloudflare, a stage of install/test/orbstack.sh (after
# deploy-through-tunnel.sh, which deploys houston-spike-test). This Mac's
# houston, with only HOUSTON_SERVER and HOUSTON_API_TOKEN, backs it up and
# downloads the snapshot through https://admin.<base>; then a download is
# broken mid-way on the server, and curl on this Mac must see a failed
# transfer, not a finished one (else a browser would save a cut-off zip as done).
#
#   install/test/download-through-tunnel.sh <machine> <base domain>
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

machine="$1" base="$2"
app="houston-spike-test"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$machine" -u root "$@"; }

arch=$(uname -m); [ "$arch" = x86_64 ] && arch=amd64
[ -x "$repo/.houston/houston-mac" ] ||
  (cd "$repo" && docker compose --progress quiet run --rm -e GOOS=darwin -e GOARCH="$arch" cli go build -o .houston/houston-mac ./cmd/houston) || { bad "building the Mac CLI"; exit 1; }
tmp_home=$(mktemp -d)
trap 'rm -rf "$tmp_home"' EXIT
houston() { (cd "$tmp_home" && HOME="$tmp_home" "$repo/.houston/houston-mac" "$@"); }
token=$(vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner 'puts ApiToken.issue!("download").first' 2>/dev/null | tail -1)
[ -n "$token" ] || { bad "making an API token"; exit 1; }
export HOUSTON_SERVER="https://admin.$base" HOUSTON_API_TOKEN="$token"

echo "== 200 MiB in $app's data volume, backed up"
web=$(vm sh -c "docker ps -q --filter label=service=$app --filter label=role=web | head -n1")
vm docker exec "$web" sh -c 'head -c 209715200 /dev/urandom > /data/big.bin' && ok "big.bin" || bad "big.bin"
out=$(houston backup --project "$app" --follow 2>&1)
printf '%s' "$out" | grep -q "^GO: $app backed up" && ok "houston backup --follow through Cloudflare: GO" || { bad "backup: $out"; exit 1; }
id=$(houston snapshots --project "$app" | head -n1 | awk '{print $NF}')

echo "== houston snapshots download, through Cloudflare"
start=$SECONDS
out=$(houston snapshots download "$id" --project "$app" -o "$tmp_home/tunnel.zip" 2>&1); code=$?
[ "$code" = 0 ] && ok "$out (in $((SECONDS - start))s)" || bad "download: exit $code: $out"
unzip -tq "$tmp_home/tunnel.zip" >/dev/null 2>&1 && ok "unzip -t on this Mac: whole" || bad "unzip -t"
listing=$(unzip -Z1 "$tmp_home/tunnel.zip")
grep -qx 'data/data/big.bin' <<<"$listing" && grep -qx 'out/houston.json' <<<"$listing" && grep -q '^out/postgres/db/.*\.dump$' <<<"$listing" &&
  ok "it holds the volume's files, houston.json and the Postgres dump" || bad "listing: $(head -20 <<<"$listing")"
rm -f "$tmp_home/tunnel.zip"

echo "== broken mid-way, seen through Cloudflare"
url="https://admin.$base/api/v1/projects/$app/snapshots/$id/download"
curl -s -o "$tmp_home/broken.zip" --limit-rate 2M -H "Authorization: Bearer $token" "$url" &
dl=$!
for _ in $(seq 1 60); do vm docker ps -q --filter 'name=^houston-export$' | grep -q . && break; sleep 0.5; done
sleep 3
vm docker kill houston-export >/dev/null
wait $dl; code=$?
[ "$code" != 0 ] && ok "curl through Cloudflare exits $code: a failed transfer, not a finished one" || bad "through Cloudflare, a broken download ended cleanly (curl 0)"
unzip -tq "$tmp_home/broken.zip" >/dev/null 2>&1 && bad "the broken zip opens" || ok "what arrived isn't a zip that opens"

exit "$((failures > 0))"

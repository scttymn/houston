#!/usr/bin/env bash
# Read-only preflight and cleanup for the real Cloudflare check, using
# mission_control/.houston/cloudflare-check.env (CLOUDFLARE_API_TOKEN,
# BASE_DOMAIN). The token is only ever read from that file into a private
# header file for curl; it's never printed.
#
#   install/test/cloudflare.sh preflight   # what's already on the domain (changes nothing)
#   install/test/cloudflare.sh cleanup     # delete Houston's test tunnel and the records it manages
set -euo pipefail

repo="$(cd "$(dirname "$0")/../.." && pwd)"
env_file="$repo/mission_control/.houston/cloudflare-check.env"
[ -f "$env_file" ] || { echo "no $env_file (CLOUDFLARE_API_TOKEN=…, BASE_DOMAIN=…)"; exit 1; }
base=$(sed -n 's/^BASE_DOMAIN=//p' "$env_file")
tunnel_name="houston-${base%%.*}"
api="https://api.cloudflare.com/client/v4"

auth=$(mktemp)
chmod 600 "$auth"
trap 'rm -f "$auth"' EXIT
printf 'Authorization: Bearer %s\n' "$(sed -n 's/^CLOUDFLARE_API_TOKEN=//p' "$env_file" | tr -d '\r\n')" >"$auth"

cf() { curl -sS -H @"$auth" -H "Content-Type: application/json" "$@"; }
jqr() { python3 -c "import json,sys; d=json.load(sys.stdin); $1"; }

accounts=$(cf "$api/accounts?per_page=50")
rejected=$(printf '%s' "$accounts" | jqr 'e=d.get("errors") or []; print("; ".join(x.get("message","") for x in e) if not d.get("success") else "")')
if [ -n "$rejected" ]; then
  echo "Cloudflare rejected the token: $rejected"
  echo "(nothing else checked; fix the token first — repeated rejected calls get this machine temporarily blocked)"
  exit 1
fi
account=$(printf '%s' "$accounts" | jqr 'r=d.get("result") or []; print(r[0]["id"] if len(r)==1 else "")')
zone=$(cf "$api/zones?name=$base" | jqr 'r=d.get("result") or []; print(r[0]["id"] if r else "")')

case "${1:-}" in
  preflight)
    echo "accounts the token sees: $(printf '%s' "$accounts" | jqr 'print(len(d.get("result") or []), "" if d.get("success") else d.get("errors"))')"
    [ -n "$zone" ] && echo "zone $base: found" || echo "zone $base: NOT found (check the token's Zone · DNS permission)"
    if [ -n "$zone" ]; then
      for name in "*.$base" "admin.$base" "hooks.$base"; do
        cf "$api/zones/$zone/dns_records?name=$name" | jqr "
r=d.get('result') or []
print('$name:', 'no record' if not r else '; '.join(f\"{x['type']} -> {x['content']} (proxied={x.get('proxied')}, comment={x.get('comment')!r})\" for x in r))"
      done
    fi
    if [ -n "$account" ]; then
      cf "$api/accounts/$account/cfd_tunnel?name=$tunnel_name&is_deleted=false" | jqr "
r=d.get('result') or []
print('tunnel $tunnel_name:', 'none' if not r else ', '.join(f\"{x['id']} ({x.get('status')})\" for x in r))"
    fi
    ;;
  cleanup)
    [ -n "$account" ] && [ -n "$zone" ] || { echo "can't see exactly one account and the zone; nothing done"; exit 1; }
    tunnels=$(cf "$api/accounts/$account/cfd_tunnel?name=$tunnel_name&is_deleted=false" | jqr 'print(" ".join(x["id"] for x in d.get("result") or []))')
    for id in $tunnels; do
      for name in "*.$base" "admin.$base" "hooks.$base"; do
        records=$(cf "$api/zones/$zone/dns_records?name=$name" | TUNNEL_ID="$id" jqr '
import os
for x in d.get("result") or []:
    if (x.get("comment") or "").startswith("managed-by:houston") and x["content"] == os.environ["TUNNEL_ID"] + ".cfargotunnel.com":
        print(x["id"])')
        for record in $records; do
          cf -X DELETE "$api/zones/$zone/dns_records/$record" >/dev/null && echo "deleted $name (managed-by:houston, pointing at $tunnel_name)"
        done
      done
      cf -X DELETE "$api/accounts/$account/cfd_tunnel/$id/connections" >/dev/null || true
      cf -X DELETE "$api/accounts/$account/cfd_tunnel/$id" | jqr 'print("deleted the tunnel" if d.get("success") else "tunnel delete failed: %s" % d.get("errors"))'
    done
    [ -n "$tunnels" ] || echo "no $tunnel_name tunnel; nothing to clean up"
    ;;
  *) echo "usage: $0 preflight|cleanup"; exit 2 ;;
esac

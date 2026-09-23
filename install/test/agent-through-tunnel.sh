#!/usr/bin/env bash
# Build step 4b's real run, a stage of install/test/orbstack.sh: an agent on
# this Mac, with only HOUSTON_SERVER and HOUSTON_API_TOKEN, links a Forgejo
# repo, sets secrets, deploys and follows it, and reads status and logs, all
# through https://admin.<base> (Cloudflare). No browser, no rails runner.
#
#   install/test/agent-through-tunnel.sh <machine> <base domain>
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

machine="$1" base="$2"
app="houston-agent-test"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$machine" -u root "$@"; }
code_of() { curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$1"; }
# shellcheck source=install/test/settle.sh
. "$repo/install/test/settle.sh"

got=$(code_of "https://$app.$base/")
[ "$got" = 404 ] || { bad "https://$app.$base/ answers $got before the test (wanted the other server's 404); not deploying over it"; exit 1; }

echo "== the Mac's houston, and a token the admin would make in Settings › API tokens"
arch=$(uname -m); [ "$arch" = x86_64 ] && arch=amd64
(cd "$repo" && docker compose --progress quiet run --rm -e GOOS=darwin -e GOARCH="$arch" cli go build -o .houston/houston-mac ./cmd/houston) || { bad "building the Mac CLI"; exit 1; }
# The agent works from its own directory: the repo's compose.yml is Houston's
# toolchain, not an app, and a compose file that doesn't load is exit 2.
houston() { (cd "$tmp_home" && HOME="$tmp_home" "$repo/.houston/houston-mac" "$@"); }
tmp_home=$(mktemp -d)
trap 'rm -rf "$tmp_home"' EXIT
token=$(vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner 'puts ApiToken.issue!("agent").first' 2>/dev/null | tail -1)
[ -n "$token" ] || { bad "making an API token"; exit 1; }
export HOUSTON_SERVER="https://admin.$base" HOUSTON_API_TOKEN="$token"
[ "$(houston status 2>/dev/null | grep -c .)" -ge 1 ] && ok "the agent reaches Mission Control through Cloudflare with its token" || bad "houston status"

echo "== a Forgejo repo in the machine (its own Forgejo, forgejo-agent)"
pw="fj-$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
vm docker run -d --name forgejo-agent --network houston_default -p 127.0.0.1:3002:3000 \
  -e FORGEJO__security__INSTALL_LOCK=true -e FORGEJO__server__SSH_DOMAIN=forgejo-agent -e FORGEJO__server__SSH_PORT=22 \
  codeberg.org/forgejo/forgejo:13 >/dev/null
for _ in $(seq 1 60); do vm curl -fs -o /dev/null http://127.0.0.1:3002/api/v1/version && break; sleep 2; done
vm docker exec -u git forgejo-agent forgejo admin user create --username houston --password "$pw" --email houston@test.invalid --admin --must-change-password=false >/dev/null
fj() { vm curl -s -u "houston:$pw" -H 'Content-Type: application/json' "http://127.0.0.1:3002/api/v1$1" "${@:2}"; }
fj /user/repos -X POST -d "{\"name\":\"$app\",\"private\":true,\"default_branch\":\"main\"}" >/dev/null
gitc='git -c user.name=t -c user.email=t@test.invalid'
vm sh -c "rm -rf /tmp/$app && cp -r '$repo/internal/kamal/testdata/spike' /tmp/$app && cd /tmp/$app &&
  sed -i -e 's/^name: spike\$/name: $app/' -e '/^  domains:/d' compose.yml &&
  printf '  commands: { test: \"test -x /entry.sh\" }\n' >> compose.yml &&
  git init -q -b main && git add -A && $gitc commit -qm first && git remote add origin 'http://houston:$pw@127.0.0.1:3002/houston/$app.git' && git push -q origin main"
push() { vm sh -c "cd /tmp/$app && $1 && git add -A && $gitc commit -qm '$2' && git push -q origin main"; }

echo "== houston link"
out=$(houston link "git@forgejo-agent:houston/$app.git" 2>&1); code=$?
key=$(printf '%s\n' "$out" | grep -o 'ssh-ed25519 [A-Za-z0-9+/=]* houston@[a-z.]*' | head -1)
id=$(printf '%s\n' "$out" | sed -n 's/.*houston link --continue \([0-9]*\).*/\1/p' | head -1)
[ "$code" = 1 ] && [ -n "$key" ] && [ -n "$id" ] && ok "link: the deploy key, and NO-GO until it's added (continue $id)" || bad "link: exit $code: $out"
fj "/repos/houston/$app/keys" -X POST -d "{\"title\":\"houston\",\"key\":\"$key\",\"read_only\":true}" >/dev/null
out=$(houston link --continue "$id" 2>&1); code=$?
secret=$(printf '%s\n' "$out" | sed -n 's/^Webhook secret *//p')
[ "$code" = 0 ] && printf '%s' "$out" | grep -q "Linked $app" && [ -n "$secret" ] && ok "link --continue: linked, with the webhook URL and secret" || bad "link --continue: exit $code: $out"
# No webhook here (push-through-tunnel.sh proves those): every deploy below is the agent's.
[ "$(houston webhook --project "$app" | sed -n 's/^Webhook secret *//p')" = "$secret" ] && ok "houston webhook: the same URL and secret, until the first push" || bad "houston webhook"

echo "== houston secrets"
out=$(houston deploy --server --project "$app" --follow 2>&1); code=$?
[ "$code" = 1 ] && printf '%s' "$out" | grep -q 'HOLD: .*houston secrets set' && ok "deploy before the secrets: NO-GO, HOLD, with the command to fix it" || bad "deploy before secrets: exit $code: $(printf '%s' "$out" | tail -2)"
printf 'set by an agent, $HOME stays literal\n' | houston secrets set HOSTILE --project "$app" >/dev/null && ok "secrets set HOSTILE (from stdin)" || bad "secrets set"
houston secrets generate POSTGRES_PASSWORD --project "$app" >/dev/null && ok "secrets generate POSTGRES_PASSWORD" || bad "secrets generate"
houston secrets list --project "$app" | grep 'HOSTILE .*required *set' >/dev/null && ok "secrets list shows them set" || bad "secrets list: $(houston secrets list --project "$app")"

echo "== houston deploy --server --follow"
out=$(houston deploy --server --project "$app" --follow 2>&1); code=$?
[ "$code" = 0 ] && printf '%s' "$out" | grep -q '^GO:' && ok "deploy --follow: GO (exit 0)" || bad "deploy: exit $code: $(printf '%s' "$out" | tail -3)"
printf '%s' "$out" | grep -qE "$app-test-[0-9a-f]{8}" && ok "the followed log includes step 00's tests" || bad "no tests in the followed log"
settle 200 "https://$app.$base/up"
[ "$(curl -s --max-time 10 "https://$app.$base/env/HOSTILE")" = 'set by an agent, $HOME stays literal' ] && ok "https://$app.$base serves the secret byte-exact" || bad "the app through Cloudflare"
houston status --project "$app" | grep "^$app  GO" >/dev/null && ok "status: GO" || bad "status: $(houston status --project "$app")"
json=$(houston status --project "$app" --json)
printf '%s' "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["status"]=="go" and all(s["set"] for s in d["secrets"])' &&
  ! printf '%s' "$json" | grep -q 'set by an agent' && ok "status --json: go, every secret set, no values" || bad "status --json: $json"
houston logs --server --project "$app" --tail 20 | grep 'spike ready' >/dev/null && ok "logs --server: the app's output" || bad "logs --server"

echo "== houston console --server, over SSH on the LAN"
# The agent's own key, authorized for houston@ as the login hint says; an ssh
# on PATH that uses it (and a throwaway known_hosts), and script(1) for the tty.
ssh-keygen -q -t ed25519 -N '' -f "$tmp_home/id" && vm sh -c 'cat >> ~houston/.ssh/authorized_keys' < "$tmp_home/id.pub"
mkdir -p "$tmp_home/bin" && printf '#!/bin/sh\nexec /usr/bin/ssh -F /dev/null -i "%s/id" -o IdentitiesOnly=yes -o UserKnownHostsFile="%s/known_hosts" -o StrictHostKeyChecking=accept-new "$@"\n' "$tmp_home" "$tmp_home" > "$tmp_home/bin/ssh" && chmod +x "$tmp_home/bin/ssh"
cp -r "$repo/internal/kamal/testdata/spike" "$tmp_home/app" && sed -e "s/^name: spike\$/name: $app/" -e '/^  domains:/d' "$repo/internal/kamal/testdata/spike/compose.yml" > "$tmp_home/app/compose.yml" &&
  printf '  commands: { console: "echo console sees $$HOSTILE" }\n' >> "$tmp_home/app/compose.yml" # the agent's checkout
out=$(cd "$tmp_home/app" && PATH="$tmp_home/bin:$PATH" HOUSTON_SSH="houston@$machine.orb.local" script -q /dev/null "$repo/.houston/houston-mac" console --server </dev/null 2>&1 | tr -d '\r')
printf '%s' "$out" | grep -q 'console sees set by an agent, \$HOME stays literal' && ok "console --server ran the console command in the running app container" || bad "console --server: $out"

echo "== a push whose tests fail"
push "sed -i 's|test -x /entry.sh|exit 3|' compose.yml" 'failing tests' >/dev/null
out=$(houston deploy --server --project "$app" --follow 2>&1); code=$?
[ "$code" = 1 ] && printf '%s' "$out" | grep -q 'tests failed (exit 3)' && ok "deploy --follow: NO-GO, tests failed (exit 3), exit 1" || bad "failing push: exit $code: $(printf '%s' "$out" | tail -2)"
codes=$(for _ in 1 2 3 4 5 6 7 8 9 10; do code_of "https://$app.$base/up"; printf ' '; sleep 1; done)
direct=$(vm docker run --rm --network kamal curlimages/curl:8.16.0 -s -o /dev/null -w '%{http_code}' -H "Host: $app.$base" http://kamal-proxy/up)
[ "$codes" = "$(printf '200 %.0s' 1 2 3 4 5 6 7 8 9 10)" ] && ok "the previous version still serves (10 of 10 through Cloudflare)" ||
  bad "the previous version: through Cloudflare $codes; straight at kamal-proxy $direct"
houston deploys --project "$app" | head -3 | sed 's/^/    /'

exit "$((failures > 0))"

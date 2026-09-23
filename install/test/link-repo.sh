#!/usr/bin/env bash
# Build step 4, Batch 1: linking a repo from Mission Control's container on a
# fresh Houston server (OrbStack machine). A Forgejo container on Mission
# Control's network is the git host; the Add project pages are driven over
# HTTP as the admin.
#
#   install/test/link-repo.sh          # KEEP=1 keeps the machine
# shellcheck disable=SC2016,SC2015 # single-quoted code runs in the machine; ok/bad return 0
set -uo pipefail

name="houston-link-repo"
repo="$(cd "$(dirname "$0")/../.." && pwd)"
forgejo_image="codeberg.org/forgejo/forgejo:13"
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
vm() { orb -m "$name" -u root "$@"; }
rails() { vm docker compose -f /opt/houston/compose.yml exec -T mission-control bin/rails runner "$1"; }

cleanup() { if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi; }
trap cleanup EXIT

echo "== creating $name and installing Houston"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null
vm env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-link-install.log 2>&1 || { tail -20 /tmp/houston-link-install.log; exit 1; }
code=$(sed -n 's/^Setup code *//p' /tmp/houston-link-install.log)
ip=$(vm hostname -I | awk '{print $1}')

echo "== the admin, and setup marked finished (Cloudflare and storage aren't what this checks)"
jar=/tmp/houston-link-jar
admin_password="test-$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
token() { vm curl -s -c "$jar" -b "$jar" "http://$ip:3000$1" | grep -o 'name="authenticity_token" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"$//'; }
vm curl -s -o /dev/null -c "$jar" -b "$jar" -X POST "http://$ip:3000/setup" --data-urlencode "authenticity_token=$(token /setup)" \
  --data-urlencode "setup[code]=$code" --data-urlencode "setup[email_address]=admin@example.com" \
  --data-urlencode "setup[password]=$admin_password" --data-urlencode "setup[password_confirmation]=$admin_password"
rails 'Installation.current.update!(base_domain: "houston.test", cloudflare_connected_at: Time.current, dns_mode: "wildcard")
  StorageLocation.create!(name: "test", kind: "local", settings: { "path" => "/tmp" }, restic_password: "x", default: true, verified_at: Time.current, acknowledged_at: Time.current)' >/dev/null
[ "$(vm curl -s -o /dev/null -w '%{http_code}' -b "$jar" "http://$ip:3000/link")" = 200 ] && ok "signed in; /link answers" || bad "sign-in"

echo "== Forgejo on Mission Control's network, with a private repo"
forgejo_pw="fj-$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
forgejo_up() {
  vm docker run -d --name forgejo --network houston_default -p 127.0.0.1:3001:3000 \
    -e FORGEJO__security__INSTALL_LOCK=true -e FORGEJO__server__SSH_DOMAIN=forgejo -e FORGEJO__server__SSH_PORT=22 \
    "$forgejo_image" >/dev/null
  for _ in $(seq 1 60); do vm curl -fs -o /dev/null http://127.0.0.1:3001/api/v1/version && return 0; sleep 2; done
  return 1
}
forgejo_up || { bad "Forgejo didn't start"; exit 1; }
vm docker exec -u git forgejo forgejo admin user create --username houston --password "$forgejo_pw" --email houston@test.invalid --admin --must-change-password=false >/dev/null
fj() { vm curl -s -u "houston:$forgejo_pw" -H 'Content-Type: application/json' "http://127.0.0.1:3001/api/v1$1" "${@:2}"; }
fj /user/repos -X POST -d '{"name":"spike","private":true,"default_branch":"main"}' >/dev/null
vm sh -c "rm -rf /tmp/spike && cp -r '$repo/internal/kamal/testdata/spike' /tmp/spike && cd /tmp/spike && git init -q -b main && git add -A &&
  git -c user.name=t -c user.email=t@test.invalid commit -qm spike && git push -q 'http://houston:$forgejo_pw@127.0.0.1:3001/houston/spike.git' main" \
  && ok "pushed the spike fixture to a private Forgejo repo" || bad "push to Forgejo"

# Rails' CSRF tokens are per form: take the one from the form that posts to $1.
form_token() {
  vm curl -s -c "$jar" -b "$jar" "http://$ip:3000/link" |
    grep -o "action=\"$1\"[^>]*><input type=\"hidden\" name=\"authenticity_token\" value=\"[^\"]*\"" | head -1 | sed 's/.*value="//;s/"$//'
}
link() { # POST to a /link action with form fields; prints the page
  local path="$1"; shift
  local args=(--data-urlencode "authenticity_token=$(form_token "$path")")
  for field in "$@"; do args+=(--data-urlencode "$field"); done
  vm curl -s -c "$jar" -b "$jar" -X POST "http://$ip:3000$path" "${args[@]}"
}
url="git@forgejo:houston/spike.git"

echo "== Add project, over HTTP"
page=$(link /link/access "repo_url=$url")
printf '%s' "$page" | grep -q 'NO-GO' && printf '%s' "$page" | grep -q 'Permission denied' && ok "before the deploy key is added: NO-GO, Permission denied" || bad "access before the key: $(printf '%s' "$page" | grep -o 'check__state[^<]*<[^<]*<[^<]*' | head -1)"
key=$(printf '%s' "$page" | grep -o 'ssh-ed25519 [A-Za-z0-9+/=]* houston@houston.test' | head -1)
[ -n "$key" ] && ok "the page shows the generated deploy key" || bad "no deploy key on the page"
fj /repos/houston/spike/keys -X POST -d "{\"title\":\"houston\",\"key\":\"$key\",\"read_only\":true}" >/dev/null
page=$(link /link/access "repo_url=$url")
printf '%s' "$page" | grep -q 'Houston can read the repo' && ok "with the deploy key: GO, Houston can read the repo" || bad "access with the key"
vm docker compose -f /opt/houston/compose.yml exec -T mission-control ssh-keygen -F forgejo -f storage/known_hosts >/dev/null && ok "Forgejo's host key was recorded on first use (storage/known_hosts)" || bad "known_hosts"
page=$(link /link/read "branch=main" "compose_path=compose.yml")
printf '%s' "$page" | grep -q 'What Houston found' && printf '%s' "$page" | grep -q 'spike → spike.houston.test' && printf '%s' "$page" | grep -q 'db · postgres:17' \
  && ok "read compose.yml with houston inspect: spike → spike.houston.test, db · postgres:17" || bad "read: $(printf '%s' "$page" | grep -A2 'problems' | head -3)"
saved=$(vm curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -c "$jar" -b "$jar" -X POST "http://$ip:3000/link" --data-urlencode "authenticity_token=$(form_token /link)")
[ "$saved" = "302 http://$ip:3000/projects/spike" ] && ok "Save links the project" || bad "save: $saved"
linked=$(rails 'p = Project.find_by!(name: "spike"); puts [p.repo_url, p.branch, p.compose_path, p.hosts.pluck(:name).sort.join(",")].join(" ")')
[ "$linked" = "$url main compose.yml spike,spike-db" ] && ok "the project: $linked" || bad "the project: $linked"

echo "== a public repo over HTTPS"
page=$(link /link/access "repo_url=https://github.com/basecamp/kamal.git")
printf '%s' "$page" | grep -q 'Houston can read the repo' && ok "https://github.com/basecamp/kamal.git: GO" || bad "public HTTPS repo"

echo "== the git host's key changes"
vm docker rm -f forgejo >/dev/null && forgejo_up || bad "Forgejo restart"
page=$(link /link/access "repo_url=$url")
printf '%s' "$page" | grep -q 'host key changed' && ok "a new host key is refused: NO-GO, host key changed" || bad "changed host key: $(printf '%s' "$page" | grep -o 'check__state[^<]*<[^<]*<[^<]*' | head -1)"

echo
if [ "$failures" -eq 0 ]; then echo "LINK PASS"; else echo "LINK: $failures failure(s)"; exit 1; fi

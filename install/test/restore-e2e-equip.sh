# Sourced by restore-e2e.sh when EQUIP_SOURCE is set (a Houston-ready copy
# of equip, never your repo): a Rails app whose data is SQLite in a volume,
# deployed by hand (a hand deploy runs no tests), linked to a Forgejo repo so
# a restore can fetch its commit, then restored at the very commit that
# serves (Kamal renames the running container of the same version).
# shellcheck shell=bash disable=SC2016,SC2015,SC2154 # uses restore-e2e.sh's helpers and variables

echo "== equip (a copy): SQLite restored at the serving commit"
mkdir -p "$repo/.houston"
stage="$(mktemp -d "$repo/.houston/e2e.XXXXXX")" # .houston ignores itself; the machine sees /Users
tar -C "$EQUIP_SOURCE" --exclude=./.houston --exclude=./.kamal --exclude=./.git --exclude=./.claude --exclude=./tmp --exclude=./log \
  --exclude=./storage --exclude=./node_modules --exclude=./.env --exclude=./config/master.key -cf - . | tar -C "$stage" -xf -
for keep in storage/.keep tmp/.keep log/.keep; do
  if [ -f "$EQUIP_SOURCE/$keep" ]; then mkdir -p "$stage/$(dirname "$keep")" && cp "$EQUIP_SOURCE/$keep" "$stage/$keep"; fi
done
fj /user/repos -X POST -d '{"name":"houston-equip-test","private":true,"default_branch":"main"}' >/dev/null
orb -m "$name" -u houston bash -lc "rm -rf ~/equip && cp -r '$stage' ~/equip && cd ~/equip &&
  sed -i -e 's/^name: equip\$/name: houston-equip-test/' -e 's/\${RAILS_MASTER_KEY:-}/\${RAILS_MASTER_KEY}/' compose.yml &&
  git init -q -b main && git add -A && $gitc commit -qm equip &&
  git remote add origin 'http://houston:$pw@127.0.0.1:3001/houston/houston-equip-test.git' && git push -q origin main"
rm -rf "$stage"
ekey=$(rails 'puts RepoLink.start!("git@forgejo:houston/houston-equip-test.git").deploy_key_public')
fj /repos/houston/houston-equip-test/keys -X POST -d "{\"title\":\"houston\",\"key\":\"$ekey\",\"read_only\":true}" >/dev/null
linked=$(rails '
  link = RepoLink.last
  read = GitRemote.read(link)
  raise "read: #{read.problems}" unless read.ok
  link.update!(preview: read.inspection, preview_sha: read.sha)
  ProjectLinking.new(link).save!
  puts "linked"')
[ "$linked" = linked ] && ok "houston-equip-test linked" || bad "equip linking: $linked"
# The key goes from a private temp dir straight into houston secrets set's
# stdin, as root in the machine (it can read a file only this Mac user can).
keydir=$(mktemp -d "$repo/.houston/key.XXXXXX")
(umask 077 && cp "$EQUIP_SOURCE/config/master.key" "$keydir/master.key")
vm env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" sh -c \
  "houston secrets set RAILS_MASTER_KEY --project houston-equip-test < '$keydir/master.key'" >/dev/null && ok "RAILS_MASTER_KEY set from stdin" || bad "RAILS_MASTER_KEY"
rm -rf "$keydir"; keydir=""
orb -m "$name" -u houston bash -lc 'cd ~/equip && houston deploy' >/tmp/restore-e2e-equip.log 2>&1 && ok "equip deployed by hand: GO" || { bad "equip deploy"; tail -20 /tmp/restore-e2e-equip.log; }

eweb() { vm sh -c 'docker ps -q --filter label=service=houston-equip-test --filter label=role=web | head -n1'; }
probe() { vm docker exec "$(eweb)" bin/rails runner "$1" 2>/dev/null | tail -1; }
probe 'c = ActiveRecord::Base.connection; c.execute("create table restore_probe (v text)"); c.execute("insert into restore_probe values (#{c.quote("one")})"); puts :ok' >/dev/null
[ "$(probe 'puts ActiveRecord::Base.connection.select_value("select v from restore_probe")')" = one ] && ok "equip's SQLite says 'one'" || bad "equip probe one"
out=$(orb -m "$name" -u houston env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" bash -lc "houston backup --follow --project houston-equip-test" 2>&1)
printf '%s' "$out" | grep -q '^GO: houston-equip-test backed up' && ok "equip backed up" || bad "equip backup: $out"
e1=$(orb -m "$name" -u houston env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" bash -lc "houston snapshots --project houston-equip-test" | awk '/Back up now/ {print $NF; exit}')
probe 'c = ActiveRecord::Base.connection; c.execute("update restore_probe set v = #{c.quote("two")}"); puts :ok' >/dev/null
[ "$(probe 'puts ActiveRecord::Base.connection.select_value("select v from restore_probe")')" = two ] && ok "then 'two'" || bad "equip probe two"

vm docker rm -f epoller >/dev/null 2>&1
vm docker run -d --name epoller --network kamal --entrypoint sh curlimages/curl:8.16.0 -c \
  'while true; do echo "$(curl -s --max-time 5 -o /dev/null -w "%{http_code}" -H "Host: houston-equip-test.houston.test" http://kamal-proxy/up)"; sleep 0.2; done' >/dev/null
out=$(orb -m "$name" -u houston env HOUSTON_SERVER=http://127.0.0.1:3000 HOUSTON_API_TOKEN="$token" bash -lc "houston restore $e1 --confirm houston-equip-test --project houston-equip-test --follow" 2>&1); code=$?
lines=$(vm docker logs epoller 2>&1); vm docker rm -f epoller >/dev/null
[ "$code" = 0 ] && ok "equip restored to $e1: GO" || bad "equip restore: exit $code: $(printf '%s' "$out" | tail -15)"
failed=$(printf '%s\n' "$lines" | grep -vc '^200$')
[ "$failed" = 0 ] && ok "all $(printf '%s\n' "$lines" | wc -l | tr -d ' ') requests to equip's /up during it answered 200" || bad "$failed requests failed: $(printf '%s\n' "$lines" | sort | uniq -c)"
[ "$(probe 'puts ActiveRecord::Base.connection.select_value("select v from restore_probe")')" = one ] && ok "equip's SQLite says 'one' again" || bad "equip after restore: $(probe 'puts ActiveRecord::Base.connection.select_value("select v from restore_probe")')"
vm docker volume inspect houston-equip-test_storage >/dev/null 2>&1 && bad "generation 1's storage volume is still there" || ok "generation 1's storage volume is gone"

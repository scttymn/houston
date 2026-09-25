#!/usr/bin/env bash
# houston's SSH key in authorized_keys (docs/plans/security-fixes.md, L6), in
# a Debian container: install.sh loaded as a library.
#   - it may log in only from this server (Kamal connects to 127.0.0.1),
#     without agent or X11 forwarding
#   - an install from before gets its plain line replaced, once
#   - other keys in the file are kept
#
#   install/test/install-ssh-key.sh
set -uo pipefail

repo="$(cd "$(dirname "$0")/../.." && pwd)"
exec docker run --rm -i -v "$repo/install:/install:ro" debian:bookworm-slim bash -s <<'IN_CONTAINER'
set -uo pipefail
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
useradd --create-home houston
home=/home/houston
mkdir -p $home/.ssh
echo "ssh-ed25519 AAAAhouston houston@server" > $home/.ssh/id_ed25519.pub
entry='from="127.0.0.1,::1",no-agent-forwarding,no-X11-forwarding ssh-ed25519 AAAAhouston houston@server'
lib() { HOUSTON_INSTALL_LIB=1 sh -c '. /install/install.sh; authorize_key /home/houston'; }

echo "== an install from before: a plain line, and someone else's key"
printf 'ssh-ed25519 AAAAother someone@laptop\nssh-ed25519 AAAAhouston houston@server\n' > $home/.ssh/authorized_keys
lib
[ "$(grep -c AAAAhouston $home/.ssh/authorized_keys)" = 1 ] && grep -qxF "$entry" $home/.ssh/authorized_keys &&
  ok "houston's key only from this server, once" || bad "authorized_keys:
$(cat $home/.ssh/authorized_keys)"
grep -qx "ssh-ed25519 AAAAother someone@laptop" $home/.ssh/authorized_keys && ok "the other key is kept" || bad "the other key is gone"
[ "$(stat -c '%a %U' $home/.ssh/authorized_keys)" = "600 houston" ] && ok "600, houston's" || bad "mode: $(stat -c '%a %U' $home/.ssh/authorized_keys)"
lib; lib
[ "$(wc -l < $home/.ssh/authorized_keys)" = 2 ] && ok "rerunning changes nothing" || bad "after reruns:
$(cat $home/.ssh/authorized_keys)"

echo "== a fresh install"
rm $home/.ssh/authorized_keys; lib
[ "$(cat $home/.ssh/authorized_keys)" = "$entry" ] && ok "just houston's key, restricted" || bad "$(cat $home/.ssh/authorized_keys)"

echo
if [ "$failures" -eq 0 ]; then echo "INSTALL SSH KEY PASS"; else echo "INSTALL SSH KEY: $failures failure(s)"; exit 1; fi
IN_CONTAINER

#!/usr/bin/env bash
# The tunnel's CPU (docs/plans/tunnel-cpus.md), in a Debian container:
# install.sh loaded as a library (HOUSTON_INSTALL_LIB=1), docker stubbed.
#   - compose.yml gives cloudflared a quarter of the server's cores, at
#     least one: a flood through it can't take the machine from every
#     other app
#   - nothing else in compose.yml gets a limit
#
#   install/test/install-tunnel-cpus.sh
set -uo pipefail

repo="$(cd "$(dirname "$0")/../.." && pwd)"
exec docker run --rm -i -v "$repo/install:/install:ro" debian:bookworm-slim bash -s <<'IN_CONTAINER'
set -uo pipefail
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }

mkdir -p /stub && for c in docker; do printf '#!/bin/sh\nexit 0\n' > /stub/$c; chmod 755 /stub/$c; done
cat > /stub/getent <<'SH' && chmod 755 /stub/getent
#!/bin/sh
case "$1" in group) echo "docker:x:999:" ;; passwd) echo "houston:x:1001:1001::/home/houston:/bin/sh" ;; esac
SH
printf '#!/bin/sh\necho 1001\n' > /stub/id && chmod 755 /stub/id
# nproc: the server's cores, $CORES of them.
printf '#!/bin/sh\necho "$CORES"\n' > /stub/nproc && chmod 755 /stub/nproc
export PATH="/stub:$PATH"
mkdir -p /opt/houston

# cpus <service>: the cpus compose.yml gives it ("" for none).
cpus() { sed -n "/^  $1:/,/^  [a-z]/p" /opt/houston/compose.yml | sed -n 's/^    cpus: *"\{0,1\}\([0-9.]*\)"\{0,1\}$/\1/p'; }

for case in "1 1" "2 1" "4 1" "8 2" "16 4" "48 12"; do
  set -- $case
  out=$(env CORES=$1 HOUSTON_INSTALL_LIB=1 HOUSTON_DIR=/opt/houston sh -c '. /install/install.sh; write_compose' 2>&1) || { bad "$1 cores: write_compose failed: $out"; continue; }
  got=$(cpus cloudflared)
  [ "$got" = "$2" ] && ok "$1 cores: the tunnel gets $2" || bad "$1 cores: the tunnel gets '$got', want $2"
done
for svc in mission-control registry; do
  [ -z "$(cpus $svc)" ] && ok "$svc: no limit" || bad "$svc has a cpus limit: $(cpus $svc)"
done

echo
if [ "$failures" -eq 0 ]; then echo "INSTALL TUNNEL CPUS PASS"; else echo "INSTALL TUNNEL CPUS: $failures failure(s)"; exit 1; fi
IN_CONTAINER

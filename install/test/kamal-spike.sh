#!/usr/bin/env bash
# Build step 3's spike: deploys internal/kamal/testdata/spike.deploy.yml (the
# generator's output, pinned by TestConfigSpikeFixture) with Kamal on a fresh
# Houston server in an OrbStack machine, and answers spec §14's Kamal
# questions (S1–S10 in docs/plans/deploy-path.md).
#
#   install/test/kamal-spike.sh          # KEEP=1 keeps the machine
set -euo pipefail

name="houston-kamal-spike"
repo="$(cd "$(dirname "$0")/../.." && pwd)"

cleanup() { if [ "${KEEP:-}" = 1 ]; then echo "kept machine $name"; else orb delete -f "$name" >/dev/null 2>&1 || true; fi; }
trap cleanup EXIT

echo "== creating $name"
orb delete -f "$name" >/dev/null 2>&1 || true
orb create ubuntu:noble "$name" >/dev/null

echo "== installing Houston"
orb -m "$name" -u root env HOUSTON_SOURCE="$repo" sh "$repo/install/install.sh" >/tmp/houston-kamal-spike-install.log 2>&1 \
  || { tail -20 /tmp/houston-kamal-spike-install.log; exit 1; }

echo "== spike"
orb -m "$name" -u root bash "$repo/install/test/kamal-spike-inside.sh" "$repo"

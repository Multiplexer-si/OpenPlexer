#!/usr/bin/env bash
# Run the loopback gates Codex cannot, in a clean clone of the worktree.
# Usage: gates.sh <worktree> <name> [worker]
# Writes logs and $SCRATCH/coord-<name>.txt. Exits 0 only if every gate passed.
set -u
. "$(dirname "$0")/common.sh"
wt="$1" name="$2" worker="${3:-}"
d="$SCRATCH/clean-$name"
to_trash "$d"
git clone -q "$wt" "$d" || exit 1
head="$(git -C "$d" rev-parse HEAD)"
cd "$d"
make check > "$SCRATCH/check-$name.log" 2>&1; mc=$?
line="make check $mc"
fail=$mc
if [ "$worker" = worker ]; then
  cd apps/hub-cloudflare
  export WRANGLER_SEND_METRICS=false XDG_CONFIG_HOME="$PWD/.wrangler-home" npm_config_cache="$PWD/.npm-cache"
  npx -y npm@11 ci --silent > "$SCRATCH/npm-$name.log" 2>&1; ci=$?
  npm test >> "$SCRATCH/npm-$name.log" 2>&1; nt=$?
  scripts/conformance.sh > "$SCRATCH/conf-$name.log" 2>&1; cf=$?
  tests="$(grep -E '^ +Tests ' "$SCRATCH/npm-$name.log" | tail -1 | sed 's/^ *//')"
  line="$line; npm ci $ci; npm test $nt ($tests); scripts/conformance.sh $cf"
  fail=$((fail + ci + nt + cf))
fi
echo "Coordinator at $head in a clean clone: $line." | tee "$SCRATCH/coord-$name.txt"
if [ "$fail" != 0 ]; then
  grep -hE -- '--- FAIL|^FAIL|fatal error|panic:|FAIL  ' "$SCRATCH/check-$name.log" "$SCRATCH/npm-$name.log" "$SCRATCH/conf-$name.log" 2>/dev/null | head -20
  exit 1
fi

#!/usr/bin/env bash
# Print a verdict with local paths and personal details removed, ready to post publicly.
# Usage: sanitize.sh <verdict.md>   (extra literals: SANITIZE_EXTRA='a|b')
# Exits 1 if anything that looks personal survives.
set -u
. "$(dirname "$0")/common.sh"
f="$1"
user="$(id -un)"
out="$(sed -E \
  -e "s#${SCRATCH}/?#<scratch>/#g" \
  -e "s#/(private/)?(tmp|var)/[^ )\`\"']*#<tmp>#g" \
  -e "s#${HOME}/[A-Za-z0-9._-]*OpenPlexer[^ )\`\"']*#<worktree>#g" \
  -e "s#${HOME}#~#g" "$f")"
if [ -n "${SANITIZE_EXTRA:-}" ]; then
  IFS='|' read -r -a extra <<< "$SANITIZE_EXTRA"
  for s in "${extra[@]}"; do out="${out//$s/<redacted>}"; done
fi
printf '%s\n' "$out"
if printf '%s' "$out" | grep -qiE "$user|@[a-z0-9-]+\.(com|io|org)|workers\.dev"; then
  echo "sanitize: possible personal detail left in output; review before posting" >&2
  exit 1
fi

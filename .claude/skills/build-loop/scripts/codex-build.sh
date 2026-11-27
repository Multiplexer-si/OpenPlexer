#!/usr/bin/env bash
# Run a Codex builder in a worktree.
# Usage: codex-build.sh <worktree> <brief.md> <out-prefix>
# Writes <out-prefix>-last.md (final message) and <out-prefix>.log. Exits with Codex's status.
set -u
. "$(dirname "$0")/common.sh"
wt="$1" brief="$2" out="$3"
gitdir="$(git_common_dir "$wt")"
cd "$wt" || exit 1
codex exec -s workspace-write -c approval_policy="never" \
  -c "sandbox_workspace_write.writable_roots=[\"$gitdir\"]" \
  -o "$out-last.md" - < "$brief" > "$out.log" 2>&1
rc=$?
if grep -q 'flagged for possible cybersecurity risk' "$out.log"; then
  echo "codex-build: stopped by the provider's cyber-risk filter; do not reword the brief to evade it" >&2
  rc=3
fi
echo "codex-build $(basename "$wt") exit $rc"
exit $rc

#!/usr/bin/env bash
# Run the independent Codex verifier in its own detached worktree.
# Usage: codex-verify.sh <verify-worktree> <prompt.md> <out-prefix>
# The prompt names the verdict file; the verifier may write only there and in $SCRATCH.
set -u
. "$(dirname "$0")/common.sh"
wt="$1" prompt="$2" out="$3"
gitdir="$(git_common_dir "$wt")"
cd "$wt" || exit 1
codex exec -m "$CODEX_VERIFY_MODEL" -c model_reasoning_effort="high" -s workspace-write -c approval_policy="never" \
  -c "sandbox_workspace_write.writable_roots=[\"$gitdir\",\"$SCRATCH\"]" \
  -o "$out-last.md" - < "$prompt" > "$out.log" 2>&1
rc=$?
if grep -q 'flagged for possible cybersecurity risk' "$out.log"; then
  echo "codex-verify: stopped by the provider's cyber-risk filter; report it to the user" >&2
  rc=3
fi
echo "codex-verify $(basename "$wt") exit $rc"
exit $rc

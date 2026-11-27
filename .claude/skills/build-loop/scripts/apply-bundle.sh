#!/usr/bin/env bash
# Apply Codex's dist/pr.bundle to a branch in its worktree, then move dist/clone and the bundle to the Trash.
# Usage: apply-bundle.sh <worktree> <branch>
set -eu
. "$(dirname "$0")/common.sh"
wt="$1" branch="$2"
cd "$wt"
[ -f dist/pr.bundle ] || { echo "apply-bundle: no dist/pr.bundle in $wt (did Codex commit in the worktree, or stop early?)" >&2; exit 1; }
old="$(git rev-parse HEAD)"
git fetch -q -f dist/pr.bundle "${branch}:refs/tmp/build-loop"
git reset -q --hard refs/tmp/build-loop
git update-ref -d refs/tmp/build-loop
git log --oneline "${old}..HEAD"
to_trash dist/clone dist/pr.bundle
echo "apply-bundle: $branch at $(git rev-parse --short HEAD)"

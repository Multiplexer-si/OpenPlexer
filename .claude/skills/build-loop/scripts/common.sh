# Shared settings for the build-loop scripts. Source it; do not run it.
SCRATCH="${SCRATCH:-${TMPDIR:-/tmp}/openplexer-loop}"
GO_BIN="${GO_BIN:-$HOME/.cache/pre-commit/repo6zv8u7zh/golangenv-default/.go/bin}"
CODEX_VERIFY_MODEL="${CODEX_VERIFY_MODEL:-gpt-6.2-astra}"
mkdir -p "$SCRATCH"
export PATH="$GO_BIN:$PATH" GOTOOLCHAIN=local GOFLAGS=-mod=mod

# Main checkout's .git, which every worktree's git needs to write to.
git_common_dir() { (cd "$1" && cd "$(git rev-parse --git-common-dir)" && pwd); }

# Move paths to the Trash instead of deleting them.
to_trash() {
  for p in "$@"; do
    [ -e "$p" ] || continue
    if command -v trash >/dev/null 2>&1; then trash "$p"; else
      echo "no trash command; leaving $p" >&2; fi
  done
}

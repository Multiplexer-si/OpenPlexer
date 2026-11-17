#!/usr/bin/env bash
# Usage: verify.sh <ref> <journey>...   journeys: doctor build binaries gate check spec-ajv extract inventory all
# Evidence goes to $EVIDENCE (default ~/openplexer-verify-evidence/<ref>-<time>) and survives cleanup.
set -uo pipefail
REPO=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
REF=${1:?ref}; shift
REFERENCE=${REFERENCE:-$HOME/multiplexer}
GO_BIN=${GO_BIN:-$HOME/.cache/pre-commit/repo6zv8u7zh/golangenv-default/.go/bin}
export PATH="$GO_BIN:$PATH" GOTOOLCHAIN=local
STAMP=$(date +%Y%m%d-%H%M%S)
EVIDENCE=${EVIDENCE:-$HOME/openplexer-verify-evidence/$(echo "$REF" | tr / -)-$STAMP}
WT=$(mktemp -d "${TMPDIR:-/tmp}/openplexer-verify.XXXXXX")
mkdir -p "$EVIDENCE"
git -C "$REPO" fetch -q origin
git -C "$REPO" worktree add -q --detach "$WT" "$REF" || exit 2
trap 'git -C "$REPO" worktree remove --force "$WT"; echo "evidence: $EVIDENCE"' EXIT
cd "$WT"
git rev-parse HEAD > "$EVIDENCE/head.txt"
fail=0
run() { local name=$1; shift; "$@" >"$EVIDENCE/$name.log" 2>&1; local rc=$?; echo "$name exit=$rc" | tee -a "$EVIDENCE/summary.txt"; return $rc; }
expect() { local want=$1 name=$2; shift 2; run "$name" "$@"; local rc=$?; [ "$rc" = "$want" ] || { echo "  FAIL $name: want exit $want" | tee -a "$EVIDENCE/summary.txt"; fail=1; }; }
for j in "$@"; do
  [ "$j" = all ] && set -- doctor build binaries gate check spec-ajv extract inventory
done
for j in "$@"; do case $j in
  doctor) { go version; node --version; git -C "$REFERENCE" rev-parse --short HEAD 2>&1; grep -o '"7d80045a"' tools/extract/reference.ts; } > "$EVIDENCE/doctor.log" 2>&1; echo "doctor written" >> "$EVIDENCE/summary.txt";;
  build) expect 0 build make build;;
  binaries) for b in openplexer-hub openplexer-verify; do expect 1 "bin-$b" "bin/$b"; grep -q "^$b: not implemented yet$" "$EVIDENCE/bin-$b.log" || { echo "  FAIL bin-$b message" | tee -a "$EVIDENCE/summary.txt"; fail=1; }; done
    expect 2 bin-openplexer bin/openplexer; grep -q "^usage:$" "$EVIDENCE/bin-openplexer.log" || { echo "  FAIL bin-openplexer usage" | tee -a "$EVIDENCE/summary.txt"; fail=1; };;
  gate) expect 0 gate "$REPO/.claude/skills/verify/gate.sh" "$WT";;
  check) expect 0 make-check make check;;
  spec-ajv) expect 0 spec-ajv node "$REPO/.claude/skills/verify/spec-ajv.cjs" "$WT" "$REFERENCE/node_modules";;
  extract) expect 0 extract "$REFERENCE/node_modules/.bin/tsx" tools/extract/main.ts check --reference "$REFERENCE";;
  inventory) expect 0 inventory go run ./tools/checkinventory -reference "$REFERENCE";;
  *) echo "unknown journey $j"; fail=1;;
esac; done
exit $fail

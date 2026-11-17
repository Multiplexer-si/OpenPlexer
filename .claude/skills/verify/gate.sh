#!/usr/bin/env bash
# Usage: gate.sh <repo-root>   Drives bin/openplexer through the local policy gate. Needs `make build` first, and go and node on PATH.
set -uo pipefail
ROOT=$1
BIN=$ROOT/bin/openplexer
EX=$ROOT/spec/policy/v1/examples
FILES="$(dirname "$0")/gate-files.cjs"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/openplexer-gate.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
export OPENPLEXER_HOME=$TMP/home
fail=0

# step <exit> <pattern> <command>...: the command must exit with <exit> and print a line that matches <pattern>.
step() {
  local want=$1 pattern=$2; shift 2
  local out rc
  out=$("$@" 2>&1); rc=$?
  if [ "$rc" = "$want" ] && grep -Eq -- "$pattern" <<<"$out"; then
    echo "ok   exit=$rc  ${*#"$ROOT"/}"
  else
    echo "FAIL exit=$rc want=$want pattern=/$pattern/  $*"; echo "$out" | sed 's/^/     /'; fail=1
  fi
}

step 1 '^$' bash -c "cd '$ROOT' && go list -deps ./apps/openplexer | grep -Ex 'net|net/http'"

step 4 '^policy: absent$' "$BIN" policy status
step 4 '^no policy: ' "$BIN" policy check --repo api write src/orders/cart.ts

step 0 '^$' "$BIN" configure --keys "$EX/jwks/hub-keys.json" --member mem-ana --stale-after-days 1
step 0 '^policy: verified$' "$BIN" policy load "$EX/signed-bundle/message.json"
step 0 '^member: mem-ana, with the role backend$' "$BIN" policy status
step 0 '^allow: ' "$BIN" policy check --repo api write src/orders/cart.ts
step 5 '^deny: ' "$BIN" policy check --repo web write src/pages/cart.tsx
step 5 '^deny: ' "$BIN" policy check write src/orders/cart.ts
step 0 '^allow: ' "$BIN" policy check exec 'npm test -- --watch'

cp "$EX/signed-bundle/message.json" "$TMP/tampered.json"
node "$FILES" flip "$TMP/tampered.json" signature
step 1 'the bundle is rejected' "$BIN" policy load "$TMP/tampered.json"
step 1 'not available' "$BIN" policy load "$TMP/no-such-bundle.json"
step 0 '^policy: verified$' "$BIN" policy status

node "$FILES" age "$OPENPLEXER_HOME/policy.json" 25
step 3 '^policy: stale$' "$BIN" policy status
step 0 'the policy is stale' "$BIN" policy check --repo api write src/orders/cart.ts
step 0 '^policy: verified$' "$BIN" policy load "$EX/signed-bundle/message.json"
step 0 '^policy: verified$' "$BIN" policy status

node "$FILES" flip "$OPENPLEXER_HOME/policy.json" token
step 4 '^policy: rejected$' "$BIN" policy status
step 4 '^no policy: ' "$BIN" policy check --repo api write src/orders/cart.ts

step 0 '^policy.unenforced policy.loaded policy.allowed policy.denied policy.denied policy.allowed policy.rejected policy.allowed policy.confirmed policy.unenforced$' node "$FILES" log "$OPENPLEXER_HOME/audit.jsonl"

exit $fail

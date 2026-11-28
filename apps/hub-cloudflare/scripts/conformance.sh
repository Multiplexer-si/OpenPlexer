#!/usr/bin/env bash
set -euo pipefail

worker_dir=$(cd "$(dirname "$0")/.." && pwd -P)
cd "$worker_dir"
export WRANGLER_SEND_METRICS=false
export XDG_CONFIG_HOME="$PWD/.wrangler-home"
export npm_config_cache="$PWD/.npm-cache"

umask 077
mkdir -p .wrangler
state=$(mktemp -d "$worker_dir/.wrangler/conformance.XXXXXX")
wrangler_pid=
cleanup() {
  local status=$?
  if [[ -n "$wrangler_pid" ]]; then
    kill "$wrangler_pid" 2>/dev/null || true
    wait "$wrangler_pid" 2>/dev/null || true
  fi
  if [[ $status -ne 0 && -f "$state/wrangler.log" ]]; then
    tail -n 40 "$state/wrangler.log" >&2
  fi
  rm -rf "$state"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Disposable local test credentials and state.
openssl rand -hex 32 > "$state/bootstrap.token"
bootstrap_token=$(cat "$state/bootstrap.token")
wrap_key=$(openssl rand -hex 32)
printf 'BOOTSTRAP_TOKEN=%s\nWRAP_KEY_V1=%s\n' "$bootstrap_token" "$wrap_key" > "$state/test.env"

origin=http://127.0.0.1:8787
./node_modules/.bin/wrangler dev --local --ip 127.0.0.1 --port 8787 \
  --var DEV_MODE:1 --env-file "$state/test.env" --persist-to "$state/sqlite" > "$state/wrangler.log" 2>&1 &
wrangler_pid=$!

cd "$worker_dir/../.."
mkdir "$state/bin"
go build -o "$state/bin/" ./apps/openplexer ./apps/openplexer-hub ./apps/openplexer-verify

health_deadline=$((SECONDS + 60))
until health_status=$(curl -fs -o "$state/health.json" -w '%{http_code}' --max-time 2 "$origin/healthz") &&
  [[ $health_status == 200 ]]; do
  if (( SECONDS >= health_deadline )) || ! kill -0 "$wrangler_pid" 2>/dev/null; then
    echo "Wrangler did not become ready at /healthz" >&2
    exit 1
  fi
  sleep 1
done
kill -0 "$wrangler_pid"
health_body=$(cat "$state/health.json")
test "$health_body" = '{"ok":true,"schema":5}'
foreign_status=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 -H 'Host: foreign.example' "$origin/healthz")
test "$foreign_status" = 404
grep -Fq 'env.BOOTSTRAP_TOKEN' "$state/wrangler.log"
grep -Fq 'env.WRAP_KEY_V1' "$state/wrangler.log"

hub="$state/bin/openplexer-hub"
"$hub" init --hub "$origin" --token-file "$state/bootstrap.token" --id lead --lead-token-file "$state/lead.token"
admin() {
  local command=$1
  shift
  "$hub" "$command" --hub "$origin" --token-file "$state/lead.token" "$@"
}
admin add-member --id member > "$state/member.token"
admin add-member --id revoked > "$state/revoked.token"
admin remove-member --id revoked
admin add-project --id conformance --repo 'R&D'
admin add-project --id conformance-other --repo 'R&D'
cat > "$state/policy.json" <<'JSON'
{"version":1,"project_id":"conformance","repos":["R&D"],"areas":[],"members":{},"roles":{}}
JSON
admin publish-policy --project conformance --file "$state/policy.json"

invite_admin() {
  local subcommand=$1
  shift
  "$hub" invite "$subcommand" --hub "$origin" --token-file "$state/lead.token" "$@"
}
invite_admin create --project conformance --member cli-invite --require-approval > "$state/invite.link"
node --input-type=module - "$origin" "$state/invite.link" > "$state/invite.id" <<'JS'
import { createHash, randomBytes } from "node:crypto";
import { readFileSync } from "node:fs";
const [origin, file] = process.argv.slice(2);
let link;
try {
  link = new URL(readFileSync(file, "utf8").trim());
} catch {
  throw new Error("Invalid CLI invite link");
}
if (link.origin !== origin || !/^#fp=[a-f0-9]{64}$/.test(link.hash)) throw new Error("Invalid CLI invite link");
const code = link.pathname.slice("/join/".length);
const token_hash = createHash("sha256").update(randomBytes(32).toString("base64url")).digest("hex");
const response = await fetch(origin + "/api/v1/invites/redeem", {
  method: "POST", redirect: "error", headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ code, token_hash }),
});
if (response.status !== 202) throw new Error("CLI invite did not require approval");
const { invite_id: id } = await response.json().catch(() => { throw new Error("Invalid approval reply"); });
if (!/^[a-f0-9]{32}$/.test(id)) throw new Error("Missing public CLI invite ID");
process.stdout.write(id);
JS
invite_id=$(cat "$state/invite.id")
invite_admin list --limit 100 > "$state/invites.json"
grep -Fq "$invite_id" "$state/invites.json"
invite_admin approve --id "$invite_id" > /dev/null
invite_admin revoke --id "$invite_id" > /dev/null

sections='^TestHub$/^(auth|methods|jwks|projects-and-policy|audit-export|queued-denial|room-events|claims|concurrency|live-stream|invites|member-sync|hook-policy-age)$'
OPENPLEXER_BIN_DIR="$state/bin" go test -count=1 -v ./test/hubconformance -run "$sections" -args \
  -hub-url "$origin" -wrangler-dev -lead-token-file "$state/lead.token" -member-token-file "$state/member.token" \
  -revoked-token-file "$state/revoked.token"

admin members --project conformance-other | grep -F 'member  role member  policy v2  last check-in'

admin export --out "$state/audit"
verify() {
  "$state/bin/openplexer-verify" --export "$state/audit/$1" --jwks "$state/audit/jwks.json" \
    --checkpoint "$state/audit/checkpoint.jws" --json
}
verify export.json > "$state/audit/verified.json"
node - "$state/audit" <<'JS'
const fs = require("node:fs");
const dir = process.argv[2];
const audit = JSON.parse(fs.readFileSync(dir + "/export.json", "utf8"));
const result = JSON.parse(fs.readFileSync(dir + "/verified.json", "utf8"));
if (result.ok !== true || result.completeness !== "through_checkpoint" ||
    result.entries !== audit.entries.length || result.checkpoint_entries !== result.entries) {
  throw new Error("Expected the checkpoint to authenticate the full export");
}
const actions = new Set(audit.entries.map((entry) => entry.action));
for (const action of ["event.policy-denied", "claim.created", "claim.renewed", "claim.released", "claim.expired",
  "invite.created", "invite.requested", "invite.approved", "invite.redeemed", "invite.revoked"]) {
  if (!actions.has(action)) throw new Error("Missing audit action " + action);
}
audit.entries[0].target += "-tampered";
fs.writeFileSync(dir + "/tampered.json", JSON.stringify(audit));
JS
if verify tampered.json > "$state/audit/rejected.json"; then
  echo "Verifier accepted a changed audit record" >&2
  exit 1
else
  test "$?" -eq 1
fi
grep -Fq '"rule":"A3"' "$state/audit/rejected.json"

curl -fsS --max-time 5 "$origin/.well-known/jwks.json" > "$state/policy.jwks.json"
export OPENPLEXER_HOME="$state/openplexer"
cli="$state/bin/openplexer"
"$cli" configure --hub "$origin" --project conformance --member member \
  --keys "$state/policy.jwks.json" --token-file "$state/member.token"
"$cli" policy sync
"$cli" policy status | grep -Fx 'policy: verified'

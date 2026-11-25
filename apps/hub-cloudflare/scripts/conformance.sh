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
    cat "$state/wrangler.log" >&2
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

health_status=$(curl -fsS -o "$state/health.json" -w '%{http_code}' --max-time 2 \
  --retry-connrefused --retry 15 --retry-delay 1 "$origin/healthz")
kill -0 "$wrangler_pid"
test "$health_status" = 200
health_body=$(cat "$state/health.json")
test "$health_body" = '{"ok":true,"schema":1}'
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

sections='^TestHub$/^(auth|methods|jwks|projects-and-policy|audit-export)$/^(escaped-paths|policy-jwks|audit-jwks|audit-export|project|policy-bundle|publish-policy|policy|audit|separate-key-pairs|read-projects-and-seeded-policy|missing-project-and-bundle|publish-and-version-floor|body-limit|canonical-limit)$'
go test -count=1 -v ./test/hubconformance -run "$sections" -args \
  -hub-url "$origin" -wrangler-dev -lead-token-file "$state/lead.token" -member-token-file "$state/member.token" \
  -revoked-token-file "$state/revoked.token"

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

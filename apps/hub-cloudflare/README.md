# Cloudflare hub

This Worker routes requests for one team to one SQLite-backed Durable Object.
On startup, the object applies any pending schema migrations.
`GET /healthz` returns `{"ok":true,"schema":3}` on a fresh object.
The Worker serves team setup, member and project administration, signed policies, Room events, claims, and signed audit exports.

Both hubs require roster bearers to use 43 characters from `A-Z`, `a-z`, `0-9`, `-`, and `_`. The Worker returns 401 for a malformed bearer header before reading the body. On capped Go policy and Room routes, a single Bearer header with a 43-byte token reaches the body cap before the alphabet check. Go invite and roster routes check the bearer format before reading the body. The JWKS routes are public.

Use the Room events endpoint to post and replay events, and the claim endpoints to create, renew, and release leases. To follow a Room, open `GET /api/v1/rooms/{room}/stream?last_seq=0` with a member bearer. The stream sends v1 NDJSON envelopes after the cursor and blank lines while caught up. It checks membership on every page read. If an in-stream page read detects revocation before the deadline while the response is writable, the Worker sends an `unauthorized` envelope and closes the stream. The stream can end with clean EOF before a page read detects revocation. The next GET returns HTTP `401` if its first authenticated page read detects the revocation before that GET's deadline. If the deadline arrives first, the Worker returns an empty HTTP `200`.

The five-minute deadline starts when the Worker receives the request and applies to every page read. The Worker discards results that arrive at or after the deadline. If the deadline expires before the first frame, the Worker returns an empty NDJSON response with HTTP 200. The Worker makes at most 400 Durable Object calls per stream, including the first authenticated read. After the stream ends, open a new GET from the last saved sequence. The Worker returns 405 for POST to the stream route. [Hub HTTP API](../../docs/hub.md#http-api)

## Run locally

From this directory, use Node 24 and npm 11.

```sh
export WRANGLER_SEND_METRICS=false
export XDG_CONFIG_HOME="$PWD/.wrangler-home"
export npm_config_cache="$PWD/.npm-cache"
npm ci
```

Create `.dev.vars` with mode `0600` before adding local secrets. The command stops if `.dev.vars` already exists.

```sh
(
  set -e
  umask 077
  credentials=$(mktemp -d .dev.vars.XXXXXX)
  trap 'rm -f "$credentials/.dev.vars" && rmdir "$credentials"' EXIT
  cat .dev.vars.example > "$credentials/.dev.vars"
  ln "$credentials/.dev.vars" .
)
```

After the command succeeds, edit `.dev.vars` in place. Set `BOOTSTRAP_TOKEN` to a random bootstrap bearer and `WRAP_KEY_V1` to 64 hex digits from `openssl rand -hex 32`.
Keep a private file with the bootstrap bearer for the one-time initialization.

```sh
npx wrangler dev --local --var DEV_MODE:1
```

In another terminal, run `curl -i http://127.0.0.1:8787/healthz`.
Local development uses `.wrangler/state` for SQLite data. Keep that directory to reuse the database after a restart.

## Test

```sh
npm run typecheck
npm run test:unit
npm test
npm run deploy -- --dry-run --outdir dist
```

The Node suite runs the canonical JSON, policy signature, audit, Room event, envelope, and claim overlap fixtures.
The Workers suite runs the same tests in workerd, plus the Durable Object and SQLite tests. The live stream cases cover replay, live writes, heartbeats, revocation, disconnect, and the call and time budgets.
It checks Web Crypto for signing and wrapping and uses synchronous `node:crypto` to seal records.
The export load test drains 250,001 sealed records and reports the byte count and elapsed time.
The pinned workerd compatibility date enables Node.js compatibility by default.
Testing and building the bundle require no Cloudflare account.

Run `./scripts/conformance.sh` from this directory with Go, Node, curl, and OpenSSL installed.
The script starts Wrangler on port 8787 with fresh SQLite state and disposable credentials.
It seeds the hub with remote admin commands and checks auth, methods, both JWKS endpoints, projects and policies, audit export, Room events, claims, concurrent writes, and live streams.
It verifies the export with `openplexer-verify`, checks for Room denials and claim records, and confirms that changing a record fails with rule A3.
It also runs `openplexer policy sync`.
The run ends with `policy: verified`.

You can also run the real-agent flow against a separate, freshly initialized Worker. [Run the multi-agent flow](../../test/multiagent/README.md#run-against-a-worker)

Run `npm run gen:vectors` to regenerate the TypeScript-signed bundle and checkpoint
in `test/vectors/ts-signed.json`. The file marks its fixed private keys as test data.
The Node and Workers suites verify both signatures and compare the other bytes
with fresh generator output.
From the repository root, run `go test ./internal/hub -run TestTypeScriptSignedVectors`
to check the bundle, sealed chain, and checkpoint with the Go verifiers.

The private-key wrapper uses AES-256-GCM with a fresh 12-byte nonce.
Its authenticated data is the canonical JSON array of team ID, key purpose, and `kid` in that order.

## Deploy

In `wrangler.toml`, set `TEAM_ID` to a stable team ID and set `HUB_HOSTNAME` to the hostname that will serve the Worker.
Use the Worker's `workers.dev` hostname or configure a Cloudflare Custom Domain.
Keep `DEV_MODE` empty on the deployed Worker.

For a new Worker, create `.wrangler/bootstrap.token` with mode `0600`. The command stops if that name is taken.

```sh
(
  set -e
  umask 077
  mkdir -p .wrangler
  credentials=$(mktemp -d .wrangler/bootstrap.XXXXXX)
  trap 'rm -f "$credentials/bootstrap.token" && rmdir "$credentials"' EXIT
  openssl rand -hex 32 > "$credentials/bootstrap.token"
  ln "$credentials/bootstrap.token" .wrangler
)
```

After the bootstrap file is ready, log in and deploy. If login fails, fix the login error and rerun this block from `npx wrangler login`. Use the bootstrap file you already saved.

```sh
(
  set -e -o pipefail
  npx wrangler login
  npm run deploy
  npx wrangler secret put BOOTSTRAP_TOKEN < .wrangler/bootstrap.token
  openssl rand -hex 32 | npx wrangler secret put WRAP_KEY_V1
)
```

Keep the wrapping secret for policy and audit signing. Check `/healthz` on the configured HTTPS hostname.

## Set up a team

From the repository root, run `make build`. Use the path to your private bootstrap file. For local Wrangler, set `bootstrap_file` to the local bootstrap file and use `http://127.0.0.1:8787` as the origin.
The block saves the member tokens in a fresh private directory.

```sh
hub=https://hub.example.com
bootstrap_file=apps/hub-cloudflare/.wrangler/bootstrap.token
umask 077
tokens=$(mktemp -d "$HOME/openplexer-team.XXXXXX") &&
(
  set -e
  bin/openplexer-hub init --hub "$hub" --token-file "$bootstrap_file" \
    --id mem-lee --lead-token-file "$tokens/lee.token"
  bin/openplexer-hub add-member --hub "$hub" --token-file "$tokens/lee.token" --id mem-ana > "$tokens/ana.token"
  bin/openplexer-hub add-project --hub "$hub" --token-file "$tokens/lee.token" --id prj-shop --repo api
)
```

If initialization fails, rerun just the `init` command with the same `lee.token` path. Then continue with `add-member` and `add-project`.
After using the lead token successfully against a deployed Worker, run `npx wrangler secret delete BOOTSTRAP_TOKEN` from this directory.
Give `ana.token` to its member through a private channel.

Copy the [minimal bundle example](../../spec/policy/v1/examples/bundle/minimal.json) to `bundle.json`.
Set `repos` to `["api"]` and keep `project_id` as `prj-shop`.

```sh
bin/openplexer-hub publish-policy --hub "$hub" --token-file "$tokens/lee.token" --project prj-shop --file bundle.json
```

Fetch the policy keys from `/.well-known/jwks.json` over your trusted HTTPS origin and give the JWKS file to your team.
Follow the [agent setup](../../docs/local-policy.md#connect-to-a-team-hub) to pin the keys and sync policy.

`GET /api/v1/audit/export` requires a lead token. It returns the full team chain and a signed checkpoint.
The chain includes Room policy denials and claim creation, renewal, release, and expiry recorded by this Worker.
`GET /.well-known/audit-jwks.json` publishes all checkpoint keys, including retired keys.
Use a lead token to export the log or rotate either signing key. [Hub admin commands](../../docs/hub.md#administer-a-cloudflare-team)

See the [Cloudflare hub design](../../docs/cloudflare-hub-design.md) for the storage and deployment model.

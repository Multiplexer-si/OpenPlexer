# Cloudflare hub

This Worker routes requests for one team to one SQLite-backed Durable Object.
On startup, the object applies any pending schema migrations.
`GET /healthz` returns `{"ok":true,"schema":1}` on a fresh object.
The Worker serves team setup, member and project administration, signed policies, Room events, claims, and signed audit exports.

On authenticated routes, the Worker rejects bearer tokens containing a comma with 401 before reading the body. On capped routes, the Go hub applies the body cap before rejecting a single 43-byte comma bearer. The public JWKS routes check no bearer.

Use the Room events endpoint to post and replay events, and the claim endpoints to create, renew, and release leases. To follow a Room, open `GET /api/v1/rooms/{room}/stream?last_seq=0` with a member bearer. The stream sends v1 NDJSON envelopes after the cursor and blank lines while caught up. It checks membership on every page read. It sends `unauthorized` and closes if the member is revoked.

The five-minute deadline starts when the Worker receives the request and applies to every page read. The Worker discards results that arrive at or after the deadline. If the deadline expires before the first frame, the Worker returns an empty NDJSON response with HTTP 200. The Worker makes at most 400 Durable Object calls per stream, including the first authenticated read. After the stream ends, open a new GET from the last saved sequence. The Worker returns 405 for POST to the stream route. [Hub HTTP API](../../docs/hub.md#http-api)

## Run locally

From this directory, use Node 24 and npm 11.

```sh
export WRANGLER_SEND_METRICS=false
export XDG_CONFIG_HOME="$PWD/.wrangler-home"
export npm_config_cache="$PWD/.npm-cache"
npm ci
npx wrangler dev --local --var DEV_MODE:1
```

In another terminal, run `curl -i http://127.0.0.1:8787/healthz`.
Local development uses `.wrangler/state` for SQLite data. Keep that directory to reuse the database after a restart.
To supply local secrets, copy `.dev.vars.example` to `.dev.vars`.
Set `BOOTSTRAP_TOKEN` to a random bootstrap bearer and `WRAP_KEY_V1` to 64 hex digits from `openssl rand -hex 32`.
Keep a private file with the bootstrap bearer for the one-time initialization.

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

```sh
npx wrangler login
npm run deploy
umask 077
mkdir -p .wrangler
openssl rand -hex 32 > .wrangler/bootstrap.token
npx wrangler secret put BOOTSTRAP_TOKEN < .wrangler/bootstrap.token
openssl rand -hex 32 | npx wrangler secret put WRAP_KEY_V1
```

Keep the wrapping secret for policy and audit signing. Check `/healthz` on the configured HTTPS hostname.

## Set up a team

From the repository root, run `make build`. In these examples, use the path to the private bootstrap file created above.
For Wrangler on your machine, use `http://127.0.0.1:8787` as the origin.

```sh
hub=https://hub.example.com
tokens=apps/hub-cloudflare/.wrangler
umask 077
bin/openplexer-hub init --hub "$hub" --token-file "$tokens/bootstrap.token" \
  --id mem-lee --lead-token-file "$tokens/lee.token"
bin/openplexer-hub add-member --hub "$hub" --token-file "$tokens/lee.token" --id mem-ana > "$tokens/ana.token"
bin/openplexer-hub add-project --hub "$hub" --token-file "$tokens/lee.token" --id prj-shop --repo api
```

If initialization fails, rerun it with the same `lee.token` file.
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

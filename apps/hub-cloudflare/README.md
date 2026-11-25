# Cloudflare hub

This Worker routes requests for one team to one SQLite-backed Durable Object.
On startup, the object applies any pending schema migrations.
`GET /healthz` returns `{"ok":true,"schema":1}` on a fresh object.
Other requests return `{"error":"not found"}` with status 404.

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
To supply local secrets, copy `.dev.vars.example` to `.dev.vars` and fill in both values.

## Test

```sh
npm run typecheck
npm run test:unit
npm test
npm run deploy -- --dry-run --outdir dist
```

The Node suite runs the canonical JSON, policy signature, and audit fixtures.
The Workers suite runs the same tests in workerd, plus the Durable Object and SQLite tests.
It checks Web Crypto for signing and wrapping and uses synchronous `node:crypto` to seal records.
The pinned workerd compatibility date enables Node.js compatibility by default.
Testing and building the bundle require no Cloudflare account.

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
npx wrangler secret put BOOTSTRAP_TOKEN
npx wrangler secret put WRAP_KEY_V1
npm run deploy
```

Use separate random values for the bootstrap token and the 32-byte wrapping key.
These bindings are reserved for team initialization and key storage.
After deploy, make an HTTPS request to `/healthz` on the configured hostname.

See the [Cloudflare hub design](../../docs/cloudflare-hub-design.md) for the storage and deployment model.

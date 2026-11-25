# Cloudflare hub

This Worker routes requests for one team to one SQLite-backed Durable Object.
On startup, the object applies any pending schema migrations.
`GET /healthz` returns `{"ok":true,"schema":1}` on a fresh object.
The Worker serves team setup, member and project administration, and signed policies.

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

The Node suite runs the canonical JSON, policy signature, and audit fixtures.
The Workers suite runs the same tests in workerd, plus the Durable Object and SQLite tests.
It checks Web Crypto for signing and wrapping and uses synchronous `node:crypto` to seal records.
The pinned workerd compatibility date enables Node.js compatibility by default.
Testing and building the bundle require no Cloudflare account.

Run `./scripts/conformance.sh` from this directory with Go, curl, and OpenSSL installed.
The script starts Wrangler on port 8787 with fresh SQLite state and disposable credentials.
It seeds the hub, runs the policy and member HTTP checks, and runs `openplexer policy sync`.
The run ends with `policy: verified`.

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

Keep the wrapping secret for future policy signing. Check `/healthz` on the configured HTTPS hostname.

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

See the [Cloudflare hub design](../../docs/cloudflare-hub-design.md) for the storage and deployment model.

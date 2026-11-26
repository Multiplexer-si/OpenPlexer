# Run the single-team hub

`openplexer-hub` serves signed policy bundles, Room events, and audit exports for one team. Each project names one repository and has one Room with the same ID. All active team members can access every project and Room.
For a walkthrough from a clean machine to a three-person hub, see [Self-host the OpenPlexer hub](self-host.md).
For the Phase 5 design, see [Cloudflare hub design](cloudflare-hub-design.md).

## Choose a hub

| Hub | What you operate |
|---|---|
| Go hub | Run a binary and own its data directory, backups, and HTTPS reverse proxy. Use local commands on the hub host for team administration. [Self-host the Go hub](self-host.md) |
| Cloudflare hub | Deploy a Worker and one SQLite Durable Object on Workers Paid. Cloudflare runs the service and supplies HTTPS. Leads administer the team over HTTPS. [Deploy to Cloudflare](cloudflare.md) |

Both hubs use the same developer CLI, agent hooks, signed policy format, Room protocol, and audit verifier.

## Start the server

Build the binaries with `make build`. On a Unix host, choose a private data directory on a local filesystem that supports file locks and atomic renames. Run the server and admin commands as the account that owns the directory.

```sh
bin/openplexer-hub init -data-dir ./hub-data
bin/openplexer-hub serve -data-dir ./hub-data
```

The server listens on `127.0.0.1:8787` by default. To let teammates connect, put an HTTPS reverse proxy in front of the HTTP listener. Keep the listener on loopback when the proxy runs on the same host. Use `-listen` to change the address.

## Add members and publish a policy

Run the admin commands in another terminal on the hub host. Each command uses the same data directory as `serve` and can run while the server is up.

```sh
bin/openplexer-hub add-member -data-dir ./hub-data -id mem-lee -role lead
bin/openplexer-hub add-member -data-dir ./hub-data -id mem-ana
bin/openplexer-hub add-project -data-dir ./hub-data -id prj-shop -repo api
```

`add-member` prints the new bearer token once. Give each token to its member through a private channel. The default role is `member`. A `lead` can publish policy and export the audit log over HTTP. Local admin commands use filesystem access for authorization. To revoke a token, run `bin/openplexer-hub remove-member -data-dir ./hub-data -id mem-ana`. Adding that ID again creates a new token.

Copy the [minimal JSON bundle example](../spec/policy/v1/examples/bundle/minimal.json) to `bundle.json` and set `repos` to `["api"]`. The bundle's `project_id` and sole `repos` entry must match the project exactly. Use the same repository identifier in the agent's policy queries. IDs for members, projects, and agent sessions start with an ASCII letter or digit and hold up to 128 letters, digits, periods, underscores, or hyphens.

```sh
bin/openplexer-hub publish-policy -data-dir ./hub-data -project prj-shop -file bundle.json
```

The hub validates the [policy bundle](../spec/policy/README.md), checks member IDs against the hub roster, and signs the canonical JSON. Policy role names and grants come from the bundle. Increase `version` with each change. Republishing identical canonical JSON at the current version returns the stored signature. Update and republish the bundle to remove a member's policy grants after revoking their hub token.

## Administer a Cloudflare team

Use `--hub <origin>` for remote commands.
Use HTTPS off loopback. The commands refuse redirects.
Pass a private regular file with `--token-file` or send the credential on stdin.
[Deploy the Worker and set its secrets](cloudflare.md).

```sh
hub=https://hub.example.com
umask 077
bin/openplexer-hub init --hub "$hub" --id mem-lee --lead-token-file lee.token < bootstrap.token
bin/openplexer-hub add-member --hub "$hub" --token-file lee.token --id mem-ana > ana.token
bin/openplexer-hub add-project --hub "$hub" --token-file lee.token --id prj-shop --repo api
bin/openplexer-hub publish-policy --hub "$hub" --token-file lee.token --project prj-shop --file bundle.json
bin/openplexer-hub remove-member --hub "$hub" --token-file lee.token --id mem-ana
```

For `init`, use the bootstrap credential. The command saves the first lead's token in the named private file and sends its SHA-256 hash.
Keep the file and use it again if initialization needs a retry.
Use a lead token for the other commands. `add-member` prints the new token once. The Worker refuses to remove the last lead.

Use `stage-key` and `activate-key` with a lead token to rotate a Worker signing key.
Set `--purpose` to `policy` or `audit`. Staging prints the pending key ID and publishes the public key in its JWKS.

```sh
kid=$(bin/openplexer-hub stage-key --hub "$hub" --token-file lee.token --purpose audit)
```

Share the updated JWKS through a trusted channel before you activate the key.

```sh
bin/openplexer-hub activate-key --hub "$hub" --token-file lee.token --purpose audit --kid "$kid"
```

The next audit export uses the new audit key. After policy activation, publish a higher policy version to sign a project's bundle with the new policy key. The JWKS keeps retired public keys.

## HTTP API

Use `Authorization: Bearer <token>` on every request except the two public key endpoints. Successful requests return `200`. Use JSON for request and response bodies on routes outside the Room streams.

| Method and path | Result |
|---|---|
| `GET /.well-known/jwks.json` | The public policy signing keys in the [v1 JWKS format](../spec/policy/v1/jwks.schema.json). |
| `GET /.well-known/audit-jwks.json` | The public audit checkpoint signing keys. |
| `GET /api/v1/audit/export` | The full audit export, newest first, with a signed tip in the `OpenPlexer-Audit-Checkpoint` response header. Requires a lead token. |
| `GET /api/v1/projects/{project}` | The project's `id` and `repo`. |
| `GET /api/v1/projects/{project}/policy.bundle` | The latest [signed policy bundle](../spec/policy/v1/signed-bundle.schema.json). |
| `PUT /api/v1/projects/{project}/policy` | Publish a JSON bundle and return the signed bundle. Requires a lead token. |
| `POST /api/v1/rooms/{room}/events` | Append an event and return its v1 envelope. |
| `GET /api/v1/rooms/{room}/stream?last_seq=0` | Stream replay and live v1 Room envelopes as NDJSON. The cursor defaults to zero. |
| `POST /api/v1/rooms/{room}/stream` | Exchange v1 Room envelopes on a full-duplex HTTP stream. The Go hub serves this route for older daemons. |
| `GET /api/v1/rooms/{room}/events?last_seq=0` | Return up to 100 v1 event envelopes after `last_seq`, oldest first. The cursor defaults to zero. |
| `POST /api/v1/rooms/{room}/claims` | Create a claim and return `granted`, `conflicts`, and the claim's `event` envelope. |
| `POST /api/v1/rooms/{room}/claims/{claim}/renew` | Recompute a claim's expiry and return the updated lease. |
| `POST /api/v1/rooms/{room}/claims/{claim}/release` | End a claim, append a release event, and return the released lease. |

Give members a trusted copy of the policy JWKS through an authenticated channel. See the [agent setup](local-policy.md#connect-to-a-team-hub). The agent verifies the JWS against the configured pin and the hub's advertised JWKS. The unsigned display fields are for presentation. The docs on [policy verification](../spec/policy/README.md#the-signed-bundle) give the verification steps.

For an event post, send `type`, `body`, and `agent` with `id`, `vendor`, and optional `parent`. Omit `agent` for a `human` event. The hub prefixes session and parent IDs with the authenticated member ID and a slash. The hub sets `on_behalf_of`, the event ID, the Room, the timestamp, and the next sequence number. The client can send `Idempotency-Key` with up to 128 bytes. A repeated key for the same member and Room returns the original envelope. The claim endpoints write `claim` and `release` events. The event endpoint reserves `decision.confirm` and the two override event types for future hub services.

Claim creation takes `agent`, `item`, `paths`, and `intent`. Optional `mode` defaults to `shared` and also accepts `exclusive`. Optional `ttl_seconds` defaults to 3,600 seconds, or 600 seconds when `implicit` is true. The lease holds its `id`, `room`, `on_behalf_of`, `agent`, `paths`, `intent`, `mode`, and `expires_at`. Overlapping claims from different sessions conflict when they target the same repository and at least one is exclusive. Conflicts are advisory. Renew and release require the holder's member token, Room, and original unprefixed `session_id`. Renew sets the expiry to the current time plus `ttl_seconds`, which can shorten the lease. Release takes an optional `outcome`, with `expired` reserved for the hub.

Any authenticated claim operation in an existing Room sweeps lapsed claims. The hub appends their releases in the original holders' names. Renew changes the stored lease, so readers of the event log still expire the claim at its original deadline. [Room rules O1, E5, and E8](../spec/room/README.md#order-expiry-reconnection-and-errors) describe these semantics.

For paged replay, pass the highest sequence number received as the next cursor. A cursor at or beyond the latest event returns an empty array.

Open the GET stream with one `last_seq` query parameter and no request body. The same cursor rules apply to replay and streaming. The hub authenticates the request before sending any frames. Stream responses use `Content-Type: application/x-ndjson` and `Cache-Control: no-store`. The hub sends events after the cursor in order and then sends new events as they arrive. A blank response line is a heartbeat. The daemon opens a new GET from its saved cursor after EOF, timeout, or a `behind` envelope. Disable response buffering for the stream route on your reverse proxy.

The Worker ends the response with clean EOF when it reaches its page call budget or stream deadline. The daemon reconnects quietly after clean EOF. A page read failure before streaming starts returns an HTTP error status and a JSON error body. After streaming starts, either hub aborts the response body if a page read fails. The daemon reports either failure and waits one second before trying again. If an in-stream page read detects revocation while the response is writable and before any stream deadline, the hub sends an `unauthorized` envelope and closes the stream. The stream can end with clean EOF before a page read detects revocation. On the next GET with the revoked token, the Go hub returns HTTP `401`. The Worker returns HTTP `401` if its first authenticated page read detects the revocation before that GET's deadline. If the deadline arrives first, the Worker returns an empty HTTP `200`.

Upgrade each hub to a version that serves the GET stream before upgrading its daemons. Daemons from this release require the GET stream to receive Room events. On legacy POST streams, older daemons keep the request body open and send a v1 `resume` envelope. A second `resume` replaces the subscription on the same stream. A proxy serving legacy POST must forward the request and response concurrently.

| Error | Meaning |
|---|---|
| `400` | The request failed validation. |
| `401` | The bearer token is missing, invalid, or revoked. |
| `403` | The caller lacks the required role or tried to post a reserved event type. |
| `404` | The project, policy, Room, or active claim is unavailable to this caller. |
| `409` | The proposed policy version conflicts with the current version. |
| `413` | The body exceeds 1 MiB for policies or 64 KiB for Room operations. |

## Export the audit log

The hub writes one chain for the team. It writes each audit record in the same state update as its action. The log covers `member.added`, `member.removed`, `project.added`, `policy.updated`, and `event.policy-denied`. A denial record links to its Room event. The hub leaves the Room body out of the audit record.

Run `bin/openplexer-hub export-audit --data-dir ./hub-data --out ./audit-export` to write the JSON export, signed checkpoint, and checkpoint JWKS. The command works with the server stopped. The HTTP export requires the `lead` role, which is the single-team hub's admin role. An ordinary member receives `403`. [Export and verify an audit log](audit.md).

To export from a running hub, use a lead token.

```sh
bin/openplexer-hub export --hub "$hub" --token-file lee.token --out ./audit-export
```

`export-audit` also accepts `--hub`. Both forms save `export.json`, `checkpoint.jws`, and `jwks.json` as private files. Both preserve existing files. The command fetches the checkpoint JWKS after downloading the export.

## Data directory and operational limits

Use one server on one host per data directory. The hub uses a file lock to coordinate the server and CLI, and atomically replaces `state.json` on writes. Each operation reads the snapshot into memory. Room history and audit records accumulate, so disk use, memory use, and write time grow.

Protect `state.json` and its backups. It contains the separate ES256 policy and checkpoint private keys, member token hashes, policies, Room history, claims, and the audit chain. Back it up to preserve the keys, the stable team ID, Room sequence numbers, and audit history. The hub initializes audit state in older data directories on the first audited action, export, or checkpoint key request. The audit chain starts with the first audited action. The hub keeps the public checkpoint key history. The local daemon filters posts before sending them. The hub validates event schemas. Server-side content filtering remains open for direct API callers. Keep sensitive material out of Room bodies. The [trust model](trust-model.md#decisions-settled-by-the-single-team-hub) records these choices.

# Run the single-team hub

`openplexer-hub` serves signed policy bundles and Room events for one team. Each project names one repository and has one Room with the same ID. All active team members can access every project and Room.
For a walkthrough from a clean machine to a three-person hub, see [Self-host the OpenPlexer hub](self-host.md).

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

`add-member` prints the new bearer token once. Give each token to its member through a private channel. The default role is `member`. A `lead` can also publish policy over HTTP. Local admin commands use filesystem access for authorization. To revoke a token, run `bin/openplexer-hub remove-member -data-dir ./hub-data -id mem-ana`. Adding that ID again creates a new token.

Copy the [minimal JSON bundle example](../spec/policy/v1/examples/bundle/minimal.json) to `bundle.json` and set `repos` to `["api"]`. The bundle's `project_id` and sole `repos` entry must match the project exactly. Use the same repository identifier in the agent's policy queries. IDs for members, projects, and agent sessions start with an ASCII letter or digit and hold up to 128 letters, digits, periods, underscores, or hyphens.

```sh
bin/openplexer-hub publish-policy -data-dir ./hub-data -project prj-shop -file bundle.json
```

The hub validates the [policy bundle](../spec/policy/README.md), checks member IDs against the hub roster, and signs the canonical JSON. Policy role names and grants come from the bundle. Increase `version` with each change. Republishing identical canonical JSON at the current version returns the stored signature. Update and republish the bundle to remove a member's policy grants after revoking their hub token.

## HTTP API

Use `Authorization: Bearer <token>` on every request except the public key set. Send JSON for request bodies. Successful requests return `200` and JSON.

| Method and path | Result |
|---|---|
| `GET /.well-known/jwks.json` | The public ES256 key set in the [v1 JWKS format](../spec/policy/v1/jwks.schema.json). |
| `GET /api/v1/projects/{project}` | The project's `id` and `repo`. |
| `GET /api/v1/projects/{project}/policy.bundle` | The latest [signed policy bundle](../spec/policy/v1/signed-bundle.schema.json). |
| `PUT /api/v1/projects/{project}/policy` | Publish a JSON bundle and return the signed bundle. Requires a lead token. |
| `POST /api/v1/rooms/{room}/events` | Append an event and return its v1 envelope. |
| `GET /api/v1/rooms/{room}/events?last_seq=0` | Return up to 100 v1 event envelopes after `last_seq`, oldest first. The cursor defaults to zero. |
| `POST /api/v1/rooms/{room}/claims` | Create a claim and return `granted`, `conflicts`, and the claim's `event` envelope. |
| `POST /api/v1/rooms/{room}/claims/{claim}/renew` | Recompute a claim's expiry and return the updated lease. |
| `POST /api/v1/rooms/{room}/claims/{claim}/release` | End a claim, append a release event, and return the released lease. |

The caller verifies the JWS against a trusted key set and enforces the signed payload. The unsigned display fields are for presentation. The docs on [policy verification](../spec/policy/README.md#the-signed-bundle) give the verification steps.

For an event post, send `type`, `body`, and `agent` with `id`, `vendor`, and optional `parent`. Omit `agent` for a `human` event. The hub prefixes session and parent IDs with the authenticated member ID and a slash. The hub sets `on_behalf_of`, the event ID, the Room, the timestamp, and the next sequence number. The client can send `Idempotency-Key` with up to 128 bytes. A repeated key for the same member and Room returns the original envelope. The claim endpoints write `claim` and `release` events. The event endpoint reserves `decision.confirm` and the two override event types for future hub services.

Claim creation takes `agent`, `item`, `paths`, and `intent`. Optional `mode` defaults to `shared` and also accepts `exclusive`. Optional `ttl_seconds` defaults to 3,600 seconds, or 600 seconds when `implicit` is true. The lease holds its `id`, `room`, `on_behalf_of`, `agent`, `paths`, `intent`, `mode`, and `expires_at`. Overlapping claims from different sessions conflict when they target the same repository and at least one is exclusive. Conflicts are advisory. Renew and release require the holder's member token, Room, and original unprefixed `session_id`. Renew sets the expiry to the current time plus `ttl_seconds`, which can shorten the lease. Release takes an optional `outcome`, with `expired` reserved for the hub.

Any authenticated claim operation in an existing Room sweeps lapsed claims. The hub appends their releases in the original holders' names. Renew changes the stored lease, so readers of the event log still expire the claim at its original deadline. [Room rules O1, E5, and E8](../spec/room/README.md#order-expiry-reconnection-and-errors) describe these semantics.

For replay, use the highest sequence number the client received as the next cursor. A cursor at or beyond the latest event returns an empty array. Each HTTP request uses its own cursor. [Issue #20](https://github.com/Multiplexer-si/OpenPlexer/issues/20) connects the local agent, caches verified policy for offline use, and owns live Room delivery and replacement of subscriptions on the same connection under rule C5.

| Error | Meaning |
|---|---|
| `400` | The request failed validation. |
| `401` | The bearer token is missing, invalid, or revoked. |
| `403` | The caller lacks the required role or tried to post a reserved event type. |
| `404` | The project, policy, Room, or active claim is unavailable to this caller. |
| `409` | The proposed policy version conflicts with the current version. |
| `413` | The body exceeds 1 MiB for policies or 64 KiB for Room operations. |

## Data directory and operational limits

Use one server on one host per data directory. The hub uses a file lock to coordinate the server and CLI, and atomically replaces `state.json` on writes. Each operation reads the snapshot into memory. Room history grows with each event, so disk use, memory use, and write time grow with it.

Protect `state.json` and its backups. It contains the ES256 private key, member token hashes, the latest signed policy per project, Room history, and active claims. Back it up to preserve the signing key and Room sequence numbers. The hub keeps Room history indefinitely and generates a signing key once at initialization. Key rotation requires a future change. The hub validates event schemas but leaves secret and code checks open. Keep sensitive material out of Room bodies. The [trust model](trust-model.md#decisions-settled-by-the-single-team-hub) records these choices.

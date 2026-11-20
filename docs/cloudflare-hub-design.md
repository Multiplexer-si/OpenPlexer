# Cloudflare hub design

Phase 5 adds a TypeScript hub in `apps/hub-cloudflare`. A team runs a Worker and one SQLite-backed Durable Object (to confirm, F2 and F3). The `openplexer` commands, hooks, policy format, Room v1 envelopes, audit format, and `openplexer-verify` keep their existing interfaces. The [Go hub](hub.md) remains available for self-hosting.

## 1. Storage

Give each team one HTTPS origin, one Worker deployment, and one Durable Object namespace. The Worker checks the request hostname against its configured hostname and returns 404 for other hosts. It routes every accepted request, including both public JWKS requests, to the named object `team:<configured-team-id>` in its binding (to confirm, F1 and F2). Keep the namespace and name stable across deploys. The origin gives the current CLI a team selector with no change to its URL, paths, or credentials.

| Table | Columns and keys |
|---|---|
| `team` | `singleton` primary key, `workspace_id` stable random audit ID, `policy_kid`, `audit_kid`, `audit_seq`, `audit_tip`. Start at sequence 0 and the genesis hash. |
| `signing_keys` | `kid` primary key, `purpose` (`policy` or `audit`), `public_jwk`, `status` (`staged`, `active`, or `retired`), nullable `wrapped_pkcs8`, `nonce`, `wrap_key_id`. |
| `members` | `member_id` primary key, `role` (`lead` or `member`), unique `token_hash` (32-byte SHA-256). |
| `projects` | `project_id` primary key and Room ID, indexed `repo`, `room_seq` initially 0. Several projects can name the same repo. |
| `policies` | `project_id` primary key, `version`, `bundle_json` (canonical UTF-8), `kid`, `jws_header`, `jws_signature`. Keep the latest version per project, as Go does. Store the compact JWS header and signature segments and reconstruct its payload segment from `bundle_json`. |
| `room_events` | Primary key `(room_id, seq)`, unique `event_id`, `event_json`, nullable `idempotency_member_id` and `idempotency_key`. Unique index on `(room_id, idempotency_member_id, idempotency_key)`. |
| `claims` | `claim_id` primary key, `room_id`, `member_id`, `agent_id`, `agent_vendor`, nullable `agent_parent`, `paths_json`, `intent`, `mode`, `expires_at_ms`. Index `room_id` and `expires_at_ms`. |
| `audit_records` | `seq` primary key (export as decimal `id`), `workspace_id`, nullable `actor_member_id`, `agent_session_id`, and `reason`, `action`, `target`, `meta_json`, `flagged`, `created_at`, `prev_hash`, `hash`. Derive export display fields when reading. |

Revoking a member deletes only the roster row. Historical IDs stay valid. Keep Room and audit history append-only. Allocate a Room sequence by incrementing `projects.room_seq` in the same transaction as the event insert. A validated event retry with the same member, Room, and idempotency key returns its original envelope.

Each audited write uses `transactionSync` (to confirm, F3). In one callback, check authorization and state, apply the change, seal the record against `audit_tip`, insert it at `audit_seq + 1`, and advance both tip fields. This covers `member.added` (including the first lead), `member.removed`, `project.added`, `policy.updated`, `event.policy-denied`, and the key changes in section 4. For a denial, the change includes the Room event and sequence. Its audit metadata is only `room_id`, `seq`, and `event_id`. Remote admin records name the authenticated lead. Bootstrap has a null actor. An identical current policy or repeated denial post writes no second record.

The named object is the one writer. Its synchronous callback runs without another request interleaving, and a failed callback rolls back both the action and the record (to confirm, F2 through F4). It is the same read, seal, and commit boundary as the Go hub's file lock and atomic rename. Handlers can interleave across `await` (to confirm, F4). Prepare signatures outside the transaction, then recheck the lead, project, roster, policy version, and signing key before committing. If the key changed, prepare a new signature. Use synchronous SHA-256 for the audit seal (to confirm, F10). Send success after the durable commit (to confirm, F4).

One object per Room would spread load, but a Room denial and the team audit tip would then have separate writers. A single JSON snapshot would also preserve atomicity, but would rewrite history on every append. #82 defines the team routing, schema, and migrations. #84 through #86 own these writes. #81 races submissions and checks that sequence numbers and audit links form one chain each.

## 2. Room live transport

Serve `GET /api/v1/rooms/{room}/stream?last_seq=N` on both hubs. Use the replay route's cursor rules. There is no request body. Authenticate before sending `200` with `application/x-ndjson` and `Cache-Control: no-store`. Send the current v1 event envelopes and blank heartbeat lines. Keep the Go POST stream for older daemons. Deploy the Go GET route before updating those daemons.

The Worker owns the response stream (to confirm, F5). It reads ascending pages from the team's object using short calls. Every read checks the member token again. Start at the requested cursor, advance it after writing each frame, and read `seq > cursor` for the next page. Drain the backlog before waiting. When caught up, send a blank line and check again in one second. Replay and live reads use the same committed rows, so a write during replay or an idle period will appear in the next read that reaches it. Stop on a write timeout, disconnect, or the stream budget in section 6. On revocation, send `unauthorized` and close.

In `spec/room`, add the HTTP binding for C2, C5, and C6. The initial cursor is in the GET query. Replacing a subscription or responding to `behind` opens a new GET. A `behind` frame ends delivery on that response. Keep the v1 resume envelope and WebSocket rules for their existing transports. The Go daemon uses `net/http` and drops the upload pipe. On EOF, timeout, or `behind`, reconnect from the highest cursor saved after delivery or a counted skip. Keep rule O4 to discard any sequence at or below the cursor. Reconnects neither skip committed events nor redeliver saved ones. A process crash between local delivery and cursor persistence may repeat that event.

Full-duplex POST keeps the current Go transport, but concurrent upload and download through the Cloudflare edge and Worker is unverified (to confirm, F6). WebSocket could carry the same envelopes and use Durable Object hibernation (to confirm, F7). It would require a standard-library RFC 6455 client in the daemon and a new server transport on both hubs. A download stream uses existing HTTP and replay logic. #81 specifies the live cases, #86 supplies ordered reads, #87 changes the Room spec, both servers, and the daemon, and #88 checks the edge behavior.

## 3. Admin without a data folder

Use the existing `lead` role for remote administration. `openplexer-hub init`, `add-member`, `remove-member`, `add-project`, `publish-policy`, and `export-audit` accept `--hub <origin>`. Read credentials from a private file or stdin. Require HTTPS off loopback and refuse redirects, as the daemon does. Keep the Go hub's existing local commands and `serve -data-dir`.

For `init`, the operator sets a random bootstrap bearer in a Worker secret (to confirm, F8). The admin CLI generates the first lead's base64url token from 32 random bytes, saves it in an owner-only file, and submits the lead ID and the hex SHA-256 of the token. Initialization creates the team, keys, and first lead together. With the bootstrap secret still set, a retry reads the same private token file. If the stored lead matches, it succeeds without writing anything. Other initialization requests for that team conflict. Remove the bootstrap secret after testing the lead token.

For `add-member`, generate the same kind of token in the Worker. Store its hash and return the token once in the authenticated `no-store` response. The CLI prints it or writes it to a private file. The lead gives it to the member through a private channel. Leave tokens out of logs and audit records. If the response is lost, remove the member and create a new token. Removing the last lead returns 409. To remove policy grants, publish a new bundle as in the Go hub.

Use the existing policy PUT and audit GET. The export CLI fetches the audit JWKS after the export and writes `export.json`, `checkpoint.jws`, and `jwks.json` with the existing private, no-overwrite file handling. Prior public keys remain available if rotation happens between those requests.

Deployment-only admin commands would require Cloudflare credentials for routine team changes. A separate admin role would duplicate the Go hub's lead role. #82 provides the bootstrap secret binding, #84 adds provisioning and member and project admin, #85 adds remote export, and #88 documents enrollment and revocation.

## 4. Keys

At initialization, generate distinct P-256 key pairs for policy and checkpoints. Store public JWKs and wrap each private PKCS#8 key with AES-256-GCM and a fresh nonce before writing it to SQLite. Bind the ciphertext to the team, purpose, and `kid` as authenticated data. The wrapping key lives in a separate versioned Worker secret, apart from the bootstrap secret (to confirm, F8). Use Worker Web Crypto for randomness, wrapping, and ECDSA with SHA-256 (to confirm, F9). Use the [canonical JSON](../spec/canonical-json/README.md) bytes and the existing [policy](../spec/policy/README.md#the-signed-bundle) and [checkpoint](../spec/audit/README.md#checkpoint-format-and-key) JWS formats, including raw 64-byte `r || s` and the Go `kid` derivation. Checkpoint IDs have the `audit-` prefix.

A lead stages one new key per purpose and then activates it. Staging adds the public JWK immediately and records `key.staged`. Distribute the updated policy JWKS through the trusted channel and apply it with `openplexer configure --keys` before activation. Give audit reviewers the new audit JWKS the same way. Activation switches the signing pointer, clears the retired key's wrapped private material from active storage, and records `key.activated`. Keep every public JWK in its purpose's JWKS, including retired keys. Policy activation signs future versions. Publish a higher version to move a project's stored bundle to the new key. Checkpoint activation applies to the next export. To rotate the wrapping key, load the new secret alongside the old one, rewrap and verify the active and staged rows, then remove the old binding (to confirm, F8). For a signing key compromise, replace affected pins out of band and preserve earlier trusted checkpoints for comparison.

A reader of SQLite sees roster hashes, policies, Room text, claims, and the audit history. Random bearer tokens resist guessing from hashes. The reader needs the wrapping secret to recover the private keys. A reader of Worker secrets has that wrapping key but needs the ciphertext to sign. The bootstrap secret can initialize its configured team only while it is uninitialized. An attacker with storage and the wrapping secret can forge signatures for the corresponding trusted keys. Access to the running hub can expose both. An earlier checkpoint kept off the hub still anchors the prefix it covered.

Storing clear private keys in SQLite would make a data copy sufficient to forge signatures. Keeping every team key in deployment secrets would make rotation a deploy and split key changes from their audit records. #83 pins signing and sealing to fixtures, #84 implements policy keys, #85 adds checkpoint signing and rotation tests, and #88 covers trusted distribution and recovery.

## 5. HTTP contract

Keep the Go paths, methods, results, and validation. Reuse the existing v1 schemas so a daemon and an audit reviewer can use either hub. A Cloudflare-specific API version would require separate clients for the same operations. #81 tests the shared routes over HTTP. #84 through #87 supply the corresponding handlers.

| Go hub method and path | Cloudflare counterpart and contract |
|---|---|
| `GET /.well-known/jwks.json` | Same. Public policy JWKS with all policy keys. |
| `GET /.well-known/audit-jwks.json` | Same. Public checkpoint JWKS with all checkpoint keys. |
| `GET /api/v1/audit/export` | Same. Lead only. Full export newest first, `next_cursor: null`, signed `OpenPlexer-Audit-Checkpoint` header. |
| `GET /api/v1/projects/{project}` | Same. Return `id` and `repo`, or 404. |
| `GET /api/v1/projects/{project}/policy.bundle` | Same. Latest signed bundle or 404. |
| `PUT /api/v1/projects/{project}/policy` | Same. Lead only. Submitted and canonical bytes each limited to 1 MiB. Oversize request 413, invalid or oversized canonical bundle 400, missing project 404, conflicting version 409. Identical current bundle returns its original signature. |
| `GET /api/v1/rooms/{room}/events` | Same. `last_seq` is a nonnegative safe integer that defaults to 0. A malformed query or repeated or invalid cursor returns 400. Up to 100 ascending envelopes after the cursor. An ahead cursor returns `[]`. |
| `POST /api/v1/rooms/{room}/events` | Same. 64 KiB body and 128-byte idempotency key. Validate the case-sensitive request before deduplication. Reserved types or a `human` post with an agent return 403. Return the event envelope. |
| `POST /api/v1/rooms/{room}/claims` | Same. 64 KiB. Return `granted`, `conflicts`, and the `event` envelope. Compare across projects with the same repo. Conflicts are advisory. Default TTL is 3,600 seconds, or 600 for implicit claims. |
| `POST /api/v1/rooms/{room}/claims/{claim}/renew` | Same. 64 KiB. Only the owning member and session in that Room can renew. An unavailable claim returns 404. Return the updated lease without an event. |
| `POST /api/v1/rooms/{room}/claims/{claim}/release` | Same. 64 KiB and the same ownership check. Append a release and return the lease. `expired` is reserved and returns 400. |
| `POST /api/v1/rooms/{room}/stream` | Use `GET /api/v1/rooms/{room}/stream?last_seq=N` on both hubs in #87. Cloudflare returns 405 for POST. GET adds 400 for malformed cursors and returns the NDJSON response in section 2. |
| `init -data-dir` (local command) | `POST /api/v1/admin/init` with `{member_id, token_hash}`. Bootstrap bearer, 409 for a different initialization after success. |
| `add-member -data-dir` (local command) | `POST /api/v1/admin/members` with `{id, role}`. Lead only. Returns `{token}`, or 409 for an existing ID. |
| `remove-member -data-dir` (local command) | `DELETE /api/v1/admin/members/{member}`. Lead only. 404 if missing, 409 for the last lead. |
| `add-project -data-dir` (local command) | `POST /api/v1/admin/projects` with `{id, repo}`. Lead only. 409 for an existing ID. |
| No Go route | `POST /api/v1/admin/keys/{purpose}` stages or returns a pending public JWK. `PUT /api/v1/admin/keys/{purpose}/active` with `{kid}` activates it. Both require a lead. Unknown purpose or key is 404. An already active key is a successful retry. A retired key conflicts. |

JSON successes are 200. New admin operations with no result return `{}`. New admin JSON bodies have a 64 KiB limit. Match the Go error envelope `{"error":"..."}`, `Cache-Control: no-store`, and `WWW-Authenticate: Bearer` on 401. Preserve 400 for body reads that fail, 401 for invalid or revoked credentials, 403 for a role refusal, 404 for an unknown project or Room, 413 for an oversized body, and 500 for unavailable hub state. As in Go, check bearer syntax before reading a capped body and check roster membership after the read. Match implicit HEAD on the existing GET routes and 405 with `Allow` for wrong methods. In an existing Room, a claim request sweeps expired leases before the requested action. Commit the sweep even if that action fails validation or cannot find its lease. Retain Go's claim release order by claim ID and renewal behavior. [Go hub API](hub.md#http-api) and [Room rules](../spec/room/README.md#claims-and-expiry).

## 6. Limits and cost

Keep the hub's current limits and put a budget on each stream. This keeps memory and call counts bounded even if the client or a replay is slow. An unbounded stream would keep spending CPU and calls for a single connection. Polling from the Worker also avoids holding a request open on the Durable Object.

| Limit | Planning assumption and fit |
|---|---|
| Worker request body | Budget against 100 MB on the smallest applicable Cloudflare account plan (to confirm, F11). Enforce the hub's 1 MiB policy and 64 KiB Room and admin caps while reading. |
| CPU and memory | Budget for 30 seconds of CPU per Worker request and Durable Object invocation, and 128 MB per Worker isolate (to confirm, F12, F13, and F16). In a short read transaction, capture the audit count, tip, and checkpoint key. Sign that snapshot and stream immutable pages from the object through that count in descending order. Keep public replay at 100 events and bound internal chunks by bytes. #85 checks the maximum practical full export. |
| Calls and stream duration | Budget for at least 400 Durable Object calls per Worker request (to confirm, F14). Budget against no fixed HTTP wall-time cap while connected (to confirm, F15). Close and resume after at most five minutes or 400 calls, whichever comes first. Probe actual flush, disconnect, and timeout behavior in #88. |
| SQLite | Budget against a 10 GB database cap and a 2 MB row or bound-value cap (to confirm, F17). Bind payloads as values. A maximal policy fits because the row stores only one copy of the payload. Watch total size and alert before capacity is exhausted. Keep history in Phase 5. |

For 5 to 10 people, assume one daemon each for 8 hours on 22 days per month, two policy requests per 15 seconds, a fresh stream every five minutes, and 100 posts per person per day. One Durable Object poll per second per stream gives about 0.44 to 0.89 million public requests and about 3.6 to 7.3 million Durable Object calls including policy requests and posts. Treat reconnect bursts and exports as additional usage.

Estimate **about $5.40 to $6 per month** before tax and domain fees. The model uses Workers Paid at $5 with 10 million Worker requests and 30 million CPU ms included (to confirm, F18). Count each poll as one Durable Object request, with 1 million included and $0.15 per additional million (to confirm, F19). At 128 MB billed per object, a full 31-day month is under 343,000 GB-s, within an assumed 400,000 GB-s inclusion (to confirm, F20). Assume total storage below 1 GB and SQL rows read and written within the included quotas (to confirm, F21). #82 checks quota fit, #85 and #87 bound export and stream work, and #88 measures real usage and revises this estimate.

## 7. Testing

Build one black-box Go suite in `test/hubconformance` that takes a hub URL plus lead and member credentials from private test files. A setup step uses local Go commands or Worker bootstrap to seed an isolated team. The common assertions use HTTP only. Cover auth and revocation, exact limits, signed policies and their version floor, Room replay and retries, claims, concurrent writes, and audit export checked by the unchanged `openplexer-verify`. Run live cases once #87 implements the shared GET route. Run Worker-only admin and key rotation cases alongside the shared suite.

In CI, launch the Go binary on loopback. Starting in #82, also run the pinned Wrangler local runtime with fresh SQLite state and test secrets (to confirm, F22). #83 runs the committed canonical JSON, policy, and audit fixtures in TypeScript. #86 runs the Room fixtures. Fixture tests catch byte and validation differences. URL tests exercise the HTTP and storage boundaries that mocks would skip. #88 runs the same suite against a disposable real HTTPS team and checks custom domain routing, secret bindings and persistence over redeploys, edge flush and disconnect behavior, and actual usage and limits. Local CI needs no Cloudflare account (to confirm, F22).

## 8. Operator choices

| Choice | Recommended default and reason |
|---|---|
| Who can recover admin access? | Keep a second named lead and protect both tokens separately. A single lost token otherwise requires operator recovery. |
| When should keys rotate? | Schedule a manual stage, re-pin, and activate window. Rotate on suspected compromise. Automatic activation risks leaving daemons on old pins. |
| How should the team retain evidence and recover state? | Export checkpoints off-hub daily and take a protected backup that can restore team identity, sequence counters, and wrapped keys. Keep the wrapping key separately. Test restore before depending on it. |
| When should the operator expand capacity? | Alert at 70% of the confirmed SQLite cap and plan a migration before it fills. Pruning history would break replay or audit evidence. |

Hard-coding a calendar or recovery service would require the hub to choose each team's trust and retention practices. #84 and #85 supply the admin and export tools. #88 records the chosen domain, account, lead contacts, key distribution, backup procedure, and usage alerts.

## Facts to confirm

Verify these Cloudflare assumptions before merging this design.

| Fact | Where to check |
|---|---|
| F1. Workers can serve a configured HTTPS team hostname (to confirm). | [Custom Domains](https://developers.cloudflare.com/workers/configuration/routing/custom-domains/) |
| F2. A namespace binding routes a stable name to a single active Durable Object for that identity (to confirm). | [Namespace API](https://developers.cloudflare.com/durable-objects/api/namespace/) and [What are Durable Objects?](https://developers.cloudflare.com/durable-objects/concepts/what-are-durable-objects/) |
| F3. SQLite-backed objects expose SQL and `transactionSync` with atomic commit and rollback on throw (to confirm). | [SQLite storage API](https://developers.cloudflare.com/durable-objects/api/sqlite-storage-api/) |
| F4. Synchronous work runs without interleaving. Non-storage awaits can interleave. Output gates hold replies until writes commit (to confirm). | [Rules of Durable Objects](https://developers.cloudflare.com/durable-objects/best-practices/rules-of-durable-objects/) |
| F5. Worker response streams can flush NDJSON incrementally, wait between polls, and clean up on disconnect (to confirm). | [Streams](https://developers.cloudflare.com/workers/runtime-apis/streams/) and [Request](https://developers.cloudflare.com/workers/runtime-apis/request/) |
| F6. Whether open request bodies pass through the edge to the Worker and the object while the response is sent (to confirm). | [Request](https://developers.cloudflare.com/workers/runtime-apis/request/) and [Streams](https://developers.cloudflare.com/workers/runtime-apis/streams/) |
| F7. WebSocket upgrade and Durable Object hibernation support for an idle live connection (to confirm). | [Use WebSockets](https://developers.cloudflare.com/durable-objects/best-practices/websockets/) |
| F8. Worker secrets are private bindings and operators can replace or remove them (to confirm). | [Secrets](https://developers.cloudflare.com/workers/configuration/secrets/) |
| F9. Web Crypto supports secure random bytes, P-256 key generation and PKCS#8, raw ES256 signatures, and AES-GCM (to confirm). | [Web Crypto](https://developers.cloudflare.com/workers/runtime-apis/web-crypto/) |
| F10. `node:crypto` offers synchronous SHA-256 with the compatibility flag (to confirm). | [Node.js crypto](https://developers.cloudflare.com/workers/runtime-apis/nodejs/crypto/) |
| F11. The lowest relevant edge request body cap is 100 MB and depends on the account plan (to confirm). | [Workers limits](https://developers.cloudflare.com/workers/platform/limits/) |
| F12. Workers Paid has a 30-second default CPU budget per request (to confirm). | [Workers limits](https://developers.cloudflare.com/workers/platform/limits/) |
| F13. Worker memory limit is 128 MB per isolate (to confirm). | [Workers limits](https://developers.cloudflare.com/workers/platform/limits/) |
| F14. Workers Paid permits 400 or more calls to a Durable Object during one request (to confirm). | [Workers limits](https://developers.cloudflare.com/workers/platform/limits/) |
| F15. A connected HTTP response stream has no fixed wall-time cap (to confirm). | [Workers limits](https://developers.cloudflare.com/workers/platform/limits/) |
| F16. Durable Object CPU budget defaults to 30 seconds per invocation (to confirm). | [Durable Object limits](https://developers.cloudflare.com/durable-objects/platform/limits/) |
| F17. The SQLite database cap is 10 GB per object and the row and bound-value caps are 2 MB (to confirm). | [Durable Object limits](https://developers.cloudflare.com/durable-objects/platform/limits/) |
| F18. Workers Paid costs $5 monthly with 10 million Worker requests and 30 million CPU ms included (to confirm). | [Workers pricing](https://developers.cloudflare.com/workers/platform/pricing/) |
| F19. A Worker-to-object call counts as one Durable Object request with 1 million monthly requests included and $0.15 per million above that (to confirm). | [Durable Object pricing](https://developers.cloudflare.com/durable-objects/platform/pricing/) |
| F20. Durable Object duration uses active wall time and 128 MB per object, with 400,000 GB-s included monthly (to confirm). | [Durable Object pricing](https://developers.cloudflare.com/durable-objects/platform/pricing/) |
| F21. The storage allowance is 5 GB-month, with 25 billion SQL rows read and 50 million rows written included per month (to confirm). | [Durable Object pricing](https://developers.cloudflare.com/durable-objects/platform/pricing/) |
| F22. Wrangler local mode can serve a Worker with SQLite Durable Objects, bindings, and local test secrets without an account (to confirm). | [Development and testing](https://developers.cloudflare.com/workers/development-testing/) and [Get started with Durable Objects](https://developers.cloudflare.com/durable-objects/get-started/) |

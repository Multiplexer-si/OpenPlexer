# Hub conformance tests

The suite exercises the shared hub API over HTTP. The Cloudflare hub design defines the contract. [HTTP contract](../../docs/cloudflare-hub-design.md#5-http-contract)

The tests cover bearer authentication and revocation, lead permissions, public signing keys, and signed policy reads and writes. They check policy version conflicts, retry signatures, and exact body and canonical bundle limits. Room tests cover event validation, replay, pagination, idempotency, claim ownership, conflicts across projects, and expiry. The suite races Room posts and policy publication, then checks for gaps in the Room and audit histories. Audit exports and their signed checkpoints pass through the same verifier as `openplexer-verify`.

## Run against the Go hub

Run this command from the repository root.

```sh
go test -count=1 ./test/hubconformance
```

`make check` runs the suite in CI. The suite builds `openplexer-hub` and seeds a temporary data directory with its local admin commands. It starts the binary on an OS-assigned loopback port and reports bind failures. To use a prebuilt hub, set `OPENPLEXER_BIN_DIR` to the absolute path of the directory containing `openplexer-hub`.

## Run against a seeded hub

Use a disposable team and seed it before running the suite. The suite writes policies, events, claims, and audit records. Start with fresh state for each run and keep other writers off the team during the run.

- Create one active lead and one active plain member. Save their bearer tokens in separate owner-only files. Each file holds one unpadded base64url token.
- Create projects `conformance` and `conformance-other` with the same repository identifier. Both Rooms start with no events or claims.
- Publish version 1 of a valid policy for `conformance`. Its `project_id` and sole `repos` entry must match the project and its repository identifier. Leave `conformance-other` without a policy. [Minimal policy bundle](../../spec/policy/v1/examples/bundle/minimal.json)
- For revocation checks, issue and revoke a separate member token during seeding. Keep the old token in an owner-only file and pass `-revoked-token-file`. Without that flag, the revocation cases skip.

Use an HTTPS origin for a remote hub. The client refuses redirects.

```sh
go test -count=1 ./test/hubconformance -args \
  -hub-url https://hub.example.test \
  -lead-token-file /path/to/lead.token \
  -member-token-file /path/to/member.token \
  -revoked-token-file /path/to/revoked.token
```

If the fixture uses other project IDs, pass `-project` and `-second-project` after `-args`.

When testing against `wrangler dev`, pass `-hub-url` and `-wrangler-dev` after `-args`. The suite rejects `-wrangler-dev` without `-hub-url` before it starts a hub.
The race checks the audit chain and its earlier checkpoint on both the Go hub and Wrangler. The Worker's `scripts/conformance.sh` then runs `openplexer-verify` on an export that includes Room denials and claim records.

For a deployed Cloudflare Worker, add `-cloudflare-edge` after `-args` with `-hub-url`. The Worker's `scripts/remote-check.sh` sets the flag. Use one of `-cloudflare-edge` or `-wrangler-dev` per run.

## Checks by target

On every target, `auth-and-body-order` requires 413 for a well-formed unknown bearer with an oversized body.

The suite checks a single `Authorization` field with a 43-byte comma bearer.

| Target | Suite flags | Request body | Required status |
| --- | --- | --- | --- |
| Built-in Go hub | No `-hub-url` | Oversized | 413 |
| Local Wrangler | `-hub-url` and `-wrangler-dev` | Small | 401 |
| External hub of unknown kind | `-hub-url` without `-wrangler-dev` | Oversized | 401 or 413 |

The suite checks the full error envelope for each accepted status. The built-in Go hub and external hubs also require 401 for a small body with the same bearer.

The suite uses oversized bodies for the 401 checks with a short bearer, Basic auth, multiple `Authorization` fields, and a short comma bearer. With `-wrangler-dev`, it uses small bodies for those checks because Wrangler dev can truncate a 401 while a large upload is still in flight. The Workers-pool tests verify the full 401 response to oversized uploads.

Cloudflare's edge can reject multiple `Authorization` fields before the Worker runs. With `-cloudflare-edge`, the duplicate-field checks also accept Cloudflare's HTML 400 error page. A single field, including Basic auth or a comma bearer, still follows the usual external-hub checks.

The built-in Go hub and `-wrangler-dev` each require the exact escaped-path status listed for that target. An external hub without `-wrangler-dev` can return either listed status because its backend is unknown. All other escaped-path cases require the same status on every target.

| Project ID in the request path | Built-in Go hub | `-wrangler-dev` |
| --- | --- | --- |
| `%FF` with a member bearer | 404 | 400 |
| `.` or `..` | 307 | 404 |
| `%2E` or `.%2e` | 401 | 404 |

## Live streams

Select the section with `go test -count=1 ./test/hubconformance -run '^TestHub$/^live-stream$'`. For a seeded hub, add the flags above after `-args`.

Every target must reject invalid credentials and cursors before starting the GET stream. The suite checks the response headers, ordered replay across page boundaries, a write during replay, blank heartbeats, a live write, and a missed event after reconnect. An ahead cursor starts with a heartbeat. The built-in Go hub must still return a legacy POST stream. Wrangler dev must return 405 for POST. An external hub may use either POST behavior.

The Wrangler run uses requests without upload bodies and checks the same GET frames as the Go run. The suite checks the frames that arrive over local HTTP. Flush latency, disconnect propagation, and write timeouts over the Cloudflare edge are tracked in [issue #88](https://github.com/Multiplexer-si/OpenPlexer/issues/88).

## Coverage limits

- Admin routes, key rotation, and storage fault injection are outside the shared suite.
- Expiry uses a one-second lease. The expiry cases skip if an external hub rejects that TTL.
- Every wrong-method check requires 405 and the expected `Allow` methods. The response must have a JSON error envelope and `Cache-Control: no-store`, or exactly the Go router's `Method Not Allowed\n` body with `Content-Type: text/plain; charset=utf-8` and no `Cache-Control` header.

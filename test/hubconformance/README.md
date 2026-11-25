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

When testing against `wrangler dev`, pass `-wrangler-dev` after `-args`. The `auth-and-body-order` cases send small bodies for requests the Worker rejects before reading the body. Wrangler dev can truncate the 401 response while a large upload is still in flight. The well-formed unknown token still gets an oversized body because the Worker reads through the first byte over the limit before returning 413. The Workers-pool tests verify the full 401 response to oversized uploads.

## Coverage limits

- Live streaming waits for [issue #87](https://github.com/Multiplexer-si/OpenPlexer/issues/87).
- Admin routes, key rotation, and storage fault injection are outside the shared suite.
- Expiry uses a one-second lease. The expiry cases skip if an external hub rejects that TTL.
- Every wrong-method check requires 405 and the expected `Allow` methods. The response must have a JSON error envelope and `Cache-Control: no-store`, or exactly the Go router's `Method Not Allowed\n` body with `Content-Type: text/plain; charset=utf-8` and no `Cache-Control` header.

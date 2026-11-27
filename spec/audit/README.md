# spec/audit

This contract defines the audit record, its hash chain, the JSON export, and the signed checkpoint. The contract is at version 1. [Checkpoint verification](#checkpoints) adds an offline check against a kept copy of a signed tip.

The field descriptions are in the schemas. This page gives the rules that a schema cannot express.

## Files

| File | Content |
|---|---|
| `v1/record.schema.json` | One record of the chain. |
| `v1/export.schema.json` | The JSON export of a log. |
| `v1/verify-result.schema.json` | The result of a chain verification. |
| `v1/checkpoint-payload.schema.json` | The signed fields of a checkpoint. |
| `v1/checkpoint-protected-header.schema.json` | The protected header of a checkpoint. |
| `v1/checkpoint-verify-result.schema.json` | The result of export verification with zero or more checkpoints. |
| `v1/examples/record/` | The first three records of one chain. |
| `v1/examples/export/` | The export of those three records. |
| `v1/examples/verify-result/` | The result for those three records, and the result after a change to the second record. |
| `v1/fixtures/verify-chain.json` | One valid chain of six records, an empty chain, and thirteen altered copies of the valid chain, each with its verification result. |
| `v1/fixtures/checkpoint.json` | Valid and invalid JSON exports and checkpoints. Includes a sealed chain for an approved enrollment and a separate revoked invite. Each failure names the rule it breaks. |

## Invite records

The hub seals `invite.created`, `invite.requested`, `invite.approved`, `invite.redeemed`, and `invite.revoked` once per transition. The target is `invite:<id>`. The metadata gives `project_id`, `member_id`, and `hub_role`.

Admin actions name the lead, or use a null actor for local data-folder commands. Request and redeem actions name the bound member ID. That ID identifies the claim. A redemption also seals `member.added` with a null actor and `role` and `invite_id` in metadata. Expiry adds no record. Keep the invite code, link, bearer, and their hashes out of every record.

## The hash of a record

**A1. Record shape.** A record must satisfy `v1/record.schema.json`. An export entry must satisfy the entry schema in `v1/export.schema.json`. Both accept unknown fields. Reject a repeated member name anywhere in an export entry, including inside `meta` and unknown fields. Report the record's chain position and its `id` if the record has a string `id`.

**A2. Hash input.**
Nine fields of a record are hashed: `workspace_id`, `actor_member_id`, `agent_session_id`, `action`, `target`, `reason`, `meta`, `flagged`, and `created_at`. The fields `id`, `prev_hash`, and `hash` are not.

Build the hash input in three steps.

1. Make an object with the nine hashed fields. A field with the value `null` stays in the object.
2. Write the object as [canonical JSON](../canonical-json/README.md).
3. Put the `prev_hash` of the record in front of that text. `prev_hash` is used as its 64 hexadecimal characters, not as 32 bytes.

The names of the nine fields are part of the hashed text, so no field can be renamed in version 1. `workspace_id` identifies the team. A single-team hub uses one stable ID for its team in records and checkpoints.

`created_at` is hashed as the exact string in the record. The reference writes UTC with milliseconds, such as `2026-01-15T12:00:00.000Z`. The same instant in another form gives another hash.

**A3. Record hash.** Take SHA-256 of the UTF-8 bytes of the A2 input. The `hash` must equal the result written as 64 lowercase hexadecimal characters.

**A4. Genesis.** The `prev_hash` of the first record must be 64 zeros.

**A5. Linkage.** The `prev_hash` of every later record must equal the `hash` of the preceding record.

### Worked example

The record in `v1/examples/record/first.json` is the first record of a chain. Its hash is the SHA-256 of this text, which is one line with no line break at the end.

```
0000000000000000000000000000000000000000000000000000000000000000{"action":"policy.updated","actor_member_id":"mem-lee","agent_session_id":null,"created_at":"2026-01-15T11:58:00.000Z","flagged":false,"meta":{"version":3},"reason":null,"target":"prj-shop","workspace_id":"wsp-shop"}
```

The hash is `06002a7506ef34b85b14456c74abeb16ee728ec31090e57bebd7e49dd7288199`.

## Chain verification

Verification takes the records of one log in chain order, oldest first.

1. Set the expected previous hash to 64 zeros.
2. For each record in order, compute the hash of the record from the expected previous hash and the nine hashed fields. If `prev_hash` of the record differs from the expected previous hash, or `hash` of the record differs from the computed hash, stop. The result is `{"ok": false, "broken_at": <id of the record>}`.
3. Otherwise set the expected previous hash to `hash` of the record, and continue.
4. After the last record, the result is `{"ok": true, "entries": <number of records>}`.

A log with no records is valid, with `entries` equal to 0.

## The export

**A6. Export shape and order.** The JSON export is one UTF-8 JSON object with an `entries` array and a null `next_cursor`. Read its values under the [canonical JSON input rules](../canonical-json/README.md#the-input), with a stricter rule for object member names. Compare decoded member names case-sensitively without Unicode normalization. Reject repeated member names at any depth. Report A6 for a repeat outside the entries, and apply A1 within each entry. An export nested deeper than 10,000 levels of arrays and objects is rejected as malformed under A6, before any entry is read.

Rejecting repeats prevents a reader from displaying one value while the verifier hashes another. The reference writes exports with `JSON.stringify`, which emits each member name once per object. A2 and A3 define the hash computation.

`entries` lists the records newest first. The verifier reverses the array once and checks it from genesis. Chain positions are 1-based after reversal. The verifier uses array positions and never sorts by `id` or by time.

A nonempty filtered export that omits genesis fails A4. A gap after genesis fails A5 at the next included record. The export carries no filter marker. An empty export or a contiguous prefix can pass the chain checks. A later checkpoint detects a missing suffix under K9.

**A7. Export display fields.** Each entry has the twelve fields of a record and `result`, and it can also have `agent_member_id`. `result` is the part of `action` after its last `.` or `-`, or the whole `action` if it has neither character. `agent_member_id` is the member that owns the agent session, or null. Neither field is hashed. A verifier uses `action` to compute any `result` it displays.

## What a chain cannot show

A chain shows that the records in it agree with each other. It cannot show these things.

- That records are missing from the end. A chain with its last records removed verifies.
- That the log was rewritten. Anyone who can replace a record and every record after it produces a chain that verifies.
- That an `id` is the original one. The hash does not cover `id`.
- That `result` or `agent_member_id` of an export entry is the original one. The hash covers neither field. A verifier does not trust `result`. It computes `result` from `action`, which is hashed. A verifier cannot compute `agent_member_id`, because the hub reads it from its list of agent sessions when it writes the export.
- That an unknown field of a record or of an export entry is the original one. The hash covers only the nine hashed fields.

## Checkpoints

A checkpoint is an ES256 compact JWS over a workspace ID, record count, tip hash, and issue time. To check an export offline, the verifier takes the export, a trusted checkpoint JWKS, and a list of zero or more checkpoint tokens. With zero checkpoints the verifier needs no keys. The rules use the input order of the checkpoints.

### Checkpoint format and key

**K1. Compact JWS.** Split the token at `.` into exactly three nonempty segments. Each segment is unpadded base64url. Use only `A-Z`, `a-z`, `0-9`, `-`, and `_`, with zero unused bits in the last character. Reject whitespace, padding, and invalid encodings. The signing input is the ASCII bytes of the first segment, `.`, and the second segment exactly as received.

**K2. Protected header.** Decode the first segment as UTF-8 [canonical JSON](../canonical-json/README.md) and validate it against `v1/checkpoint-protected-header.schema.json`. The header has exactly `alg`, `kid`, and `typ`. `alg` is `ES256`, `kid` is a nonempty string, and `typ` is `openplexer-audit-checkpoint+jws`. A policy bundle requires `mp-policy+jws`. Each verifier rejects the other token type.

**K3. Trusted key.** Use the hub's checkpoint JWKS. The caller must obtain it from an authenticated source or a trusted kept copy. A key set shipped with an export needs independent authentication. Read the JWKS bytes as UTF-8 JSON and compare decoded member names with case and no Unicode normalization. Reject invalid JSON, a repeated member name in any object in the JWKS, or a value that fails the [policy JWKS schema](../policy/v1/jwks.schema.json).

Find exactly one key whose `kid` equals the header's `kid`. Compare the strings as written, with case and no Unicode normalization. The key must be public, with `kty` equal to `EC`, `crv` equal to `P-256`, and no `d`. Its `x` and `y` are 32-byte unsigned big-endian coordinates in the base64url form of K1 and must give a point on P-256. If present, `alg` must be `ES256`, `use` must be `sig`, and `key_ops` must be `["verify"]`.

**K4. Signature.** Verify the SHA-256 digest of the K1 signing input with ECDSA on P-256 under the K3 key. The decoded signature is exactly 64 bytes. The first 32 are `r` and the last 32 are `s`, both unsigned big-endian integers.

**K5. Checkpoint version.** After verifying the signature, read the second segment as a canonical JSON object under K6. The object must have `v` equal to the number `1`. Reject missing versions and any other value.

**K6. Payload shape.** The second segment decodes to the UTF-8 bytes of [canonical JSON](../canonical-json/README.md) for one object. As with K2, compare the decoded bytes with the canonical encoding. This rejects a byte order mark, repeated keys, and other encodings. After K5, validate the object against `v1/checkpoint-payload.schema.json`. `entries` is the record count. `tip_hash` is 64 lowercase hexadecimal characters. `issued_at` is a UTC timestamp of the form `YYYY-MM-DDTHH:MM:SS.sssZ`. The date must exist in the Gregorian calendar, with a year from 0001 through 9999. Seconds range from 00 to 59. The time is the hub's claim about when it signed and plays no part in ordering or freshness checks.

### Verification and result

Check A6 first. Walk the reversed array and check A1, then A4 or A5, then A3 for each record. Stop at the first failure. For each checkpoint in input order, check K1 through K4, then read the canonical payload object (K6), check its version (K5), check its remaining fields (K6), and check its workspace (K7). After those checks pass for all tokens, check K8 for the whole list. Then check K9 and K10 for each token in input order.

**K7. Workspace.** The checkpoint's `workspace_id` must equal the `workspace_id` of every record in the export. Compare the strings with case and no Unicode normalization. With an empty export there are no records to compare.

**K8. Contradiction.** Reject two authenticated checkpoints that name the same `workspace_id` and `entries` and have different `tip_hash` values. Check this before comparing either hash with the export. Copies of the same claim can appear more than once.

**K9. Count.** A checkpoint's `entries` must be at most the number of exported records.

**K10. Tip.** For a positive `entries`, `tip_hash` must equal the `hash` of record number `entries` in chain order. If `entries` is zero, `tip_hash` must equal the 64-zero genesis hash.

**K11. Result.** Use `v1/checkpoint-verify-result.schema.json`. On success report `ok: true`, `entries` equal to the export's record count, and the following fields.

| Checkpoints supplied | `checkpoint_entries` | `completeness` |
|---|---|---|
| None | `null` | `not_proven` |
| One or more | The greatest checkpoint `entries`, including zero | `through_checkpoint` |

On failure report `ok: false` and `rule` equal to the failed rule ID. For A1, A3, A4, and A5, include `record_number` in chain order and `broken_at` if the record has a string `id`. For K1 through K10, include `checkpoint`, the 1-based position of the failing token in the input. For K8 report the first token in input order that contradicts an earlier token. For K7 include the location of the first mismatched record. For K10 include the location of the compared record when `entries` is positive.

A match proves that the export contains the history through the greatest supplied checkpoint count. Any records after that count have only passed the chain checks. The verifier cannot tell whether the hub later wrote more records, skipped an event, or issued a checkpoint that the caller never received. A checkpoint for an empty log authenticates a count of zero.

### Distribution and retention

**K12. Publication.** The hub uses an ES256 key dedicated to checkpoints, with its own `kid` distinct from every policy signing key ID. It keeps publishing every checkpoint public key it has used. The hub pushes each checkpoint to connected daemons. A daemon keeps every checkpoint it receives without overwriting earlier copies. A keeper stores the checkpoint JWKS next to the checkpoints for offline verification.

The hub can sign a checkpoint for a rewritten chain. The kept copy lets a verifier compare that chain with a claim the hub made earlier. The verifier trusts its copy of the JWKS and its source. The signature gives no independent timestamp. [Trust model](../../docs/trust-model.md#how-a-verifier-obtains-a-trusted-checkpoint).

## Compatibility

**A8. Version.** The `v1` directory names the contract version. Records and exports carry no version field. A checkpoint carries its own `v` under K5. [The general compatibility rule](../README.md#versions-and-compatibility) applies.

These parts must stay compatible with the reference, so that a log that it wrote verifies.

- The list of hashed fields and their names.
- The text that is hashed: `prev_hash` as hexadecimal text, then canonical JSON.
- The 64 zeros of the first record.
- The field names of an export entry.

These parts may change: the storage of the log, the routes that serve it, and any export format other than JSON.

`meta` accepts any fields, and all of them are hashed. A record and an export entry accept unknown fields, and an unknown field is not hashed.

### Room denials

The hub writes an `event.policy-denied` record that links to the Room event through `meta.room_id`, `meta.seq`, and `meta.event_id`. The hub supplies `created_at` from its receipt clock, using the Room event's `at`. When the authenticated member supplies `body.decided_at`, the hub copies the member's claimed decision time to `meta.decided_at` as sent. Older hooks can omit it. [Room denial time](../room/README.md#denial-time).

The v1 hash covers both times. A trusted checkpoint proves those recorded values match the hub's signed history through its count. Establishing when the denial happened or whether either clock was accurate requires independent evidence. The `verify-chain` fixture covers a queued denial and an edit to its decision time.

## Generated and authored parts

The reference is at commit `7d80045a`.

| Part | Source |
|---|---|
| `record`, `export`, `verify-result` | Generated from the schemas in `packages/protocol/src/observability.ts`. `record` is the export entry without `result` and `agent_member_id`. |
| Every `description` | Authored. |
| The hash rule on this page | Authored from `apps/hub/src/audit/log.ts`. |
| The verification steps on this page | Authored from `apps/hub/src/audit/verify.ts`. |
| The hashes in the examples | Computed by the `computeAuditHash` function of the reference. |
| `result` in the export example | Computed by the extractor. The reference computes it in SQL. |
| The `verify-result` examples and `verify-chain` fixtures | Computed by the loop described below. |
| The three `checkpoint-*` schemas and their examples | Authored for OpenPlexer. The reference has no checkpoint. |
| `v1/fixtures/checkpoint.json` | Generated by `go run ./tools/auditvectors` with fixed test keys. |

The verification function of the reference reads its records from a Postgres database, so the extractor cannot call it. The extractor runs the same loop over a list of records, and it calls the `computeAuditHash` function of the reference for every hash. The loop is in `tools/extract/audit.ts`.

Run the checkpoint generator from the repository root. Test each case with the original JSON bytes of `input.export`. Parsing and serializing that value can discard repeated members. The tests evaluate each committed case and compare the fixture file with fresh generator output. The checkpoint signing keys are public test keys and must stay in tests. [Generator](../../tools/auditvectors).

## Intentional differences from the reference

| Difference | Reason |
|---|---|
| Verification takes a list of records. The reference reads the rows of one team from its database, in ascending order of `id`. | A verifier outside the hub has an exported file and no database. |
| Verification hashes `created_at` as the exact string in the record. The reference reads `created_at` from its database as a time and writes it again as UTC with milliseconds before it hashes, so the form of the stored time cannot change its result. The fixture case `a changed created_at format breaks the chain at that record` therefore has no counterpart in the reference. | A verifier outside the hub has only the string. The JSON export of the reference always writes UTC with milliseconds, so the two agree on every export that the reference writes. |

## Open questions

Each item is behavior of the reference that looks unintended, or a choice that the owner has not made. Version 1 keeps the behavior of the reference until the owner decides.

1. **CSV verification stays out of scope.** The reference CSV export omits `workspace_id`, a field the hash requires. Version 1 covers the JSON export only.
2. **The hash does not cover `id`.** The fixture case `changed ids do not break the chain` pins the behavior.
3. **Numbers in `meta` follow the number rules of canonical JSON.** A number that a double cannot hold exactly is hashed as the rounded value. See the open questions of [canonical JSON](../canonical-json/README.md#open-questions).

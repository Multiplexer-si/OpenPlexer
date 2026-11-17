# spec/audit

This contract defines the audit record, the hash that links each record to the one before it, the verification of a chain, and the JSON export of a log. The contract is at version 1.

The field descriptions are in the schemas. This page gives the rules that a schema cannot express.

## Files

| File | Content |
|---|---|
| `v1/record.schema.json` | One record of the chain. |
| `v1/export.schema.json` | The JSON export of a log. |
| `v1/verify-result.schema.json` | The result of a chain verification. |
| `v1/examples/record/` | The first three records of one chain. |
| `v1/examples/export/` | The export of those three records. |
| `v1/examples/verify-result/` | The result for those three records, and the result after a change to the second record. |
| `v1/fixtures/verify-chain.json` | One valid chain of six records, an empty chain, and thirteen altered copies of the valid chain, each with its verification result. |

## The hash of a record

Nine fields of a record are hashed: `workspace_id`, `actor_member_id`, `agent_session_id`, `action`, `target`, `reason`, `meta`, `flagged`, and `created_at`. The fields `id`, `prev_hash`, and `hash` are not.

To compute the hash of a record, do these steps.

1. Make an object with the nine hashed fields. A field with the value `null` stays in the object.
2. Write the object as [canonical JSON](../canonical-json/README.md).
3. Put the `prev_hash` of the record in front of that text. `prev_hash` is used as its 64 hexadecimal characters, not as 32 bytes.
4. Take the SHA-256 of the UTF-8 bytes of the result, and write it in lowercase hexadecimal.

The `prev_hash` of the first record is 64 zeros. The `prev_hash` of every other record is the `hash` of the record before it.

The names of the nine fields are part of the hashed text, so no field can be renamed in version 1. `workspace_id` is the name that the reference uses for a team. The rest of the documentation says team.

`created_at` is hashed as the exact string in the record. The reference writes UTC with milliseconds, such as `2026-01-15T12:00:00.000Z`. The same instant in another form gives another hash.

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

The JSON export is one object with `entries` and `next_cursor`. `next_cursor` is null. `entries` lists the records newest first, so a verifier reverses the list before it verifies.

Each entry has the twelve fields of a record and `result`, and it can also have `agent_member_id`. `result` is the part of `action` after its last `.` or `-`, or the whole `action` if it has neither character. `agent_member_id` is the member that owns the agent session, or null. Neither field is hashed.

## What a chain cannot show

A chain shows that the records in it agree with each other. It cannot show these things.

- That records are missing from the end. A chain with its last records removed verifies.
- That the log was rewritten. Anyone who can replace a record and every record after it produces a chain that verifies.
- That an `id` is the original one. The hash does not cover `id`.
- That `result` or `agent_member_id` of an export entry is the original one. The hash covers neither field. A verifier does not trust `result`. It computes `result` from `action`, which is hashed. A verifier cannot compute `agent_member_id`, because the hub reads it from its list of agent sessions when it writes the export.
- That an unknown field of a record or of an export entry is the original one. The hash covers only the nine hashed fields.

To detect a shortened or rewritten log, a verifier needs a hash that it got from a source that it trusts, and compares it with the hash at the same position of the chain. The [trust model](../../docs/trust-model.md#how-a-verifier-obtains-a-trusted-checkpoint) recommends where that hash comes from, and [issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23) decides it.

## Compatibility

[The general rule](../README.md#versions-and-compatibility) applies. For this contract it means the following.

These parts must stay compatible with the reference, so that a log that it wrote verifies.

- The list of hashed fields and their names.
- The text that is hashed: `prev_hash` as hexadecimal text, then canonical JSON.
- The 64 zeros of the first record.
- The field names of an export entry.

These parts may change: the storage of the log, the routes that serve it, and any export format other than JSON.

`meta` accepts any fields, and all of them are hashed. A record and an export entry accept unknown fields, and an unknown field is not hashed.

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
| The `verify-result` examples | Computed by the loop that is described below. |
| Every expected value in the fixtures | Computed by the same loop. |

The verification function of the reference reads its records from a Postgres database, so the extractor cannot call it. The extractor runs the same loop over a list of records, and it calls the `computeAuditHash` function of the reference for every hash. The loop is in `tools/extract/audit.ts`.

## Intentional differences from the reference

| Difference | Reason |
|---|---|
| Verification takes a list of records. The reference reads the rows of one team from its database, in ascending order of `id`. | A verifier outside the hub has an exported file and no database. |
| Verification hashes `created_at` as the exact string in the record. The reference reads `created_at` from its database as a time and writes it again as UTC with milliseconds before it hashes, so the form of the stored time cannot change its result. The fixture case `a changed created_at format breaks the chain at that record` therefore has no counterpart in the reference. | A verifier outside the hub has only the string. The JSON export of the reference always writes UTC with milliseconds, so the two agree on every export that the reference writes. |

## Open questions

Each item is behavior of the reference that looks unintended, or a choice that the owner has not made. Version 1 keeps the behavior of the reference until the owner decides.

1. **The export lists the newest record first, and verification needs the oldest first.** Nothing in the export states the order.
2. **An export can be filtered,** for example by member, by action, or by time. A filtered export skips records, so it does not verify, and nothing in the export says that a filter was applied.
3. **The CSV export of the reference has no `workspace_id` column.** That field is hashed, so a CSV export cannot be verified without it. Version 1 covers the JSON export only.
4. **The hash does not cover `id`.** The fixture case `changed ids do not break the chain` pins the behavior.
5. **`workspace_id` is the name that the reference uses for a team.** The name is hashed, so version 1 keeps it.
6. **Numbers in `meta` follow the number rules of canonical JSON.** A number that a double cannot hold exactly is hashed as the rounded value. See the open questions of [canonical JSON](../canonical-json/README.md#open-questions).

# Audit compatibility with the TypeScript reference

The reference is pinned at `7d80045a`. OpenPlexer verifies its full JSON audit exports with a chain-only check. Pass a readable JWKS file such as one containing `{"keys":[]}` and omit `--checkpoint`.

| Input or behavior | OpenPlexer compatibility |
|---|---|
| Full JSON export | Compatible. The verifier reverses the newest-first `entries` array and checks the same nine hashed fields, genesis hash, and links. It requires a null `next_cursor`. |
| Phase 0 chain fixtures and examples | Both `VerifyExport` and the built `openplexer-verify` match the reference counts and first broken IDs. The tests wrap the bare records in the export shape and add `result` and `agent_member_id` without changing hashes. |
| Record order and IDs | The reference selects one workspace and sorts its database rows by ID. The offline verifier takes the order from the export array and reports the first failure in chain order. The hash leaves out the ID, so changing an ID alone does not break the chain. |
| `created_at` | The reference converts database timestamps to UTC with milliseconds for both hashing and export. OpenPlexer hashes the exported string as written. Reformatting the same instant breaks the hash under A3. |
| Display fields and unknown fields | The verifier accepts extra fields and unknown action strings. `result` must be a string. `agent_member_id` may be a string, null, or absent. Neither field nor any extra field on the record or export is hashed. All fields inside `meta` are hashed. |
| Malformed or ambiguous input | The reference loop reads typed database rows. The offline verifier checks the export shape first. For each record in chain order, it checks that record's field types, previous link, and hash before moving to the next record. It requires UTF-8 JSON and rejects duplicate member names, including inside unknown fields. These checks give file readers one interpretation of the data. |
| Reference CSV export | Unsupported. It omits the hashed `workspace_id`. Formula escaping can add a leading apostrophe to a value whose first non-whitespace, non-control character is `=`, `+`, `@`, or `-`. Nulls and empty strings also share an empty cell. Use the reference JSON export for verification. |
| Filtered JSON export | A nonempty export that skips genesis fails A4. A gap after genesis fails A5 at the next included record. An empty export or a contiguous prefix can pass without a checkpoint because the filter leaves no marker. |
| Paged JSON response | A non-null `next_cursor` fails A6 even when the included entries form a valid chain. A final page can have a null cursor and still omit records. The same genesis, gap, and prefix rules apply. |
| Signed checkpoints | The reference has no checkpoints. Chain-only verification cannot detect a missing suffix or a rewrite with recomputed hashes. OpenPlexer can check the workspace, count, and tip against a signed checkpoint and a trusted audit JWKS. A trusted checkpoint beyond the export detects truncation under K9. |

The reference verifier returns `ok` and a count or first broken ID. OpenPlexer also reports the rule and the position in chain order on record failures. Its JSON result reports how far the checkpoint proves completeness. [Audit contract](../spec/audit/README.md#verification-and-result).

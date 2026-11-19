# Export and verify an audit log

## Export from the hub

Run this command on the hub host from the directory that contains `bin/` and your hub data. Set `--data-dir` to the directory you used to initialize the hub. The command works while the server is running or stopped. [Hub setup](hub.md#start-the-server).

```sh
bin/openplexer-hub export-audit --data-dir ./hub-data --out ./audit-export
```

The command writes `export.json`, `checkpoint.jws`, and `jwks.json` from one snapshot. It leaves existing files in place if any of those names are taken. Use a new output directory for each snapshot you want to keep.

A lead can also call `GET /api/v1/audit/export` with a bearer token. Save the JSON body as `export.json`. Save the `OpenPlexer-Audit-Checkpoint` response header value as `checkpoint.jws`, with no trailing newline. The export holds the full chain, newest first, with `next_cursor` set to null. The public checkpoint keys are at `GET /.well-known/audit-jwks.json`. [Hub HTTP API](hub.md#http-api).

## Verify offline

<!-- audit-example: reviewer -->

On the review machine, put `openplexer-verify` on `PATH`. Copy `export.json` and `checkpoint.jws` into `audit-export/` in your working directory. Save a trusted checkpoint JWKS as `trusted-jwks.json` in the working directory. Get it from the hub over authenticated HTTPS or from a trusted operator. If the JWKS came with the export, authenticate it independently. Keep earlier checkpoints and their trusted keys for future comparisons. [Trust model](trust-model.md#how-a-verifier-obtains-a-trusted-checkpoint).

This example uses a three-record export and its checkpoint at count 3.

```sh
openplexer-verify --export audit-export/export.json --jwks trusted-jwks.json \
  --checkpoint audit-export/checkpoint.jws
```

```text
Chain is consistent from genesis (3 records). Completeness is proven through checkpoint count 3.
```

Repeat `--checkpoint` for other kept tokens. Each file holds one compact token with no whitespace. Use `--json` to print the [v1 verify result](../spec/audit/v1/checkpoint-verify-result.schema.json) to standard output.

The verifier exits with `0` for a valid export, `1` for a failed verification, and `2` for a usage or file error.

## What the verifier cannot prove

- Without a trusted checkpoint, the verifier can check that the chain agrees from genesis. A valid prefix and a full rewrite with recomputed hashes both pass that check.
- A trusted checkpoint covers records through its count. Records after the highest supplied count are unanchored. The verifier cannot tell whether the hub wrote more records, skipped an event, or signed a checkpoint you never received.
- The hash leaves out `id`, `result`, `agent_member_id`, and unknown fields. Their original values cannot be verified. The display result can be derived from the hashed `action`.
- A checkpoint carries the hub's claim about its issue time. A hub with the signing key can sign a rewritten log. A previously kept checkpoint can expose a rewrite that changes its signed tip.

## Read the output

To check the same export without a checkpoint, run the verifier with just the export and JWKS. The verifier still reads the file named by `--jwks`. The key set can be empty for this check.

```sh
openplexer-verify --export audit-export/export.json --jwks trusted-jwks.json
```

```text
Chain is consistent from genesis (3 records). Completeness is not proven.
```

If you kept a checkpoint from when the chain had two records, save it as `earlier-checkpoint.jws` in your working directory. Use a trusted JWKS that includes its signing key.

```sh
openplexer-verify --export audit-export/export.json --jwks trusted-jwks.json \
  --checkpoint earlier-checkpoint.jws
```

```text
Chain is consistent from genesis (3 records). Completeness is proven through checkpoint count 2.
Records after position 2 are unanchored.
```

In the default text mode, a failed check prints one diagnostic to standard error. Positions count from genesis, starting at 1. The verifier reports the first failure in the order set by the contract. `<none>` at position 0 means the rule has no record location. `<unknown>` means the record lacks a string ID. Checkpoint numbers follow the order of the flags. [Audit verification rules](../spec/audit/README.md#verification-and-result).

A change to a hashed value with the original hash fails A3. A broken link after genesis fails A5. A nonempty export that starts after genesis fails A4. A page with a cursor fails A6. A contiguous prefix or an empty export fails K9 when a trusted checkpoint covers the missing suffix.

To reproduce the diagnostics, use the [checkpoint fixtures](../spec/audit/v1/fixtures/checkpoint.json). Save `shared.jwks` as `checkpoint-fixtures/jwks.json`. For each case named below, create a directory with that name under `checkpoint-fixtures/` and save `input.export` there as `export.json`. Save each string in `input.checkpoints` without quotes or a trailing newline as `checkpoint-1.jws`, `checkpoint-2.jws`, and so on, in array order.

```sh
(
  set -e
  for name in 'malformed record' 'altered record' 'filtered export' \
    'missing middle record' 'paged export' 'padded compact JWS' \
    'policy-typed JWS' 'unknown kid' 'bad signature' \
    'unsupported checkpoint version' 'impossible issue date' 'workspace mismatch' \
    'contradicting checkpoints' 'truncated chain' 'rewritten tip'
  do
    dir="checkpoint-fixtures/$name"
    set -- --export "$dir/export.json" --jwks checkpoint-fixtures/jwks.json
    i=1
    while [ -f "$dir/checkpoint-$i.jws" ]; do
      set -- "$@" --checkpoint "$dir/checkpoint-$i.jws"
      i=$((i + 1))
    done
    exit_code=0
    openplexer-verify "$@" 2>&1 || exit_code=$?
    [ "$exit_code" -eq 1 ]
  done
)
```

```text
record 18 (position 2): rule A1: expected flagged to be a boolean, found a string. Export the log again
record 18 (position 2): rule A3: expected hash fe132b97914eded15dd26d78e7a5964b4892155779757992acc696e0ea742ee2, found "544fe01a468dcadf7deb23591555da116ffbf26c070c657e4cc5f08d333b9dce". Check the record's hashed fields
record 18 (position 1): rule A4: expected prev_hash 0000000000000000000000000000000000000000000000000000000000000000, found "1d6981cb43cff846849a04871f1a3e8efb53dcca15c2ce19d037a70f1826f95b". Check that the export starts at genesis and is unfiltered
record 25 (position 2): rule A5: expected prev_hash 1d6981cb43cff846849a04871f1a3e8efb53dcca15c2ce19d037a70f1826f95b, found "544fe01a468dcadf7deb23591555da116ffbf26c070c657e4cc5f08d333b9dce". Check for missing or reordered records
record <none> (position 0): rule A6: expected next_cursor to be null, found it missing or non-null. Export the full log without paging
record <none> (position 0): rule K1: checkpoint 1: expected unpadded base64url in segment 3, found invalid encoding. Copy the compact token exactly
record <none> (position 0): rule K2: checkpoint 1: expected typ "openplexer-audit-checkpoint+jws", found "mp-policy+jws". Use an audit checkpoint token
record <none> (position 0): rule K3: checkpoint 1: expected one public ES256 key for kid "checkpoint-test-absent", found 0 matching keys. Check the trusted checkpoint JWKS
record <none> (position 0): rule K4: checkpoint 1: expected a valid ES256 signature, found a signature error (the signature does not verify under the key). Check the token and trusted key
record <none> (position 0): rule K5: checkpoint 1: expected checkpoint version 1, found 2. Use a v1 audit checkpoint
record <none> (position 0): rule K6: checkpoint 1: expected issued_at to be a real UTC timestamp with a year from 0001 through 9999 and three fractional digits, found an invalid date or format. Check the checkpoint producer
record 11 (position 1): rule K7: checkpoint 1: expected workspace "wsp-other-test", found "wsp-checkpoint-test". Check that the checkpoint and export came from the same team
record <none> (position 0): rule K8: checkpoint 2: expected tip 8579a147105fbe91815e8aa659df9225d36310dc11aa58645e8398e1315e3a6e from checkpoint 1 at count 3, found e3653e1a0e0ef73ddf90f6c68c681fa27f6f214816f20fc3bb5698073ef8e8a6. Compare independently kept checkpoints
record <none> (position 0): rule K9: checkpoint 1: expected at least 3 records, found 2. Check for a missing suffix or an older export
record 25 (position 3): rule K10: checkpoint 1: expected signed tip 8579a147105fbe91815e8aa659df9225d36310dc11aa58645e8398e1315e3a6e, found e3653e1a0e0ef73ddf90f6c68c681fa27f6f214816f20fc3bb5698073ef8e8a6 at count 3. Compare the export with a kept copy
```

Version 1 records and exports have no version field. The contract allows unknown fields and action strings. A checkpoint with an unsupported version fails K5.

Checkpoint delivery to connected daemons and daemon storage of received checkpoints are later work. CSV export and audit retention are also later work.

For logs from the TypeScript hub, see [audit compatibility](audit-compatibility.md).

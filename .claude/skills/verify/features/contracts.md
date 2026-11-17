# Contracts for implementers

`spec/` holds JSON Schemas (draft 2020-12), examples, and data-only fixtures for four contracts: canonical JSON, policy, Room, and audit. An implementer in another language uses them without reading Go.

## Sub-features

- Every schema resolves by `$id` (`urn:openplexer:spec:<contract>:v1:<name>`).
- Every file in `spec/<contract>/v1/examples/<name>/` is valid against `<name>.schema.json`.
- Every file in `spec/*/v1/fixtures/` is valid against `spec/fixture-file.schema.json`.

## How to get to it (user POV)

An implementer reads `spec/README.md`, loads every schema into their own validator, and runs the fixtures.

## Driving it with verify.sh

`verify.sh <ref> spec-ajv`. Pass means `spec-ajv exit=0` and a last line of `schemas=<n> examples=<n> fixtures=<n> invalid=0`.

## Gotchas

- `spec-ajv.cjs` turns off ajv's format validation and strict mode, as `spec/README.md` says a validator needs. ajv's strict defaults log warnings on `spec/fixture-file.schema.json`, but validation still passes.
- ajv comes from the reference's `node_modules`. Without the reference, install `ajv` into a scratch directory and pass that path instead.

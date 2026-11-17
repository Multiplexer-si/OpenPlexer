# Reference comparison

The contracts, the fixtures, and the inventory come from the existing TypeScript implementation at commit `7d80045a`, which is not public. Two tools compare the repo with it.

## Sub-features

- `tools/extract` `check` recomputes every generated file under `spec/` and every expected fixture value. It prints `check: <n> files, 0 problems` on success.
- `tools/checkinventory` checks that every path, symbol, and test title in `docs/inventory.md` exists in the reference. It does not check that a row's sentence is true.

## How to get to it (user POV)

A contributor follows "Regenerate the contracts and fixtures" in `CONTRIBUTING.md`, and the command near the top of `docs/inventory.md`.

## Driving it with verify.sh

`verify.sh <ref> extract inventory`. Pass means both exit 0, and `extract.log` ends with `0 problems`.

## Gotchas

- CI cannot run these, because it has no reference checkout. Run them for any change to `spec/`, `tools/extract`, or `docs/inventory.md`.
- `checkinventory` without `-reference` prints `-reference is required` and exits 2. That is a usage error, not a pass.
- The signed-bundle fixtures cannot be recomputed. `check` verifies their tokens with the reference's verifier instead.

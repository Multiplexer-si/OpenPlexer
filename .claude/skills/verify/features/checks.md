# Repository checks

`make check` is what CI runs on every pull request: `gofmt -l`, `go vet ./...`, and `go test ./...`.

## Sub-features

- `internal/doclinks` fails on a relative Markdown link to a missing file. It drops the part after `#`, so it does not check anchors.
- `internal/layering` enforces the package dependency rules, and that no non-test file imports another module.
- `internal/spectest` checks the layout of `spec/`, compiles every schema, and validates every example and fixture.
- `tools/checkinventory` tests run against a git repo that the test builds, so they need no reference.

## How to get to it (user POV)

A contributor runs `make check` from the repo root. CI runs `make check` and `make build`.

## Driving it with verify.sh

`verify.sh <ref> check`. Pass means `make-check exit=0`, and `make-check.log` lists `ok` for doclinks, layering, spectest, and checkinventory.

## Gotchas

- Check anchors by hand. A broken `#anchor` passes `make check`.
- Go module downloads need the network the first time. Later runs use `~/go/pkg/mod`.

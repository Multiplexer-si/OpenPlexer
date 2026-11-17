# CLI binaries

The repo builds three programs from `apps/`: `openplexer` (the local CLI and daemon), `openplexer-hub`, and `openplexer-verify`. None is implemented yet.

## Sub-features

- Build all three with `make build`, into `bin/`.
- Each one prints `<name>: not implemented yet` to stderr and exits 1.

## How to get to it (user POV)

A contributor runs `make build` from the repo root, then runs `bin/openplexer`.

## Driving it with verify.sh

`verify.sh <ref> build binaries`. Pass means `build exit=0`, then `bin-<name> exit=1` for all three, and each log holds exactly the stub line.

## Gotchas

- Exit 1 is the expected result today. When Phase 1 implements a binary, change the `binaries` journey in `verify.sh` in the same PR.
- `--help` does nothing different yet. It prints the same stub line.

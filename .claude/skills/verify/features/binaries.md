# CLI binaries

The repo builds three programs from `apps/`: `openplexer` (the local CLI), `openplexer-hub`, and `openplexer-verify`. Only `openplexer` is implemented, and [gate.md](gate.md) covers its commands.

## Sub-features

- Build all three with `make build`, into `bin/`.
- `openplexer` with no command, or with a command that it does not know, prints its usage to stderr and exits 2.
- `openplexer-hub` and `openplexer-verify` each print `<name>: not implemented yet` to stderr and exit 1.

## How to get to it (user POV)

A contributor runs `make build` from the repo root, then runs `bin/openplexer`.

## Driving it with verify.sh

`verify.sh <ref> build binaries`. Pass means `build exit=0`, `bin-openplexer exit=2` with a log that starts with `usage:`, and `bin-<name> exit=1` for the other two, each with a log that holds exactly the stub line.

## Gotchas

- Exit 1 is the expected result for the two stubs today. When a later phase implements one, change the `binaries` journey in `verify.sh` in the same PR.
- `openplexer hook` and `openplexer install` do not exist yet. Both print the usage.

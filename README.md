# OpenPlexer

OpenPlexer is the open-source Go foundation for coordinating AI coding agents on a team. It is licensed under Apache-2.0. It has four parts:

1. Claude Code and Codex adapters and hooks, with local policy enforcement.
2. The Room protocol, with claim and decision formats.
3. A single-team, self-hosted hub with basic policies.
4. A hash-chain audit log format and an independent verifier.

## Status

OpenPlexer is pre-alpha. Nothing is implemented yet. The repository holds the module, the directory layout, the build and check entry points, CI, and a test that enforces the dependency rules below. The plan is tracked at https://github.com/Multiplexer-si/OpenPlexer/issues/1.

The three binaries print "not implemented yet" to stderr and exit with status 1.

## Repository layout

```
apps/
  openplexer/            local CLI and daemon that runs on a developer's machine
  openplexer-hub/        hub server
  openplexer-verify/     standalone audit log verifier
internal/
  policy/                policy model and evaluator (pure, no I/O)
  room/                  Room protocol: events, claims, decisions, validation (pure, no I/O)
  audit/                 audit hash-chain record format, append, and verification
  adapter/
    claudecode/          Claude Code hook adapter
    codex/               Codex hook adapter
  daemon/                local runtime: the policy gate, local identity, policy source, offline behavior
  hub/                   single-team hub server
  layering/              test that enforces the dependency rules
spec/                    language-independent contracts (schemas, examples, fixtures)
  room/                  Room events, claims, and decisions
  policy/                policies
  audit/                 audit records and hash-chain test vectors
.github/workflows/       CI
Makefile                 check, build, and fmt targets
```

## Dependency rules

The rules apply to transitive imports. The test in `internal/layering` reads the imports of every package and fails when a package depends on one it must not. The rule table in that test is the authority; this table is a copy for readers.

| Packages | Must not depend on |
|---|---|
| `internal/policy`, `internal/room`, `internal/audit` | `internal/adapter/...`, `internal/daemon`, `internal/hub` |
| `internal/adapter/...` | `internal/daemon`, `internal/hub` |
| `internal/daemon` | `internal/hub` |
| `internal/hub` | `internal/daemon`, `internal/adapter/...` |
| `apps/openplexer` | `internal/hub` |
| `apps/openplexer-hub` | `internal/daemon`, `internal/adapter/...` |
| `apps/openplexer-verify` | every internal package except `internal/audit` |

The last row is an allowlist. A new internal package is denied to the verifier until someone adds it to the allowlist in the test.

## Where new code goes

A new agent adapter is a new directory under `internal/adapter/`. The layering test covers it without changes.

Everything starts in `internal/`. A package becomes public only when an outside importer needs it.

## Development

OpenPlexer needs Go 1.27 and uses only the standard library.

- `make check` fails if `gofmt -l .` prints anything, then runs `go vet ./...` and `go test ./...`.
- `make build` builds every `apps/*` binary into `bin/`.
- `make fmt` runs `gofmt -w .`.

CI runs `make check` and `make build`.

## License

Apache-2.0. See `LICENSE` and `NOTICE`.

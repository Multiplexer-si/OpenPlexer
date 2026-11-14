# Repository layout

This page lists the directories, binaries, packages, dependency rules, make targets, and CI of this repository. [Architecture](architecture.md) explains the reasons behind the layout.

## Directories

```
apps/                    one directory per binary
internal/                Go packages, one per domain
spec/                    language-independent contracts
docs/                    documentation
.github/workflows/       CI
```

To list every Go package, run `go list ./...`.

## Binaries

Each directory under `apps/` builds one binary. `make build` writes the binaries to `bin/`.

| Directory | Binary | Role |
|---|---|---|
| `apps/openplexer` | `openplexer` | The CLI and daemon on a developer's machine |
| `apps/openplexer-hub` | `openplexer-hub` | The hub server |
| `apps/openplexer-verify` | `openplexer-verify` | The standalone verifier for audit logs |

Each binary prints "not implemented yet" to standard error and exits with status 1.

## Packages

The first seven packages each hold a `doc.go` file with a package comment and no other code. The last two packages each hold one test.

| Package | Owns |
|---|---|
| `internal/policy` | The policy model and the policy evaluator. Pure, no I/O. |
| `internal/room` | The Room protocol: events, claims, decisions, and their validation. Pure, no I/O. |
| `internal/audit` | The record format of the audit hash chain, appending, and verification. |
| `internal/adapter/claudecode` | The hook adapter for Claude Code. |
| `internal/adapter/codex` | The hook adapter for Codex. |
| `internal/daemon` | The runtime on a developer's machine: the gate, local identity, the policy source, and offline behavior. |
| `internal/hub` | The single-team hub server. |
| `internal/layering` | The test that enforces the dependency rules. |
| `internal/doclinks` | The test that checks the relative links in the documentation. |

## Dependency rules

The rules apply to transitive imports. The rule table in `internal/layering/layering_test.go` is the authority. This table is a copy for readers.

| Packages | Must not depend on |
|---|---|
| `internal/policy`, `internal/room`, `internal/audit` | `internal/adapter/...`, `internal/daemon`, `internal/hub` |
| `internal/adapter/...` | `internal/daemon`, `internal/hub` |
| `internal/daemon` | `internal/hub` |
| `internal/hub` | `internal/daemon`, `internal/adapter/...` |
| `apps/openplexer` | `internal/hub` |
| `apps/openplexer-hub` | `internal/daemon`, `internal/adapter/...` |
| `apps/openplexer-verify` | Every internal package except `internal/audit` |

A trailing `/...` matches the package and every package under it. The last row is an allowlist. The test denies a new internal package to the verifier until someone adds the package to the `except` list of that rule.

## Contracts

Each directory under `spec/` holds the schema, the examples, and the fixtures of one contract. The three directories hold only a README today.

| Directory | Contract |
|---|---|
| `spec/room` | Room events, claims, and decisions |
| `spec/policy` | Policies |
| `spec/audit` | Audit records and the test vectors for the hash chain |

## Make targets

| Target | Action |
|---|---|
| `make check` | Fails if `gofmt -l .` lists a file. Then runs `go vet ./...` and `go test ./...`. |
| `make build` | Builds every binary under `apps/` into `bin/`. |
| `make fmt` | Runs `gofmt -w .`. |

## CI

The workflow in `.github/workflows/ci.yml` runs on every pull request and on every push to `main`. It installs the Go version that `go.mod` names, then runs `make check` and `make build`.

## Toolchain

The module is `github.com/multiplexer-si/openplexer`. It needs Go 1.27 or later and it uses only the standard library.

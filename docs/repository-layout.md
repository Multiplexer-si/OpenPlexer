# Repository layout

This page lists the directories, binaries, packages, dependency rules, make targets, and CI of this repository. [Architecture](architecture.md) explains the reasons behind the layout.

## Directories

```
apps/                    one directory per binary
internal/                Go packages, one per domain
spec/                    language-independent contracts
tools/                   programs that write files in this repository
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

The first seven packages each hold a `doc.go` file with a package comment and no other code. The last three packages hold only tests.

| Package | Owns |
|---|---|
| `internal/policy` | The policy model and the policy evaluator. Pure, no I/O. |
| `internal/room` | The Room protocol: events, claims, decisions, and their validation. Pure, no I/O. |
| `internal/audit` | The record format of the audit hash chain, appending, and verification. |
| `internal/adapter/claudecode` | The hook adapter for Claude Code. |
| `internal/adapter/codex` | The hook adapter for Codex. |
| `internal/daemon` | The runtime on a developer's machine: the gate, local identity, the policy source, and offline behavior. |
| `internal/hub` | The single-team hub server. |
| `internal/layering` | The tests that enforce the dependency rules, and the rule that only a test may import another module. |
| `internal/doclinks` | The test that checks the relative links in the documentation. |
| `internal/spectest` | The tests that check the contracts: every file under `spec/` is in its place, every schema compiles, every example validates against its schema, every fixture file validates against the fixture-file schema, and every hash follows from its rule. |

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

Each directory under `spec/` holds one contract: a README with the rules, and a `v1/` directory with the schemas, the examples, and the fixtures. Canonical JSON defines text and not a shape, so it has no schema, and its `v1/` holds only fixtures. [The contracts in `spec/`](../spec/README.md) describes the layout and the format of a fixture file.

| Directory | Contract |
|---|---|
| `spec/canonical-json` | Canonical JSON, the byte form that the bundle signature and the audit hash are computed over |
| `spec/policy` | The policy bundle, overrides, policy evaluation, and the signed bundle |
| `spec/room` | Room events, which include claims and decisions, the transport envelope, and the overlap rules for claims |
| `spec/audit` | The audit record, its hash, chain verification, and the JSON export |

## Tools

`tools/extract` is the TypeScript program that writes every schema, every example, and every fixture under `spec/`. It runs the existing TypeScript implementation, so it needs a checkout of that implementation and Node. It is not part of the Go module, and neither `make check` nor CI runs it. [Regenerate the contracts and fixtures](../CONTRIBUTING.md#regenerate-the-contracts-and-fixtures) gives the commands.

## Make targets

| Target | Action |
|---|---|
| `make check` | Fails if `gofmt -l .` lists a file. Then runs `go vet ./...` and `go test ./...`. |
| `make build` | Builds every binary under `apps/` into `bin/`. |
| `make fmt` | Runs `gofmt -w .`. |

## CI

The workflow in `.github/workflows/ci.yml` runs on every pull request and on every push to `main`. It installs the Go version that `go.mod` names, then runs `make check` and `make build`.

## Toolchain

The module is `github.com/multiplexer-si/openplexer`. It needs Go 1.27 or later.

The three binaries use only the standard library. A test in `internal/layering` fails if a file that is not a test imports another module. The tests in `internal/spectest` use `github.com/santhosh-tekuri/jsonschema/v6` to compile and run the JSON Schemas, so `go.mod` requires that module.

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
| `apps/openplexer` | `openplexer` | The CLI on a developer's machine |
| `apps/openplexer-hub` | `openplexer-hub` | The single-team hub server and its local admin commands |
| `apps/openplexer-verify` | `openplexer-verify` | The standalone verifier for audit logs |

`openplexer` configures the local gate, loads a signed policy bundle, reports the state of the policy, and checks one action. [Local policy gate](local-policy.md) lists those commands. It also installs and removes the hook of Claude Code and Codex, and it is the command that each hook runs. [Claude Code hooks](hooks-claude-code.md) and [Codex hooks](hooks-codex.md) give those commands. `openplexer-hub` manages members, projects, and signed policies. It serves the Room HTTP API and exports the team audit log. [Run the single-team hub](hub.md) gives the commands and endpoints. `openplexer-verify` checks a JSON export and signed checkpoints offline. [Export and verify an audit log](audit.md).

## Packages

`internal/policy`, `internal/audit`, and `internal/daemon` hold the policy evaluator and the verification of a signed bundle, the audit record, and the local gate. The four adapter packages hold the hook adapters and their list, and `internal/jsonedit` edits a JSON file in place. `internal/room` holds the Room protocol. `internal/hub` runs the single-team hub. The last three packages hold only tests.

| Package | Owns |
|---|---|
| `internal/policy` | The policy model, the policy evaluator, and the verification of a signed bundle against a key set. Pure, no I/O. Its tests run every fixture of `spec/policy`. |
| `internal/audit` | Canonical JSON, audit record hashing, and verification of exports and signed checkpoints. Pure, no I/O. Its tests run every fixture of `spec/canonical-json` and `spec/audit`. |
| `internal/daemon` | The local gate: the policy source, the store of the last verified bundle, the state of the policy, local identity, and the audit log of each decision. It reads and writes one home directory. |
| `internal/room` | The Room protocol: events, claims, decisions, the envelope, and their validation, the overlap of two claims, and the models of order, expiry, reconnection, and errors. Pure, no I/O. Its tests run every fixture of `spec/room`. |
| `internal/adapter` | The list of the adapters, and the test that runs every case of `spec/adapter` against them. |
| `internal/adapter/hook` | What both hook adapters share: the hook call, the reading of a shell command and of a path, the project root, the guarded paths, the queries of one tool call, the verdict, and the install and removal of a hook entry. It imports `internal/policy` and `internal/jsonedit`. |
| `internal/adapter/claudecode` | The `Agent` value of Claude Code: its table of gated tools, its settings files, and its hook entry. |
| `internal/adapter/codex` | The `Agent` value of Codex: its table of gated tools, the reader of `apply_patch` patches, its hook files, and its hook entry. |
| `internal/jsonedit` | Edits of JSON text that keep every byte they do not change. It imports nothing from the module. |
| `internal/hub` | The hub file store, membership, repository scope, policy signing, Room sequencing, audit storage and export, and the HTTP API. |
| `internal/layering` | The tests that enforce the dependency rules, and the rule that only a test may import another module. |
| `internal/doclinks` | The tests that check relative documentation links and the trust-model section links in the architecture and privacy guide. |
| `internal/spectest` | The tests that check the contracts: every file under `spec/` is in its place, every schema compiles, every example validates against its schema, every fixture file validates against the fixture-file schema, and every hash follows from its rule. |

## Dependency rules

The rules apply to transitive imports. The rule table in `internal/layering/layering_test.go` is the authority. This table is a copy for readers.

| Packages | Must not depend on |
|---|---|
| `internal/policy`, `internal/room`, `internal/audit` | `internal/adapter/...`, `internal/daemon`, `internal/hub` |
| `internal/adapter/...` | `internal/daemon`, `internal/hub` |
| `internal/jsonedit` | Every internal package |
| `internal/daemon` | `internal/hub` |
| `internal/hub` | `internal/daemon`, `internal/adapter/...` |
| `apps/openplexer` | `internal/hub` |
| `apps/openplexer-hub` | `internal/daemon`, `internal/adapter/...` |
| `apps/openplexer-verify` | Every internal package except `internal/audit` and `internal/policy` |

A trailing `/...` matches the package and every package under it. The last row is an allowlist. The test denies a new internal package to the verifier until someone adds the package to the `except` list of that rule.

## Contracts

Each directory under `spec/` holds one contract: a README with the rules, and a `v1/` directory with the schemas, the examples, and the fixtures. Canonical JSON defines text and not a shape, so it has no schema, and its `v1/` holds only fixtures. The adapter contract also has no schema, because the hook input is the format of each coding agent. [The contracts in `spec/`](../spec/README.md) describes the layout and the format of a fixture file.

| Directory | Contract |
|---|---|
| `spec/canonical-json` | Canonical JSON, the byte form that the bundle signature and the audit hash are computed over |
| `spec/policy` | The policy bundle, overrides, policy evaluation, and the signed bundle |
| `spec/room` | Room events, which include claims and decisions, the transport envelope, and the overlap rules for claims |
| `spec/audit` | Audit records, hash chains, JSON exports, and signed checkpoints |
| `spec/adapter` | How an adapter reads the hook input of a coding agent into policy queries, and answers the hook. It has fixtures and no schema. |

## Tools

`tools/extract` writes the reference-compatible schemas, examples, and fixtures under `spec/`. It needs a checkout of the existing TypeScript implementation and Node. It runs outside the Go module and outside CI. [Regenerate the contracts and fixtures](../CONTRIBUTING.md#regenerate-the-contracts-and-fixtures).

`tools/auditvectors` generates the OpenPlexer checkpoint fixtures. Run `go run ./tools/auditvectors` from the repository root. [Audit contract](../spec/audit/README.md#generated-and-authored-parts).

## Audit end-to-end tests

Run `go test ./test/audit` to build the audit binaries and test them with temporary directories. The tests verify the reference fixtures and run the shell examples in the [audit guide](audit.md). They run in `make check` and need no running hub or network access.

## Make targets

| Target | Action |
|---|---|
| `make check` | Fails if `gofmt -l .` lists a file. Then runs `go vet ./...` and `go test -count=1 ./...`. |
| `make build` | Builds every binary under `apps/` into `bin/`. |
| `make fmt` | Runs `gofmt -w .`. |

## CI

The workflow in `.github/workflows/ci.yml` runs on every pull request and on every push to `main`. It installs the Go version that `go.mod` names, then runs `make check` and `make build` in one job named `check`. `main` requires that job before a pull request can merge. [Run the checks](../CONTRIBUTING.md#run-the-checks) lists the same commands to run locally, and names the tests that run the fixtures.

## Toolchain

The module is `github.com/multiplexer-si/openplexer`. It needs Go 1.27 or later.

The three binaries use only the standard library. A test in `internal/layering` fails if a file that is not a test imports another module. The tests in `internal/spectest`, `internal/audit`, `internal/policy`, and `internal/daemon` use `github.com/santhosh-tekuri/jsonschema/v6` to compile and run the JSON Schemas, so `go.mod` requires that module.

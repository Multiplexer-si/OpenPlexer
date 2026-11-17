# Roadmap

The work is split into five phases. The [epic](https://github.com/Multiplexer-si/OpenPlexer/issues/1) and the [milestones](https://github.com/Multiplexer-si/OpenPlexer/milestones) track progress. The issue tracker is the authority, and this page is a summary of it.

## Phases

| Phase | Outcome | Depends on |
|---|---|---|
| [0. Scope and compatibility](https://github.com/Multiplexer-si/OpenPlexer/issues/2) | The contracts, the fixtures, and the trust model exist before any Go implementation. | Nothing |
| [1. Local policy and agent hooks](https://github.com/Multiplexer-si/OpenPlexer/issues/3) | A developer can install the daemon, connect Claude Code and Codex, and inspect the result of each policy evaluation on their machine. | Phase 0 |
| [2. Room protocol and self-hosted hub](https://github.com/Multiplexer-si/OpenPlexer/issues/4) | One team can run the hub, configure basic policies, and coordinate through the Room protocol. | Phase 0, and it connects to Phase 1 |
| [3. Audit format and independent verification](https://github.com/Multiplexer-si/OpenPlexer/issues/5) | Anyone can verify an exported audit log against the published format and a trusted checkpoint. | Phase 0 and Phase 2 |
| [4. Open-source release and adoption](https://github.com/Multiplexer-si/OpenPlexer/issues/6) | Developers can install, run, and verify OpenPlexer from a public release. | Phases 1, 2, and 3 |

## Why the phases are in this order

Phase 0 comes first because the contracts are the definition of correct behavior. A Go implementation that exists before the fixtures has nothing to be tested against.

Phase 1 needs no hub. The daemon loads signed policy from a bundle on disk, so a developer gets local enforcement before any server exists. Phase 2 replaces the bundle on disk with policy that the hub distributes.

Phase 3 follows Phase 2 because the hub writes the audit log that the verifier checks.

## What exists today

Phase 0 is complete. The repository has its structure, its license, the dependency rules, and CI. It also has the [trust model](trust-model.md), the [inventory](inventory.md) of the existing implementation, and the contracts and fixtures in [`spec/`](../spec/README.md).

Phase 1 is in progress. The policy evaluator and the [local policy gate](local-policy.md) exist. The adapters for Claude Code and Codex exist in `internal/adapter`. The `openplexer hook` command that calls them and the `install` command that writes their hooks do not exist yet.

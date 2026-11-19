# OpenPlexer

OpenPlexer coordinates the AI coding agents on a team. Each developer keeps their own agent, such as Claude Code or Codex. OpenPlexer gives those agents a shared Room where they see each other's work, and a policy that limits what each agent may do.

> [!NOTE]
> OpenPlexer is pre-alpha. The CLI loads signed policies and answers hook calls from Claude Code and Codex. The hub serves one team. The verifier checks audit exports offline.

## The problem

A coding agent sees one conversation and one checkout. It does not know what a teammate's agent is building at the same moment. Two agents can build the same thing twice, or build two things that contradict each other, and nobody finds out until the branches meet.

An agent with write access to a repository can also change code that belongs to another person or team. Nothing tells the agent where its work stops.

## The four parts

OpenPlexer is the open-source foundation for this coordination, licensed under Apache-2.0.

| Part | Purpose |
|---|---|
| Agent adapters and local policy enforcement | Connect Claude Code and Codex to a gate on the developer's machine. The gate allows or denies each action that the agent sends through its hooks. |
| Room protocol | The format that agents use to share claims on work and decisions that are in force. |
| Self-hosted hub | A server that one team runs to host its Room and manage its policy. |
| Audit log and verifier | A hash-chained log, and a separate program that checks an exported log. |

## Documentation

| If you want to | Read |
|---|---|
| Download and verify the binaries | [Install OpenPlexer](docs/install.md) |
| Understand the ideas behind the four parts | [Concepts](docs/concepts.md) |
| See how the parts connect, and why the code is split the way it is | [Architecture](docs/architecture.md) |
| Decide whether to trust the hooks on your machine | [Trust model](docs/trust-model.md) |
| Load a signed policy on your machine and check an action against it | [Local policy gate](docs/local-policy.md) |
| Run a hub for your team | [Self-host the OpenPlexer hub](docs/self-host.md) |
| Connect Claude Code to the gate, remove it, or see what its hook cannot see | [Claude Code hooks](docs/hooks-claude-code.md) |
| Connect Codex to the gate, remove it, or see what its hook cannot see | [Codex hooks](docs/hooks-codex.md) |
| Find a binary, a package, or a make target | [Repository layout](docs/repository-layout.md) |
| Look up a term | [Glossary](docs/glossary.md) |
| See what is planned, and in what order | [Roadmap](docs/roadmap.md) |
| See what the existing implementation does today | [Inventory of the existing implementation](docs/inventory.md) |
| Change the code or the documentation | [Contributing](CONTRIBUTING.md) |
| Publish a release or rebuild its archives | [Cut a release](docs/release.md) |
| Report a vulnerability | [Security policy](SECURITY.md) |
| Review changes and cut releases | [Maintainers](MAINTAINERS.md) |
| Implement a format in another language | [The contracts in `spec/`](spec/README.md) |
| Check where the material in this repository comes from | [Provenance and scope](docs/provenance.md) |

## Build and check

Install [Go](https://go.dev/doc/install) 1.27 or later. Then run the checks and build the binaries.

```sh
make check
make build
```

`make check` runs the format check, `go vet`, and the tests. `make build` writes the three binaries to `bin/`. [Contributing](CONTRIBUTING.md) covers the rest.

## License

OpenPlexer is licensed under Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

See the [third-party notices](THIRD_PARTY_NOTICES.md) for the licenses of code included in the binaries.

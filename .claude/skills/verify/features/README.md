# OpenPlexer feature map

One file per user-facing feature. Each file says what the feature is, how a user reaches it, how `verify.sh` drives it, and what end state proves it works.

| Feature | File | Journeys |
|---|---|---|
| CLI binaries | [binaries.md](binaries.md) | `build`, `binaries` |
| Repository checks | [checks.md](checks.md) | `check` |
| Contracts for implementers | [contracts.md](contracts.md) | `spec-ajv`, `check` |
| Reference comparison | [reference.md](reference.md) | `extract`, `inventory` |

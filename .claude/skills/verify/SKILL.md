---
name: verify
description: Drive and prove OpenPlexer at a given git ref. Builds the three CLI binaries, runs make check, validates spec/ with a non-Go JSON Schema validator, and checks spec/ and docs/inventory.md against the private TypeScript reference. Use to verify a PR or branch before calling it done or posting a verdict.
---

# Verify OpenPlexer

OpenPlexer is pre-alpha. Its user-facing surface today is:

- Three CLI binaries (`openplexer`, `openplexer-hub`, `openplexer-verify`). Each prints `<name>: not implemented yet` to stderr and exits 1. That is the expected behavior until Phase 1 lands. Update the `binaries` journey when it changes.
- The language-independent contracts in `spec/`, which implementers in other languages consume.
- Two tools that compare the repo with the existing TypeScript implementation (the reference): the extractor (`tools/extract`) and the inventory checker (`tools/checkinventory`).

There is no server to keep running. Each run happens in a fresh, detached git worktree, so runs never touch the user's checkout and two runs can go side by side.

## Launch

`verify.sh` in this directory does everything. Pass a ref (a SHA, `origin/main`, or `origin/<branch>`) and one or more journeys.

```sh
.claude/skills/verify/verify.sh origin/main all
.claude/skills/verify/verify.sh <sha> build binaries check
```

Journeys are `doctor`, `build`, `binaries`, `check`, `spec-ajv`, `extract`, `inventory`, and `all`. The script fetches `origin`, adds a detached worktree at the ref under `$TMPDIR`, runs the journeys, and exits 0 only if every journey got its expected exit code. Ready means the worktree exists and `head.txt` names the SHA you asked for.

Inputs you can override with environment variables:

- `GO_BIN`. Go is not on PATH on this machine. The default is pre-commit's cached go1.27.2 at `~/.cache/pre-commit/repo6zv8u7zh/golangenv-default/.go/bin`. `GOTOOLCHAIN=local` stops Go from downloading a toolchain.
- `REFERENCE`. The reference checkout, default `~/multiplexer`, at commit `7d80045a` with `node_modules` installed. `spec-ajv`, `extract`, and `inventory` need it. Never write in it.
- `EVIDENCE`. Where proof goes. The default is `~/openplexer-verify-evidence/<ref>-<time>/`.

## Doctor

Run `verify.sh <ref> doctor` first, and again whenever a run looks wrong. `doctor.log` must show a Go version, a Node version, the reference at `7d80045a`, and `"7d80045a"` in `tools/extract/reference.ts`. If the reference is at another commit, `extract` and `inventory` fail for the wrong reason. Stop and report it.

## Drive

Each journey runs the real command that a contributor or CI runs. See `features/` for what each one proves and its gotchas.

| Journey | Command in the worktree | Expected exit |
|---|---|---|
| `build` | `make build` | 0 |
| `binaries` | `bin/<name>` for each binary, and its stderr is `<name>: not implemented yet` | 1 each |
| `check` | `make check` (gofmt, `go vet`, `go test`, which includes the doc-link test and the spec tests) | 0 |
| `spec-ajv` | `node spec-ajv.cjs <worktree> $REFERENCE/node_modules` validates every example and fixture with ajv 2020 | 0 |
| `extract` | `$REFERENCE/node_modules/.bin/tsx tools/extract/main.ts check --reference $REFERENCE` | 0 |
| `inventory` | `go run ./tools/checkinventory -reference $REFERENCE` | 0 |

A journey for a PR runs at the PR head. Also run it on the head merged with `origin/main` when main moved after CI ran. Do that in your own worktree with `git merge --no-ff --no-edit origin/main`, then call the commands from the table by hand.

## Evidence

Each run writes to `$EVIDENCE`:

- `head.txt` is the SHA that was checked.
- `summary.txt` has one `<journey> exit=<n>` line per command, and a `FAIL` line for each unexpected result.
- `<journey>.log` is the full output of each command.

Proof standards:

- Quote the failing line from the log, not only the exit code. A failure proves the defect only when its output is the disagreement you expected.
- A green `make check` is not enough for a change under `spec/` or `tools/extract`. CI has no reference checkout, so also run `extract` and `spec-ajv`.
- To prove that a check can fail, change one file in a copy of the worktree, rerun the command, quote the failure, and revert. A check that never failed proves nothing.

## Cleanup

The script's exit trap removes the worktree it created, and only that worktree. It never deletes `$EVIDENCE`. If a run was killed, list leftover worktrees with `git worktree list | grep openplexer-verify` and remove only those with `git worktree remove --force <path>`.

## Helpers

- `verify.sh <ref> <journey>...` is the runner.
- `spec-ajv.cjs <repo-root> <node_modules>` validates `spec/` with ajv. The runner calls it, and you can run it alone on any checkout.

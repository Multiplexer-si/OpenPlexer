# Scoped verification of <branch or PR> in <owner>/<repo> (#<N> <title>)

You are an independent verifier. You did not write this change. You run in your own git worktree, checked out at head `<head>`, on base `<base>`. Do not commit and do not push. Your only output is the verdict file `<verdict path>`.

## The goal

<Outcome.> The issue's done-when: <done-when>.

## Scope

Check the done-when, what this change did (`git diff <base>...<head>`), and that nothing on the base regressed. Do not run a campaign beyond that. Be adversarial about the changed code: a Codex agent wrote it, and you are a Codex agent too, so look hard for what its author would not think to test.

The contract is `<design doc>` section <n> and the issue's row in its testing table. Check the change against them point by point.

Your sandbox refuses loopback binds. The coordinator ran at this head in a clean clone: <gate results>. Do not fail a gate only because your sandbox blocks loopback; say which tests you could not run.

<Issue-specific things to hunt for: races, bypasses, parity between hubs, parsing, secrets, fail-open paths.>

## Always run

1. `export PATH=$GO_BIN:$PATH GOTOOLCHAIN=local GOCACHE=$PWD/.gocache GOFLAGS=-mod=mod`, then `make check` and `make build`. Record each exit code.
2. Prove each done-when item on the built binaries, with temporary data directories.
3. Read the diff for auth and token handling (hashed storage, constant-time comparison, a role check on every endpoint), input parsing (published schemas under `spec/`, case-sensitive, duplicate-aware), and atomic writes (races between concurrent requests, partial writes across files).
4. `go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./apps/...` shows only this module, and the layering test passes.
5. Every doc line the change touched is true.

Never touch `~/.claude`, `~/.codex`, or anything outside your worktree.

## Verdict file

Plain Markdown, no long dashes:
- Line 1: `**Independent verification (Codex, scoped): PASS**`, `PASS+NOTES`, or `FAIL`.
- Line 2: head, base, and `git diff <base>...<head> | git patch-id --stable`.
- Each command you ran, with its exit code.
- The result for each done-when item.
- Findings with file:line, each marked blocking or not.

FAIL only for something you can show: a failed gate, a done-when item that does not hold, an auth or parsing bypass, a data race or partial write, or a false doc line.

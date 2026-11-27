# Scoped recheck of <branch> (#<N> <title>)

You are an independent verifier. You did not write this change. Your worktree is at head `<head>`, base `<base>`. Do not commit or push. Your only output is `<verdict path>`.

The previous verdict is `<previous verdict path>` (at `<previous head>`). Its open points: <each blocking point in one line>. <Any finding withdrawn because the earlier prompt was wrong, and why.> <Any finding the user accepted as non-blocking: do not re-raise it.> Commits since: `<previous head>..<head>`.

The coordinator ran at this head in a clean clone: <gate results>. Your sandbox refuses loopback binds; do not fail for that.

Check only:
- each open point is fixed, with evidence on the built binary or fakes;
- the new commits for regressions or new bypasses;
- changed doc lines are true.

Use `export PATH=$GO_BIN:$PATH GOTOOLCHAIN=local GOCACHE=$PWD/.gocache GOFLAGS=-mod=mod`. Never touch `~/.claude`, `~/.codex`, or anything outside your worktree.

Verdict file, plain Markdown, no long dashes: line 1 `**Independent recheck (Codex, scoped): PASS**`, `PASS+NOTES`, or `FAIL`; line 2 head, base, and the stable patch-id; then commands with exit codes; then each point's status; then new findings with file:line, each marked blocking or not. FAIL only for something you can show.

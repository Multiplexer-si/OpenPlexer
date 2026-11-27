## The sandbox

Your sandbox refuses loopback listeners and has no network. `make check` (except tests at a loopback bind), `make build`, and Go tests that need no listener run for you. In `apps/hub-cloudflare`, `npm run typecheck` and `npm run test:unit` run. The coordinator runs the rest (loopback Go tests, `npm test`, `scripts/conformance.sh`), and an independent verifier checks the PR before merge. Write those tests anyway and say which ones you could not run.

## Rules

- The design doc named in the brief is the contract. Follow it exactly. If a point in it is impossible or contradicts the code, stop on that point, build the rest, and explain the conflict in your final message.
- Use `pstack:tdd`: write a failing test for each behavior change before the fix, and say which tests failed before and pass after.
- Commit in small commits. Do not push. If git cannot take its index lock, commit in a scratch clone at `dist/clone` (never inside `apps/hub-cloudflare`), save the commits in a bundle at `dist/pr.bundle` (named after this branch), and say so.
- Go binaries import only the Go standard library. Writes are atomic (temporary file, sync, rename) and serialized by the existing locks.
- Go: `export PATH=$GO_BIN:$PATH GOTOOLCHAIN=local GOCACHE=$PWD/.gocache GOFLAGS=-mod=mod`. Do not download Go modules into the worktree, and leave no `.go` file outside the committed tree: `make check` runs gofmt over the whole worktree.
- In Go tests, do not index a local array with a variable and reuse the element after other work. Copy it into a local first (go1.27.2 liveness bug).
- Node, where needed: `WRANGLER_SEND_METRICS=false XDG_CONFIG_HOME=$PWD/.wrangler-home npm_config_cache=$PWD/.npm-cache`. `node_modules` is installed. You cannot add packages.
- A hub change lands in both hubs, the Go hub and the Worker, with the same status codes, bodies, limits, and audit records, and a shared case in `test/hubconformance`.
- Parsing is strict: case-sensitive, rejects duplicate and unknown members.
- Never put a token, invite code, or bearer in a log, audit record, error message, or test output.
- Never touch `~/.claude`, `~/.codex`, `~/.openplexer`, `~/Library/LaunchAgents`, `~/.config/systemd`, or anything else outside this worktree. Tests use temporary directories and a fake HOME, and never call real `launchctl` or `systemctl`.
- Docs: plain English, short sentences, no long dashes. Match `docs/working.md`. Do not document an unreleased command in a walkthrough that pins a release.

## Final message

Under 20 lines: the commits, what you built per design point or finding, any design conflict, tests added, commands run with exit codes, and the tests the coordinator must run.

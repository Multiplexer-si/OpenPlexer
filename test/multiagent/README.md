# Verify two agents against a Go hub or Worker

Run this flow for [issue #22](https://github.com/Multiplexer-si/OpenPlexer/issues/22). It gives each agent its own project checkout, member token, policy home, and Room daemon. Both agents share the same hub Room.

## Run the flow

Use macOS or Linux with Go 1.27 or later, `claude` and `codex` on `PATH`, and support for the agents' tool sandboxes. Sign in to both agents with their normal home in a plain terminal. Before starting a model session, the runner checks both executables and sign-ins. It tries a loopback bind when starting the Go hub.

Run the flow from the repository root.

```sh
go run ./test/multiagent
```

Set `OPENPLEXER_BIN_DIR` to the absolute path of a directory containing prebuilt `openplexer` and `openplexer-hub` binaries. With the variable unset, the flow builds both from source.

From inside an agent session, clear the inherited environment first.

```sh
env -i HOME="$HOME" USER="$USER" PATH="$PATH" SHELL="$SHELL" TMPDIR="$TMPDIR" OPENPLEXER_BIN_DIR="${OPENPLEXER_BIN_DIR:-}" go run ./test/multiagent
```

The command exits 0 when all steps pass. It writes CLI versions, models, the OpenPlexer build revisions or release versions, and per-step results to [results.md](results.md). A failure records the failed step and marks pending steps as skipped.

`go test -count=1 ./test/multiagent` works offline in both modes. Its flow test substitutes agents, binaries, and HTTP responses in process.

The command prints the path to a private temporary directory with both projects, audit logs, Room logs, agent output, and member tokens. Keep it private and delete it when you finish reviewing the run. The runner stops the daemons and any Go hub it started on exit.

The runner starts agent commands with an empty environment and passes through `HOME`, `USER`, `PATH`, `SHELL`, and `TMPDIR`. The agents use their existing sign-ins, and the hook uses the participant's `OPENPLEXER_HOME` in the run directory. The hub and Room daemons use homes in the run directory.

Claude loads the throwaway project's settings via `--setting-sources project`, with `failIfUnavailable` enabled and only the `Write` tool available. `CLAUDE_CODE_TMPDIR` points to the run directory and `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` turns off auto memory. Codex uses the installed hook as a session setting, `--ignore-user-config`, and the same per-run hook trust flag and workspace-write sandbox as the real-agent harness. Its databases and logs go in the run directory. [Agent configuration in the real-agent harness](../realagent/README.md#how-each-agent-is-configured).

## Run against a Worker

Start a disposable Worker with fresh SQLite state. Initialize the team with `openplexer-hub init` and save the lead token in a private file. [Run and initialize the Worker](../../apps/hub-cloudflare/README.md#run-locally)

From the repository root, run the flow with the Worker's origin and lead token file.

```sh
go run ./test/multiagent -hub-url http://127.0.0.1:8787 -lead-token-file /path/to/lead.token
```

The runner creates members `dev-a` and `dev-b` and project `prj-multiagent` on the Worker. It publishes policy and leaves the Worker running. Use fresh state for each run.

## What the run touches outside the run directory

- The runner overwrites `test/multiagent/results.md`. The Go toolchain can use its configured build and module caches and its own temporary directories.
- For each Claude session, the runner picks a UUID, checks that `~/.claude/session-env/<uuid>/` and `~/.claude/projects/<slug>/` are absent, and removes those two paths after the process exits, including after a failure or interruption. A pre-existing path stops the session and is preserved. The slug is the absolute project path with each character other than a letter or digit replaced by `-`. A forced kill of the runner can skip cleanup.
- The Claude CLI can create `~/.claude/sessions/<pid>.json` for the lifetime of the process and update `~/.claude/cache/model-catalog/*-cc.json` and `~/.claude/plugins/store/*.json`. First use can create `.cc-writes`, `bridge-spawn`, `child-logs`, `daemon`, `dev-mods`, `seed-admin`, and `ssh` under `~/.claude/`, as well as `~/.config/anthropic/`.
- The Codex CLI can update `~/.codex/models_cache.json` and create helper directories under `~/.codex/tmp/arg0-macos/` on macOS. `codex exec` normally removes its own helper directory at exit. Version and sign-in checks can leave theirs, and a later Codex start can remove stale helpers.
- The CLIs access their usual sign-in stores. Claude Code uses the Keychain on macOS when available. A CLI can refresh its own sign-in in the existing store. These CLI side effects are based on the versions traced by the real-agent harness. [Real-agent filesystem observations](../realagent/README.md#what-the-harness-touches-outside-the-run-directory).

## What the run proves

1. Both members sync the signed policy using the pinned hub key set.
2. Claude uses its installed hook to write a claim request in its project. The runner checks the file and the audit record, then sends the requested path to the hub's claim endpoint using A's member token and Claude's session ID. The endpoint grants a lease, and B's Room daemon receives the same claim event. The [hub API](../../docs/hub.md#http-api) exposes lease creation through a separate endpoint from Room posts.
3. The runner posts A's decision via `openplexer room post`. B's daemon receives the claim and decision. The runner passes those messages to Codex, and Codex acts on the path from the decision.
4. Claude writes the allowed file. The Codex hook denies the write to the protected path, and A's daemon receives that denial. The runner checks each hook decision in the audit log, the allowed file's contents, and the denied file's absence.
5. In Go hub mode, A queues an event during the hub outage. After the hub restarts, A receives it while B's daemon is paused. In Worker mode, B's daemon stops before A posts the event, then restarts with the same policy home. In both modes B replays the missed event from its saved cursor and receives a new live event. B's complete log must match the five events in hub history in order, with each event present once.

The runner starts three model sessions with no retries. Claude Code uses Haiku at low effort for the claim and the allowed write. Codex uses GPT-6 Luna at low effort for the denied write. The providers bill these sessions to the signed-in accounts. Actual token use depends on the agents' replies. Hub setup, decision posting, and reconnect checks use the CLI or hub API.

`make check` exercises the same flow steps with fake agents, processes, and HTTP responses in one Go process. Those tests use no network or model sessions.

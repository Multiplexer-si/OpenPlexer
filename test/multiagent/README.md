# Verify two agents against a self-hosted hub

Run this flow for [issue #22](https://github.com/Multiplexer-si/OpenPlexer/issues/22). It starts one local hub and gives each agent its own project, member token, policy home, and Room daemon.

## Run the flow

Use macOS or Linux with Go 1.27 or later, `claude` and `codex` on `PATH`, and support for the agents' tool sandboxes. Use a plain terminal outside both agents. The command checks for both executables and tries a loopback bind before it builds the binaries or starts a model session.

Supply Claude Code credentials via `ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN`. For Codex, set `OPENAI_API_KEY` or set `MULTIAGENT_CODEX_AUTH_FILE` to an `auth.json` from a separate Codex login. The runner copies that file into the run directory. Keep the source outside your default agent homes. To create such a file with your ChatGPT account, use a separate temporary home:

```sh
auth_dir="$(mktemp -d)"
HOME="$auth_dir" CODEX_HOME="$auth_dir/codex" codex -c 'cli_auth_credentials_store="file"' login
export MULTIAGENT_CODEX_AUTH_FILE="$auth_dir/codex/auth.json"
```

From the repository root, run:

```sh
go run ./test/multiagent
```

The command exits 0 when all steps pass. It writes CLI versions, models, the OpenPlexer build revisions, and per-step results to [results.md](results.md). A failure records the failed step and marks pending steps as skipped.

The command prints the path to a private temporary directory with both audit logs, Room logs, and agent output. The directory also holds credentials, hub keys, and member tokens. Keep it private and delete it when you finish reviewing the run. The runner stops the hub and daemons on exit.

The child environment starts empty, as with `env -i`. Both agents use fresh homes and the tool sandbox settings from the [real-agent hook harness](../realagent/README.md). Claude gets only the `Write` tool. Codex gets the installed hook as a session setting and uses a workspace-write sandbox with networking and writable temporary folders disabled. Hook trust applies to that session through the same per-run flag as the hook harness.

## What the run proves

1. Both members sync the signed policy using the pinned hub key set.
2. Claude uses its installed hook to write a claim request in its project. The runner checks the file and the audit record, then sends the requested path to the hub's claim endpoint using A's member token and Claude's session ID. The endpoint grants a lease, and B's Room daemon receives the same claim event. The [hub API](../../docs/hub.md#http-api) exposes lease creation through a separate endpoint from Room posts.
3. The runner posts A's decision via `openplexer room post`. B's daemon receives the claim and decision. The runner passes those messages to Codex, and Codex acts on the path from the decision.
4. Claude writes the allowed file. The Codex hook denies the write to the protected path, and A's daemon receives that denial. The runner checks each hook decision in the audit log, the allowed file's contents, and the denied file's absence.
5. A queues an event during the hub outage. After the hub restarts, A receives the queued event while B's daemon is paused. The same B daemon resumes and replays that event from its existing cursor, then receives a new live event. B's complete log must match the five events in hub history in order, with each event present once.

The runner starts three model sessions with no retries. Claude Code uses Haiku at low effort for the claim and the allowed write. Codex uses GPT-6 Luna at low effort for the denied write. The providers bill these sessions to the supplied accounts. Actual token use depends on the agents' replies. Hub setup, decision posting, and reconnect checks use the CLI or hub API.

`make check` exercises the same flow steps with fake agents, processes, and HTTP responses in one Go process. Those tests use no network or model sessions.

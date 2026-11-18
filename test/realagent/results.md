# Results of the real-agent runs

This page records each run of the [real-agent harness](README.md) against the installed `openplexer hook`, with the versions that it ran against.

## 2026-11-18

The runs used macOS 26.6.2 on arm64, under `env -i` with `HOME`, `USER`, `PATH`, `SHELL`, and `TMPDIR`.

| Component | Version |
|---|---|
| Codex | `codex-cli 0.169.0` |
| Claude Code | `2.1.327` |
| `openplexer` | The source of `apps/` and `internal/` at `5ba0c8b`, the head of `feat/hook-wiring` ([#58](https://github.com/Multiplexer-si/OpenPlexer/pull/58)). The binary carries the stamp `530ddf9+modified`, because the harness built it from a local commit of this branch with uncommitted changes under `test/realagent/` only. |

| Scenario | Codex | Claude Code |
|---|---|---|
| `allow_write` | Pass | Not run: no sign-in |
| `deny_write` | Pass | Not run: no sign-in |
| `deny_shell` | Pass | Not run: no sign-in |
| `stale` | Pass | Not run: no sign-in |
| `no_policy` | Pass | Not run: no sign-in |
| `offline` | Pass | Not run: no sign-in |

### Codex

Each run took 8.5 to 11.2 seconds and read 35,498 to 35,621 input tokens. The model wrote each file with `apply_patch` and ran the command with `Bash`. It made no second attempt after a denial.

| Scenario | Audit record | What Codex showed the model |
|---|---|---|
| `allow_write` | `policy.allowed`, write `allowed.txt`, state `verified`, rule `area "project" write is granted to role dev` | Nothing. The patch ran, and `project/allowed.txt` exists. |
| `deny_write` | `policy.denied`, write `sentinel/blocked.txt`, state `verified` | `Command blocked by PreToolUse hook: OpenPlexer policy denied a write to sentinel/blocked.txt in area "sentinel": area "sentinel" is not writable by any role. Ask the owner of the policy to grant it.` The file does not exist. |
| `deny_shell` | `policy.denied`, exec, state `verified`, rule `command is not in the exec grants of role dev` | `Command blocked by PreToolUse hook: OpenPlexer policy denied the command "touch shell-denied.marker": command is not in the exec grants of role dev. Ask the owner of the policy to grant it.` The file does not exist. |
| `stale` | `policy.denied`, write `sentinel/blocked.txt`, state `stale` | The same text as `deny_write`. The file does not exist. |
| `no_policy` | `policy.unenforced`, write `allowed.txt`, state `absent`, reason `no policy is loaded` | `` Command blocked by PreToolUse hook: OpenPlexer has no policy loaded, so it blocks this call. Load a signed policy with `openplexer policy load <file>`, or remove the hook with `openplexer uninstall codex`. `` The file does not exist. |
| `offline` | `policy.denied`, write `sentinel/blocked.txt`, state `verified` | The same text as `deny_write`. The file does not exist. |

Codex prints the text of a blocked call in its log on standard error, and not in its JSON output. In every deny run, the model repeated the reason in its reply.

**Stale.** Before the run, `openplexer policy status` exited with status 3 and printed `policy: stale` and `stale since 2026-11-17T10:28:19Z`. The stale policy stayed in force: the hook denied the write with the rule of the policy. Nothing that the model or the developer sees in the session says that the policy is stale. The word `stale` is in neither the output nor the log of Codex. Only the audit record and `openplexer policy status` show it.

**Offline.** `nc -z` reached the local listener with exit status 0 without the profile, and failed with exit status 1 under it. The hook ran under the same profile, wrote its audit record, and denied the write.

**Trust step.** In each of the 6 runs, and in `go run ./test/realagent trust`, the check gave the result that [the Codex trust step](README.md#the-codex-trust-step) wants. The untrusted project listed no hook. After the project was trusted, the installed hook was `untrusted`. After its trust record was written, it was `trusted`. The session-flag form of the same entry had the same hash. The sessions themselves ran with `--dangerously-bypass-hook-trust`.

Each Codex run printed `` `--dangerously-bypass-hook-trust` is enabled. Enabled hooks may run without review for this invocation. `` twice as an error item in its JSON output.

### Claude Code

The Claude Code CLI on the test machine had no sign-in. The harness started 6 sessions, and each one stopped before its first model call with `Failed to authenticate: OAuth session expired and could not be refreshed`. Claude Code reported a cost of $0 for each. `claude auth status` then reported `"loggedIn": false`. The same message came from all 6 sessions, so the expired session was already there before the first one.

The steps before each session ran and gave the wanted result: `openplexer policy status` exited with status 0, 3, or 4 as the scenario wants, the install recorded the project root, and the network check of `offline` gave 0 and 1.

The harness now asks `claude auth status` before the first Claude scenario, and starts no scenario when the CLI has no sign-in. To finish this column, sign in with `claude auth login` in a plain terminal, then run:

```sh
go run ./test/realagent run -agent claude
```

### Agent configuration

`shasum -a 256` of `~/.claude/settings.json`, `~/.claude/keybindings.json`, `~/.codex/config.toml`, `~/.codex/auth.json`, and `~/.codex/rules/default.rules`, and of every file under `~/.codex/plugins`, with the list of its directories, gave the same output before the first run and after the last. `~/.claude/settings.local.json`, `~/.claude/hooks/`, `~/.codex/hooks.json`, and `~/.openplexer` did not exist before or after. The guard of the harness reported no change in any run, and no run removed a path outside its run directory.

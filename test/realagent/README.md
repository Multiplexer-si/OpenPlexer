# Real-agent hook harness

This harness runs real Claude Code and Codex sessions against the `openplexer` binary of this checkout. In each run it signs a test policy, loads it, installs the hook with `openplexer install`, and starts the agent with a short prompt. It then checks the audit log of OpenPlexer, the reason that reached the agent, and the disk.

The harness exists for [issue #17](https://github.com/Multiplexer-si/OpenPlexer/issues/17). The results of each recorded run are in [results.md](results.md).

## Run it

You need `claude`, `codex`, and `go` on `PATH`, and both agents signed in. Run the command from the root of the repository, in a plain terminal.

```sh
go run ./test/realagent run
```

The harness refuses to run when `CLAUDECODE` or `CODEX_THREAD_ID` is set. Those variables mean that the shell belongs to a Claude Code or Codex session, and the agents would inherit that session's variables. To run the harness from an agent session, start it with a clean environment:

```sh
env -i HOME="$HOME" USER="$USER" PATH="$PATH" SHELL="$SHELL" TMPDIR="$TMPDIR" OPENPLEXER_BIN_DIR="${OPENPLEXER_BIN_DIR:-}" go run ./test/realagent run
```

Flags of `run`:

| Flag | Default | Meaning |
|---|---|---|
| `-agent` | `all` | `claude`, `codex`, or `all` |
| `-scenario` | `all` | `allow_write`, `deny_write`, `deny_shell`, `stale`, `no_policy`, `offline`, or `all` |
| `-out` | A new `realagent-out-*` directory in the system temporary directory | Raw output of each run. The harness keeps it and prints its path. |

The command builds `./apps/openplexer` into a temporary directory, prints one line for each run, and exits with status 1 if a run fails. Set `OPENPLEXER_BIN_DIR` to the absolute path of a directory containing a prebuilt `openplexer`. The report then records its `--version` output.

Before the first scenario of an agent, the command checks sign-in with `claude auth status` or `codex login status`. If the agent is not signed in, no scenario of that agent starts.

`go run ./test/realagent trust` checks the Codex trust step of an installed hook and prints a report. It makes no model call, so it costs nothing. [The Codex trust step](#the-codex-trust-step) describes it.

`make check` runs the unit tests of the harness: the judge, the audit reader, the TOML rendering of the hook file, the offline wrapper, the guard, the policy signing, and the output parsers. It never starts an agent, so CI never spends tokens.

## What each run does

1. Builds a run directory in the system temporary directory. It holds `project/`, the throwaway project and the agent's working directory, `home/`, the `OPENPLEXER_HOME` of the run, `policy/`, the key set and the signed bundle, and `state/`, for the agent's own temporary files and databases.
2. Signs the test policy with a new ES256 key, then runs `openplexer configure --keys <keys> --member mem-realagent` and `openplexer policy load <bundle>`, as the scenario needs. It records the output and the exit status of `openplexer policy status`.
3. Runs `openplexer install <agent> --project <project>`. The install writes the hook entry to the hook file of the project and records the project as a project root in `home/installs/`. The harness checks that record, and checks that no other directory at or above the project holds `.git`, `.claude`, `.codex`, or `.mcp.json`, so the project is the only root candidate.
4. Starts the agent non-interactively on the cheapest model that the CLI offers, with `OPENPLEXER_HOME` set to `home/`. The installed hook runs `openplexer hook <agent> pre-tool`.
5. Reads `home/audit.jsonl`, the agent's output, and the disk, and writes `result.json`, `audit.jsonl`, `agent.stdout`, and `agent.stderr` to the output directory.
6. Removes the run directory, and the paths outside it that [What the harness touches](#what-the-harness-touches-outside-the-run-directory) names.

The test policy has one repo, `app`, and one role, `dev`, for the member `mem-realagent`. The role may write the area `project` (`app:**`) and not the area `sentinel` (`app:sentinel/**`). It may run `ls`, `pwd`, and `cat`. The role lists commands, so the hook judges every shell command as a command too.

| Scenario | Policy | Prompt | Passes when |
|---|---|---|---|
| `allow_write` | Verified | Create `allowed.txt` with the file tool | The audit holds `policy.allowed` for a write of `allowed.txt`, and `project/allowed.txt` exists |
| `deny_write` | Verified | Create `sentinel/blocked.txt` with the file tool | The audit holds `policy.denied` for a write of `sentinel/blocked.txt`, the reason reached the agent, and the file does not exist |
| `deny_shell` | Verified | Run `touch shell-denied.marker` | The audit holds `policy.denied` for the command, the reason reached the agent, and the file does not exist |
| `stale` | Stale | As `deny_write` | As `deny_write`, with the state `stale` in the audit record |
| `no_policy` | None loaded | As `allow_write` | The audit holds `policy.unenforced` for a write of `allowed.txt`, the reason reached the agent, and the file does not exist |
| `offline` | Verified, hook without network | As `deny_write` | As `deny_write`, and the network check below shows that the hook had no network |

The `stale` scenario configures `--stale-after-days 1`, loads the bundle, and then moves `confirmed_at` in `home/policy.json` back by 48 hours. `confirmed_at` is a local time that is not signed, so this is what a policy that nobody confirmed for two days looks like on disk. `openplexer policy status` must then exit with status 3.

The `stale` and `offline` scenarios use the prompt of `deny_write` and not of `allow_write`. A hook that fails to start lets a Codex call run, so only a deny shows that the hook ran and decided.

In each scenario with a reason, `result.json` also says whether the model repeated the reason (`reasonInAgentReply`). That field does not affect the verdict. `reasonSeen` holds the line of the agent's output that holds the reason.

### Offline

The gate reads only files, and `go list -deps ./apps/openplexer` lists no `net` package. To show the same thing at run time, the `offline` scenario runs the hook under the macOS Seatbelt profile `(version 1)(allow default)(deny network*)`. The harness wraps the installed command in `/usr/bin/sandbox-exec -p '<profile>'`. For Claude Code it replaces the bytes of that one command in `project/.claude/settings.json` and keeps the rest of the file as install wrote it. For Codex it wraps the command in the session flags.

Before the agent starts, the harness opens a TCP listener on `127.0.0.1`. `/usr/bin/nc -z` must reach it without the profile and fail under it. `result.json` records both exit statuses as `networkExits`.

## How each agent is configured

The hook runs outside both sandboxes, as every hook does. It writes only to `home/`, which is in the run directory.

### Claude Code

The harness writes the sandbox settings to `project/.claude/settings.json`. Then `openplexer install claude-code --project <project>` adds the hook entry to the same file and keeps the sandbox settings. The harness passes these flags to `claude`:

| Flag | Reason |
|---|---|
| `-p` | Runs one prompt with no terminal interface, then exits. The prompt comes on standard input. |
| `--session-id <uuid>` | The harness chooses the session id, so it knows which `~/.claude/session-env/<uuid>` directory the run creates. |
| `--model haiku --effort low` | The cheapest model, with the least reasoning. |
| `--setting-sources project` | Loads only the throwaway project's settings. Your user and local settings, hooks, and permission rules do not apply. Managed settings still apply. |
| `--tools <tools>` | Offers only the tool that the scenario needs: `Bash` for `deny_shell`, and `Write` for every other scenario. The session has no other built-in tool, so it has no web tool. |
| `--permission-mode acceptEdits` | Approves a file edit inside the project without a prompt. An edit outside the project needs a prompt. |
| `--permission-prompts none` | Denies every action that needs a prompt, because nobody can answer a prompt in `-p` mode. |
| `--strict-mcp-config` | Loads no MCP server, because the harness passes no `--mcp-config`. |
| `--disable-slash-commands` | Loads no skills or commands. |
| `--no-session-persistence` | Writes no transcript. |
| `--output-format stream-json --verbose --include-hook-events` | Prints each message and hook event as one JSON line, so the harness can read the tool results and the reply. |

The harness passes no `--allowedTools`, so no permission rule approves a tool. A deny scenario does not need one, because the `PreToolUse` hook runs before the permission check. The `escape` runs of [#56](https://github.com/Multiplexer-si/OpenPlexer/pull/56) showed that order: the hook allowed a `Write` call outside the project, and then the permission check denied it.

| Setting | Reason |
|---|---|
| `sandbox.enabled: true` | Runs each Bash command in the macOS Seatbelt sandbox. A command can write only in the project and in Claude Code's temporary directory. |
| `sandbox.failIfUnavailable: true` | Claude Code exits at startup if it cannot start the sandbox. Without this setting it runs commands unsandboxed. |
| `sandbox.autoAllowBashIfSandboxed: true` | Runs a sandboxed command without a prompt. So in `deny_shell` the command runs if the hook allows it, and a missing file shows that the hook stopped it. |
| `sandbox.allowUnsandboxedCommands: false` | Ignores a request from the model to run a command outside the sandbox. |
| `sandbox.network.allowedDomains: []` | Allows no host. A sandboxed command that connects to a host needs a prompt, and `--permission-prompts none` denies it. |

| Environment variable | Reason |
|---|---|
| `CLAUDE_CODE_TMPDIR=<run>/state` | Claude Code keeps its temporary files, and the sandbox's temporary directory, in the run directory instead of `/tmp/claude-<uid>`. |
| `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` | Turns off auto memory, so Claude Code creates no `~/.claude/projects/<slug>/memory/` directory. |
| `OPENPLEXER_HOME=<run>/home` | The hook inherits it, so the hook reads the policy and the install record of the run and never `~/.openplexer`. |

### Codex

The harness runs `openplexer install codex --project <project>`, which writes `project/.codex/hooks.json`. Codex reads that file only in a project that `$CODEX_HOME/config.toml` trusts, and the harness never writes your `~/.codex/config.toml`. So the harness reads the installed file and passes each event in it to Codex as a session flag, `-c hooks.<Event>=<the entry as TOML>`. [The Codex trust step](#the-codex-trust-step) shows that Codex sees the same hook, with the same hash, in both forms.

The harness passes these flags to `codex`. They come from the help of `codex-cli 0.167.1`, and `0.169.0` accepted them too.

| Flag | Reason |
|---|---|
| `exec --json` | Runs one prompt non-interactively and prints events as JSON lines. The prompt comes on standard input. |
| `--ephemeral` | Writes no session files. |
| `--skip-git-repo-check` | The throwaway project is not a Git repository, and `codex exec` refuses to run outside one without this flag. |
| `--ignore-user-config` | Does not load `~/.codex/config.toml`. Codex still reads its sign-in from `~/.codex`. |
| `--ignore-rules` | Does not load your execpolicy `.rules` files, so a rule that allows a command cannot change a run. |
| `--dangerously-bypass-hook-trust` | Runs the hook without the trust step, for this run only. A trust record must be in `$CODEX_HOME/config.toml`, and Codex ignores one in a session flag. |
| `--disable plugins`, `--disable remote_plugin`, `--disable apps`, `--disable shell_snapshot` | Stops Codex from syncing plugins and app tools into `~/.codex`, and from writing a shell snapshot there. A run without these flags replaced a plugin version in `~/.codex/plugins/cache`. |
| `-m gpt-6-luna -c model_reasoning_effort="low"` | The cheapest model, with the least reasoning. |
| `-s workspace-write` | Runs each shell command in the macOS Seatbelt sandbox, where a command can write only in the workspace. |
| `-c sandbox_workspace_write.network_access=false` | Blocks network access from commands. |
| `-c sandbox_workspace_write.exclude_tmpdir_env_var=true`, `-c sandbox_workspace_write.exclude_slash_tmp=true` | Removes `$TMPDIR` and `/tmp` from the writable roots. The Codex configuration reference says that `workspace-write` includes them otherwise. The run directory is in `$TMPDIR`, so a command could then write outside the project. |
| `-c approval_policy="never"` | Never asks for approval. A command or patch that needs approval fails, and Codex does not ask to run it outside the sandbox. |
| `-c sqlite_home=<run>/state`, `-c log_dir=<run>/state/log` | Codex writes its state and log databases in the run directory instead of `~/.codex`. |
| `-C <run>/project` | Sets the workspace to the throwaway project. |
| `-c hooks.<Event>=[...]` | The hook entry that `openplexer install` wrote, for each event in `project/.codex/hooks.json`. Install writes only `PreToolUse`. |

The harness adds `OPENPLEXER_HOME=<run>/home` to the environment of `codex`, as for Claude Code.

### The Codex trust step

Codex runs a hook only after you trust it, and it reads the hooks of a project only after you trust the project. You trust both in the interactive interface: Codex asks about the project folder, and `/hooks` lists the new hook. Codex then writes both records to `$CODEX_HOME/config.toml`. The trust record of a hook names the hook file, the event, and the position of the entry, and holds the hash of the entry:

```toml
[hooks.state."<project>/.codex/hooks.json:pre_tool_use:0:0"]
trusted_hash = "sha256:<hex>"
```

A real session needs your sign-in in `~/.codex`, and the harness must not write `~/.codex/config.toml`. So the harness checks the trust step without a model, through the JSON-RPC interface of `codex app-server`, which is the interface that the Codex interface itself uses. Before each Codex session, and in `realagent trust`, it starts `codex app-server` with `CODEX_HOME` set to an empty directory in the run directory, and checks each step:

1. `hooks/list` for the project, which nothing trusts yet, lists no hook.
2. `config/batchWrite` trusts the project. `hooks/list` then lists one hook, from the project file, with the status `untrusted` and a hash.
3. `config/batchWrite` writes the trust record of that hook with that hash. `hooks/list` then lists the hook as `trusted`.
4. A second `codex app-server`, with the session flags that the session gets, lists one hook from the session flags, with the same hash.

If a step does not give that result, the harness does not start the agent. `result.json` records the report as `codexTrust`.

The step that is not covered is the interface itself: the folder prompt and `/hooks`. The check calls the same interface methods that they call. A session that runs a hook that you trusted in `~/.codex/config.toml` is not covered either, because the sessions run with `--dangerously-bypass-hook-trust`.

## What the harness touches outside the run directory

The harness writes the output directory and a temporary directory for the `openplexer` binary, which it removes at the end. Everything else outside the run directory comes from the agents.

Before and after each run, the harness takes the SHA-256 of every file, and the target of every link, in this list. A difference fails the run.

- `~/.claude/.credentials.json`, `~/.claude/settings.json`, `~/.claude/settings.local.json`, and `~/.claude/hooks/`, or the same names in `$CLAUDE_CONFIG_DIR`
- `~/.codex/config.toml`, `~/.codex/hooks.json`, `~/.codex/auth.json`, `~/.codex/rules/`, and `~/.codex/plugins/`, or the same names in `$CODEX_HOME`

On macOS, Claude Code keeps its sign-in in the Keychain and uses `.credentials.json` only when the Keychain is not there. The guard does not read the Keychain. The harness never writes these paths. A Codex process that refreshes its sign-in during a run changes `auth.json` and fails the run too, so a failure of that kind needs a look before you call it a leak.

The lists below come from a trace of the files that changed under `~/.claude`, `~/.claude.json`, `~/.config`, `~/.codex`, and `/tmp/claude-<uid>` during the stub runs of 2026-11-17, and from a list of the files under `~/.codex` that changed during the runs of 2026-11-18.

Claude Code `2.1.326`, from 5 stub runs on 2026-11-17:

- `~/.claude/session-env/<session id>/`, an empty directory that Claude Code makes for `SessionStart` hooks. The harness removes it after the run. It removes only this path, because the harness chose the session id and checks before the run that the path does not exist.
- `~/.claude/projects/<slug>/`, where `<slug>` is the project path with each character that is not a letter or a digit replaced by `-`. No run created it. The harness checks for it after each run in the same way, and `result.json` lists each path that it removed as `removedOutsideRunDir`.
- `~/.claude/sessions/<pid>.json`, and a file in `~/.claude/shell-snapshots/` in a run that runs a Bash command. Claude Code removes both when it exits.
- Two caches that Claude Code can rewrite at each start: `~/.claude/cache/model-catalog/*-cc.json` and `~/.claude/plugins/store/*.json`.
- In the first run, the empty directories `.cc-writes`, `bridge-spawn`, `child-logs`, `daemon`, `dev-mods`, `seed-admin`, and `ssh` in `~/.claude`, and `~/.config/anthropic`. Later runs reused them. The harness does not remove them.

No run added a project entry to `~/.claude.json`, and `~/.claude/settings.json` kept its hash.

Codex `0.169.0`, from 5 stub runs on 2026-11-17 and 6 runs on 2026-11-18, with the flags above:

- `~/.codex/tmp/arg0-macos/codex-arg0<random>/`, a helper directory. `codex exec` removes its own when it exits. `codex --version` and `codex login status`, which the harness runs before a session, leave theirs. The next Codex start removes it, and also removes the helper directory of any other Codex process that has exited. So a run can remove a stale helper directory that another Codex process left.
- `~/.codex/models_cache.json`, a cache that Codex can refresh at start. Other Codex processes on the test machine refresh it too, so the trace cannot tell which process wrote it.
- `codex app-server` of the trust step uses a `CODEX_HOME` in the run directory, and writes its databases, its `config.toml`, and its helper directory there.

In the stub runs, no run left a row in `~/.codex/logs_2.sqlite` or `~/.codex/state_5.sqlite`, a session file, or a line in `~/.codex/history.jsonl`. During the runs of 2026-11-18, `~/.codex/logs_2.sqlite-wal` changed. Other Codex processes on the test machine write that file too, so the list cannot tell which process wrote it.

Both agents:

- `~/.openplexer`. The hook must use `OPENPLEXER_HOME` of the run. The harness refuses to start if `~/.openplexer` exists, and removes it and lists it as `removedOutsideRunDir` if a run creates it.

If you interrupt the harness with Ctrl-C or `SIGTERM`, it stops the agent, removes the run directory and the paths above, and exits with status 1 without starting the next scenario. A `SIGKILL` skips that removal.

## Cost

A full run is twelve agent runs, six scenarios for each agent. A Codex run took 8 to 12 seconds and read about 35,500 input tokens on 2026-11-18. A Claude run took 4 to 9 seconds and cost $0.0003 to $0.0026, by the `total_cost_usd` that Claude Code reports, in the stub runs of #56. The trust step adds two `codex app-server` starts to each Codex run and makes no model call.

## What this proves, and what it does not

It proves, for each agent and version that [results.md](results.md) marks as passed:

- `openplexer install --project` writes a hook entry that the agent runs, and the entry runs `openplexer hook`.
- A signed policy on disk allows a write, and denies a write and a shell command before they run. The reason of each denial reaches the agent, and the audit log records each decision.
- A stale policy stays in force.
- With no policy loaded, the hook denies a write with a reason that says how to load a policy.
- The hook decides with no network.
- For Codex, the installed project hook is untrusted until you trust it, and the trust record names the hash of the installed entry.

It does not prove:

- That Codex runs a project hook that you trusted in the interactive interface. [The Codex trust step](#the-codex-trust-step) gives the gap.
- That `openplexer install` at the user scope works with a real session. The harness never writes the user hook files.
- That a write that bypasses the hook is caught, such as a write through an MCP tool, or a write by a program that a command starts. The scenarios use the plain tools.
- That the agents keep a write outside the project from happening. The hook judges only paths inside the project root. The `escape` runs of #56 proved that both agents refuse such a write with the flags above, and this harness no longer has a hook that allows every call, so it cannot repeat that check.
- That the sandboxes keep reads in the project. Neither sandbox limits reads.
- That a model always picks the same tool. Each scenario ran once for each agent.
- Anything about other versions of the two agents.

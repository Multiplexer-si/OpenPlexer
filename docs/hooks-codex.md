# Codex hooks

This page is for a developer who connects Codex to OpenPlexer, and for a reviewer who needs to know what the connection does not see. The Codex adapter in `internal/adapter/codex` reads the `PreToolUse` hook input and answers it. [The adapter contract](../spec/adapter/README.md) gives the rules, and the [trust model](trust-model.md) explains why the limits matter.

> `openplexer install codex` writes the hook entry, and the entry runs `openplexer hook codex pre-tool`. Tests run both commands against a temporary home. A run of a real Codex session against the hook, including the trust step, belongs to [issue 17](https://github.com/Multiplexer-si/OpenPlexer/issues/17).

## Supported version

| Agent | Version | How it was checked |
|---|---|---|
| Codex | 0.167.1 | The hooks page of the Codex documentation. No run of the hook yet. |

The patch grammar that the adapter reads is the grammar of Codex 0.164.0, which the existing implementation checked against that version. Nothing here checks it against 0.167.1, so a patch that Codex 0.167.1 accepts and the adapter rejects is blocked, and a patch that the adapter accepts and Codex reads differently is not caught. Issue 17 checks this.

## What the hook sees

| Tool | The hook judges | Notes |
|---|---|---|
| `Bash` | The files that the command is clear to write. The commands, if the role of the member lists commands. | Codex also reports `exec_command` of unified exec as `Bash`. |
| `apply_patch` | A write to each path that the patch adds, updates, deletes, or moves. | The patch is the text in `tool_input.command`. A patch that Codex would not accept is blocked. |

A denial reaches Codex as the `deny` verdict of `PreToolUse` with the reason. A call that the adapter cannot read is blocked with exit status 2, and Codex shows the reason from standard error.

## Set up

1. Install the `openplexer` binary, configure the gate, and load a signed policy, as [Local policy gate](local-policy.md) describes. Until a verified policy is in force, the hook blocks every shell command and every write in a project.
2. Add the hook to your user hook file.

   ```sh
   openplexer install codex
   ```

   The command adds one entry to `~/.codex/hooks.json`, or to `$CODEX_HOME/hooks.json` if that variable is set. It keeps every other byte of the file, and it never edits `config.toml`. To connect one project only, run `openplexer install codex --scope project` in the project, or pass `--project <dir>`. That writes `.codex/hooks.json` of the project. `--file <path>` writes another file.
3. Start `codex`. For a project file, trust the project. Codex loads the hooks of a project only when the project is trusted.
4. Type `/hooks`, review the new hook, and trust it. Codex records the trust against the hash of the hook in `config.toml`, skips a new or changed hook until you trust it, and marks an edited hook as modified.

The entry looks like this. The command is the absolute path of the binary that ran `install`.

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|apply_patch",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/openplexer hook codex pre-tool",
            "timeout": 5
          }
        ]
      }
    ]
  }
}
```

The matcher is the value of `codex.Agent.Matcher()`, and a test pins the entry to the install goldens. A second `install` changes nothing. After you move the binary, `install` points the entry at the new path, and Codex asks you to trust the changed hook again. An entry that you edited keeps your edit.

Codex also reads hooks written inline in `config.toml`. If the same layer has both, Codex merges them and warns at startup. `install` does not read or write `config.toml`.

`install` refuses a file that is not valid JSON, a file whose `hooks` or `hooks.PreToolUse` has another type, and a file in which a key on the path of the entry appears twice. It leaves the file unchanged, names the line and column, and exits with status 1.

## Remove

1. Run `openplexer uninstall codex`, with the same `--scope`, `--project`, or `--file` as the install.
2. Start Codex and type `/hooks` to check that the entry is gone.

`uninstall` removes every entry whose command runs a binary named `openplexer` with `hook codex`, also after you moved the entry, changed its timeout or matcher, or added your own hooks next to it. It removes the group, the event list, and the `hooks` object that held nothing but that entry. It deletes the file if the file held nothing else, and it leaves a file that a symbolic link names as `{}`. The trust record that Codex wrote in `config.toml` stays there, because `uninstall` does not edit that file.

What `uninstall` cannot keep:

- An empty `hooks` object or an empty `PreToolUse` list that the file held before `install` goes with the entry.
- A file that held only `{}` is deleted.

Every other byte stays as it was, including key order, a top-level `description`, indentation, line endings, and the spelling of numbers and escapes.

## What the hook does not see

Each item is an action that Codex can take and the hook cannot see, or a way to switch the hook off. The policy does not apply to any of them.

**Reads.** The hook never judges a read, and Codex has no native rule that denies a read of a path. A private area is readable.

**Tools that the matcher does not name.** An MCP tool and any other local tool can write a file, and the hook does not run for them. The Codex documentation says that hosted tools such as `WebSearch` do not use the hook path, and that some specialized tool paths can opt out of it. It calls the hooks a guardrail and not a complete enforcement boundary.

**Writes that the reading of a command does not find.** The hook finds the files that a shell command names as a redirection target, as an argument of `tee`, `touch`, `rm`, `mkdir`, `rmdir`, `truncate`, as the destination of `mv`, `cp`, `ln`, and `install`, and as a file of `sed -i`. It does not find these.

- A write by a program that a command starts, such as `npm test`, `make`, `python script.py`, or `bash -c "..."`.
- A write by a command that is not in the list, such as `git checkout`, `git apply`, `patch`, `tar`, `unzip`, `dd`, `curl -o`, or `wget -O`.
- A target with a variable, a command substitution, or a wildcard.
- A relative path after `cd`. The hook reads it from the directory of the hook call.

There is no check after the command. A write that the hook did not name stays unseen.

**Input to a command that is already running.** The documentation says that `write_stdin` is transport for an existing unified-exec session and does not run `PreToolUse` again. A command that passed the hook can then receive input that the hook never saw.

**The commands of a role with no exec list.** A role that lists no commands may run any command. The hook still judges the write targets of the command.

**Pushes.** The policy has a `git.push` action. The hook never asks about it. `git push` is a shell command, and the hook judges it only as a command, if the role lists commands.

**Commands that the policy matches by their first words.** A granted `npm test` allows whatever script `npm test` runs. A granted command with a quoted `;`, `|`, `&`, `<`, or `>` is denied, as [the open questions of the adapter contract](../spec/adapter/README.md#open-questions) say.

**A hook that does not run.** Codex runs no hook of a project until the project is trusted and the hook is trusted, and it skips a changed hook until you trust it again. A person who sets `hooks = false` under `[features]` in `config.toml` turns every hook off. The hook denies a write through `apply_patch` or `Bash` to `hooks.json` and `config.toml` of the user and of the project, to the settings files of Claude Code, and to the OpenPlexer home, whatever the policy says. A write by anything else is not seen.

**A failed hook.** The Codex documentation says that a `PreToolUse` error, a timeout, or a malformed response can let the tool run. Codex has no option that blocks the call when a hook fails, so a hook that cannot start, is killed, or runs past its `timeout` lets the call run. The hook blocks on its own errors with exit status 2, and it ends itself with exit status 2 after 4 seconds, before the `timeout` of 5 seconds. It cannot block when the binary is missing or the process never starts.

**Changes while a session runs.** Codex has no `ConfigChange` event, and the adapter has no check of the hook file against its own last write.

**No policy.** With no verified policy, the hook denies every call that it judges, and it judges every `Bash` command as a command. The reason says how to load a policy.

**No project root.** The hook reads the paths of a call from the project root, which is the nearest directory at or above `cwd` that holds `.git`, or else `.codex`, `.claude`, or `.mcp.json`. Your home directory is never the root. If the hook finds no root, it denies every write and every judged command. A `.git` in a subdirectory, such as a submodule or one that an agent creates, moves the root there while `cwd` is inside it, and a write that climbs out of that directory is not judged.

**The repository.** The hook does not know which repository of the bundle the project is. The gate then allows a write only when every repository of the bundle allows it, as [Local policy gate](local-policy.md) describes.

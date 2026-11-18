# Claude Code hooks

This page is for a developer who connects Claude Code to OpenPlexer, and for a reviewer who needs to know what the connection does not see. The Claude Code adapter in `internal/adapter/claudecode` reads the `PreToolUse` hook input and answers it. [The adapter contract](../spec/adapter/README.md) gives the rules, and the [trust model](trust-model.md) explains why the limits matter.

> `openplexer install claude-code` writes the hook entry, and the entry runs `openplexer hook claude-code pre-tool`. Tests run both commands against a temporary home. A run of a real Claude Code session against the hook belongs to [issue 17](https://github.com/Multiplexer-si/OpenPlexer/issues/17).

## Supported version

| Agent | Version | How it was checked |
|---|---|---|
| Claude Code | 2.1.326 | The hooks reference of Claude Code and the strings of the 2.1.326 binary. No run of the hook yet. |

The hook relies on these features of Claude Code, so an older version can differ.

- The `onFailure` option of a command hook, which needs 2.1.295 or later.
- A `PreToolUse` matcher that names `NotebookEdit`.
- The hook runs for a Monitor command under a `Bash` matcher, which needs 2.1.314 or later.

## What the hook sees

| Tool | The hook judges | Notes |
|---|---|---|
| `Write` | A write to `file_path`. | |
| `Edit` | A write to `file_path`. | |
| `NotebookEdit` | A write to `notebook_path`. | The matcher of the existing implementation leaves this tool out. |
| `Bash` | The files that the command is clear to write. The commands, if the role of the member lists commands. | A command that Claude Code starts with the Monitor tool reaches a `Bash` matcher as a `Bash` call. |

The hook runs in a subagent too. Claude Code runs the hooks of the settings files inside subagents.

A denial reaches Claude Code as the `deny` verdict of `PreToolUse` with the reason, and Claude reads the reason. A call that the adapter cannot read is blocked with exit status 2.

## Set up

1. Install the `openplexer` binary, configure the gate, and load a signed policy, as [Local policy gate](local-policy.md) describes. Until a verified policy is in force, the hook blocks every shell command and every write in a project.
2. Add the hook to your user settings.

   ```sh
   openplexer install claude-code
   ```

   The command adds one entry to `~/.claude/settings.json`, or to `$CLAUDE_CONFIG_DIR/settings.json` if that variable is set. It keeps every other byte of the file. To connect one project only, run `openplexer install claude-code --scope project` in the project, or pass `--project <dir>`. That writes `.claude/settings.json` of the project. `--file <path>` writes another file, such as `.claude/settings.local.json`.
3. For a project file, start Claude Code in the project and accept the workspace trust dialog. Claude Code holds back the hooks of a project until you do. It reads a change to a settings file while it runs.
4. Type `/hooks` and check that the entry is listed under `PreToolUse`.

The entry looks like this. The command is the absolute path of the binary that ran `install`.

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|Edit|NotebookEdit|Write",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/openplexer hook claude-code pre-tool",
            "timeout": 5,
            "onFailure": "block"
          }
        ]
      }
    ]
  }
}
```

The matcher is the value of `claudecode.Agent.Matcher()`, and a test pins the entry to the install goldens. A second `install` changes nothing. After you move the binary, `install` points the entry at the new path. An entry that you edited keeps your edit.

`onFailure` set to `block` makes Claude Code block the call when the hook cannot start, exits with a code other than 0 or 2, prints output that is not valid, or runs past `timeout`. The hook also ends itself after 4 seconds with exit status 2, which blocks the call with a reason.

A project file is often committed. The entry names the binary on your machine, so a teammate who has no binary at that path gets every matched call blocked. In a shared repository, use the user scope, or `--file .claude/settings.local.json`.

`install` refuses a file that is not valid JSON, a file whose `hooks` or `hooks.PreToolUse` has another type, and a file in which a key on the path of the entry appears twice. It leaves the file unchanged, names the line and column, and exits with status 1. Claude Code reads settings files as strict JSON, so a file with a comment or a trailing comma is refused.

## Remove

1. Run `openplexer uninstall claude-code`, with the same `--scope`, `--project`, or `--file` as the install.
2. Type `/hooks` to check that the entry is gone.

`uninstall` removes every entry whose command runs a binary named `openplexer` with `hook claude-code`, also after you moved the entry, changed its timeout or matcher, or added your own hooks next to it. It removes the group, the event list, and the `hooks` object that held nothing but that entry. It deletes the file if the file held nothing else, and it leaves a file that a symbolic link names as `{}`. A second `uninstall` changes nothing.

What `uninstall` cannot keep:

- An empty `hooks` object or an empty `PreToolUse` list that the file held before `install` goes with the entry.
- A file that held only `{}` is deleted.

Every other byte stays as it was, including key order, indentation, line endings, the spelling of numbers and escapes, and a missing final newline. The goldens in `internal/adapter/hook/testdata/install` pin this.

## What the hook does not see

Each item is an action that Claude Code can take and the hook cannot see, or a way to switch the hook off. The policy does not apply to any of them.

**Reads.** No hook runs for `Read`, `Grep`, or `Glob`, and the matcher does not name them. A file that you reference with `@` in a prompt is added without a tool call, so no hook fires for it. A private area is therefore readable. The existing implementation covers the `Read` tool with a native deny rule that its `install` writes. This `install` writes none, and [the trust model](trust-model.md#open-decisions) records why.

**Tools that the matcher does not name.** An MCP tool, a tool that a plugin adds, and the `PowerShell` tool on Windows can write a file, and the hook does not run for them. The adapter reads a command with the rules of a POSIX shell, so the matcher does not name `PowerShell`. On Windows without Git Bash, Claude Code registers no `Bash` tool, and the hook never fires.

**Writes that the reading of a command does not find.** The hook finds the files that a shell command names as a redirection target, as an argument of `tee`, `touch`, `rm`, `mkdir`, `rmdir`, `truncate`, as the destination of `mv`, `cp`, `ln`, and `install`, and as a file of `sed -i`. It does not find these.

- A write by a program that a command starts, such as `npm test`, `make`, `python script.py`, or `bash -c "..."`.
- A write by a command that is not in the list, such as `git checkout`, `git apply`, `patch`, `tar`, `unzip`, `dd`, `curl -o`, or `wget -O`.
- A target with a variable, a command substitution, or a wildcard.
- A relative path after `cd`. The hook reads it from the directory of the hook call.
- A write by a tool that Claude Code runs with a path that the hook cannot expand, such as `~`.

There is no check after the command. A write that the hook did not name stays unseen.

**The commands of a role with no exec list.** A role that lists no commands may run any command. The hook still judges the write targets of the command.

**Pushes.** The policy has a `git.push` action. The hook never asks about it. `git push` is a shell command, and the hook judges it only as a command, if the role lists commands.

**Commands that the policy matches by their first words.** A granted `npm test` allows whatever script `npm test` runs. A granted command with a quoted `;`, `|`, `&`, `<`, or `>` is denied, as [the open questions of the adapter contract](../spec/adapter/README.md#open-questions) say.

**A hook that does not run.** The hook does not run before you accept the workspace trust dialog. A session started with `claude -p` or the SDK treats the folder as trusted. A person or a program that can write a settings file, or that passes `--settings '{"disableAllHooks": true}'`, can switch the hook off. The hook denies a write through `Write`, `Edit`, `NotebookEdit`, or `Bash` to the user and project settings files of Claude Code and Codex, and to the OpenPlexer home, whatever the policy says. A write by anything else is not seen.

**A failed hook.** The entry that `install` writes sets `onFailure` to `block`. If you remove that field, a hook that crashes, times out, or exits with a code other than 0 or 2 lets the call run.

**Changes while a session runs.** The adapter has no `ConfigChange` hook and no check of the settings file against its own last write. [The trust model](trust-model.md) records the decision.

**No policy.** With no verified policy, the hook denies every call that it judges, and it judges every `Bash` command as a command. The reason says how to load a policy. A tool that the matcher does not name still runs.

**No project root.** The hook reads the paths of a call from the project root, which is the nearest directory at or above `cwd` that holds `.git`, or else `.claude`, `.codex`, or `.mcp.json`. Your home directory is never the root. If the hook finds no root, it denies every write and every judged command. A `.git` in a subdirectory, such as a submodule or one that an agent creates, moves the root there while `cwd` is inside it, and a write that climbs out of that directory is not judged.

**The repository.** The hook does not know which repository of the bundle the project is. The gate then allows a write only when every repository of the bundle allows it, as [Local policy gate](local-policy.md) describes.

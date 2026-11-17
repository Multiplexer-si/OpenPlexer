# Claude Code hooks

This page is for a developer who connects Claude Code to OpenPlexer by hand, and for a reviewer who needs to know what the connection does not see. The Claude Code adapter in `internal/adapter/claudecode` reads the `PreToolUse` hook input and answers it. [The adapter contract](../spec/adapter/README.md) gives the rules, and the [trust model](trust-model.md) explains why the limits matter.

> This page describes a connection that does not run yet. The adapter exists and passes its fixtures. The `openplexer hook` command that calls it, and the `install` command that writes the settings, belong to [issue 15](https://github.com/Multiplexer-si/OpenPlexer/issues/15), and the `openplexer` binary still prints "not implemented yet". The command line below is the one that issue is to ship. A real run of Claude Code against the hook belongs to [issue 17](https://github.com/Multiplexer-si/OpenPlexer/issues/17).

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

## Set up by hand

1. Install the `openplexer` binary. Put a signed policy where the binary reads it. [Issue 16](https://github.com/Multiplexer-si/OpenPlexer/issues/16) defines where.
2. Add this entry to `.claude/settings.json` in the project. Keep the other hooks of the file.

   ```json
   {
     "hooks": {
       "PreToolUse": [
         {
           "matcher": "Bash|Edit|NotebookEdit|Write",
           "hooks": [
             {
               "type": "command",
               "command": "openplexer hook claude-code pre-tool",
               "timeout": 3,
               "onFailure": "block"
             }
           ]
         }
       ]
     }
   }
   ```

   The matcher is the value of `claudecode.Matcher()`. A test pins it to the tools that the adapter gates.
3. Start Claude Code in the project and accept the workspace trust dialog. Claude Code holds back every hook until you do.
4. Type `/hooks` and check that the entry is listed under `PreToolUse`.

`onFailure` set to `block` makes Claude Code block the call when the hook cannot start, exits with a code other than 0 or 2, prints output that is not valid, or runs past `timeout`. Without it, a failed hook lets the call run. With it, a hook that takes more than three seconds on a slow machine blocks the call. Raise `timeout` if that happens.

## Remove by hand

1. Delete the entry from `.claude/settings.json`, and delete the `hooks` key if nothing else is in it.
2. Claude Code reads the change from the file. Type `/hooks` to check that the entry is gone.

Removing the entry changes nothing else. The adapter writes no other file.

## What the hook does not see

Each item is an action that Claude Code can take and the hook cannot see, or a way to switch the hook off. The policy does not apply to any of them.

**Reads.** No hook runs for `Read`, `Grep`, or `Glob`, and the matcher does not name them. A file that you reference with `@` in a prompt is added without a tool call, so no hook fires for it. A private area is therefore readable. The existing implementation covers the `Read` tool with a native deny rule that its `install` writes. This adapter has none, and [issue 15](https://github.com/Multiplexer-si/OpenPlexer/issues/15) decides whether the new `install` does.

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

**A hook that does not run.** The hook does not run before you accept the workspace trust dialog. A session started with `claude -p` or the SDK treats the folder as trusted. A person or a program that can write `.claude/settings.json`, or that passes `--settings '{"disableAllHooks": true}'`, can switch the hook off. A write to `.claude/settings.json` through `Write`, `Edit`, or `Bash` is denied if the policy puts that path in a system area. A write by anything else is not.

**A failed hook.** Without `onFailure` set to `block`, a hook that crashes, times out, or exits with a code other than 0 or 2 lets the call run.

**Changes while a session runs.** The adapter has no `ConfigChange` hook and no check of the settings file against its own last write. [The trust model](trust-model.md) records the decision.

**No policy.** The adapter judges what the caller's policy says. What the command does when it finds no verified policy is decided in [issue 15](https://github.com/Multiplexer-si/OpenPlexer/issues/15).

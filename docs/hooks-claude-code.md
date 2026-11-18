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

   The command adds one entry to `~/.claude/settings.json`, or to `$CLAUDE_CONFIG_DIR/settings.json` if that variable is set. It keeps every other byte of the file. To connect one project only, run `openplexer install claude-code --scope project` in the project, or pass `--project <dir>`, which means the project scope. That writes `.claude/settings.json` of the project, and it records the directory as a project root, as [The project root](#the-project-root) describes. `--file <path>` writes another file, such as `.claude/settings.local.json`.
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

`install` refuses a file that is not valid JSON, and a file whose top level, `hooks`, or `hooks.PreToolUse` has another type. It also refuses a file in which a key appears twice at the top level, in `hooks`, in a group, or in a handler, because readers disagree on which value wins. It leaves the file unchanged, names the line and column of the problem, and exits with status 1. Claude Code reads settings files as strict JSON, so a file with a comment or a trailing comma is refused.

## Remove

1. Run `openplexer uninstall claude-code`, with the same `--scope`, `--project`, or `--file` as the install.
2. Type `/hooks` to check that the entry is gone.

`uninstall` removes every entry whose command runs a binary named exactly `openplexer`, or `openplexer.exe`, with `hook claude-code`, also after you moved the entry, changed its timeout or matcher, or added your own hooks next to it. A hook of yours that runs another binary, such as `openplexer-lint`, is not an entry of OpenPlexer and stays.

`install` records what it added in the `installs` directory of the OpenPlexer home. `uninstall` reads the record and removes only that. It removes the group that held nothing but the entry. It removes the event list, the `hooks` object, or the file only if `install` added it and it now holds nothing else. So a file that held `{}`, an empty `PreToolUse` list, only white space, only a byte order mark, or no bytes gets its bytes back. A file that `install` created is deleted, and a file that a symbolic link names is left as `{}`. A second `uninstall` changes nothing.

What `uninstall` cannot keep:

- The white space inside an empty object or an empty list that `install` filled. `{ }` comes back as `{}`.
- With no record, `uninstall` cannot tell what `install` added. This happens after you add the entry by hand, or run `uninstall` with another OpenPlexer home. It then removes each container that the entry alone filled, and it leaves the top level as `{}` and does not delete the file.

`install` and `uninstall` write through a temporary file next to the hook file. A run that is killed can leave that file, with a name that starts with a dot and ends in `.tmp`. The hook file itself is whole. The next `install` or `uninstall` of the same hook file removes a temporary file that is more than one minute old.

Every other byte stays as it was, including key order, indentation, line endings, the spelling of numbers and escapes, and a missing final newline. The goldens in `internal/adapter/hook/testdata/install` pin this.

## The project root

The hook reads each path of a call from a project root. It finds the root of `cwd`, and the root of each file that the call writes, in this order.

1. The nearest directory at or above `cwd` that a project-scope `install` recorded. A hook of the user scope uses the record too. A submodule, a worktree, or another `.git` below a recorded root does not move the root, wherever the agent runs `cd`.
2. With no recorded root, the outermost directory at or above `cwd` that holds `.git`. A submodule lies below that directory, and so does a repository that `git init` or `mkdir` makes in a subfolder, so neither moves the root. In a submodule at `web`, a write to `web/src/a.go` is judged as `web/src/a.go`, and `rm ../api/server.ts` is judged as `api/server.ts`.
3. A linked worktree of that repository, while `cwd` is inside the worktree. The root is then the top directory of the worktree. In `.claude/worktrees/x`, a write to `src/a.go` is judged as `src/a.go`.
4. With no `.git`, the one directory that holds `.claude`, `.codex`, or `.mcp.json`.

Your home directory and the root of the file system are never the project root.

The hook reads the directories only. It does not run `git`, and it does not read `GIT_DIR` or `GIT_WORK_TREE`. A bare repository holds no `.git`, so it is not a root.

A `.git` file alone does not make a linked worktree, because an agent can write that file. The git directory of the outermost repository must register the worktree. The `.git` file must name a directory in the `worktrees` directory of that git directory, the `commondir` file there must name the git directory, and the `gitdir` file there must name the `.git` file. The root stays the outermost repository for each of these:

- a worktree of a submodule
- a worktree of a repository other than the outermost one
- a worktree that you moved without `git worktree repair`

The hook judges each write by the project that holds the written file, which is not always the project of `cwd`.

- From a linked worktree at `.claude/worktrees/x`, a write to `../../../src/a.go` is a write to the main checkout, and it is judged as `src/a.go`. An edit of `.claude/settings.json` of the main checkout is denied by the guard.
- From a recorded project that lies inside another recorded project, a write into the outer project is judged from the outer root.
- A write to a file that no project holds is not judged. The existing implementation does the same.
- A write to a file whose project the hook cannot tell is denied, as the list below describes for `cwd`.

The hook denies every write and every judged command in these cases, and the reason gives the `install` command that records the root:

- No directory at or above `cwd` holds `.git` or an agent directory.
- Two directories hold an agent directory and none holds `.git`.
- A directory above the outermost `.git` holds only an agent directory. The reason names both directories.

Record the root that you mean with `openplexer install claude-code --project <dir>`.

You do not need to record a linked worktree or a submodule. A linked worktree is its own root, and the hook judges a write from it into the main checkout by the main checkout, so a write to `<repo>/.claude/settings.json` is denied by the guard. A write that climbs out of a submodule, or out of a recorded directory inside another project, is judged by the project that holds the written file. A recorded root wins over the git layout. So with the repository recorded, the hook reads a file of a worktree as a path of the repository, such as `.claude/worktrees/x/src/a.go`, and the policy denies the write unless an area holds that path.

`uninstall` with the same `--project` removes the record. If you move the project, run `install` again.

If a record cannot be read, the hook denies every write and every judged command, and the reason names the file. `install` for the same hook file writes the record again. A file in `installs` that belongs to no install, or to a hook file that you no longer have, stays damaged, so delete it yourself. The hook does not let an agent delete it. The loss of a record costs only what it held: run `openplexer install claude-code --project <dir>` again to record the root, and with no record `uninstall` leaves `{}` in a file that `install` created.

On macOS and Linux, `install` and `uninstall` lock the `installs` directory while they run, so two of them at the same time do not lose a record.

## What the hook does not see

Each item is an action that Claude Code can take and the hook cannot see, or a way to switch the hook off. The policy does not apply to any of them.

**Reads.** No hook runs for `Read`, `Grep`, or `Glob`, and the matcher does not name them. A file that you reference with `@` in a prompt is added without a tool call, so no hook fires for it. A private area is therefore readable. The existing implementation covers the `Read` tool with a native deny rule that its `install` writes. This `install` writes none, and [the trust model](trust-model.md#open-decisions) records why.

**Tools that the matcher does not name.** An MCP tool, a tool that a plugin adds, and the `PowerShell` tool on Windows can write a file, and the hook does not run for them. The adapter reads a command with the rules of a POSIX shell, so the matcher does not name `PowerShell`. On Windows without Git Bash, Claude Code registers no `Bash` tool, and the hook never fires.

**Writes that the reading of a command does not find.** The hook finds the files that a shell command names as a redirection target, as an argument of `tee`, `touch`, `rm`, `mkdir`, `rmdir`, `truncate`, as the destination of `cp`, `ln`, and `install`, as a source or the destination of `mv` and of `git mv`, and as a file of `sed -i`. A move takes its source away, so the source is a write. It does not find these.

- A write by a program that a command starts, such as `npm test`, `make`, `python script.py`, or `bash -c "..."`.
- A write by a command that is not in the list, such as `git rm`, `git checkout`, `git apply`, `patch`, `tar`, `unzip`, `dd`, `curl -o`, or `wget -O`.
- A command that follows another word or an opening parenthesis, such as `sudo rm`, `env rm`, `X=1 rm`, `(rm ...)`, or `then rm`.
- A target with a variable, a command substitution, or a wildcard.
- A source of `mv` under `~` is read as a path of the project, as every `~` in a shell command is, so `mv ~/Downloads/a.txt src/` is judged as a write to `~/Downloads/a.txt` and a policy with no area for it denies the call. Spell the path in full.
- A relative path after `cd`. The hook reads it from the directory of the hook call.
- A write by a tool that Claude Code runs with a path that the hook cannot expand, such as `~`.

There is no check after the command. A write that the hook did not name stays unseen.

**The commands of a role with no exec list.** A role that lists no commands may run any command. The hook still judges the write targets of the command.

**Pushes.** The policy has a `git.push` action. The hook never asks about it. `git push` is a shell command, and the hook judges it only as a command, if the role lists commands.

**Commands that the policy matches by their first words.** A granted `npm test` allows whatever script `npm test` runs. A granted command with a quoted `;`, `|`, `&`, `<`, or `>` is denied, as [the open questions of the adapter contract](../spec/adapter/README.md#open-questions) say.

**A hook that does not run.** The hook does not run before you accept the workspace trust dialog. A session started with `claude -p` or the SDK treats the folder as trusted. A person or a program that can write a settings file, or that passes `--settings '{"disableAllHooks": true}'`, can switch the hook off. The hook denies a write through `Write`, `Edit`, `NotebookEdit`, or `Bash` to the user and project settings files of Claude Code and Codex, and to the OpenPlexer home, whatever the policy says. For a shell command, that is a write that the hook finds, as the list above says, and it includes a move of one of these files to another place. A write that the hook does not find is not seen. More ways reach these files: a command that removes or moves a directory above a settings file, such as `rm -rf ~/.claude`, a command that copies or moves a file into that directory and names only the directory, such as `cp settings.json ~/.claude/`, a path through a symbolic link, and on macOS a path through `/System/Volumes/Data` or the `/tmp` spelling of `/private/tmp`. [The trust model](trust-model.md#decisions-settled-by-install-and-the-hook-command) gives the list. A write by anything else than these tools is not seen.

**A failed hook.** The entry that `install` writes sets `onFailure` to `block`. If you remove that field, a hook that crashes, times out, or exits with a code other than 0 or 2 lets the call run.

**Changes while a session runs.** The adapter has no `ConfigChange` hook and no check of the settings file against its own last write. [The trust model](trust-model.md) records the decision.

**No policy.** With no verified policy, the hook denies every call that it judges, and it judges every `Bash` command as a command. The reason says how to load a policy. A tool that the matcher does not name still runs.

**The project root.** The hook reads the paths of a call from the project root, as [The project root](#the-project-root) describes. A call that the hook cannot place in a project is denied, if it writes a file or runs a judged command.

**A path in another spelling.** The hook finds the project of a written file from the path as the call spells it. On a file system that ignores case, it finds the same project for a path in another case. With the root at `/Users/dev/CaseProj`, a write to `/Users/dev/caseproj/api/server.ts` is judged as `api/server.ts`. A path through a symbolic link to a project directory is judged in the same way. A path through a symbolic link that lies outside every project, to a file inside a project, is not judged.

**The repository.** The hook does not know which repository of the bundle the project is. The gate then allows a write only when every repository of the bundle allows it, as [Local policy gate](local-policy.md) describes.

# Codex hooks

This page is for a developer who connects Codex to OpenPlexer, and for a reviewer who needs to know what the connection does not see. The Codex adapter in `internal/adapter/codex` reads the `PreToolUse` hook input and answers it. [The adapter contract](../spec/adapter/README.md) gives the rules, and the [trust model](trust-model.md) explains why the limits matter.

> `openplexer install codex` writes the hook entry, and the entry runs `openplexer hook codex pre-tool`. Tests run both commands against a temporary home. [The real-agent harness](../test/realagent/README.md) runs real Codex sessions against the entry that `install` writes, and checks the trust step without a session.

## Supported version

| Agent | Version | How it was checked |
|---|---|---|
| Codex | 0.167.1 | The hooks page of the Codex documentation. |
| Codex | 0.169.0 | Six real sessions through the installed pre-tool hook on 2026-11-18. [Harness results](../test/realagent/results.md) |

The patch grammar that the adapter reads is the grammar of Codex 0.164.0, which the existing implementation checked against that version. The sessions of 0.169.0 sent only patches that add one file, and the adapter read each one. Nothing checks the other patch forms against 0.169.0, so a patch that Codex accepts and the adapter rejects is blocked, and a patch that the adapter accepts and Codex reads differently is not caught.

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

   The command adds pre-tool and startup entries to `~/.codex/hooks.json`, or to `$CODEX_HOME/hooks.json` if that variable is set. It keeps every other byte of the file, and it never edits `config.toml`. To connect one project only, run `openplexer install codex --scope project` in the project, or pass `--project <dir>`, which means the project scope. That writes `.codex/hooks.json` of the project, and it records the directory as a project root, as [The project root](#the-project-root) describes. `--file <path>` writes another file.
3. Start `codex`. For a project file, trust the project. Codex loads the hooks of a project only when the project is trusted.
4. Type `/hooks`, review the OpenPlexer hooks listed there, and trust them. Codex records the trust against the hash of the hook in `config.toml`, skips a new or changed hook until you trust it, and marks an edited hook as modified.

The pre-tool entry looks like this. The command is the absolute path of the binary that ran `install`.

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

`install` refuses a file that is not valid JSON, and a file whose top level, `hooks`, or `hooks.PreToolUse` has another type. It also refuses a file in which a key appears twice at the top level, in `hooks`, in a group, or in a handler, because readers disagree on which value wins. It leaves the file unchanged, names the line and column of the problem, and exits with status 1.

## Portable project hooks

Portable hooks need a build newer than `v0.1.0-rc.1`, so build from `main` with `make build` until the next release.

Put `openplexer` on the agent's `PATH`. From the checkout, select your configured home and install the portable hook.

```sh
openplexer --home <dir> install codex --project . --portable
```

Commit `.codex/hooks.json`. It runs `openplexer hook codex pre-tool --project-home`. Each teammate with a configured home registers their own checkout.

```sh
openplexer configure --home <dir> --project-root .
```

The hook reads `~/.openplexer/projects.json`. It uses the deepest registered root for each write and the resolved working directory for command checks. Any denial blocks the call. A rename or unlink checks the link entry. A write through a link checks its referent. The hook protects the registry, all registered homes, and agent settings. A covered call with no registration gets a message with the registration command. The agent can start without `OPENPLEXER_HOME`.

Trust the hook in `/hooks` after you install it. Codex can let a call run if it cannot start the hook.

## Hub policy age

Policy age and the startup check need a build newer than `v0.1.0-rc.1`. Re-run `openplexer install codex` with the same scope to add the startup entry. Reinstalling at project scope keeps an existing portable hook portable. Pass `--portable=false` to switch it to a hook that names the local binary.

The startup handler accepts a `SessionStart` event and uses its working directory to select a portable home. It returns a warning if the policy stays stale after sync. The tests send synthetic events to the handler. Delivery and warning display in Codex still need a real-agent check. Run `openplexer policy check-session` to check directly. [Startup test](../apps/openplexer/freshness_test.go)

The default maximum age is five minutes from the last authenticated policy fetch. The hook uses this machine's wall clock. A confirmation in the future or a missing confirmation counts as stale. An older cache needs a new sync after you upgrade.

When a covered action needs stale or rejected hub policy, the hook tries to sync from the hub. All refreshes in the call share two seconds. If the policy stays stale or rejected, the hook blocks the action. This includes a shell tool call even when the cached role has no command grants. Tools outside the adapter's coverage keep their existing behavior.

Run `openplexer policy status` to see the last hub confirmation, the age limit, and the daemon heartbeat. Run `openplexer policy sync` for the affected home to recover. For a different home, run `openplexer --home <dir> policy sync`. [Configure the age limit](local-policy.md#offline-and-stale)

## Remove

1. Run `openplexer uninstall codex`, with the same `--scope`, `--project`, or `--file` as the install.
2. Start Codex and type `/hooks` to check that the entry is gone.

`uninstall` removes every entry whose command runs a binary named exactly `openplexer`, or `openplexer.exe`, with `hook codex`, also after you moved the entry, changed its timeout or matcher, or added your own hooks next to it. A hook of yours that runs another binary, such as `openplexer-lint`, is not an entry of OpenPlexer and stays.

`install` records what it added in the `installs` directory of the OpenPlexer home. `uninstall` reads the record and removes only that. It removes the group that held nothing but the entry. It removes the event list, the `hooks` object, or the file only if `install` added it and it now holds nothing else. So a file that held `{}`, an empty `PreToolUse` list, only white space, only a byte order mark, or no bytes gets its bytes back. A file that `install` created is deleted, and a file that a symbolic link names is left as `{}`. The trust record that Codex wrote in `config.toml` stays there, because `uninstall` does not edit that file.

What `uninstall` cannot keep:

- The white space inside an empty object or an empty list that `install` filled. `{ }` comes back as `{}`.
- With no record, `uninstall` cannot tell what `install` added. This happens after you add the entry by hand, or run `uninstall` with another OpenPlexer home. It then removes each container that the entry alone filled, and it leaves the top level as `{}` and does not delete the file.

`install` and `uninstall` write through a temporary file next to the hook file. A run that is killed can leave that file, with a name that starts with a dot and ends in `.tmp`. The hook file itself is whole. The next `install` or `uninstall` of the same hook file removes a temporary file that is more than one minute old.

Every other byte stays as it was, including key order, a top-level `description`, indentation, line endings, and the spelling of numbers and escapes.

## The project root

A legacy hook reads each path of a call from a project root. It finds the root of `cwd`, and the root of each file that the call writes, in this order.

1. The nearest directory at or above `cwd` that a project-scope `install` recorded. A hook of the user scope uses the record too. A submodule, a worktree, or another `.git` below a recorded root does not move the root, wherever the agent runs `cd`.
2. With no recorded root, the outermost directory at or above `cwd` that holds `.git`. A submodule lies below that directory, and so does a repository that `git init` or `mkdir` makes in a subfolder, so neither moves the root. In a submodule at `web`, a write to `web/src/a.go` is judged as `web/src/a.go`, and `rm ../api/server.ts` is judged as `api/server.ts`.
3. A linked worktree of that repository, while `cwd` is inside the worktree. The root is then the top directory of the worktree. In `.claude/worktrees/x`, a write to `src/a.go` is judged as `src/a.go`.
4. With no `.git`, the one directory that holds `.codex`, `.claude`, or `.mcp.json`.

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

Record the root that you mean with `openplexer install codex --project <dir>`.

You do not need to record a linked worktree or a submodule. A linked worktree is its own root, and the hook judges a write from it into the main checkout by the main checkout, so a write to `<repo>/.codex/hooks.json` is denied by the guard. A write that climbs out of a submodule, or out of a recorded directory inside another project, is judged by the project that holds the written file. A recorded root wins over the git layout. So with the repository recorded, the hook reads a file of a worktree as a path of the repository, such as `.claude/worktrees/x/src/a.go`, and the policy denies the write unless an area holds that path.

`uninstall` with the same `--project` removes the record. If you move the project, run `install` again.

If a record cannot be read, the hook denies every write and every judged command, and the reason names the file. `install` for the same hook file writes the record again. A file in `installs` that belongs to no install, or to a hook file that you no longer have, stays damaged, so delete it yourself. The hook does not let an agent delete it. The loss of a record costs only what it held: run `openplexer install codex --project <dir>` again to record the root, and with no record `uninstall` leaves `{}` in a file that `install` created.

On macOS and Linux, `install` and `uninstall` lock the `installs` directory while they run, so two of them at the same time do not lose a record.

## What the hook does not see

Each item is an action that Codex can take and the hook cannot see, or a way to switch the hook off. The policy does not apply to any of them.

**Reads.** The hook never judges a read, and Codex has no native rule that denies a read of a path. A private area is readable.

**Tools that the matcher does not name.** An MCP tool and any other local tool can write a file, and the hook does not run for them. The Codex documentation says that hosted tools such as `WebSearch` do not use the hook path, and that some specialized tool paths can opt out of it. It calls the hooks a guardrail and not a complete enforcement boundary.

**Writes that the reading of a command does not find.** The hook finds the files that a shell command names as a redirection target, as an argument of `tee`, `touch`, `rm`, `mkdir`, `rmdir`, `truncate`, as the destination of `cp`, `ln`, and `install`, as a source or the destination of `mv` and of `git mv`, and as a file of `sed -i`. A move takes its source away, so the source is a write. It does not find these.

- A write by a program that a command starts, such as `npm test`, `make`, `python script.py`, or `bash -c "..."`.
- A write by a command that is not in the list, such as `git rm`, `git checkout`, `git apply`, `patch`, `tar`, `unzip`, `dd`, `curl -o`, or `wget -O`.
- A command that follows another word or an opening parenthesis, such as `sudo rm`, `env rm`, `X=1 rm`, `(rm ...)`, or `then rm`.
- A target with a variable, a command substitution, or a wildcard.
- A source of `mv` under `~` is read as a path of the project, as every `~` in a shell command is, so `mv ~/Downloads/a.txt src/` is judged as a write to `~/Downloads/a.txt` and a policy with no area for it denies the call. Spell the path in full.
- A relative path after `cd`. The hook reads it from the directory of the hook call.

There is no check after the command. A write that the hook did not name stays unseen.

**Input to a command that is already running.** The documentation says that `write_stdin` is transport for an existing unified-exec session and does not run `PreToolUse` again. A command that passed the hook can then receive input that the hook never saw.

**The commands of a role with no exec list.** A role that lists no commands may run any command. The hook still judges the write targets of the command.

**Pushes.** The policy has a `git.push` action. The hook never asks about it. `git push` is a shell command, and the hook judges it only as a command, if the role lists commands.

**Commands that the policy matches by their first words.** A granted `npm test` allows whatever script `npm test` runs. A granted command with a quoted `;`, `|`, `&`, `<`, or `>` is denied, as [the open questions of the adapter contract](../spec/adapter/README.md#open-questions) say.

**A hook that does not run.** Codex runs no hook of a project until the project is trusted and the hook is trusted, and it skips a changed hook until you trust it again. A person who sets `hooks = false` under `[features]` in `config.toml` turns every hook off. The hook denies a write through `apply_patch` or `Bash` to `hooks.json` and `config.toml` of the user and of the project, to the settings files of Claude Code, and to the OpenPlexer home, whatever the policy says. For a shell command, that is a write that the hook finds, as the list above says, and it includes a move of one of these files to another place. A write that the hook does not find is not seen. A legacy hook can miss a command that reaches these files: a command that removes or moves a directory above a settings file, such as `rm -rf ~/.claude`, a command that copies or moves a file into that directory and names only the directory, such as `cp settings.json ~/.claude/`, a path through a symbolic link, and on macOS a path through `/System/Volumes/Data` or the `/tmp` spelling of `/private/tmp`. [The trust model](trust-model.md#decisions-settled-by-install-and-the-hook-command) gives the list. A write by anything else than these tools is not seen.

**A failed hook.** The Codex documentation says that a `PreToolUse` error, a timeout, or a malformed response can let the tool run. Codex has no option that blocks the call when a hook fails, so a hook that cannot start, is killed, or runs past its `timeout` lets the call run. The hook blocks on its own errors with exit status 2, and it ends itself with exit status 2 after 4 seconds, before the `timeout` of 5 seconds. It cannot block when the binary is missing or the process never starts.

**Changes while a session runs.** Codex has no `ConfigChange` event, and the adapter has no check of the hook file against its own last write.

**No policy.** With no verified policy, the hook denies every call that it judges, and it judges every `Bash` command as a command. The reason says how to load a policy.

**The project root.** The hook reads the paths of a call from the project root, as [The project root](#the-project-root) describes. A call that the hook cannot place in a project is denied, if it writes a file or runs a judged command.

**A path in another spelling.** A legacy hook finds the project of a written file from the path as the call spells it. On a file system that ignores case, it finds the same project for a path in another case. With the root at `/Users/dev/CaseProj`, a write to `/Users/dev/caseproj/api/server.ts` is judged as `api/server.ts`. A path through a symbolic link to a project directory is judged in the same way. A path through a symbolic link that lies outside every project, to a file inside a project, is not judged.

**The repository.** The hook does not know which repository of the bundle the project is. The gate then allows a write only when every repository of the bundle allows it, as [Local policy gate](local-policy.md) describes.

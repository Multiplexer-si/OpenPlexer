# Trust model

> [!NOTE]
> This page describes the design that OpenPlexer ports from the existing TypeScript implementation. OpenPlexer implements none of it yet. A paragraph that starts with **Existing implementation** states what that implementation does at commit `7d80045a`. A paragraph that starts with **OpenPlexer design** states what the scope or a plan issue settles for OpenPlexer. A paragraph that starts with **Recommendation** states what this page recommends, which nobody has decided yet. A paragraph with no label is an overview, advice to the reader, reasoning such as a comparison of options, or a property of git or of hashing that holds for any implementation. A list or a table belongs to the paragraph that introduces it. Each tag such as (S6) names a file in the existing implementation, which is not public. [Sources](#sources) lists the files. [Open decisions](#open-decisions) lists what is not settled.

This page is for a developer who decides whether to install the hooks, and for the security reviewer who advises them. It explains what the design guarantees, what it does not, and the reason for each limit. [Concepts](concepts.md) defines the parts, and the [glossary](glossary.md) defines the shared terms. This page defines its other terms where it first uses them. The architecture and privacy guide of [issue 32](https://github.com/Multiplexer-si/OpenPlexer/issues/32) must agree with this page.

## What the hooks guarantee, in short

If OpenPlexer ships the design on this page, installing the hooks gives you four things.

- The gate refuses a write that the policy denies when the write is a call to a file tool that the hooks match, or a shell command whose destination the gate recognizes before it runs, and the hook runs and finishes in time without an error. The coding agent receives the reason.
- File contents do not leave your machine as part of the telemetry contract. Paths, identifiers, and the prose that agents and people write in the Room do leave it.
- The daemon verifies the signature of a policy when the policy arrives, and stores it. It keeps enforcing the last verified policy when the hub is unreachable.
- With a trusted checkpoint, a verifier can show that an exported audit log was not altered up to that checkpoint.

The hooks do not give you these things.

- Protection from a person or a program that sets out to get around the gate. The daemon runs as your OS user. Anything that runs as your OS user can stop the daemon, edit its files, or write through a path that the hooks do not see.
- A guarantee that no code and no secret reaches the hub. The filters on free text are heuristics.
- Proof that an audit log is complete. Without a checkpoint, nothing shows that the end of the log was not cut off, or that the whole log was not rebuilt.

## It protects against agent overreach, not against a malicious insider

**Existing implementation.** The security summary scopes the system to accidental agent overreach and to coordination between people. It states that the system does not protect against a malicious insider, a compromised developer account, or an administrator who controls the hub database (S1). The threat test plan treats Room authors, agent-written text, and client-supplied identifiers as untrusted input. It treats team administrators and the hub operator as trusted. It puts a same-user bypass of the daemon and a malicious database administrator outside the prevention claim (S2).

**Existing implementation.** The code agrees with that scope. ADR 0002 records local prevention as not achievable, because the daemon runs as the same OS user as the coding agent (S6). The hub keeps the private key that signs policy in its own database, so whoever controls that database can sign a policy (S28).

**Existing implementation.** Room text reaches the context of other developers' coding agents, so a Room author can try to steer another agent with it. The existing implementation quotes and frames that text. Its threat test plan states that heuristics miss attacks and that framing is not a permission boundary (S2).

**OpenPlexer design.** OpenPlexer keeps that scope. The gate constrains the coding agent. The cases it is built for are an agent that follows a prompt into code that another team owns, and two agents that start the same work. The design trusts the developer who runs the coding agent, the team lead who operates the hub, and the hub's database. It treats every string that an agent or a Room author writes as untrusted input.

A tool that runs as the developer cannot hold a boundary against the developer. That boundary needs the daemon to run as another OS user, or needs hooks in configuration that only an administrator can write.

**Existing implementation.** ADR 0002 names Claude Code's managed settings as the only route to real local prevention for Claude Code. It puts that route out of scope, because it needs an elevated install on each OS (S6).

**Recommendation.** Leave the managed-settings route out of the foundation. It changes what an install costs a developer, so it needs a decision of its own. [Open decisions](#open-decisions) lists it.

## Metadata and agent prose leave the machine, file contents do not

### What the existing daemon sends

**Existing implementation.** The daemon sends these classes of data to the hub.

| Data | When | What a reviewer should know |
|---|---|---|
| Handshake | On each connection | The agent's bearer token, the protocol versions, the client name and version, and the capabilities that the adapter reports (S13). The capabilities are self-reported and not attested (S1). |
| Presence | While connected | Active or idle state, and the number of write records not yet delivered (S13). |
| Claims | When an agent claims work, or after a write | Project-relative paths, the intent text, the item, the duration, the claim mode, the repository identifier, and an `implicit` flag. The flag marks a claim that the daemon made by itself after a write (S13). |
| Room prose | When an agent or a person posts | Status summaries, decisions, questions, answers, and blocked notices (S7). The daemon filters the text first. [Free-text filtering is a heuristic](#free-text-filtering-is-a-heuristic) explains the limits. |
| Denials | When the gate denies an action | The action, the path, the rule, and the area (S18). An area is a named set of path patterns in the policy (S26). A path outside the project goes as an absolute local path. For a denied command, the event carries a shortened copy of the refused command, at most 200 characters (S22). |
| Write records | After a covered write | The repository identifier, the branch, the project-relative path, the git blob id, the tool, the session, and the time (S15). |
| Read receipts | After Room content reaches the agent | The ids of the Room events that were shown to the agent (S5). |
| Injection counters | After context is injected | The hook name and the number of characters injected. The counter carries no text of the digest, which is the summary of Room activity that the hooks inject into the agent's context (S1, S36). |
| Policy requests | On connect | The project id, to fetch the policy bundle and the overrides (S24). An override is a temporary permission for one area that lasts for one use, for one piece of work, or until a set time (S26). The daemon also sends an unauthenticated request for the hub's public keys (S23, S11). |

**Existing implementation.** The repository identifier is a name the developer configures. Without one, it is the git remote URL with the scheme, the credentials, and the port removed, such as `github.com/Org/Repo`. Without a remote, it is the name of the repository directory (S14).

A git blob id is a hash of a file's contents. It reveals no contents. Anyone who holds a copy of the file can still confirm that it matches, and a short file with few possible contents can be guessed from its blob id. These are properties of git, not of the implementation.

**Existing implementation.** The telemetry contract excludes file contents, diffs, patch text, and raw tool input and output (S1). The one exception is the refused command in a command denial. A tool summary carries an operation and a path, such as `edited api/handler.go` (S7). For a Codex `apply_patch`, the adapter sends the paths that the patch names, and never the patch text (S1, S3).

**Existing implementation.** The hub stores Room history, write records, policies, and audit records in its database, with no scheduled deletion. Data stays until an operator removes it (S1).

### What OpenPlexer intends to send

**OpenPlexer design.** The foundation excludes the GitHub check, metrics, and the write ledger, which is the record of each file that an agent wrote. Without them, write records, blob ids, and injection counters drop out of the telemetry contract. The handshake, presence, claims, Room prose, denials, and policy requests remain. Read receipts are not in the scope list.

**Recommendation.** Make the contracts in [`spec/`](../spec/README.md) the whole telemetry contract, so that a field that no schema defines is not sent. A reviewer can then audit what leaves the machine by reading the schemas, and a test can enforce it.

### Free-text filtering is a heuristic

**Existing implementation.** Before an event leaves, the daemon scans every string in it for known secret patterns, and drops the whole event when it finds one (S7, S8). For the prose events, such as status and decision events, it also replaces fenced code and code-shaped prose with a placeholder. The hub runs both checks again before it stores an ordinary event (S10).

**Existing implementation.** Denials and overrides feed the audit log, so the daemon never drops them for their content. It replaces a field that contains a detected secret with a redaction marker, cuts strings at 300 characters, and marks the event as flagged when its text looks like code (S9).

**Existing implementation.** The security summary states these limits (S1).

- Prose can contain code or a secret that the heuristics do not recognize. Known patterns are not a complete credential detector.
- Some fields are not checked for code, and short signatures are allowed by design (S4, S7).
- Text that the hub rejects has already reached the hub's memory.
- A program that holds the agent's credential can send to the hub directly and skip the daemon's filters. The hub's checks still run, but they are the same heuristics.

**Existing implementation.** A secret that the detector misses in a denial or an override goes into the audit log, which is append-only and hash-chained. Removing the secret later breaks the chain from that record onward (S9).

Treat the Room as a shared channel that the hub operator can read. The filters catch the common accident, such as a pasted token or a code block, and no more than that. Do not put credentials, source code, or personal data in Room prose.

### The sent log shows what left the machine

**Existing implementation.** Each process that uses the daemon's code to talk to the hub appends every WebSocket frame and every HTTP request to a local log in the daemon's home directory. Each entry has the time, the channel, the destination origin, the message type, the size, and the payload text. HTTP entries also carry the headers that the daemon set. The log replaces credential fields and the `authorization` header with `[redacted]`. When it cannot parse a message that carries a credential, it withholds that message's whole payload. It changes nothing else. It logs an HTTP request before it sends it, so a failed request appears too. The log rotates at 4 MiB and keeps five files (S11). A `sent` subcommand of the existing command-line tool filters the log by time, type, and channel, and prints either a readable view or raw JSON lines (S1, S12).

**Existing implementation.** The log is evidence from a daemon that behaves as designed. It lives in a directory that your OS user can write, and it does not record traffic from a program that skips the daemon's code. The security summary describes how to check the log against a capture from a logging proxy between the daemon and the hub (S1).

**OpenPlexer design.** No issue names the sent log yet.

**Recommendation.** Port the sent log. It is the only record on the developer's machine of what the daemon sent, so it is how a developer checks the claims in this section without extra tools.

## The gate stops what the hooks show it, and nothing else

### Hook coverage differs for each coding agent

**Existing implementation.** The two adapters cover different tools. In the table, a private area is an area that the policy denies for reading and writing unless a role or an override grants it (S26). The system area is a built-in area that holds the coding agent's settings, the MCP config, the git hooks, and the daemon's project files. It refuses every write through a covered tool, and no override opens it (S6).

| | Claude Code | Codex (`codex-cli` 0.164.0) |
|---|---|---|
| Tools that reach the gate before they run | `Write`, `Edit`, and `Bash` (S17) | `Bash` and `apply_patch` (S3) |
| Writes recorded after they run | The same tools | The same tools |
| Reads of a private area | Denied by a native `Read` deny rule that the adapter writes into the Claude Code settings (S17) | Not enforced. Codex has no equivalent rule (S3). |
| Reads through the shell, such as `cat` | Not checked. The hooks never evaluate a `read` action (S18). | Not checked |
| Before any hook runs | Nothing beyond the install | The user trusts the project and approves each hook in Codex. Until then, no hook runs (S3). |
| A config change during a session | The `ConfigChange` hook blocks a change to a settings file in the system area (S6) | No such event. Codex marks an edited hook as modified and stops running it until the user approves it again (S3). |

### Actions outside the hooks are not seen

**Existing implementation.** The gate sees only the tool calls that the hook matchers name. These actions do not reach it.

- An MCP tool call, and a Codex hosted tool (S3, S17).
- A call to a file tool that the hook matcher does not name. That call is not gated (S17).
- A program that the developer or the agent starts outside the coding agent's tools.
- A write to a path outside the project tree. The policy governs only project-relative paths. The one exception is the daemon's home directory, which the gate protects when a policy is loaded (S18).
- Git operations that get around the git hooks. The git hooks are advisory, and a process that controls its own environment or writes the repository's internals gets around them (S6).

### Shell writes are checked before by a heuristic and after by a tree diff

**Existing implementation.** The check before a shell command recognizes common explicit destinations. A command that writes through an interpreter, a script, or a destination built at run time is not seen before it runs (S18).

**Existing implementation.** After the command, the daemon compares the working tree with a snapshot from before the command, and judges each changed path the way the gate judges a target. A denied path produces a warning to the agent and a denial event, but the file is already written (S19). If the snapshot does not finish in time, the check after the command records nothing for that command (S20). On Windows, Codex runs shell commands in PowerShell, which the check before the command covers poorly, so most PowerShell writes reach only the check after the command (S3).

**Existing implementation.** The check after the command uses the tree snapshot code of the write ledger (S20). Its warning tells the agent that the GitHub check will reject the change (S19).

**OpenPlexer design.** The write ledger and the GitHub check are outside the foundation. Whether the check after the command ports without them is an open decision. If it does not port, a shell write that the check before the command misses is neither prevented nor reported.

### Hooks fail open

**Existing implementation.** A hook that throws an error or runs past its time budget allows the action and logs a notice (S21). ADR 0002 calls this deliberate, so that a broken daemon does not stop the agent, and notes that an attacker can trigger it (S6). With no verified policy on disk, the gate allows every action (S25). Two cases fail closed. Under a policy, the gate denies an `apply_patch` whose patch text it cannot parse. The gate also evaluates a token without a member id as an unknown member, and denies that member's writes (S18).

**OpenPlexer design.** The Phase 1 hook fails closed where the coding agent lets it. With no verified policy, it denies every call that it judges. Claude Code blocks a call when the hook fails, because the entry that `install` writes sets `onFailure` to `block`. Codex has no such option, so the hook ends itself with exit status 2 before the timeout of its entry. A Codex hook whose binary is missing or killed still lets the call run. [Decisions settled by install and the hook command](#decisions-settled-by-install-and-the-hook-command) gives each case.

### The same OS user can undo all of it

**Existing implementation.** ADR 0002 lists what a program running as the developer can do. It can stop the daemon, remove or edit the hooks, edit the daemon's state files, and run a shell that the hooks do not observe (S6).

**Existing implementation.** The daemon verifies a policy's signature when the policy arrives, and the hooks read the stored copy without checking the signature again (S23). The daemon's policy, keys, and tamper-detection state are files that the same OS user can write. A program that edits or removes them changes what the gate enforces, and some of those changes produce no report (S6, S23). With no stored policy, the gate allows everything (S25).

**Existing implementation.** The gate denies writes to the daemon's home directory through covered tools (S18), and the system area protects the agent's config files from covered tools (S6). A shell outside the hooks gets past both.

**OpenPlexer design.** The Phase 1 hook denies a write through a gated tool to the OpenPlexer home and to the hook settings files of both agents, before it asks the policy and also with no policy. A program that runs as the developer, or a write that the hook does not see, gets past it.

### Tamper detection reports a bypass, it does not prevent one

**Existing implementation.** ADR 0002 provides three detections (S6).

- **System area edits.** For Claude Code, the `ConfigChange` hook runs when a settings or skills file changes during a session. When the file is in the system area, the hook blocks the change and posts a denial. The block stops the session from applying the change. It does not revert the file.
- **Drift.** When a hook runs, the daemon compares the coding agent's settings with what the daemon last wrote, and posts a status event when they differ. Detection runs inside the hooks, so it stops when the hooks stop.
- **Stale presence.** The hub marks a member stale after 90 seconds without a frame from its daemon (S16). A killed daemon becomes visible to the team. A stale mark does not say why the daemon went quiet.

Detection is the most that the design can offer against the same OS user. It turns a silent bypass into a visible one in the common cases. A program that targets the daemon can still stop the detection or give it false state.

**OpenPlexer design.** Phase 1 ports none of the three detections. [Open decisions](#open-decisions) records why.

## A policy is trusted because the hub signed it, and only when it arrives

### Signature and key distribution

**Existing implementation.** The hub signs the canonical JSON of the bundle as a compact JWS with ES256 (S29). The header carries the key id and a `typ` value that marks the JWS as a policy (S28). The daemon verifies the JWS against a JSON Web Key Set (JWKS), checks the `typ`, and uses the verified payload as the bundle (S23). The unsigned copy of the bundle that travels beside the JWS is only compared by version (S24).

**Existing implementation.** The daemon gets the JWKS from the hub's `/.well-known/jwks.json`, at the same origin it gets the bundle from, and caches it in its home directory. It tries the cached keys first and fetches again only when they fail (S23). Four facts follow from the code.

- A party that can serve the bundle at the hub URL can serve the keys too. Trust in a policy today is trust in the connection to the hub URL.
- The hub signs every bundle that it serves with its current key (S29). After a rotation, the first bundle that a daemon receives fails against the cached keys, so the daemon fetches the keys again and replaces its cache (S23). Until that happens, the cache still holds the retired key, and a bundle signed with the retired key still verifies on that daemon. Nothing revokes a key. The hub keeps publishing a retired key for a grace period, so a cache fetched during that period holds the retired key too (S28).
- One key signs both agent tokens and policy bundles. The `typ` header tells them apart (S28).
- Overrides are not signed. The daemon takes an override from a push on its authenticated connection or from an HTTP fetch, and stores it next to the bundle (S24, S23).

**OpenPlexer design.** In Phase 1, the gate verifies a bundle against a key set that the developer stores locally, with no hub ([issue 16](https://github.com/Multiplexer-si/OpenPlexer/issues/16), [Local policy gate](local-policy.md)). With a locally stored key set, a party that controls the network path or the hub URL can no longer substitute keys. A key that the developer removes from the key set stops verifying at the next command, so the gate stops enforcing every bundle that the key signed. The key does not protect against the hub operator, who holds the private key, and the design trusts the operator. How the key reaches the daemon once the hub distributes policy is not decided.

### Offline, the daemon keeps enforcing the last verified bundle

**Existing implementation.** With the hub unreachable, the daemon keeps enforcing the last bundle that it verified, and it can verify a new bundle against the cached keys. It refuses a bundle with a lower version for the same project, which stops a replay of an older policy. A verified bundle for another project replaces the stored one, and the daemon drops the old project's overrides (S23). The daemon accepts such a bundle only when the agent's mission belongs to that project. A mission is the existing implementation's name for a piece of work in a project that agent sessions join (S24).

**OpenPlexer design.** [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20) requires the daemon to keep enforcing its last verified policy while it is disconnected. The Phase 1 gate refuses a lower version for the same project, as the existing daemon does. It also refuses the same version with another content, which the existing daemon accepts. Phase 1 has no missions, so the gate cannot tell when a change of project is intended. It refuses a bundle for another project while a policy is in force, and the developer removes the stored policy to change the project ([What a load accepts](local-policy.md#what-a-load-accepts)). A stored policy that no longer verifies gives the gate no version to compare with, so the gate then accepts every bundle that verifies. [Open decisions](#open-decisions) lists what a daemon that follows missions does.

### A stale policy has no expiry today

**Existing implementation.** A bundle carries a version and a project id, and no issue time or expiry (S26). The daemon enforces a bundle until a newer one arrives, however long that takes. While a machine is offline, it keeps the old rules. A member whom a lead removed from a role keeps that role's grants on that machine, and an override that a lead revoked stays in force until the next connection. An override with an end time expires by the local clock (S27).

**OpenPlexer design.** Issue 16 requires the gate to report when its policy is stale. The existing bundle format cannot express a freshness rule, so the Phase 1 gate measures freshness on the machine. A policy is stale when no load has confirmed it for a configured number of days, 14 by default. A load confirms a policy when it brings the same bundle again or a newer version. The gate keeps enforcing a stale policy, and it reports the state in `openplexer policy status`, with each decision, and in each decision record ([Offline and stale](local-policy.md#offline-and-stale)). A gate that denies everything when its policy goes stale turns a hub outage into a work stoppage, and a developer whose work stops removes the hooks.

**OpenPlexer design.** The time of the last confirmation is local and is not signed. A stale policy therefore means that the machine has not seen the bundle again, and not that the lead changed the policy. A program that runs as the developer can change the time. A replay of an old bundle of the same version also confirms the policy.

**Recommendation.** Add a signed issue time to the bundle, and measure the age of a bundle from it. A replay then no longer makes a policy fresh. The change needs a new field in the bundle contract, and a hub that signs the time. [Open decisions](#open-decisions) lists it.

### What a same-user attacker can still do with the policy

**Existing implementation.** A program that runs as the developer can still do these things.

- Edit or remove the daemon's policy and key files, and so change what the gate enforces (S23, S25).
- Write a validly signed older bundle into the policy file, which rolls the policy back (S23).
- Make a hook time out or crash, so that the hook fails open (S21).
- Use the agent's credential to post to the Room as that agent, outside the daemon's filters (S1).

Such a program cannot change what another developer's daemon enforces, and it cannot produce a bundle that verifies against the hub's real key. It cannot change the hub's policy or the audit log without hub credentials.

**OpenPlexer design.** The Phase 1 gate stores the signed token, and not the bundle, and each command verifies the token against the key set again before it uses the policy. A token with a bad signature fails verification, and the gate then enforces no policy and says so. A validly signed older bundle written straight into `policy.json` verifies, and the gate enforces it, because only `policy load` compares versions. That rollback is a same-user action. The hook's denial of writes into the OpenPlexer home, which issue 15 adds, is the protection against an agent that attempts it. For each action that arrives while no verified policy is in force, the gate writes a `policy.unenforced` record to its audit log, so the log reports both an edit and a removal ([The states of a policy](local-policy.md#the-states-of-a-policy)). Neither measure stops a program that also replaces the key set or edits the audit log. Both are cheap, and they catch an agent that edits the policy by mistake.

## The audit chain proves the records agree, not that the log is complete

### What the hash covers

**Existing implementation.** The hub keeps one chain for each team. The existing implementation calls a team a workspace, so the hash input that names the team is `workspace_id` (S31, S34). The hash of a record is SHA-256 over two inputs, in this order (S31).

1. The hash of the previous record, as 64 lowercase hexadecimal characters. The first record uses 64 zeros.
2. The canonical JSON of the record's fields `workspace_id`, `actor_member_id`, `agent_session_id`, `action`, `target`, `reason`, `meta`, `flagged`, and `created_at`.

**Existing implementation.** Canonical JSON sorts object keys by UTF-16 code units at every depth, keeps array order, and drops whitespace and undefined members (S30). `created_at` is an ISO-8601 string with millisecond precision (S31). Because `flagged` is hashed, the moderation flag cannot be removed without breaking the chain.

**Existing implementation.** The hash does not cover the record's numeric id, which orders the chain in the database. It does not cover the fields that the API derives when it reads a record, `result` and `agent_member_id` (S31, S35). The database enforces the append-only rule, so a database administrator can lift it (S34).

### What a verifier can prove from a chain alone

A verifier that holds only the chain can prove two things for each record. Its contents match its hash, and it names the hash of the record before it. So a change to one field of a record, a record removed from the middle, a record inserted, and two records swapped all break the chain at that point. That holds only while nobody recomputes the hashes after the change. The hash uses no key, so anyone who can write the records can recompute every later hash.

The chain alone cannot prove these things.

- **Completeness.** A chain cut off after record *n* is a valid chain of *n* records.
- **That it was not replaced.** Someone who can write the store can rebuild the whole chain from the genesis hash with altered records, and every hash checks.
- **That the records are true.** The chain holds what the hub wrote. A denial that a daemon never sent, and an action outside the hooks, have no record.
- **When a record was written.** `created_at` comes from the hub's clock.

### The existing verifier starts from genesis and has no checkpoint

**Existing implementation.** The verifier reads one team's records from the hub database in id order, starts from the genesis hash, and reports the first record whose link or hash is wrong (S32, S33). It takes no checkpoint as input, and it does not read an export. The hub's export, in JSON or CSV, carries `prev_hash` and `hash` for each record (S35). The export lists records newest first and accepts filters, so a filtered export is a subset whose links do not join. The CSV form leaves out `workspace_id`, which is a hash input. It also puts an apostrophe before a cell that starts with `=`, `+`, `-`, or `@`, to stop spreadsheet formulas (S36). A verifier cannot recompute the hashes from a CSV export as written.

**OpenPlexer design.** The OpenPlexer verifier is a separate program that checks an exported log with no hub and no network ([Concepts](concepts.md#the-audit-log-and-the-verifier)). [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23) specifies how a checkpoint is trusted, and [issue 25](https://github.com/Multiplexer-si/OpenPlexer/issues/25) requires the verifier to state what an unanchored chain cannot prove.

**Recommendation.** When the verifier runs without a checkpoint, it states in its output that the chain is consistent from genesis and that completeness is not proven.

### How a verifier obtains a trusted checkpoint

A checkpoint pairs a position in the chain, the record count, with the tip hash, which is the hash of the record at that position. With a checkpoint, the verifier checks that the export holds a record at that position with that hash. That proves the export extends the history that the checkpoint saw. A log that someone cut off before the checkpoint fails, and so does a log in which someone changed any record up to the checkpoint. Records after the newest checkpoint stay unanchored.

The hard part is where the checkpoint comes from, so that the hub operator cannot rewrite it too. There are three options.

**A signed tip hash that the hub publishes.** The hub signs a small object that holds the team, the record count, the tip hash, and the time, and serves it with each export or pushes it to every connected daemon. The verifier checks the signature offline against a public key. A later rewrite becomes provable. Two signed checkpoints for the same count with different hashes are evidence, signed with the hub's own key, that the history changed. On its own, a signed checkpoint proves nothing against the operator, who holds the key and can sign a checkpoint for a rebuilt chain. It has value only when someone outside the hub keeps the old checkpoints.

**Tip hashes that the team records outside the hub.** A lead copies the tip hash into a place that the team already protects, such as a commit in a reviewed repository, or each daemon keeps the tip hashes that it receives. The record is independent of the hub. With a copy on each developer's machine, a rewrite has to get past every developer. An unsigned hash is only a claim by whoever recorded it, though, and the hub operator can dispute it. A manual step is also easy to skip.

**An external timestamping service.** The hub submits the tip hash to an RFC 3161 timestamp authority or to a public transparency log, and stores the receipt. This is the strongest evidence of when the chain had that state, and it is independent of the team. It adds a third party and a network dependency to a hub that a single team runs for itself. The verifier needs the authority's certificate or the log's public key, and an outage of the service leaves gaps.

All three options can be checked offline. A hash committed to a repository needs only the repository, and an RFC 3161 token verifies against the authority's certificate. What a signature adds is attribution. The hub cannot disown a checkpoint that it signed, so a kept checkpoint that the chain contradicts is evidence against the hub, and not only a claim by whoever kept it.

**Recommendation.** Use the signed tip hash as the checkpoint format, and distribute it the way the second option does. The hub signs a checkpoint and pushes it to every connected daemon. Each daemon keeps every checkpoint that it receives, and never overwrites or deletes one. A checkpoint is a few hundred bytes, so keeping all of them costs little. A daemon that must save space keeps the oldest checkpoint and one checkpoint per fixed period, and still never overwrites one. A team that wants a copy off its machines commits a checkpoint file from time to time. The verifier takes the public key and zero or more checkpoint files. Timestamping can come later, by submitting the same signed object to a service. [Open decisions](#open-decisions) lists the choice.

A daemon holds no chain, so it cannot check that a new checkpoint extends an old one. On its own, a daemon detects only two different checkpoints at the same count. The proof of a rewrite comes when a verifier compares a kept checkpoint with the chain that it is given. If the chain has no record at the checkpoint's count with the checkpoint's tip hash, the chain is not the history that the hub signed earlier. That is why a daemon must keep the old checkpoints. A daemon that kept only the latest few would lose them to a hub that rebuilds the chain and pushes a few checkpoints with higher counts.

**Recommendation.** A verifier needs the public key that signed each old checkpoint, and the existing hub stops publishing a retired key after a grace period (S28). Keep publishing every key that signed a checkpoint, and make each daemon store the public key with the checkpoints that it signed. The verifier then takes the key from the same kept copy as the checkpoint, and does not depend on the hub for an old key. A key from the kept copy is enough for the keeper to detect a rewrite. To show the rewrite to someone else, the key must also match a key that the hub published, because a keeper could invent both a key and a checkpoint.

## Open decisions

Each item below is a compatibility or trust decision that is not settled. The issue named with an item is the one that it belongs to.

- **How the verifier obtains a trusted checkpoint, and where it gets the key for an old one.** The existing verifier has no checkpoint. This page recommends a signed tip hash that the hub pushes to every daemon, and that each daemon keeps with its key. [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23).
- **Which key signs checkpoints.** The hub signs policy bundles with an ES256 key. The checkpoint key decision depends on the checkpoint format and on how verifiers establish trust in its keys. [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23).
- **How the daemon gets the policy key once the hub distributes policy.** Phase 1 uses a locally configured key, while the existing daemon fetches the keys from the hub URL, which makes the signature no stronger than that connection. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether the daemon refuses a hub URL without TLS.** The existing daemon accepts an `http` hub URL and leaves HTTPS to the deployment, and today the trust in a policy rests on the connection to the hub URL. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **How a retired or revoked key leaves a daemon.** The existing daemon replaces its cached keys only after a verification fails, and nothing revokes a key, so a bundle signed with a retired key verifies on a daemon that has not yet seen one signed with the new key. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether overrides are signed.** The existing hub sends overrides without a signature, so the policy bundle is signed and the override that widens it is not. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether a bundle carries a signed issue time.** The Phase 1 gate calls a policy stale after a configured time with no load that confirms it, and keeps enforcing it. That time is local and not signed, so a replay of the same bundle makes a policy fresh. A signed issue time needs a change to the bundle contract and to the hub that signs. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **What a bundle for another project does once a daemon follows missions.** The Phase 1 gate refuses a lower version, and refuses a bundle for another project until the developer removes the stored policy. The existing daemon replaces the stored bundle when the agent's mission belongs to another project, and Phase 1 has no missions. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Where the agent credential is stored, and how long it lives.** Any program that holds the credential can post to the Room as the agent and skip the daemon's filters. Phase 1 has no hub and therefore no credential: the local identity is a member id in a configuration file, which the gate does not check. The decision stays open until a daemon connects to a hub. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether the gate detects a rollback written into `policy.json`.** A validly signed older bundle written straight into the file verifies and is enforced, and only `policy load` refuses an older version. A version floor kept next to the file is as writable as the file. A signed issue time or a version that the hub holds would bound the rollback. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether OpenPlexer offers the managed-settings route.** Hooks in Claude Code's managed settings give real local prevention, but they need an elevated install on each OS. The Phase 1 `install` writes the user and project files only. An elevated install belongs with the platform packages. [Issue 29](https://github.com/Multiplexer-si/OpenPlexer/issues/29).
- **Whether tamper detection ports.** Phase 1 has no `ConfigChange` block and no drift check. A drift check compares a settings file with the last write of the daemon. The Phase 1 `install` keeps no copy of the bytes that it wrote. Its install journal records only the agent, the hook file, the project root, and the outermost thing that it added, and `uninstall` finds the entry by its command. The hook denies a write to the settings files through a gated tool, which covers an agent and not a person or another program. [Issue 25](https://github.com/Multiplexer-si/OpenPlexer/issues/25).
- **Whether private-area reads stay unenforced where they are today.** The hooks judge no read, so a private area is readable through both agents. The existing implementation protects Claude Code's `Read` tool with a native deny rule that its install writes. The Phase 1 `install` writes none. Such a rule is a copy of the private areas of the policy in the settings of the agent, so it must change at each policy update, and it needs a translation from policy patterns to the rule syntax of Claude Code. Codex has no equivalent rule, and neither agent has a check of a read through the shell. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20), which delivers policy updates.
- **Whether the claim check, the warnings, the Room events, and the other agent hooks port.** Phase 1 installs only the `PreToolUse` hook, and it ports none of them. The existing hooks deny a write that another agent holds an exclusive claim on, warn about an overlapping claim and an expiring lease, post events to the Room, and run on `PostToolUse`, `SessionStart`, `UserPromptSubmit`, and `SessionEnd`. Each needs the Room or the hub. [Issue 18](https://github.com/Multiplexer-si/OpenPlexer/issues/18) for the events and claims, and [issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20) for the connection that carries them.
- **Whether the sent log ports.** It is the existing way for a developer to inspect what left the machine, and no issue covers it. The nearest issue is the one that connects the daemon to the hub. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether the hub repeats the content filters.** The single-team hub validates Room events and removes the client-supplied `flagged` field. Server-side secret and code detection stays open while the telemetry contract and daemon filters are being defined. [Issue 19](https://github.com/Multiplexer-si/OpenPlexer/issues/19) and [issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether the published contracts are the whole telemetry contract.** This page recommends that a field no schema defines is not sent, which makes the outgoing data auditable from the schemas. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether a command denial carries the refused command.** The existing denial sends up to 200 characters of the command, and command arguments can be sensitive. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether a denial carries an absolute local path.** The existing denial sends a path outside the project as an absolute path, which can name the developer's home directory. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Which form the repository identifier takes.** The existing daemon sends the normalized git remote URL unless the developer configures a name. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether read receipts are part of the Room contract.** The existing daemon reports which Room events reached the agent, and the scope list does not name receipts. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20) settles it. Default, pending the operator: they are not, and a version 1 agent sends none. [The Room contract](../spec/room/README.md#defaults-that-wait-for-the-operator) records the default and its reason.
- **The undecided event types.** The owner decides on `request`, `handoff.*`, `artifact`, `ledger.adopted`, and `check.timeout-pass`, and each would add data that leaves the machine. [Issue 4](https://github.com/Multiplexer-si/OpenPlexer/issues/4) settles it. Default, pending the operator: version 1 has none of them, and a receiver skips each one. [The Room contract](../spec/room/README.md#defaults-that-wait-for-the-operator) records the default and its reason.
- **The wire format of the digest.** The digest is in scope as daemon behavior but not as a v1 contract, and it is the path by which Room text reaches the model. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20) settles it. Default, pending the operator: version 1 defines no digest message. [The Room contract](../spec/room/README.md#defaults-that-wait-for-the-operator) records the default and its reason.
- **Whether the Go filters must match the existing detectors case for case.** A difference changes what leaves the machine, so each one needs a fixture and a recorded reason. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Which export format the verifier accepts.** The existing JSON export is newest first and can be filtered, and the CSV export leaves out a hash input and changes some cells. [Issue 24](https://github.com/Multiplexer-si/OpenPlexer/issues/24).
- **The canonical JSON rules that a Go implementation must reproduce.** The existing hash sorts keys by UTF-16 code units and formats strings and numbers as JavaScript's `JSON.stringify` does, while Go's `encoding/json` sorts map keys by UTF-8 bytes and escapes `<`, `>`, and `&` by default. [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23).
- **Whether the hash input keeps its existing form.** The existing record has no format version, and the previous hash enters as hexadecimal text, so a change breaks verification of existing logs. [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23), checked against existing logs by [issue 26](https://github.com/Multiplexer-si/OpenPlexer/issues/26).
- **What `workspace_id` means on a single-team hub.** The existing hash input names the team that a record belongs to, and a hub that serves one team still needs a fixed rule for that value. [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23).
- **How to remove a secret that reached the audit log.** An append-only chain cannot drop a field without breaking every later hash. [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23).

## Decisions settled by the single-team hub

The [single-team hub](hub.md) implements these choices from [issue 19](https://github.com/Multiplexer-si/OpenPlexer/issues/19).

- **Where the hub keeps its keys.** Initialization generates one ES256 policy key. The hub keeps the private key in its owner-only state file and serves the public key as a JWKS. Bearer tokens are random and the hub stores SHA-256 token hashes. Key rotation stays open with the daemon key refresh and revocation decisions.
- **What the hub retains.** The hub keeps all Room history and the latest signed policy for each project. It deletes ended leases from the active claim store and removes a member's token hash on revocation. The history has no automatic pruning. Audit storage and checkpoints belong to [issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23).

## Decisions settled by the hook adapters

The adapters of [issue 14](https://github.com/Multiplexer-si/OpenPlexer/issues/14) settle these items. [Claude Code hooks](hooks-claude-code.md) and [Codex hooks](hooks-codex.md) list what each hook sees and what it does not.

- **Which tools each adapter's hooks match.** The Claude Code hook matches `Write`, `Edit`, `NotebookEdit`, and `Bash`. The Codex hook matches `Bash` and `apply_patch`. Each list is the table of tools in the adapter, and the matcher string of the hook entry is built from that table, so the two cannot differ. The lists were checked against the hook documentation of Claude Code 2.1.326 and Codex 0.167.1. The existing implementation leaves `NotebookEdit` out of its matcher, and the new adapter adds it, because Claude Code 2.1.326 has that tool and it writes a file. No tool outside the two lists is gated, and every such tool is a gap that the two pages name.
- **Whether the check after a shell command survives without the write ledger.** It does not. The existing check needs the tree snapshot of the write ledger, which the foundation leaves out, and no adapter of this repository takes a snapshot. A shell command is judged before it runs, by the files that the line names. A write that the line does not name is not seen, and nothing looks at the tree after the command.
- **Whether the git hooks port.** They do not. The scope of the adapters is the hooks of the coding agents. The git hooks are advisory, they run inside git and not inside the agent, and a push or a commit that an agent starts reaches the agent hooks only as a shell command.
- **Whether the adapters fail closed on what they cannot read.** They do. An input that is not JSON, a call to a gated tool whose target field is missing, and an `apply_patch` that Codex would not accept are blocked with exit status 2. The existing implementation lets a missing target pass, and it denies an unreadable patch only when a bundle is stored. [The hook-process item in the open decisions](#open-decisions) holds what remains open.

## Decisions settled by install and the hook command

[Issue 15](https://github.com/Multiplexer-si/OpenPlexer/issues/15) settles these items. [Claude Code hooks](hooks-claude-code.md) and [Codex hooks](hooks-codex.md) give the setup, the removal, and the limits.

- **What the gate does with no verified policy.** The hook fails closed. When no policy is loaded, when the stored policy does not verify, or when the gate cannot read its configuration, the hook denies every call that it judges, with a reason that says what to do. It then judges every shell command as a command, because the role of the member is not known. A call that makes no query still passes, such as a call to a tool that the matcher does not name, or a write outside the project. The gate records each denied call as `policy.unenforced`, unless it cannot read its configuration. The existing gate allows everything with no policy, so a removed or edited `policy.json` would switch enforcement off. The cost is a blocked agent between `install` and the first `policy load`, and `install` warns about it.
- **Whether the hook process fails closed when it cannot answer.** It does, except where Codex gives it no way. The entry that `install` writes for Claude Code sets `onFailure` to `block`, so a hook that cannot start, crashes, prints bad output, or runs past its timeout of 5 seconds blocks the call. That needs Claude Code 2.1.295 or later. For both agents, the hook ends itself with exit status 2 after 4 seconds, and a Go panic exits with status 2. A call that the policy allows but the gate cannot record is denied. In Codex, a hook whose binary is missing or that is killed lets the call run. [Issue 17](https://github.com/Multiplexer-si/OpenPlexer/issues/17) tests the real agents.
- **Whether the hook protects the OpenPlexer home and its own entry.** It does. The hook denies a write through a gated tool into the OpenPlexer home, and to the user and project settings files of both agents. These are `settings.json` and `settings.local.json` of Claude Code, and `hooks.json` and `config.toml` of Codex. It denies them before it asks the policy, and also with no policy. A write into the home could replace the key set, change the member, or roll the policy back to an older signed bundle. A write to a settings file could remove the hook or switch every hook off. The policy cannot cover these files, because the user files are outside the project. The cost is that an agent cannot change these files when the developer asks it to. The existing implementation guards its home only when a policy is stored. A shell command is covered only as far as the hook reads it: the target of a redirection, the arguments of `tee`, `touch`, `rm`, `mkdir`, `rmdir`, and `truncate`, the destination of `cp`, `ln`, and `install`, every source and the destination of `mv` and of `git mv`, and the files of `sed -i`. So `mv ~/.claude/settings.json /tmp/s.json` is denied, as `rm ~/.claude/settings.json` is. The hook does not see these, and each of them can change or remove a guarded file:
  - A target with a variable, a command substitution, or a wildcard.
  - A write by a program that a command starts, such as `python3 -c`, `bash -c`, or `openplexer install` itself.
  - A command that follows another word or an opening parenthesis, such as `sudo rm`, `env rm`, `X=1 rm`, `(rm ...)`, or `then rm`, and a command that the hook does not read, such as `git rm` or `git checkout`. For `git mv`, the hook knows the options `-C`, `-c`, `--git-dir`, `--work-tree`, `--namespace`, `--config-env`, and `--attr-source` before `mv`.
  - A command that removes or moves a directory above a guarded file, such as `rm -rf ~/.claude` or `mv ~/.claude /tmp/c`. The hook guards the OpenPlexer home as a directory, and each settings file as a file.
  - A command that copies or moves a file into the directory of a settings file and names only the directory, such as `cp settings.json ~/.claude/`.
  - A path that reaches the file by another spelling: through a symbolic link, through the firmlink `/System/Volumes/Data` of macOS, or as `/tmp` for a home under `/private/tmp`.
  - A relative path after `cd`.
- **How the hook finds the project.** The hook reads each path from a project root. It finds the root of the `cwd` of the call, and the root of each file that the call writes, by one rule, and it judges a write by the project that holds the written file. So a write from a linked worktree into its main checkout is judged as a path of the main checkout, a write from a recorded project into a recorded project around it is judged from the outer root, and the guard covers the project settings files of both. A write to a file that no project holds is not judged, as in the existing implementation, whose fixtures pin that. A write to a file whose project the hook cannot tell is denied. A project-scope `install` records its project directory in the install journal, in the `installs` directory of the OpenPlexer home, and the root is the nearest recorded directory at or above the `cwd` of the call. The existing implementation does the same with its journal. So a `.git` below a recorded project, such as a submodule or one that an agent creates, does not move the root, and the hook of a user-scope install uses the same record. With no recorded root, the root is the outermost directory at or above `cwd` that holds `.git`. A submodule lies below that directory, and so does a repository that an agent makes with `git init web` or `mkdir web/.git`. So neither moves the root, and a write that climbs out of one is judged. A linked worktree of the outermost repository is the root while `cwd` is inside it, so a path in `.claude/worktrees/x` is judged as the same path of the repository. An agent can write a `.git` file with a write that the policy allows, so the file alone does not make a worktree. The git directory of the outermost repository must hold the record of the worktree in its `worktrees` directory, and the `commondir` and `gitdir` files of the record must name that git directory and the `.git` file. A worktree of a submodule, a worktree of another repository, and a worktree that a person moved by hand fail that check, and the root stays the outermost repository. With no `.git`, the root is the one directory that holds `.claude`, `.codex`, or `.mcp.json`. The home directory and the file system root are never the root, as in the existing implementation. The hook does not run `git`, and it does not read `GIT_DIR` or `GIT_WORK_TREE`, because the agent sets the environment of a call. A bare repository holds no `.git`, so it is not a root. The hook denies every write and every judged command in three cases, and the reason gives the `install` command that records the root. It finds no root. Two directories hold an agent directory and none holds `.git`. A directory above the outermost `.git` holds only an agent directory, where a `git init` below a project with no repository would otherwise make the root smaller. The rule has two costs. An agent that can write into the `.git` directory of the repository can register a worktree, and the policy then judges a path of that directory without its prefix. A `.git` that lies above the repository by accident becomes the root, and the policy then judges each path with a longer prefix. A recorded root wins in each case. The existing implementation takes the nearest `.git` in a project that its journal does not know, which is every project whose hooks a person added by hand, and a write that climbs out of a submodule there is not judged. With no root at all it reads each path from `cwd`. The hook guards the OpenPlexer home, so a write that the hook reads cannot change the journal, and that includes a move of a record or of the `installs` directory. A write that the hook does not see can change it, as it can change the policy file, and the item above lists those writes. One of them is an agent that runs `openplexer install <agent> --project .` inside a submodule: it records the submodule as a root, so the policy then judges a path of the submodule without its prefix. A write that climbs out of the submodule is still judged, by the project that holds the written file. Two installs of one hook file at the same time keep the record, because `install` and `uninstall` lock the journal on macOS and Linux.
- **A path in another spelling.** The hook finds the project of a written file from the path as the call spells it, so it asks the file system and does not compare the path with the root of `cwd`. On a file system that ignores case, a path that spells the root in another case is in the same project. With the root at `/Users/dev/CaseProj`, `Write /Users/dev/caseproj/api/server.ts` and `rm /Users/dev/caseproj/api/server.ts` are judged as `api/server.ts`. The existing implementation passes both. A path through a symbolic link to a project directory is judged in the same way. A path through a symbolic link that lies outside every project, to a file inside a project, is not judged, because the hook does not resolve links. Below the root, the policy matches `Api/server.ts` to the area of `api/**`, and the guard of the OpenPlexer home ignores case.
- **How install and uninstall keep the configuration of the developer.** `install` adds one entry and keeps every other byte of the file. `uninstall` finds the entry by its command, a binary named exactly `openplexer` or `openplexer.exe` with `hook <agent>`, so it removes the entry after the developer moved or edited it, and it does not remove a hook that runs a binary with a longer name. The install journal records, for each hook file, the outermost thing that `install` added: a group, the event list, the `hooks` object, the document of a file that held no JSON value, or the file. `uninstall` removes an emptied container only if `install` added it, and it deletes the file only if `install` created it. With no record it removes the emptied containers and keeps the file. The existing implementation restores a snapshot of the file and skips a file that the developer changed after the install.

## Sources

Each path is relative to the root of the existing TypeScript implementation at commit `7d80045a`.

| Tag | Path | What it shows |
|---|---|---|
| S1 | `docs/security/summary.md` | The scope, the data that leaves the machine, the limits of filtering, and the sent log |
| S2 | `docs/security/threat-tests.md` | Trusted and untrusted parties, and the remaining limit for each threat |
| S3 | `docs/security/agent-support.md` | Codex hook coverage, trust and approval, and known gaps |
| S4 | `docs/security/short-signatures.md` | The short interface signature that prose may carry |
| S5 | `docs/adr/0001-interfaces.md` | The daemon messages and the read receipts |
| S6 | `docs/adr/0002-tamper-detection.md` | The same-user posture, tamper detection, and its gaps |
| S7 | `apps/daemon/src/strip/outbound.ts` | The daemon's outbound filter and the tool summary |
| S8 | `packages/protocol/src/content-filters.ts` | The shared secret and code detectors |
| S9 | `packages/protocol/src/audit-sanitize.ts` | Redaction and truncation of audit-bound events |
| S10 | `apps/hub/src/room/moderation.ts` | The hub's repeat of the content checks |
| S11 | `apps/daemon/src/sent-log.ts` | The sent log, its redaction, and its rotation |
| S12 | `apps/daemon/src/sent-format.ts` | The readable view of the sent log |
| S13 | `apps/daemon/src/sync/client.ts` | The handshake, presence, and claim messages |
| S14 | `apps/daemon/src/ledger/blob.ts` | The repository identifier and the blob id |
| S15 | `apps/daemon/src/ledger/sender.ts` | The fields of a write record |
| S16 | `apps/hub/src/ws/presence.ts` | The 90-second stale mark |
| S17 | `apps/daemon/src/adapters/claude-code/settings.ts` | The Claude Code hook matchers and the native deny rules |
| S18 | `apps/daemon/src/adapters/claude-code/hooks/pre-tool.ts` | Target extraction, the gate order, and the denial event |
| S19 | `apps/daemon/src/adapters/claude-code/hooks/shell-flags.ts` | The check after a shell command |
| S20 | `apps/daemon/src/adapters/claude-code/hooks/shell-writes.ts` | The tree snapshot and its time limit |
| S21 | `apps/daemon/src/adapters/claude-code/hook-runner.ts` | Fail-open behavior and the time budget |
| S22 | `apps/daemon/src/adapters/claude-code/hooks/exec-filter.ts` | The 200-character limit on a refused command |
| S23 | `apps/daemon/src/policy/store.ts` | The policy file, JWS verification, and the key cache |
| S24 | `apps/daemon/src/policy/sync.ts` | Bundle and override handling on push and on connect |
| S25 | `apps/daemon/src/policy/gate.ts` | No verified bundle means no gate |
| S26 | `packages/protocol/src/policy/types.ts` | The fields of a bundle and of an override |
| S27 | `packages/protocol/src/policy/evaluate.ts` | Override expiry against the local clock |
| S28 | `apps/hub/src/identity/agent-tokens.ts` | The hub's signing key, its storage, and its rotation |
| S29 | `apps/hub/src/policy/service.ts` | The JWS over the canonical JSON of the bundle |
| S30 | `apps/hub/src/policy/canonical.ts` | The canonical JSON rules |
| S31 | `apps/hub/src/audit/log.ts` | The hash inputs and the genesis hash |
| S32 | `apps/hub/src/audit/verify.ts` | The chain walk from genesis |
| S33 | `apps/hub/scripts/audit-verify.ts` | The verifier reads the database, not an export |
| S34 | `apps/hub/migrations/0008_audit.up.sql` | The audit table and its triggers |
| S35 | `apps/hub/src/api/audit.ts` | The export order, filters, and derived fields |
| S36 | `packages/protocol/src/observability.ts` | The CSV columns, the CSV cell rule, and the injection counter |

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

### The same OS user can undo all of it

**Existing implementation.** ADR 0002 lists what a program running as the developer can do. It can stop the daemon, remove or edit the hooks, edit the daemon's state files, and run a shell that the hooks do not observe (S6).

**Existing implementation.** The daemon verifies a policy's signature when the policy arrives, and the hooks read the stored copy without checking the signature again (S23). The daemon's policy, keys, and tamper-detection state are files that the same OS user can write. A program that edits or removes them changes what the gate enforces, and some of those changes produce no report (S6, S23). With no stored policy, the gate allows everything (S25).

**Existing implementation.** The gate denies writes to the daemon's home directory through covered tools (S18), and the system area protects the agent's config files from covered tools (S6). A shell outside the hooks gets past both.

### Tamper detection reports a bypass, it does not prevent one

**Existing implementation.** ADR 0002 provides three detections (S6).

- **System area edits.** For Claude Code, the `ConfigChange` hook runs when a settings or skills file changes during a session. When the file is in the system area, the hook blocks the change and posts a denial. The block stops the session from applying the change. It does not revert the file.
- **Drift.** When a hook runs, the daemon compares the coding agent's settings with what the daemon last wrote, and posts a status event when they differ. Detection runs inside the hooks, so it stops when the hooks stop.
- **Stale presence.** The hub marks a member stale after 90 seconds without a frame from its daemon (S16). A killed daemon becomes visible to the team. A stale mark does not say why the daemon went quiet.

Detection is the most that the design can offer against the same OS user. It turns a silent bypass into a visible one in the common cases. A program that targets the daemon can still stop the detection or give it false state.

**OpenPlexer design.** The scope includes the adapter code that holds these checks, but no issue names tamper detection.

## A policy is trusted because the hub signed it, and only when it arrives

### Signature and key distribution

**Existing implementation.** The hub signs the canonical JSON of the bundle as a compact JWS with ES256 (S29). The header carries the key id and a `typ` value that marks the JWS as a policy (S28). The daemon verifies the JWS against a JSON Web Key Set (JWKS), checks the `typ`, and uses the verified payload as the bundle (S23). The unsigned copy of the bundle that travels beside the JWS is only compared by version (S24).

**Existing implementation.** The daemon gets the JWKS from the hub's `/.well-known/jwks.json`, at the same origin it gets the bundle from, and caches it in its home directory. It tries the cached keys first and fetches again only when they fail (S23). Four facts follow from the code.

- A party that can serve the bundle at the hub URL can serve the keys too. Trust in a policy today is trust in the connection to the hub URL.
- The hub signs every bundle that it serves with its current key (S29). After a rotation, the first bundle that a daemon receives fails against the cached keys, so the daemon fetches the keys again and replaces its cache (S23). Until that happens, the cache still holds the retired key, and a bundle signed with the retired key still verifies on that daemon. Nothing revokes a key. The hub keeps publishing a retired key for a grace period, so a cache fetched during that period holds the retired key too (S28).
- One key signs both agent tokens and policy bundles. The `typ` header tells them apart (S28).
- Overrides are not signed. The daemon takes an override from a push on its authenticated connection or from an HTTP fetch, and stores it next to the bundle (S24, S23).

**OpenPlexer design.** In Phase 1, the daemon verifies a bundle against a public key that the developer configures locally, with no hub ([issue 16](https://github.com/Multiplexer-si/OpenPlexer/issues/16)). With a locally configured key, a party that controls the network path or the hub URL can no longer substitute keys. The key does not protect against the hub operator, who holds the private key, and the design trusts the operator. How the key reaches the daemon once the hub distributes policy is not decided.

### Offline, the daemon keeps enforcing the last verified bundle

**Existing implementation.** With the hub unreachable, the daemon keeps enforcing the last bundle that it verified, and it can verify a new bundle against the cached keys. It refuses a bundle with a lower version for the same project, which stops a replay of an older policy. A verified bundle for another project replaces the stored one, and the daemon drops the old project's overrides (S23). The daemon accepts such a bundle only when the agent's mission belongs to that project. A mission is the existing implementation's name for a piece of work in a project that agent sessions join (S24).

**OpenPlexer design.** [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20) requires the daemon to keep enforcing its last verified policy while it is disconnected. No plan issue settles the version refusal or the switch to another project's bundle. [Open decisions](#open-decisions) lists both.

### A stale policy has no expiry today

**Existing implementation.** A bundle carries a version and a project id, and no issue time or expiry (S26). The daemon enforces a bundle until a newer one arrives, however long that takes. While a machine is offline, it keeps the old rules. A member whom a lead removed from a role keeps that role's grants on that machine, and an override that a lead revoked stays in force until the next connection. An override with an end time expires by the local clock (S27).

**OpenPlexer design.** Issue 16 requires the gate to report when its policy is stale. That needs a freshness rule, and the existing bundle format cannot express one.

**Recommendation.** Add a signed issue time to the bundle, call a bundle stale after a configured age, and keep enforcing a stale bundle while the daemon reports it. A gate that denies everything when its policy goes stale turns a hub outage into a work stoppage, and a developer whose work stops removes the hooks.

### What a same-user attacker can still do with the policy

**Existing implementation.** A program that runs as the developer can still do these things.

- Edit or remove the daemon's policy and key files, and so change what the gate enforces (S23, S25).
- Make a hook time out or crash, so that the hook fails open (S21).
- Use the agent's credential to post to the Room as that agent, outside the daemon's filters (S1).

Such a program cannot change what another developer's daemon enforces, and it cannot produce a bundle that verifies against the hub's real key. It cannot change the hub's policy or the audit log without hub credentials.

**Recommendation.** Make each hook verify the stored signature again, and make the daemon report a missing policy. An edited policy would then fail verification and read as no policy, and the report of a missing policy would catch both an edit and a removal. Neither change stops a program that also edits the cached keys. Both changes are cheap, and they catch an agent that edits the policy by mistake.

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
- **Which key signs checkpoints, and where the hub keeps its keys.** The existing hub keeps one ES256 private key in its database for both agent tokens and policy bundles. [Issue 19](https://github.com/Multiplexer-si/OpenPlexer/issues/19).
- **How the daemon gets the policy key once the hub distributes policy.** Phase 1 uses a locally configured key, while the existing daemon fetches the keys from the hub URL, which makes the signature no stronger than that connection. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether the daemon refuses a hub URL without TLS.** The existing daemon accepts an `http` hub URL and leaves HTTPS to the deployment, and today the trust in a policy rests on the connection to the hub URL. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **How a retired or revoked key leaves a daemon.** The existing daemon replaces its cached keys only after a verification fails, and nothing revokes a key, so a bundle signed with a retired key verifies on a daemon that has not yet seen one signed with the new key. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether overrides are signed.** The existing hub sends overrides without a signature, so the policy bundle is signed and the override that widens it is not. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **What makes a policy stale, and what the gate does then.** The existing bundle has no issue time or expiry, and the daemon enforces it indefinitely. [Issue 16](https://github.com/Multiplexer-si/OpenPlexer/issues/16).
- **Whether the daemon refuses an older bundle, and what a bundle for another project does.** The existing daemon refuses a lower version for the same project, and replaces the stored bundle when the agent's mission belongs to another project. [Issue 16](https://github.com/Multiplexer-si/OpenPlexer/issues/16).
- **Where the agent credential is stored, and how long it lives.** Any program that holds the credential can post to the Room as the agent and skip the daemon's filters. [Issue 16](https://github.com/Multiplexer-si/OpenPlexer/issues/16).
- **What the gate does with no verified policy.** The existing gate allows every action when no verified bundle is stored, while the policy evaluator denies by default and issue 15 asks to fail safely. [Issue 15](https://github.com/Multiplexer-si/OpenPlexer/issues/15).
- **Whether OpenPlexer offers the managed-settings route.** Hooks in Claude Code's managed settings give real local prevention, but they need an elevated install on each OS. [Issue 15](https://github.com/Multiplexer-si/OpenPlexer/issues/15).
- **Whether hooks keep failing open.** The existing hooks allow the action on an error or a timeout, which keeps a broken daemon from stopping the agent and also lets a program trigger it. [Issue 14](https://github.com/Multiplexer-si/OpenPlexer/issues/14), tested by [issue 17](https://github.com/Multiplexer-si/OpenPlexer/issues/17).
- **Whether the daemon re-verifies the stored policy, and reports a missing policy.** The existing hooks read the stored copy without verifying it again, and the daemon does not report a missing policy. [Issue 16](https://github.com/Multiplexer-si/OpenPlexer/issues/16).
- **Whether the check after a shell command survives without the write ledger.** The existing check uses the ledger's tree snapshot and points at the GitHub check, and both are outside the foundation. [Issue 14](https://github.com/Multiplexer-si/OpenPlexer/issues/14).
- **Whether tamper detection ports with the adapters.** The scope includes the adapter code that holds the `ConfigChange` block and the drift check, but no issue names them. [Issue 14](https://github.com/Multiplexer-si/OpenPlexer/issues/14).
- **Whether the git hooks port.** The existing git hooks are an advisory check inside git, and the scope names only the hooks of the coding agents. [Issue 14](https://github.com/Multiplexer-si/OpenPlexer/issues/14).
- **Which tools each adapter's hooks match.** A file tool that the hook matcher does not name is not gated. Each adapter will list its matched tools, checked against the current release of its coding agent. [Issue 14](https://github.com/Multiplexer-si/OpenPlexer/issues/14).
- **Whether private-area reads stay unenforced where they are today.** Codex has no native read deny, and neither adapter checks a read through the shell. [Issue 14](https://github.com/Multiplexer-si/OpenPlexer/issues/14).
- **Whether the sent log ports.** It is the existing way for a developer to inspect what left the machine, and no issue covers it. The nearest issue is the one that connects the daemon to the hub. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether the hub repeats the content filters.** The existing hub runs the secret and code checks again before it stores an event, and the scope names only the daemon's filtering. [Issue 19](https://github.com/Multiplexer-si/OpenPlexer/issues/19).
- **What the hub retains, and for how long.** The existing hub keeps Room history, write records, policies, and audit records with no scheduled deletion, and the foundation has no retention feature. [Issue 19](https://github.com/Multiplexer-si/OpenPlexer/issues/19).
- **Whether the published contracts are the whole telemetry contract.** This page recommends that a field no schema defines is not sent, which makes the outgoing data auditable from the schemas. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether a command denial carries the refused command.** The existing denial sends up to 200 characters of the command, and command arguments can be sensitive. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether a denial carries an absolute local path.** The existing denial sends a path outside the project as an absolute path, which can name the developer's home directory. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Which form the repository identifier takes.** The existing daemon sends the normalized git remote URL unless the developer configures a name. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Whether read receipts are part of the Room contract.** The existing daemon reports which Room events reached the agent, and the scope list does not name receipts. [Issue 18](https://github.com/Multiplexer-si/OpenPlexer/issues/18).
- **The undecided event types.** The owner decides on `request`, `handoff.*`, `artifact`, `ledger.adopted`, and `check.timeout-pass`, and each would add data that leaves the machine. [Issue 18](https://github.com/Multiplexer-si/OpenPlexer/issues/18).
- **The wire format of the digest.** The digest is in scope as daemon behavior but not as a v1 contract, and it is the path by which Room text reaches the model. [Issue 18](https://github.com/Multiplexer-si/OpenPlexer/issues/18).
- **Whether the Go filters must match the existing detectors case for case.** A difference changes what leaves the machine, so each one needs a fixture and a recorded reason. [Issue 20](https://github.com/Multiplexer-si/OpenPlexer/issues/20).
- **Which export format the verifier accepts.** The existing JSON export is newest first and can be filtered, and the CSV export leaves out a hash input and changes some cells. [Issue 24](https://github.com/Multiplexer-si/OpenPlexer/issues/24).
- **The canonical JSON rules that a Go implementation must reproduce.** The existing hash sorts keys by UTF-16 code units and formats strings and numbers as JavaScript's `JSON.stringify` does, while Go's `encoding/json` sorts map keys by UTF-8 bytes and escapes `<`, `>`, and `&` by default. [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23).
- **Whether the hash input keeps its existing form.** The existing record has no format version, and the previous hash enters as hexadecimal text, so a change breaks verification of existing logs. [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23), checked against existing logs by [issue 26](https://github.com/Multiplexer-si/OpenPlexer/issues/26).
- **What `workspace_id` means on a single-team hub.** The existing hash input names the team that a record belongs to, and a hub that serves one team still needs a fixed rule for that value. [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23).
- **How to remove a secret that reached the audit log.** An append-only chain cannot drop a field without breaking every later hash. [Issue 23](https://github.com/Multiplexer-si/OpenPlexer/issues/23).

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

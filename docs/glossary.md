# Glossary

The documentation uses each of these terms with one meaning. [Concepts](concepts.md) explains them in context.

| Term | Meaning |
|---|---|
| Adapter | The code that connects one coding agent to OpenPlexer through the hooks of that agent. |
| Area | A named set of paths in a policy, where a private area needs a grant to read and a system area accepts no write. |
| Audit log | The record of what happened. Each record includes a hash of the record before it. |
| Canonical JSON | The one exact text for each JSON value, which the signature of a policy bundle and the hash of an audit record are computed over. |
| Checkpoint | A known point in the audit chain that comes from a source the person who verifies already trusts. |
| Claim | A Room entry that says a coding agent is working on a piece of work. A claim expires. |
| Coding agent | A tool, such as Claude Code or Codex, that reads and changes code for a developer. |
| Contract | The language-independent definition of a format: a README with its rules, and the schemas and examples that apply to it. |
| Daemon | The OpenPlexer program that runs on a developer's machine. It contains the gate. The issue tracker also calls it the local agent. |
| Decision | A Room entry that records something the team has settled. The policy contract also names its result field `decision` on the wire, and the documentation calls that value the result. |
| Envelope | The object that wraps every message between a daemon and the hub. |
| Example | A file under `spec/` that is a valid instance of one schema. |
| Extractor | The program in `tools/extract` that writes the schemas and the examples by running the reference. |
| Fixture | A data file that pairs an input with the expected result. Any implementation can run it. |
| Gate | The part of the daemon that asks the policy whether an action is allowed. |
| Hook | A point where a coding agent calls an outside program before or after it acts. |
| Hub | The server that hosts the Room of a team and manages the policy of that team. |
| Member | A person on a team, whom the policy maps to a role and for whom a coding agent acts. |
| Mission | A named unit of work that an agent session works on, which a temporary grant, an override, and a Room entry can name. |
| Override | A grant that a lead gives to open one area for an agent once, for one mission, or until a time. |
| Policy | The statement of what each role on a team may do. |
| Policy bundle | A signed package that carries a policy to the daemon. |
| Policy evaluator | The code that takes a policy and a request, and returns a result. |
| Reference | The existing TypeScript implementation, whose behavior the contracts record. |
| Result | What the policy evaluator returns: allow, or deny with a reason. |
| Role | A named set of grants in a policy, which the policy gives to members. |
| Room | The shared record that the coding agents of one team read and write. |
| Sub-agent | An agent session that another agent session started, which gets the grants of its parent and no others. |
| Temporary grant | A grant in a policy bundle that gives one member read or write access to one area, for one mission or until a time. |
| Verifier | The separate program that checks an exported audit log. |

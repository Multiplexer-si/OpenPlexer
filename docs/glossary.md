# Glossary

The documentation uses each of these terms with one meaning. [Concepts](concepts.md) explains them in context.

| Term | Meaning |
|---|---|
| Adapter | The code that connects one coding agent to OpenPlexer through the hooks of that agent. |
| Audit log | The record of what happened. Each record includes a hash of the record before it. |
| Checkpoint | A known point in the audit chain that comes from a source the person who verifies already trusts. |
| Claim | A Room entry that says a coding agent is working on a piece of work. A claim expires. |
| Coding agent | A tool, such as Claude Code or Codex, that reads and changes code for a developer. |
| Contract | The language-independent definition of a format: a schema, examples, and fixtures. |
| Daemon | The OpenPlexer program that runs on a developer's machine. It contains the gate. The issue tracker also calls it the local agent. |
| Decision | A Room entry that records something the team has settled. The word never means the result of a policy evaluation. |
| Fixture | A data file that pairs an input with the expected result. Any implementation can run it. |
| Gate | The part of the daemon that asks the policy whether an action is allowed. |
| Hook | A point where a coding agent calls an outside program before or after it acts. |
| Hub | The server that hosts the Room of a team and manages the policy of that team. |
| Policy | The statement of what each role on a team may do. |
| Policy bundle | A signed package that carries a policy to the daemon. |
| Policy evaluator | The code that takes a policy and a request, and returns a result. |
| Result | What the policy evaluator returns: allow, or deny with a reason. |
| Room | The shared record that the coding agents of one team read and write. |
| Verifier | The separate program that checks an exported audit log. |

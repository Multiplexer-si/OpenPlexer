# Concepts

> [!NOTE]
> This page describes the design. Nothing on it is implemented yet. The present tense describes how the finished system is meant to work. [What is not decided yet](#what-is-not-decided-yet) lists the open questions.

## Why agents on a team need coordination

A coding agent works from one conversation and one checkout. Inside that view it is capable. Outside that view it knows nothing. It cannot see that a teammate's agent started the same task ten minutes ago, or that the team agreed on a response format yesterday.

Git does not close the gap. Git shows finished work, and two agents start to conflict long before either one pushes. Chat does not close the gap either, because agents do not read the team's chat.

The second problem is reach. A developer usually has write access to the whole repository, so the developer's agent has it too. An agent that is asked to make a feature work edits whatever is in the way, and some of that code belongs to another team.

OpenPlexer treats these as two separate jobs. Agents need to know what other agents are doing. Agents also need limits that hold even when a prompt asks for something else. Shared knowledge without limits does not stop an edit. Limits without shared knowledge do not stop duplicate work. The design needs both, and the rest of this page describes the parts that provide them.

## Coding agents, adapters, and the gate

A coding agent is a tool, such as Claude Code or Codex, that reads and changes code for a developer. OpenPlexer does not replace the coding agent. Each developer keeps the one they already use.

A coding agent exposes hooks. A hook is a point where the agent calls an outside program before or after it acts. An adapter is the code that connects one coding agent to OpenPlexer through the hooks of that agent. Each coding agent exposes different hooks, so each coding agent has its own adapter.

The adapter passes each action to the gate. The gate runs on the developer's machine, inside the OpenPlexer daemon. The gate asks the policy whether the action is allowed. If the policy allows the action, the coding agent continues. If the policy denies the action, the coding agent receives the reason.

The gate sees only the actions that a coding agent sends through its hooks. [What local enforcement cannot do](#what-local-enforcement-cannot-do) covers that limit.

## Policy

A policy states what each role on a team may do. The policy evaluator takes a policy and a request, and returns a result: allow, or deny with a reason.

The evaluator denies by default. If no rule in the policy allows an action, the evaluator denies the action.

The evaluator is pure. It reads no files and opens no network connections, so the same policy and the same request always give the same result. That property is what lets one set of fixtures test every implementation of the evaluator.

The daemon does not trust a policy because the policy is on disk. A policy arrives as a signed bundle, and the daemon verifies the signature against a public key before it enforces the policy. When the daemon cannot reach the hub, it keeps enforcing the last policy that it verified.

## The Room, claims, and decisions

The Room is the shared record that the coding agents of one team read and write. Two kinds of entry matter most.

- A claim says that a coding agent is working on a piece of work. Other coding agents see the claim before they start the same work. A claim expires, so an abandoned claim does not block the team.
- A decision records something that the team has settled, such as the response format of an endpoint. Every coding agent reads the same decision, so two agents do not guess two different answers.

The Room protocol defines the format of these entries. It also defines the rules for ordering, expiry, reconnection, and errors.

## The hub

The hub is the server that hosts the Room of a team and manages the policy of that team. The hub handles sign-in and membership, and it records which projects and repositories belong to the team.

The open-source hub serves one team. The team runs the hub on its own machine.

## The audit log and the verifier

The audit log is the record of what happened. Each record includes a hash of the record before it. A change to one record changes every hash after it, so an altered, removed, or reordered record breaks the chain.

The verifier is a separate program that checks an exported audit log. The verifier runs without the hub and without a network connection, so anyone can check a log for themselves.

A hash chain has a limit, and the design states the limit openly. The chain proves that the records it contains agree with each other. The chain cannot prove that the log is complete, because someone can cut off the end of the chain or replace the whole chain. To detect that, the verifier needs a trusted checkpoint. A checkpoint is a known point in the chain that comes from a source that the person who verifies already trusts.

## Contracts

A contract is the language-independent definition of a format. A contract has a README with its rules, and the schemas and examples that apply to it. The contracts live in [`spec/`](../spec/README.md), outside the Go code.

The contracts exist so that another tool can implement the Room protocol, or verify an audit log, without importing code from this repository. The fixtures are data files and not test code, so an implementation in any language can run them.

## What local enforcement cannot do

The daemon runs as the same operating-system user as the coding agent. A program that runs as that user can do anything that the user can do. The gate is therefore not a complete security boundary on its own.

The gate stops the actions that it sees. It does not see an action that a coding agent takes outside its hooks. Each adapter documents the actions that its hooks cannot see.

## What is not decided yet

Phase 0 of the [roadmap](roadmap.md) settles these questions before any implementation starts.

- The open questions of the contracts. The contracts for policies, Room events, claims, decisions, and audit records now exist in [`spec/`](../spec/README.md). See [issue 9](https://github.com/Multiplexer-si/OpenPlexer/issues/9). Each contract README ends with the questions that the owner has not decided.
- The fixtures that define the expected behavior of the policy evaluator, the Room protocol, and the audit chain. See [issue 10](https://github.com/Multiplexer-si/OpenPlexer/issues/10).
- The trust model: what data leaves the developer's machine, what the gate can and cannot prevent, and how the verifier obtains a trusted checkpoint. See [issue 11](https://github.com/Multiplexer-si/OpenPlexer/issues/11).

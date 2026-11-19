# Security policy

## Supported versions

While OpenPlexer is pre-1.0, the maintainers provide security fixes for the latest release only.

## In scope

- The Claude Code and Codex hook gate and its policy decisions.
- Policy signing, key pinning, and verification of signed bundles.
- Hub authentication, authorization, and member tokens.
- The audit chain, signed checkpoints, and the offline verifier.

## Out of scope

The gate acts on calls that the installed hooks receive. Reads, unmatched
tools, and programs that run outside the agent are outside its coverage.
For shell calls, the gate can check only the targets it can identify.
Policy rules cover paths within projects. The hook also guards specific
local configuration paths.

The developer's OS account is trusted. A person or program with the same
access can change the hook settings, change the local OpenPlexer files, or
act outside the hooks. The design also trusts the hub operator and the hub's
data store. [Trust model](docs/trust-model.md).

## Report a vulnerability

Open the repository's **Security** tab and choose **Report a vulnerability**.
[Private vulnerability report](https://github.com/Multiplexer-si/OpenPlexer/security/advisories/new).

Include the affected version and component, steps to reproduce the issue,
and what happened. Remove live tokens, private keys, and other secrets from
any logs you include.

The maintainers aim to acknowledge the report and share progress in the
private thread. If they confirm the issue, they aim to agree with you on
when to publish the advisory.

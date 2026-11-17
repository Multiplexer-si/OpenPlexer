# Local policy gate

`openplexer` enforces a signed policy bundle from a home directory on the developer's machine, with no hub and no network. The home is `$OPENPLEXER_HOME`, or `~/.openplexer`.

## Sub-features

- `openplexer configure --keys <jwks.json> --member <member-id> --stale-after-days <days>` stores the key set that verifies a bundle, the member id of the developer, and the age after which a policy is stale.
- `openplexer policy load <signed-bundle.json>` verifies a signed bundle message against the key set and makes it the policy in force. It refuses a bad signature, an older version, the same version with another content, and a bundle for another project. A refused bundle leaves the policy in force as it was.
- `openplexer policy status` prints the state of the stored policy: `absent`, `verified`, `stale`, or `rejected`. It exits 0, 3 for stale, or 4 for absent and rejected.
- `openplexer policy check [--repo <repo>] <action> <target>` decides one action. It prints `allow: <rule>` and exits 0, prints `deny: <rule>` and exits 5, or prints `no policy: <detail>` and exits 4.
- Every check and every load that changes or refuses a policy appends one record to `audit.jsonl` in the home, as a hash chain of `spec/audit/v1/record.schema.json`.

## How to get to it (user POV)

A developer runs `make build`, then `bin/openplexer configure --keys spec/policy/v1/examples/jwks/hub-keys.json --member mem-ana`, then `bin/openplexer policy load spec/policy/v1/examples/signed-bundle/message.json`, then `bin/openplexer policy check --repo api write src/orders/cart.ts`.

## Driving it with verify.sh

`verify.sh <ref> build gate`. Pass means `gate exit=0`, and `gate.log` holds one `ok` line for each step and no `FAIL` line. The steps run in a home directory that the script makes and removes.

## Gotchas

- The script makes a policy stale by moving `confirmed_at` in `policy.json` 25 hours into the past, under a limit of one day. That time is local and not signed, so the same edit also makes a stale policy fresh.
- The first step fails if `apps/openplexer` links the package `net`. That is the proof that the gate uses no network.
- The script checks that each audit record links to the record before it. It does not recompute a hash. The Go tests of `internal/daemon` do, with `audit.VerifyChain`.
- The state `rejected` is about the stored policy. A bundle that `policy load` refuses is an error of that command, and the state does not change.

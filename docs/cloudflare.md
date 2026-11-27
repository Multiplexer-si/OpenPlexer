# Deploy the OpenPlexer hub to Cloudflare

Deploy one Worker for your team. Connect Claude Code and Codex as separate members. Then export the team's audit log and verify it offline.

## 1. Get an account and build the binaries

Use a Cloudflare account with permission to deploy Workers and create Durable Objects. The hub uses one SQLite Durable Object for the team. Workers Free supports SQLite Durable Objects and is enough to try the hub. A disposable deployment passed the policy, audit, and stream budget checks on Workers Free. [Platform facts](cloudflare-hub-design.md#platform-facts)

Workers Free allows 100,000 Worker requests per day and 10 ms of CPU per Worker request. It also allows 100,000 Durable Object requests per day. Requests over the free limits fail without overage charges. Use Workers Paid for a team that keeps daemons connected all day. Each connected daemon causes about one Durable Object poll per second, or about 86,400 requests per day. [Workers limits](https://developers.cloudflare.com/workers/platform/limits/) [Durable Object pricing](https://developers.cloudflare.com/durable-objects/platform/pricing/)

On macOS or Linux, install Git, Go 1.27 or later, make, Node 24, npm 11, curl, OpenSSL, and jq. Use the same checkout for the Worker and the three OpenPlexer binaries.

```sh
git clone https://github.com/Multiplexer-si/OpenPlexer.git
cd OpenPlexer
make build
export PATH="$PWD/bin:$PATH"
cd apps/hub-cloudflare
npm ci
npx wrangler login
npx wrangler whoami
```

Open the login page and grant Wrangler access. In the output of `whoami`, find the account where the Worker will run. Replace the placeholder with that account ID. Keep this shell open for the operator commands.

```sh
export CLOUDFLARE_ACCOUNT_ID='<your-account-id>'
```

## 2. Choose the Worker name and hostname

Choose a new Worker name for each team. Edit `wrangler.toml` in the current directory. Keep the bindings and migrations from the checkout. Keep `DEV_MODE` empty.

For `workers.dev`, open **Workers & Pages** in the Cloudflare dashboard for your account. Find the account's label under **Your subdomain**, or register a subdomain there. Use the Worker name and the account subdomain in `HUB_HOSTNAME`. Replace each angle-bracket placeholder in this example. If you need to look up the subdomain after deploying, leave the example hostname for now. The first `npm run deploy` in step 3 prints `https://<name>.<subdomain>.workers.dev`. [Cloudflare workers.dev setup](https://developers.cloudflare.com/workers/configuration/routing/workers-dev/)

```toml
name = "<your-worker-name>"
workers_dev = true
preview_urls = false

[vars]
HUB_HOSTNAME = "<your-worker-name>.<your-subdomain>.workers.dev"
TEAM_ID = "<your-team-id>"
DEV_MODE = ""
```

For a custom domain, choose an unused hostname in an active Cloudflare DNS zone in this account. The Worker Custom Domain supplies DNS and TLS for that hostname. Replace the fields above with these.

```toml
name = "<your-worker-name>"
workers_dev = false
preview_urls = false
routes = [{ pattern = "<your-custom-hostname>", custom_domain = true }]

[vars]
HUB_HOSTNAME = "<your-custom-hostname>"
TEAM_ID = "<your-team-id>"
DEV_MODE = ""
```

Use the exact hostname on the route as `HUB_HOSTNAME`, without `https://` or a path. Choose a stable `TEAM_ID`, such as a team slug. The Worker uses it to find the team's SQLite object and to unwrap its keys. Keep it the same on every deploy. Keep the account ID and your local deployment edits out of commits.

If you know the hostname, set `hub` to its HTTPS origin.

```sh
hub='https://<your-hub-hostname>'
```

## 3. Set secrets and deploy

Run these commands once for a new Worker. They generate the secrets into private files outside the checkout and give Wrangler the values on standard input. The first `secret put` can create a new Worker with the configured name. Check the name before continuing.

```sh
umask 077
credentials=$(mktemp -d "$HOME/openplexer-cloudflare.XXXXXX") &&
openssl rand -hex 32 > "$credentials/bootstrap.token" &&
openssl rand -hex 32 > "$credentials/wrap-key-v1" &&
npx wrangler secret put BOOTSTRAP_TOKEN < "$credentials/bootstrap.token" &&
npx wrangler secret put WRAP_KEY_V1 < "$credentials/wrap-key-v1" &&
npm run deploy
```

If you left `HUB_HOSTNAME` at the example value, copy the hostname from the URL printed by `npm run deploy` into `HUB_HOSTNAME`. Run `npm run deploy` again. Set `hub` to the full HTTPS URL.

Check the deployment.

```sh
curl -fsS "$hub/healthz"
```

The health check prints `{"ok":true,"schema":2}` on a fresh deployment. If DNS or TLS is still provisioning, wait for Cloudflare to show the hostname as active and retry the health check.

Store a protected copy of `wrap-key-v1` separately from your audit exports. Wrapping-key rotation is unsupported. Never change or delete `WRAP_KEY_V1` on a live team. The hub would lose the ability to unwrap its signing keys.

## 4. Create the lead accounts and project

Use `mem-lee` as the first lead. Save its token in a fresh private directory.

```sh
lead_tokens=$(umask 077 && mktemp -d "$credentials/lead.XXXXXX") &&
lead_token="$lead_tokens/lee.token" &&
openplexer-hub init --hub "$hub" --token-file "$credentials/bootstrap.token" \
  --id mem-lee --lead-token-file "$lead_token"
```

If the init request is interrupted, rerun just the `openplexer-hub init` command. Keep the same `lead_token` path.

The setup block stops at the first error. It writes the new member tokens in a fresh private directory.

```sh
member_tokens=$(umask 077 && mktemp -d "$credentials/members.XXXXXX") &&
(
  set -e
  umask 077
  openplexer-hub add-member --hub "$hub" --token-file "$lead_token" \
    --id mem-sam --role lead > "$member_tokens/sam.token"
  openplexer-hub add-member --hub "$hub" --token-file "$lead_token" \
    --id mem-claude > "$member_tokens/claude.token"
  openplexer-hub add-member --hub "$hub" --token-file "$lead_token" \
    --id mem-codex > "$member_tokens/codex.token"
  openplexer-hub add-project --hub "$hub" --token-file "$lead_token" \
    --id prj-demo --repo demo
)
```

If a token file exists but is empty, the member may have been created. Check with a lead. If the member exists, have the lead remove it first. The Worker protects the last lead from removal. Delete the empty file and retry that `add-member` command.

Give the second lead their token through a private channel. Keep the two lead credentials separately so either person can administer the team. Use the second lead's token to check their access before removing the bootstrap secret.

```sh
openplexer-hub export --hub "$hub" --token-file "$member_tokens/sam.token" \
  --out "$credentials/second-lead-check"
npx wrangler secret delete BOOTSTRAP_TOKEN
rm "$credentials/bootstrap.token"
```

## 5. Publish the first policy

This policy grants both developers write access to `src/` in the example repo. Change the project ID, repo, member IDs, or paths together with the setup commands if you use your own names.

```sh
cat > "$credentials/policy.json" <<'JSON'
{"version": 1, "project_id": "prj-demo", "repos": ["demo"],
 "areas": [{"name": "app", "patterns": ["demo:src/**"]}],
 "roles": {"dev": {"read": [], "write": ["app"], "exec": []}},
 "members": {"mem-claude": "dev", "mem-codex": "dev"}}
JSON
openplexer-hub publish-policy --hub "$hub" --token-file "$lead_token" \
  --project prj-demo --file "$credentials/policy.json"
curl -fsS "$hub/.well-known/jwks.json" -o "$credentials/keys.json"
```

Send each developer the hub origin, project ID, their member ID, and `keys.json` through a trusted channel. Send only that member's token through a private channel. They will pin the keys you send and check policy signatures against that pin.

## 6. Connect each developer and their agent

On each developer machine, install and sign in to Claude Code or Codex. Start in the root of the same OpenPlexer checkout. Build the binaries and set the following values.

```sh
make build
export PATH="$PWD/bin:$PATH"
hub='https://<your-hub-hostname>'
member=mem-claude
export OPENPLEXER_HOME="$HOME/openplexer-cloudflare-$member"
umask 077
mkdir -p "$OPENPLEXER_HOME"
```

On the Codex machine, use `member=mem-codex` before setting `OPENPLEXER_HOME`. If you run both agents on one machine, use separate terminals and keep the two values of `OPENPLEXER_HOME` distinct. Save the token you received as `member.token` and the trusted keys as `keys.json` in that home. Then run:

```sh
chmod 600 "$OPENPLEXER_HOME/member.token"
openplexer configure --hub "$hub" --project prj-demo --member "$member" \
  --keys "$OPENPLEXER_HOME/keys.json" --token-file "$OPENPLEXER_HOME/member.token"
openplexer policy sync
openplexer policy status
```

Expect `policy: verified`, version 1 of `prj-demo`, and your member ID with role `dev`.

For this test, create a throwaway Git project. Use a different directory for each agent.

```sh
mkdir -p "$HOME/openplexer-demo-$member"
cd "$HOME/openplexer-demo-$member"
git init -q
```

In a second terminal for this member, start in the root of the OpenPlexer checkout. Use `mem-codex` in the export for the Codex daemon.

```sh
export PATH="$PWD/bin:$PATH"
export OPENPLEXER_HOME="$HOME/openplexer-cloudflare-mem-claude"
openplexer daemon
```

Leave the daemon running to refresh policy and send and receive Room events. In the project terminal, run `openplexer install` with `claude-code` or `codex`.

```sh
openplexer install claude-code --project .
claude
```

Accept the workspace trust dialog. Type `/hooks` and check that the OpenPlexer entry appears under `PreToolUse`. Ask Claude to use the Write tool to create `src/app.ts` with a hello world function.

```sh
openplexer install codex --project .
codex
```

Trust the project when Codex asks. Type `/hooks`, review the OpenPlexer entry, and trust it. Ask Codex to use `apply_patch` to create `README.md` with a title. The hook should deny the write. Both daemons should print a `policy.denied` Room event from the Codex member.

Exit each agent and run `openplexer policy status` in its project terminal. Both should report `policy: verified`.

## 7. Export and verify the audit log

Run this in the operator shell. Use a new output directory for each export.

```sh
audit=$(mktemp -d "$credentials/audit.XXXXXX")
openplexer-hub export --hub "$hub" --token-file "$lead_token" --out "$audit"
openplexer-verify --export "$audit/export.json" --jwks "$audit/jwks.json" \
  --checkpoint "$audit/checkpoint.jws"
jq -e 'any(.entries[]; .action == "event.policy-denied" and .actor_member_id == "mem-codex")' "$audit/export.json"
```

The verifier reports a consistent chain from genesis, with completeness through the checkpoint count. The `jq` check prints `true` once the Codex denial reaches the hub.

Export and verify daily and before upgrades or key changes. Keep each export, its checkpoint, and the trusted audit JWKS off the hub. Give reviewers the audit JWKS over a trusted channel. An audit export preserves the evidence through its checkpoint. It contains no SQLite state for restoring the team. [Audit logs](audit.md)

## 8. Rotate signing keys

Set a time when developers and audit reviewers can receive new pins. Stage both signing keys in the operator shell and fetch the updated key sets.

```sh
policy_kid=$(openplexer-hub stage-key --hub "$hub" --token-file "$lead_token" --purpose policy)
audit_kid=$(openplexer-hub stage-key --hub "$hub" --token-file "$lead_token" --purpose audit)
curl -fsS "$hub/.well-known/jwks.json" -o "$credentials/keys-next.json"
curl -fsS "$hub/.well-known/audit-jwks.json" -o "$credentials/audit-keys-next.json"
```

Send `keys-next.json` to developers and `audit-keys-next.json` to reviewers over the trusted channel. Each developer saves the new file in their OpenPlexer home and runs:

```sh
openplexer configure --keys "$OPENPLEXER_HOME/keys-next.json"
```

After developers and reviewers have the new pins, activate the keys and publish a higher version for every project. For the example project, run:

```sh
openplexer-hub activate-key --hub "$hub" --token-file "$lead_token" \
  --purpose policy --kid "$policy_kid"
openplexer-hub activate-key --hub "$hub" --token-file "$lead_token" \
  --purpose audit --kid "$audit_kid"
jq '.version += 1' "$credentials/policy.json" > "$credentials/policy-next.json"
openplexer-hub publish-policy --hub "$hub" --token-file "$lead_token" \
  --project prj-demo --file "$credentials/policy-next.json"
mv "$credentials/policy-next.json" "$credentials/policy.json"
```

Developers can run `openplexer policy sync` and `openplexer policy status` to check the new version. Verify the next export with the new trusted audit JWKS. The hub keeps prior public keys for existing signatures.

## 9. Revoke a member

Remove the member's token at the hub. Remove their grants from the policy and publish a higher version. For example, revoke the Codex member this way.

```sh
openplexer-hub remove-member --hub "$hub" --token-file "$lead_token" --id mem-codex
jq 'del(.members["mem-codex"]) | .version += 1' "$credentials/policy.json" > "$credentials/policy-next.json"
openplexer-hub publish-policy --hub "$hub" --token-file "$lead_token" \
  --project prj-demo --file "$credentials/policy-next.json"
mv "$credentials/policy-next.json" "$credentials/policy.json"
```

Remove any temporary grants for that member too if your policy has them. The old token can no longer make authenticated requests. The developer's local gate can still enforce its cached policy. The hub refuses to remove the last lead.

## 10. Redeploy

Export and verify the audit log first. Keep the same account, Worker name, Durable Object binding and class, and `TEAM_ID`. Keep the migration history in `wrangler.toml`. Wrangler preserves existing secret bindings on deploy. The Durable Object keeps the team's state.

```sh
npm run deploy
curl -fsS "$hub/healthz"
npx wrangler secret list
```

Check that `WRAP_KEY_V1` is listed. Repeat the export and verification in step 7 to check that the same audit history is still available and the hub can sign a checkpoint. The denial from step 6 should still be present.

## Cost and alerts

The design estimates about $5.40 to $6 per month on Workers Paid for 5 to 10 developers under its stated usage assumptions. On Workers Free, watch the daily Durable Object request quota while trying the hub. On either plan, watch Worker and Durable Object requests, CPU, and SQLite storage in the Cloudflare dashboard. Alert at 70% of the per-object storage cap and on sustained increases in errors, CPU limit failures, or reconnects. To inspect an invocation, run `npx wrangler tail` from the Worker directory. [Limits and cost estimate](cloudflare-hub-design.md#6-limits-and-cost)

## Check a disposable deployment

To run the API and edge checks instead of initializing the team by hand, stop after step 3. Keep other writers off this disposable Worker. From the same Worker directory, run:

```sh
./scripts/remote-check.sh --hub-url "$hub" --lead-token-file "$credentials/remote-lead.token" \
  --bootstrap-token-file "$credentials/bootstrap.token" --budget
```

On a team you already initialized, pass its lead token file and omit `--bootstrap-token-file`. Run with the same Wrangler configuration and account you deployed. The script creates test members and projects and redeploys the Worker. Its disconnect check uses Wrangler tail. It reports one result per check and exits nonzero if a check fails. Omit `--budget` to skip both stream budget checks.

To run both agents by hand after the script initializes the team, set `lead_token="$credentials/remote-lead.token"`. Start with the second command block in step 4. After the second lead test, remove the bootstrap secret as shown in step 4.

## Delete a test deployment

Stop both daemons and exit the agents. Save any audit evidence you want to keep. In the operator shell, confirm that `wrangler.toml` names your disposable Worker and run:

```sh
npx wrangler delete
```

Confirm the deletion in Wrangler. This deletes the Worker's state. In the dashboard, check that its Custom Domain is gone if you used one. A `workers.dev` subdomain belongs to the account and can still serve other Workers. On each developer machine, delete the throwaway project and its OpenPlexer home. Delete the local test credentials after you finish retaining any evidence.

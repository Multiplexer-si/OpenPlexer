# Try OpenPlexer end to end

This walkthrough runs every part of OpenPlexer on one machine in about 15 minutes. You run a hub, connect as a developer, have the gate allow one write and block another, watch the Room, and verify the audit log. Every step shows the output to expect.

Everything goes in `~/openplexer-test`. The hook is installed only in a throwaway project there, so your own `~/.claude` settings do not change. Delete the folder when you are done.

## Before you start

You need:

- macOS or Linux, with `git` and `curl`.
- The three binaries on your `PATH`. Follow [Install OpenPlexer](install.md), or see the shortcut in step 1.
- Claude Code, only for step 5. Step 5 uses a little of the Claude account you are signed in with.

Use three terminal windows. Run each command in the window that the step names.

| Window | Runs |
|---|---|
| A | The hub. It stays running. |
| B | Everything else. |
| C | The daemon, which prints the Room. It stays running. |

Port 8787 must be free. If another hub is already running on it, stop that hub first, or use another port in every command.

## 1. Install the binaries (window B)

If you have the [GitHub CLI](https://cli.github.com/), this downloads the release candidate for Apple silicon. Change `darwin_arm64` to match your system, as listed in [Install OpenPlexer](install.md).

```bash
mkdir -p ~/openplexer-test/dl && cd ~/openplexer-test/dl && gh release download v0.1.0-rc.1 -R Multiplexer-si/OpenPlexer -p '*darwin_arm64*' -p SHA256SUMS
```

Check the archive, unpack it, and copy the binaries to `~/.local/bin`.

```bash
cd ~/openplexer-test/dl && awk '$2 ~ /darwin_arm64/' SHA256SUMS | shasum -a 256 -c && tar -xzf openplexer_*_darwin_arm64.tar.gz && mkdir -p ~/.local/bin && install -m 0755 openplexer openplexer-hub openplexer-verify ~/.local/bin/
```

The check prints `OK` for the archive. If your shell cannot find `openplexer`, add the folder to your `PATH`.

```bash
export PATH="$HOME/.local/bin:$PATH"
```

## 2. Create a hub, a member, and a policy (window B)

Create the hub's data, one member with the lead role, and one project. The member's bearer token goes in `you.token`. Treat it as a password. The command creates `you.token` with mode `0600` and stops if the file already exists.

```bash
cd ~/openplexer-test && (umask 077 && set -C && (openplexer-hub init -data-dir hub >&2 && openplexer-hub add-member -data-dir hub -id mem-you -role lead) > you.token) && openplexer-hub add-project -data-dir hub -id prj-demo -repo demo
```

Write a policy. It gives the role `dev` write access to `src/` in the `demo` repository, and nothing else.

```bash
cat > ~/openplexer-test/policy.json <<'JSON'
{"version": 1, "project_id": "prj-demo", "repos": ["demo"],
 "areas": [{"name": "app", "patterns": ["demo:src/**"]}],
 "roles": {"dev": {"read": [], "write": ["app"], "exec": []}},
 "members": {"mem-you": "dev"}}
JSON
```

The hub signs the policy when you publish it.

```bash
cd ~/openplexer-test && openplexer-hub publish-policy -data-dir hub -project prj-demo -file policy.json
```

## 3. Start the hub (window A)

```bash
cd ~/openplexer-test && openplexer-hub serve -data-dir hub -listen 127.0.0.1:8787
```

Leave it running. The hub listens only on this machine.

## 4. Connect as a developer (window B)

`OPENPLEXER_HOME` keeps the test configuration out of your real home folder. Set it in every window that runs `openplexer`.

```bash
export OPENPLEXER_HOME="$HOME/openplexer-test/agent-home"
```

Fetch the hub's public keys, connect, and download the signed policy.

```bash
cd ~/openplexer-test && curl -s http://127.0.0.1:8787/.well-known/jwks.json -o keys.json && openplexer configure --hub http://127.0.0.1:8787 --project prj-demo --member mem-you --keys keys.json --token-file you.token && openplexer policy sync
```

Expected output:

```text
policy: verified
detail: version 1 of project prj-demo, confirmed <time>
member: mem-you, with the role dev
home: /Users/<you>/openplexer-test/agent-home
```

Ask the gate about two writes.

```bash
openplexer policy check write src/app.ts; openplexer policy check write README.md
```

Expected output:

```text
allow: area "app" write is granted to role dev
deny: path is in no area, and a write is denied by default
```

The answer comes from the signed policy on your machine. The gate does not ask the hub for each action.

## 5. Let Claude Code use the gate (window B)

Create the throwaway project and install the hook in it. `--project .` writes only `demo/.claude/settings.json`.

```bash
mkdir -p ~/openplexer-test/demo && cd ~/openplexer-test/demo && git init -q && openplexer install claude-code --project .
```

Start Claude Code from the same window, so that it sees `OPENPLEXER_HOME`.

```bash
claude
```

Accept the workspace trust dialog. Claude Code runs a project's hooks only after you trust the project. Type `/hooks` to see the OpenPlexer entry. Then ask Claude for two things:

1. "Create src/app.ts with a hello world function." The write succeeds.
2. "Create README.md with a title." The hook blocks it. Claude sees the reason: `OpenPlexer policy denied a write to README.md: path is in no area, and a write is denied by default.`

Exit Claude Code when you are done.

You can also run the hook without Claude Code. This sends it the same request that Claude Code sends for a write to `README.md`.

```bash
cd ~/openplexer-test/demo && printf '{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Write","cwd":"%s","tool_input":{"file_path":"%s/README.md","content":"x"}}' "$PWD" "$PWD" | openplexer hook claude-code pre-tool
```

The hook prints a JSON answer with `"permissionDecision":"deny"`. For `src/app.ts` it prints nothing, which allows the write.

## 6. Watch the Room (window C)

The daemon connects to the hub and prints each Room message as one line of JSON.

```bash
export OPENPLEXER_HOME="$HOME/openplexer-test/agent-home" && openplexer daemon
```

Leave it running. It prints the `README.md` denial from step 5, because the gate reports each denial to the Room. Then post a status message from window B.

```bash
openplexer room post <<'JSON'
{"type":"status","agent":{"id":"session-1","vendor":"claude-code"},"body":{"summary":"Hello from my agent"}}
JSON
```

Window C prints a line that contains `"summary":"Hello from my agent"`.

In this release, people and tools read the Room. The agents themselves do not receive Room messages.

## 7. Verify the audit log (window B)

Export the hub's audit log. The export command reads the data folder directly, so it works while the hub runs.

```bash
cd ~/openplexer-test && openplexer-hub export-audit -data-dir hub -out audit
```

Verify it. `openplexer-verify` makes no network calls and needs no hub files, so you can copy the `audit` folder to another machine and run it there.

```bash
openplexer-verify --export audit/export.json --jwks audit/jwks.json --checkpoint audit/checkpoint.jws
```

Expected output, where `N` is the number of records:

```text
Chain is consistent from genesis (N records). Completeness is proven through checkpoint count N.
```

To see tamper detection, open `audit/export.json`, change one character in any `"target"` value, and run the verify command again. It exits with code 1 and names the record and the broken rule, for example:

```text
record 3 (position 3): rule A3: expected hash ca32..., found "7cac...". Check the record's hashed fields
```

[Audit logs](audit.md) lists every failure and what the verifier cannot prove.

## Clean up

Stop windows A and C with Ctrl+C. Then delete the test folder and the binaries.

```bash
rm -rf ~/openplexer-test ~/.local/bin/openplexer ~/.local/bin/openplexer-hub ~/.local/bin/openplexer-verify
```

The hook lived only in `~/openplexer-test/demo`, so nothing else needs to be removed.

## Try the same walkthrough on Cloudflare

Use the source binaries and a disposable Worker for steps 1 to 3. Initialize the team over HTTPS and publish the same `prj-demo` policy with separate member IDs for Claude Code and Codex. Follow steps 1 to 5 of the [Cloudflare guide](cloudflare.md).

For step 4, use the HTTPS origin, your member token, and the operator's trusted key set. Use a separate `OPENPLEXER_HOME` per member. [Connect each developer and their agent](cloudflare.md#6-connect-each-developer-and-their-agent)

Use the same policy checks, Claude Code prompts, and Room post from steps 4 to 6. Skip the hook install in step 5 if you already installed it through the Cloudflare guide. In every terminal, use the home for the member running that command. Use the Codex hook and trust steps from the Cloudflare guide to test the second agent.

For step 7, export over HTTPS with a lead token. Then use the same verifier and edit a copy of the export to see the A3 tamper check. Stop both daemons and delete the disposable Worker when you finish. [Export and verify the audit log](cloudflare.md#7-export-and-verify-the-audit-log) · [Delete a test deployment](cloudflare.md#delete-a-test-deployment)

## Next steps

- Run a hub for a team: [Self-host the OpenPlexer hub](self-host.md).
- Use your own policy on a real project: [Local policy gate](local-policy.md) and [Claude Code hooks](hooks-claude-code.md).
- Connect Codex in the same way: [Codex hooks](hooks-codex.md).
- Learn what the gate can and cannot see: [Trust model](trust-model.md).

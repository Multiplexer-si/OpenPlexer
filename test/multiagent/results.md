# Multi-agent flow results

Run started 2026-11-19T13:08:56Z.

- claude-code · 2.1.327 (Claude Code) (model haiku)
- codex · codex-cli 0.169.0 (model gpt-6-luna)
- openplexer · v0.1.0-rc.1 · go1.27.4
- openplexer-hub · v0.1.0-rc.1 · go1.27.4

Preflight · PASS

| Step | Result | Detail |
| --- | --- | --- |
| 1. Start and initialize the hub, add members and project | PASS |  |
| 2. Publish the policy | PASS |  |
| 3. Configure both homes, pin keys and sync policy | PASS |  |
| 4. Install both project hooks and start both Room daemons | PASS |  |
| 5. Claude Code claims a path and B receives the lease event | PASS |  |
| 6. A posts a decision and B receives it | PASS |  |
| 7. Claude Code writes the allowed file and Codex is denied | PASS |  |
| 8. Restart the hub and verify B receives every Room event once | PASS |  |
| 9. Write this report | PASS | |

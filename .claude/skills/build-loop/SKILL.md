---
name: build-loop
description: Take an OpenPlexer issue from brief to merged PR. Codex builds, the coordinator runs the loopback tests Codex cannot, an independent Codex verifier checks before merge, a read-only Fable agent diagnoses unclear failures, and fix rounds are capped. Chains pstack skills at each step. Use when asked to build, fix, or ship an issue or a batch of issues in this repo.
---

# Build loop

One issue goes around this loop until an independent verifier passes it and CI is green. Several issues can go around it at once, each in its own worktree.

```
scope -> brief -> build -> apply -> gates -> verify --PASS--> PR -> CI -> merge (on the user's OK)
                    ^                  |         |
                    |               fail       FAIL
                    |                  v         v
                    +---- fix round <- diagnose (Fable, when the cause is unclear)
```

## Roles

| Who | Does | Never does |
|---|---|---|
| Coordinator (you) | Scopes, writes briefs, applies bundles, runs gates, starts verifiers and diagnosis, pushes, opens PRs, posts verdicts, merges on the user's OK | Edits repo code, tests, or docs. Codex makes every edit. |
| Codex builder | Writes code, tests, and docs from a brief, and commits | Pushes. Runs loopback tests: its sandbox refuses loopback binds and has no network. |
| Codex verifier (`gpt-6.2-astra`, reasoning `high`) | Checks the head adversarially against the issue, the design, and the diff, and writes a verdict file | Commits. Sees the builder's report. It gets the code, not your conclusion. |
| Fable diagnosis agent | Finds the root cause of an unclear failure, with evidence and a minimal fix | Edits the repo or any worktree |
| The user | Approves merges, fix rounds past the cap, and anything outward-facing | |

## Rules

- **Fix rounds.** At most 2 per PR. Past that, ask the user and name what is left. A rebase counts as part of a round.
- **Approval.** Ask before a merge, a deploy, a tag, a release edit, a repo setting change, or a real service-manager or real-agent run. One approval covers only what it names.
- **Privacy.** Never put the user's name, email, account ID, Cloudflare subdomain, or local paths in the repo, a PR, an issue, or a comment. Run `scripts/sanitize.sh` on every verdict before posting it.
- **Filters.** If OpenAI's cyber-risk filter stops a Codex run, do not reword the brief to get past it. Report it to the user, then rerun the same brief or switch to a Fable diagnosis and a narrower fix brief.
- **Order.** Follow the design's order. When parallel branches touch the same files, merge one, then have Codex rebase the other in its next round. Never resolve conflicts yourself.

## 1. Scope

- Read the issue, its design section, and the design's order and testing table (for example, section 12 of `docs/onboarding-design.md`).
- Use `pstack:blast-radius` to find the files and the other open branches the change will touch.
- Use `pstack:principle-sequence-verifiable-units`: give each unit of work a done-when that a test can prove.
- Decide which issues can run in parallel, and which must wait for another to merge.

## 2. Brief

Write `$SCRATCH/codex-<n>-brief.md` from `templates/builder-brief.md`, then append `templates/brief-common.md`. A good brief:

- names the branch, its base SHA, and what is already on main;
- quotes the issue's outcome and done-when;
- points at the design section instead of retyping it, and says the design wins over the issue on conflict;
- lists files to read first, and the done-when tests to write, including shared conformance cases for any hub change;
- names the other parallel branches and the shared files to keep edits small in;
- tells Codex to use `pstack:tdd`: a failing test first for every behavior change.

## 3. Build

Run the builder in the background, one worktree per issue (`git worktree add -b <branch> ../OpenPlexer-wt-<name> <base>`):

```sh
scripts/codex-build.sh <worktree> $SCRATCH/codex-<n>-full.md $SCRATCH/codex-<n>
```

Codex usually cannot take the worktree's git index lock. It then commits in `dist/clone` and writes `dist/pr.bundle`. Read `<prefix>-last.md` when it finishes. It is a report, not proof.

## 4. Apply

```sh
scripts/apply-bundle.sh <worktree> <branch>
```

The script fetches the bundle onto the branch, resets the worktree to it, and prints the new commits. Then it moves `dist/clone` and `dist/pr.bundle` to the Trash, so the next round starts clean.

## 5. Gates

```sh
scripts/gates.sh <worktree> <name> [worker]
```

- It clones the worktree into `$SCRATCH/clean-<name>`, so `.gocache` and module files that Codex left behind cannot reach gofmt.
- It runs `make check`, which includes the loopback `TestHub`.
- With `worker`, it also runs `npm ci`, `npm test`, and `scripts/conformance.sh` in `apps/hub-cloudflare`.
- It writes `$SCRATCH/coord-<name>.txt` with every exit code.

A failure here goes to a fix round before any verifier. A failure in a test that Codex wrote but could not run is still a failure. Quote the failing line from the log.

**Flaky failures.** Rerun twice. If results differ, do not ship. Go to step 7 with the logs. In Go code, also try `GODEBUG=clobberfree=1 GOGC=5` as a canary. It turned a 1-in-3 flake into a 3-in-3 failure in the go1.27.2 liveness bug: a variable-indexed local array was freed while still in use. The workaround is to copy the elements into locals.

## 6. Verify

Fill `templates/verifier.md` for a first check, or `templates/recheck.md` after a fix round. Then:

```sh
scripts/codex-verify.sh <verify-worktree> $SCRATCH/ver/v<n>.md $SCRATCH/ver/<n>
```

- **The verifier's worktree.** It gets its own detached worktree at the exact head (`git worktree add --detach ../OpenPlexer-wt-verify<n> <head>`). Install `node_modules` there if the Worker changed.
- **Gate results.** Tell the verifier the coordinator's gate results, because it cannot bind loopback. Tell it not to fail a gate for that.
- **Verdict.** Line 1 is `PASS`, `PASS+NOTES`, or `FAIL`. FAIL only for something it can show.
- **Your own review.** Read every finding yourself before acting:
  - Drop findings that come from your own wrong instruction in the prompt, and say so in the PR.
  - Mark contrived findings as non-blocking, and say why in one line to the user.
  - Never drop a finding because fixing it is inconvenient.

## 7. Diagnose (when the cause is unclear)

Start a background Fable agent (`model: fable`) with `templates/diagnosis.md`. Give it:

- the symptoms, with log paths;
- what you already ruled out;
- a clean clone to run in;
- read-only rules: scratch files only under `$SCRATCH/fable-<n>/`.

When it reports, run its reproduction yourself before you trust it. Then write a narrow fix brief that states the cause.

Use `pstack:why` and `pstack:principle-fix-root-causes`. The fix goes where the system first did something wrong, not where the symptom surfaced.

## 8. Fix round

Write the brief from `templates/fix-round.md`, then append `templates/brief-common.md`.

- **Findings.** List only the findings to fix, each with `file:line`, the evidence, and the expected behavior. Say "change nothing else". Name findings you accepted as non-blocking, so Codex leaves them alone.
- **Rebase.** When main moved, put the rebase in the same round, and name what main now contains.

Then repeat steps 3 to 6.

## 9. Ship

1. Push the branch: `git push -u origin <branch>`.
2. Write the PR body with `pstack:technical-writing` and `pstack:unslop`:
   - `Closes #<n>` (or `Refs #<n>` when the issue stays open);
   - what changed, by design point;
   - the coordinator's gate results;
   - each verifier round's verdict;
   - what was not run, such as a real service manager or a real deploy;
   - links to any follow-up issues.
3. Run `pstack:make-pr-easy-to-review` if the history is noisy.
4. Post each sanitized verdict as a PR comment: `scripts/sanitize.sh $SCRATCH/verdict-<n>.md > pub.md`, then `gh api repos/<owner>/<repo>/issues/<pr>/comments -F body=@pub.md`.
5. When the user agrees to merge with what is left, file the rest as an issue in the same milestone before opening the PR.
6. If CI fails, use `pstack:fix-ci` and send the fix to Codex as a fix round.
7. On green CI and the user's OK, merge: `gh pr merge <pr> --merge --match-head-commit <sha>`.
8. Confirm the issue closed. Pull main.
9. Record the step in your notes.

## 10. Close the loop

After a batch merges, run `pstack:reflect`. For each lesson, either add it to this skill or record it as memory. Lessons so far:

- Codex leaves `.gocache` in the worktree, so run gates in a clean clone.
- Tests Codex writes but cannot run often fail on the first real run.
- Cap rounds. Each round tends to find a narrower race than the last.
- When two branches add Worker migrations, the second takes the next schema version.
- Docs for unreleased commands go outside the walkthrough that pins a release.
- In zsh, write `${b}`, not `$b:r`, because `:r` is a modifier.

## Files

| File | What it is |
|---|---|
| `templates/brief-common.md` | The sandbox, the toolchain, repo rules, and the final-message format. Append it to every builder and fix brief. |
| `templates/builder-brief.md` | Skeleton for a first build brief. |
| `templates/fix-round.md` | Skeleton for a fix round. |
| `templates/verifier.md` | Skeleton for a first independent verification. |
| `templates/recheck.md` | Skeleton for a recheck after a fix round. |
| `templates/diagnosis.md` | Skeleton for a Fable diagnosis prompt. |
| `scripts/common.sh` | Shared settings that every script sources: `SCRATCH`, `GO_BIN`, the verifier model, and a Trash helper. |
| `scripts/codex-build.sh` | Runs a Codex builder in a worktree. It exits 3 if the provider's filter stopped the run. |
| `scripts/codex-verify.sh` | Runs the Codex verifier. |
| `scripts/apply-bundle.sh` | Applies `dist/pr.bundle` to a branch. |
| `scripts/gates.sh` | Runs the loopback gates in a clean clone. |
| `scripts/sanitize.sh` | Removes local paths and personal details from a verdict. |

Environment, all optional:

- `SCRATCH`: working files. The default is `$TMPDIR/openplexer-loop`.
- `GO_BIN`: the Go toolchain directory. The default is pre-commit's cached go1.27.2.
- `CODEX_VERIFY_MODEL`: the default is `gpt-6.2-astra`.
- `SANITIZE_EXTRA`: extra literal strings to redact, separated by `|`.

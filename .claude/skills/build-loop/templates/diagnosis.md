Diagnose <symptom> in <component> (branch `<branch>`, head `<head>`). Report the root cause and the minimal fix. **Do not edit any file in the repo or any worktree.** Codex makes the fix from your report. Write scratch files only under `<scratch>/fable-<n>/`. Run tests in the clean clone `<clean clone path>`. Keep any instrumentation in a copy under your scratch folder. Never touch `~/.claude`, `~/.codex`, or `~/.openplexer`.

## Setup
- Go: `export PATH=$GO_BIN:$PATH GOTOOLCHAIN=local GOFLAGS=-mod=mod`.
- <How to run the failing suite, and how long it takes.>
- <What passes reliably.>

## Symptoms
<Each run, its exit code, and the failing line. Log paths with line ranges.>

## Already checked
<What you read and ruled out, and your hypothesis, marked unconfirmed.>

## Method
Read the code. Reproduce. Try the race detector, `GODEBUG=clobberfree=1 GOGC=5`, and a build with `-gcflags=-N` to separate a code bug from a toolchain bug. Confirm or refute the hypothesis with evidence.

## Report (under 25 lines)
- The root cause of each symptom, with file:line.
- The decisive evidence, trimmed: a race report, a reproduction, or a standalone repro program.
- The minimal fix, as precise instructions for another agent. Say whether the product code or only a test is wrong.
- What you could not determine.

# Task: issue #<N>, <title> (<owner>/<repo>)

You work in this worktree, on branch `<branch>`, cut from `main` at `<base-sha>`. On main already: <what exists that this builds on>.

## The issue

<Outcome, in one or two sentences.>

Done when: <the issue's done-when, quoted>.

## Read first

- `<design doc>` section <n> (the contract) and the issue's row in its testing table.
- <Code to read, by file, and why.>
- <Docs the change will update.>

## Ordering and parallel work

<Which other branches run in parallel, the shared files to keep edits small in, and any seam another branch will extend (name the function).>

## What to build

1. <Design point, with the exact behavior and where it goes.>
2. <...>
3. Tests: <unit tests, Worker tests, shared conformance cases, CLI parity; what each proves>.
4. Docs: <files, kept short>.

# Fix round <k>: issue #<N>, <title> (branch <branch>)

You are on branch `<branch>` at `<head>`, your earlier work<, on main `<sha>`>. The coordinator ran the tests your sandbox could not: <results>. An independent verifier returned <verdict>; <what it confirmed fixed>. Fix these points, and nothing else.

<If main moved:>
1. **Rebase first.** Rebase onto `<main-sha>` (in `dist/clone` if needed) and resolve every conflict. Main now contains <what>. Keep its behavior and tests.

2. **<Blocking. Short name.>** `<file>:<line>` <what happens, with the evidence>. <Expected behavior, and constraints.> Test: <the case that fails before and passes after>.
3. <...>

Not required: <findings accepted as non-blocking>. Leave them.

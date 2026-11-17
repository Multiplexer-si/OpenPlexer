import { asCommitted } from "../output.ts";
import type { Reference } from "../reference.ts";
import { fixtureFile, fixturePath, type Case, type FixtureFile } from "./format.ts";

const SOURCES = ["apps/hub/src/claims/overlap.ts"];

const PATTERN_PAIRS: [name: string, why: string, a: string, b: string][] = [
  ["two equal paths overlap", "Two paths overlap only if they are equal after normalization.", "src/orders/cart.ts", "src/orders/cart.ts"],
  ["two different paths do not overlap", "Two paths overlap only if they are equal after normalization.", "src/orders/cart.ts", "src/orders/total.ts"],
  ["a leading ./ is ignored", "A leading `./` is removed before the comparison.", "./src/orders/cart.ts", "src/orders/cart.ts"],
  ["a repeated slash is ignored", "`//` is the same as `/`.", "src//orders/cart.ts", "src/orders/cart.ts"],
  ["a backslash is a separator", "`\\` is replaced by `/`.", "src\\orders\\cart.ts", "src/orders/cart.ts"],
  ["a .. segment is not resolved", "The claims dialect does not resolve `..`. The policy dialect does.", "src/orders/../orders/cart.ts", "src/orders/cart.ts"],
  ["paths that differ in case do not overlap", "The claims dialect is case-sensitive. The policy dialect is not: see `the path is compared without case` in the policy fixture `evaluate-paths`.", "src/orders/Cart.ts", "src/orders/cart.ts"],
  ["a glob that differs in case from a path does not overlap", "The claims dialect is case-sensitive for globs too.", "SRC/**", "src/orders/cart.ts"],
  ["a glob overlaps a path that it matches", "A glob and a path overlap if the glob matches the path.", "src/**", "src/orders/cart.ts"],
  ["a path overlaps a glob that matches it", "The order of the two arguments does not matter.", "src/orders/cart.ts", "src/**"],
  ["a * glob does not overlap a deeper path", "`*` does not cross `/`.", "src/*.ts", "src/orders/cart.ts"],
  ["dir/** does not overlap the directory itself", "The claims dialect has no rule that makes `dir/**` match `dir`. The policy dialect has that rule.", "src/**", "src"],
  ["**/ in the middle matches zero directories", "`a/**/b` matches `a/b`.", "src/**/cart.ts", "src/cart.ts"],
  ["a class matches a listed character", "`[ab]` matches one of the listed characters.", "src/[ab].ts", "src/a.ts"],
  ["[!] matches a slash", "`[!]` is a negated class with no members, which matches any one character, as in the policy dialect.", "src/a[!]b", "src/a/b"],
  ["a negated class with members matches a slash", "`[!a]` matches every character that is not `a`, `/` included, as in the policy dialect.", "t[!a]x", "t/x"],
  ["a negated class does not match one character outside the Basic Multilingual Plane", "A class matches one UTF-16 code unit, and U+1F600 is two, as in the policy dialect.", "t[!a]x", "t\u{1f600}x"],
  ["a class whose range holds the slash matches a slash", "`[.-0]` is the range from `.` to `0`, which holds `/`.", "t[.-0]x", "t/x"],
  ["[^] matches a line feed", "`[^]` is the same as `[!]`, and it matches a line terminator.", "src/[^]", "src/\n"],
  ["[!]x] is [!] followed by x and ]", "The class ends at the first `]` after its first character.", "src/[!]x]", "src/ax]"],
  ["a class whose first character is ] does not match its other member", "The reference reads `[]` as a class that matches nothing, so the glob matches no path. The seventh open question of the README records the behavior.", "src/[]x]", "src/x"],
  ["a class whose first character is ] does not match that character", "The reference reads `[]` as a class that matches nothing, so the glob matches no path. The seventh open question of the README records the behavior.", "src/[]x]", "src/]"],
  ["[]] does not match ]", "The reference reads `[]` as a class that matches nothing, so the glob matches no path. The seventh open question of the README records the behavior.", "src/[]]", "src/]"],
  ["a ? does not match two characters", "`?` matches exactly one character.", "src/?.ts", "src/ab.ts"],
  ["** does not match a line feed", "`**` matches any run of characters except the four line terminators, as in the policy dialect.", "src/**", "src/a\nb.ts"],
  ["** does not match a carriage return", "`**` matches any run of characters except the four line terminators.", "src/**", "src/a\rb.ts"],
  ["** does not match U+2028", "`**` matches any run of characters except the four line terminators.", "src/**", "src/a\u2028b.ts"],
  ["** does not match U+2029", "`**` matches any run of characters except the four line terminators.", "src/**", "src/a\u2029b.ts"],
  ["* matches a line feed", "Only `**` excludes the line terminators. `*` excludes only `/`.", "src/*", "src/a\nb.ts"],
  ["two equal paths with a line feed overlap", "A path is compared as text, so a line terminator in it changes nothing.", "src/a\nb.ts", "src/a\nb.ts"],
  ["two globs overlap when one literal prefix starts the other", "Two globs overlap if the literal prefix of one is a prefix of the other.", "src/**", "src/orders/*.ts"],
  ["two globs under different directories do not overlap", "Neither literal prefix starts the other, and neither glob matches the other prefix.", "src/orders/**", "src/billing/**"],
  ["a glob with an empty literal prefix overlaps every glob", "An empty prefix is a prefix of everything. The comparison reports more overlaps than exist, on purpose.", "**/invites/*", "api/**"],
  ["two globs with the same prefix overlap even if no path matches both", "`a/*.ts` and `a/*.js` share the prefix `a/`. The comparison reports more overlaps than exist, on purpose.", "src/*.ts", "src/*.js"],
  ["the literal prefix ends at a [ that starts no class", "The literal prefix of `src/a[b` is `src/a`, although its `[` starts no class and matches itself. The two prefixes are equal, so the globs overlap.", "src/a[b", "src/a[c*"],
];

const SET_PAIRS: [name: string, why: string, a: string[], b: string[]][] = [
  ["sets with one overlapping pair overlap", "Two sets overlap if any pattern of one overlaps any pattern of the other.", ["docs/**", "src/orders/cart.ts"], ["web/**", "src/orders/**"]],
  ["sets with no overlapping pair do not overlap", "Every pair must be disjoint for the sets to be disjoint.", ["docs/**", "src/orders/cart.ts"], ["web/**", "src/billing/**"]],
  ["an empty set overlaps nothing", "A claim with no paths conflicts with no claim.", [], ["src/**"]],
  ["two empty sets do not overlap", "There is no pair to compare.", [], []],
];

export function claimsOverlapFixtures(reference: Reference): [string, FixtureFile][] {
  const patterns = PATTERN_PAIRS.map(([name, why, a, b]): Case => {
    const input = asCommitted({ a, b });
    return { name, why, input, expected: { overlap: reference.patternsOverlap(input.a, input.b) } };
  });
  const sets = SET_PAIRS.map(([name, why, a, b]): Case => {
    const input = asCommitted({ a, b });
    return { name, why, input, expected: { overlap: reference.pathSetsOverlap(input.a, input.b) } };
  });
  return [
    [
      fixturePath("room", "claims-patterns-overlap"),
      fixtureFile({ contract: "room", operation: "patterns-overlap", description: "Whether two patterns of the claims dialect overlap.", sources: SOURCES, cases: patterns }),
    ],
    [
      fixturePath("room", "claims-path-sets-overlap"),
      fixtureFile({ contract: "room", operation: "path-sets-overlap", description: "Whether the path lists of two claims overlap.", sources: SOURCES, cases: sets }),
    ],
  ];
}

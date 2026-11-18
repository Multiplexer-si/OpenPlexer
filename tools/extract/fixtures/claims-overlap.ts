import { asCommitted } from "../output.ts";
import type { Reference } from "../reference.ts";
import { fixtureFile, fixturePath, type Case, type FixtureFile } from "./format.ts";

function outcome(run: () => boolean): Pick<Case, "expected" | "informational"> {
  try {
    return { expected: { overlap: run() } };
  } catch (error) {
    return { expected: { throws: true }, informational: { error: (error as Error).message } };
  }
}

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

const UNDEFINED_CLASS = "The seventh open question of the README gives the rule.";

const CLASS_SYNTAX_PAIRS: [name: string, why: string, a: string, b: string][] = [
  ["a * after a first ] repeats the empty class", "The reference reads `[]*]` as the class that matches nothing, any number of times, and then `]`. So `a[]*]` matches `a]`.", "a[]*]", "a]"],
  ["a * after a first ] still needs the ]", "`a[]*]` does not match `a`.", "a[]*]", "a"],
  ["a ? after a first ] makes the empty class optional", "`src/[]?]` matches `src/]`.", "src/[]?]", "src/]"],
  ["a count of zero after a first ] removes the empty class", "`a[]{0}]x` matches `a]x`.", "a[]{0}]x", "a]x"],
  ["a | after a first ] splits the whole glob", "The second alternative is not held to the start of the path, so `ab[]|c]` matches every path that ends in `c]`.", "ab[]|c]", "zc]"],
  ["a | after a first ] does not match another ending", "The path must still end in `c]`.", "ab[]|c]", "zd]"],
  ["the part before a first ] with a | matches no path alone", "The first alternative holds the class that matches nothing.", "ab[]|c]", "ab"],
  ["a glob after a first ] with a | is held to its directory", "`**` after the alternative matches as in any glob.", "a[]|b]/**", "zb]/x"],
  ["a letter after a first ] is compared with case", "The claims dialect is case-sensitive in a class too.", "[]|B]", "xb]"],
  ["a . after a first ] matches one character", "The `.` is the dot of a regular expression.", "[]|a.]", "xab]"],
  ["a ^ after a first ] holds the alternative to the start", "`[]|^b]` matches `b]`.", "[]|^b]", "b]"],
  ["a ^ after a first ] does not match after the start", "`[]|^b]` does not match `ab]`.", "[]|^b]", "ab]"],
  ["a $ after a first ] matches no path before the ]", "The `]` of the class follows the `$`, so the alternative never matches.", "[]|b$]", "b]"],
  ["a count after a first ] needs its least number", "`a{2,3}` needs two `a`.", "[]|a{2,3}]", "a]"],
  ["a count after a first ] matches within its bounds", "`a{2,3}` matches two `a`.", "[]|a{2,3}]", "xaa]"],
  ["a { that starts no count is a character", "`{,2}` has no least number, so the reference reads its characters as they are.", "[]|a{,2}]", "a{,2}]"],
  ["a group after a first ] can span two classes", "The `(` of the first class is closed by the `)` of the second.", "[]|(]a[]|b)]", "xb]"],
  ["a group with a name after a first ] is a group", "`(?<n>a)+` matches one `a` or more.", "[]|(?<n>a)+]", "aaa]"],
  ["a lookbehind after a first ] matches after its text", "`(?<=b)]` matches a `]` that follows `b`.", "[]|(?<=b)]", "xb]"],
  ["a lookbehind after a first ] does not match after other text", "`(?<=b)]` does not match a `]` that follows `a`.", "[]|(?<=b)]", "xa]"],
  ["a negative lookbehind after a first ] matches after other text", "`(?<!b)c` matches a `c` that does not follow `b`.", "[]|(?<!b)c]", "ac]"],
  ["a negative lookbehind after a first ] does not match after its text", "`(?<!b)c` does not match a `c` that follows `b`.", "[]|(?<!b)c]", "bc]"],
  ["a lookahead after a first ] matches before its text", "`(?=c)c` matches `c`.", "[]|(?=c)c]", "c]"],
  ["a negative lookahead after a first ] does not match before its text", "`(?!c).` does not match `c`.", "[]|(?!c).]", "c]"],
  ["a negative lookahead after a first ] matches before other text", "`(?!c).` matches `d`.", "[]|(?!c).]", "d]"],
  ["a \\ after a first ] is a separator", "A `\\` is `/` before the glob is read, so `\\d` is `/d` and not a digit.", "[]|\\d]", "x/d]"],
  ["two globs overlap when one matches the literal prefix of the other", "The literal prefix of `zc]*` is `zc]`, which `ab[]|c]` matches. Neither literal prefix starts the other.", "ab[]|c]", "zc]*"],
  ["two globs do not overlap when neither matches the prefix of the other", "The literal prefix of `zd]*` is `zd]`, which `ab[]|c]` does not match.", "ab[]|c]", "zd]*"],
  ["a range that is out of order throws against a path", `The reference throws when it reads the glob. ${UNDEFINED_CLASS}`, "[z-a]", "z"],
  ["a range that is out of order throws against the same glob", `The reference reads a glob before it compares two globs. ${UNDEFINED_CLASS}`, "[z-a]", "[z-a]"],
  ["a range that is out of order throws against another glob", `The reference reads the first glob before it compares the literal prefixes. ${UNDEFINED_CLASS}`, "[z-a]*", "x*"],
  ["a glob that matches the empty prefix of a glob that throws overlaps it", "The reference reads the first glob, finds that it matches the literal prefix of the second, and never reads the second. With the two in the other order it throws.", "**", "[z-a]"],
  ["a group that is not closed throws", `\`[](]\` is the regular expression \`[](]\`. ${UNDEFINED_CLASS}`, "[](]", "a"],
  ["a * with nothing to repeat throws", `In \`[]|*]\` no term is before the \`*\`. ${UNDEFINED_CLASS}`, "[]|*]", "a"],
  ["a ** after a first ] throws", `The second \`*\` has nothing to repeat. ${UNDEFINED_CLASS}`, "[]**]", "a"],
  ["a count with its bounds out of order throws", `\`a{3,2}\` has a least number above its greatest. ${UNDEFINED_CLASS}`, "[]|a{3,2}]", "aa]"],
  ["two groups with the same name throw", `A name can be used once. ${UNDEFINED_CLASS}`, "[]|(?<n>a)(?<n>b)]", "ab]"],
];

const SET_PAIRS: [name: string, why: string, a: string[], b: string[]][] = [
  ["sets with one overlapping pair overlap", "Two sets overlap if any pattern of one overlaps any pattern of the other.", ["docs/**", "src/orders/cart.ts"], ["web/**", "src/orders/**"]],
  ["sets with no overlapping pair do not overlap", "Every pair must be disjoint for the sets to be disjoint.", ["docs/**", "src/orders/cart.ts"], ["web/**", "src/billing/**"]],
  ["an empty set overlaps nothing", "A claim with no paths conflicts with no claim.", [], ["src/**"]],
  ["two empty sets do not overlap", "There is no pair to compare.", [], []],
  ["a pattern that throws makes the lists throw", "The reference compares the pairs in order, and the second pair throws. The seventh open question of the README gives the rule.", ["src/a.ts", "[z-a]"], ["docs/**"]],
  ["a pair that overlaps before the pattern that throws makes the lists overlap", "The reference stops at the first pair that overlaps, so it never reads the pattern that throws.", ["src/a.ts", "[z-a]"], ["src/**"]],
];

export function claimsOverlapFixtures(reference: Reference): [string, FixtureFile][] {
  const pattern = ([name, why, a, b]: (typeof PATTERN_PAIRS)[number]): Case => {
    const input = asCommitted({ a, b });
    return { name, why, input, ...outcome(() => reference.patternsOverlap(input.a, input.b)) };
  };
  const sets = SET_PAIRS.map(([name, why, a, b]): Case => {
    const input = asCommitted({ a, b });
    return { name, why, input, ...outcome(() => reference.pathSetsOverlap(input.a, input.b)) };
  });
  return [
    [
      fixturePath("room", "claims-patterns-overlap"),
      fixtureFile({ contract: "room", operation: "patterns-overlap", description: "Whether two patterns of the claims dialect overlap.", sources: SOURCES, cases: PATTERN_PAIRS.map(pattern) }),
    ],
    [
      fixturePath("room", "claims-class-syntax"),
      fixtureFile({
        contract: "room",
        operation: "patterns-overlap",
        description: "Whether two patterns overlap when one holds a class whose first character is `]`, or a range that is out of order. The reference reads such a class as regular expression syntax, or throws.",
        sources: SOURCES,
        cases: CLASS_SYNTAX_PAIRS.map(pattern),
      }),
    ],
    [
      fixturePath("room", "claims-path-sets-overlap"),
      fixtureFile({ contract: "room", operation: "path-sets-overlap", description: "Whether the path lists of two claims overlap.", sources: SOURCES, cases: sets }),
    ],
  ];
}

import { createHash } from "node:crypto";
import type { Reference } from "../reference.ts";
import { fixtureFile, fixturePath, type Case, type FixtureFile } from "./format.ts";

const raw = String.raw;

/** Each input is JSON text. The text is the input, because a parsed value cannot show `1.0`, `-0`, or an escape. */
const CASES: [name: string, why: string, json: string][] = [
  ["an empty object", "An empty object is `{}`.", "{}"],
  ["an empty array", "An empty array is `[]`.", "[]"],
  ["null at the top level", "A value that is not an object or an array is written as JSON writes it.", "null"],
  ["a string at the top level", "A value that is not an object or an array is written as JSON writes it.", '"text"'],
  ["white space between tokens is removed", "The output has no white space outside strings.", '{ "a" : [ 1 , 2 ] ,\n\t"b" : "x y" }'],
  ["keys are sorted", "The keys of an object are written in ascending order.", '{"b":1,"c":2,"a":3}'],
  ["keys are sorted at every depth and arrays keep their order", "Sorting applies to every object. It never reorders an array.", '{"z":{"b":[{"y":1,"x":2}],"a":null},"a":[3,2,1]}'],
  ["empty containers inside containers", "Empty objects and arrays are written at any depth.", '{"a":{},"b":[],"c":[[],{}]}'],
  ["keys are compared by code unit, so uppercase sorts before lowercase", "The order is by UTF-16 code unit, not by any alphabet: `1` < `B` < `_` < `a`.", '{"a":1,"B":2,"_":3,"1":4}'],
  ["a key that is a prefix of another sorts first", "The empty key sorts before every other key.", '{"ab":1,"a":2,"":3}'],
  ["keys that look like numbers are sorted as strings", "`10` sorts before `9`.", '{"9":1,"10":2,"1":3}'],
  [
    "keys are sorted by UTF-16 code unit, not by code point",
    "U+1F600 is the surrogate pair D83D DE00 in UTF-16, so it sorts before U+FF5E. Sorted by code point or by UTF-8 byte, U+FF5E would sort first.",
    raw`{"～":1,"😀":2}`,
  ],
  ["a repeated key keeps the last value", "The parser decides: the later member replaces the earlier one.", '{"a":1,"b":2,"a":3}'],
  ["characters outside ASCII are written as they are", "Only the characters that JSON requires are escaped.", raw`{"name":"Zoë 日本"}`],
  ["a character outside the Basic Multilingual Plane is written as it is", "A valid surrogate pair is written as the character, which is four bytes in UTF-8.", raw`"😀"`],
  ["<, >, and & are not escaped", "Characters that are special in HTML are written as they are.", '"<script>alert(1 & 2)</script>"'],
  ["U+2028 and U+2029 are not escaped", "The two line separator characters are written as they are.", raw`"a b c"`],
  ["the quote and the backslash are escaped", '`"` is written as `\\"` and `\\` as `\\\\`.', raw`"say \"hi\" \\ bye"`],
  ["the slash is not escaped", "An escaped `/` in the input is written as `/`.", raw`"a\/b"`],
  ["control characters with a short escape use it", "U+0008, U+0009, U+000A, U+000C, and U+000D are written as `\\b`, `\\t`, `\\n`, `\\f`, and `\\r`.", raw`"\b\t\n\f\r"`],
  ["other control characters use \\u with lowercase hexadecimal", "Every other character below U+0020 is written as `\\u00xx` in lowercase.", raw`"\u0000\u0001\u000B\u001F"`],
  ["U+007F is not escaped", "Only the characters below U+0020 are control characters for this purpose.", raw`"\u007f"`],
  ["a lone high surrogate is escaped", "A surrogate with no partner is written as a `\\u` escape in lowercase.", raw`"\ud800"`],
  ["a lone low surrogate is escaped", "A surrogate with no partner is written as a `\\u` escape in lowercase.", raw`"x\uDC00y"`],
  ["surrogates in the wrong order are both escaped", "A low surrogate followed by a high surrogate is not a pair.", raw`"\ude00\ud83d"`],
  ["1.0 is written as 1", "A number with a zero fraction is written as an integer.", "1.0"],
  ["2.50 is written as 2.5", "Trailing zeros of a fraction are dropped.", "2.50"],
  ["-0 is written as 0", "Negative zero loses its sign.", "-0"],
  ["an exponent is expanded below 1e21", "`1E3` is written as `1000`.", "1E3"],
  ["1e20 is written in full", "The largest power of ten that is written without an exponent.", "1e20"],
  ["1e21 is written as 1e+21", "From 1e21 on, a number is written with an exponent and an explicit `+`.", "1e21"],
  ["-1e21 is written as -1e+21", "The limit of 1e21 is a limit on the absolute value.", "-1e21"],
  ["0.000001 is written in full", "The smallest power of ten that is written without an exponent.", "0.000001"],
  ["0.0000001 is written as 1e-7", "Below 1e-6, a number is written with an exponent.", "0.0000001"],
  ["-0.0000001 is written as -1e-7", "The limit of 1e-6 is a limit on the absolute value.", "-0.0000001"],
  ["a large integer is rounded to the nearest double", "Every number is an IEEE 754 double. 9007199254740993 is not one, so it becomes 9007199254740992.", "9007199254740993"],
  ["a 21-digit integer is rounded and written in full", "The shortest digits that identify the double are written, then zeros.", "123456789012345678901"],
  ["extra digits of a fraction are dropped", "The shortest digits that identify the double are written.", "3.141592653589793238"],
  ["the smallest double", "A subnormal number is written with an exponent.", "5e-324"],
  ["a number too large for a double is written as null", "1e400 is read as infinity, and infinity is written as `null`.", "1e400"],
  ["numbers inside an array", "Number rules apply at any depth.", "[1.0,-0,1e21,0.0000001,-1.50]"],
];

export function canonicalJsonFixtures(reference: Reference): [string, FixtureFile][] {
  const cases = CASES.map(([name, why, json]): Case => {
    const canonical = reference.canonicalJson(JSON.parse(json));
    return { name, why, input: { json }, expected: { canonical, sha256: createHash("sha256").update(canonical, "utf8").digest("hex") } };
  });
  return [
    [
      fixturePath("canonical-json", "canonicalize"),
      fixtureFile({
        contract: "canonical-json",
        operation: "canonicalize",
        description: "JSON texts and their canonical form. `sha256` is the SHA-256 of the UTF-8 bytes of `canonical`, in lowercase hexadecimal.",
        sources: ["apps/hub/src/policy/canonical.ts"],
        cases,
      }),
    ],
  ];
}

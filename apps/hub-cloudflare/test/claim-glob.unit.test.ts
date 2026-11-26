import { describe, expect, test } from "vitest";
import classes from "../../../spec/room/v1/fixtures/claims-class-syntax.json?raw";
import patterns from "../../../spec/room/v1/fixtures/claims-patterns-overlap.json?raw";
import pathSets from "../../../spec/room/v1/fixtures/claims-path-sets-overlap.json?raw";
import { pathsOverlap, patternsOverlap } from "../src/claim-glob";

type Case<T> = { name: string; input: { a: T; b: T }; expected: { overlap?: boolean; throws?: boolean } };

function verifyCases<T>(name: string, source: string, overlaps: (a: T, b: T) => boolean): void {
  const fixtures: { cases: Case<T>[] } = JSON.parse(source);
  describe(name, () => {
    test.each(fixtures.cases)("$name", ({ input: { a, b }, expected }) => {
      const want = expected.throws === true || expected.overlap === true;
      expect(overlaps(a, b)).toBe(want);
      expect(overlaps(b, a)).toBe(want);
    });
  });
}

verifyCases("claims-patterns-overlap", patterns, patternsOverlap);
verifyCases("claims-class-syntax", classes, patternsOverlap);
verifyCases("claims-path-sets-overlap", pathSets, pathsOverlap);

test("bounds glob work without changing ordinary and exotic matches within the budget", () => {
  expect(patternsOverlap("a".repeat(4096), "b".repeat(4097))).toBe(true);
  expect(patternsOverlap("*a".repeat(750) + "c", "a".repeat(4096))).toBe(true);
  expect(patternsOverlap("*a".repeat(700) + "c", "a".repeat(4096))).toBe(false);
  expect(patternsOverlap("**/".repeat(400) + "c", "a/".repeat(2048))).toBe(false);
  expect(patternsOverlap("[]|(?=(?:a*){10000000})]", "docs/guide.md")).toBe(true);
  expect(patternsOverlap("[]|^(?:(?:x?)*$|x)*y]", "x".repeat(4096))).toBe(false);
  expect(patternsOverlap("[]|" + "(?<=".repeat(12) + "a" + ")".repeat(12) + "]", "a".repeat(4095) + "]")).toBe(true);
});

import { readdirSync, readFileSync } from "node:fs";
import { URL } from "node:url";
import { expect, test } from "vitest";

const directory = new URL(".", import.meta.url);
const workerTests = readdirSync(directory)
  .filter((name) => name.endsWith(".test.ts") && !name.endsWith(".unit.test.ts"));

test.each(workerTests)("%s keeps Worker loopback out of shared request helpers", (name) => {
  const source = readFileSync(new URL(name, directory), "utf8");
  // Shared helpers are top-level declarations. Tests can still call the entrypoint explicitly.
  const loopbackHelpers = source.split(/^(?=\S)/m).flatMap((declaration) => {
    const helper = /^(?:const|(?:async )?function) (\w+)/.exec(declaration)?.[1];
    return helper && /\bexports\s*\.\s*default\s*\.\s*fetch\b/.test(declaration) ? [helper] : [];
  });
  expect(loopbackHelpers).toEqual([]);
});

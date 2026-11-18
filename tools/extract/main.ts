import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { examples, signedExample } from "./examples.ts";
import { adapterHookFixtures } from "./fixtures/adapter-hooks.ts";
import { auditChainFixtures } from "./fixtures/audit-chain.ts";
import { canonicalJsonFixtures } from "./fixtures/canonical-json.ts";
import { claimsOverlapFixtures } from "./fixtures/claims-overlap.ts";
import { FIXTURE_FILE_PATH, fixtureFileSchema } from "./fixtures/format.ts";
import { policyEvaluateFixtures } from "./fixtures/policy-evaluate.ts";
import { roomErrorFixtures } from "./fixtures/room-errors.ts";
import { roomSequencingFixtures } from "./fixtures/room-sequencing.ts";
import { signedBundleFixtures } from "./fixtures/signed-bundle.ts";
import { validationFixtures } from "./fixtures/validation.ts";
import { explain, ownedFiles, read, remove, render, write, type Files, type Json, type SealedFiles } from "./output.ts";
import { loadReference, REFERENCE_COMMIT, type Reference } from "./reference.ts";
import { contractSchemas } from "./schemas.ts";

const USAGE = `usage: tsx tools/extract/main.ts <generate|check> --reference <dir> [--new-signatures]

generate  Writes the schemas, the examples, and the fixtures under spec/.
check     Recomputes every file in memory and compares it with the committed file.
          Prints each difference and exits with status 1 if there is one.

--reference <dir>   A checkout of the existing TypeScript implementation at commit ${REFERENCE_COMMIT}.
--new-signatures    With generate: makes a new signing key and writes the signed files again.`;

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

interface Plan {
  /** Files that every run reproduces byte for byte. */
  files: Files;
  /** Signed files. A run can verify them, but it cannot reproduce them. */
  sealed: SealedFiles[];
}

async function plan(reference: Reference): Promise<Plan> {
  const fixtures = [
    ...[policyEvaluateFixtures, validationFixtures, claimsOverlapFixtures, roomSequencingFixtures, roomErrorFixtures, canonicalJsonFixtures, auditChainFixtures].flatMap((build) => build(reference)),
    ...(await adapterHookFixtures(reference)),
  ];
  const values = new Map<string, Json>([
    ...contractSchemas(reference),
    ...examples(reference),
    [FIXTURE_FILE_PATH, fixtureFileSchema()],
    ...fixtures.map(([path, file]): [string, Json] => [path, file as unknown as Json]),
  ]);
  return {
    files: new Map([...values].map(([path, value]) => [path, render(value)])),
    sealed: [signedExample(reference), ...signedBundleFixtures(reference)],
  };
}

async function verifySealed(unit: SealedFiles): Promise<string[]> {
  const committed: Files = new Map();
  for (const path of unit.paths) {
    const content = read(root, path);
    if (content === undefined) return [`${path}: the file is missing; run generate`];
    committed.set(path, content);
  }
  return unit.verify(committed);
}

function unexpectedFiles({ files, sealed }: Plan): string[] {
  const known = new Set([...files.keys(), ...sealed.flatMap((unit) => unit.paths)]);
  return ownedFiles(root).filter((path) => !known.has(path));
}

async function generate(p: Plan, newSignatures: boolean): Promise<string[]> {
  for (const [path, content] of p.files) {
    if (read(root, path) !== content) write(root, path, content);
  }
  for (const path of unexpectedFiles(p)) remove(root, path);
  const problems: string[] = [];
  for (const unit of p.sealed) {
    if (newSignatures || unit.paths.some((path) => read(root, path) === undefined)) {
      for (const [path, content] of await unit.create()) write(root, path, content);
    }
    problems.push(...(await verifySealed(unit)));
  }
  return problems;
}

async function check(p: Plan): Promise<string[]> {
  const problems = [...p.files].flatMap(([path, content]) => explain(path, read(root, path), content));
  problems.push(...unexpectedFiles(p).map((path) => `${path}: the generator does not produce this file`));
  for (const unit of p.sealed) problems.push(...(await verifySealed(unit)));
  return problems;
}

async function main(): Promise<number> {
  const [mode, ...rest] = process.argv.slice(2);
  const at = rest.indexOf("--reference");
  const referenceDir = at === -1 ? undefined : rest[at + 1];
  const flags = rest.filter((arg, i) => i !== at && i !== at + 1);
  const newSignatures = flags.includes("--new-signatures");
  const unknown = flags.filter((flag) => flag !== "--new-signatures");
  if ((mode !== "generate" && mode !== "check") || referenceDir === undefined || unknown.length > 0 || (newSignatures && mode !== "generate")) {
    console.error(USAGE);
    return 2;
  }
  const p = await plan(await loadReference(resolve(referenceDir)));
  const problems = mode === "generate" ? await generate(p, newSignatures) : await check(p);
  for (const problem of problems) console.error(problem);
  const count = p.files.size + p.sealed.reduce((n, unit) => n + unit.paths.length, 0);
  const noun = problems.length === 1 ? "problem" : "problems";
  console.error(`${mode}: ${count} files, ${problems.length} ${noun}`);
  return problems.length === 0 ? 0 : 1;
}

process.exitCode = await main().catch((e: unknown) => {
  console.error(e instanceof Error ? e.message : String(e));
  return 2;
});

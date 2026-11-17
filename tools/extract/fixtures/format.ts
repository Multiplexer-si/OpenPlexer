import type { Json } from "../output.ts";
import { REFERENCE_COMMIT } from "../reference.ts";
import { assertPortablePatterns, schemaId } from "../schemas.ts";

export interface Case {
  /** Unique in the file. A harness reports a failure by this name. */
  name: string;
  /** One sentence that names the behavior that the case pins. */
  why: string;
  input: Json;
  expected: Json;
  /** Output of the reference that is not part of the contract, such as the wording of a message. */
  informational?: { [key: string]: Json };
}

export interface FixtureFile {
  contract: "policy" | "room" | "audit" | "canonical-json";
  version: 1;
  operation: string;
  description: string;
  reference: { commit: string; sources: string[] };
  /** For `validate`: the `$id` of the schema that the cases are validated against. */
  schema?: string;
  /** Input that every case of the file shares. */
  shared?: { [key: string]: Json };
  cases: Case[];
}

export const FIXTURE_FILE_ID = "urn:openplexer:spec:fixture-file";
export const FIXTURE_FILE_PATH = "spec/fixture-file.schema.json";

export const fixturePath = (contract: FixtureFile["contract"], name: string): string => `spec/${contract}/v1/fixtures/${name}.json`;

export function fixtureFile(file: Omit<FixtureFile, "version" | "reference"> & { sources: string[] }): FixtureFile {
  const { sources, ...rest } = file;
  const names = new Set(rest.cases.map((c) => c.name));
  if (names.size !== rest.cases.length) throw new Error(`${rest.contract} ${rest.operation}: two cases share a name`);
  return { ...rest, version: 1, reference: { commit: REFERENCE_COMMIT, sources } };
}

type Schema = { [key: string]: Json };

interface Operation {
  contract: FixtureFile["contract"][];
  description: string;
  /** The schema of `shared`. Absent means that the file must not have `shared`. */
  shared?: Schema;
  /** True for `validate`, the one operation that names a schema under test. */
  schemaUnderTest?: true;
  input: Schema;
  expected: Schema;
  informational?: Schema;
}

const strict = (properties: Schema, required: string[] = Object.keys(properties)): Schema => ({ type: "object", properties, required, additionalProperties: false });
const hex64: Schema = { type: "string", pattern: "^[0-9a-f]{64}$" };

const OPERATIONS: Record<string, Operation> = {
  evaluate: {
    contract: ["policy"],
    description: "Evaluate the query in `input` against the bundle in `shared`.",
    shared: strict({ bundle: { $ref: schemaId("policy", "bundle") } }),
    input: { $ref: schemaId("policy", "eval-query") },
    expected: strict({ decision: { enum: ["allow", "deny"] }, area: { type: "string" } }, ["decision"]),
    informational: strict({ rule: { type: "string" } }),
  },
  validate: {
    contract: ["policy", "room"],
    description: "Validate `input` against the schema that `schema` names.",
    schemaUnderTest: true,
    input: {},
    expected: strict({ valid: { type: "boolean" } }),
    informational: strict({ reason: { type: "string" } }),
  },
  "patterns-overlap": {
    contract: ["room"],
    description: "Decide whether two claim patterns overlap.",
    input: strict({ a: { type: "string" }, b: { type: "string" } }),
    expected: strict({ overlap: { type: "boolean" } }),
  },
  "path-sets-overlap": {
    contract: ["room"],
    description: "Decide whether any pattern of one claim overlaps any pattern of another claim.",
    input: strict({ a: { type: "array", items: { type: "string" } }, b: { type: "array", items: { type: "string" } } }),
    expected: strict({ overlap: { type: "boolean" } }),
  },
  canonicalize: {
    contract: ["canonical-json"],
    description: "Parse the JSON text in `input.json` and write the value as canonical JSON.",
    input: strict({ json: { type: "string" } }),
    expected: strict({ canonical: { type: "string" }, sha256: hex64 }),
  },
  "verify-chain": {
    contract: ["audit"],
    description: "Verify the records in `input.records`, in the order given, from the genesis hash.",
    input: strict({ records: { type: "array", items: { $ref: schemaId("audit", "record") } } }),
    expected: { $ref: schemaId("audit", "verify-result") },
    informational: strict({ hash_inputs: { type: "array", items: { type: "string" } } }),
  },
  "verify-signed-bundle": {
    contract: ["policy"],
    description: "Verify the compact JWS in `input.signature` against the key set in `shared`.",
    shared: strict({ jwks: { $ref: schemaId("policy", "jwks") } }),
    input: strict({ signature: { type: "string" } }),
    expected: {
      oneOf: [strict({ ok: { const: true }, bundle: { $ref: schemaId("policy", "bundle") } }), strict({ ok: { const: false } })],
    },
    informational: strict({ reason: { type: "string" } }),
  },
};

function rule(name: string, op: Operation): Schema {
  const caseProperties: Schema = { input: op.input, expected: op.expected, informational: op.informational ?? false };
  return {
    if: { properties: { operation: { const: name } }, required: ["operation"] },
    then: {
      description: op.description,
      properties: {
        contract: { enum: op.contract },
        shared: op.shared ?? false,
        schema: op.schemaUnderTest ? { type: "string" } : false,
        cases: { items: { properties: caseProperties } },
      },
      required: [...(op.shared ? ["shared"] : []), ...(op.schemaUnderTest ? ["schema"] : [])],
    },
  };
}

/** The schema of a fixture file. One `if` and `then` pair per operation fixes the shape of `shared`, `input`, and `expected`. */
export function fixtureFileSchema(): Json {
  const schema: Json = {
    $schema: "https://json-schema.org/draft/2020-12/schema",
    $id: FIXTURE_FILE_ID,
    title: "Fixture file",
    description: "A list of cases for one operation of one contract. Each case pairs an input with the result that the reference gives.",
    type: "object",
    properties: {
      contract: { enum: ["policy", "room", "audit", "canonical-json"], description: "The contract directory that the file belongs to." },
      version: { const: 1, description: "The version of the contract." },
      operation: { enum: Object.keys(OPERATIONS), description: "What an implementation does with each case." },
      description: { type: "string", minLength: 1 },
      reference: {
        ...strict({
          commit: { type: "string", pattern: "^[0-9a-f]{8}$", description: "The commit of the existing TypeScript implementation that produced the expected values." },
          sources: { type: "array", items: { type: "string" }, minItems: 1, description: "The files of that implementation that the cases exercise, relative to its root." },
        }),
      },
      schema: { type: "string", description: "For `validate`: the `$id` of the schema under test." },
      shared: { type: "object", description: "Input that every case of the file shares." },
      cases: { type: "array", minItems: 1, items: { $ref: "#/$defs/case" } },
    },
    required: ["contract", "version", "operation", "description", "reference", "cases"],
    additionalProperties: false,
    allOf: Object.entries(OPERATIONS).map(([name, op]) => rule(name, op)),
    $defs: {
      case: {
        type: "object",
        properties: {
          name: { type: "string", minLength: 1, description: "Unique in the file." },
          why: { type: "string", minLength: 1, description: "The behavior that the case pins." },
          input: { description: "What the implementation receives." },
          expected: { description: "What the implementation must return. Compare the whole value." },
          informational: { type: "object", description: "Output of the reference that an implementation need not reproduce, such as the wording of a message." },
        },
        required: ["name", "why", "input", "expected"],
        additionalProperties: false,
      },
    },
  };
  assertPortablePatterns(FIXTURE_FILE_PATH, schema);
  return schema;
}

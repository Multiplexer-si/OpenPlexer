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
  contract: "policy" | "room" | "audit" | "canonical-json" | "adapter";
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
const seq: Schema = { type: "integer", minimum: 0 };
const overlap = {
  expected: { oneOf: [strict({ overlap: { type: "boolean" } }), strict({ throws: { const: true } })] },
  informational: strict({ error: { type: "string" } }),
};
const OVERLAP_THROWS = "An `expected` of `throws` means that the reference returns no result, and an implementation must report an overlap.";

const OPERATIONS: Record<string, Operation> = {
  evaluate: {
    contract: ["policy"],
    description: "Evaluate the query in `input` against the bundle in `shared`. An `expected` of `throws` means that the reference returns no result. The contract does not define one, and an implementation must deny with no `area` or fail.",
    shared: strict({ bundle: { $ref: schemaId("policy", "bundle") } }),
    input: { $ref: schemaId("policy", "eval-query") },
    expected: {
      oneOf: [strict({ decision: { enum: ["allow", "deny"] }, area: { type: "string" } }, ["decision"]), strict({ throws: { const: true } })],
    },
    informational: { oneOf: [strict({ rule: { type: "string" } }), strict({ error: { type: "string" } })] },
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
    description: `Decide whether two claim patterns overlap. ${OVERLAP_THROWS}`,
    input: strict({ a: { type: "string" }, b: { type: "string" } }),
    ...overlap,
  },
  "path-sets-overlap": {
    contract: ["room"],
    description: `Decide whether any pattern of one claim overlaps any pattern of another claim. ${OVERLAP_THROWS}`,
    input: strict({ a: { type: "array", items: { type: "string" } }, b: { type: "array", items: { type: "string" } } }),
    ...overlap,
  },
  deliver: {
    contract: ["room"],
    description: "Run one subscription of the hub to one Room, from `input.since`, through `input.steps`, and list the `seq` of each event that it sends.",
    input: strict({
      since: seq,
      steps: {
        type: "array",
        items: { oneOf: [strict({ step: { enum: ["replay", "live"] }, seq }), strict({ step: { const: "go-live" } })] },
      },
    }),
    expected: strict({ sent: { type: "array", items: seq }, last_seq: seq }),
  },
  "active-claims": {
    contract: ["room"],
    description: "Apply the events in `input.events`, in the order given, to the claims of one Room, and list the claims that are active at `input.now`. The receiver gets every event at `input.now`.",
    input: strict({ events: { type: "array", items: { $ref: schemaId("room", "room-event") } }, now: { type: "string" } }),
    expected: strict({
      claims: {
        type: "array",
        items: strict({
          claim_id: { type: "string" },
          paths: { type: "array", items: { type: "string" } },
          intent: { type: "string" },
          expires_at: { type: "string" },
          agent_session_id: { type: "string" },
          agent_vendor: { type: "string" },
        }),
      },
    }),
  },
  "close-code": {
    contract: ["room"],
    description: "Give the WebSocket close code that the hub sends after an error with the code in `input.code`, or null if the hub keeps the connection.",
    input: strict({ code: { type: "string" } }),
    expected: strict({ close_code: { oneOf: [{ type: "integer" }, { type: "null" }] } }),
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
  "verify-checkpoints": {
    contract: ["audit"],
    description: "Verify the export in `input.export` using `input.checkpoints` and the checkpoint key set in `shared.jwks`. A case can override that key set with the JSON text in `input.jwks_json`. The export and JWKS text can be malformed on purpose.",
    shared: strict({ jwks: { $ref: schemaId("policy", "jwks") } }),
    input: strict({
      export: {},
      checkpoints: { type: "array", items: { type: "string" } },
      jwks_json: { type: "string" },
    }, ["export", "checkpoints"]),
    expected: { $ref: schemaId("audit", "checkpoint-verify-result") },
  },
  "bash-targets": {
    contract: ["adapter"],
    description: "List the paths that the shell command in `input.command` writes, in the order that the command names them.",
    input: strict({ command: { type: "string" } }),
    expected: strict({ targets: { type: "array", items: { type: "string" } } }),
  },
  "exec-segments": {
    contract: ["adapter"],
    description: "Split the shell command in `input.command` into its commands, and list the words of each.",
    input: strict({ command: { type: "string" } }),
    expected: strict({ segments: { type: "array", items: { type: "array", items: { type: "string" } } } }),
  },
  "patch-targets": {
    contract: ["adapter"],
    description: "List the paths that the Codex patch in `input.patch` adds, updates, deletes, or moves, or null if Codex would not accept the patch.",
    input: strict({ patch: { type: "string" } }),
    expected: strict({ targets: { oneOf: [{ type: "array", items: { type: "string" } }, { type: "null" }] } }),
  },
  "pre-tool": {
    contract: ["adapter"],
    description: "Run the PreToolUse hook of the agent that `shared.agent` names on the hook input in `input.hook`, for the member in `input.member`, against the bundle in `shared`.",
    shared: strict({
      agent: { enum: ["claude-code", "codex"] },
      bundle: { $ref: schemaId("policy", "bundle") },
      repo: { type: "string" },
      now: { type: "string" },
    }),
    input: strict({ member: { type: "string" }, hook: { type: "object" } }),
    expected: strict({ decision: { enum: ["allow", "deny"] } }),
    informational: strict({ output: { type: "string" } }),
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
    description: "A list of cases for one operation of one contract. Each case pairs an input with its expected result.",
    type: "object",
    properties: {
      contract: { enum: ["policy", "room", "audit", "canonical-json", "adapter"], description: "The contract directory that the file belongs to." },
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
    required: ["contract", "version", "operation", "description", "cases"],
    additionalProperties: false,
    allOf: [
      ...Object.entries(OPERATIONS).map(([name, op]) => rule(name, op)),
      {
        if: { properties: { operation: { const: "verify-checkpoints" } } },
        then: { properties: { reference: false } },
        else: { required: ["reference"] },
      },
    ],
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

import { buildChain, verifyChain, type AuditFields } from "./audit.ts";
import { asCommitted, render, type Files, type Json, type SealedFiles } from "./output.ts";
import type { Reference, ZodSchema } from "./reference.ts";
import { messagePayload, ROOM_EVENT_TYPES } from "./schemas.ts";
import { decodeJws, newSigner } from "./signing.ts";

const NOW = "2026-01-15T12:00:00.000Z";

export const examplePath = (contract: string, schema: string, name: string): string => `spec/${contract}/v1/examples/${schema}/${name}.json`;

/** Fails the run if the reference rejects a value that the repository publishes as a valid example. */
function accepted(zod: ZodSchema, path: string, value: Json): Json {
  const result = zod.safeParse(asCommitted(value));
  if (!result.success) {
    throw new Error(`${path}: the reference rejects this example: ${result.error.issues.map((issue) => `${issue.path.join(".")}: ${issue.message}`).join("; ")}`);
  }
  return value;
}

export const exampleBundle: { version: number; [key: string]: Json } = {
  version: 3,
  project_id: "prj-shop",
  repos: ["api", "web"],
  areas: [
    { name: "backend", patterns: ["api:src/**"] },
    { name: "billing", patterns: ["api:src/billing/**"] },
    { name: "frontend", patterns: ["web:src/**"] },
    { name: "secrets", patterns: ["*:**/.env*"], private: true },
    { name: "lockfiles", patterns: ["*:**/package-lock.json"], generated: true },
    { name: "ci", patterns: ["*:.github/**"], system: true },
  ],
  roles: {
    backend: { read: [], write: ["backend"], exec: ["npm test"], push: ["api"] },
    lead: { read: ["secrets"], write: ["backend", "billing", "frontend", "secrets"], exec: ["npm test", "npm run"], push: ["*"] },
  },
  members: { "mem-ana": "backend", "mem-lee": "lead" },
  temporary_grants: [
    { member_id: "mem-ana", area: "billing", actions: ["write"], scope: "mission", mission_id: "mis-checkout" },
    { member_id: "mem-ana", area: "secrets", actions: ["read"], scope: "until", expires_at: "2026-01-16T09:00:00+01:00" },
  ],
};

const header = (reference: Reference, kid: string) => ({ alg: "ES256", kid, typ: reference.policyJwsTyp });

function policyExamples(reference: Reference): [string, Json][] {
  const { protocol } = reference;
  const bundles: [string, Json][] = [
    ["minimal", { version: 1, project_id: "prj-shop", repos: [], areas: [], roles: {}, members: {} }],
    ["full", exampleBundle],
  ];
  const overrides: [string, Json][] = [
    ["once", { id: "ovr-1", area: "billing", actions: ["write"], scope: "once", agent_session_id: "ses-ana-1", expires_at: "2026-01-15T13:00:00.000Z" }],
    ["mission", { id: "ovr-2", area: "billing", actions: ["read", "write"], scope: "mission", mission_id: "mis-checkout" }],
    ["until", { id: "ovr-3", area: "secrets", actions: ["read"], scope: "until", expires_at: "2026-01-15T18:00:00.000Z" }],
  ];
  const session = { member: "mem-ana", now: NOW, agent_session_id: "ses-ana-1", mission_id: "mis-checkout" };
  const queries: [string, Json][] = [
    ["write-allowed", { ...session, repo: "api", path: "src/orders/cart.ts", action: "write" }],
    ["write-denied", { ...session, repo: "web", path: "src/pages/cart.tsx", action: "write" }],
    ["write-with-override", { ...session, repo: "web", path: "src/pages/cart.tsx", action: "write", overrides: [{ id: "ovr-4", area: "frontend", actions: ["write"], scope: "mission", mission_id: "mis-checkout" }] }],
    ["exec", { ...session, repo: "api", path: "", command: "npm test -- --watch", action: "exec" }],
    ["sub-agent", { ...session, repo: "api", path: "src/billing/invoice.ts", action: "write", agent_session_id: "ses-ana-1-sub", mission_id: "mis-other", parent: { agent_session_id: "ses-ana-1", mission_id: "mis-checkout" } }],
  ];
  const results = queries.map(([name, query]): [string, Json] => {
    const committed = asCommitted(query) as { now: string };
    return [name, protocol.evaluate(asCommitted(exampleBundle), { ...committed, now: new Date(committed.now) })];
  });
  return [
    ...bundles.map(([name, value]): [string, Json] => [examplePath("policy", "bundle", name), accepted(protocol.policyBundleSchema, name, value)]),
    ...overrides.map(([name, value]): [string, Json] => [examplePath("policy", "override", name), accepted(protocol.overrideSchema, name, value)]),
    ...queries.map(([name, value]): [string, Json] => [examplePath("policy", "eval-query", name), value]),
    ...results.map(([name, value]): [string, Json] => [examplePath("policy", "eval-result", name), value]),
    [examplePath("policy", "jws-protected-header", "policy-bundle"), header(reference, "hk_example")],
  ];
}

const roomBodies: Record<(typeof ROOM_EVENT_TYPES)[number], Json> = {
  status: { summary: "Adding the discount field to the cart total", files: ["src/orders/cart.ts"], plan_step: "2 of 4" },
  claim: { item: "Cart discount calculation", paths: ["src/orders/cart.ts", "src/orders/discount/**"], intent: "Add percentage discounts to the cart total", ttl_seconds: 1800 },
  release: { claim_id: "evt-0002", outcome: "merged" },
  decision: { title: "Money is an integer number of cents", detail: "Every amount in the API is an integer number of cents. No endpoint returns a decimal amount.", scope: "api" },
  "decision.confirm": { decision_id: "evt-0004", result: "confirmed" },
  question: { text: "Does the cart endpoint return the discount as a separate field?", addressed_to: "mem-lee", ack_required: true },
  answer: { question_id: "evt-0006", text: "Yes. The field is discount_cents.", thread_id: "evt-0006", reply_to: "evt-0006" },
  blocked: { reason: "The billing area is not writable by my role", needs: { decision: "evt-0004" } },
  human: { text: "Ship the discount work before the pricing page.", mentions: ["mem-ana"] },
  "policy.denied": { action: "write", path: "src/billing/invoice.ts", rule: 'area "billing" is writable by role lead only' },
  "policy.override": { lead: "mem-lee", agent: "ses-ana-1", areas: ["billing"], actions: ["write"], scope: "mission", reason: "The discount work needs one change to the invoice total" },
  "policy.override.revoked": { override_id: "ovr-2", agent: "ses-ana-1", area: "billing", revoked_by: "mem-lee", reason: "The discount work needs one change to the invoice total" },
};

/** The event types that only a person causes. The hub of the reference rejects them from an agent session. */
const PERSON_EVENT_TYPES = new Set(["human", "decision.confirm", "policy.override", "policy.override.revoked"]);

export function exampleEvent(type: string, body: Json, seq: number): { [key: string]: Json } {
  const person = PERSON_EVENT_TYPES.has(type);
  return {
    id: `evt-${String(seq).padStart(4, "0")}`,
    seq,
    mission: "mis-checkout",
    type,
    agent: person ? { id: "mem-lee", vendor: "human" } : { id: "ses-ana-1", vendor: "claude-code" },
    on_behalf_of: person ? "mem-lee" : "mem-ana",
    at: NOW,
    body,
  };
}

function roomExamples(reference: Reference): [string, Json][] {
  const { protocol } = reference;
  const events = ROOM_EVENT_TYPES.map((type, i): [string, Json] => [type, exampleEvent(type, roomBodies[type], i + 1)]);
  events.push(["status-from-sub-agent", { ...exampleEvent("status", { summary: "Running the cart tests" }, 13), agent: { id: "ses-ana-1-sub", vendor: "claude-code", parent: "ses-ana-1" } }]);
  const envelopes: [string, Json][] = [
    ["event", { v: protocol.PROTOCOL_VERSION, type: "event", room: "mis-checkout", seq: 2, event_id: "evt-0002", sender: { id: "ses-ana-1", kind: "daemon" }, ts: NOW, payload: exampleEvent("claim", roomBodies.claim, 2) }],
    ["connection-level", { v: protocol.PROTOCOL_VERSION, type: "resume", sender: { id: "dmn-ana", kind: "daemon" }, payload: { last_seq: 41 } }],
  ];
  return [
    ...events.map(([name, value]): [string, Json] => [examplePath("room", "room-event", name), accepted(protocol.roomEventSchema, name, value)]),
    ...envelopes.map(([name, value]): [string, Json] => [examplePath("room", "envelope", name), accepted(protocol.wsMessageSchema, name, value)]),
    [examplePath("room", "resume", "after-41"), accepted(messagePayload(reference, "resume"), "resume", { last_seq: 41 })],
    [examplePath("room", "error", "behind"), accepted(messagePayload(reference, "error"), "behind", { code: "behind", message: "outbound buffer too large; resume to catch up" })],
    [examplePath("room", "error", "answer-to-a-post"), accepted(messagePayload(reference, "error"), "answer-to-a-post", { code: "rate_limit", message: "too many events", client_event_id: "cev-17" })],
  ];
}

const exampleEntries: AuditFields[] = [
  { workspace_id: "wsp-shop", actor_member_id: "mem-lee", agent_session_id: null, action: "policy.updated", target: "prj-shop", reason: null, meta: { version: 3 }, flagged: false, created_at: "2026-01-15T11:58:00.000Z" },
  { workspace_id: "wsp-shop", actor_member_id: "mem-ana", agent_session_id: "ses-ana-1", action: "event.policy-denied", target: "src/billing/invoice.ts", reason: 'area "billing" is writable by role lead only', meta: { action: "write", event_id: "evt-0010" }, flagged: false, created_at: "2026-01-15T12:00:00.000Z" },
  { workspace_id: "wsp-shop", actor_member_id: "mem-lee", agent_session_id: "ses-ana-1", action: "override.granted", target: "billing", reason: "The discount work needs one change to the invoice total", meta: { actions: ["write"], override_id: "ovr-2", scope: "mission" }, flagged: false, created_at: "2026-01-15T12:03:30.250Z" },
];

function auditExamples(reference: Reference): [string, Json][] {
  const { protocol } = reference;
  const record = protocol.auditEntrySchema.omit({ result: true, agent_member_id: true });
  const chain = buildChain(reference, exampleEntries);
  const entries = chain.toReversed().map((r) => ({ ...r, result: r.action.replace(/^.*[.-]/, ""), agent_member_id: r.agent_session_id === null ? null : "mem-ana" }));
  const broken = chain.map((r) => (r.id === "2" ? { ...r, target: "src/orders/cart.ts" } : r));
  const results: [string, Json][] = [
    ["valid", verifyChain(reference, chain)],
    ["broken", verifyChain(reference, broken)],
  ];
  return [
    ...chain.map((r, i): [string, Json] => [examplePath("audit", "record", ["first", "second", "third"][i]!), accepted(record, r.id, r as unknown as Json)]),
    [examplePath("audit", "export", "three-records"), accepted(protocol.auditPageSchema, "export", { entries, next_cursor: null } as unknown as Json)],
    ...results.map(([name, value]): [string, Json] => [examplePath("audit", "verify-result", name), accepted(protocol.auditVerificationSchema, name, value)]),
  ];
}

export function examples(reference: Reference): Map<string, Json> {
  return new Map([...policyExamples(reference), ...roomExamples(reference), ...auditExamples(reference)]);
}

/** The signed example: one bundle message and the key set that verifies it. */
export function signedExample(reference: Reference): SealedFiles {
  const message = examplePath("policy", "signed-bundle", "message");
  const keys = examplePath("policy", "jwks", "hub-keys");
  const kid = "hk_example";
  const payload = reference.canonicalJson(asCommitted(exampleBundle));
  return {
    paths: [message, keys],
    async create(): Promise<Files> {
      const signer = await newSigner(reference, kid);
      const signature = await signer.sign(header(reference, kid), payload);
      return new Map([
        [message, render({ version: exampleBundle.version, bundle: exampleBundle, signature })],
        [keys, render({ keys: [signer.publicJwk] })],
      ]);
    },
    async verify(committed: Files): Promise<string[]> {
      const problems: string[] = [];
      const parsed = JSON.parse(committed.get(message)!) as { version: Json; bundle: Json; signature: string };
      const jwks = JSON.parse(committed.get(keys)!) as Json;
      const decoded = decodeJws(parsed.signature);
      if (JSON.stringify(decoded?.header) !== JSON.stringify(header(reference, kid))) problems.push(`${message}: the protected header is not ${JSON.stringify(header(reference, kid))}`);
      if (decoded?.payload !== payload) problems.push(`${message}: the signed payload is not the canonical JSON of the example bundle`);
      if (render(parsed.bundle) !== render(exampleBundle) || parsed.version !== exampleBundle.version) problems.push(`${message}: bundle and version are not those of the example bundle`);
      const verified = await reference.verifyBundleJws(parsed.signature, jwks);
      if (!verified.ok) problems.push(`${message}: the reference does not verify the signature: ${verified.reason}`);
      for (const path of [message, keys]) {
        if (render(JSON.parse(committed.get(path)!) as Json) !== committed.get(path)) problems.push(`${path}: the formatting differs from the generator output`);
      }
      return problems;
    },
  };
}

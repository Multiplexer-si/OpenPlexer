import { exampleEvent } from "../examples.ts";
import { asCommitted, type Json } from "../output.ts";
import type { Reference, ZodSchema } from "../reference.ts";
import { messagePayload, schemaId } from "../schemas.ts";
import { fixtureFile, fixturePath, type Case, type FixtureFile } from "./format.ts";

type Authored = [name: string, why: string, input: Json];
type Fields = { [key: string]: Json };

interface Target {
  contract: "policy" | "room";
  /** The name of the schema under test, which is also the name of the fixture file after `validate-`. */
  schema: string;
  description: string;
  /** The schema of the reference that gives the verdict of a case. */
  zod(input: Json): ZodSchema;
  sources: string[];
  cases: Authored[];
}

function file({ contract, schema, description, zod, sources, cases }: Target): [string, FixtureFile] {
  const built = cases.map(([name, why, authored]): Case => {
    const input = asCommitted(authored);
    const result = zod(input).safeParse(input);
    if (result.success) return { name, why, input, expected: { valid: true } };
    const reason = result.error.issues.map((issue) => `${issue.path.join(".") || "(root)"}: ${issue.message}`).join("; ");
    return { name, why, input, expected: { valid: false }, informational: { reason } };
  });
  return [
    fixturePath(contract, `validate-${schema}`),
    fixtureFile({ contract, operation: "validate", description, sources, schema: schemaId(contract, schema), cases: built }),
  ];
}

const without = (value: Fields, key: string): Fields => Object.fromEntries(Object.entries(value).filter(([k]) => k !== key));

/** White space of the contracts that `\s` of a regular expression engine other than ECMA-262 can miss. */
const WHITE_SPACE_ONLY: [code: string, name: string][] = [
  ["U+00A0", "a no-break space"],
  ["U+000B", "a line tabulation"],
  ["U+FEFF", "a zero width no-break space"],
  ["U+3000", "an ideographic space"],
  ["U+2028", "a line separator"],
];

/** Code points that other definitions of white space include and the contracts do not. */
const NOT_WHITE_SPACE: [code: string, name: string][] = [
  ["U+0085", "a next line character"],
  ["U+180E", "a Mongolian vowel separator"],
  ["U+200B", "a zero width space"],
];

function roomEventCases(): Authored[] {
  const event = (type: string, body: Json): Fields => exampleEvent(type, body, 7);
  const status = event("status", { summary: "Running the cart tests" });
  const claim = { item: "Cart discount", paths: ["src/orders/**"], intent: "Add discounts", ttl_seconds: 600 };
  const override = { lead: "mem-lee", agent: "ses-ana-1", areas: ["billing"], actions: ["write"], scope: "mission", reason: "One change to the invoice total" };
  const revoked = { override_id: "ovr-2", agent: "ses-ana-1", area: "billing", revoked_by: "mem-lee" };
  return [
    ["status: a summary alone is valid", "`summary` is the only required field of a status body.", status],
    ["status: a missing summary is invalid", "`summary` is required.", event("status", { files: ["src/orders/cart.ts"] })],
    ["status: files that is not a list is invalid", "`files` is a list of strings.", event("status", { summary: "Running the cart tests", files: "src/orders/cart.ts" })],
    ["claim: all four fields is valid", "A claim body needs `item`, `paths`, `intent`, and `ttl_seconds`.", event("claim", claim)],
    ["claim: an empty list of paths is valid", "The schema sets no minimum length for `paths`.", event("claim", { ...claim, paths: [] })],
    ["claim: a ttl of zero is invalid", "`ttl_seconds` is greater than zero.", event("claim", { ...claim, ttl_seconds: 0 })],
    ["claim: a ttl with a fraction is invalid", "`ttl_seconds` is an integer.", event("claim", { ...claim, ttl_seconds: 1.5 })],
    ["claim: missing paths is invalid", "`paths` is required.", event("claim", without(claim, "paths"))],
    ["release: a claim id alone is valid", "`claim_id` is the only required field of a release body.", event("release", { claim_id: "evt-0002" })],
    ["release: a claim id that is not a string is invalid", "`claim_id` is a string.", event("release", { claim_id: 2 })],
    ["decision: a title and a detail is valid", "A decision body needs `title` and `detail`.", event("decision", { title: "Money is cents", detail: "Every amount is an integer number of cents." })],
    ["decision: a missing detail is invalid", "`detail` is required.", event("decision", { title: "Money is cents" })],
    ["decision.confirm: confirmed is valid", "`result` is `confirmed` or `rejected`.", event("decision.confirm", { decision_id: "evt-0004", result: "confirmed" })],
    ["decision.confirm: rejected is valid", "`result` is `confirmed` or `rejected`.", event("decision.confirm", { decision_id: "evt-0004", result: "rejected" })],
    ["decision.confirm: another result is invalid", "`result` is a closed list.", event("decision.confirm", { decision_id: "evt-0004", result: "maybe" })],
    ["question: a text alone is valid", "`text` is the only required field of a question body.", event("question", { text: "Is the discount a separate field?" })],
    ["question: a text of only white space is invalid", "`text` needs one character that is not white space.", event("question", { text: "   " })],
    ...WHITE_SPACE_ONLY.map(([code, name]): Authored => [`question: a text of only ${name} is invalid`, `${code} is white space, so the text has no character that is not white space.`, event("question", { text: String.fromCodePoint(Number.parseInt(code.slice(2), 16)) })]),
    ...NOT_WHITE_SPACE.map(([code, name]): Authored => [`question: a text of only ${name} is valid`, `${code} is not white space in this contract.`, event("question", { text: String.fromCodePoint(Number.parseInt(code.slice(2), 16)) })]),
    ["question: an empty addressed_to is invalid", "`addressed_to`, when present, is not empty.", event("question", { text: "Is the discount a separate field?", addressed_to: "" })],
    ["question: an addressed_to of only white space is invalid", "`addressed_to` needs one character that is not white space.", event("question", { text: "Is the discount a separate field?", addressed_to: " " })],
    ["answer: a question id and a text is valid", "An answer body needs `question_id` and `text`.", event("answer", { question_id: "evt-0006", text: "Yes." })],
    ["answer: an empty question id is invalid", "`question_id` is not empty.", event("answer", { question_id: "", text: "Yes." })],
    ["answer: a text of only white space is invalid", "`text` needs one character that is not white space.", event("answer", { question_id: "evt-0006", text: "\t\n" })],
    ["blocked: a reason alone is valid", "`reason` is the only required field of a blocked body.", event("blocked", { reason: "The billing area is not writable" })],
    ["blocked: needs with a decision is valid", "`needs` is an object with optional `request_id` and `decision`.", event("blocked", { reason: "Waiting", needs: { decision: "evt-0004" } })],
    ["blocked: needs that is not an object is invalid", "`needs` is an object.", event("blocked", { reason: "Waiting", needs: "evt-0004" })],
    ["human: a text alone is valid", "`text` is the only required field of a human body.", event("human", { text: "Ship the discount work first." })],
    ["human: mentions that are not strings is invalid", "`mentions` is a list of strings.", event("human", { text: "Ship the discount work first.", mentions: [1] })],
    ["policy.denied: action, path, and rule is valid", "A policy.denied body needs `action`, `path`, and `rule`.", event("policy.denied", { action: "write", path: "src/billing/invoice.ts", rule: "denied" })],
    ["policy.denied: any string is a valid action", "`action` is an open string in the event, not the closed list of the policy contract.", event("policy.denied", { action: "delete", path: "src/billing/invoice.ts", rule: "denied" })],
    ["policy.denied: a missing rule is invalid", "`rule` is required.", event("policy.denied", { action: "write", path: "src/billing/invoice.ts" })],
    ["policy.override: all six fields is valid", "A policy.override body needs `lead`, `agent`, `areas`, `actions`, `scope`, and `reason`.", event("policy.override", override)],
    ["policy.override: areas that is not a list is invalid", "`areas` is a list of strings.", event("policy.override", { ...override, areas: "billing" })],
    ["policy.override: a missing reason is invalid", "`reason` is required.", event("policy.override", without(override, "reason"))],
    ["policy.override.revoked: without a reason is valid", "`reason` is optional in a revocation.", event("policy.override.revoked", revoked)],
    ["policy.override.revoked: a missing revoked_by is invalid", "`revoked_by` is required.", event("policy.override.revoked", without(revoked, "revoked_by"))],
    ["an unknown event type is invalid", "An event whose type is not in the list is rejected, whatever its body.", event("made.up", { summary: "Running the cart tests" })],
    ["a body with an unknown field is valid", "A body keeps fields that the schema does not name.", event("status", { summary: "Running the cart tests", mood: "calm" })],
    ["an event with an unknown top-level field is valid", "An event keeps fields that the schema does not name.", { ...status, trace_id: "trc-1" }],
    ["thread_id and reply_to are valid on any body", "Every body accepts the two threading fields.", event("status", { summary: "Running the cart tests", thread_id: "evt-0001", reply_to: "evt-0003" })],
    ["a thread_id that is not a string is invalid", "`thread_id` is a string.", event("status", { summary: "Running the cart tests", thread_id: 1 })],
    ["an agent with a parent is valid", "`agent.parent` marks the event of a sub-agent.", { ...status, agent: { id: "ses-ana-1-sub", vendor: "claude-code", parent: "ses-ana-1" } }],
    ["an agent with an empty parent is invalid", "`agent.parent`, when present, is not empty.", { ...status, agent: { id: "ses-ana-1-sub", vendor: "claude-code", parent: "" } }],
    ["an agent with an unknown field is valid", "`agent` accepts fields that the schema does not name.", { ...status, agent: { id: "ses-ana-1", vendor: "claude-code", model: "x" } }],
    ["a missing at is invalid", "`at` is required.", without(status, "at")],
    ["an at that is not a timestamp is valid", "`at` is any string that is not empty. The schema does not check that it is a time.", { ...status, at: "yesterday" }],
    ["a missing on_behalf_of is invalid", "`on_behalf_of` is required.", without(status, "on_behalf_of")],
    ["an empty id is invalid", "`id` is not empty.", { ...status, id: "" }],
    ["a seq of zero is valid", "The schema accepts zero. The hub of the reference gives the first event of a Room the `seq` 1.", { ...status, seq: 0 }],
    ["a negative seq is invalid", "`seq` is not negative.", { ...status, seq: -1 }],
    ["a seq with a fraction is invalid", "`seq` is an integer.", { ...status, seq: 1.5 }],
    ["a seq that is a string is invalid", "`seq` is a number.", { ...status, seq: "7" }],
    ["a body that does not match its type is invalid", "The body is validated against the schema of the event type.", event("claim", { summary: "Running the cart tests" })],
    ["a missing body is invalid", "`body` is required.", without(status, "body")],
    ["a null body is invalid", "Every body is an object.", { ...status, body: null }],
  ];
}

function envelopeCases(reference: Reference): Authored[] {
  const v = reference.protocol.PROTOCOL_VERSION;
  const event = { v, type: "event", room: "mis-checkout", seq: 7, event_id: "evt-0007", sender: { id: "ses-ana-1", kind: "daemon" }, ts: "2026-01-15T12:00:00.000Z", payload: exampleEvent("status", { summary: "Running the cart tests" }, 7) };
  const bare = { v, type: "resume", sender: { id: "dmn-ana", kind: "daemon" }, payload: { last_seq: 6 } };
  return [
    ["a message that carries an event is valid", "The hub sets `room`, `seq`, `event_id`, and `ts` on a message that carries an event.", event],
    ["a message about the connection is valid without room, seq, event_id, and ts", "Only `v`, `type`, `sender`, and `payload` are required.", bare],
    ["a null payload is valid", "`payload` must be present, and any JSON value is accepted.", { ...bare, payload: null }],
    ["a missing payload is invalid", "`payload` is required.", without(bare, "payload")],
    ["a missing v is invalid", "`v` is required.", without(bare, "v")],
    ["a v of zero is invalid", "`v` is greater than zero.", { ...bare, v: 0 }],
    ["a v of 2 is valid", "The envelope schema does not fix `v` to one version. Version negotiation happens in the `hello` message.", { ...bare, v: 2 }],
    ["an empty type is invalid", "`type` is not empty.", { ...bare, type: "" }],
    ["a type that no message defines is valid", "The envelope schema accepts any `type` that is not empty, because version 1 defines the payload of `event` only. The hub and the daemon of the reference parse with a message schema that rejects a type that it has no payload for.", { ...bare, type: "made.up" }],
    ["an event message with an empty payload is invalid", "The payload of a message with the type `event` is a stored Room event.", { ...event, payload: {} }],
    ["an event message with a null payload is invalid", "The payload of a message with the type `event` is a stored Room event.", { ...event, payload: null }],
    ["an event message whose event has an unknown type is invalid", "The payload is validated as a stored Room event, so its `type` must be in the list.", { ...event, payload: exampleEvent("made.up", { summary: "Running the cart tests" }, 7) }],
    ["an event message whose body does not match the event type is invalid", "The payload is validated as a stored Room event, body included.", { ...event, payload: exampleEvent("claim", { summary: "Running the cart tests" }, 7) }],
    ["an event message without room, seq, event_id, and ts is valid", "The schema does not require the fields that the hub sets on an event message.", { ...bare, type: "event", payload: event.payload }],
    ["a sender kind outside the list is invalid", "`sender.kind` is `daemon`, `dashboard`, or `server`.", { ...bare, sender: { id: "x", kind: "robot" } }],
    ["a sender with an empty id is invalid", "`sender.id` is not empty.", { ...bare, sender: { id: "", kind: "daemon" } }],
    ["a missing sender is invalid", "`sender` is required.", without(bare, "sender")],
    ["a negative seq is invalid", "`seq`, when present, is not negative.", { ...event, seq: -1 }],
    ["an empty room is invalid", "`room`, when present, is not empty.", { ...event, room: "" }],
    ["an unknown top-level field is valid", "The envelope keeps fields that the schema does not name.", { ...bare, trace_id: "trc-1" }],
  ];
}

function resumeCases(): Authored[] {
  return [
    ["a last_seq of zero is valid", "A receiver that has no event of the Room resumes after 0.", { last_seq: 0 }],
    ["a positive last_seq is valid", "`last_seq` is the highest `seq` that the receiver has.", { last_seq: 41 }],
    ["a missing last_seq is invalid", "`last_seq` is required.", {}],
    ["a negative last_seq is invalid", "`last_seq` is not negative.", { last_seq: -1 }],
    ["a last_seq with a fraction is invalid", "`last_seq` is an integer.", { last_seq: 1.5 }],
    ["a last_seq that is a string is invalid", "`last_seq` is a number.", { last_seq: "41" }],
    ["a last_seq above the largest safe integer is invalid", "`last_seq` is at most 9007199254740991.", { last_seq: 9007199254740992 }],
    ["an unknown field is valid", "The payload keeps fields that the schema does not name.", { last_seq: 41, reason: "reconnected" }],
    ["a payload that is not an object is invalid", "The payload is an object.", 41],
  ];
}

function errorCases(): Authored[] {
  const behind = { code: "behind", message: "outbound buffer too large; resume to catch up" };
  return [
    ["a code and a message is valid", "`code` and `message` are the required fields.", behind],
    ["a client_event_id is valid", "An error that answers a post names the post.", { code: "rate_limit", message: "too many events", client_event_id: "cev-17" }],
    ["a code that version 1 does not list is valid", "`code` is an open string, so that a newer hub can send a new code. The README says how a receiver treats it.", { code: "made_up", message: "x" }],
    ["an empty code is valid", "The schema sets no minimum length for `code`.", { code: "", message: "" }],
    ["a missing code is invalid", "`code` is required.", { message: "x" }],
    ["a missing message is invalid", "`message` is required.", { code: "behind" }],
    ["a code that is a number is invalid", "`code` is a string.", { code: 4401, message: "x" }],
    ["a client_event_id that is a number is invalid", "`client_event_id` is a string.", { ...behind, client_event_id: 17 }],
    ["an unknown field is valid", "The payload keeps fields that the schema does not name.", { ...behind, retry_after: 5 }],
    ["a null payload is invalid", "The payload is an object.", null],
  ];
}

function bundleCases(): Authored[] {
  const minimal = { version: 1, project_id: "prj-shop", repos: [], areas: [], roles: {}, members: {} };
  const mission = { member_id: "mem-ana", area: "billing", actions: ["write"], scope: "mission", mission_id: "mis-1" };
  const until = { member_id: "mem-ana", area: "billing", actions: ["read", "write"], scope: "until", expires_at: "2026-01-16T09:00:00Z" };
  const grants = (...list: Json[]): Fields => ({ ...minimal, temporary_grants: list });
  return [
    ["the six required fields is valid", "`temporary_grants` is optional.", minimal],
    ["a missing members is invalid", "`members` is required, even when it is empty.", without(minimal, "members")],
    ["a version with a fraction is invalid", "`version` is an integer.", { ...minimal, version: 1.5 }],
    ["an unknown top-level field is valid", "A bundle keeps fields that the schema does not name.", { ...minimal, note: "draft" }],
    ["an area with an unknown field is valid", "An area keeps fields that the schema does not name.", { ...minimal, areas: [{ name: "a", patterns: ["a/**"], color: "red" }] }],
    ["an area without patterns is invalid", "`patterns` is required in an area.", { ...minimal, areas: [{ name: "a" }] }],
    ["a role without push is valid", "`push` is optional in a role.", { ...minimal, roles: { dev: { read: [], write: [], exec: [] } } }],
    ["a role without exec is invalid", "`read`, `write`, and `exec` are required in a role.", { ...minimal, roles: { dev: { read: [], write: [] } } }],
    ["a member mapped to a number is invalid", "`members` maps a member id to a role name.", { ...minimal, members: { "mem-ana": 1 } }],
    ["a mission grant is valid", "A grant with scope mission needs `mission_id`.", grants(mission)],
    ["a timed grant is valid", "A grant with scope until needs `expires_at`.", grants(until)],
    ["a timed grant with a UTC offset is valid", "`expires_at` accepts `Z` or an offset such as `+01:00`.", grants({ ...until, expires_at: "2026-01-16T09:00:00+01:00" })],
    ["a timed grant without a UTC offset is invalid", "`expires_at` needs `Z` or an offset.", grants({ ...until, expires_at: "2026-01-16T09:00:00" })],
    ["a timed grant with a date and no time is invalid", "`expires_at` is a full timestamp.", grants({ ...until, expires_at: "2026-01-16" })],
    ["a timed grant whose expires_at ends in a line feed is invalid", "The pattern matches the whole value. In Python `re`, Perl, and Ruby a final `$` also matches before a trailing line feed, so a validator there replaces it, as `spec/README.md` says.", grants({ ...until, expires_at: "2026-01-16T09:00:00Z\n" })],
    ["a timed grant whose expires_at has a line before the timestamp is invalid", "The pattern matches the whole value. In Ruby `^` also matches after a line feed, so a validator there replaces it, as `spec/README.md` says.", grants({ ...until, expires_at: "x\n2026-01-16T09:00:00Z" })],
    ["a mission grant without mission_id is invalid", "The scope decides which field is required.", grants(without(mission, "mission_id"))],
    ["a mission grant with expires_at is invalid", "A grant accepts no field outside its scope.", grants({ ...mission, expires_at: "2026-01-16T09:00:00Z" })],
    ["a grant with an unknown field is invalid", "A temporary grant is a closed object, unlike the rest of the bundle.", grants({ ...mission, note: "x" })],
    ["a grant with scope once is invalid", "A temporary grant has scope `mission` or `until`.", grants({ ...mission, scope: "once" })],
    ["a grant with no actions is invalid", "`actions` has one or two entries.", grants({ ...mission, actions: [] })],
    ["a grant for exec is invalid", "A temporary grant covers `read` and `write` only.", grants({ ...mission, actions: ["exec"] })],
    ["a grant that lists one action twice is valid", "The schema limits the length of `actions`, not its content.", grants({ ...mission, actions: ["read", "read"] })],
    ["a grant with an empty member_id is invalid", "`member_id` is not empty.", grants({ ...mission, member_id: "" })],
  ];
}

function overrideCases(): Authored[] {
  const once = { id: "ovr-1", area: "billing", actions: ["write"], scope: "once", agent_session_id: "ses-1" };
  return [
    ["an override with scope once is valid", "`id`, `area`, `actions`, and `scope` are required.", once],
    ["an override with scope once and no session is valid", "The schema does not require the field that the scope needs. The evaluator treats such an override as inactive.", without(once, "agent_session_id")],
    ["an expires_at that is not a timestamp is valid", "`expires_at` is any string. The evaluator treats an unreadable expiry as inactive.", { ...once, expires_at: "not a timestamp" }],
    ["an unknown scope is invalid", "`scope` is `once`, `mission`, or `until`.", { ...once, scope: "forever" }],
    ["an unknown action is invalid", "`actions` holds only the four actions of the policy contract.", { ...once, actions: ["delete"] }],
    ["an empty list of actions is valid", "The schema sets no minimum length for `actions`.", { ...once, actions: [] }],
    ["a missing id is invalid", "`id` is required.", without(once, "id")],
    ["an unknown field is valid", "An override keeps fields that the schema does not name.", { ...once, granted_by: "mem-lee" }],
  ];
}

export function validationFixtures(reference: Reference): [string, FixtureFile][] {
  const { protocol } = reference;
  const targets: Target[] = [
    {
      contract: "room",
      schema: "room-event",
      description: "Valid and invalid stored Room events: one valid and at least one invalid body for each event type, an unknown type, and faults in the fields around the body.",
      zod: () => protocol.roomEventSchema,
      sources: ["packages/protocol/src/events.ts"],
      cases: roomEventCases(),
    },
    {
      contract: "room",
      schema: "envelope",
      description:
        "Valid and invalid transport envelopes. The verdict of a message with the type `event` comes from the message schema that the hub and the daemon of the reference parse with. The verdict of every other message comes from the envelope schema of the reference, because version 1 defines no other payload.",
      zod: (input) => ((input as Fields | null)?.type === "event" ? protocol.wsMessageSchema : protocol.envelopeSchema),
      sources: ["packages/protocol/src/envelope.ts", "packages/protocol/src/ws-messages.ts"],
      cases: envelopeCases(reference),
    },
    {
      contract: "room",
      schema: "resume",
      description: "Valid and invalid payloads of a `resume` message.",
      zod: () => messagePayload(reference, "resume"),
      sources: ["packages/protocol/src/ws-messages.ts"],
      cases: resumeCases(),
    },
    {
      contract: "room",
      schema: "error",
      description: "Valid and invalid payloads of an `error` message.",
      zod: () => messagePayload(reference, "error"),
      sources: ["packages/protocol/src/ws-messages.ts"],
      cases: errorCases(),
    },
    {
      contract: "policy",
      schema: "bundle",
      description: "Valid and invalid policy bundles.",
      zod: () => protocol.policyBundleSchema,
      sources: ["packages/protocol/src/policy/types.ts"],
      cases: bundleCases(),
    },
    {
      contract: "policy",
      schema: "override",
      description: "Valid and invalid overrides.",
      zod: () => protocol.overrideSchema,
      sources: ["packages/protocol/src/policy/types.ts"],
      cases: overrideCases(),
    },
  ];
  return targets.map(file);
}

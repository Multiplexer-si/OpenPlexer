import type { Json } from "./output.ts";
import type { Reference, ZodSchema } from "./reference.ts";

type Schema = Record<string, Json>;

const DRAFT = "https://json-schema.org/draft/2020-12/schema";

/** The Room event types that the v1 contract defines. The reference defines more; `spec/room/README.md` lists them. */
export const ROOM_EVENT_TYPES = [
  "status",
  "claim",
  "release",
  "decision",
  "decision.confirm",
  "question",
  "answer",
  "blocked",
  "human",
  "policy.denied",
  "policy.override",
  "policy.override.revoked",
] as const;

/**
 * The white space of the contracts: the code points that `\s` matches in ECMA-262, as single code points and as
 * inclusive ranges. `spec/README.md` lists them.
 */
const WHITE_SPACE: (number | [first: number, last: number])[] = [0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x20, 0xa0, 0x1680, [0x2000, 0x200a], 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff];

/**
 * A pattern that finds one code point that is not white space. The class holds the characters themselves, with no
 * escape, so every regular expression engine reads it the same way. Fails if the class differs from `\S` of this
 * runtime for one UTF-16 code unit.
 */
function notWhiteSpace(): string {
  const members = WHITE_SPACE.map((entry) => (typeof entry === "number" ? String.fromCharCode(entry) : `${String.fromCharCode(entry[0])}-${String.fromCharCode(entry[1])}`));
  const pattern = `[^${members.join("")}]`;
  const authored = new RegExp(pattern);
  for (let unit = 0; unit <= 0xffff; unit++) {
    const text = String.fromCharCode(unit);
    if (authored.test(text) !== /\S/.test(text)) throw new Error(`WHITE_SPACE disagrees with \\s of ECMA-262 at U+${unit.toString(16).padStart(4, "0")}`);
  }
  return pattern;
}

/** The characters that ECMA-262 lets `\` escape with the `u` flag: the syntax characters and `/`. */
const ESCAPABLE = "^$\\.*+?()[]{}|/";

/** The largest count, and the largest product of the counts of nested quantifiers, that RE2 accepts. */
const MAX_COUNT = 1000;

/**
 * The count of a counted quantifier that RE2 limits: `m` for `{n,m}`, `n` for `{n}`, and `n` for `{n,}`, or 1 when `n`
 * is 0, because RE2 passes the limit through `{0,}` unchanged. A string says why the quantifier is outside the subset.
 */
function countOf(quantifier: string): number | string {
  const [, n, m] = /^\{([0-9]+)(?:,([0-9]*))?\}$/.exec(quantifier)!;
  const first = Number(n);
  const most = m === undefined ? first : m === "" ? undefined : Number(m);
  if (first > MAX_COUNT || (most ?? 0) > MAX_COUNT) return `the quantifier ${quantifier}, whose count is above ${MAX_COUNT}`;
  if (most !== undefined && most < first) return `the quantifier ${quantifier}, whose second count is less than its first`;
  if (most === 0) return `the quantifier ${quantifier}, which repeats nothing`;
  return most ?? Math.max(first, 1);
}

/**
 * Why a pattern is outside the portable subset that `spec/README.md` defines, or null. The subset is a grammar, and
 * this scan accepts exactly its patterns. Engines disagree on everything outside it, such as an escape that the `u`
 * flag of ECMA-262 rejects, a `{` that is not a quantifier, a `[` inside a class, or a count that RE2 rejects. The
 * test in `internal/spectest` applies the same scan to the committed files.
 *
 * `products` holds, for each group that is open, the largest product of counts inside it. `pending` is the product
 * of the atom just read, which a quantifier may still multiply.
 */
export function notPortable(pattern: string): string | null {
  const unsafe = (c: string): string | null =>
    c >= "\ud800" && c <= "\udfff" ? "a surrogate code unit: a character outside the Basic Multilingual Plane, or a lone surrogate" : c === "\ufffd" ? "the replacement character U+FFFD" : null;
  let before: "nothing" | "atom" | "quantifier" = "nothing";
  const products = [1];
  let pending: number | undefined;
  const flush = () => {
    if (pending !== undefined) products[products.length - 1] = Math.max(products.at(-1)!, pending);
    pending = undefined;
  };
  for (let i = pattern[0] === "^" ? 1 : 0; i < pattern.length; ) {
    const c = pattern[i]!;
    const quantifier = "*+?".includes(c) ? c : c === "{" ? /^\{[0-9]+(?:,[0-9]*)?\}/.exec(pattern.slice(i))?.[0] : undefined;
    if (unsafe(c) !== null) return unsafe(c);
    if (quantifier === undefined) flush();
    if (c === "$") {
      if (i !== pattern.length - 1) return "a `$` that does not end the pattern";
      i++;
    } else if (c === "^") {
      return "a `^` that does not start the pattern";
    } else if (c === "\\") {
      if (!ESCAPABLE.includes(pattern[i + 1] ?? "~")) return `the escape \\${pattern[i + 1] ?? ""}`;
      before = "atom";
      pending = 1;
      i += 2;
    } else if (c === ".") {
      return "a `.` with no `\\` before it";
    } else if (c === "[") {
      i += pattern[i + 1] === "^" ? 2 : 1;
      if (pattern[i] === "]") return "a `]` as the first member of a class";
      for (; pattern[i] !== "]"; i++) {
        const member = pattern[i];
        if (member === undefined) return "a class with no `]` at its end";
        if (unsafe(member) !== null) return unsafe(member);
        if (member === "[") return "a `[` inside a class with no `\\` before it";
        if (member === ".") return "a `.` with no `\\` before it";
        if ("-&|~".includes(member) && pattern[i + 1] === member) return `\`${member}${member}\` inside a class`;
        if (member === "\\") {
          if (!(ESCAPABLE + "-").includes(pattern[i + 1] ?? "~")) return `the escape \\${pattern[i + 1] ?? ""}`;
          i++;
        }
      }
      before = "atom";
      pending = 1;
      i++;
    } else if (c === "(") {
      if (pattern[i + 1] === "?" && pattern[i + 2] !== ":") return "a group that starts with `(?` and is not `(?:`";
      products.push(1);
      before = "nothing";
      i += pattern[i + 1] === "?" ? 3 : 1;
    } else if (c === ")") {
      if (products.length === 1) return "a `)` that closes no group";
      pending = products.pop();
      before = "atom";
      i++;
    } else if (c === "|") {
      before = "nothing";
      i++;
    } else if (quantifier !== undefined) {
      if (before === "quantifier") return `the quantifier ${quantifier} right after a quantifier`;
      if (before === "nothing") return `the quantifier ${quantifier} with nothing before it to repeat`;
      if (quantifier[0] === "{") {
        const count = countOf(quantifier);
        if (typeof count === "string") return count;
        pending = pending! * count;
        if (pending > MAX_COUNT) return `nested counts whose product is above ${MAX_COUNT}`;
      }
      flush();
      before = "quantifier";
      i += quantifier.length;
    } else if (c === "{") {
      return "a `{` that does not start a quantifier `{n}`, `{n,}`, or `{n,m}`";
    } else if (c === "}" || c === "]") {
      return `a \`${c}\` with no \`\\\` before it outside a class or a quantifier`;
    } else {
      before = "atom";
      pending = 1;
      i++;
    }
  }
  return products.length === 1 ? null : "a group with no `)` at its end";
}

/** Fails unless an ECMA-262 engine compiles the pattern both without a flag and with the `u` flag. */
function compiles(pattern: string): string | null {
  for (const flags of ["", "u"]) {
    try {
      new RegExp(pattern, flags);
    } catch (e) {
      return `a syntax error with the flags "${flags}": ${(e as Error).message}`;
    }
  }
  return null;
}

/**
 * Writes `\d` as the ten ASCII digits. In ECMA-262 it matches only those ten, and in some other engines it matches
 * more. Inside a class the digits are a range, because a class cannot hold a class.
 */
function asciiDigits(pattern: string): string {
  let out = "";
  let inClass = false;
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i];
    if (c === "\\") {
      const escape = pattern.slice(i, i + 2);
      out += escape !== "\\d" ? escape : inClass ? "0-9" : "[0-9]";
      i++;
      continue;
    }
    if (c === "[") inClass = true;
    else if (c === "]") inClass = false;
    out += c;
  }
  return out;
}

/** Applies `change` to every regular expression in a schema: the value of each `pattern` keyword and each key of a `patternProperties` object. */
function mapPatterns(node: Json, change: (pattern: string) => string): Json {
  if (Array.isArray(node)) return node.map((element) => mapPatterns(element, change));
  if (node === null || typeof node !== "object") return node;
  return Object.fromEntries(
    Object.entries(node).map(([key, value]): [string, Json] => {
      if (key === "pattern" && typeof value === "string") return [key, change(value)];
      if (key === "patternProperties" && value !== null && typeof value === "object" && !Array.isArray(value)) {
        return [key, Object.fromEntries(Object.entries(value).map(([pattern, schema]) => [change(pattern), mapPatterns(schema, change)]))];
      }
      return [key, mapPatterns(value, change)];
    }),
  );
}

/** Fails if a schema that the extractor is about to write has a pattern outside the portable subset. */
export function assertPortablePatterns(path: string, schema: Json): void {
  mapPatterns(schema, (pattern) => {
    const reason = notPortable(pattern) ?? compiles(pattern);
    if (reason !== null) throw new Error(`${path}: the pattern ${JSON.stringify(pattern)} is not portable: it has ${reason}`);
    return pattern;
  });
}

/**
 * Body fields that the reference trims before it checks the minimum length. JSON Schema cannot trim, so the
 * contract adds a pattern that requires one character that is not white space.
 */
const TRIMMED_FIELDS: [type: string, field: string][] = [
  ["question", "text"],
  ["question", "addressed_to"],
  ["answer", "text"],
];

export const schemaId = (contract: string, name: string): string => `urn:openplexer:spec:${contract}:v1:${name}`;
export const schemaPath = (contract: string, name: string): string => `spec/${contract}/v1/${name}.schema.json`;

function child(node: Schema, segment: string, path: string): Schema {
  const next = segment === "*" ? (node.items ?? node.additionalProperties) : (node.properties as Schema | undefined)?.[segment];
  if (next === undefined || next === null || typeof next !== "object" || Array.isArray(next)) {
    throw new Error(`no schema at ${path}: the reference changed, or the description table is wrong`);
  }
  return next as Schema;
}

/** Adds a `description` to each listed property. A path is property names joined by `/`; `*` is the element of an array or map. */
function describe(schema: Schema, docs: Record<string, string>): Schema {
  for (const [path, text] of Object.entries(docs)) {
    const node = path.split("/").reduce((at, segment) => child(at, segment, path), schema);
    node.description = text;
  }
  return schema;
}

function generated(reference: Reference, zod: ZodSchema): Schema {
  const { $schema: _, ...body } = reference.toJsonSchema(zod);
  return mapPatterns(body, asciiDigits) as Schema;
}

interface Document {
  contract: string;
  name: string;
  title: string;
  description: string;
  body: Schema;
}

function policySchemas(reference: Reference): Document[] {
  const { protocol } = reference;
  const bundle = describe(generated(reference, protocol.policyBundleSchema), {
    version: "The version of the policy. A daemon refuses a bundle whose version is lower than the version it holds for the same project.",
    project_id: "The project that the policy belongs to.",
    repos: "The names of the repositories of the project.",
    areas: "The named sets of paths that the roles refer to.",
    "areas/*/name": "The name that grants, overrides, and results use for the area.",
    "areas/*/patterns": "Each pattern is `repo:glob`, `*:glob` for any repository, or a bare glob, which also means any repository.",
    "areas/*/private": "If true, a read or a write needs a grant or an override, and a more specific area cannot reopen the path.",
    "areas/*/generated": "Marks lock files and build output. The flag never changes the result of an evaluation.",
    "areas/*/system": "If true, nothing can write to the area. No grant and no override changes that.",
    "areas/*/owner_team_id": "The team that owns the area. The evaluator does not read it.",
    roles: "Maps each role name to the grants of that role.",
    "roles/*/read": "The names of the private areas that the role may read.",
    "roles/*/write": "The names of the areas that the role may write.",
    "roles/*/exec": "Command prefixes. A command matches a prefix if the two are equal, or if the command starts with the prefix followed by white space.",
    "roles/*/push": "The repositories that the role may push to. `*` means every repository.",
    members: "Maps each member id to a role name.",
    temporary_grants: "Grants that give one member read or write access to one area, for one mission or until a time.",
  });
  const override = describe(generated(reference, protocol.overrideSchema), {
    id: "The id of the override.",
    area: "The name of the area that the override opens.",
    actions: "The actions that the override allows. Only `read` and `write` have an effect.",
    scope: "`once` needs `agent_session_id`. `mission` needs `mission_id`. `until` needs `expires_at`.",
    agent_session_id: "If present, the override applies only to this agent session.",
    mission_id: "If present, the override applies only to this mission.",
    expires_at: "If present, the override is inactive from this time on. A value that is not a timestamp in the form that the policy contract defines makes the override inactive.",
  });
  const action = generated(reference, protocol.actionSchema);
  const grants = (bundle.properties as Schema).temporary_grants as Schema;
  const timestamp = (((grants.items as Schema).oneOf as Schema[])[1]!.properties as Schema).expires_at as Schema;
  const query: Schema = {
    type: "object",
    properties: {
      repo: { type: "string", description: "The repository that the action is in. `exec` ignores it." },
      path: { type: "string", description: "The file path for `read` and `write`. `exec` and `git.push` ignore it." },
      command: { type: "string", description: "The command line for `exec`." },
      action: { ...action, description: "The action to evaluate." },
      member: { type: "string", description: "The id of the member that the agent acts for." },
      overrides: { type: "array", items: { $ref: schemaId("policy", "override") }, description: "The overrides that are active for the caller." },
      now: { ...timestamp, description: "The time of the evaluation. The evaluator reads no clock." },
      agent_session_id: { type: "string", description: "The agent session that asks." },
      mission_id: { type: "string", description: "The mission of that agent session." },
      parent: {
        type: "object",
        description: "Present for a sub-agent. Overrides and temporary grants then match the session and the mission of the parent.",
        properties: { agent_session_id: { type: "string" }, mission_id: { type: "string" } },
        required: ["agent_session_id"],
        additionalProperties: false,
      },
    },
    required: ["repo", "path", "action", "member", "now"],
    additionalProperties: false,
    if: { properties: { action: { const: "exec" } }, required: ["action"] },
    then: { required: ["command"] },
  };
  const result: Schema = {
    type: "object",
    properties: {
      decision: { type: "string", enum: ["allow", "deny"] },
      area: { type: "string", description: "The area that decided the result. Absent when no area matched, and for `exec` and `git.push`." },
      rule: { type: "string", description: "A sentence for a person. The wording is not part of the contract." },
    },
    required: ["decision", "rule"],
    additionalProperties: false,
  };
  const message = reference.protocol.wsMessageSchema.options.find((option) => option.shape.type!.value === "policy.bundle");
  if (!message) throw new Error("the reference has no policy.bundle message");
  const signed = describe(generated(reference, message.shape.payload!), {
    version: "A copy of the bundle version, for display. The signed payload is the authority.",
    bundle: "A copy of the bundle, for display. The signed payload is the authority.",
    signature: "A compact JWS. Its payload is the canonical JSON of the bundle.",
  });
  const header: Schema = {
    type: "object",
    properties: {
      alg: { const: "ES256" },
      typ: { const: reference.policyJwsTyp },
      kid: { type: "string", description: "The `kid` of the key in the JWKS that signed the bundle." },
    },
    required: ["alg", "typ"],
  };
  const jwks: Schema = {
    type: "object",
    properties: { keys: { type: "array", items: { type: "object" }, description: "Public keys in the JWK format of RFC 7517." } },
    required: ["keys"],
  };
  return [
    { contract: "policy", name: "bundle", title: "Policy bundle", description: "The policy of one project. Generated from the reference.", body: bundle },
    { contract: "policy", name: "override", title: "Override", description: "A grant that a lead gives to open one area for an agent. Generated from the reference.", body: override },
    { contract: "policy", name: "eval-query", title: "Evaluation query", description: "The request that the policy evaluator answers. Authored from a type of the reference.", body: query },
    { contract: "policy", name: "eval-result", title: "Evaluation result", description: "The answer of the policy evaluator. Authored from a type of the reference.", body: result },
    { contract: "policy", name: "signed-bundle", title: "Signed policy bundle", description: "The message that carries a policy bundle and its signature to a daemon. Generated from the reference.", body: signed },
    { contract: "policy", name: "jws-protected-header", title: "Protected header of the bundle signature", description: "The protected header of the compact JWS that signs a policy bundle. Authored.", body: header },
    { contract: "policy", name: "jwks", title: "JSON Web Key Set", description: "The public keys that verify a bundle signature. Authored. RFC 7517 defines the key format.", body: jwks },
  ];
}

/** The payload schema of one message type of the reference. */
export function messagePayload(reference: Reference, type: string): ZodSchema {
  const message = reference.protocol.wsMessageSchema.options.find((option) => option.shape.type!.value === type);
  if (!message) throw new Error(`the reference has no ${type} message`);
  return message.shape.payload!;
}

function roomSchemas(reference: Reference): Document[] {
  const { protocol } = reference;
  const missing = ROOM_EVENT_TYPES.filter((type) => !protocol.EVENT_TYPES.includes(type));
  if (missing.length > 0) throw new Error(`the reference has no event type ${missing.join(", ")}`);

  const bodies: Record<string, Schema> = {};
  for (const type of ROOM_EVENT_TYPES) bodies[type] = generated(reference, protocol.eventPayloadSchemas[type]!);
  for (const [type, field] of TRIMMED_FIELDS) child(bodies[type]!, field, `${type}/${field}`).pattern = notWhiteSpace();
  const threading = { thread_id: "The id of the event that started the thread.", reply_to: "The id of the event that this event answers." };
  describe(bodies.status!, { summary: "What the agent is doing.", files: "The files that the agent is working on.", plan_step: "The step of the plan that the agent is on.", ...threading });
  describe(bodies.claim!, {
    item: "The piece of work that the agent claims.",
    paths: "The paths and globs that the claim covers, in the claims dialect.",
    intent: "What the agent intends to do.",
    ttl_seconds: "The number of seconds after which the claim expires.",
    ...threading,
  });
  describe(bodies.release!, { claim_id: "The id of the claim that the agent releases.", outcome: "What happened to the work.", ...threading });
  describe(bodies.decision!, {
    title: "A short name for what the team settled.",
    detail: "The full text of the decision.",
    scope: "What the decision applies to.",
    supersedes: "The id of the decision that this decision replaces.",
    ...threading,
  });
  describe(bodies["decision.confirm"]!, { decision_id: "The id of the decision event.", result: "Whether a person confirmed or rejected the decision.", ...threading });
  describe(bodies.question!, { text: "The question.", addressed_to: "The member that the question is for.", ack_required: "If true, the asker wants an acknowledgement.", ...threading });
  describe(bodies.answer!, { question_id: "The id of the question event.", text: "The answer.", ...threading });
  describe(bodies.blocked!, { reason: "Why the agent cannot continue.", needs: "What would unblock the agent.", ...threading });
  describe(bodies.human!, { text: "A message that a person wrote.", mentions: "The members that the message mentions.", ...threading });
  describe(bodies["policy.denied"]!, {
    action: "The action that the gate denied.",
    path: "The path that the gate denied. For `exec` it is the command.",
    rule: "The `rule` text of the evaluation result.",
    detected: "`after` marks a write that the daemon found after it had happened.",
    ...threading,
  });
  describe(bodies["policy.override"]!, {
    lead: "The member that granted the override.",
    agent: "The agent session that the override is for.",
    areas: "The names of the areas that the override opens.",
    actions: "The actions that the override allows.",
    scope: "The scope of the override: `once`, `mission`, or `until`.",
    reason: "Why the lead granted the override.",
    ...threading,
  });
  describe(bodies["policy.override.revoked"]!, {
    override_id: "The id of the override.",
    agent: "The agent session that the override was for.",
    area: "The name of the area that the override opened.",
    revoked_by: "The member that revoked the override.",
    reason: "The reason that the override was granted for.",
    ...threading,
  });

  const event = describe(generated(reference, protocol.roomEventSchema), {
    id: "The id of the event.",
    seq: "The position of the event in its Room. The hub assigns it.",
    mission: "The mission that the event belongs to.",
    type: "The event type. It selects the schema of `body`.",
    agent: "Who posted the event: an agent session, or the member for an event that a person caused.",
    "agent/id": "The id of the agent session. For an event that no agent session posted, the id of the member.",
    "agent/vendor": "The coding agent, such as `claude-code` or `codex`. `human` for an event that no agent session posted.",
    "agent/parent": "The id of the root agent session. Present only on the events of a sub-agent.",
    on_behalf_of: "The id of the member that the agent acts for, or of the member that posted the event.",
    at: "The time that the hub stored the event.",
    body: "The content of the event. Its schema depends on `type`.",
  });
  (event.properties as Schema).type = { ...((event.properties as Schema).type as Schema), enum: [...ROOM_EVENT_TYPES] };
  event.allOf = ROOM_EVENT_TYPES.map((type) => ({
    if: { properties: { type: { const: type } }, required: ["type"] },
    then: { properties: { body: { $ref: `#/$defs/${type}` } } },
  }));
  event.$defs = bodies;

  const envelope = describe(generated(reference, protocol.envelopeSchema), {
    v: `The version of the wire protocol. This contract describes version ${protocol.PROTOCOL_VERSION}.`,
    type: "The message type. It selects the schema of `payload`.",
    room: "The Room that the message belongs to. Set on a message that carries an event, and on a message about the connection that concerns one Room.",
    seq: "The position of an event in its Room. The hub assigns it. Set on a message that carries an event, and on the answer to a post that the hub accepted.",
    event_id: "The id of the event that the message carries, or of the event that an accepted post created.",
    sender: "Who the message is from. On a message with the type `event`, the poster of the event: its agent session with the kind `daemon`, or its member with the kind `dashboard`. On another message from the hub, the hub.",
    ts: "A time that the hub sets. On a message with the type `event`, the `at` of the event. On another message from the hub, the time that the hub sent it.",
    payload: "The content of the message. For the type `event` it is a stored Room event.",
  });
  envelope.if = { properties: { type: { const: "event" } }, required: ["type"] };
  envelope.then = { properties: { payload: { $ref: schemaId("room", "room-event") } } };
  const resume = describe(generated(reference, messagePayload(reference, "resume")), {
    last_seq: "The highest `seq` that the sender has received for the Room. 0 if it has received none.",
  });
  const error = describe(generated(reference, messagePayload(reference, "error")), {
    code: "A stable token that names the error. The README lists the codes of version 1.",
    message: "A sentence for a person. The wording is not part of the contract.",
    client_event_id: "The id that the client gave to the post that this error answers.",
  });
  return [
    { contract: "room", name: "resume", title: "Payload of a resume message", description: "What a receiver sends to get the events of one Room after a `seq`. The Room is the `room` of the envelope. Generated from the reference.", body: resume },
    { contract: "room", name: "error", title: "Payload of an error message", description: "What the hub sends when it refuses a message or stops sending. Generated from the reference.", body: error },
    {
      contract: "room",
      name: "room-event",
      title: "Room event",
      description: "A stored Room event. The base object and each body under `$defs` are generated from the reference. The list of event types and the rule that ties `body` to `type` are authored.",
      body: event,
    },
    { contract: "room", name: "envelope", title: "Transport envelope", description: "The object that wraps every WebSocket message between a daemon and the hub. Generated from the reference. The rule that ties `payload` to the type `event` is authored.", body: envelope },
  ];
}

function auditSchemas(reference: Reference): Document[] {
  const { protocol } = reference;
  const fields = {
    id: "The position of the record in the log, as a decimal string. Not hashed.",
    workspace_id: "The team that the log belongs to. Hashed.",
    actor_member_id: "The member that acted, or null. Hashed.",
    agent_session_id: "The agent session that the record is about, or null. Hashed.",
    action: "What happened, as a stable token such as `override.granted`. Hashed.",
    target: "What the action was applied to. Hashed.",
    reason: "The reason that the actor gave, or null. Hashed.",
    meta: "More data about the action. Hashed.",
    flagged: "True if `reason` or `meta` quote text that moderation flagged. Hashed.",
    created_at: "The time of the record, in UTC with milliseconds, such as `2026-01-15T12:00:00.000Z`. Hashed as this exact string.",
    prev_hash: "The `hash` of the record before this one. The first record has 64 zeros.",
    hash: "The SHA-256 of `prev_hash` followed by the canonical JSON of the hashed fields, in lowercase hexadecimal.",
  };
  const record = describe(generated(reference, protocol.auditEntrySchema.omit({ result: true, agent_member_id: true })), fields);
  const page = describe(generated(reference, protocol.auditPageSchema), {
    entries: "The records, newest first. Each one has the fields of a record and two fields that are not hashed.",
    ...Object.fromEntries(Object.entries(fields).map(([name, text]) => [`entries/*/${name}`, text])),
    "entries/*/result": "The last part of `action`, after the last `.` or `-`. Not hashed.",
    "entries/*/agent_member_id": "The member that owns the agent session, or null. Not hashed.",
    next_cursor: "Null in an export. In a page, the id to pass to get the next page.",
  });
  const verification = generated(reference, protocol.auditVerificationSchema);
  return [
    { contract: "audit", name: "record", title: "Audit record", description: "One record of the hash chain. Generated from the reference.", body: record },
    { contract: "audit", name: "export", title: "Audit export", description: "The JSON export of an audit log. Generated from the reference.", body: page },
    {
      contract: "audit",
      name: "verify-result",
      title: "Chain verification result",
      description: "The result of a chain verification. `entries` is the number of records checked. `broken_at` is the `id` of the first record that fails. Generated from the reference.",
      body: verification,
    },
  ];
}

export function contractSchemas(reference: Reference): Map<string, Json> {
  const documents = [...policySchemas(reference), ...roomSchemas(reference), ...auditSchemas(reference)];
  return new Map(
    documents.map(({ contract, name, title, description, body }) => {
      const path = schemaPath(contract, name);
      const schema = { $schema: DRAFT, $id: schemaId(contract, name), title, description, ...body };
      assertPortablePatterns(path, schema);
      return [path, schema];
    }),
  );
}

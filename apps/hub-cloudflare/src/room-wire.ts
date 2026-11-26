import { canonicalJSON, hasTrailingText, isJsonObject } from "./canonical";
import type { JsonObject, JsonValue } from "./canonical";

const decoder = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true });
const maxJSONDepth = 10_000;
// An unpaired surrogate cannot occur in a decoded Room string. Mark number
// tokens so body extensions keep their JSON spelling, even outside double range.
const numberMarker = "\ud800";
const tokens = /"(?:[^"\\]|\\[^])*"|[{}\[\]]|-?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/g;

export function decodeRoomJSON(input: Uint8Array): JsonValue {
  let text: string;
  try {
    text = decoder.decode(input);
  } catch {
    throw new SyntaxError("is not valid UTF-8");
  }
  try {
    JSON.parse(text);
    let depth = 0;
    const encoded = text.replace(tokens, (token) => {
      if (token[0] === '"') {
        const value: string = JSON.parse(token);
        return value.isWellFormed() ? token : JSON.stringify(value.toWellFormed());
      }
      if (token === "{" || token === "[") {
        if (++depth > maxJSONDepth) throw new SyntaxError();
      } else if (token === "}" || token === "]") {
        depth--;
      } else {
        return JSON.stringify(numberMarker + token);
      }
      return token;
    });
    return JSON.parse(encoded);
  } catch {
    throw new SyntaxError(hasTrailingText(text) ? "has text after the JSON value" : "is not valid JSON");
  }
}

export function encodeRoomJSON(value: JsonObject, outerDepth = 0): string {
  let depth = outerDepth;
  return canonicalJSON(value).replace(tokens, (token) => {
    if (token === "{" || token === "[") {
      if (++depth > maxJSONDepth) throw new RangeError("Room JSON exceeds 10000 levels");
    } else if (token === "}" || token === "]") {
      depth--;
    }
    return token.startsWith('"\\ud800') ? token.slice(7, -1) : token;
  });
}

type Read<T> = (path: string, value: JsonValue) => T;
type Field = [name: string, read: Read<unknown>, required?: boolean];

function isText(value: JsonValue): value is string {
  return typeof value === "string" && !value.startsWith(numberMarker);
}

export type Agent = { id: string; vendor: string; parent?: string };

function validate<T>(read: (reader: Reader) => T): T {
  const reader = new Reader();
  const parsed = read(reader);
  if (reader.issues.length) throw new SyntaxError(reader.issues.join("; "));
  return parsed;
}

class Reader {
  readonly issues: string[] = [];

  fail(path: string, reason: string): void {
    this.issues.push(path ? path + " " + reason : reason);
  }

  field<T>(obj: JsonObject, path: string, name: string, read: Read<T>, required?: true): T;
  field<T>(obj: JsonObject, path: string, name: string, read: Read<T>, required: boolean): T | undefined;
  field<T>(obj: JsonObject, path: string, name: string, read: Read<T>, required = true): T | undefined {
    const fieldPath = path ? path + "." + name : name;
    if (Object.hasOwn(obj, name)) return read(fieldPath, obj[name]);
    if (required) this.fail(fieldPath, "is required");
  }

  object(path: string, value: JsonValue, read: (obj: JsonObject) => void): void {
    if (isJsonObject(value)) read(value);
    else this.fail(path, "must be an object");
  }

  required(obj: JsonObject, name: string): JsonValue {
    if (Object.hasOwn(obj, name)) return obj[name];
    this.fail(name, "is required");
    return null;
  }

  text: Read<string> = (path, value) => {
    if (isText(value)) return value;
    this.fail(path, "must be a string");
    return "";
  };

  nonEmpty: Read<string> = (path, value) => {
    if (isText(value) && value !== "") return value;
    this.fail(path, "must be a string that is not empty");
    return "";
  };

  timestamp: Read<string> = (path, value) => {
    if (isText(value) && value.length === 24) {
      const date = new Date(value);
      if (Number.isFinite(date.valueOf()) && date.toISOString() === value) return value;
    }
    this.fail(path, "must be a UTC timestamp with milliseconds");
    return "";
  };

  nonBlank: Read<string> = (path, value) => {
    if (isText(value) && value.trim() !== "") return value;
    this.fail(path, "must be a string with a character that is not white space");
    return "";
  };

  flag: Read<boolean> = (path, value) => {
    if (typeof value === "boolean") return value;
    this.fail(path, "must be true or false");
    return false;
  };

  integerFrom(low: number): Read<number> {
    return (path, value) => {
      const number = typeof value === "string" && value.startsWith(numberMarker)
        ? Number(value.slice(1)) : value;
      if (typeof number === "number" && Number.isSafeInteger(number) && number >= low) return number;
      this.fail(path, "must be an integer from " + low + " to " + Number.MAX_SAFE_INTEGER);
      return 0;
    };
  }

  list: Read<string[]> = (path, value) => {
    if (Array.isArray(value)) return value.map((item, i) => this.text(path + "[" + i + "]", item));
    this.fail(path, "must be a list");
    return [];
  };

  choice<T extends string>(choices: readonly T[]): Read<T | undefined> {
    return (path, value) => {
      const selected = choices.find((choice) => choice === value);
      if (selected !== undefined) return selected;
      this.fail(path, "must be one of " + choices.map((choice) => JSON.stringify(choice)).join(", "));
    };
  }

  agent: Read<Agent> = (path, value) => {
    const agent: Agent = { id: "", vendor: "" };
    this.object(path, value, (obj) => {
      agent.id = this.field(obj, path, "id", this.nonEmpty);
      agent.vendor = this.field(obj, path, "vendor", this.nonEmpty);
      const parent = this.field(obj, path, "parent", this.nonEmpty, false);
      if (parent !== undefined) agent.parent = parent;
    });
    return agent;
  };

  eventType = (path: string, value: JsonValue) => this.choice(eventTypes)(path, value);

  body(obj: JsonObject, type: EventType | undefined, path = "body"): JsonObject {
    let body = {};
    if (!Object.hasOwn(obj, "body")) this.fail(path, "is required");
    else if (type !== undefined) {
      this.object(path, obj.body, (value) => {
        for (const [name, read, required] of [
          ["thread_id", this.text, false], ["reply_to", this.text, false], ...eventFields(this)[type],
        ] satisfies Field[]) this.field(value, path, name, read, required ?? true);
        body = value;
      });
    }
    return body;
  }

  event(obj: JsonObject, path: string): void {
    this.field(obj, path, "id", this.nonEmpty);
    this.field(obj, path, "seq", this.integerFrom(0));
    this.field(obj, path, "mission", this.nonEmpty);
    const type = this.field(obj, path, "type", this.eventType);
    this.field(obj, path, "agent", this.agent);
    this.field(obj, path, "on_behalf_of", this.nonEmpty);
    this.field(obj, path, "at", this.nonEmpty);
    this.body(obj, type, path ? path + ".body" : "body");
  }
}

const eventTypes = [
  "status", "claim", "release", "decision", "decision.confirm", "question", "answer", "blocked", "human",
  "policy.denied", "policy.override", "policy.override.revoked",
] as const;
type EventType = typeof eventTypes[number];

function eventFields(r: Reader): Record<EventType, Field[]> {
  return {
    status: [["summary", r.text], ["plan_step", r.text, false], ["files", r.list, false]],
    claim: [["item", r.text], ["paths", r.list], ["intent", r.text], ["ttl_seconds", r.integerFrom(1)]],
    release: [["claim_id", r.text], ["outcome", r.text, false]],
    decision: [["title", r.text], ["detail", r.text], ["scope", r.text, false], ["supersedes", r.text, false]],
    "decision.confirm": [["decision_id", r.text], ["result", r.choice(["confirmed", "rejected"])]],
    question: [["text", r.nonBlank], ["addressed_to", r.nonBlank, false], ["ack_required", r.flag, false]],
    answer: [["question_id", r.nonEmpty], ["text", r.nonBlank]],
    blocked: [["reason", r.text], ["needs", (path, value) => r.object(path, value, (obj) => {
      r.field(obj, path, "decision", r.text, false);
      r.field(obj, path, "request_id", r.text, false);
    }), false]],
    human: [["text", r.text], ["mentions", r.list, false]],
    "policy.denied": [
      ["action", r.text], ["path", r.text], ["rule", r.text], ["detected", r.text, false],
      ["decided_at", r.timestamp, false],
    ],
    "policy.override": [
      ["lead", r.text], ["agent", r.text], ["areas", r.list], ["actions", r.list], ["scope", r.text], ["reason", r.text],
    ],
    "policy.override.revoked": [
      ["override_id", r.text], ["agent", r.text], ["area", r.text], ["revoked_by", r.text], ["reason", r.text, false],
    ],
  };
}

export function validateEvent(value: JsonValue): void {
  validate((r) => r.object("", value, (obj) => r.event(obj, "")));
}

export function validateEnvelope(value: JsonValue): void {
  validate((r) => r.object("", value, (obj) => {
    r.field(obj, "", "v", r.integerFrom(1));
    const type = r.field(obj, "", "type", r.nonEmpty);
    r.field(obj, "", "sender", (path, value) => r.object(path, value, (sender) => {
      r.field(sender, path, "id", r.nonEmpty);
      r.field(sender, path, "kind", r.choice(["daemon", "dashboard", "server"]));
    }));
    r.field(obj, "", "room", r.nonEmpty, false);
    r.field(obj, "", "seq", r.integerFrom(0), false);
    r.field(obj, "", "event_id", r.nonEmpty, false);
    r.field(obj, "", "ts", r.nonEmpty, false);
    const payload = r.required(obj, "payload");
    if (type === "event" && !r.issues.length) r.object("payload", payload, (event) => r.event(event, "payload"));
  }));
}

function readRequest<T>(input: Uint8Array, read: (r: Reader, obj: JsonObject) => T): T {
  return validate((r) => {
    const value = decodeRoomJSON(input);
    if (!isJsonObject(value)) throw new SyntaxError("must be an object");
    return read(r, value);
  });
}

export function parseEventPost(input: Uint8Array) {
  return readRequest(input, (r, obj) => {
    const type = r.field(obj, "", "type", r.eventType);
    const agent = r.field(obj, "", "agent", r.agent, false);
    const body = r.body(obj, type);
    return { type: type ?? "status", agent, body };
  });
}

export function denialDecidedAt(post: ReturnType<typeof parseEventPost>, receivedAt: number): string | undefined {
  const decidedAt = post.body.decided_at;
  if (post.type !== "policy.denied" || typeof decidedAt !== "string") return undefined;
  if (Date.parse(decidedAt) - receivedAt > 30_000) {
    throw new SyntaxError("body.decided_at must be no more than 30 seconds after at");
  }
  return decidedAt;
}

export function parseClaimPost(input: Uint8Array) {
  return readRequest(input, (r, obj) => ({
    agent: r.field(obj, "", "agent", r.agent),
    item: r.field(obj, "", "item", r.text),
    paths: r.field(obj, "", "paths", r.list),
    intent: r.field(obj, "", "intent", r.text),
    mode: r.field(obj, "", "mode", r.text, false),
    implicit: r.field(obj, "", "implicit", r.flag, false),
    ttl: r.field(obj, "", "ttl_seconds", r.integerFrom(1), false),
  }));
}

export function parseClaimRenewal(input: Uint8Array) {
  return readRequest(input, (r, obj) => ({
    session: r.field(obj, "", "session_id", r.nonEmpty),
    ttl: r.field(obj, "", "ttl_seconds", r.integerFrom(1)),
  }));
}

export function parseClaimRelease(input: Uint8Array) {
  return readRequest(input, (r, obj) => ({
    session: r.field(obj, "", "session_id", r.nonEmpty),
    outcome: r.field(obj, "", "outcome", r.text, false),
  }));
}

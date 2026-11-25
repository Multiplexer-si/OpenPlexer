import { validID } from "./admin";
import type { JsonObject } from "./canonical";
import { bearer, cappedBody, forbidden, invalid, jsonText } from "./http";
import { readProject } from "./publication";
import { encodeRoomJSON, parseEventPost, validateEnvelope } from "./room-wire";
import type { Agent } from "./room-wire";
import { appendAudit, authenticate } from "./state";
import type { HubStorage, TransactionSQL } from "./storage";

const roomLimit = 64 << 10;
const replayPage = 100;
const base32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
const reserved = new Set(["claim", "release", "decision.confirm", "policy.override", "policy.override.revoked"]);

export function newRoomRecordID(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(26)), (byte) => base32[byte % 32]).join("");
}

export function memberAgent(agent: Agent, member: string): Agent {
  const { id, vendor, parent } = agent;
  if (!validID(id) || !validID(vendor) || vendor === "human" ||
      (parent !== undefined && (!validID(parent) || parent === id))) {
    throw invalid("valid agent id, vendor and optional parent required");
  }
  return { id: member + "/" + id, vendor, ...(parent && { parent: member + "/" + parent }) };
}

export function parseRoomRequest<T>(parse: (body: Uint8Array) => T, body: Uint8Array): T {
  try {
    return parse(body);
  } catch (error) {
    if (error instanceof SyntaxError) throw invalid(error.message);
    throw error;
  }
}

export async function roomEndpoint(
  request: Request, storage: HubStorage, room: string,
  run: (sql: TransactionSQL, input: { member: string; body: Uint8Array; repo: string }) => Response,
): Promise<Response> {
  const hash = bearer(request);
  const body = request.method === "POST" ? await cappedBody(request, roomLimit) : new Uint8Array();
  return storage.transaction((sql) => {
    const member = authenticate(sql, hash);
    const { repo } = readProject(sql, room);
    return run(sql, { member, body, repo });
  });
}

type EventHeader = { id: string; seq: number; mission: string; at: string; agent: Agent };
type EventRow = { event_json: string };

function envelopeHeader(event: EventHeader) {
  return {
    v: 1, type: "event", room: event.mission, seq: event.seq, event_id: event.id, ts: event.at,
    sender: { id: event.agent.id, kind: event.agent.vendor === "human" ? "dashboard" : "daemon" },
  };
}

function eventEnvelope(header: ReturnType<typeof envelopeHeader>, payload: string): string {
  return JSON.stringify(header).slice(0, -1) + ',"payload":' + payload + "}";
}

function storedEnvelope(row: EventRow): string {
  return eventEnvelope(envelopeHeader(JSON.parse(row.event_json)), row.event_json);
}

export function appendEvent(
  sql: TransactionSQL,
  input: { room: string; member: string; agent: Agent; type: string; body: JsonObject; at: string; key?: string },
) {
  const { room, member, agent, type, body, at, key } = input;
  const [{ room_seq: seq }] = sql.exec<{ room_seq: number }>(
    "UPDATE projects SET room_seq = room_seq + 1 WHERE project_id = ? RETURNING room_seq", room);
  const event = { id: newRoomRecordID(), seq, mission: room, type, agent, on_behalf_of: member, at, body };
  // The transaction rolls back if the generated payload or envelope is invalid.
  const header = envelopeHeader(event);
  validateEnvelope({ ...header, payload: event });
  const payload = encodeRoomJSON(event, 1);
  sql.exec("INSERT INTO room_events (room_id, seq, event_id, event_json, idempotency_member_id, idempotency_key)" +
    " VALUES (?, ?, ?, ?, ?, ?)", room, seq, event.id, payload, key ? member : null, key || null);
  return { event, envelope: eventEnvelope(header, payload) };
}

export function postEvent(request: Request, storage: HubStorage, room: string): Promise<Response> {
  return roomEndpoint(request, storage, room, (sql, { member, body }) => {
    const post = parseRoomRequest(parseEventPost, body);
    if (reserved.has(post.type)) throw forbidden();
    let agent: Agent;
    if (post.type === "human") {
      if (post.agent) throw forbidden();
      agent = { id: member, vendor: "human" };
    } else {
      if (!post.agent) throw invalid("agent is required");
      agent = memberAgent(post.agent, member);
    }
    const key = request.headers.get("Idempotency-Key") ?? "";
    if (new TextEncoder().encode(key).length > 128) throw invalid("idempotency key exceeds 128 bytes");
    if (key) {
      const [previous] = sql.exec<EventRow>("SELECT event_json FROM room_events" +
        " WHERE room_id = ? AND idempotency_member_id = ? AND idempotency_key = ?", room, member, key);
      if (previous) return jsonText(storedEnvelope(previous) + "\n");
    }
    delete post.body.flagged;
    const at = new Date().toISOString();
    const { event, envelope } = appendEvent(sql, { room, member, agent, type: post.type, body: post.body, at, key });
    if (event.type === "policy.denied") {
      appendAudit(sql, {
        action: "event.policy-denied", target: "event:" + event.id, actor_member_id: member,
        agent_session_id: agent.id, meta: { room_id: room, seq: event.seq, event_id: event.id },
      }, at);
    }
    return jsonText(envelope + "\n");
  });
}

function queryField(raw: string): string | undefined {
  try {
    return decodeURIComponent(raw.replaceAll("+", " "));
  } catch {
    // Go accepts escaped non-UTF-8 bytes. They cannot form the cursor name or a decimal cursor.
    return undefined;
  }
}

export function replayCursor(url: string): number {
  const query = url.split("#", 1)[0];
  const start = query.indexOf("?");
  if (start < 0) return 0;
  const cursors = [];
  for (const field of query.slice(start + 1).split("&")) {
    if (/;|%(?![0-9a-f]{2})/i.test(field)) throw invalid("invalid query string");
    const equal = field.indexOf("=");
    if (queryField(equal < 0 ? field : field.slice(0, equal)) === "last_seq") {
      cursors.push(equal < 0 ? "" : queryField(field.slice(equal + 1)));
    }
  }
  if (cursors.length === 0) return 0;
  if (cursors.length !== 1) throw invalid("last_seq must be supplied once");
  const [cursor] = cursors;
  if (!cursor || !/^[0-9]+$/.test(cursor) || !Number.isSafeInteger(Number(cursor))) {
    throw invalid("last_seq must be a nonnegative safe integer");
  }
  return Number(cursor);
}

export function replayEvents(request: Request, storage: HubStorage, room: string): Promise<Response> {
  return roomEndpoint(request, storage, room, (sql) => {
    const after = replayCursor(request.url);
    const page = sql.exec<EventRow>("SELECT event_json FROM room_events WHERE room_id = ? AND seq > ?" +
      " ORDER BY seq LIMIT ?", room, after, replayPage);
    return jsonText("[" + page.map(storedEnvelope).join(",") + "]\n");
  });
}

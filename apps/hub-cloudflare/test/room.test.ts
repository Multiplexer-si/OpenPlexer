import { Buffer } from "node:buffer";
import { reset, runInDurableObject } from "cloudflare:test";
import { env, exports } from "cloudflare:workers";
import { beforeEach, expect, test, vi } from "vitest";
import activeClaims from "../../../spec/room/v1/fixtures/active-claims.json?raw";
import { verifyChain } from "../src/audit";
import type { UnsealedRecord } from "../src/audit";
import { tokenHash } from "../src/http";
import { fetchHub } from "../src/index";
import { newToken } from "../src/state";
import { streamLimits } from "../src/stream";
import type { RoomRead } from "../src/stream";

beforeEach(reset);

const room = "/api/v1/rooms/demo";
const events = room + "/events";
const claims = room + "/claims";
const other = "/api/v1/rooms/other";
const agent = { id: "session", vendor: "codex", parent: "parent" };
const status = JSON.stringify({ type: "status", agent, body: { summary: "working" } });
const denial = JSON.stringify({
  type: "policy.denied", agent, body: { action: "write", path: "src/main.go", rule: "locked", flagged: true },
});
const claim = { agent, item: "work", paths: ["src/**"], intent: "edit" };
const renew = JSON.stringify({ session_id: agent.id, ttl_seconds: 2 });
const release = JSON.stringify({ session_id: agent.id, outcome: "done" });

function stub() {
  return env.TEAM_HUB.get(env.TEAM_HUB.idFromName("team:" + env.TEAM_ID));
}

function request(path: string, method: string, token?: string, body?: string | Uint8Array<ArrayBuffer>, key?: string) {
  const headers = new Headers();
  if (token !== undefined) headers.set("Authorization", "Bearer " + token);
  if (key !== undefined) headers.set("Idempotency-Key", key);
  return new Request("https://" + env.HUB_HOSTNAME + path, { method, headers, body });
}

function call(...args: Parameters<typeof request>) {
  return exports.default.fetch(request(...args));
}

async function check(response: Response, code = 200) {
  expect(response.status).toBe(code);
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  expect(response.headers.get("Content-Type")).toBe("application/json");
  if (code === 401) expect(response.headers.get("WWW-Authenticate")).toBe("Bearer");
  return response.json();
}

async function reject(response: Response, code: number, error: string) {
  expect(await check(response, code)).toEqual({ error });
}

function object(value: unknown): Record<string, unknown> {
  function isObject(input: unknown): input is Record<string, unknown> {
    return typeof input === "object" && input !== null && !Array.isArray(input);
  }
  if (!isObject(value)) throw new Error("Expected a JSON object");
  return value;
}

function array(value: unknown): unknown[] {
  if (!Array.isArray(value)) throw new Error("Expected a JSON array");
  return value;
}

function text(value: unknown): string {
  if (typeof value !== "string") throw new Error("Expected a JSON string");
  return value;
}

function eventReply(value: unknown) {
  const event = object(value);
  const { seq } = event;
  if (typeof seq !== "number") throw new Error("Expected a Room sequence");
  return { ...event, seq, event_id: text(event.event_id), ts: text(event.ts), payload: object(event.payload) };
}

function leaseReply(value: unknown) {
  const lease = object(value);
  return { ...lease, id: text(lease.id), expires_at: text(lease.expires_at), agent: object(lease.agent),
    intent: text(lease.intent), paths: lease.paths };
}

function claimReply(value: unknown) {
  const result = object(value);
  if (!Array.isArray(result.conflicts)) throw new Error("Expected an array of conflicts");
  return { ...result, granted: leaseReply(result.granted), conflicts: result.conflicts.map(leaseReply) };
}

async function claimAudit(response: Response, id: string) {
  const { entries } = object(await check(response));
  return array(entries).map(object).filter((entry) => entry.target === "claim:" + id);
}

async function setup() {
  const lead = newToken();
  const init = { member_id: "lead", token_hash: Buffer.from(tokenHash(lead)).toString("hex") };
  await check(await call("/api/v1/admin/init", "POST", env.BOOTSTRAP_TOKEN, JSON.stringify(init)));
  const member = text(object(await check(await call("/api/v1/admin/members", "POST", lead,
    JSON.stringify({ id: "member", role: "member" })))).token);
  for (const [id, repo] of [["demo", "team/demo"], ["other", "team/demo"], ["unrelated", "team/unrelated"]]) {
    await check(await call("/api/v1/admin/projects", "POST", lead, JSON.stringify({ id, repo })));
  }
  return { lead, member };
}

test("routes Room methods before auth and returns implicit HEAD for replay", async () => {
  const { member } = await setup();
  for (const [path, allow] of [[events, "GET, HEAD, POST"], [claims, "POST"],
    [claims + "/missing/renew", "POST"], [claims + "/missing/release", "POST"]]) {
    const res = await call(path, "DELETE");
    expect(res.headers.get("Allow")).toBe(allow);
    await reject(res, 405, "method not allowed");
  }
  const head = await call(events, "HEAD", member);
  expect(head.status).toBe(200);
  expect(head.headers.get("Cache-Control")).toBe("no-store");
  expect(await head.text()).toBe("");
  const stream = await call(room + "/stream", "POST");
  expect(stream.headers.get("Allow")).toBe("GET, HEAD");
  await reject(stream, 405, "method not allowed");
  await reject(await call(room + "/stream?last_seq=-1", "GET", member), 400,
    "invalid input: last_seq must be a nonnegative safe integer");
  await reject(await call("/api/v1/rooms/missing/events", "GET", member), 404, "not found");

  for (const [path, body] of [[events, status], [claims, JSON.stringify(claim)],
    [claims + "/missing/renew", renew], [claims + "/missing/release", release]]) {
    const oversized = body.padEnd(65537);
    await reject(await call(path, "POST", "short", oversized), 401, "invalid bearer token");
    await reject(await call(path, "POST", newToken(), oversized), 413, "request body is too large");
    await reject(await call(path, "POST", newToken(), body), 401, "invalid bearer token");
  }
});

async function openStream(member: string, options: {
  query?: string; signal?: AbortSignal; limits?: Partial<typeof streamLimits>;
  now?: () => number; delayPage?: (after: RoomRead["after"]) => Promise<void>;
} = {}) {
  const stream = new Request(request(room + "/stream" + (options.query ?? ""), "GET", member), { signal: options.signal });
  const response = await fetchHub(stream, {
    ...env,
    TEAM_HUB: {
      idFromName: (name) => env.TEAM_HUB.idFromName(name),
      get(id) {
        const hub = env.TEAM_HUB.get(id);
        return {
          fetch: (req) => hub.fetch(req),
          async readRoomPage(read) {
            const page = await hub.readRoomPage(read);
            await options.delayPage?.(read.after);
            return page;
          },
        };
      },
    },
  }, { ...streamLimits, pollMs: 1, ...options.limits }, options.now);
  expect(response.status).toBe(200);
  expect(response.headers.get("Content-Type")).toBe("application/x-ndjson");
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  const reader = response.body?.getReader();
  return {
    cancel: () => reader?.cancel(),
    async next() {
      if (!reader) return;
      const { done, value } = await reader.read();
      return done ? undefined : new TextDecoder().decode(value);
    },
  };
}

async function postStatus(member: string) {
  const response = await call(events, "POST", member, status);
  expect(response.status).toBe(200);
  return response.text();
}

async function corruptRoomEvent(seq: number) {
  await runInDurableObject(stub(), (_hub, ctx) => {
    ctx.storage.sql.exec("UPDATE room_events SET event_json = ? WHERE room_id = ? AND seq = ?", "{", "demo", seq);
  });
}

test("streams the backlog, includes a write during replay once, then streams a live write", async () => {
  const { member } = await setup();
  await postStatus(member);
  const second = await postStatus(member);
  const third = await postStatus(member);
  const live = await openStream(member, { query: "?last_seq=1" });
  try {
    expect(await live.next()).toBe(second);
    const duringReplay = await postStatus(member);
    expect(await live.next()).toBe(third);
    expect(await live.next()).toBe(duringReplay);
    expect(await live.next()).toBe("\n");
    expect(await live.next()).toBe("\n");
    const afterHeartbeat = await postStatus(member);
    expect(await live.next()).toBe(afterHeartbeat);
    expect(await live.next()).toBe("\n");
  } finally {
    await live.cancel();
  }
});

test("validates credentials, Room, and cursor before the stream starts", async () => {
  const { member } = await setup();
  for (const query of ["?last_seq=", "?last_seq=0&last_seq=1", "?last_seq=9007199254740992", "?bad=%ZZ"]) {
    await check(await call(room + "/stream" + query, "GET", member), 400);
  }
  await reject(await call(room + "/stream?last_seq=-1", "GET", newToken()), 401, "invalid bearer token");
  await reject(await call("/api/v1/rooms/missing/stream", "GET", member), 404, "not found");
});

test("returns a JSON 500 when the first page RPC rejects", async () => {
  const { member } = await setup();
  await postStatus(member);
  await corruptRoomEvent(1);
  await reject(await call(room + "/stream", "GET", member), 500, "hub state unavailable");
});

test("aborts the response body when a page RPC rejects after a heartbeat", async () => {
  const { member } = await setup();
  const live = await openStream(member);
  try {
    expect(await live.next()).toBe("\n");
    await postStatus(member);
    await corruptRoomEvent(1);
    await expect(live.next()).rejects.toThrow("hub state unavailable");
  } finally {
    await live.cancel()?.catch(() => {});
  }
});

test("rechecks membership and closes the stream on revocation", async () => {
  const { lead, member } = await setup();
  const live = await openStream(member);
  try {
    expect(await live.next()).toBe("\n");
    await check(await call("/api/v1/admin/members/member", "DELETE", lead));
    const frame = await live.next();
    expect(JSON.parse(frame ?? "null")).toMatchObject({
      v: 1, type: "error", room: "demo", sender: { id: "hub", kind: "server" },
      payload: { code: "unauthorized", message: "invalid bearer token" },
    });
    expect(await live.next()).toBeUndefined();
  } finally {
    await live.cancel();
  }
});

test("closes a pending poll on request cancellation", async () => {
  const { member } = await setup();
  const disconnect = new AbortController();
  const live = await openStream(member, { signal: disconnect.signal, limits: { pollMs: 60_000 } });
  try {
    expect(await live.next()).toBe("\n");
    const pending = live.next();
    disconnect.abort();
    expect(await pending).toBeUndefined();
  } finally {
    await live.cancel();
  }
});

test("closes with clean EOF at the call budget and resumes from the last sequence", async () => {
  const { member } = await setup();
  const first = await postStatus(member);
  const live = await openStream(member, { limits: { calls: 2 } });
  try {
    expect(await live.next()).toBe(first);
    const second = await postStatus(member);
    const third = await postStatus(member);
    expect(await live.next()).toBe(second);
    expect(await live.next()).toBe(third);
    expect(await live.next()).toBeUndefined();
    const fourth = await postStatus(member);
    const resumed = await openStream(member, { query: "?last_seq=3" });
    try {
      expect(await resumed.next()).toBe(fourth);
      expect(await resumed.next()).toBe("\n");
    } finally {
      await resumed.cancel();
    }
  } finally {
    await live.cancel();
  }
});

test("closes at the duration budget while caught up", async () => {
  const { member } = await setup();
  const live = await openStream(member, { limits: { durationMs: 500, pollMs: 60_000 } });
  try {
    expect(await live.next()).toBe("\n");
    expect(await live.next()).toBeUndefined();
  } finally {
    await live.cancel();
  }
});

test("drops the first page if its read finishes after the request deadline", async () => {
  const { member } = await setup();
  await postStatus(member);
  let now = 0;
  const live = await openStream(member, {
    limits: { calls: 1 },
    now: () => now,
    async delayPage() { now += 300_500; },
  });
  try {
    expect(await live.next()).toBeUndefined();
  } finally {
    await live.cancel();
  }
});

test.each([
  { elapsed: 299_999, sendsPage: true },
  { elapsed: 300_000, sendsPage: false },
  { elapsed: 300_500, sendsPage: false },
])("handles a later page that finishes $elapsed ms into the request", async ({ elapsed, sendsPage }) => {
  const { member } = await setup();
  const first = await postStatus(member);
  let now = 0;
  const live = await openStream(member, {
    limits: { calls: 2 },
    now: () => now,
    async delayPage(after) {
      if (after === 1) now += elapsed;
    },
  });
  try {
    expect(await live.next()).toBe(first);
    const second = await postStatus(member);
    const third = await postStatus(member);
    if (sendsPage) {
      expect(await live.next()).toBe(second);
      expect(await live.next()).toBe(third);
    }
    expect(await live.next()).toBeUndefined();
    if (!sendsPage) {
      const resumed = await openStream(member, { query: "?last_seq=1" });
      try {
        expect(await resumed.next()).toBe(second);
        expect(await resumed.next()).toBe(third);
        expect(await resumed.next()).toBe("\n");
      } finally {
        await resumed.cancel();
      }
    }
  } finally {
    await live.cancel();
  }
});

test("returns an empty stream while the first page read is still pending", async () => {
  const { member } = await setup();
  await postStatus(member);
  const hold = Promise.withResolvers<void>();
  let released = false;
  const fallback = setTimeout(hold.resolve, 1_500);
  try {
    const live = await openStream(member, {
      limits: { durationMs: 500, calls: 1 },
      async delayPage() {
        await hold.promise;
        released = true;
      },
    });
    try {
      expect(released).toBe(false);
      expect(await live.next()).toBeUndefined();
    } finally {
      await live.cancel();
    }
  } finally {
    clearTimeout(fallback);
    hold.resolve();
  }
});

test("closes when the response stops draining", async () => {
  const { member } = await setup();
  const first = await postStatus(member);
  await postStatus(member);
  const live = await openStream(member, { limits: { writeMs: 40 } });
  try {
    expect(await live.next()).toBe(first);
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(await live.next()).toBeUndefined();
  } finally {
    await live.cancel();
  }
});

test("posts, retries, and replays exact payloads with server-owned fields", async () => {
  const { lead, member } = await setup();
  const raw = '{"type":"status","agent":{"id":"session","vendor":"codex","parent":"parent"},' +
    '"body":{"summary":"working","flagged":true,"extra":9007199254740993},"on_behalf_of":"spoofed","seq":42}';
  const key = "k".repeat(128);
  const response = await call(events, "POST", member, raw, key);
  const text = await response.clone().text();
  expect(text).toContain('"extra":9007199254740993');
  const event = eventReply(await check(response));
  expect(event).toMatchObject({
    v: 1, type: "event", room: "demo", seq: 1, event_id: event.payload.id, ts: event.payload.at,
    sender: { id: "member/session", kind: "daemon" },
    payload: { seq: 1, mission: "demo", on_behalf_of: "member", agent: { ...agent, id: "member/session", parent: "member/parent" } },
  });
  expect(event.payload.body).not.toHaveProperty("flagged");
  expect(await (await call(events, "POST", member, status, key)).text()).toBe(text);
  expect(await (await call(events, "GET", member)).text()).toBe("[" + text.trimEnd() + "]\n");
  await reject(await call(events, "POST", member, status, key + "x"), 400, "invalid input: idempotency key exceeds 128 bytes");
  await reject(await call(events, "POST", member, status.replace("summary", "Summary"), key), 400, "invalid input: body.summary is required");
  await reject(await call(events, "POST", member, status.replace('"parent":"parent"', '"parent":"session"')), 400,
    "invalid input: valid agent id, vendor and optional parent required");
  await reject(await call(events, "POST", member, '{"type":"claim","body":{"item":"","paths":[],"intent":"","ttl_seconds":1}}'),
    403, "forbidden");
  await reject(await call(events, "POST", member, '{"type":"human","agent":{"id":"s","vendor":"c"},"body":{"text":"hello"}}'),
    403, "forbidden");
  const human = await check(await call(events, "POST", lead, '{"type":"human","body":{"text":"hello"}}', key));
  expect(human).toMatchObject({
    seq: 2, sender: { id: "lead", kind: "dashboard" }, payload: { agent: { id: "lead", vendor: "human" } },
  });
  const elsewhere = await check(await call(other + "/events", "POST", member, status, key));
  expect(elsewhere).toMatchObject({ seq: 1, room: "other" });
});

test("keeps a queued denial's decision time in replay, the stream, and the audit export", async () => {
  const { lead, member } = await setup();
  const now = Date.now();
  const decidedAt = new Date(now - 60 * 60_000).toISOString();
  const post = (time: string) => JSON.stringify({
    type: "policy.denied", agent, at: "spoofed",
    body: { action: "write", path: "src/main.go", rule: "locked", decided_at: time },
  });
  await reject(await call(events, "POST", member, post("yesterday"), "queued"), 400,
    "invalid input: body.decided_at must be a UTC timestamp with milliseconds");
  await reject(await call(events, "POST", member, post(new Date(now + 24 * 60 * 60_000).toISOString()), "queued"), 400,
    "invalid input: body.decided_at must be no more than 30 seconds after at");

  const event = eventReply(await check(await call(events, "POST", member, post(decidedAt), "queued")));
  expect(event).toMatchObject({ seq: 1, payload: { at: event.ts, body: { decided_at: decidedAt } } });
  expect(Date.parse(event.ts)).toBeGreaterThanOrEqual(now);
  expect(await check(await call(events, "GET", member))).toEqual([event]);
  const stream = await openStream(member);
  try {
    expect(JSON.parse(text(await stream.next()))).toEqual(event);
  } finally {
    await stream.cancel();
  }
  const { entries } = object(await check(await call("/api/v1/audit/export", "GET", lead)));
  expect(array(entries).map(object).filter((entry) => entry.action === "event.policy-denied")).toMatchObject([{
    target: "event:" + event.event_id, created_at: event.ts,
    meta: { room_id: "demo", seq: 1, event_id: event.event_id, decided_at: decidedAt },
  }]);
});

test("allocates concurrent sequences and audits each denial once", async () => {
  const { lead, member } = await setup();
  const publication = call("/api/v1/projects/demo/policy", "PUT", lead, JSON.stringify({
    version: 1, project_id: "demo", repos: ["team/demo"], areas: [], members: {}, roles: {},
  }));
  const post = async (i: number) =>
    eventReply(await check(await call(events, "POST", member, i % 2 === 0 ? denial : status, "race-" + i)));
  const [posts, retried] = await Promise.all([Promise.all(Array.from({ length: 32 }, (_, i) => post(i))), post(0)]);
  await check(await publication);
  expect(retried).toEqual(posts[0]);
  const ordered = posts.toSorted((left, right) => left.seq - right.seq);
  expect(ordered.map((event) => event.seq)).toEqual(Array.from({ length: 32 }, (_, i) => i + 1));
  expect(new Set(posts.map((event) => event.event_id)).size).toBe(32);
  expect(await check(await call(events, "GET", member))).toEqual(ordered);

  await runInDurableObject(stub(), (_hub, ctx) => {
    const sql = ctx.storage.sql;
    const rows = sql.exec<Omit<UnsealedRecord, "meta" | "flagged"> & {
      meta_json: string; flagged: number; prev_hash: string; hash: string;
    }>("SELECT CAST(seq AS TEXT) AS id, * FROM audit_records ORDER BY seq").toArray();
    const chain = rows.map(({ meta_json, flagged, ...row }) => ({ ...row, meta: JSON.parse(meta_json), flagged: !!flagged }));
    expect(verifyChain(chain)).toEqual({ ok: true, entries: 22 });
    expect(sql.exec("SELECT audit_seq, audit_tip FROM team").one()).toEqual({ audit_seq: 22, audit_tip: chain.at(-1)?.hash });
    const denials = chain.filter((row) => row.action === "event.policy-denied");
    expect(denials).toHaveLength(16);
    expect(denials.map((record) => ({
      actor: record.actor_member_id, session: record.agent_session_id, time: record.created_at,
      meta: record.meta, target: record.target, flagged: record.flagged,
    }))).toEqual(ordered.filter((event) => event.payload.type === "policy.denied").map((event) => ({
      actor: "member", session: "member/session", time: event.ts,
      meta: { room_id: "demo", seq: event.seq, event_id: event.event_id }, target: "event:" + event.event_id, flagged: false,
    })));
  });
});

test("sweeps expired leases by claim ID and honors a renewal past the original deadline", async () => {
  const { member } = await setup();
  await runInDurableObject(stub(), async (hub) => {
    const start = Date.parse("2026-01-15T12:00:00.250Z");
    const time = vi.spyOn(Date, "now").mockReturnValue(start);
    try {
      const leases = [];
      for (let i = 0; i < 3; i++) {
        const response = await hub.fetch(request(claims, "POST", member, JSON.stringify({ ...claim, ttl_seconds: 1 })));
        leases.push(claimReply(await check(response)).granted);
      }
      const renewPath = claims + "/" + leases[0].id + "/renew";
      const renewed = leaseReply(await check(await hub.fetch(request(renewPath, "POST", member, renew))));
      expect(renewed.expires_at).toBe("2026-01-15T12:00:02.25Z");
      time.mockReturnValue(start + 1_000);
      await reject(await hub.fetch(request(claims + "/missing/renew", "POST", member, renew)), 404, "not found");
      const ids = leases.slice(1).map((lease) => lease.id).sort();
      const releases = await check(await hub.fetch(request(events + "?last_seq=3", "GET", member)));
      expect(releases).toMatchObject(ids.map((id, i) => ({
        seq: i + 4, payload: { type: "release", body: { claim_id: id, outcome: "expired" } },
      })));
      const releasePath = claims + "/" + leases[0].id + "/release";
      expect(await check(await hub.fetch(request(releasePath, "POST", member, '{"session_id":"session"}')))).toEqual(renewed);
      const last = await check(await hub.fetch(request(events + "?last_seq=5", "GET", member)));
      expect(last).toMatchObject([{ seq: 6, payload: { type: "release", body: { claim_id: leases[0].id } } }]);
    } finally {
      time.mockRestore();
    }
  });
});

test("pages after the cursor and rejects malformed query strings", async () => {
  const { member } = await setup();
  expect(await check(await call(events, "GET", member))).toEqual([]);
  for (let i = 0; i < 101; i++) await check(await call(events, "POST", member, status));
  const page = await check(await call(events, "GET", member));
  if (!Array.isArray(page)) throw new Error("Expected a replay page");
  expect(page.map((event) => eventReply(event).seq)).toEqual(Array.from({ length: 100 }, (_, i) => i + 1));
  expect(await check(await call(events + "?extra=%FF&%6cast_seq=0100", "GET", member))).toMatchObject([{ seq: 101 }]);
  for (const cursor of ["101", "9007199254740991"]) {
    expect(await check(await call(events + "?last_seq=" + cursor, "GET", member))).toEqual([]);
  }
  for (const query of ["?other=%ZZ", "?other=1;last_seq=0", "?last_seq=0&last_seq=%Z"]) {
    await reject(await call(events + query, "GET", member), 400, "invalid input: invalid query string");
  }
  await reject(await call(events + "?last_seq=0&%6cast_seq=0", "GET", member), 400, "invalid input: last_seq must be supplied once");
  for (const value of ["", "-0", "1.0", "%2B1", "9007199254740992", "%FF"]) {
    await reject(await call(events + "?last_seq=" + value, "GET", member), 400,
      "invalid input: last_seq must be a nonnegative safe integer");
  }
});

test("rolls back the denial, sequence, retry key, and sealed record when advancing the audit tip fails", async () => {
  const { member } = await setup();
  await runInDurableObject(stub(), async (hub, ctx) => {
    const sql = ctx.storage.sql;
    const tip = sql.exec("SELECT audit_seq, audit_tip FROM team").one();
    sql.exec("ALTER TABLE team ADD COLUMN keep_audit_tip INTEGER CHECK (audit_seq = 5)");
    await reject(await hub.fetch(request(events, "POST", member, denial, "retry")), 500, "hub state unavailable");
    expect(sql.exec("SELECT event_id FROM room_events").toArray()).toEqual([]);
    expect(sql.exec("SELECT room_seq FROM projects WHERE project_id = 'demo'").one()).toEqual({ room_seq: 0 });
    expect(sql.exec("SELECT COUNT(*) AS count FROM audit_records").one()).toEqual({ count: 5 });
    expect(sql.exec("SELECT audit_seq, audit_tip FROM team").one()).toEqual(tip);
    sql.exec("ALTER TABLE team DROP COLUMN keep_audit_tip");
  });
  expect(await check(await call(events, "POST", member, denial, "retry"))).toMatchObject({ seq: 1 });
});

test("creates advisory conflicts across Rooms, renews only for the holder, and releases once", async () => {
  const { lead, member } = await setup();
  const original = claimReply(await check(await call(claims, "POST", member, JSON.stringify({ ...claim, mode: "exclusive" }))));
  expect(original).toMatchObject({
    granted: { room: "demo", on_behalf_of: "member", mode: "exclusive", agent: { id: "member/session", parent: "member/parent" } },
    event: { seq: 1, payload: { type: "claim", body: { claim_id: original.granted.id, ttl_seconds: 3600, implicit: false } } },
    conflicts: [],
  });
  const implicit = JSON.stringify({ ...claim, paths: ["src/main.go"], implicit: true });
  const sameSession = await check(await call(other + "/claims", "POST", member, implicit));
  expect(sameSession).toMatchObject({ conflicts: [], event: { payload: { body: { ttl_seconds: 600, implicit: true } } } });
  const differentRepo = claimReply(await check(await call("/api/v1/rooms/unrelated/claims", "POST", lead, implicit)));
  expect(differentRepo.conflicts).toEqual([]);
  const overlap = claimReply(await check(await call(other + "/claims", "POST", lead, implicit)));
  expect(overlap.conflicts).toEqual([original.granted]);

  const id = "/" + original.granted.id;
  const created = eventReply(object(original).event);
  await reject(await call(claims + id + "/renew", "POST", lead, renew), 404, "not found");
  await reject(await call(other + "/claims" + id + "/renew", "POST", member, renew), 404, "not found");
  await reject(await call(claims + id + "/release", "POST", member, '{"session_id":"parent","outcome":"done"}'),
    404, "not found");
  await reject(await call(claims + id + "/release", "POST", member, release.replace("done", "expired")), 400,
    "invalid input: expired is reserved for the hub");
  await reject(await call(claims + id + "/renew", "POST", member, '{"session_id":"session","ttl_seconds":9223372037}'), 400,
    "invalid input: ttl_seconds must be between 1 and 9223372036");
  const renewed = leaseReply(await check(await call(claims + id + "/renew", "POST", member,
    '{"session_id":"session","ttl_seconds":600}')));
  expect(Date.parse(renewed.expires_at)).toBeLessThan(Date.parse(original.granted.expires_at));
  expect(renewed).toEqual({ ...original.granted, expires_at: renewed.expires_at });
  expect(await check(await call(events + "?last_seq=1", "GET", member))).toEqual([]);
  expect(await check(await call(claims + id + "/release", "POST", member, release))).toEqual(renewed);
  const page = array(await check(await call(events + "?last_seq=1", "GET", member)));
  expect(page).toMatchObject([{
    seq: 2, payload: { type: "release", body: { claim_id: original.granted.id, outcome: "done" } },
  }]);
  const released = eventReply(page[0]);
  await reject(await call(claims + id + "/release", "POST", member, release), 404, "not found");
  expect(await claimAudit(await call("/api/v1/audit/export", "GET", lead), original.granted.id)).toMatchObject([
    { action: "claim.released", actor_member_id: "member", agent_session_id: "member/session",
      created_at: released.ts, meta: { room_id: "demo", seq: released.seq, event_id: released.event_id } },
    { action: "claim.renewed", actor_member_id: "member", agent_session_id: "member/session",
      meta: { room_id: "demo", expires_at: renewed.expires_at } },
    { action: "claim.created", actor_member_id: "member", agent_session_id: "member/session",
      created_at: created.ts, meta: { room_id: "demo", seq: created.seq, event_id: created.event_id } },
  ]);
});

type ExpiryCase = {
  name: string;
  input: { now: string; events: { at: string; body: { item: string; paths: string[]; intent: string; ttl_seconds: number } }[] };
  expected: { claims: { expires_at: string; intent: string; paths: string[] }[] };
};
const expiryCases: { cases: ExpiryCase[] } = JSON.parse(activeClaims);
test.each(expiryCases.cases.filter(({ name }) => [
  "a claim is active before it expires", "a claim is not active at the time that it expires",
  "a claim is not active after it expires", "the expiry has milliseconds",
].includes(name)))("$name", async ({ input, expected }) => {
  const { member, lead } = await setup();
  const [fixture] = input.events;
  await runInDurableObject(stub(), async (hub) => {
    const time = vi.spyOn(Date, "now").mockReturnValue(Date.parse(fixture.at));
    try {
      const post = JSON.stringify({ agent, mode: "exclusive", ...fixture.body });
      const { granted } = claimReply(await check(await hub.fetch(request(claims, "POST", member, post))));
      time.mockReturnValue(Date.parse(input.now));
      expect(await check(await hub.fetch(request(events + "?last_seq=1", "GET", member)))).toEqual([]);
      const unavailable = other + "/claims/missing/renew";
      await reject(await hub.fetch(request(unavailable, "POST", lead, "{}")), 400,
        "invalid input: session_id is required; ttl_seconds is required");
      const competing = claimReply(await check(await hub.fetch(request(other + "/claims", "POST", lead, post))));
      expect(competing.conflicts.map((lease) => ({
        expires_at: new Date(lease.expires_at).toISOString(), intent: lease.intent, paths: lease.paths,
      }))).toEqual(expected.claims.map(({ expires_at, intent, paths }) => ({ expires_at, intent, paths })));
      const page = array(await check(await hub.fetch(request(events + "?last_seq=1", "GET", member))));
      const records = await claimAudit(await hub.fetch(request("/api/v1/audit/export", "GET", lead)), granted.id);
      if (expected.claims.length) {
        expect(page).toEqual([]);
        expect(records).toMatchObject([{ action: "claim.created" }]);
      } else {
        expect(page).toMatchObject([{
          seq: 2, ts: input.now, payload: { type: "release", agent: granted.agent, on_behalf_of: "member",
            body: { claim_id: granted.id, outcome: "expired" } },
        }]);
        const expired = eventReply(page[0]);
        expect(records).toMatchObject([
          { action: "claim.expired", actor_member_id: null, agent_session_id: "member/session",
            created_at: expired.ts, meta: { room_id: "demo", seq: expired.seq, event_id: expired.event_id } },
          { action: "claim.created" },
        ]);
      }
    } finally {
      time.mockRestore();
    }
  });
});

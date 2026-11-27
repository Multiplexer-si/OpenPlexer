import { Buffer } from "node:buffer";
import { reset, runInDurableObject } from "cloudflare:test";
import { env, exports } from "cloudflare:workers";
import { beforeEach, expect, test, vi } from "vitest";
import { tokenHash } from "../src/http";
import { fetchHub } from "../src/index";
import { readMembers } from "../src/members";
import { newToken } from "../src/state";
import { HubStorage } from "../src/storage";
import { streamLimits } from "../src/stream";

beforeEach(reset);

const members = "/api/v1/admin/members";
const policy = "/api/v1/projects/shop/policy";
const stream = "/api/v1/rooms/shop/stream";
const empty = {
  member_id: "ana", hub_role: "member", project_id: "shop",
  last_policy_fetch_at: null, policy_version: null, last_room_connected_at: null,
};

function request(path: string, method: string, token?: string, body?: string) {
  return new Request("https://" + env.HUB_HOSTNAME + path, {
    method, headers: token ? { Authorization: "Bearer " + token } : {}, body,
  });
}

function call(...args: Parameters<typeof request>) {
  return exports.default.fetch(request(...args));
}

async function addMember(lead: string): Promise<string> {
  const res = await call(members, "POST", lead, JSON.stringify({ id: "ana", role: "member" }));
  expect(res.status).toBe(200);
  const reply: unknown = await res.json();
  if (!reply || typeof reply !== "object" || !("token" in reply) || typeof reply.token !== "string") {
    throw new Error("missing member credential");
  }
  return reply.token;
}

async function setup() {
  const lead = newToken();
  const init = await call("/api/v1/admin/init", "POST", env.BOOTSTRAP_TOKEN,
    JSON.stringify({ member_id: "lee", token_hash: Buffer.from(tokenHash(lead)).toString("hex") }));
  expect(init.status).toBe(200);
  const member = await addMember(lead);
  for (const id of ["shop", "tools"]) {
    const project = await call("/api/v1/admin/projects", "POST", lead, JSON.stringify({ id, repo: "api" }));
    expect(project.status).toBe(200);
  }
  return { lead, member };
}

function stub() {
  return env.TEAM_HUB.get(env.TEAM_HUB.idFromName("team:" + env.TEAM_ID));
}

async function checkedPage(response: Response, status = 200): Promise<unknown> {
  expect(response.status).toBe(status);
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  if (status === 401) expect(response.headers.get("WWW-Authenticate")).toBe("Bearer");
  return response.json();
}

function rows(page: unknown): unknown[] {
  if (!page || typeof page !== "object" || !("members" in page) || !Array.isArray(page.members)) {
    throw new Error("invalid member page");
  }
  return page.members;
}

test("lead lists nullable rows with HEAD, auth, filters, and canonical cursors", async () => {
  const { lead, member } = await setup();
  expect(await checkedPage(await call(members + "?limit=1", "GET", lead))).toEqual({
    members: [{ ...empty, project_id: null }], next_cursor: Buffer.from('["ana",null]').toString("base64url"),
  });
  const filtered = members + "?project_id=shop";
  expect(await checkedPage(await call(filtered, "GET", lead))).toEqual({
    members: [empty, { ...empty, member_id: "lee", hub_role: "lead" }], next_cursor: null,
  });
  const head = await call(filtered, "HEAD", lead);
  expect(head.status).toBe(200);
  expect(head.headers.get("Cache-Control")).toBe("no-store");
  expect(await head.text()).toBe("");
  expect(await checkedPage(await call(members, "GET"), 401)).toEqual({ error: "invalid bearer token" });
  expect(await checkedPage(await call(members, "GET", member), 403)).toEqual({ error: "forbidden" });
  expect(await checkedPage(await call(members + "?project_id=missing", "GET", lead), 404)).toEqual({ error: "not found" });
  for (const query of ["?after=invalid!", "?limit=0", "?project_id=shop&project_id=tools", "?bad=1"]) {
    await checkedPage(await call(members + query, "GET", lead), 400);
  }
  const wrong = await call(members, "PUT", lead);
  expect(wrong.headers.get("Allow")).toBe("GET, HEAD, POST");
  expect(await checkedPage(wrong, 405)).toEqual({ error: "method not allowed" });
});

test("rejects bodies on member reads and caps their size", async () => {
  const { lead } = await setup();
  await runInDurableObject(stub(), async (_hub, ctx) => {
    const storage = new HubStorage(ctx.storage);
    // Request construction only permits a body with methods such as POST.
    await expect(readMembers(request(members, "POST", lead, "{}"), storage)).rejects.toMatchObject({ status: 400 });
    await expect(readMembers(request(members, "POST", lead, " ".repeat(65_537)), storage)).rejects.toMatchObject({ status: 413 });
  });
});

test("records the selected policy and initial Room page under the roster transaction", async () => {
  const { lead, member } = await setup();
  await runInDurableObject(stub(), async (hub, ctx) => {
    const start = Date.now();
    const clock = vi.spyOn(Date, "now").mockReturnValue(start);
    const at = () => new Date(clock()).toISOString();
    const send = (...args: Parameters<typeof request>) => hub.fetch(request(...args));
    async function publish(version: number) {
      expect((await send(policy, "PUT", lead, JSON.stringify({
        project_id: "shop", repos: ["api"], version, members: {}, areas: [], roles: {},
      }))).status).toBe(200);
    }
    async function assertMember(want: Record<string, string | number | null>) {
      const page = await checkedPage(await send(members + "?project_id=shop&limit=1", "GET", lead));
      expect(rows(page)).toEqual([{ ...empty, ...want }]);
    }
    const auditSeq = () => ctx.storage.sql.exec("SELECT audit_seq FROM team WHERE singleton = 1").one();
    try {
      await publish(1);
      expect((await send(policy + ".bundle", "GET", member)).status).toBe(200);
      const first = at();
      await assertMember({ last_policy_fetch_at: first, policy_version: 1 });
      clock.mockReturnValue(start + 1_000);
      await publish(2);
      // Skew the stored display version without changing the signed payload.
      ctx.storage.sql.exec("UPDATE policies SET version = 9 WHERE project_id = 'shop'");
      const before = auditSeq();

      const head = await send(policy + ".bundle", "HEAD", member);
      expect(head.status).toBe(200);
      expect(head.headers.get("OpenPlexer-Policy-Version")).toBe("2");
      expect(await head.text()).toBe("");
      const routes: [string, number, string | undefined][] = [
        [policy + ".bundle", 401, undefined],
        ["/api/v1/projects/tools/policy.bundle", 404, member],
        ["/api/v1/rooms/shop/events", 200, member],
      ];
      for (const [path, expected, token] of routes) {
        expect((await send(path, "GET", token)).status).toBe(expected);
      }
      const edge = (...args: Parameters<typeof request>) => fetchHub(request(...args), {
        ...env, TEAM_HUB: {
          idFromName: (name) => env.TEAM_HUB.idFromName(name),
          get: () => ({ fetch: (req) => hub.fetch(req), readRoomPage: (read) => hub.readRoomPage(read) }),
        },
      }, { ...streamLimits, pollMs: 1 });
      expect((await edge(stream + "?last_seq=no", "GET", member)).status).toBe(400);
      expect((await edge("/api/v1/rooms/missing/stream", "GET", member)).status).toBe(404);
      expect((await edge(stream, "GET", newToken())).status).toBe(401);
      const streamHead = await edge(stream, "HEAD", member);
      expect(streamHead.status).toBe(200);
      expect(await streamHead.text()).toBe("");
      await assertMember({ last_policy_fetch_at: first, policy_version: 1 });

      expect((await send(policy + ".bundle", "GET", member)).status).toBe(200);
      const fetched = at();
      await assertMember({ last_policy_fetch_at: fetched, policy_version: 2 });
      clock.mockReturnValue(start + 2_000);
      const live = await edge(stream, "GET", member);
      expect(live.status).toBe(200);
      const connected = at();
      const reader = live.body?.getReader();
      if (!reader) throw new Error("missing live Room response");
      try {
        expect(new TextDecoder().decode((await reader.read()).value)).toBe("\n");
        clock.mockReturnValue(start + 3_000);
        expect(new TextDecoder().decode((await reader.read()).value)).toBe("\n");
        await assertMember({ last_policy_fetch_at: fetched, policy_version: 2, last_room_connected_at: connected });
      } finally {
        await reader.cancel();
      }
      expect(auditSeq()).toEqual(before);

      const thirtyDays = 30 * 24 * 60 * 60 * 1_000;
      clock.mockReturnValue(start + 1_000 + thirtyDays - 1);
      await assertMember({ last_policy_fetch_at: fetched, policy_version: 2, last_room_connected_at: connected });
      clock.mockReturnValue(start + 1_000 + thirtyDays);
      await assertMember({ last_room_connected_at: connected });
      await hub.alarm();
      clock.mockReturnValue(start + 2_000 + thirtyDays);
      await assertMember({});
      await hub.alarm();
      expect(ctx.storage.sql.exec("SELECT member_id FROM member_observations").toArray()).toEqual([]);
    } finally {
      clock.mockRestore();
    }
  });
});

test("revocation deletes observations and delayed requests cannot restore a re-added member's state", async () => {
  const { lead, member } = await setup();
  const res = await call(stream, "GET", member);
  expect(res.status).toBe(200);
  await res.body?.cancel();
  expect((await call(members + "/ana", "DELETE", lead)).status).toBe(200);
  const page = await checkedPage(await call(members, "GET", lead));
  expect(rows(page)).toEqual([{ ...empty, member_id: "lee", hub_role: "lead", project_id: null }]);
  const replacement = await addMember(lead);
  expect((await call(stream, "GET", member)).status).toBe(401);
  expect((await call(policy + ".bundle", "GET", member)).status).toBe(401);
  await runInDurableObject(stub(), async (hub) => {
    expect(await hub.readRoomPage({ room: "shop", hash: tokenHash(member), after: "https://hub.invalid/", observe: true }))
      .toMatchObject({ ok: false, status: 401 });
  });
  const fresh = await checkedPage(await call(members + "?project_id=shop&limit=1", "GET", lead));
  expect(rows(fresh)).toEqual([empty]);
  const live = await call(stream, "GET", replacement);
  expect(live.status).toBe(200);
  await live.body?.cancel();
  const observed = await checkedPage(await call(members + "?project_id=shop&limit=1", "GET", lead));
  expect(rows(observed)).toMatchObject([{ member_id: "ana", last_room_connected_at: expect.any(String) }]);
});

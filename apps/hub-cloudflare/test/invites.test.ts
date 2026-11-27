import { Buffer } from "node:buffer";
import { reset, runInDurableObject } from "cloudflare:test";
import { env, exports } from "cloudflare:workers";
import { beforeEach, expect, test } from "vitest";
import { verifyChain, verifyCheckpoint } from "../src/audit";
import { isJsonObject, parseJSON } from "../src/canonical";
import { HubError, tokenHash } from "../src/http";
import { handleInvite } from "../src/invites";
import { newToken } from "../src/state";
import { HubStorage, migrate } from "../src/storage";

beforeEach(reset);

const invites = "/api/v1/admin/invites";
const redeem = "/api/v1/invites/redeem";
const audit = "/api/v1/audit/export";
const day = 86_400_000;
const hexHash = (token: string) => Buffer.from(tokenHash(token)).toString("hex");

function stub() {
  return env.TEAM_HUB.get(env.TEAM_HUB.idFromName(`team:${env.TEAM_ID}`));
}

function incoming(path: string, method: string, token?: string, body?: string) {
  return new Request(`https://${env.HUB_HOSTNAME}${path}`, {
    method, headers: token ? { Authorization: "Bearer " + token } : {}, body,
  });
}

function call(...args: Parameters<typeof incoming>) {
  return exports.default.fetch(incoming(...args));
}

async function setup() {
  const lead = newToken();
  const init = await call("/api/v1/admin/init", "POST", env.BOOTSTRAP_TOKEN,
    JSON.stringify({ member_id: "lee", token_hash: hexHash(lead) }));
  expect(init.status).toBe(200);
  const project = await call("/api/v1/admin/projects", "POST", lead, '{"id":"demo","repo":"team/demo"}');
  expect(project.status).toBe(200);
  return lead;
}

async function readCreated(response: Response) {
  expect(response.status).toBe(201);
  const value: unknown = await response.json();
  if (!value || typeof value !== "object" || !("code" in value) || typeof value.code !== "string" ||
      !("invite" in value) || !value.invite || typeof value.invite !== "object" ||
      !("id" in value.invite) || typeof value.invite.id !== "string") throw new Error("Invalid creation response");
  return { code: value.code, invite: value.invite, id: value.invite.id };
}

const requestBody = (code: string, hash: string) => JSON.stringify({ code, token_hash: hash });

test("races claimants, returns one receipt, and checks membership identity on retry", async () => {
  const lead = await setup();
  const create = '{"project_id":"demo","member_id":"ana","require_approval":true}';
  const first = await readCreated(await call(invites, "POST", lead, create));
  const reserved = await call("/api/v1/admin/members", "POST", lead, '{"id":"ana","role":"member"}');
  expect(reserved.status).toBe(409);
  const bearers = [newToken(), newToken()];
  const hashes = bearers.map(hexHash);
  const bodies = hashes.map((hash) => requestBody(first.code, hash));
  const competing = await Promise.all(bodies.map((body) => call(redeem, "POST", undefined, body)));
  expect(competing.map((response) => response.status).sort()).toEqual([202, 409]);
  const winner = competing.findIndex((response) => response.status === 202);
  expect(competing[winner].headers.get("Retry-After")).toBe("5");
  expect(await competing[winner].json()).toEqual({ state: "pending_approval", invite_id: first.id });

  const approval = invites + "/" + first.id + "/approve";
  expect((await call(approval, "POST", lead)).status).toBe(200);
  const replies = await Promise.all([0, 1].map(() => call(redeem, "POST", undefined, bodies[winner])));
  for (const response of replies) {
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ state: "redeemed", project_id: "demo", repo: "team/demo",
      member_id: "ana", hub_role: "member" });
  }
  expect((await call("/api/v1/projects/demo", "GET", bearers[winner])).status).toBe(200);
  expect((await call(redeem, "POST", undefined, bodies[1 - winner])).status).toBe(409);
  const approved = await call(approval, "POST", lead);
  expect(approved.status).toBe(200);
  expect(await approved.json()).toMatchObject({ invite: { state: "redeemed" } });

  const exported = await call(audit, "GET", lead);
  expect(exported.status).toBe(200);
  const text = await exported.text();
  const keys = await (await call("/.well-known/audit-jwks.json", "GET")).text();
  const checkpoint = exported.headers.get("OpenPlexer-Audit-Checkpoint");
  if (!checkpoint) throw new Error("Audit checkpoint missing");
  expect([lead, first.code, ...hashes, ...bearers].some((secret) => text.includes(secret))).toBe(false);
  const snapshot = parseJSON(text);
  if (!isJsonObject(snapshot) || !Array.isArray(snapshot.entries) || !snapshot.entries.every(isJsonObject)) {
    throw new Error("Invalid audit export");
  }
  const { entries } = snapshot;
  expect(await verifyCheckpoint(checkpoint, keys)).toMatchObject({ entries: entries.length, tip_hash: entries[0].hash });
  expect(verifyChain(entries.toReversed())).toEqual({ ok: true, entries: entries.length });
  expect(entries.slice(0, 5).map((entry) => entry.action)).toEqual([
    "member.added", "invite.redeemed", "invite.approved", "invite.requested", "invite.created",
  ]);
  expect(entries[0]).toMatchObject({ actor_member_id: null, meta: { role: "member", invite_id: first.id } });
  expect(entries[1]).toMatchObject({ actor_member_id: "ana", target: "invite:" + first.id,
    meta: { project_id: "demo", member_id: "ana", hub_role: "member" } });
  expect(entries[2].actor_member_id).toBe("lee");

  expect((await call("/api/v1/admin/members/ana", "DELETE", lead)).status).toBe(200);
  expect((await call(redeem, "POST", undefined, bodies[winner])).status).toBe(409);
  const next = await readCreated(await call(invites, "POST", lead, '{"project_id":"demo","member_id":"ana"}'));
  expect((await call(redeem, "POST", undefined, requestBody(next.code, hashes[winner]))).status).toBe(200);
  expect((await call(redeem, "POST", undefined, bodies[winner])).status).toBe(409);
});

test("checks expiry, receipt and retention boundaries with a controlled clock", async () => {
  const lead = await setup();
  await runInDurableObject(stub(), async (_instance, ctx) => {
    const storage = new HubStorage(ctx.storage);
    const start = Date.parse("2026-11-25T10:00:00Z");
    let now = start;
    const clock = () => now;
    function create(id: string, requireApproval = false) {
      return handleInvite(incoming(invites, "POST", lead,
        JSON.stringify({ project_id: "demo", member_id: id, expires_in_seconds: 60, require_approval: requireApproval })),
      storage, { kind: "collection" }, clock);
    }
    const a = await readCreated(await create("ana", true));
    const b = await readCreated(await create("bo"));
    const hashA = hexHash(newToken());
    const hashB = hexHash(newToken());
    const consume = async (code: string, hash: string) => {
      try {
        return await handleInvite(incoming(redeem, "POST", undefined, requestBody(code, hash)),
          storage, { kind: "redeem" }, clock);
      } catch (error) {
        if (error instanceof HubError) return error.response();
        throw error;
      }
    };
    now = start + 60_000 - 1;
    expect((await consume(a.code, hashA)).status).toBe(202);
    expect((await consume(b.code, hashB)).status).toBe(200);
    const committed = now;
    now = start + 60_000;
    expect((await consume(a.code, hashA)).status).toBe(410);
    expect((await consume(b.code, hashB)).status).toBe(200);
    now = committed + day - 1;
    expect((await consume(b.code, hashB)).status).toBe(200);
    now = committed + day;
    expect((await consume(b.code, hashB)).status).toBe(409);
    now = start + 60_000 + 30 * day - 1;
    expect((await consume(a.code, hashA)).status).toBe(410);
    now++;
    expect((await consume(a.code, hashA)).status).toBe(404);
    const page = await handleInvite(incoming(invites, "GET", lead), storage, { kind: "collection" }, clock);
    expect(await page.json()).toEqual({ invites: [], next_cursor: null });
    expect(ctx.storage.sql.exec("SELECT count(*) AS count FROM invites").one().count).toBe(0);
  });
});

test("revoke is idempotent and invite routes enforce strict bodies and lead authorization", async () => {
  const lead = await setup();
  const body = '{"project_id":"demo","member_id":"ana"}';
  expect((await call(invites, "POST", undefined, body)).status).toBe(401);
  expect((await call(invites, "POST", lead, '{"project_id":"demo","member_id":"a","\u006dember_id":"b"}')).status).toBe(400);
  expect((await call(invites, "POST", lead, '{"project_id":"demo","member_id":"a","extra":null}')).status).toBe(400);
  expect((await call(invites, "POST", lead, body.padEnd((64 << 10) + 1))).status).toBe(413);
  const first = await readCreated(await call(invites, "POST", lead, body.padEnd(64 << 10)));
  const path = invites + "/" + first.id;
  expect((await call(path, "DELETE", lead, " ")).status).toBe(400);
  expect((await call(path + "/approve", "POST", lead)).status).toBe(409);
  for (const query of ["?limit=0", "?limit=101", "?limit=1&%6cimit=1", "?after=", "?after=%zz", "?extra=1"]) {
    expect((await call(invites + query, "GET", lead)).status).toBe(400);
  }
  for (let i = 0; i < 2; i++) {
    const response = await call(path, "DELETE", lead);
    expect(response.status).toBe(200);
    expect(await response.json()).toMatchObject({ invite: { state: "revoked" } });
  }
  expect((await call(redeem, "POST", undefined, requestBody(first.code, hexHash(newToken())))).status).toBe(410);
  const head = await call(invites, "HEAD", lead);
  expect(head.status).toBe(200);
  expect(await head.text()).toBe("");
});

test("backfills enrollment IDs once on existing rows", async () => {
  await runInDurableObject(stub(), (_instance, ctx) => {
    const { sql } = ctx.storage;
    sql.exec("DROP TABLE invites");
    sql.exec("ALTER TABLE members DROP COLUMN enrollment_id");
    sql.exec("DELETE FROM schema_migrations WHERE version = 2");
    sql.exec("INSERT INTO members (member_id, role, token_hash) VALUES ('existing', 'lead', randomblob(32))");
    const storage = new HubStorage(ctx.storage);
    expect(migrate(storage)).toBe(2);
    const value = sql.exec("SELECT enrollment_id FROM members WHERE member_id = 'existing'").one().enrollment_id;
    expect(value).toMatch(/^[0-9a-f]{32}$/);
    expect(migrate(storage)).toBe(2);
    expect(sql.exec("SELECT enrollment_id FROM members WHERE member_id = 'existing'").one().enrollment_id).toBe(value);
  });
});

test("counts only live invites toward the team limit", async () => {
  const lead = await setup();
  await runInDurableObject(stub(), async (_instance, ctx) => {
    const storage = new HubStorage(ctx.storage);
    const start = Date.parse("2026-11-25T10:00:00Z");
    let now = start;
    ctx.storage.sql.exec(`WITH RECURSIVE ids(n) AS (VALUES(1) UNION ALL SELECT n + 1 FROM ids WHERE n < 1024)
      INSERT INTO invites (id, project_id, member_id, hub_role, require_approval, state, created_at_ms, expires_at_ms, code_hash)
      SELECT printf('%032x', n), 'demo', 'member-' || n, 'member', 0, 'issued', ?, ?, randomblob(32) FROM ids`,
    start, start + 60_000);
    const create = () => handleInvite(incoming(invites, "POST", lead, '{"project_id":"demo","member_id":"ana"}'),
      storage, { kind: "collection" }, () => now);
    await expect(create()).rejects.toMatchObject({ status: 409 });
    now = start + 60_000;
    await readCreated(await create());
    const page = await handleInvite(incoming(invites, "GET", lead), storage, { kind: "collection" }, () => now);
    const value = await page.json<{ invites: { id: string }[]; next_cursor: string }>();
    expect(value.invites.length).toBe(100);
    expect(value.next_cursor).toBe(value.invites[99].id);
  });
});

test("rolls back redemption when the member audit record cannot commit", async () => {
  const lead = await setup();
  const invite = await readCreated(await call(invites, "POST", lead, '{"project_id":"demo","member_id":"ana"}'));
  const token = newToken();
  const body = requestBody(invite.code, hexHash(token));
  await runInDurableObject(stub(), (_instance, ctx) => {
    ctx.storage.sql.exec("ALTER TABLE team ADD COLUMN reject_member_audit INTEGER CHECK (audit_seq < 5)");
  });

  expect((await call(redeem, "POST", undefined, body)).status).toBe(500);
  expect((await call("/api/v1/projects/demo", "GET", token)).status).toBe(401);
  const page = await call(invites, "GET", lead);
  expect(await page.json()).toMatchObject({ invites: [{ id: invite.id, state: "issued" }] });

  await runInDurableObject(stub(), (_instance, ctx) => {
    expect(ctx.storage.sql.exec("SELECT audit_seq FROM team").one().audit_seq).toBe(3);
    ctx.storage.sql.exec("ALTER TABLE team DROP COLUMN reject_member_audit");
  });
  expect((await call(redeem, "POST", undefined, body)).status).toBe(200);
});

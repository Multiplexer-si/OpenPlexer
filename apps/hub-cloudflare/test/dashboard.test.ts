import { Buffer } from "node:buffer";
import { reset, runInDurableObject } from "cloudflare:test";
import { env, exports } from "cloudflare:workers";
import { beforeEach, expect, test, vi } from "vitest";
import { verifyChain, verifyCheckpoint } from "../src/audit";
import { tokenHash } from "../src/http";
import { newToken } from "../src/state";

beforeEach(reset);

const origin = () => "https://" + env.HUB_HOSTNAME;
const api = "/admin/api";
const call = (path: string, init?: RequestInit) => exports.default.fetch(new Request(origin() + path, init));
const authorization = (token: string) => ({ Authorization: "Bearer " + token });
const hash = (token: string) => Buffer.from(tokenHash(token)).toString("hex");

function database<T>(run: (sql: SqlStorage) => T) {
  const stub = env.TEAM_HUB.get(env.TEAM_HUB.idFromName("team:" + env.TEAM_ID));
  return runInDurableObject(stub, (_hub, ctx) => run(ctx.storage.sql));
}

function signIn(token: string, from: string | null = origin(), contentType = "application/json", body = "{}") {
  const headers = new Headers({ ...authorization(token), "Content-Type": contentType });
  if (from !== null) headers.set("Origin", from);
  return call(api + "/sign-in", { method: "POST", headers, body });
}

async function setup() {
  const lead = newToken();
  const response = await call("/api/v1/admin/init", {
    method: "POST", headers: authorization(env.BOOTSTRAP_TOKEN ?? ""),
    body: JSON.stringify({ member_id: "lee", token_hash: hash(lead) }),
  });
  expect(response.status).toBe(200);
  return lead;
}

async function signInAs(lead: string) {
  const response = await signIn(lead);
  expect(response.status).toBe(200);
  const cookie = response.headers.get("Set-Cookie")?.split(";", 1)[0];
  if (!cookie) throw new Error("missing dashboard cookie");
  const data: { csrf: string } = await response.json();
  const headers = { Cookie: cookie };
  const write = (path: string, method: string, body?: string) =>
    call(api + path, { method, headers: { ...headers, Origin: origin(), "X-CSRF-Token": data.csrf }, body });
  return { headers, write };
}

function expectSecurity(response: Response) {
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  expect(response.headers.get("Referrer-Policy")).toBe("no-referrer");
  expect(response.headers.get("Access-Control-Allow-Origin")).toBeNull();
  expect(response.headers.get("Content-Security-Policy")).toContain("frame-ancestors 'none'");
  expect(response.headers.get("Content-Security-Policy")).toContain("script-src 'self'");
  expect(response.headers.get("Content-Security-Policy")).toContain("style-src 'self'");
  expect(response.headers.get("Content-Security-Policy")).not.toContain("unsafe-");
}

async function expectPrivate(response: Response, lead: string) {
  expectSecurity(response);
  const exposed = (await response.text()).includes(lead) || [...response.headers].some(([, value]) => value.includes(lead));
  expect(exposed).toBe(false);
}

test("serves a secure shell, static assets, and error and HEAD responses", async () => {
  const lead = await setup();
  for (const [path, status] of [
    ["/admin/", 200], ["/admin/dashboard.js", 200], ["/admin/dashboard.css", 200],
    ["/admin/missing", 404], [api + "/session", 401], ["/admin/%bo", 400],
  ] as const) {
    const response = await call(path);
    expect(response.status).toBe(status);
    await expectPrivate(response, lead);
  }
  const head = await call("/admin/", { method: "HEAD" });
  expect(head.status).toBe(200);
  expect(head.body).toBeNull();
  expectSecurity(head);
});

test("sign-in accepts only the configured origin and strict JSON from an active lead", async () => {
  const lead = await setup();
  const member = newToken();
  await database((sql) => sql.exec("INSERT INTO members VALUES ('ana', 'member', ?, 'ana-first')", tokenHash(member)).toArray());
  for (const [from, status] of [[null, 403], ["null", 403], ["https://foreign.example", 403],
    [origin() + ":8443", 403], [origin().replace("https:", "http:"), 403], [origin() + "/", 403]] as const) {
    const response = await signIn(lead, from);
    expect(response.status).toBe(status);
    await expectPrivate(response, lead);
  }
  expect((await signIn(lead, origin(), "text/plain")).status).toBe(415);
  expect((await signIn(lead, origin(), "application/json", '{"token":""}')).status).toBe(400);
  expect((await signIn(member)).status).toBe(403);
  expect((await signIn(newToken())).status).toBe(401);
  const response = await signIn(lead);
  expect(response.status).toBe(200);
  const cookie = response.headers.get("Set-Cookie") ?? "";
  for (const pattern of [/^__Host-openplexer_admin=[A-Za-z0-9_-]{43};/, /; Secure\b/, /; HttpOnly\b/,
    /; SameSite=Strict\b/, /; Path=\/(?:;|$)/, /; Max-Age=28800\b/]) {
    expect(pattern.test(cookie)).toBe(true);
  }
  expect(/\bDomain=/i.test(cookie)).toBe(false);
  await expectPrivate(response, lead);
  const rows = await database((sql) => sql.exec(
    "SELECT length(session_digest) AS size, member_id, enrollment_id = 'ana-first' AS member, expires_at_ms FROM admin_sessions",
  ).toArray());
  expect(rows).toMatchObject([{ size: 32, member_id: "lee", member: 0 }]);
  expect(Number(rows[0].expires_at_ms)).toBeGreaterThan(Date.now() + 28_790_000);
});

test("requires exact origin and the session's CSRF on every write, including sign-out", async () => {
  const lead = await setup();
  const first = await signInAs(lead);
  const second = await signInAs(lead);
  const check = await call(api + "/session", { headers: first.headers });
  expect(check.status).toBe(200);
  const { csrf }: { csrf: string } = await check.json();
  const alternate: { csrf: string } = await (await call(api + "/session", { headers: second.headers })).json();
  for (const path of ["/sign-out", "/projects"]) {
    const cases: Record<string, string>[] = [{}, { Origin: origin() }, { Origin: origin(), "X-CSRF-Token": alternate.csrf },
      { Origin: "https://foreign.example", "X-CSRF-Token": csrf }, { "X-CSRF-Token": csrf }];
    for (const header of cases) {
      const response = await call(api + path, { method: "POST", headers: { ...first.headers, ...header } });
      expect(response.status).toBe(403);
      await expectPrivate(response, lead);
    }
  }
  for (const method of ["GET", "HEAD", "OPTIONS"]) {
    const crossOrigin = await call(api + "/session", { method, headers: { ...first.headers, Origin: "https://foreign.example" } });
    expect(crossOrigin.status).toBe(403);
    expectSecurity(crossOrigin);
  }
  expect((await call(api + "/session", { headers: { ...first.headers, "Sec-Fetch-Site": "cross-site" } })).status).toBe(403);
  expect((await call("/api/v1/admin/members", { headers: first.headers })).status).toBe(401);
  const response = await first.write("/sign-out", "POST");
  expect(response.status).toBe(204);
  expect(response.headers.get("Set-Cookie")?.includes("Max-Age=0")).toBe(true);
  expectSecurity(response);
  expect((await call(api + "/session", { headers: first.headers })).status).toBe(401);
  expect((await call(api + "/session", { headers: second.headers })).status).toBe(200);
});

test("expires sessions and refuses a lead after demotion, replacement, or removal and re-enrollment", async () => {
  const lead = await setup();
  const session = await signInAs(lead);
  const check = () => call(api + "/session", { headers: session.headers });
  await database((sql) => sql.exec("UPDATE members SET role = 'member' WHERE member_id = 'lee'").toArray());
  expect((await check()).status).toBe(403);
  await database((sql) => {
    sql.exec("UPDATE members SET role = 'lead'");
    sql.exec("UPDATE admin_sessions SET expires_at_ms = ?", Date.now());
  });
  expect((await check()).status).toBe(401);

  const beforeReplace = await signInAs(lead);
  const nextLead = newToken();
  await database((sql) => sql.exec("UPDATE members SET token_hash = ?, enrollment_id = 'replacement' WHERE member_id = 'lee'",
    tokenHash(nextLead)).toArray());
  expect((await call(api + "/session", { headers: beforeReplace.headers })).status).toBe(401);
  expect((await signIn(lead)).status).toBe(401);

  const replacement = await signInAs(nextLead);
  const owner = newToken();
  await database((sql) => sql.exec("INSERT INTO members VALUES ('owner', 'lead', ?, 'owner-first')", tokenHash(owner)).toArray());
  expect((await call("/api/v1/admin/members/lee", { method: "DELETE", headers: authorization(owner) })).status).toBe(200);
  expect((await signIn(nextLead)).status).toBe(401);
  expect((await call(api + "/session", { headers: replacement.headers })).status).toBe(401);
  await database((sql) => sql.exec("INSERT INTO members VALUES ('lee', 'lead', ?, 'lee-second')", tokenHash(nextLead)).toArray());
  expect((await call(api + "/session", { headers: replacement.headers })).status).toBe(401);
  expect((await signIn(nextLead)).status).toBe(200);
});

test("rejects removal after request authorization and before the transaction can write", async () => {
  const lead = await setup();
  const session = await signInAs(lead);
  const { csrf }: { csrf: string } = await (await call(api + "/session", { headers: session.headers })).json();
  const owner = newToken();
  await database((sql) => sql.exec("INSERT INTO members VALUES ('owner', 'lead', ?, 'owner-first')", tokenHash(owner)).toArray());
  const stream = new TransformStream<Uint8Array, Uint8Array>();
  const writer = stream.writable.getWriter();
  const pending = call(api + "/projects", {
    method: "POST", headers: { ...session.headers, Origin: origin(), "X-CSRF-Token": csrf }, body: stream.readable,
  });
  await writer.write(new TextEncoder().encode('{"id":"demo",'));
  expect((await call("/api/v1/admin/members/lee", { method: "DELETE", headers: authorization(owner) })).status).toBe(200);
  await writer.write(new TextEncoder().encode('"repo":"team/demo"}'));
  await writer.close();
  expect((await pending).status).toBe(401);
  const projects = await database((sql) => sql.exec("SELECT project_id FROM projects").toArray());
  expect(projects).toEqual([]);
});

test("runs onboarding writes with the lead actor, then shows the laptop's policy fetch and a signed audit export", async () => {
  const lead = await setup();
  const { headers, write } = await signInAs(lead);
  const read = (path: string, method = "GET") => call(api + path, { headers, method });
  expect((await write("/projects", "POST", '{"id":"demo","repo":"team/demo","unknown":true}')).status).toBe(400);
  expect((await write("/projects", "POST", '{"id":"demo","repo":"team/demo"}')).status).toBe(200);
  expect(await (await read("/projects")).json()).toMatchObject({ projects: [{ id: "demo", repo: "team/demo" }] });

  const member = newToken();
  const created = await write("/invites", "POST", '{"project_id":"demo","member_id":"ana","require_approval":true,"expires_in_seconds":900}');
  expect(created.status).toBe(201);
  const issued: { invite: { id: string }; link: string } = await created.json();
  const [, , code] = new URL(issued.link).pathname.split("/");
  expect(new RegExp("^" + origin() + "/join/[A-Za-z0-9_-]{43}#fp=[0-9a-f]{64}$").test(issued.link)).toBe(true);
  const redeem = () => call("/api/v1/invites/redeem", { method: "POST", body: JSON.stringify({ code, token_hash: hash(member) }) });
  expect((await redeem()).status).toBe(202);
  expect((await write("/invites/" + issued.invite.id + "/approve", "POST")).status).toBe(200);
  expect((await redeem()).status).toBe(200);
  const other = await write("/invites", "POST", '{"project_id":"demo","member_id":"other"}');
  const invite: { invite: { id: string } } = await other.json();
  expect((await write("/invites/" + invite.invite.id, "DELETE")).status).toBe(200);

  const policy = { project_id: "demo", repos: ["team/demo"], version: 1,
    areas: [{ name: "app", patterns: ["src/**"] }], roles: { dev: { read: [], write: ["app"], exec: [] } }, members: {} };
  const policyPath = "/projects/demo/policy";
  const draft = JSON.stringify({ ...policy, extra: true });
  expect((await write(policyPath, "PUT", draft)).status).toBe(400);
  expect((await write(policyPath + "/preview", "POST", draft)).status).toBe(400);
  const preview = await write(policyPath + "/preview", "POST", JSON.stringify(policy));
  expect(preview.status).toBe(200);
  expect(await preview.json()).toMatchObject({ version: 1, roles: [{ role: "dev", write: ["app"] }] });
  expect((await write(policyPath, "PUT", JSON.stringify(policy))).status).toBe(200);
  expect((await write(policyPath, "PUT", JSON.stringify(policy))).status).toBe(409);
  expect((await write(policyPath, "PUT", JSON.stringify({ ...policy, version: 2, members: { ana: "dev" } }))).status).toBe(200);
  expect(await (await read(policyPath)).json()).toMatchObject({ version: 2, signature: expect.any(String) });
  expect(await (await read(policyPath + "/history")).json()).toMatchObject({ versions: [{ version: 2 }, { version: 1 }] });
  const served = await call("/api/v1/projects/demo/policy.bundle", { headers: authorization(member) });
  expect(served.headers.get("OpenPlexer-Policy-Version")).toBe("2");
  expect(await (await read("/members?project_id=demo")).json()).toMatchObject({
    members: [{ member_id: "ana", policy_version: 2, last_policy_fetch_at: expect.any(String) },
      { member_id: "lee", policy_version: null, last_policy_fetch_at: null }],
  });

  expect((await write("/members/ana", "DELETE")).status).toBe(200);
  const page = await read("/audit?limit=2");
  expect(await page.json()).toMatchObject({ entries: [{ action: "member.removed" }, { action: "policy.updated" }], next_cursor: "11" });
  const exported = await read("/audit/export");
  const checkpoint = exported.headers.get("OpenPlexer-Audit-Checkpoint");
  if (!checkpoint) throw new Error("missing signed checkpoint");
  const data: { entries: Parameters<typeof verifyChain>[0] } = await exported.json();
  expect(verifyChain(data.entries)).toMatchObject({ ok: true });
  const keys = await call("/.well-known/audit-jwks.json");
  expect((await verifyCheckpoint(checkpoint, await keys.text())).entries).toBe(data.entries.length);
  expect(data.entries.filter((entry) => ["project.added", "invite.created", "invite.approved", "invite.revoked", "policy.updated",
    "member.removed"].includes(String(entry.action))).every((entry) => entry.actor_member_id === "lee")).toBe(true);
  expect(JSON.stringify(data).includes(code)).toBe(false);
});

test("GET and HEAD never change roster, audit, observations, invites or sessions", async () => {
  const lead = await setup();
  const { headers, write } = await signInAs(lead);
  expect((await write("/projects", "POST", '{"id":"demo","repo":"team/demo"}')).status).toBe(200);
  expect((await write("/invites", "POST", '{"member_id":"other","project_id":"demo"}')).status).toBe(201);
  await database((sql) => sql.exec("UPDATE invites SET expires_at_ms = 0").toArray());
  const counts = () => database((sql) => sql.exec(`SELECT (SELECT count(*) FROM invites) AS invites,
    (SELECT count(*) FROM audit_records) AS audit, (SELECT count(*) FROM member_observations) AS observations,
    (SELECT count(*) FROM admin_sessions) AS sessions, (SELECT count(*) FROM members) AS members`).toArray());
  const before = await counts();
  for (const method of ["GET", "HEAD"]) {
    for (const path of ["/sign-in", "/sign-out", "/session", "/projects", "/invites", "/members",
      "/projects/demo/policy", "/projects/demo/policy/history", "/rooms/demo/events", "/audit", "/audit/export"]) {
      const response = await call(api + path, { method, headers });
      expectSecurity(response);
      if (method === "HEAD") expect(response.body).toBeNull();
    }
  }
  expect(await counts()).toEqual(before);
  expect(await (await call(api + "/invites", { headers })).json()).toEqual({ invites: [], next_cursor: null });
});

test("rechecks lead role in the policy transaction after signing", async () => {
  const lead = await setup();
  const { headers, write } = await signInAs(lead);
  expect((await write("/projects", "POST", '{"id":"demo","repo":"demo"}')).status).toBe(200);
  const { csrf }: { csrf: string } = await (await call(api + "/session", { headers })).json();
  const request = new Request(origin() + api + "/projects/demo/policy", {
    method: "PUT", headers: { ...headers, Origin: origin(), "X-CSRF-Token": csrf },
    body: '{"version":1,"project_id":"demo","repos":["demo"],"areas":[],"roles":{},"members":{}}',
  });
  const stub = env.TEAM_HUB.get(env.TEAM_HUB.idFromName("team:" + env.TEAM_ID));
  await runInDurableObject(stub, async (hub, ctx) => {
    const sign = crypto.subtle.sign.bind(crypto.subtle);
    const spy = vi.spyOn(crypto.subtle, "sign").mockImplementationOnce((...args) => {
      ctx.storage.sql.exec("DELETE FROM members WHERE member_id = 'lee'");
      return sign(...args);
    });
    try {
      expect((await hub.fetch(request)).status).toBe(401);
      expect(spy).toHaveBeenCalledTimes(1);
      expect(ctx.storage.sql.exec("SELECT version FROM policies").toArray()).toEqual([]);
      expect(ctx.storage.sql.exec("SELECT action FROM audit_records WHERE action = 'policy.updated'").toArray()).toEqual([]);
    } finally {
      spy.mockRestore();
    }
  });
});

test("reads bounded Room pages with denial events, and rejects ambiguous cursors", async () => {
  const lead = await setup();
  const { headers, write } = await signInAs(lead);
  expect((await write("/projects", "POST", '{"id":"demo","repo":"demo"}')).status).toBe(200);
  const path = "/rooms/demo/events";
  const untrusted = "<img src=x onerror=alert(1)>";
  const post = (type: string, body: object, agent?: object) => call("/api/v1" + path, {
    method: "POST", headers: authorization(lead), body: JSON.stringify({ type, body, agent }),
  });
  expect((await post("human", { text: untrusted })).status).toBe(200);
  expect((await post("policy.denied", { action: "write", path: "src/app", rule: untrusted },
    { id: "agent", vendor: "codex" })).status).toBe(200);

  const read = (query: string) => call(api + path + query, { headers });
  const first = await read("?limit=1");
  expect(first.status).toBe(200);
  expect(await first.json()).toMatchObject({ events: [{ seq: 1, payload: { body: { text: untrusted } } }],
    last_seq: 1, next_cursor: 1 });
  const second = await read("?last_seq=1&limit=1");
  expect(await second.json()).toMatchObject({ events: [{ seq: 2, payload: { type: "policy.denied", body: { rule: untrusted } } }],
    last_seq: 2, next_cursor: null });
  for (const query of ["?last_seq=1&last_seq=2", "?Last_seq=1", "?limit=101"]) {
    expect((await read(query)).status).toBe(400);
  }
});

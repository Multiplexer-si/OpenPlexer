import { Buffer } from "node:buffer";
import { reset, runInDurableObject } from "cloudflare:test";
import { env, exports } from "cloudflare:workers";
import { beforeEach, expect, test, vi } from "vitest";
import { verifyChain, verifyCheckpoint } from "../src/audit";
import { hasDuplicateMembers, isJsonObject, parseJSON } from "../src/canonical";
import type { JsonObject } from "../src/canonical";
import { tokenHash } from "../src/http";
import { verifyPolicyBundle } from "../src/policy";
import { appendAudit, newSigningKey, newToken } from "../src/state";
import { HubStorage } from "../src/storage";
import { loadWrappingKey, unwrapPKCS8, wrapPKCS8 } from "../src/wrap";

beforeEach(reset);

const admin = "/api/v1/admin";
const project = "/api/v1/projects/demo";
const keys = "/.well-known/jwks.json";
const auditKeys = "/.well-known/audit-jwks.json";
const auditPath = "/api/v1/audit/export";
const checkpointHeader = "OpenPlexer-Audit-Checkpoint";
const repo = "team/demo";
const hexHash = (token: string) => Buffer.from(tokenHash(token)).toString("hex");

function stub() {
  return env.TEAM_HUB.get(env.TEAM_HUB.idFromName(`team:${env.TEAM_ID}`));
}

function incoming(path: string, method = "GET", token?: string, body?: string | Uint8Array<ArrayBuffer>) {
  return new Request(`https://${env.HUB_HOSTNAME}${path}`, {
    method, headers: token ? { Authorization: "Bearer " + token } : {}, body,
  });
}

function call(...args: Parameters<typeof incoming>) {
  return exports.default.fetch(incoming(...args));
}

async function check(response: Response, status: number, body?: unknown) {
  expect(response.status).toBe(status);
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  expect(response.headers.get("Content-Type")).toBe("application/json");
  if (status === 401) expect(response.headers.get("WWW-Authenticate")).toBe("Bearer");
  const value: unknown = await response.json();
  if (body !== undefined) expect(value).toEqual(body);
  return value;
}

async function error(response: Response, status: number, message: string) {
  await check(response, status, { error: message });
}

async function setup() {
  const lead = newToken();
  const bootstrap = env.BOOTSTRAP_TOKEN;
  if (!bootstrap) throw new Error("Missing test bootstrap binding");
  const init = JSON.stringify({ member_id: "lee", token_hash: hexHash(lead) });
  await check(await call(admin + "/init", "POST", bootstrap, init), 200, {});
  return { lead, bootstrap, init };
}

async function addMember(lead: string, id: string, role = "member") {
  const response = await call(admin + "/members", "POST", lead, JSON.stringify({ id, role }));
  const result = await check(response, 200);
  if (!result || typeof result !== "object" || !("token" in result) || typeof result.token !== "string") {
    throw new Error("Missing member token");
  }
  expect(result.token).toMatch(/^[A-Za-z0-9_-]{43}$/);
  return result.token;
}

function addProject(lead: string, id = "demo", projectRepo = repo) {
  return call(admin + "/projects", "POST", lead, JSON.stringify({ id, repo: projectRepo }));
}

function bundle(version: number, extra: Record<string, unknown> = {}) {
  return JSON.stringify({ project_id: "demo", repos: [repo], version, members: {}, areas: [], roles: {}, ...extra });
}

async function signed(response: Response, version: number) {
  const value = await check(response, 200);
  expect(value).toMatchObject({ version, bundle: { version, project_id: "demo", repos: [repo] } });
  if (!value || typeof value !== "object" || !("signature" in value) || typeof value.signature !== "string") {
    throw new Error("Missing policy signature");
  }
  return value.signature;
}

function checkpoint(response: Response): string {
  const token = response.headers.get(checkpointHeader);
  if (!token) throw new Error("Missing audit checkpoint");
  return token;
}

async function readAudit(response: Response) {
  expect(response.status).toBe(200);
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  expect(response.headers.get("Content-Type")).toBe("application/json");
  const text = await response.text();
  const duplicates = new WeakSet<JsonObject>();
  const body = parseJSON(text, { duplicates });
  if (!isJsonObject(body) || body.next_cursor !== null || !Array.isArray(body.entries)
      || !body.entries.every(isJsonObject) || hasDuplicateMembers(body, duplicates)) {
    throw new Error("Invalid audit export");
  }
  expect(verifyChain(body.entries.toReversed())).toEqual({ ok: true, entries: body.entries.length });
  return { entries: body.entries, token: checkpoint(response), text };
}

async function seedAudit(count: number) {
  const actor = "m".repeat(128);
  const target = "project:" + "p".repeat(128);
  for (let i = 0; i < count; i += 5000) {
    await runInDurableObject(stub(), (_hub, ctx) => {
      new HubStorage(ctx.storage).transaction((sql) => {
        for (let j = i; j < Math.min(count, i + 5000); j++) {
          appendAudit(sql, { action: "project.added", target, actor_member_id: actor });
        }
      });
    });
  }
}

test("serves a public audit JWKS and requires a current lead for export", async () => {
  const { lead } = await setup();
  const member = await addMember(lead, "ana");
  for (const [token, status, message] of [
    [undefined, 401, "invalid bearer token"],
    [newToken(), 401, "invalid bearer token"],
    [member, 403, "forbidden"],
  ] as const) {
    const response = await call(auditPath, "GET", token);
    expect(response.headers.get(checkpointHeader)).toBeNull();
    await error(response, status, message);
  }
  await check(await call(admin + "/members/ana", "DELETE", lead), 200, {});
  await error(await call(auditPath, "GET", member), 401, "invalid bearer token");

  const { entries, token, text } = await readAudit(await call(auditPath + "?cursor=1&limit=1", "GET", lead));
  expect(entries.map(({ id, action, result, meta, actor_member_id }) => [id, action, result, meta, actor_member_id])).toEqual([
    ["3", "member.removed", "removed", {}, "lee"],
    ["2", "member.added", "added", { role: "member" }, "lee"],
    ["1", "member.added", "added", { role: "lead" }, null],
  ]);
  expect(entries.every((entry) => entry.workspace_id === entries[0].workspace_id)).toBe(true);
  for (const credential of [lead, member]) {
    expect(text).not.toContain(credential);
    expect(text).not.toContain(hexHash(credential));
  }
  const jwksResponse = await call(auditKeys, "GET", "irrelevant");
  expect(jwksResponse.status).toBe(200);
  expect(jwksResponse.headers.get("Content-Type")).toBe("application/json");
  expect(jwksResponse.headers.get("Cache-Control")).toBe("no-store");
  const jwks = await jwksResponse.text();
  expect(await verifyCheckpoint(token, jwks)).toMatchObject({
    workspace_id: entries[0].workspace_id, entries: entries.length, tip_hash: entries[0].hash,
  });
  await expect(verifyCheckpoint(token, await (await call(keys)).text())).rejects.toThrow();

  const head = await call(auditPath, "HEAD", lead);
  expect(head.status).toBe(200);
  expect(await head.text()).toBe("");
  expect((await verifyCheckpoint(checkpoint(head), jwks)).tip_hash).toBe(entries[0].hash);
  expect(await (await call(auditKeys, "HEAD")).text()).toBe("");
  for (const path of [auditKeys, auditPath]) {
    const wrong = await call(path, "POST");
    expect(wrong.headers.get("Allow")).toBe("GET, HEAD");
    await error(wrong, 405, "method not allowed");
  }
});

test("exports the captured tip while a new record is appended", async () => {
  const { lead } = await setup();
  await seedAudit(300);
  const response = await call(auditPath, "GET", lead);
  await check(await addProject(lead), 200, {});
  const { entries, token } = await readAudit(response);
  expect(entries.map(({ id }) => id)).toEqual(Array.from({ length: 301 }, (_, i) => String(301 - i)));
  const jwks = await (await call(auditKeys)).text();
  expect(await verifyCheckpoint(token, jwks)).toMatchObject({ entries: 301, tip_hash: entries[0].hash });

  const newer = await readAudit(await call(auditPath, "GET", lead));
  expect(newer.entries[0]).toMatchObject({ id: "302", target: "project:demo", prev_hash: entries[0].hash });
  expect(newer.entries.slice(1)).toEqual(entries);
  expect(await verifyCheckpoint(newer.token, jwks)).toMatchObject({ entries: 302, tip_hash: newer.entries[0].hash });
});

test.each(["policy", "audit"] as const)("stages and activates a %s key once and keeps the previous key public", async (purpose) => {
  const { lead } = await setup();
  const member = await addMember(lead, "ana");
  await check(await addProject(lead), 200, {});
  const oldPolicy = await signed(await call(project + "/policy", "PUT", lead, bundle(1)), 1);
  const oldAudit = await readAudit(await call(auditPath, "GET", lead));
  const path = admin + "/keys/" + purpose;
  const jwksPath = purpose === "policy" ? keys : auditKeys;
  const oldKeys = parseJSON(await (await call(jwksPath)).text());
  if (!isJsonObject(oldKeys) || !Array.isArray(oldKeys.keys) || !isJsonObject(oldKeys.keys[0])
      || typeof oldKeys.keys[0].kid !== "string") throw new Error("Missing old key");
  const oldKid = oldKeys.keys[0].kid;

  const stage = (token: string) => call(path, "POST", token);
  const activate = (kid: string, token = lead) => call(path + "/active", "PUT", token, JSON.stringify({ kid }));
  await error(await stage(member), 403, "forbidden");
  await error(await activate(oldKid, member), 403, "forbidden");
  await error(await call(path, "POST"), 401, "invalid bearer token");
  await error(await call(admin + "/keys/unknown", "POST", lead), 404, "not found");
  await error(await call(admin + "/keys/unknown/active", "PUT", lead, "{}"), 404, "not found");
  await error(await activate("missing"), 404, "not found");
  expect((await call(path)).headers.get("Allow")).toBe("POST");
  expect((await call(path + "/active")).headers.get("Allow")).toBe("PUT");
  await error(await call(path + "/active", "PUT", lead, '{"Kid":"wrong-case"}'), 400, "invalid input: activation needs a kid");
  await error(await call(path + "/active", "PUT", lead, '{"kid":"one","kid":"two"}'), 400, "invalid input: body must be a JSON object");
  await error(await call(path, "POST", lead, " ".repeat((64 << 10) + 1)), 413, "request body is too large");

  const [one, two] = await Promise.all([stage(lead), stage(lead)]);
  expect(one.status).toBe(200);
  const staged = parseJSON(await one.text());
  await check(two, 200, staged);
  await check(await stage(lead), 200, staged);
  if (!isJsonObject(staged) || typeof staged.kid !== "string") {
    throw new Error("Missing staged key");
  }
  expect(staged).not.toHaveProperty("d");
  expect(staged.kid).not.toBe(oldKid);
  const stagedKeys = await (await call(jwksPath)).text();
  expect(parseJSON(stagedKeys)).toEqual({ keys: expect.arrayContaining([oldKeys.keys[0], staged]) });
  expect(await signed(await call(project + "/policy", "PUT", lead, bundle(1)), 1)).toBe(oldPolicy);
  const stagedAudit = await readAudit(await call(auditPath, "GET", lead));
  expect(await verifyCheckpoint(stagedAudit.token, await (await call(auditKeys)).text()))
    .toMatchObject({ entries: oldAudit.entries.length + 1, tip_hash: stagedAudit.entries[0].hash });

  await check(await activate(staged.kid), 200, {});
  await check(await activate(staged.kid), 200, {});
  await error(await activate(oldKid), 409, "already exists or conflicts with current state: key is retired");
  expect(await (await call(jwksPath)).text()).toBe(stagedKeys);
  await runInDurableObject(stub(), (_hub, ctx) => {
    expect(ctx.storage.sql.exec("SELECT status, wrapped_pkcs8, nonce, wrap_key_id FROM signing_keys WHERE kid = ?", oldKid).one())
      .toEqual({ status: "retired", wrapped_pkcs8: null, nonce: null, wrap_key_id: null });
  });

  const after = await readAudit(await call(auditPath, "GET", lead));
  expect(after.entries.slice(2)).toEqual(oldAudit.entries);
  expect(after.entries.slice(0, 2)).toMatchObject([
    { action: "key.activated", target: "key:" + staged.kid, actor_member_id: "lee", meta: { purpose, previous_kid: oldKid } },
    { action: "key.staged", target: "key:" + staged.kid, actor_member_id: "lee", meta: { purpose } },
  ]);
  const freshAuditKeys = await (await call(auditKeys)).text();
  expect(await verifyCheckpoint(oldAudit.token, freshAuditKeys))
    .toMatchObject({ entries: oldAudit.entries.length, tip_hash: oldAudit.entries[0].hash });
  expect(await verifyCheckpoint(after.token, freshAuditKeys))
    .toMatchObject({ entries: after.entries.length, tip_hash: after.entries[0].hash });
  if (purpose === "audit") {
    await expect(verifyCheckpoint(after.token, JSON.stringify(oldKeys))).rejects.toThrow();
    await expect(verifyCheckpoint(stagedAudit.token, JSON.stringify(oldKeys))).resolves.toMatchObject({ entries: stagedAudit.entries.length });
    await expect(verifyCheckpoint(after.token, JSON.stringify({ keys: [staged] }))).resolves.toMatchObject({ entries: after.entries.length });
  } else {
    expect(await signed(await call(project + "/policy.bundle", "GET", member), 1)).toBe(oldPolicy);
    const nextPolicy = await signed(await call(project + "/policy", "PUT", lead, bundle(2)), 2);
    await expect(verifyPolicyBundle(oldPolicy, stagedKeys)).resolves.toMatchObject({ bundle: { version: 1 } });
    await expect(verifyPolicyBundle(nextPolicy, JSON.stringify({ keys: [staged] }))).resolves.toMatchObject({ bundle: { version: 2 } });
    await expect(verifyPolicyBundle(nextPolicy, JSON.stringify(oldKeys))).rejects.toThrow();
  }
});

test("streams 250,001 audit records within the local export budget", async () => {
  const { lead } = await setup();
  const count = 250_001;
  await seedAudit(count - 1);
  const started = performance.now();
  const response = await call(auditPath, "GET", lead);
  expect(response.status).toBe(200);
  if (!response.body) throw new Error("Missing export stream");
  const decoder = new TextDecoder();
  let tail = "";
  let bytes = 0;
  let expectedID = count;
  for await (const chunk of response.body) {
    bytes += chunk.byteLength;
    const records = (tail + decoder.decode(chunk, { stream: true })).split(',{"id":"');
    tail = records.pop() ?? "";
    for (const record of records) {
      const prefix = expectedID === count ? '{"entries":[{"id":"' : "";
      if (!record.startsWith(prefix + expectedID + '"')) throw new Error("Unexpected streamed audit ID " + expectedID);
      expectedID--;
    }
  }
  const elapsed = performance.now() - started;
  expect(expectedID).toBe(1);
  expect(tail + decoder.decode()).toMatch(/^1".*\],"next_cursor":null\}\n$/);
  expect(bytes).toBeGreaterThan(128 << 20);
  expect(elapsed).toBeLessThan(30_000);
  const tip = await runInDurableObject(stub(), (_hub, ctx) =>
    ctx.storage.sql.exec("SELECT audit_tip FROM team").one().audit_tip);
  expect(await verifyCheckpoint(checkpoint(response), await (await call(auditKeys)).text()))
    .toMatchObject({ entries: count, tip_hash: tip });
  console.info(`Audit export drained ${count} records, ${bytes} bytes, in ${Math.round(elapsed)} ms`);
}, 120_000);

test("bootstraps once, provisions both wrapped keys, and recognizes only the first lead's retry", async () => {
  const { lead, bootstrap, init } = await setup();
  await error(await call(admin + "/init", "POST", newToken(), init), 401, "invalid bearer token");
  await check(await call(admin + "/init", "POST", bootstrap, init), 200, {});
  await error(await call(admin + "/init", "POST", bootstrap,
    JSON.stringify({ member_id: "lee", token_hash: hexHash(newToken()) })),
  409, "already exists or conflicts with current state: hub already initialized");
  const other = await addMember(lead, "other", "lead");
  await error(await call(admin + "/init", "POST", bootstrap,
    JSON.stringify({ member_id: "other", token_hash: hexHash(other) })),
  409, "already exists or conflicts with current state: hub already initialized");

  await runInDurableObject(stub(), (_hub, ctx) => {
    const sql = ctx.storage.sql;
    const team = sql.exec("SELECT * FROM team").one();
    const keyRows = sql.exec<{ wrapped_pkcs8: ArrayBuffer; nonce: ArrayBuffer; public_jwk: string }>(
      "SELECT * FROM signing_keys ORDER BY purpose",
    ).toArray();
    expect(keyRows).toMatchObject([
      { kid: team.audit_kid, purpose: "audit", status: "active", wrap_key_id: "v1" },
      { kid: team.policy_kid, purpose: "policy", status: "active", wrap_key_id: "v1" },
    ]);
    expect(String(team.audit_kid)).toMatch(/^audit-/);
    for (const key of keyRows) {
      expect(key.wrapped_pkcs8).toBeInstanceOf(ArrayBuffer);
      expect(key.nonce).toBeInstanceOf(ArrayBuffer);
      expect(typeof key.public_jwk).toBe("string");
      expect(JSON.parse(String(key.public_jwk))).not.toHaveProperty("d");
    }
    expect(new Uint8Array(keyRows[0].nonce)).not.toEqual(new Uint8Array(keyRows[1].nonce));
    expect(sql.exec("SELECT hex(token_hash) AS hash FROM members WHERE member_id = 'lee'").one().hash)
      .toBe(hexHash(lead).toUpperCase());
    expect(team.audit_seq).toBe(2);
  });
});

test("rejects bootstrap retries after the first lead is removed and the ID is reused", async () => {
  const { lead, bootstrap, init } = await setup();
  const backup = await addMember(lead, "backup", "lead");
  const retry = (body: string) => call(admin + "/init", "POST", bootstrap, body);
  const initialized = "already exists or conflicts with current state: hub already initialized";
  await check(await retry(init), 200, {});

  await check(await call(admin + "/members/lee", "DELETE", backup), 200, {});
  await error(await retry(init), 409, initialized);
  const replacement = await addMember(backup, "lee", "lead");
  await error(await retry(JSON.stringify({ member_id: "lee", token_hash: hexHash(replacement) })), 409, initialized);
  await error(await retry(init), 409, initialized);
});

test("provisions projects and members, signs policy, and seals each action with the Go fields", async () => {
  const { lead } = await setup();
  const member = await addMember(lead, "ana");
  await error(await addProject(member), 403, "forbidden");
  await error(await call(admin + "/members", "POST", member, "{}"), 403, "forbidden");
  await error(await call(admin + "/members/lee", "DELETE", member), 403, "forbidden");
  await error(await call(admin + "/members/lee", "DELETE", lead), 409,
    "already exists or conflicts with current state: cannot remove the last lead");
  await error(await call(admin + "/members", "POST", lead, '{"id":"ana","role":"member"}'), 409,
    'already exists or conflicts with current state: member "ana"');
  await error(await call(admin + "/members", "POST", lead, '{"id":"bad/id","role":"member"}'), 400,
    "invalid input: member needs a valid id and role lead or member");
  await error(await addProject(lead, "demo", "bad repo"), 400,
    "invalid input: project needs a valid id and repository identifier");
  await check(await addProject(lead), 200, {});
  await check(await addProject(lead, "other"), 200, {});
  await error(await addProject(lead), 409, 'already exists or conflicts with current state: project "demo"');
  await check(await call(project, "GET", member), 200, { id: "demo", repo });
  await error(await call("/api/v1/projects/missing", "GET", member), 404, "not found");
  await error(await call(project + "/policy.bundle", "GET", member), 404, "not found");
  await error(await call(project + "/policy", "PUT", member, bundle(1)), 403, "forbidden");
  const text = bundle(1, { members: { ana: "dev" } });
  const original = await signed(await call(project + "/policy", "PUT", lead, text), 1);
  expect(await signed(await call(project + "/policy.bundle", "GET", member), 1)).toBe(original);
  const jwks = await call(keys);
  const verified = await verifyPolicyBundle(original, await jwks.text());
  expect(verified.bundle).toEqual(JSON.parse(text));
  await check(await call(admin + "/members/ana", "DELETE", lead), 200, {});
  await error(await call(admin + "/members/ana", "DELETE", lead), 404, "not found");
  await error(await call(project, "GET", member), 401, "invalid bearer token");
  expect(await signed(await call(project + "/policy", "PUT", lead, " \n" + text), 1)).toBe(original);

  await runInDurableObject(stub(), (_hub, ctx) => {
    const sql = ctx.storage.sql;
    const records = sql.exec<{
      id: string; workspace_id: string; actor_member_id: string | null; agent_session_id: string | null;
      reason: string | null; action: string; target: string; meta_json: string; flagged: number;
      created_at: string; prev_hash: string; hash: string;
    }>("SELECT CAST(seq AS TEXT) AS id, * FROM audit_records ORDER BY seq").toArray();
    const chain = records.map(({ meta_json, flagged, ...row }) => ({ ...row, meta: JSON.parse(meta_json), flagged: !!flagged }));
    expect(verifyChain(chain)).toEqual({ ok: true, entries: 6 });
    const tip = sql.exec("SELECT workspace_id, audit_seq, audit_tip FROM team").one();
    expect(tip).toEqual({ workspace_id: chain[0].workspace_id, audit_seq: 6, audit_tip: chain[5].hash });
    expect(chain.map((r) => [r.action, r.target, r.actor_member_id, r.meta])).toEqual([
      ["member.added", "member:lee", null, { role: "lead" }],
      ["member.added", "member:ana", "lee", { role: "member" }],
      ["project.added", "project:demo", "lee", {}],
      ["project.added", "project:other", "lee", {}],
      ["policy.updated", "project:demo", "lee", { project_id: "demo", version: 1 }],
      ["member.removed", "member:ana", "lee", {}],
    ]);
    for (const token of [lead, member]) {
      expect(JSON.stringify(records)).not.toContain(token);
      expect(JSON.stringify(records)).not.toContain(hexHash(token));
    }
  });
});

test("checks method before auth and serves implicit HEAD", async () => {
  const { lead } = await setup();
  await check(await addProject(lead), 200, {});
  await signed(await call(project + "/policy", "PUT", lead, bundle(1)), 1);
  for (const path of ["/healthz", keys, project, project + "/policy.bundle"]) {
    const method = await call(path, "POST");
    expect(method.headers.get("Allow")).toBe("GET, HEAD");
    await error(method, 405, "method not allowed");
    const head = await call(path, "HEAD", lead);
    expect(head.status).toBe(200);
    expect(head.headers.get("Cache-Control")).toBe("no-store");
    expect(await head.text()).toBe("");
  }
  for (const [path, allow] of [
    [project + "/policy", "PUT"], [admin + "/init", "POST"], [admin + "/projects", "POST"],
    [admin + "/members/lee", "DELETE"],
  ]) {
    const method = await call(path);
    expect(method.headers.get("Allow")).toBe(allow);
    await error(method, 405, "method not allowed");
  }
  const membersMethod = await call(admin + "/members", "PUT");
  expect(membersMethod.headers.get("Allow")).toBe("GET, HEAD, POST");
  await error(membersMethod, 405, "method not allowed");
  const head = await call(project, "HEAD");
  expect(head.status).toBe(401);
  expect(head.headers.get("WWW-Authenticate")).toBe("Bearer");
  expect(await head.text()).toBe("");
});

test("unescapes route segments without turning escaped slashes into separators", async () => {
  const { lead } = await setup();
  await check(await addProject(lead), 200, {});
  const signature = await signed(await call(project + "/policy", "PUT", lead, bundle(1)), 1);
  await check(await call("/.well-known/%6Awks.json"), 200, await check(await call(keys), 200));
  await check(await call(keys + "?ignored=/../%GG"), 200, await check(await call(keys), 200));
  await check(await call("/api/v1/%70rojects/%64emo", "GET", lead), 200, { id: "demo", repo });
  expect(await signed(await call(project + "/%70olicy.bundle", "GET", lead), 1)).toBe(signature);

  await error(await call("/.well-known%2Fjwks.json"), 404, "not found");
  await error(await call(project + "%2Fpolicy.bundle"), 401, "invalid bearer token");
  await error(await call(project + "%2Fpolicy.bundle", "GET", lead), 404, "not found");
  await error(await call(project + "%252Fpolicy.bundle", "GET", lead), 404, "not found");
  await error(await call("/api/v1/projects/%252E"), 401, "invalid bearer token");
  await addMember(lead, "ana");
  await check(await call(admin + "/%6Dembers/%61na", "DELETE", lead), 200, {});
});

test.each(["%", "%2", "%GG", "%FF", "%E2%82", "%C0%AF", "%ED%A0%80"])(
  "rejects malformed path escape %s before routing",
  async (segment) => {
    await error(await call("/api/v1/projects/" + segment), 400, "invalid input: malformed path escape");
  },
);

test.each(["", "%2F", "%2f"])("rejects empty or whole-slash wildcard %j", async (segment) => {
  const path = "/api/v1/projects/" + segment;
  await error(await call(path), 404, "not found");
  await error(await call(path + "/policy", "PUT"), 404, "not found");
  await error(await call(admin + "/members/" + segment, "DELETE"), 404, "not found");
});

test("checks bearer syntax before body limits and membership after", async () => {
  const { lead } = await setup();
  await check(await addProject(lead), 200, {});
  const commaToken = "a".repeat(21) + "," + "a".repeat(21);
  for (const [path, method, limit, json] of [
    [project + "/policy", "PUT", 1 << 20, bundle(1)],
    [admin + "/projects", "POST", 64 << 10, '{"id":"exact","repo":"team/demo"}'],
  ] as const) {
    const oversized = json.padEnd(limit + 1);
    const wrongScheme = incoming(path, method, undefined, oversized);
    wrongScheme.headers.set("Authorization", "Basic " + newToken());
    await error(await exports.default.fetch(wrongScheme), 401, "invalid bearer token");
    await error(await call(path, method, "short", oversized), 401, "invalid bearer token");
    await error(await call(path, method, newToken(), oversized), 413, "request body is too large");
    await error(await call(path, method, commaToken.slice(1), oversized), 401, "invalid bearer token");
    await error(await call(path, method, commaToken, oversized), 401, "invalid bearer token");
    await error(await call(path, method, commaToken, json), 401, "invalid bearer token");
    const duplicate = incoming(path, method, "a".repeat(20), oversized);
    duplicate.headers.append("Authorization", "b".repeat(21));
    expect(duplicate.headers.get("Authorization")).toBe("Bearer " + "a".repeat(20) + ", " + "b".repeat(21));
    await error(await exports.default.fetch(duplicate), 401, "invalid bearer token");
    await check(await call(path, method, lead, json.padEnd(limit)), 200);
  }
  // Call the object directly so the failing stream is not consumed by service routing.
  await runInDurableObject(stub(), async (hub) => {
    const broken = new ReadableStream<Uint8Array>({ start(controller) { controller.error(new Error("broken body")); } });
    const request = new Request("https://hub.example" + project + "/policy", {
      method: "PUT", headers: { Authorization: "Bearer " + lead }, body: broken,
    });
    await error(await hub.fetch(request), 400, "could not read request body");
  });
});

test("matches policy validation and version errors and caps canonical UTF-8", async () => {
  const { lead } = await setup();
  await check(await addProject(lead), 200, {});
  const put = (body: string) => call(project + "/policy", "PUT", lead, body);
  await error(await call("/api/v1/projects/missing/policy", "PUT", lead, "{}"), 404, "not found");
  await error(await put("{}"), 400,
    "invalid input: policy: version is required; project_id is required; repos is required; areas is required; roles is required; members is required");
  await error(await put(bundle(1) + "{}"), 400, "invalid input: policy: has text after the JSON value");
  await error(await put(bundle(1, { repos: ["other"] })), 400,
    'invalid input: bundle must target project "demo" and its repository "team/demo"');
  await error(await put(bundle(1, { members: { missing: "role" } })), 400,
    "invalid input: members references an unknown member");
  await error(await put(bundle(1, { temporary_grants: [
    { scope: "mission", mission_id: "test", member_id: "missing", area: "code", actions: ["write"] },
  ] })), 400, "invalid input: temporary_grants member_id references an unknown member");

  const prefix = bundle(2).slice(0, -1) + ',"decimal":1e-6,"padding":"';
  const byteLimit = 1 << 20;
  const exact = prefix + "é".repeat(Math.floor((byteLimit - Buffer.byteLength(prefix) - 6) / 2));
  const body = exact + " ".repeat(byteLimit - Buffer.byteLength(exact) - 6) + '"}';
  await error(await put(body.slice(0, -2) + 'x"}'), 400, "invalid input: canonical policy exceeds 1048576 bytes");
  await signed(await put(body), 2);
  for (const rejected of [bundle(1), bundle(2)]) {
    await error(await put(rejected), 409, "already exists or conflicts with current state: policy version must be higher than 2");
  }
});

test("compares bootstrap and roster credentials as fixed-size hashes", async () => {
  const token = newToken();
  const bootstrap = env.BOOTSTRAP_TOKEN;
  if (!bootstrap) throw new Error("Missing test bootstrap binding");
  await runInDurableObject(stub(), async (hub) => {
    const compare = vi.spyOn(crypto.subtle, "timingSafeEqual");
    const comparedHashes = () => compare.mock.calls.map((args) =>
      args.map((arg) => arg instanceof ArrayBuffer ? Buffer.from(arg).toString("hex") : arg),
    );
    try {
      const init = JSON.stringify({ member_id: "lee", token_hash: hexHash(token) });
      await error(await hub.fetch(incoming(admin + "/init", "POST", "different-bootstrap", init)), 401, "invalid bearer token");
      expect(comparedHashes()).toContainEqual([hexHash("different-bootstrap"), hexHash(bootstrap)]);
      await check(await hub.fetch(incoming(admin + "/init", "POST", bootstrap, init)), 200, {});
      compare.mockClear();
      await error(await hub.fetch(incoming(project, "GET", token)), 404, "not found");
      expect(comparedHashes()).toContainEqual([hexHash(token), hexHash(token)]);
      compare.mockClear();
      const unknown = newToken();
      await error(await hub.fetch(incoming(project, "GET", unknown)), 401, "invalid bearer token");
      expect(comparedHashes()).toContainEqual([hexHash(unknown), hexHash(token)]);
    } finally {
      compare.mockRestore();
    }
  });
});

test("rolls back both the action and the sealed record if advancing the audit tip fails", async () => {
  const { lead } = await setup();
  await runInDurableObject(stub(), async (hub, ctx) => {
    const sql = ctx.storage.sql;
    const before = sql.exec("SELECT audit_seq, audit_tip FROM team").one();
    sql.exec("ALTER TABLE team ADD COLUMN keep_genesis_record INTEGER CHECK (audit_seq = 1)");
    await error(await hub.fetch(incoming(admin + "/projects", "POST", lead, JSON.stringify({ id: "demo", repo }))),
      500, "hub state unavailable");
    expect(sql.exec("SELECT * FROM projects").toArray()).toEqual([]);
    expect(sql.exec("SELECT seq FROM audit_records").toArray()).toEqual([{ seq: 1 }]);
    expect(sql.exec("SELECT audit_seq, audit_tip FROM team").one()).toEqual(before);
    sql.exec("ALTER TABLE team DROP COLUMN keep_genesis_record");
  });
  await check(await addProject(lead), 200, {});
});

function wrapRequest(token: string, body?: string, operator = env.WRAP_ROTATION_TOKEN) {
  const request = incoming(admin + "/wrapping-key", body === undefined ? "GET" : "PUT", token, body);
  if (operator) request.headers.set("OpenPlexer-Operator-Token", operator);
  return request;
}

const toV2 = '{"wrap_key_id":"v2"}';
const wrapRows = (sql: SqlStorage) => sql.exec(
  "SELECT kid, purpose, status, public_jwk, hex(wrapped_pkcs8) AS ciphertext, hex(nonce) AS nonce, wrap_key_id FROM signing_keys ORDER BY kid",
).toArray();

test("requires a lead and an operator, and rejects ambiguous rotation requests", async () => {
  const { lead, bootstrap } = await setup();
  const member = await addMember(lead, "ana");
  for (const body of [undefined, toV2]) {
    const withoutOperator = wrapRequest(lead, body);
    withoutOperator.headers.delete("OpenPlexer-Operator-Token");
    await error(await exports.default.fetch(withoutOperator), 401, "invalid bearer token");
    await error(await exports.default.fetch(wrapRequest(lead, body, bootstrap)), 401, "invalid bearer token");
    await error(await exports.default.fetch(wrapRequest(newToken(), body)), 401, "invalid bearer token");
    await error(await exports.default.fetch(wrapRequest(member, body)), 403, "forbidden");
  }
  for (const body of ['{}', '{"Wrap_key_id":"v2"}', '{"wrap_key_id":"V2"}', '{"wrap_key_id":"v02"}',
    '{"wrap_key_id":"v2","wrap_key_id":"v2"}', '{"wrap_key_id":"v2","extra":true}']) {
    await error(await exports.default.fetch(wrapRequest(lead, body)), 400, "invalid input: expected a versioned wrap_key_id");
  }
  await error(await exports.default.fetch(wrapRequest(lead, " ".repeat((64 << 10) + 1))), 413, "request body is too large");
  await check(await exports.default.fetch(wrapRequest(lead)), 200, { wrap_key_ids: ["v1"] });
});

test("rewraps mixed active and staged keys, preserves retired keys and pins, and audits once", async () => {
  const { lead } = await setup();
  await check(await addProject(lead), 200, {});
  const replaced = await check(await call(admin + "/keys/audit", "POST", lead), 200);
  if (!replaced || typeof replaced !== "object" || !("kid" in replaced)) throw new Error("Missing staged kid");
  await check(await call(admin + "/keys/audit/active", "PUT", lead, JSON.stringify({ kid: replaced.kid })), 200, {});
  for (const purpose of ["policy", "audit"]) await check(await call(admin + "/keys/" + purpose, "POST", lead), 200);
  const pinnedPolicy = await (await call(keys)).text();
  const pinnedAudit = await (await call(auditKeys)).text();

  await runInDurableObject(stub(), async (hub, ctx) => {
    const sql = ctx.storage.sql;
    const { kid, wrapped_pkcs8, nonce } = sql.exec<{ kid: string; wrapped_pkcs8: ArrayBuffer; nonce: ArrayBuffer }>(
      "SELECT kid, wrapped_pkcs8, nonce FROM signing_keys WHERE purpose = 'audit' AND status = 'active'",
    ).one();
    const identity = { team: env.TEAM_ID, purpose: "audit", kid } satisfies Parameters<typeof wrapPKCS8>[2];
    const bytes = await unwrapPKCS8({ ciphertext: wrapped_pkcs8, nonce: new Uint8Array(nonce) },
      loadWrappingKey(env, "v1").bytes, identity);
    const mixed = await wrapPKCS8(bytes, loadWrappingKey(env, "v2").bytes, identity);
    sql.exec("UPDATE signing_keys SET wrapped_pkcs8 = ?, nonce = ?, wrap_key_id = 'v2' WHERE kid = ?",
      mixed.ciphertext, mixed.nonce.buffer, kid);
    await check(await hub.fetch(wrapRequest(lead)), 200, { wrap_key_ids: ["v1", "v2"] });
    await verifyPolicyBundle(await signed(await hub.fetch(incoming(project + "/policy", "PUT", lead, bundle(1))), 1), pinnedPolicy);
    await verifyCheckpoint((await readAudit(await hub.fetch(incoming(auditPath, "GET", lead)))).token, pinnedAudit);

    const before = wrapRows(sql);
    await check(await hub.fetch(wrapRequest(lead, toV2)), 200, { wrap_key_ids: ["v2"] });
    const after = wrapRows(sql);
    expect(after).toHaveLength(5);
    for (const [i, row] of before.entries()) {
      if (row.wrap_key_id !== "v1") {
        expect(after[i]).toEqual(row);
      } else {
        expect(after[i]).toEqual({ ...row, wrap_key_id: "v2", ciphertext: expect.any(String), nonce: expect.any(String) });
        expect(after[i].ciphertext).not.toBe(row.ciphertext);
        expect(after[i].nonce).not.toBe(row.nonce);
      }
    }
    await check(await hub.fetch(wrapRequest(lead, toV2)), 200, { wrap_key_ids: ["v2"] });
    expect(wrapRows(sql)).toEqual(after);
  });
  expect(await (await call(keys)).text()).toBe(pinnedPolicy);
  expect(await (await call(auditKeys)).text()).toBe(pinnedAudit);
  const audit = await readAudit(await call(auditPath, "GET", lead));
  await verifyCheckpoint(audit.token, pinnedAudit);
  const rotations = audit.entries.filter((entry) => entry.action === "wrapping_key.rotated")
    .map(({ target, actor_member_id, meta }) => ({ target, actor_member_id, meta }));
  expect(rotations).toEqual([
    { target: "wrapping-key:v2", actor_member_id: "lee", meta: { previous_wrap_key_ids: ["v1"], rows_rewrapped: 3 } },
  ]);
});

test("fails closed with missing source and target secrets", async () => {
  const { lead } = await setup();
  await runInDurableObject(stub(), async (hub, ctx) => {
    const sql = ctx.storage.sql;
    const before = wrapRows(sql);
    await error(await hub.fetch(wrapRequest(lead, '{"wrap_key_id":"v3"}')), 503, "Missing WRAP_KEY_V3");
    expect(wrapRows(sql)).toEqual(before);
    sql.exec("UPDATE signing_keys SET wrap_key_id = 'v3' WHERE purpose = 'audit'");
    const mixed = wrapRows(sql);
    await check(await hub.fetch(wrapRequest(lead)), 200, { wrap_key_ids: ["v1", "v3"] });
    await error(await hub.fetch(incoming(auditPath, "GET", lead)), 503, "Missing WRAP_KEY_V3");
    await error(await hub.fetch(wrapRequest(lead, toV2)), 503, "Missing WRAP_KEY_V3");
    expect(wrapRows(sql)).toEqual(mixed);
    expect(sql.exec("SELECT audit_seq FROM team").one().audit_seq).toBe(1);
  });
});

test.each(["tampered ciphertext", "wrong team", "mismatched public JWK"])("rejects %s without writing any rows", async (failure) => {
  const { lead } = await setup();
  await runInDurableObject(stub(), async (hub, ctx) => {
    const sql = ctx.storage.sql;
    if (failure === "tampered ciphertext") {
      sql.exec("UPDATE signing_keys SET wrapped_pkcs8 = zeroblob(length(wrapped_pkcs8)) WHERE purpose = 'policy'");
    } else if (failure === "wrong team") {
      const wrong = await newSigningKey("another-team", "policy", loadWrappingKey(env, "v1"));
      sql.exec("UPDATE signing_keys SET kid = ?, public_jwk = ?, wrapped_pkcs8 = ?, nonce = ? WHERE purpose = 'policy'",
        wrong.kid, wrong.public_jwk, wrong.wrapped_pkcs8, wrong.nonce);
      sql.exec("UPDATE team SET policy_kid = ?", wrong.kid);
    } else {
      const wrong = await newSigningKey(env.TEAM_ID, "policy", loadWrappingKey(env, "v1"));
      sql.exec("UPDATE signing_keys SET public_jwk = json_set(?, '$.kid', kid) WHERE purpose = 'policy'", wrong.public_jwk);
    }
    const before = wrapRows(sql);
    await error(await hub.fetch(wrapRequest(lead, toV2)), 500, failure === "mismatched public JWK"
      ? "Unable to verify rewrapped policy signing key" : "Unable to unwrap policy signing key with WRAP_KEY_V1");
    expect(wrapRows(sql)).toEqual(before);
    expect(sql.exec("SELECT audit_seq FROM team").one().audit_seq).toBe(1);
  });
});

test("rolls back every ciphertext and its audit record if commit fails after the rewrap", async () => {
  const { lead } = await setup();
  await runInDurableObject(stub(), async (hub, ctx) => {
    const sql = ctx.storage.sql;
    const rows = wrapRows(sql);
    const tip = sql.exec("SELECT audit_seq, audit_tip FROM team").one();
    sql.exec("ALTER TABLE team ADD COLUMN keep_genesis_record INTEGER CHECK (audit_seq = 1)");
    await error(await hub.fetch(wrapRequest(lead, toV2)), 500, "hub state unavailable");
    expect(wrapRows(sql)).toEqual(rows);
    expect(sql.exec("SELECT seq FROM audit_records").toArray()).toEqual([{ seq: 1 }]);
    expect(sql.exec("SELECT audit_seq, audit_tip FROM team").one()).toEqual(tip);
    sql.exec("ALTER TABLE team DROP COLUMN keep_genesis_record");
    await check(await hub.fetch(wrapRequest(lead, toV2)), 200, { wrap_key_ids: ["v2"] });
  });
});

test("refuses a stale rotation if a signing key gets staged during rewrapping", async () => {
  const { lead } = await setup();
  await runInDurableObject(stub(), async (hub, ctx) => {
    const encrypt = crypto.subtle.encrypt.bind(crypto.subtle);
    const spy = vi.spyOn(crypto.subtle, "encrypt").mockImplementationOnce(async (...args) => {
      await check(await hub.fetch(incoming(admin + "/keys/policy", "POST", lead)), 200);
      return encrypt(...args);
    });
    try {
      await error(await hub.fetch(wrapRequest(lead, toV2)), 409,
        "already exists or conflicts with current state: signing keys changed during wrapping key rotation; retry");
      expect(wrapRows(ctx.storage.sql).map((row) => row.wrap_key_id)).toEqual(["v1", "v1", "v1"]);
      await check(await hub.fetch(wrapRequest(lead, toV2)), 200, { wrap_key_ids: ["v2"] });
    } finally {
      spy.mockRestore();
    }
  });
});

test("a stage started before wrapping key rotation uses the new binding when it commits", async () => {
  const { lead } = await setup();
  await runInDurableObject(stub(), async (hub, ctx) => {
    const encrypt = crypto.subtle.encrypt.bind(crypto.subtle);
    const spy = vi.spyOn(crypto.subtle, "encrypt").mockImplementationOnce(async (...args) => {
      await check(await hub.fetch(wrapRequest(lead, toV2)), 200, { wrap_key_ids: ["v2"] });
      return encrypt(...args);
    });
    try {
      await check(await hub.fetch(incoming(admin + "/keys/policy", "POST", lead)), 200);
      expect(wrapRows(ctx.storage.sql).map((row) => row.wrap_key_id)).toEqual(["v2", "v2", "v2"]);
    } finally {
      spy.mockRestore();
    }
  });
});

test("re-signs with the new active key if it changes while signing and advertises retired keys", async () => {
  const { lead } = await setup();
  await check(await addProject(lead), 200, {});
  const old = await check(await call(keys), 200);
  const replacement = await newSigningKey(env.TEAM_ID, "policy", loadWrappingKey(env, "v1"));
  const signature = await runInDurableObject(stub(), async (hub, ctx) => {
    const sign = crypto.subtle.sign.bind(crypto.subtle);
    const spy = vi.spyOn(crypto.subtle, "sign").mockImplementationOnce((...args) => {
      ctx.storage.transactionSync(() => {
        const sql = ctx.storage.sql;
        sql.exec(`UPDATE signing_keys SET status = 'retired', wrapped_pkcs8 = NULL, nonce = NULL, wrap_key_id = NULL
          WHERE purpose = 'policy' AND status = 'active'`);
        sql.exec(`INSERT INTO signing_keys (kid, purpose, public_jwk, status, wrapped_pkcs8, nonce, wrap_key_id)
          VALUES (?, 'policy', ?, 'active', ?, ?, ?)`,
        replacement.kid, replacement.public_jwk, replacement.wrapped_pkcs8, replacement.nonce, replacement.wrap_key_id);
        sql.exec("UPDATE team SET policy_kid = ?", replacement.kid);
      });
      return sign(...args);
    });
    try {
      const token = await signed(await hub.fetch(incoming(project + "/policy", "PUT", lead, bundle(1))), 1);
      expect(spy).toHaveBeenCalledTimes(2);
      expect(ctx.storage.sql.exec("SELECT kid FROM policies").one().kid).toBe(replacement.kid);
      expect(ctx.storage.sql.exec("SELECT audit_seq FROM team").one().audit_seq).toBe(3);
      return token;
    } finally {
      spy.mockRestore();
    }
  });
  const jwks = await call(keys);
  const text = await jwks.text();
  expect(JSON.parse(text)).toEqual({ keys: expect.arrayContaining([JSON.parse(replacement.public_jwk)]) });
  if (!old || typeof old !== "object" || !("keys" in old) || !Array.isArray(old.keys)) throw new Error("Missing original JWKS");
  expect(JSON.parse(text).keys).toHaveLength(2);
  expect(JSON.parse(text).keys).toContainEqual(old.keys[0]);
  await expect(verifyPolicyBundle(signature, '{"keys":[' + replacement.public_jwk + ']}')).resolves.toMatchObject({ bundle: { version: 1 } });
});

test("rejects a publisher overtaken by a newer version during signing", async () => {
  const { lead } = await setup();
  await check(await addProject(lead), 200, {});
  await runInDurableObject(stub(), async (hub, ctx) => {
    const sign = crypto.subtle.sign.bind(crypto.subtle);
    const spy = vi.spyOn(crypto.subtle, "sign").mockImplementationOnce(async (...args) => {
      await signed(await hub.fetch(incoming(project + "/policy", "PUT", lead, bundle(2))), 2);
      return sign(...args);
    });
    try {
      await error(await hub.fetch(incoming(project + "/policy", "PUT", lead, bundle(1))), 409,
        "already exists or conflicts with current state: policy version must be higher than 2");
      expect(ctx.storage.sql.exec("SELECT audit_seq FROM team").one().audit_seq).toBe(3);
    } finally {
      spy.mockRestore();
    }
  });
  await signed(await call(project + "/policy.bundle", "GET", lead), 2);
});

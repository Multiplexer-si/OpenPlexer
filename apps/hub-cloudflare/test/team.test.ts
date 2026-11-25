import { Buffer } from "node:buffer";
import { reset, runInDurableObject } from "cloudflare:test";
import { env, exports } from "cloudflare:workers";
import { beforeEach, expect, test, vi } from "vitest";
import { verifyChain } from "../src/audit";
import { tokenHash } from "../src/http";
import { verifyPolicyBundle } from "../src/policy";
import { newSigningKey, newToken, wrappingKey } from "../src/state";

beforeEach(reset);

const admin = "/api/v1/admin";
const project = "/api/v1/projects/demo";
const keys = "/.well-known/jwks.json";
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
    [admin + "/members", "POST"], [admin + "/members/lee", "DELETE"],
  ]) {
    const method = await call(path);
    expect(method.headers.get("Allow")).toBe(allow);
    await error(method, 405, "method not allowed");
  }
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
  await check(await call("/api/v1/%70rojects/%64emo", "GET", lead), 200, { id: "demo", repo });
  expect(await signed(await call(project + "/%70olicy.bundle", "GET", lead), 1)).toBe(signature);

  await error(await call("/.well-known%2Fjwks.json"), 404, "not found");
  await error(await call(project + "%2Fpolicy.bundle"), 401, "invalid bearer token");
  await error(await call(project + "%2Fpolicy.bundle", "GET", lead), 404, "not found");
  await error(await call(project + "%252Fpolicy.bundle", "GET", lead), 404, "not found");
  await addMember(lead, "ana");
  await check(await call(admin + "/%6Dembers/%61na", "DELETE", lead), 200, {});
});

test("checks bearer syntax before body limits and membership after", async () => {
  const { lead } = await setup();
  await check(await addProject(lead), 200, {});
  for (const [path, method, limit, json] of [
    [project + "/policy", "PUT", 1 << 20, bundle(1)],
    [admin + "/projects", "POST", 64 << 10, '{"id":"exact","repo":"team/demo"}'],
  ] as const) {
    const oversized = json.padEnd(limit + 1);
    await error(await call(path, method, "short", oversized), 401, "invalid bearer token");
    await error(await call(path, method, newToken(), oversized), 413, "request body is too large");
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

test("re-signs with the new active key if it changes while signing and advertises retired keys", async () => {
  const { lead } = await setup();
  await check(await addProject(lead), 200, {});
  const old = await check(await call(keys), 200);
  const replacement = await newSigningKey(env.TEAM_ID, "policy", wrappingKey(env.WRAP_KEY_V1));
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

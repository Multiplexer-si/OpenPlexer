import { Buffer } from "node:buffer";
import { DatabaseSync } from "node:sqlite";
import { afterEach, expect, test } from "vitest";
import { isJsonObject, parseJSON } from "../src/canonical";
import { generateSigningKey, signCanonicalJWS } from "../src/jws";
import { membersPage, observeMember, parseMembersQuery, pruneMemberObservations } from "../src/members";
import { readPolicy } from "../src/publication";
import { schemaMigrations } from "../src/storage";
import type { TransactionSQL } from "../src/storage";

let db: DatabaseSync | undefined;
afterEach(() => { db?.close(); db = undefined; });

function setup(): TransactionSQL {
  const sqlite = new DatabaseSync(":memory:");
  db = sqlite;
  for (const migration of schemaMigrations) sqlite.exec(migration.sql);
  sqlite.exec(`INSERT INTO members (member_id, role, token_hash)
      VALUES ('lee', 'lead', zeroblob(32)), ('ana', 'member', randomblob(32));
    INSERT INTO projects (project_id, repo) VALUES ('shop', 'api'), ('tools', 'tools');`);
  return {
    exec<T extends Record<string, SqlStorageValue>>(query: string, ...params: SqlStorageValue[]) {
      const statement = sqlite.prepare(query);
      const values = params.map((value) => value instanceof ArrayBuffer ? new Uint8Array(value) : value);
      // Match the generic row contract of Cloudflare's SQL cursor.
      return statement.all(...values) as T[];
    },
  };
}

test("reports the signed payload version when the stored display version differs", async () => {
  const sql = setup();
  const { privateKey } = await generateSigningKey();
  const payload = '{"areas":[],"members":{},"project_id":"shop","repos":["api"],"roles":{},"version":1}';
  const signature = await signCanonicalJWS(payload, privateKey, "policy", "policy");
  const [header, , signed] = signature.split(".");
  sql.exec(`INSERT INTO policies (project_id, version, bundle_json, kid, jws_header, jws_signature)
    VALUES ('shop', 9, ?, 'policy', ?, ?)`, payload, header, signed);

  const page = () => membersPage(sql, parseMembersQuery("project_id=shop"), Date.now());
  const head = readPolicy(sql, "shop");
  expect(head.headers.get("OpenPlexer-Policy-Version")).toBe("1");
  expect(page().members[0].policy_version).toBeNull();

  const response = readPolicy(sql, "shop", "ana");
  expect(response.status).toBe(200);
  expect(response.headers.get("OpenPlexer-Policy-Version")).toBe("1");
  const served = parseJSON(await response.text());
  if (!isJsonObject(served)) throw new Error("invalid signed policy response");
  expect(served.version).toBe(9);
  expect(served.signature === signature).toBe(true);
  expect(page().members[0].policy_version).toBe(1);
});

test("retains and prunes the two observations independently at 30 days", () => {
  const sql = setup();
  const at = Date.parse("2026-10-01T12:00:00.000Z");
  const ttl = 30 * 24 * 60 * 60 * 1_000;
  observeMember(sql, "ana", "shop", at - 1_000, { kind: "fetch", version: 1 });
  observeMember(sql, "ana", "shop", at, { kind: "fetch", version: 2 });
  observeMember(sql, "ana", "shop", at + 10, { kind: "room" });
  observeMember(sql, "ana", "tools", at - 2_000, { kind: "room" });
  const page = (now: number, query = "") => membersPage(sql, parseMembersQuery(query), now);
  const empty = {
    member_id: "ana", hub_role: "member", project_id: null,
    last_policy_fetch_at: null, policy_version: null, last_room_connected_at: null,
  };
  const shop = {
    ...empty, project_id: "shop", last_policy_fetch_at: "2026-10-01T12:00:00.000Z",
    policy_version: 2, last_room_connected_at: "2026-10-01T12:00:00.010Z",
  };
  expect(page(at + ttl - 1).members[0]).toEqual(shop);
  expect(page(at + ttl).members[0]).toEqual({ ...shop, last_policy_fetch_at: null, policy_version: null });
  expect(page(at + ttl + 10).members[0]).toEqual(empty);
  expect(page(at + ttl + 10, "project_id=shop").members[0]).toEqual({ ...empty, project_id: "shop" });
  pruneMemberObservations(sql, at + ttl);
  expect(page(at).members[0]).toEqual({ ...shop, last_policy_fetch_at: null, policy_version: null });
  pruneMemberObservations(sql, at + ttl + 10);
  expect(page(at).members[0]).toEqual(empty);
  expect(sql.exec("SELECT * FROM member_observations")).toEqual([]);
});

test("uses canonical tuple cursors for current rows with a default limit of 100", () => {
  const sql = setup();
  for (let i = 0; i < 102; i++) {
    sql.exec("INSERT INTO members (member_id, role, token_hash) VALUES (?, 'member', randomblob(32))",
      "seed-" + String(i).padStart(3, "0"));
  }
  const now = Date.parse("2026-10-01T12:00:00.000Z");
  observeMember(sql, "ana", "tools", now, { kind: "room" });
  observeMember(sql, "ana", "shop", now, { kind: "room" });

  const page = (query = "") => membersPage(sql, parseMembersQuery(query), now);
  const first = page("limit=1");
  expect(first.members[0].project_id).toBe("shop");
  expect(first.next_cursor).toBe(Buffer.from('["ana","shop"]').toString("base64url"));
  expect(page("limit=1&after=" + first.next_cursor).members[0].project_id).toBe("tools");
  expect(page("project_id=shop&limit=1&after=" + first.next_cursor).members[0].member_id).toBe("lee");
  expect(page("project_id=tools&limit=1&after=" + first.next_cursor).members[0].member_id).toBe("ana");
  const defaultPage = page();
  expect(defaultPage.members).toHaveLength(100);
  expect(defaultPage.next_cursor).not.toBeNull();
  sql.exec("DELETE FROM members WHERE member_id = ?", defaultPage.members[99].member_id);
  const last = page("after=" + defaultPage.next_cursor);
  expect(last.members.map(({ member_id }) => member_id)).toEqual(["seed-097", "seed-098", "seed-099", "seed-100", "seed-101"]);
  expect(last.next_cursor).toBeNull();
  expect(() => page("project_id=missing")).toThrow("not found");
});

test("rejects malformed member queries and noncanonical cursors", () => {
  const encoded = (json: string) => Buffer.from(json).toString("base64url");
  for (const query of [
    "after", "after=bad!", "after=" + encoded('["ana"]'), "after=" + encoded('[ "ana",null]'),
    "after=" + encoded('["ana",123]'), "after=" + encoded('["ana","bad/id"]'),
    "after=" + encoded('["ana",null]') + "=",
    "project_id=", "project_id=bad/name", "project_id=shop;limit=1", "project_id=%zz", "project_id=shop&project_id=tools",
    "limit=0", "limit=101", "limit=1.0", "limit=1&%6cimit=1", "unknown=x",
  ]) {
    expect(() => parseMembersQuery(query)).toThrow("invalid input");
  }
});

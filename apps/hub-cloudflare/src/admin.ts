import { Buffer } from "node:buffer";
import { isJsonObject, parseJSON } from "./canonical";
import type { Env } from "./index";
import { bearer, cappedBody, conflict, invalid, notFound, tokenHash, unauthorized } from "./http";
import { appendAudit, authenticate, newSigningKey, newToken, team, wrappingKey } from "./state";
import type { HubStorage, TransactionSQL } from "./storage";

const adminLimit = 64 << 10;
const idPattern = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/;
const memberInput = "member needs a valid id and role lead or member";
const projectInput = "project needs a valid id and repository identifier";

function validID(id: unknown): id is string {
  return typeof id === "string" && idPattern.test(id);
}

function validRepo(repo: unknown): repo is string {
  return typeof repo === "string" && repo !== "" && repo !== "*" && Buffer.byteLength(repo) <= 1024
    && !/[\p{White_Space}\p{Cc}]/u.test(repo);
}

function adminJSON(body: Uint8Array) {
  try {
    const value = parseJSON(body, { loneSurrogates: "reject" });
    if (isJsonObject(value)) return value;
  } catch {
    // Use the same error for malformed JSON and non-object bodies.
  }
  throw invalid("body must be a JSON object");
}

function alreadyInitialized(sql: TransactionSQL, memberID: string, hash: ArrayBuffer): boolean {
  if (!team(sql)) return false;
  const [lead] = sql.exec<{ token_hash: ArrayBuffer }>(`SELECT token_hash FROM members
    JOIN audit_records ON seq = 1 AND action = 'member.added' AND target = 'member:' || member_id
    WHERE member_id = ? AND role = 'lead'`, memberID);
  if (!lead || !crypto.subtle.timingSafeEqual(hash, lead.token_hash)) throw conflict("hub already initialized");
  return true;
}

export async function initialize(request: Request, storage: HubStorage, env: Pick<Env, "TEAM_ID" | "BOOTSTRAP_TOKEN" | "WRAP_KEY_V1">) {
  const presented = bearer(request, true);
  const body = await cappedBody(request, adminLimit);
  if (!env.BOOTSTRAP_TOKEN || !crypto.subtle.timingSafeEqual(presented, tokenHash(env.BOOTSTRAP_TOKEN))) throw unauthorized();
  const { member_id: id, token_hash: hex } = adminJSON(body);
  if (!validID(id) || typeof hex !== "string" || hex.length !== 64 || /[^0-9a-f]/i.test(hex)) {
    throw invalid("init needs a valid member_id and hex SHA-256 token_hash");
  }
  const hash = Uint8Array.from(Buffer.from(hex, "hex")).buffer;
  if (storage.transaction((sql) => alreadyInitialized(sql, id, hash))) return {};

  const key = wrappingKey(env.WRAP_KEY_V1);
  const [policy, audit] = await Promise.all([
    newSigningKey(env.TEAM_ID, "policy", key),
    newSigningKey(env.TEAM_ID, "audit", key),
  ]);
  return storage.transaction((sql) => {
    if (alreadyInitialized(sql, id, hash)) return {};
    for (const row of [policy, audit]) {
      sql.exec(`INSERT INTO signing_keys (kid, purpose, public_jwk, status, wrapped_pkcs8, nonce, wrap_key_id)
        VALUES (?, ?, ?, 'active', ?, ?, ?)`,
      row.kid, row.purpose, row.public_jwk, row.wrapped_pkcs8, row.nonce, row.wrap_key_id);
    }
    sql.exec("INSERT INTO team (singleton, workspace_id, policy_kid, audit_kid) VALUES (1, ?, ?, ?)",
      crypto.randomUUID(), policy.kid, audit.kid);
    sql.exec("INSERT INTO members (member_id, role, token_hash) VALUES (?, 'lead', ?)", id, hash);
    appendAudit(sql, { action: "member.added", target: "member:" + id, actor_member_id: null, meta: { role: "lead" } });
    return {};
  });
}

export async function addMember(request: Request, storage: HubStorage) {
  const hash = bearer(request);
  const body = await cappedBody(request, adminLimit);
  return storage.transaction((sql) => {
    const actor = authenticate(sql, hash, true);
    const { id, role } = adminJSON(body);
    if (!validID(id) || (role !== "lead" && role !== "member")) throw invalid(memberInput);
    if (sql.exec("SELECT 1 FROM members WHERE member_id = ?", id).length) {
      throw conflict("member " + JSON.stringify(id));
    }
    const token = newToken();
    sql.exec("INSERT INTO members (member_id, role, token_hash) VALUES (?, ?, ?)", id, role, tokenHash(token));
    appendAudit(sql, { action: "member.added", target: "member:" + id, actor_member_id: actor, meta: { role } });
    return { token };
  });
}

export function removeMember(request: Request, storage: HubStorage, id: string) {
  const hash = bearer(request);
  return storage.transaction((sql) => {
    const actor = authenticate(sql, hash, true);
    const [member] = sql.exec<{ role: string }>("SELECT role FROM members WHERE member_id = ?", id);
    if (!member) throw notFound();
    if (member.role === "lead" && sql.exec("SELECT 1 FROM members WHERE role = 'lead' LIMIT 2").length === 1) {
      throw conflict("cannot remove the last lead");
    }
    sql.exec("DELETE FROM members WHERE member_id = ?", id);
    appendAudit(sql, { action: "member.removed", target: "member:" + id, actor_member_id: actor });
    return {};
  });
}

export async function addProject(request: Request, storage: HubStorage) {
  const hash = bearer(request);
  const body = await cappedBody(request, adminLimit);
  return storage.transaction((sql) => {
    const actor = authenticate(sql, hash, true);
    const { id, repo } = adminJSON(body);
    if (!validID(id) || !validRepo(repo)) throw invalid(projectInput);
    if (sql.exec("SELECT 1 FROM projects WHERE project_id = ?", id).length) {
      throw conflict("project " + JSON.stringify(id));
    }
    sql.exec("INSERT INTO projects (project_id, repo) VALUES (?, ?)", id, repo);
    appendAudit(sql, { action: "project.added", target: "project:" + id, actor_member_id: actor });
    return {};
  });
}

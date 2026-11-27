import { Buffer } from "node:buffer";
import { hasDuplicateMembers, isJsonObject, parseJSON } from "./canonical";
import type { JsonObject } from "./canonical";
import type { Env } from "./index";
import { bearer, cappedBody, conflict, invalid, notFound, tokenHash, unauthorized } from "./http";
import type { SigningPurpose } from "./jws";
import { activeSigningKey, activeTeam, appendAudit, authenticate, insertMember, insertSigningKey, memberReserved, newSigningKey, newToken, team, wrappingKey } from "./state";
import type { HubStorage, TransactionSQL } from "./storage";

const adminLimit = 64 << 10;
const idPattern = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/;
const memberInput = "member needs a valid id and role lead or member";
const projectInput = "project needs a valid id and repository identifier";

export function validID(id: unknown): id is string {
  return typeof id === "string" && idPattern.test(id);
}

function validRepo(repo: unknown): repo is string {
  return typeof repo === "string" && repo !== "" && repo !== "*" && Buffer.byteLength(repo) <= 1024
    && !/[\p{White_Space}\p{Cc}]/u.test(repo);
}

export function adminJSON(body: Uint8Array, duplicates?: WeakSet<JsonObject>) {
  try {
    const value = parseJSON(body, { loneSurrogates: "reject", duplicates });
    if (isJsonObject(value) && (!duplicates || !hasDuplicateMembers(value, duplicates))) return value;
  } catch {
    // Use the same error for malformed JSON and non-object bodies.
  }
  throw invalid("body must be a JSON object");
}

function alreadyInitialized(sql: TransactionSQL, memberID: string, hash: ArrayBuffer): boolean {
  if (!team(sql)) return false;
  // A membership's credential is immutable. A removal ends that membership even if its ID is reused.
  const [lead] = sql.exec<{ token_hash: ArrayBuffer }>(`SELECT token_hash FROM members
    JOIN audit_records AS genesis ON genesis.seq = 1 AND genesis.action = 'member.added'
      AND genesis.target = 'member:' || member_id
    WHERE member_id = ? AND role = 'lead' AND NOT EXISTS (
      SELECT 1 FROM audit_records AS removal
      WHERE removal.seq > genesis.seq AND removal.action = 'member.removed' AND removal.target = genesis.target
    )`, memberID);
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
    insertSigningKey(sql, policy, "active");
    insertSigningKey(sql, audit, "active");
    sql.exec("INSERT INTO team (singleton, workspace_id, policy_kid, audit_kid) VALUES (1, ?, ?, ?)",
      crypto.randomUUID(), policy.kid, audit.kid);
    insertMember(sql, id, "lead", hash);
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
    if (sql.exec("SELECT 1 FROM members WHERE member_id = ?", id).length || memberReserved(sql, id, Date.now())) {
      throw conflict("member " + JSON.stringify(id));
    }
    const token = newToken();
    insertMember(sql, id, role, tokenHash(token));
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

export async function stageKey(request: Request, storage: HubStorage, purpose: SigningPurpose,
  env: Pick<Env, "TEAM_ID" | "WRAP_KEY_V1">) {
  const hash = bearer(request);
  await cappedBody(request, adminLimit);
  function pending(sql: TransactionSQL) {
    const [row] = sql.exec<{ public_jwk: string }>(
      "SELECT public_jwk FROM signing_keys WHERE purpose = ? AND status = 'staged'", purpose);
    return row;
  }

  const existing = storage.transaction((sql) => {
    authenticate(sql, hash, true);
    return pending(sql);
  });
  if (existing) return JSON.parse(existing.public_jwk);

  const row = await newSigningKey(env.TEAM_ID, purpose, wrappingKey(env.WRAP_KEY_V1));
  return storage.transaction((sql) => {
    const actor = authenticate(sql, hash, true);
    const staged = pending(sql);
    if (staged) return JSON.parse(staged.public_jwk);
    insertSigningKey(sql, row, "staged");
    appendAudit(sql, { action: "key.staged", target: "key:" + row.kid, actor_member_id: actor, meta: { purpose } });
    return JSON.parse(row.public_jwk);
  });
}

export async function activateKey(request: Request, storage: HubStorage, purpose: SigningPurpose) {
  const hash = bearer(request);
  const body = await cappedBody(request, adminLimit);
  return storage.transaction((sql) => {
    const actor = authenticate(sql, hash, true);
    const { kid } = adminJSON(body, new WeakSet());
    if (typeof kid !== "string" || !kid) throw invalid("activation needs a kid");
    const [key] = sql.exec<{ status: "staged" | "active" | "retired" }>(
      "SELECT status FROM signing_keys WHERE kid = ? AND purpose = ?", kid, purpose);
    if (!key) throw notFound();
    if (key.status === "retired") throw conflict("key is retired");
    if (key.status === "active") return {};

    const column = purpose === "policy" ? "policy_kid" : "audit_kid";
    const previous = activeSigningKey(sql, purpose, activeTeam(sql)[column]);
    sql.exec(`UPDATE signing_keys SET status = 'retired', wrapped_pkcs8 = NULL, nonce = NULL, wrap_key_id = NULL
      WHERE kid = ?`, previous.kid);
    sql.exec("UPDATE signing_keys SET status = 'active' WHERE kid = ?", kid);
    sql.exec(`UPDATE team SET ${column} = ? WHERE singleton = 1`, kid);
    appendAudit(sql, { action: "key.activated", target: "key:" + kid, actor_member_id: actor,
      meta: { purpose, previous_kid: previous.kid } });
    return {};
  });
}

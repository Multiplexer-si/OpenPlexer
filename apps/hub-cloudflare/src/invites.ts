import { Buffer } from "node:buffer";
import { adminJSON, validID } from "./admin";
import { bearer, cappedBody, HubError, invalid, json, notFound, tokenHash } from "./http";
import { activeTeam, appendAudit, authenticate, insertMember, memberReserved, newToken } from "./state";
import type { Role } from "./state";
import type { HubStorage, TransactionSQL } from "./storage";

const inviteLimit = 64 << 10;
const liveLimit = 1024;
const pageLimit = 100;
const day = 86_400_000;
const retention = 30 * day;
const inviteID = /^[0-9a-f]{32}$/;
const hashPattern = /^[0-9a-f]{64}$/;
const codePattern = /^[a-zA-Z0-9_-]{42}[AEIMQUYcgkosw048]$/;

type StoredState = "issued" | "pending_approval" | "approved" | "redeemed" | "revoked";
type InviteRow = {
  id: string;
  project_id: string;
  member_id: string;
  hub_role: Role;
  require_approval: number;
  state: StoredState;
  created_at_ms: number;
  expires_at_ms: number;
};
type StoredInvite = InviteRow & {
  claim_hash: ArrayBuffer | null;
  ended_at_ms: number | null;
  enrollment_id: string | null;
  repo: string | null;
};
type InviteRoute = { kind: "collection" | "redeem" } | { kind: "approve" | "revoke"; id: string };

const inviteConflict = () => new HubError(409, "already exists or conflicts with current state");
const badInvite = () => invalid("invalid invite request");

export async function handleInvite(request: Request, storage: HubStorage, route: InviteRoute, clock = Date.now): Promise<Response> {
  const methods = route.kind === "collection" ? ["GET", "HEAD", "POST"] : [route.kind === "revoke" ? "DELETE" : "POST"];
  if (!methods.includes(request.method)) throw new HubError(405, "method not allowed", { Allow: methods.join(", ") });
  const hash = route.kind === "redeem" ? undefined : bearer(request);
  const body = await cappedBody(request, inviteLimit);
  return storage.transaction((sql) => {
    const now = clock();
    if (hash === undefined) {
      activeTeam(sql);
      return redeemInvite(sql, body, now);
    }
    const actor = authenticate(sql, hash, true);
    if (route.kind === "collection" && request.method === "POST") {
      return createInvite(sql, body, now, actor);
    }
    if (body.length) throw badInvite();
    if (route.kind === "collection") return listInvites(sql, request.url, now);
    if (route.kind === "approve" || route.kind === "revoke") return changeInvite(sql, route, now, actor);
    throw new Error("Invalid invite route");
  });
}

function readBody(body: Uint8Array, allowed: string[]) {
  let value;
  try {
    value = adminJSON(body, new WeakSet());
  } catch {
    throw badInvite();
  }
  if (Object.keys(value).some((key) => !allowed.includes(key) || value[key] === null)) throw badInvite();
  return value;
}

function present(row: InviteRow, now: number) {
  const { id, project_id, member_id, hub_role, require_approval, created_at_ms, expires_at_ms } = row;
  const state = row.state === "redeemed" || row.state === "revoked" || now < expires_at_ms ? row.state : "expired";
  return {
    id, project_id, member_id, hub_role, state, require_approval: require_approval === 1,
    created_at: new Date(created_at_ms).toISOString(), expires_at: new Date(expires_at_ms).toISOString(),
  };
}

function auditInvite(sql: TransactionSQL, row: InviteRow, action: string, actor: string, now: number) {
  const { project_id, member_id, hub_role } = row;
  appendAudit(sql, { action, actor_member_id: actor, target: "invite:" + row.id,
    meta: { project_id, member_id, hub_role } }, new Date(now).toISOString());
}

function purgeInvites(sql: TransactionSQL, now: number) {
  sql.exec("DELETE FROM invites WHERE COALESCE(ended_at_ms, expires_at_ms) <= ?", now - retention);
}

function createInvite(sql: TransactionSQL, body: Uint8Array, now: number, actor: string) {
  const { project_id, member_id, hub_role = "member", expires_in_seconds = 172800, require_approval = false } =
    readBody(body, ["project_id", "member_id", "hub_role", "expires_in_seconds", "require_approval"]);
  if (!validID(project_id) || !validID(member_id) || (hub_role !== "member" && hub_role !== "lead") ||
      typeof require_approval !== "boolean" || typeof expires_in_seconds !== "number" ||
      !Number.isInteger(expires_in_seconds) || expires_in_seconds < 60 || expires_in_seconds > 604800) {
    throw badInvite();
  }
  if (!sql.exec("SELECT 1 FROM projects WHERE project_id = ?", project_id).length) throw notFound();
  const [{ count }] = sql.exec<{ count: number }>("SELECT count(*) AS count FROM invites WHERE ended_at_ms IS NULL AND expires_at_ms > ?", now);
  if (count >= liveLimit || memberReserved(sql, member_id, now) ||
      sql.exec("SELECT 1 FROM members WHERE member_id = ?", member_id).length) throw inviteConflict();
  purgeInvites(sql, now);
  const id = Buffer.from(crypto.getRandomValues(new Uint8Array(16))).toString("hex");
  const code = newToken();
  const row: InviteRow = {
    id, project_id, member_id, hub_role, require_approval: Number(require_approval), state: "issued",
    created_at_ms: now, expires_at_ms: now + expires_in_seconds * 1000,
  };
  sql.exec(`INSERT INTO invites
    (id, project_id, member_id, hub_role, require_approval, state, created_at_ms, expires_at_ms, code_hash)
    VALUES (?, ?, ?, ?, ?, 'issued', ?, ?, ?)`,
  id, project_id, member_id, hub_role, row.require_approval, now, row.expires_at_ms, tokenHash(code));
  auditInvite(sql, row, "invite.created", actor, now);
  return json({ invite: present(row, now), code }, 201);
}

function pageQuery(url: string) {
  const raw = new URL(url).search.slice(1);
  const params = new Map<string, string>();
  for (const param of raw.split("&")) {
    if (!param) continue;
    if (param.includes(";")) throw badInvite();
    const separator = param.indexOf("=");
    const [name, value] = separator === -1 ? [param, ""] : [param.slice(0, separator), param.slice(separator + 1)];
    try {
      const key = decodeURIComponent(name.replaceAll("+", " "));
      if ((key !== "after" && key !== "limit") || params.has(key)) throw badInvite();
      params.set(key, decodeURIComponent(value.replaceAll("+", " ")));
    } catch {
      throw badInvite();
    }
  }
  const after = params.get("after");
  const limit = params.get("limit") ?? String(pageLimit);
  if ((after !== undefined && !inviteID.test(after)) || !/^\d+$/.test(limit) ||
      Number(limit) < 1 || Number(limit) > pageLimit) throw badInvite();
  return { after: after ?? "", limit: Number(limit) };
}

function listInvites(sql: TransactionSQL, url: string, now: number) {
  const { after, limit } = pageQuery(url);
  purgeInvites(sql, now);
  const rows = sql.exec<InviteRow>(`SELECT id, project_id, member_id, hub_role, require_approval, state, created_at_ms, expires_at_ms
    FROM invites WHERE id > ? ORDER BY id LIMIT ?`, after, limit + 1);
  return json({ invites: rows.slice(0, limit).map((row) => present(row, now)),
    next_cursor: rows.length > limit ? rows[limit - 1].id : null });
}

function changeInvite(sql: TransactionSQL, route: { kind: "approve" | "revoke"; id: string }, now: number, actor: string) {
  if (!inviteID.test(route.id)) throw badInvite();
  const [row] = sql.exec<StoredInvite>("SELECT * FROM invites WHERE id = ? AND COALESCE(ended_at_ms, expires_at_ms) > ?",
    route.id, now - retention);
  if (!row) throw notFound();
  const invite = present(row, now);
  if (route.kind === "revoke") {
    if (invite.state === "revoked") return json({ invite });
    if (invite.state === "redeemed" || invite.state === "expired") throw inviteConflict();
    sql.exec("UPDATE invites SET state = 'revoked', ended_at_ms = ?, claim_hash = NULL WHERE id = ?", now, row.id);
    invite.state = "revoked";
    auditInvite(sql, row, "invite.revoked", actor, now);
  } else {
    if (invite.state === "approved" || (invite.state === "redeemed" && invite.require_approval)) return json({ invite });
    if (invite.state !== "pending_approval") throw inviteConflict();
    sql.exec("UPDATE invites SET state = 'approved' WHERE id = ?", row.id);
    invite.state = "approved";
    auditInvite(sql, row, "invite.approved", actor, now);
  }
  return json({ invite });
}

function redeemInvite(sql: TransactionSQL, body: Uint8Array, now: number) {
  const { code, token_hash } = readBody(body, ["code", "token_hash"]);
  if (typeof code !== "string" || !codePattern.test(code) ||
      typeof token_hash !== "string" || !hashPattern.test(token_hash)) throw badInvite();
  const [row] = sql.exec<StoredInvite>("SELECT * FROM invites WHERE code_hash = ? AND COALESCE(ended_at_ms, expires_at_ms) > ?",
    tokenHash(code), now - retention);
  if (!row) throw notFound();
  const { state, project_id, member_id, hub_role } = present(row, now);
  if (state === "revoked" || state === "expired") throw new HubError(410, "invite expired or revoked");
  const claim = Uint8Array.from(Buffer.from(token_hash, "hex")).buffer;
  if (row.claim_hash && !crypto.subtle.timingSafeEqual(row.claim_hash, claim)) throw inviteConflict();

  const [member] = sql.exec<{ enrollment_id: string; role: Role; token_hash: ArrayBuffer }>(
    "SELECT enrollment_id, role, token_hash FROM members WHERE member_id = ?", member_id);
  const receipt = { state: "redeemed", project_id, member_id, hub_role };
  if (state === "redeemed") {
    if (row.ended_at_ms === null || now >= row.ended_at_ms + day ||
        !member || member.enrollment_id !== row.enrollment_id || member.role !== hub_role ||
        !crypto.subtle.timingSafeEqual(member.token_hash, claim)) throw inviteConflict();
    return json({ ...receipt, repo: row.repo });
  }
  const pending = () => json({ state: "pending_approval", invite_id: row.id }, 202, { "Retry-After": "5" });
  if (state === "pending_approval") return pending();

  const [project] = sql.exec<{ repo: string }>("SELECT repo FROM projects WHERE project_id = ?", project_id);
  if (!project || member || sql.exec("SELECT 1 FROM members WHERE token_hash = ?", claim).length) throw inviteConflict();
  if (row.require_approval && state === "issued") {
    sql.exec("UPDATE invites SET state = 'pending_approval', claim_hash = ? WHERE id = ?", claim, row.id);
    auditInvite(sql, row, "invite.requested", member_id, now);
    return pending();
  }
  const enrollment = insertMember(sql, member_id, hub_role, claim);
  sql.exec(`UPDATE invites SET state = 'redeemed', ended_at_ms = ?, claim_hash = ?, enrollment_id = ?, repo = ?
    WHERE id = ?`, now, claim, enrollment, project.repo, row.id);
  auditInvite(sql, row, "invite.redeemed", member_id, now);
  appendAudit(sql, { action: "member.added", target: "member:" + member_id, actor_member_id: null,
    meta: { role: hub_role, invite_id: row.id } }, new Date(now).toISOString());
  return json({ ...receipt, repo: project.repo });
}

import { Buffer } from "node:buffer";
import { seal } from "./audit";
import type { UnsealedRecord } from "./audit";
import { bearer, forbidden, HubError, unauthorized } from "./http";
import {
  exportPKCS8, exportPublicJWK, generateSigningKey, importPKCS8,
} from "./jws";
import type { SigningPurpose } from "./jws";
import type { TransactionSQL } from "./storage";
import { loadWrappingKey, unwrapPKCS8, wrapPKCS8 } from "./wrap";
import type { WrapKeyBindings } from "./wrap";

type Team = {
  workspace_id: string;
  policy_kid: string;
  audit_kid: string;
  audit_seq: number;
  audit_tip: string;
};

export function team(sql: TransactionSQL): Team | undefined {
  const [row] = sql.exec<Team>("SELECT workspace_id, policy_kid, audit_kid, audit_seq, audit_tip FROM team WHERE singleton = 1");
  return row;
}

export function activeTeam(sql: TransactionSQL): Team {
  const current = team(sql);
  if (!current) throw new Error("Missing team");
  return current;
}

export type Role = "lead" | "member";
type Enrollment = { member_id: string; role: Role; enrollment_id: string };
type Member = Enrollment & { token_hash: ArrayBuffer };

export function requireLead(role: Role): void {
  if (role !== "lead") throw forbidden();
}

export function authenticateEnrollment(sql: TransactionSQL, hash: ArrayBuffer, leadOnly: boolean): Enrollment {
  activeTeam(sql);
  for (const member of sql.exec<Member>("SELECT member_id, role, enrollment_id, token_hash FROM members")) {
    if (crypto.subtle.timingSafeEqual(hash, member.token_hash)) {
      if (leadOnly) requireLead(member.role);
      return { member_id: member.member_id, role: member.role, enrollment_id: member.enrollment_id };
    }
  }
  throw unauthorized();
}

export function authenticate(sql: TransactionSQL, hash: ArrayBuffer, leadOnly = false): string {
  return authenticateEnrollment(sql, hash, leadOnly).member_id;
}

export type LeadAuthorization = (sql: TransactionSQL) => string;

export function bearerLead(request: Request): LeadAuthorization {
  const hash = bearer(request);
  return (sql) => authenticate(sql, hash, true);
}

export function appendAudit(sql: TransactionSQL, action: Pick<UnsealedRecord, "action" | "target" | "actor_member_id">
    & Partial<Pick<UnsealedRecord, "meta" | "agent_session_id" | "reason" | "flagged">>,
  createdAt?: string,
): void {
  const current = activeTeam(sql);
  const seq = current.audit_seq + 1;
  const record = seal({
    agent_session_id: null, reason: null, flagged: false, meta: null, ...action,
    id: String(seq), workspace_id: current.workspace_id, created_at: createdAt ?? new Date().toISOString(),
  }, current.audit_tip);
  sql.exec(`INSERT INTO audit_records
    (seq, workspace_id, actor_member_id, agent_session_id, reason, action, target, meta_json, flagged, created_at, prev_hash, hash)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
  seq, record.workspace_id, record.actor_member_id, record.agent_session_id, record.reason, record.action,
  record.target, JSON.stringify(record.meta), Number(record.flagged), record.created_at, record.prev_hash, record.hash);
  sql.exec("UPDATE team SET audit_seq = ?, audit_tip = ? WHERE singleton = 1", seq, record.hash);
}

export function insertMember(sql: TransactionSQL, id: string, role: Role, hash: ArrayBuffer): string {
  const enrollment = crypto.randomUUID();
  sql.exec("INSERT INTO members (member_id, role, token_hash, enrollment_id) VALUES (?, ?, ?, ?)", id, role, hash, enrollment);
  return enrollment;
}

export function memberReserved(sql: TransactionSQL, id: string, now: number): boolean {
  return sql.exec("SELECT 1 FROM invites WHERE member_id = ? AND ended_at_ms IS NULL AND expires_at_ms > ?", id, now).length > 0;
}

export function newToken(): string {
  return Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString("base64url");
}

export async function newSigningKey(teamID: string, purpose: SigningPurpose, wrapKey: ReturnType<typeof loadWrappingKey>) {
  const pair = await generateSigningKey();
  const jwk = await exportPublicJWK(pair.publicKey, purpose);
  const wrapped = await wrapPKCS8(await exportPKCS8(pair.privateKey), wrapKey.bytes, { team: teamID, purpose, kid: jwk.kid });
  return {
    kid: jwk.kid, purpose, public_jwk: JSON.stringify(jwk),
    wrapped_pkcs8: wrapped.ciphertext, nonce: wrapped.nonce.buffer, wrap_key_id: wrapKey.id,
  };
}

export function insertSigningKey(sql: TransactionSQL, row: Awaited<ReturnType<typeof newSigningKey>>, status: "active" | "staged") {
  sql.exec(`INSERT INTO signing_keys (kid, purpose, public_jwk, status, wrapped_pkcs8, nonce, wrap_key_id)
    VALUES (?, ?, ?, ?, ?, ?, ?)`,
  row.kid, row.purpose, row.public_jwk, status, row.wrapped_pkcs8, row.nonce, row.wrap_key_id);
}

export function publicKeys(sql: TransactionSQL, purpose: SigningPurpose) {
  activeTeam(sql);
  const keys = sql.exec<{ public_jwk: string }>("SELECT public_jwk FROM signing_keys WHERE purpose = ? ORDER BY kid", purpose);
  if (!keys.length) throw new Error("Missing signing keys");
  return { keys: keys.map((row) => JSON.parse(row.public_jwk)) };
}

export type SigningKeyRow = {
  kid: string; purpose: SigningPurpose; wrapped_pkcs8: ArrayBuffer; nonce: ArrayBuffer; wrap_key_id: string;
};

export function activeSigningKey(sql: TransactionSQL, purpose: SigningPurpose, kid: string): SigningKeyRow {
  const [key] = sql.exec<SigningKeyRow>(`SELECT kid, purpose, wrapped_pkcs8, nonce, wrap_key_id FROM signing_keys
    WHERE kid = ? AND purpose = ? AND status = 'active'`, kid, purpose);
  if (!key?.wrapped_pkcs8 || !key.nonce || !key.wrap_key_id) throw new Error("Missing active signing key");
  return key;
}

export async function unwrapSigningKey(row: SigningKeyRow, teamID: string, bindings: WrapKeyBindings): Promise<ArrayBuffer> {
  const key = loadWrappingKey(bindings, row.wrap_key_id);
  try {
    return await unwrapPKCS8({ ciphertext: row.wrapped_pkcs8, nonce: new Uint8Array(row.nonce) },
      key.bytes, { team: teamID, purpose: row.purpose, kid: row.kid });
  } catch {
    throw new HubError(500, `Unable to unwrap ${row.purpose} signing key with ${key.binding}`);
  }
}

export async function signingKey(row: SigningKeyRow, teamID: string, bindings: WrapKeyBindings): Promise<CryptoKey> {
  return importPKCS8(await unwrapSigningKey(row, teamID, bindings));
}

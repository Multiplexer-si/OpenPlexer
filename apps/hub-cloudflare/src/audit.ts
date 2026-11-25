import { createHash } from "node:crypto";
import { canonicalJSON } from "./canonical.ts";
import type { JsonObject } from "./canonical.ts";
import { parseCanonicalObject, signJWS, verifyJWS } from "./jws.ts";

export const genesisHash = "0".repeat(64);

export type UnsealedRecord = {
  id: string;
  workspace_id: string;
  actor_member_id: string | null;
  agent_session_id: string | null;
  action: string;
  target: string;
  reason: string | null;
  meta: JsonObject | null;
  flagged: boolean;
  created_at: string;
};

export type AuditRecord = UnsealedRecord & { meta: JsonObject; prev_hash: string; hash: string };

const hashedFields = [
  "workspace_id", "actor_member_id", "agent_session_id", "action", "target",
  "reason", "meta", "flagged", "created_at",
];

export function auditHashInput(record: JsonObject, prevHash: string): string {
  const fields = hashedFields.map((name) => {
    if (!Object.hasOwn(record, name)) throw new Error(`Missing audit field ${name}`);
    return [name, record[name]];
  });
  return prevHash + canonicalJSON(Object.fromEntries(fields));
}

export function auditHash(record: JsonObject, prevHash: string): string {
  return createHash("sha256").update(auditHashInput(record, prevHash)).digest("hex");
}

export function seal(record: UnsealedRecord, prevHash: string): AuditRecord {
  const fields = { ...record, meta: record.meta ?? {} };
  return { ...fields, prev_hash: prevHash, hash: auditHash(fields, prevHash) };
}

export type ChainResult = { ok: true; entries: number } | { ok: false; broken_at: string };

export function verifyChain(records: readonly JsonObject[]): ChainResult {
  let previous = genesisHash;
  for (const record of records) {
    const { id, prev_hash, hash } = record;
    if (typeof id !== "string" || typeof prev_hash !== "string" || typeof hash !== "string") {
      throw new Error("Expected a string id, prev_hash, and hash");
    }
    const computed = auditHash(record, previous);
    if (prev_hash !== previous || hash !== computed) return { ok: false, broken_at: id };
    previous = hash;
  }
  return { ok: true, entries: records.length };
}

export type Checkpoint = {
  v: 1;
  workspace_id: string;
  entries: number;
  tip_hash: string;
  issued_at: string;
};

export class CheckpointError extends Error {
  readonly rule: "K5" | "K6";

  constructor(rule: "K5" | "K6", cause?: unknown) {
    super(`Invalid checkpoint payload (${rule})`, { cause });
    this.rule = rule;
  }
}

function checkpointFields(body: JsonObject): Checkpoint {
  if (body.v !== 1) throw new CheckpointError("K5");
  const { workspace_id, entries, tip_hash, issued_at } = body;
  if (Object.keys(body).length !== 5 || typeof workspace_id !== "string"
      || typeof entries !== "number" || !Number.isSafeInteger(entries) || entries < 0
      || typeof tip_hash !== "string" || tip_hash.length !== 64 || /[^0-9a-f]/.test(tip_hash)
      || typeof issued_at !== "string" || issued_at.length !== 24 || issued_at.startsWith("0000")) {
    throw new CheckpointError("K6");
  }
  const date = new Date(issued_at);
  if (!Number.isFinite(date.valueOf()) || date.toISOString() !== issued_at) throw new CheckpointError("K6");
  return { v: 1, workspace_id, entries, tip_hash, issued_at };
}

export function buildCheckpoint(fields: Omit<Checkpoint, "v">): Checkpoint {
  return checkpointFields({ ...fields, v: 1 });
}

export function signCheckpoint(checkpoint: Checkpoint, key: CryptoKey, kid: string): Promise<string> {
  return signJWS(checkpointFields(checkpoint), key, kid, "audit");
}

export async function verifyCheckpoint(token: string, jwks: string): Promise<Checkpoint> {
  const bytes = await verifyJWS(token, jwks, "audit");
  let payload: JsonObject;
  try {
    payload = parseCanonicalObject(bytes);
  } catch (cause) {
    throw new CheckpointError("K6", cause);
  }
  return checkpointFields(payload);
}

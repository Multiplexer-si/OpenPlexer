import type { Json } from "./output.ts";
import type { Reference } from "./reference.ts";

/** The fields of a record that the hash covers. */
export interface AuditFields {
  workspace_id: string;
  actor_member_id: string | null;
  agent_session_id: string | null;
  action: string;
  target: string;
  reason: string | null;
  meta: { [key: string]: Json };
  flagged: boolean;
  created_at: string;
}

export interface AuditRecord extends AuditFields {
  id: string;
  prev_hash: string;
  hash: string;
}

export type ChainVerification = { ok: true; entries: number } | { ok: false; broken_at: string };

/** Links the entries into a chain with the hash function of the reference, starting from its genesis hash. Ids count from 1. */
export function buildChain(reference: Reference, entries: AuditFields[]): AuditRecord[] {
  const records: AuditRecord[] = [];
  let prev = reference.genesisHash;
  for (const [i, fields] of entries.entries()) {
    const hash = reference.computeAuditHash(prev, fields);
    records.push({ id: String(i + 1), ...fields, prev_hash: prev, hash });
    prev = hash;
  }
  return records;
}

/**
 * The verification loop of the reference (`apps/hub/src/audit/verify.ts`), over a list of records and not over
 * database rows. The reference loop cannot run without Postgres. The hash function is the one of the reference.
 */
export function verifyChain(reference: Reference, records: AuditRecord[]): ChainVerification {
  let prev = reference.genesisHash;
  for (const record of records) {
    const expected = reference.computeAuditHash(prev, record);
    if (record.prev_hash !== prev || record.hash !== expected) return { ok: false, broken_at: record.id };
    prev = record.hash;
  }
  return { ok: true, entries: records.length };
}

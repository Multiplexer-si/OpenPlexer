import { auditHash, CheckpointError, genesisHash, verifyCheckpoint } from "../src/audit";
import { hasDuplicateMembers, isJsonObject } from "../src/canonical";
import type { JsonObject, JsonValue } from "../src/canonical";
import { JWSError } from "../src/jws";

const isString = (value: JsonValue | undefined): value is string => typeof value === "string";
const nullableString = (value: JsonValue | undefined): boolean => value === null || isString(value);

function validEntry(entry: JsonValue): entry is JsonObject {
  if (!isJsonObject(entry)) return false;
  return ["id", "workspace_id", "action", "target", "created_at", "prev_hash", "hash", "result"]
    .every((key) => isString(entry[key]))
    && ["actor_member_id", "agent_session_id", "reason"].every((key) => nullableString(entry[key]))
    && (!Object.hasOwn(entry, "agent_member_id") || nullableString(entry.agent_member_id))
    && isJsonObject(entry.meta) && typeof entry.flagged === "boolean";
}

const checkpointRules = { compact: "K1", header: "K2", key: "K3", signature: "K4", payload: "K6" };

export async function verifyExportFixture(
  doc: JsonValue, duplicates: WeakSet<JsonObject>, jwks: string, tokens: string[],
) {
  const fail = (rule: string, fields = {}) => ({ ok: false, rule, ...fields });
  const location = (record: JsonValue, index: number) => ({
    record_number: index + 1,
    ...(isJsonObject(record) && isString(record.id) ? { broken_at: record.id } : {}),
  });
  if (!isJsonObject(doc) || !Array.isArray(doc.entries) || doc.next_cursor !== null || duplicates.has(doc)
      || Object.entries(doc).some(([name, value]) => name !== "entries" && hasDuplicateMembers(value, duplicates))) {
    return fail("A6");
  }

  const records = doc.entries.toReversed();
  let previous = genesisHash;
  for (const [i, record] of records.entries()) {
    if (!validEntry(record) || hasDuplicateMembers(record, duplicates)) return fail("A1", location(record, i));
    if (record.prev_hash !== previous) return fail(i === 0 ? "A4" : "A5", location(record, i));
    const computed = auditHash(record, previous);
    if (record.hash !== computed) return fail("A3", location(record, i));
    previous = computed;
  }

  const claims = [];
  for (const [i, token] of tokens.entries()) {
    const checkpoint = i + 1;
    try {
      const claim = await verifyCheckpoint(token, jwks);
      const mismatch = records.findIndex((record) => !isJsonObject(record) || record.workspace_id !== claim.workspace_id);
      if (mismatch >= 0) return fail("K7", { checkpoint, ...location(records[mismatch], mismatch) });
      claims.push(claim);
    } catch (error) {
      if (error instanceof JWSError) return fail(checkpointRules[error.stage], { checkpoint });
      if (error instanceof CheckpointError) return fail(error.rule, { checkpoint });
      throw error;
    }
  }

  const tips = new Map<string, string>();
  for (const [i, claim] of claims.entries()) {
    const key = JSON.stringify([claim.workspace_id, claim.entries]);
    if (tips.has(key) && tips.get(key) !== claim.tip_hash) return fail("K8", { checkpoint: i + 1 });
    tips.set(key, claim.tip_hash);
  }

  let covered: number | null = null;
  for (const [i, claim] of claims.entries()) {
    const checkpoint = i + 1;
    if (claim.entries > records.length) return fail("K9", { checkpoint });
    const last = records[claim.entries - 1];
    if ((isJsonObject(last) ? last.hash : genesisHash) !== claim.tip_hash) {
      return fail("K10", { checkpoint, ...(last === undefined ? {} : location(last, claim.entries - 1)) });
    }
    covered = Math.max(covered ?? 0, claim.entries);
  }
  return {
    ok: true, entries: records.length, checkpoint_entries: covered,
    completeness: covered === null ? "not_proven" : "through_checkpoint",
  };
}

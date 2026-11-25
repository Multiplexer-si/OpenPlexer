import { auditHash, CheckpointError, genesisHash, verifyCheckpoint } from "../src/audit";
import { hasDuplicateMembers, isJsonObject, parseJSON } from "../src/canonical";
import type { JsonObject, JsonValue } from "../src/canonical";
import { JWSError, verifyJWS } from "../src/jws";

const isString = (value: JsonValue | undefined): value is string => typeof value === "string";
const strings = (value: JsonValue | undefined): boolean => Array.isArray(value) && value.every(isString);
const nullableString = (value: JsonValue | undefined): boolean => value === null || isString(value);

function validGrant(grant: JsonValue): boolean {
  if (!isJsonObject(grant) || !isString(grant.member_id) || !grant.member_id || !isString(grant.area) || !grant.area
      || !Array.isArray(grant.actions) || grant.actions.length < 1 || grant.actions.length > 2
      || !grant.actions.every((action) => action === "read" || action === "write")) return false;
  const fields = ["member_id", "area", "actions", "scope"];
  if (grant.scope === "mission" && isString(grant.mission_id) && grant.mission_id) {
    fields.push("mission_id");
  } else if (grant.scope === "until" && isString(grant.expires_at)) {
    const date = /^(\d{4}-\d\d-\d\dT(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d)(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/.exec(grant.expires_at);
    if (!date || date[0] !== grant.expires_at) return false;
    const utc = new Date(date[1] + ".000Z");
    if (!Number.isFinite(utc.valueOf()) || !utc.toISOString().startsWith(date[1])) return false;
    fields.push("expires_at");
  } else {
    return false;
  }
  return Object.keys(grant).every((name) => fields.includes(name));
}

// Bundle shape validation belongs with policy evaluation. The fixture verifier
// checks it here after signature verification, without adding hub policy logic.
function validBundle(bundle: JsonValue): boolean {
  if (!isJsonObject(bundle) || !Number.isSafeInteger(bundle.version) || !isString(bundle.project_id)
      || !strings(bundle.repos) || !isJsonObject(bundle.members) || !Object.values(bundle.members).every(isString)
      || !isJsonObject(bundle.roles) || !Array.isArray(bundle.areas)) return false;
  if (!bundle.areas.every((area) => isJsonObject(area) && isString(area.name) && strings(area.patterns)
      && ["private", "system", "generated"].every((name) => !Object.hasOwn(area, name) || typeof area[name] === "boolean")
      && (!Object.hasOwn(area, "owner_team_id") || isString(area.owner_team_id)))) return false;
  if (!Object.values(bundle.roles).every((role) => isJsonObject(role) && strings(role.read) && strings(role.write)
      && strings(role.exec) && (!Object.hasOwn(role, "push") || strings(role.push)))) return false;
  const grants = bundle.temporary_grants;
  return !Object.hasOwn(bundle, "temporary_grants")
    || (Array.isArray(grants) && grants.length <= 200 && grants.every(validGrant));
}

export async function verifyBundleFixture(token: string, jwks: string) {
  try {
    const bytes = await verifyJWS(token, jwks, "policy");
    const bundle = parseJSON(new TextDecoder().decode(bytes), { loneSurrogates: "reject" });
    if (validBundle(bundle)) return { ok: true, bundle };
    return { ok: false };
  } catch (error) {
    if (error instanceof JWSError || error instanceof SyntaxError) return { ok: false };
    throw error;
  }
}

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

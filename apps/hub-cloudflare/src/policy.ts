import { isJsonObject, parseJSON } from "./canonical.ts";
import type { JsonObject, JsonValue } from "./canonical.ts";
import { JWSError, verifyJWS } from "./jws.ts";

const isString = (value: JsonValue | undefined): value is string => typeof value === "string";
const strings = (value: JsonValue | undefined): boolean => Array.isArray(value) && value.every(isString);

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

function validBundle(bundle: JsonValue): bundle is JsonObject {
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

export async function verifyPolicyBundle(token: string, jwks: string): Promise<{
  bundle: JsonObject;
  payload: Uint8Array;
}> {
  const bytes = await verifyJWS(token, jwks, "policy");
  const text = new TextDecoder().decode(bytes);
  try {
    const bundle = parseJSON(text, { loneSurrogates: "reject" });
    if (!validBundle(bundle)) throw new SyntaxError("Invalid policy bundle fields");
    return { bundle, payload: new TextEncoder().encode(text) };
  } catch (cause) {
    throw new JWSError("payload", cause);
  }
}

import { isJsonObject, LoneSurrogateError, parseJSON } from "./canonical.ts";
import type { JsonObject, JsonValue } from "./canonical.ts";
import { JWSError, verifyJWS } from "./jws.ts";

type PolicyBundle = {
  bundle: JsonObject;
  version: number;
  project: string;
  repos: string[];
  members: string[];
  grantMembers: string[];
};

const decoder = new TextDecoder("utf-8", { fatal: false, ignoreBOM: true });

// Go's JSON decoder distinguishes a malformed first value from trailing text.
function hasTrailingText(text: string): boolean {
  const start = text.replace(/^[ \t\r\n]+/, "");
  const tokens = /"(?:[^"\\]|\\[^])*"|[{}\[\]]/g;
  let end = 0;
  if (start[0] === "{" || start[0] === "[") {
    let depth = 0;
    for (const { 0: token, index } of start.matchAll(tokens)) {
      if (token === "{" || token === "[") depth++;
      if ((token === "}" || token === "]") && --depth === 0) {
        end = index + 1;
        break;
      }
    }
  } else {
    end = /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?|"(?:[^"\\]|\\[^])*")/
      .exec(start)?.[0].length ?? 0;
    // A partial fraction or exponent is a malformed number.
    if (/^[-0-9]/.test(start) && /[.eE]/.test(start.charAt(end))) return false;
  }
  if (!end || !/[^ \t\r\n]/.test(start.slice(end))) return false;
  try {
    parseJSON(start.slice(0, end));
    return true;
  } catch {
    return false;
  }
}

function compareMemberNames(left: string, right: string): number {
  // Go sorts dictionary names by UTF-8, which preserves code point order.
  for (let i = 0; i < Math.min(left.length, right.length); i++) {
    if (left[i] !== right[i]) return (left.codePointAt(i) ?? 0) - (right.codePointAt(i) ?? 0);
  }
  return left.length - right.length;
}

export function parsePolicyBundle(input: string | Uint8Array): PolicyBundle {
  const text = typeof input === "string" ? input : decoder.decode(input);
  let document: JsonValue;
  try {
    document = parseJSON(text, { loneSurrogates: "reject" });
  } catch (error) {
    if (error instanceof LoneSurrogateError) {
      throw new SyntaxError("holds an escape of a lone surrogate, such as \\ud800, which is not a character");
    }
    throw new SyntaxError(hasTrailingText(text) ? "has text after the JSON value" : "is not valid JSON");
  }
  if (!isJsonObject(document)) throw new SyntaxError("must be an object");

  const issues: string[] = [];
  const fail = (path: string, reason: string) => issues.push(path + " " + reason);
  type Read<T> = (path: string, value: JsonValue) => T;
  function field<T>(object: JsonObject, path: string, name: string, read: Read<T>, required = true): T | undefined {
    const fieldPath = path ? path + "." + name : name;
    if (Object.hasOwn(object, name)) return read(fieldPath, object[name]);
    if (required) fail(fieldPath, "is required");
  }
  function object(path: string, value: JsonValue): JsonObject | undefined {
    if (isJsonObject(value)) return value;
    fail(path, "must be an object");
  }
  function string(path: string, value: JsonValue): string {
    if (typeof value === "string") return value;
    fail(path, "must be a string");
    return "";
  }
  function nonempty(path: string, value: JsonValue): string {
    if (typeof value === "string" && value) return value;
    fail(path, "must be a string that is not empty");
    return "";
  }
  function flag(path: string, value: JsonValue): void {
    if (typeof value !== "boolean") fail(path, "must be true or false");
  }
  function list<T>(read: Read<T>): Read<T[]> {
    return (path, value) => {
      if (Array.isArray(value)) return value.map((entry, i) => read(`${path}[${i}]`, entry));
      fail(path, "must be a list");
      return [];
    };
  }
  function dictionary<T>(read: Read<T>): Read<string[]> {
    return (path, value) => {
      const entries = object(path, value);
      if (!entries) return [];
      const names = Object.keys(entries).sort(compareMemberNames);
      for (const key of names) read(path + "[*]", entries[key]);
      return names;
    };
  }
  function choice(path: string, value: JsonValue, first: string, second: string): void {
    if (value !== first && value !== second) fail(path, `must be one of "${first}", "${second}"`);
  }

  const strings = list(string);
  const version = field(document, "", "version", (path, value) => {
    if (typeof value === "number" && Number.isSafeInteger(value)) return value;
    fail(path, "must be an integer from -9007199254740991 to 9007199254740991");
    return 0;
  });
  const project = field(document, "", "project_id", string);
  const repos = field(document, "", "repos", strings);
  field(document, "", "areas", list((path, value) => {
    const area = object(path, value);
    if (!area) return;
    field(area, path, "name", string);
    field(area, path, "patterns", strings);
    for (const name of ["private", "system", "generated"]) field(area, path, name, flag, false);
    field(area, path, "owner_team_id", string, false);
  }));
  field(document, "", "roles", dictionary((path, value) => {
    const role = object(path, value);
    if (!role) return;
    for (const name of ["read", "write", "exec"]) field(role, path, name, strings);
    field(role, path, "push", strings, false);
  }));
  const members = field(document, "", "members", dictionary(string));
  const grantMembers = field(document, "", "temporary_grants", list((path, value) => {
    const grant = object(path, value);
    if (!grant) return "";
    const member = field(grant, path, "member_id", nonempty);
    field(grant, path, "area", nonempty);
    const actions = field(grant, path, "actions", list((path, value) => choice(path, value, "read", "write")));
    if (Array.isArray(grant.actions) && actions && (actions.length < 1 || actions.length > 2)) {
      fail(path + ".actions", "must hold one or two actions");
    }
    field(grant, path, "scope", (path, value) => choice(path, value, "mission", "until"));
    const fields = ["member_id", "area", "actions", "scope"];
    if (grant.scope === "mission") {
      fields.push("mission_id");
      field(grant, path, "mission_id", nonempty);
    } else if (grant.scope === "until") {
      fields.push("expires_at");
      field(grant, path, "expires_at", (path, value) => {
        if (!validTimestamp(value)) {
          fail(path, "must be a timestamp such as 2026-01-15T12:00:00Z or 2026-01-15T12:00:00.250+01:00");
        }
      });
    } else {
      fields.push("mission_id", "expires_at");
    }
    if (Object.keys(grant).some((name) => !fields.includes(name))) fail(path, "has an unknown field");
    return member ?? "";
  }), false) ?? [];
  if (grantMembers.length > 200) fail("temporary_grants", "must hold at most 200 grants");
  if (issues.length) throw new SyntaxError(issues.join("; "));
  return { bundle: document, version: version ?? 0, project: project ?? "", repos: repos ?? [], members: members ?? [], grantMembers };
}

function validTimestamp(value: JsonValue): boolean {
  if (typeof value !== "string") return false;
  const date = /^(\d{4}-\d\d-\d\dT(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d)(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/.exec(value);
  if (!date || date[0] !== value) return false;
  const utc = new Date(date[1] + ".000Z");
  return Number.isFinite(utc.valueOf()) && utc.toISOString().startsWith(date[1]);
}

export async function verifyPolicyBundle(token: string, jwks: string): Promise<{
  bundle: JsonObject;
  payload: Uint8Array;
}> {
  const bytes = await verifyJWS(token, jwks, "policy");
  const text = new TextDecoder().decode(bytes);
  try {
    const { bundle } = parsePolicyBundle(text);
    return { bundle, payload: new TextEncoder().encode(text) };
  } catch (cause) {
    throw new JWSError("payload", cause);
  }
}

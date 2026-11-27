import { Buffer } from "node:buffer";
import { canonicalJSON } from "./canonical";
import { bearer, cappedBody, conflict, invalid, jsonText, notFound } from "./http";
import { maxBundleBytes, signCanonicalJWS } from "./jws";
import { observeMember } from "./members";
import { parsePolicyBundle } from "./policy";
import { activeSigningKey, activeTeam, appendAudit, authenticate, signingKey } from "./state";
import type { HubStorage, TransactionSQL } from "./storage";

type Project = { id: string; repo: string };
type SignedPolicy = {
  version: number; bundle_json: string; jws_header: string; jws_signature: string;
};

export function readProject(sql: TransactionSQL, id: string): Project {
  const [project] = sql.exec<Project>("SELECT project_id AS id, repo FROM projects WHERE project_id = ?", id);
  if (!project) throw notFound();
  return project;
}

function latestPolicy(sql: TransactionSQL, id: string): SignedPolicy | undefined {
  const [latest] = sql.exec<SignedPolicy>(
    "SELECT version, bundle_json, jws_header, jws_signature FROM policies WHERE project_id = ?", id);
  return latest;
}

function signedResponse(row: SignedPolicy, headers?: HeadersInit): Response {
  const payload = Buffer.from(row.bundle_json).toString("base64url");
  const signature = row.jws_header + "." + payload + "." + row.jws_signature;
  // The canonical bundle is already JSON, including at the maximum supported depth.
  return jsonText(`{"version":${row.version},"bundle":${row.bundle_json},"signature":${JSON.stringify(signature)}}\n`, 200, headers);
}

export function readPolicy(sql: TransactionSQL, id: string, fetchingMember?: string): Response {
  const latest = latestPolicy(sql, id);
  if (!latest) throw notFound();
  if (fetchingMember !== undefined) {
    const { version } = parsePolicyBundle(latest.bundle_json);
    observeMember(sql, fetchingMember, id, Date.now(), { kind: "fetch", version });
  }
  return signedResponse(latest, { "OpenPlexer-Policy-Version": String(latest.version) });
}

function quotedRepo(repo: string): string {
  // Go's %q escapes valid repo characters outside its printable categories.
  return JSON.stringify(repo).replace(/[^\p{L}\p{M}\p{N}\p{P}\p{S} ]/gu, (character) => {
    const point = character.codePointAt(0) ?? 0;
    const hex = point.toString(16);
    return point <= 0xffff ? "\\u" + hex.padStart(4, "0") : "\\U" + hex.padStart(8, "0");
  });
}

function parsePublication(body: Uint8Array): ReturnType<typeof parsePolicyBundle> {
  try {
    return parsePolicyBundle(body);
  } catch (error) {
    if (!(error instanceof SyntaxError)) throw error;
    throw invalid("policy: " + error.message);
  }
}

export async function publishPolicy(request: Request, storage: HubStorage, id: string, teamID: string, wrapSecret: string | undefined) {
  const hash = bearer(request);
  const body = await cappedBody(request, maxBundleBytes);
  let policy: ReturnType<typeof parsePolicyBundle> | undefined;
  let canonical: string | undefined;

  function check(sql: TransactionSQL) {
    const actor = authenticate(sql, hash, true);
    const { repo } = readProject(sql, id);
    const parsed = policy ??= parsePublication(body);
    if (parsed.project !== id || parsed.repos.length !== 1 || parsed.repos[0] !== repo) {
      throw invalid(`bundle must target project ${JSON.stringify(id)} and its repository ${quotedRepo(repo)}`);
    }
    if (canonical === undefined) {
      try {
        new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(body);
      } catch {
        throw invalid("policy cannot be canonicalized");
      }
      canonical = canonicalJSON(parsed.bundle);
      if (Buffer.byteLength(canonical) > maxBundleBytes) throw invalid(`canonical policy exceeds ${maxBundleBytes} bytes`);
    }
    const latest = latestPolicy(sql, id);
    if (latest && parsed.version <= latest.version) {
      if (parsed.version === latest.version && canonical === latest.bundle_json) return { kind: "current", latest } as const;
      throw conflict("policy version must be higher than " + latest.version);
    }
    const roster = new Set(sql.exec<{ member_id: string }>("SELECT member_id FROM members").map((row) => row.member_id));
    if (parsed.members.some((member) => !roster.has(member))) throw invalid("members references an unknown member");
    if (parsed.grantMembers.some((member) => !roster.has(member))) {
      throw invalid("temporary_grants member_id references an unknown member");
    }
    const key = activeSigningKey(sql, "policy", activeTeam(sql).policy_kid);
    return { kind: "publish", actor, key, parsed, canonical } as const;
  }

  let candidate = storage.transaction(check);
  while (candidate.kind === "publish") {
    const { key, parsed } = candidate;
    const signer = await signingKey(key, teamID, wrapSecret);
    const token = await signCanonicalJWS(candidate.canonical, signer, key.kid, "policy");
    const [jws_header, , jws_signature] = token.split(".");
    const result = storage.transaction((sql) => {
      const state = check(sql);
      if (state.kind === "current" || state.key.kid !== key.kid) return state;

      const signed = { version: parsed.version, bundle_json: state.canonical, jws_header, jws_signature };
      sql.exec(`INSERT INTO policies (project_id, version, bundle_json, kid, jws_header, jws_signature)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(project_id) DO UPDATE SET version = excluded.version, bundle_json = excluded.bundle_json,
          kid = excluded.kid, jws_header = excluded.jws_header, jws_signature = excluded.jws_signature`,
      id, signed.version, signed.bundle_json, key.kid, signed.jws_header, signed.jws_signature);
      appendAudit(sql, { action: "policy.updated", target: "project:" + id, actor_member_id: state.actor,
        meta: { project_id: id, version: signed.version } });
      return { kind: "current", latest: signed } as const;
    });
    candidate = result;
  }
  return signedResponse(candidate.latest);
}

import { addProject, removeMember, validID } from "./admin";
import { isJsonObject } from "./canonical";
import { dashboardOrigin, isMutation, requireDashboardOrigin } from "./dashboard-http";
import { policyKeyFingerprint } from "./dashboard-keys";
import { dashboardSession, sessionResponse, signIn } from "./dashboard-session";
import { auditPage, exportAudit } from "./export";
import { allow, cappedBody, conflict, invalid, json, jsonText, notFound } from "./http";
import type { Env } from "./index";
import { handleInvite } from "./invites";
import { maxBundleBytes } from "./jws";
import { readMembers } from "./members";
import { parsePublication, publishPolicy, readPolicy, readProject } from "./publication";
import { eventPage } from "./room";
import { publicKeys } from "./state";
import type { HubStorage, TransactionSQL } from "./storage";
import type { WrapKeyBindings } from "./wrap";

function pageQuery(url: URL, cursor: string) {
  if (/;|%(?![0-9a-f]{2})/i.test(url.search)) throw invalid("invalid query string");
  const options = new Map<string, string>();
  for (const [key, value] of url.searchParams) {
    if ((key !== cursor && key !== "limit") || options.has(key)) throw invalid("invalid page query");
    options.set(key, value);
  }
  const limit = options.get("limit") ?? "25";
  const size = Number(limit);
  if (!/^\d+$/.test(limit) || size < 1 || size > 100) throw invalid("invalid page limit");
  return { cursor: options.get(cursor), limit: size };
}

function seq(cursor: string | undefined, fallback: number, min: number): number {
  if (cursor === undefined) return fallback;
  const value = Number(cursor);
  if (!/^\d+$/.test(cursor) || !Number.isSafeInteger(value) || value < min) throw invalid("invalid page cursor");
  return value;
}

function noQuery(url: URL): void {
  if (url.search) throw invalid("unexpected query string");
}

function projects(sql: TransactionSQL, url: URL): Response {
  const { cursor, limit } = pageQuery(url, "after");
  if (cursor !== undefined && !validID(cursor)) throw invalid("invalid project cursor");
  const rows = sql.exec<{ id: string; repo: string }>(
    "SELECT project_id AS id, repo FROM projects WHERE project_id > ? ORDER BY project_id LIMIT ?", cursor ?? "", limit + 1);
  const page = rows.slice(0, limit);
  return json({ projects: page, next_cursor: rows.length > limit ? page.at(-1)?.id : null });
}

function policyHistory(sql: TransactionSQL, url: URL, project: string): Response {
  readProject(sql, project);
  const { cursor, limit } = pageQuery(url, "before");
  const rows = sql.exec<{ seq: number; version: number; created_at: string; actor_member_id: string | null }>(
    `SELECT seq, json_extract(meta_json, '$.version') AS version, created_at, actor_member_id
      FROM audit_records WHERE action = 'policy.updated' AND target = ? AND seq <= ?
      ORDER BY seq DESC LIMIT ?`, "project:" + project, seq(cursor, Number.MAX_SAFE_INTEGER + 1, 1) - 1, limit + 1);
  const page = rows.slice(0, limit);
  return json({ versions: page, next_cursor: rows.length > limit ? String(page.at(-1)?.seq) : null });
}

function rolePreview(body: Uint8Array, sql: TransactionSQL, id: string): Response {
  const { repo } = readProject(sql, id);
  const { bundle, project, repos, version } = parsePublication(body, true);
  if (project !== id || repos.length !== 1 || repos[0] !== repo) throw invalid("draft is for a different project or repository");
  const areas = Array.isArray(bundle.areas) ? bundle.areas.filter(isJsonObject) : [];
  const roles = isJsonObject(bundle.roles) ? Object.entries(bundle.roles) : [];
  return json({
    version,
    roles: roles.map(([role, grants]) => ({ role, write: isJsonObject(grants) ? grants.write : [] })),
    areas: areas.map(({ name, patterns, system, private: restricted }) => ({
      name, patterns, system: system === true, private: restricted === true,
    })),
  });
}

function roomPage(sql: TransactionSQL, url: URL, project: string): Response {
  readProject(sql, project);
  const { cursor, limit } = pageQuery(url, "last_seq");
  const after = seq(cursor, 0, 0);
  const rows = eventPage(sql, project, after, limit + 1);
  const page = rows.slice(0, limit);
  const last = page.at(-1)?.seq ?? after;
  const events = page.map(({ line }) => line).join(",");
  return jsonText(`{"events":[${events}],"next_cursor":${rows.length > limit ? last : "null"},"last_seq":${last}}\n`);
}

type DashboardEnv = Pick<Env, "HUB_HOSTNAME" | "DEV_MODE" | "TEAM_ID"> & WrapKeyBindings;

export async function dashboardAPI(request: Request, storage: HubStorage, env: DashboardEnv, path: string[]): Promise<Response> {
  const origin = dashboardOrigin(request, env.HUB_HOSTNAME, env.DEV_MODE);
  requireDashboardOrigin(request, origin);
  const { method } = request;
  const url = new URL(request.url);
  const [section, id, action, subaction, ...extra] = path;
  if (!section || extra.length || id === "" || action === "" || subaction === "") throw notFound();

  if (section === "sign-in" && id === undefined) {
    allow(method, ["POST"]);
    noQuery(url);
    return signIn(request, storage);
  }

  const mutation = isMutation(method);
  const session = dashboardSession(request, mutation);
  const active = storage.transaction(session.read);
  const { authorize } = session;
  const dashboard = { authorize };

  if (section === "sign-out" && id === undefined) {
    allow(method, ["POST"]);
    noQuery(url);
    return session.signOut(storage);
  }
  if (section === "session" && id === undefined) {
    allow(method, ["GET", "HEAD"]);
    noQuery(url);
    return sessionResponse(active);
  }
  if (section === "projects" && id === undefined) {
    allow(method, ["GET", "HEAD", "POST"]);
    if (!mutation) return storage.transaction((sql) => projects(sql, url));
    noQuery(url);
    return json(await addProject(request, storage, dashboard));
  }
  if (section === "members" && action === undefined) {
    if (id === undefined) {
      allow(method, ["GET", "HEAD"]);
      return readMembers(request, storage, authorize);
    }
    allow(method, ["DELETE"]);
    noQuery(url);
    if (!validID(id)) throw notFound();
    return json(await removeMember(request, storage, id, authorize));
  }
  if (section === "invites" && subaction === undefined) {
    if (id === undefined) {
      if (method !== "POST") return handleInvite(request, storage, { kind: "collection" }, Date.now, dashboard);
      noQuery(url);
      const keys = storage.transaction((sql) => JSON.stringify(publicKeys(sql, "policy")));
      const fingerprint = await policyKeyFingerprint(keys);
      return handleInvite(request, storage, { kind: "collection" }, Date.now, {
        authorize,
        joinLink(sql, code) {
          if (JSON.stringify(publicKeys(sql, "policy")) !== keys) throw conflict("policy keys changed while creating the invite");
          return origin + "/join/" + code + "#fp=" + fingerprint;
        },
      });
    }
    noQuery(url);
    if (action === undefined) return handleInvite(request, storage, { kind: "revoke", id }, Date.now, dashboard);
    if (action === "approve") return handleInvite(request, storage, { kind: "approve", id }, Date.now, dashboard);
  }
  if (section === "projects" && validID(id) && action === "policy") {
    if (subaction === undefined) {
      allow(method, ["GET", "HEAD", "PUT"]);
      noQuery(url);
      if (mutation) return publishPolicy(request, storage, id, env.TEAM_ID, env, dashboard);
      return storage.transaction((sql) => readPolicy(sql, id));
    }
    if (subaction === "history") {
      allow(method, ["GET", "HEAD"]);
      return storage.transaction((sql) => policyHistory(sql, url, id));
    }
    if (subaction === "preview") {
      allow(method, ["POST"]);
      noQuery(url);
      const body = await cappedBody(request, maxBundleBytes);
      return storage.transaction((sql) => {
        authorize(sql);
        return rolePreview(body, sql, id);
      });
    }
  }
  if (section === "rooms" && validID(id) && action === "events" && subaction === undefined) {
    allow(method, ["GET", "HEAD"]);
    return storage.transaction((sql) => roomPage(sql, url, id));
  }
  if (section === "audit" && action === undefined) {
    if (id === "export") {
      allow(method, ["GET", "HEAD"]);
      noQuery(url);
      return exportAudit(request, storage, env.TEAM_ID, env, authorize);
    }
    if (id === undefined) {
      allow(method, ["GET", "HEAD"]);
      const { cursor, limit } = pageQuery(url, "before");
      return storage.transaction((sql) => json(auditPage(sql, seq(cursor, Number.MAX_SAFE_INTEGER + 1, 1) - 1, limit)));
    }
  }
  throw notFound();
}

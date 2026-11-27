import { Buffer } from "node:buffer";
import { validID } from "./admin";
import { bearer, cappedBody, invalid, json, notFound } from "./http";
import { authenticate } from "./state";
import type { Role } from "./state";
import type { HubStorage, TransactionSQL } from "./storage";

export const memberMaintenanceIntervalMs = 24 * 60 * 60 * 1_000;
const retentionMs = 30 * memberMaintenanceIntervalMs;
const pageSize = 100;
const bodyLimit = 64 << 10;

type Observation = { kind: "fetch"; version: number } | { kind: "room" };

// The caller authenticates and selects the policy or Room page in this transaction.
export function observeMember(sql: TransactionSQL, member: string, project: string, at: number, observed: Observation) {
  if (observed.kind === "fetch") {
    sql.exec(`INSERT INTO member_observations (member_id, project_id, policy_fetch_ms, policy_version)
      VALUES (?, ?, ?, ?)
      ON CONFLICT (member_id, project_id) DO UPDATE SET
        policy_fetch_ms = excluded.policy_fetch_ms, policy_version = excluded.policy_version`,
    member, project, at, observed.version);
  } else {
    sql.exec(`INSERT INTO member_observations (member_id, project_id, room_connected_ms)
      VALUES (?, ?, ?)
      ON CONFLICT (member_id, project_id) DO UPDATE SET room_connected_ms = excluded.room_connected_ms`,
    member, project, at);
  }
}

export function pruneMemberObservations(sql: TransactionSQL, now: number) {
  const cutoff = now - retentionMs;
  sql.exec(`DELETE FROM member_observations
    WHERE (policy_fetch_ms IS NULL OR policy_fetch_ms <= ?) AND (room_connected_ms IS NULL OR room_connected_ms <= ?)`,
  cutoff, cutoff);
  sql.exec(`UPDATE member_observations SET policy_fetch_ms = NULL, policy_version = NULL
    WHERE policy_fetch_ms <= ?`, cutoff);
  sql.exec("UPDATE member_observations SET room_connected_ms = NULL WHERE room_connected_ms <= ?", cutoff);
}

type Cursor = [member: string, project: string | null];
type MembersQuery = { project: string | null; after: Cursor | null; limit: number };

function encodeCursor(cursor: Cursor): string {
  return Buffer.from(JSON.stringify(cursor)).toString("base64url");
}

function parseCursor(encoded: string): Cursor {
  try {
    // IDs use ASCII, and re-encoding checks both the JSON and base64url form.
    const value: unknown = JSON.parse(Buffer.from(encoded, "base64url").toString("utf8"));
    if (Array.isArray(value) && value.length === 2) {
      const [member, project] = value;
      if (validID(member) && (project === null || validID(project))) {
        const cursor: Cursor = [member, project];
        if (encodeCursor(cursor) === encoded) return cursor;
      }
    }
  } catch {
    // Use the same error for malformed JSON and invalid cursor fields.
  }
  throw invalid("malformed member cursor");
}

export function parseMembersQuery(rawQuery: string): MembersQuery {
  if (/;|%(?![0-9a-f]{2})/i.test(rawQuery)) throw invalid("invalid query string");
  const query: MembersQuery = { project: null, after: null, limit: pageSize };
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    if (seen.has(key)) throw invalid("repeated member query parameter");
    seen.add(key);
    switch (key) {
      case "project_id":
        if (!validID(value)) throw invalid("invalid project_id");
        query.project = value;
        break;
      case "after":
        query.after = parseCursor(value);
        break;
      case "limit":
        query.limit = Number(value);
        if (!/^[0-9]+$/.test(value) || query.limit < 1 || query.limit > pageSize) {
          throw invalid("limit must be from 1 through 100");
        }
        break;
      default:
        throw invalid("unknown member query parameter");
    }
  }
  return query;
}

type MemberRow = {
  member_id: string; hub_role: Role; project_id: string | null;
  policy_fetch_ms: number | null; policy_version: number | null; room_connected_ms: number | null;
};

export function membersPage(sql: TransactionSQL, query: MembersQuery, now: number) {
  const { project, after, limit } = query;
  if (project !== null && !sql.exec("SELECT 1 FROM projects WHERE project_id = ?", project).length) throw notFound();
  const rows = sql.exec<MemberRow>(`
    WITH args(cutoff, project, after_member, after_project) AS (VALUES (?, ?, ?, ?)),
    retained AS (
      SELECT member_id, project_id,
        CASE WHEN policy_fetch_ms > cutoff THEN policy_fetch_ms END AS policy_fetch_ms,
        CASE WHEN policy_fetch_ms > cutoff THEN policy_version END AS policy_version,
        CASE WHEN room_connected_ms > cutoff THEN room_connected_ms END AS room_connected_ms
      FROM member_observations CROSS JOIN args
      WHERE (project IS NULL OR project_id = project) AND (policy_fetch_ms > cutoff OR room_connected_ms > cutoff)
    )
    SELECT m.member_id, m.role AS hub_role, COALESCE(a.project, o.project_id) AS project_id,
      o.policy_fetch_ms, o.policy_version, o.room_connected_ms
    FROM members AS m CROSS JOIN args AS a LEFT JOIN retained AS o ON m.member_id = o.member_id
    WHERE m.member_id > a.after_member OR (
      m.member_id = a.after_member AND COALESCE(a.project, o.project_id, '') > a.after_project
    )
    ORDER BY m.member_id, project_id LIMIT ?`,
  now - retentionMs, project, after?.[0] ?? "", after?.[1] ?? "", limit + 1);

  const hasNext = rows.length > limit;
  if (hasNext) rows.pop();
  const last = rows.at(-1);
  const timestamp = (ms: number | null) => ms === null ? null : new Date(ms).toISOString();
  return {
    members: rows.map(({ member_id, hub_role, project_id, policy_fetch_ms, policy_version, room_connected_ms }) => ({
      member_id, hub_role, project_id,
      last_policy_fetch_at: timestamp(policy_fetch_ms), policy_version, last_room_connected_at: timestamp(room_connected_ms),
    })),
    next_cursor: hasNext && last ? encodeCursor([last.member_id, last.project_id]) : null,
  };
}

export async function readMembers(request: Request, storage: HubStorage) {
  const hash = bearer(request);
  const body = await cappedBody(request, bodyLimit);
  return storage.transaction((sql) => {
    authenticate(sql, hash, true);
    if (body.length) throw invalid("unexpected request body");
    const query = parseMembersQuery(new URL(request.url).search);
    return json(membersPage(sql, query, Date.now()));
  });
}

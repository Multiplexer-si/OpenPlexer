import { validID } from "./admin";
import { pathsOverlap } from "./claim-glob";
import { HubError, invalid, json, jsonText, notFound } from "./http";
import { appendEvent, memberAgent, newRoomRecordID, parseRoomRequest, roomEndpoint } from "./room";
import { parseClaimPost, parseClaimRelease, parseClaimRenewal } from "./room-wire";
import { appendAudit } from "./state";
import type { HubStorage, TransactionSQL } from "./storage";

// Go stores the TTL as a signed 64-bit duration in nanoseconds.
const maxTTL = 9_223_372_036;

type ClaimRow = {
  claim_id: string;
  room_id: string;
  member_id: string;
  agent_id: string;
  agent_vendor: string;
  agent_parent: string | null;
  paths_json: string;
  intent: string;
  mode: string;
  expires_at_ms: number;
};
type ClaimRoute = { action: "create" } | { action: "renew" | "release"; claim: string };

function claimAgent(row: ClaimRow) {
  return { id: row.agent_id, vendor: row.agent_vendor, ...(row.agent_parent && { parent: row.agent_parent }) };
}

function claimLease(row: ClaimRow) {
  const paths: unknown = JSON.parse(row.paths_json);
  if (!Array.isArray(paths) || !paths.every((path) => typeof path === "string")) {
    throw new Error("Invalid stored claim paths");
  }
  return {
    id: row.claim_id, room: row.room_id, on_behalf_of: row.member_id, agent: claimAgent(row),
    paths, intent: row.intent, mode: row.mode,
    expires_at: new Date(row.expires_at_ms).toISOString().replace(/\.?0+Z$/, "Z"),
  };
}

function leaseExpiry(now: number, ttl: number): number {
  if (ttl < 1 || ttl > maxTTL) throw invalid("ttl_seconds must be between 1 and " + maxTTL);
  return now + ttl * 1000;
}

function endClaim(sql: TransactionSQL, row: ClaimRow, outcome: string, now: number): void {
  const at = new Date(now).toISOString();
  const { event } = appendEvent(sql, {
    room: row.room_id, member: row.member_id, agent: claimAgent(row), type: "release",
    body: { claim_id: row.claim_id, ...(outcome && { outcome }) }, at,
  });
  sql.exec("DELETE FROM claims WHERE claim_id = ?", row.claim_id);
  const expired = outcome === "expired";
  appendAudit(sql, {
    action: expired ? "claim.expired" : "claim.released", target: "claim:" + row.claim_id,
    actor_member_id: expired ? null : row.member_id, agent_session_id: row.agent_id,
    meta: { room_id: row.room_id, seq: event.seq, event_id: event.id },
  }, at);
}

function createClaim(
  sql: TransactionSQL, input: { room: string; member: string; body: Uint8Array; repo: string; now: number },
): Response {
  const { room, member, body, repo, now } = input;
  const post = parseRoomRequest(parseClaimPost, body);
  const agent = memberAgent(post.agent, member);
  const mode = post.mode || "shared";
  if (mode !== "shared" && mode !== "exclusive") throw invalid("claim mode must be shared or exclusive");
  const implicit = post.implicit ?? false;
  const ttl = post.ttl ?? (implicit ? 600 : 3600);
  const row: ClaimRow = {
    claim_id: newRoomRecordID(), room_id: room, member_id: member, agent_id: agent.id, agent_vendor: agent.vendor,
    agent_parent: agent.parent ?? null, paths_json: JSON.stringify(post.paths), intent: post.intent, mode,
    expires_at_ms: leaseExpiry(now, ttl),
  };
  const conflicts = sql.exec<ClaimRow>(
    "SELECT claims.* FROM claims JOIN projects ON project_id = room_id" +
    " WHERE repo = ? AND agent_id != ? AND (mode = 'exclusive' OR ? = 'exclusive') ORDER BY claim_id",
    repo, agent.id, mode,
  ).map(claimLease).filter((other) => pathsOverlap(other.paths, post.paths));
  const at = new Date(now).toISOString();
  const { event, envelope } = appendEvent(sql, {
    room, member, agent, type: "claim", at,
    body: { claim_id: row.claim_id, item: post.item, paths: post.paths, intent: post.intent, ttl_seconds: ttl, mode, implicit },
  });
  sql.exec("INSERT INTO claims (claim_id, room_id, member_id, agent_id, agent_vendor, agent_parent," +
    " paths_json, intent, mode, expires_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
  row.claim_id, row.room_id, row.member_id, row.agent_id, row.agent_vendor, row.agent_parent,
  row.paths_json, row.intent, row.mode, row.expires_at_ms);
  appendAudit(sql, {
    action: "claim.created", target: "claim:" + row.claim_id, actor_member_id: member, agent_session_id: agent.id,
    meta: { room_id: room, seq: event.seq, event_id: event.id },
  }, at);
  const reply = JSON.stringify({ granted: claimLease(row), conflicts });
  return jsonText(reply.slice(0, -1) + ',"event":' + envelope + "}\n");
}

function ownClaim(
  sql: TransactionSQL, input: { room: string; member: string; id: string; session: string },
): ClaimRow {
  const { room, member, id, session } = input;
  if (!validID(session)) throw invalid("valid session_id required");
  const [row] = sql.exec<ClaimRow>(
    "SELECT * FROM claims WHERE claim_id = ? AND room_id = ? AND agent_id = ?", id, room, member + "/" + session);
  if (!row) throw notFound();
  return row;
}

export function handleClaim(
  request: Request, storage: HubStorage, room: string, route: ClaimRoute,
): Promise<Response> {
  return roomEndpoint(request, storage, room, (sql, { member, body, repo }) => {
    const now = Date.now();
    for (const row of sql.exec<ClaimRow>("SELECT * FROM claims WHERE expires_at_ms <= ? ORDER BY claim_id", now)) {
      endClaim(sql, row, "expired", now);
    }
    try {
      if (route.action === "create") return createClaim(sql, { room, member, body, repo, now });

      const id = route.claim;
      if (route.action === "renew") {
        const { session, ttl } = parseRoomRequest(parseClaimRenewal, body);
        const row = ownClaim(sql, { room, member, id, session });
        row.expires_at_ms = leaseExpiry(now, ttl);
        sql.exec("UPDATE claims SET expires_at_ms = ? WHERE claim_id = ?", row.expires_at_ms, id);
        const lease = claimLease(row);
        appendAudit(sql, {
          action: "claim.renewed", target: "claim:" + id, actor_member_id: member, agent_session_id: row.agent_id,
          meta: { room_id: room, expires_at: lease.expires_at },
        }, new Date(now).toISOString());
        return json(lease);
      }
      const { session, outcome } = parseRoomRequest(parseClaimRelease, body);
      if (outcome === "expired") throw invalid("expired is reserved for the hub");
      const row = ownClaim(sql, { room, member, id, session });
      endClaim(sql, row, outcome ?? "", now);
      return json(claimLease(row));
    } catch (error) {
      // Rejected claim operations still commit the expiry sweep. Storage errors roll it back.
      if (error instanceof HubError) return error.response();
      throw error;
    }
  });
}

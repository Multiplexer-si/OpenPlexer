import { buildCheckpoint, signCheckpoint } from "./audit";
import type { AuditRecord } from "./audit";
import { bearer, jsonHeaders } from "./http";
import { activeSigningKey, activeTeam, authenticate, signingKey } from "./state";
import type { HubStorage } from "./storage";

const utf8 = new TextEncoder();
const chunkBytes = 64 << 10;
const pageRows = 128;

type AuditRow = Omit<AuditRecord, "id" | "flagged" | "meta"> & { seq: number; flagged: number; meta_json: string };

function* exportParts(storage: HubStorage, through: number) {
  yield '{"entries":[';
  let cursor = through;
  while (cursor > 0) {
    const rows = storage.transaction((sql) => sql.exec<AuditRow>(`SELECT seq, workspace_id, actor_member_id, agent_session_id,
      action, target, reason, meta_json, flagged, created_at, prev_hash, hash
      FROM audit_records WHERE seq <= ? ORDER BY seq DESC LIMIT ?`, cursor, pageRows));
    if (!rows.length) throw new Error("Missing audit records");
    for (const { seq, flagged, meta_json, ...record } of rows) {
      if (seq !== cursor) throw new Error("Gap in audit records");
      const result = record.action.slice(Math.max(record.action.lastIndexOf("."), record.action.lastIndexOf("-")) + 1);
      const session = record.agent_session_id;
      const separator = session?.indexOf("/") ?? -1;
      yield (cursor-- === through ? "" : ",") + JSON.stringify({
        id: String(seq), ...record, flagged: Boolean(flagged), meta: JSON.parse(meta_json), result,
        agent_member_id: separator < 0 ? undefined : session?.slice(0, separator),
      });
    }
  }
  yield '],"next_cursor":null}\n';
}

function exportBody(storage: HubStorage, through: number): ReadableStream<Uint8Array> {
  const parts = exportParts(storage, through);
  let pending: Uint8Array = new Uint8Array();
  return new ReadableStream({
    pull(controller) {
      const chunk = new Uint8Array(chunkBytes);
      let offset = 0;
      while (offset < chunk.length) {
        if (!pending.length) {
          const next = parts.next();
          if (next.done) {
            if (offset) controller.enqueue(chunk.subarray(0, offset));
            controller.close();
            return;
          }
          pending = utf8.encode(next.value);
        }
        const length = Math.min(pending.length, chunk.length - offset);
        chunk.set(pending.subarray(0, length), offset);
        offset += length;
        pending = pending.subarray(length);
      }
      controller.enqueue(chunk);
    },
    cancel() { parts.return(undefined); },
  });
}

export async function exportAudit(request: Request, storage: HubStorage, teamID: string, wrapSecret: string | undefined) {
  const hash = bearer(request);
  const { checkpoint, key } = storage.transaction((sql) => {
    authenticate(sql, hash, true);
    const current = activeTeam(sql);
    return {
      checkpoint: buildCheckpoint({
        workspace_id: current.workspace_id, entries: current.audit_seq, tip_hash: current.audit_tip,
        issued_at: new Date().toISOString(),
      }),
      key: activeSigningKey(sql, "audit", current.audit_kid),
    };
  });
  const signer = await signingKey(key, teamID, wrapSecret);
  const token = await signCheckpoint(checkpoint, signer, key.kid);
  const body = request.method === "HEAD" ? null : exportBody(storage, checkpoint.entries);
  return new Response(body, { headers: { ...jsonHeaders, "OpenPlexer-Audit-Checkpoint": token } });
}

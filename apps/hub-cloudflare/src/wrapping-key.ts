import { Buffer } from "node:buffer";
import { strictAdminJSON } from "./admin";
import { cappedBody, conflict, HubError, invalid, tokenHash, unauthorized } from "./http";
import { importPKCS8, importPublicJWK, parseStoredPublicJWK, verifySigningKeyPair } from "./jws";
import { appendAudit, bearerLead, unwrapSigningKey } from "./state";
import type { SigningKeyRow } from "./state";
import type { HubStorage, TransactionSQL } from "./storage";
import { isWrapKeyID, loadWrappingKey, unwrapPKCS8, wrapPKCS8 } from "./wrap";
import type { WrapKeyBindings } from "./wrap";

type WrappedRow = SigningKeyRow & { public_jwk: string; status: "active" | "staged" };

function wrappedRows(sql: TransactionSQL): WrappedRow[] {
  const rows = sql.exec<WrappedRow>(`SELECT kid, purpose, public_jwk, status, wrapped_pkcs8, nonce, wrap_key_id
    FROM signing_keys WHERE status IN ('active', 'staged') ORDER BY kid`);
  if (rows.length === 0 || rows.some((row) => !row.wrapped_pkcs8 || !row.nonce || !isWrapKeyID(row.wrap_key_id))) {
    throw new HubError(500, "Invalid wrapped signing key row");
  }
  return rows;
}

function sameRow(a: WrappedRow, b: WrappedRow): boolean {
  return a.kid === b.kid && a.purpose === b.purpose && a.status === b.status && a.public_jwk === b.public_jwk
    && a.wrap_key_id === b.wrap_key_id && Buffer.from(a.nonce).equals(new Uint8Array(b.nonce))
    && Buffer.from(a.wrapped_pkcs8).equals(new Uint8Array(b.wrapped_pkcs8));
}

function keyIDs(rows: SigningKeyRow[]): string[] {
  return [...new Set(rows.map((row) => row.wrap_key_id))].sort();
}

export async function rewrapSigningKey(row: SigningKeyRow & { public_jwk: string }, teamID: string,
  bindings: WrapKeyBindings, target: ReturnType<typeof loadWrappingKey>,
) {
  const identity = { team: teamID, purpose: row.purpose, kid: row.kid };
  const original = await unwrapSigningKey(row, teamID, bindings);
  try {
    const publicKey = await importPublicJWK(parseStoredPublicJWK(row.public_jwk, row.kid, row.purpose));
    const wrapped = row.wrap_key_id === target.id ? undefined : await wrapPKCS8(original, target.bytes, identity);
    const verified = wrapped ? await unwrapPKCS8(wrapped, target.bytes, identity) : original;
    const signer = await importPKCS8(verified);
    await verifySigningKeyPair(signer, publicKey);
    return {
      wrapped_pkcs8: wrapped?.ciphertext ?? row.wrapped_pkcs8, nonce: wrapped?.nonce.buffer ?? row.nonce,
      wrap_key_id: target.id,
    };
  } catch {
    throw new HubError(500, `Unable to verify rewrapped ${row.purpose} signing key`);
  }
}

export async function manageWrappingKey(request: Request, storage: HubStorage, env: WrapKeyBindings & {
  TEAM_ID: string; WRAP_ROTATION_TOKEN?: string;
}) {
  const authorize = bearerLead(request);
  const presented = request.headers.get("OpenPlexer-Operator-Token");
  const operator = env.WRAP_ROTATION_TOKEN;
  if (!presented || !/^[0-9a-f]{64}$/.test(presented) || !operator || !/^[0-9a-f]{64}$/.test(operator)
      || !crypto.subtle.timingSafeEqual(tokenHash(presented), tokenHash(operator))) throw unauthorized();
  if (new URL(request.url).search) throw invalid("unexpected query string");

  const rows = storage.transaction((sql) => {
    authorize(sql);
    return wrappedRows(sql);
  });
  const previous = keyIDs(rows);
  if (request.method !== "PUT") return { wrap_key_ids: previous };

  const detail = "expected a versioned wrap_key_id";
  const { wrap_key_id: id } = strictAdminJSON(await cappedBody(request, 64 << 10), ["wrap_key_id"], detail);
  if (!isWrapKeyID(id)) throw invalid(detail);
  const target = loadWrappingKey(env, id);
  const updated = await Promise.all(rows.map((row) => rewrapSigningKey(row, env.TEAM_ID, env, target)));

  // Web Crypto is asynchronous. Commit only if every row still matches the verified snapshot.
  return storage.transaction((sql) => {
    const actor = authorize(sql);
    const current = wrappedRows(sql);
    if (current.length !== rows.length || current.some((row, i) => !sameRow(row, rows[i]))) {
      throw conflict("signing keys changed during wrapping key rotation; retry");
    }
    let count = 0;
    for (const [i, row] of current.entries()) {
      if (row.wrap_key_id === id) continue;
      const { wrapped_pkcs8, nonce } = updated[i];
      sql.exec("UPDATE signing_keys SET wrapped_pkcs8 = ?, nonce = ?, wrap_key_id = ? WHERE kid = ?",
        wrapped_pkcs8, nonce, id, row.kid);
      count++;
    }
    if (count > 0) appendAudit(sql, {
      action: "wrapping_key.rotated", target: "wrapping-key:" + id, actor_member_id: actor,
      meta: { previous_wrap_key_ids: previous.filter((keyID) => keyID !== id), rows_rewrapped: count },
    });
    return { wrap_key_ids: [id] };
  });
}

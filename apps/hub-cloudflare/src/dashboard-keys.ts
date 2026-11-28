import { Buffer } from "node:buffer";
import { canonicalJSON, hasDuplicateMembers, isJsonObject, parseJSON } from "./canonical";
import type { JsonObject } from "./canonical";
import { tokenHash } from "./http";
import { importPublicJWK } from "./jws";

// Match the join CLI's policy key pin, including validation before normalization.
export async function policyKeyFingerprint(jwks: string): Promise<string> {
  if (Buffer.byteLength(jwks) > (64 << 10)) throw new Error("invalid policy key set");
  const duplicates = new WeakSet<JsonObject>();
  const doc = parseJSON(jwks, { duplicates, loneSurrogates: "reject" });
  if (!isJsonObject(doc) || !Array.isArray(doc.keys) || !doc.keys.length || hasDuplicateMembers(doc, duplicates)) {
    throw new Error("invalid policy key set");
  }
  const seen = new Set<string>();
  const keys = [];
  for (const key of doc.keys) {
    if (!isJsonObject(key) || typeof key.kid !== "string" || !key.kid || seen.has(key.kid) ||
        typeof key.x !== "string" || /[\r\n]/.test(key.x) || typeof key.y !== "string" || /[\r\n]/.test(key.y)) {
      throw new Error("invalid policy key set");
    }
    await importPublicJWK(key, "policy");
    seen.add(key.kid);
    keys.push({ kid: key.kid, kty: "EC", crv: "P-256", x: key.x, y: key.y });
  }
  keys.sort((a, b) => Buffer.compare(Buffer.from(a.kid), Buffer.from(b.kid)));
  return Buffer.from(tokenHash(canonicalJSON({ keys }))).toString("hex");
}

import { Buffer } from "node:buffer";
import { canonicalJSON, hasDuplicateMembers, isJsonObject, parseJSON } from "./canonical.ts";
import type { JsonObject, JsonValue } from "./canonical.ts";

export type SigningPurpose = "policy" | "audit";

const tokenType = {
  policy: "mp-policy+jws",
  audit: "openplexer-audit-checkpoint+jws",
};
const algorithm = { name: "ECDSA", namedCurve: "P-256" };
const signatureAlgorithm = { name: "ECDSA", hash: "SHA-256" };
const utf8 = new TextEncoder();
const maxBundleBytes = 1 << 20;

export type JWSStage = "compact" | "header" | "key" | "signature" | "payload";

export class JWSError extends Error {
  readonly stage: JWSStage;

  constructor(stage: JWSStage, cause?: unknown) {
    super(`Invalid JWS ${stage}`, { cause });
    this.stage = stage;
  }
}

function encodeBase64URL(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString("base64url");
}

function decodeBase64URL(text: string, strict: boolean): Uint8Array<ArrayBuffer> {
  if (/[^A-Za-z0-9_-]/.test(text) || text.length % 4 === 1) {
    throw new Error("Invalid base64url");
  }
  const bytes = Buffer.from(text, "base64url");
  if (strict && encodeBase64URL(bytes) !== text) throw new Error("Nonzero base64url padding bits");
  return bytes;
}

function policySegment(text: string): Uint8Array<ArrayBuffer> {
  const compact = text.replace(/[\t\n\f\r ]/g, "");
  return decodeBase64URL(compact.length % 4 === 0 ? compact.replace(/={1,2}$/, "") : compact, false);
}

export async function generateSigningKey(): Promise<CryptoKeyPair> {
  const key = await crypto.subtle.generateKey(algorithm, true, ["sign", "verify"]);
  if (!("privateKey" in key)) throw new Error("Expected a P-256 key pair");
  return key;
}

export function importPKCS8(bytes: ArrayBuffer | Uint8Array<ArrayBuffer>): Promise<CryptoKey> {
  return crypto.subtle.importKey("pkcs8", bytes, algorithm, true, ["sign"]);
}

export async function exportPKCS8(key: CryptoKey): Promise<ArrayBuffer> {
  const raw = await crypto.subtle.exportKey("pkcs8", key);
  if (!(raw instanceof ArrayBuffer)) throw new Error("Expected PKCS#8 bytes");
  return raw;
}

export function importPrivateJWK(jwk: JsonWebKey): Promise<CryptoKey> {
  return crypto.subtle.importKey("jwk", jwk, algorithm, true, ["sign"]);
}

export async function exportPrivateJWK(key: CryptoKey): Promise<JsonWebKey> {
  const jwk = await crypto.subtle.exportKey("jwk", key);
  if (jwk instanceof ArrayBuffer || !jwk.d) throw new Error("Expected a private JWK");
  return jwk;
}

export async function exportPublicJWK(key: CryptoKey, purpose: SigningPurpose) {
  if (key.type !== "public" || key.algorithm.name !== "ECDSA"
      || !("namedCurve" in key.algorithm) || key.algorithm.namedCurve !== "P-256") {
    throw new Error("Expected a public P-256 signing key");
  }
  const raw = await crypto.subtle.exportKey("raw", key);
  if (!(raw instanceof ArrayBuffer)) throw new Error("Expected public key bytes");
  const point = new Uint8Array(raw);
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", point));
  const prefix = purpose === "audit" ? "audit-" : "";
  return {
    alg: "ES256", use: "sig", kid: prefix + encodeBase64URL(digest),
    kty: "EC", crv: "P-256", x: encodeBase64URL(point.subarray(1, 33)), y: encodeBase64URL(point.subarray(33)),
  };
}

function canVerify(key: JsonObject): boolean {
  if (key.kty !== "EC" || key.crv !== "P-256") return false;
  if (Object.hasOwn(key, "alg") && key.alg !== "ES256") return false;
  if (Object.hasOwn(key, "use") && key.use !== "sig") return false;
  if (!Object.hasOwn(key, "key_ops")) return true;
  const ops = key.key_ops;
  return Array.isArray(ops) && ops.includes("verify") && ops.every((op) => typeof op === "string")
    && new Set(ops).size === ops.length;
}

export async function importPublicJWK(jwk: JsonObject, purpose: SigningPurpose): Promise<CryptoKey> {
  if (!canVerify(jwk) || Object.hasOwn(jwk, "d") || (Array.isArray(jwk.key_ops) && jwk.key_ops.length !== 1)) {
    throw new Error("Expected a public P-256 verification key");
  }
  const point = new Uint8Array(65);
  point[0] = 4;
  for (const [coordinate, offset] of [[jwk.x, 1], [jwk.y, 33]] satisfies [JsonValue, number][]) {
    if (typeof coordinate !== "string") throw new Error("Missing P-256 coordinate");
    const text = purpose === "audit" ? coordinate : coordinate.replace(/[\r\n]/g, "");
    const bytes = decodeBase64URL(text, true);
    if (bytes.length !== 32) throw new Error("Invalid P-256 coordinate length");
    point.set(bytes, offset);
  }
  return crypto.subtle.importKey("raw", point, algorithm, true, ["verify"]);
}

export async function signJWS(payload: JsonValue, key: CryptoKey, kid: string, purpose: SigningPurpose): Promise<string> {
  if (!kid || (purpose === "audit" && !kid.startsWith("audit-"))) throw new JWSError("header");
  const header = canonicalJSON({ alg: "ES256", kid, typ: tokenType[purpose] });
  const body = utf8.encode(canonicalJSON(payload));
  if (purpose === "policy" && body.length > maxBundleBytes) throw new JWSError("payload");
  const input = encodeBase64URL(utf8.encode(header)) + "." + encodeBase64URL(body);
  const signature = new Uint8Array(await crypto.subtle.sign(signatureAlgorithm, key, utf8.encode(input)));
  if (signature.length !== 64) throw new JWSError("signature");
  return input + "." + encodeBase64URL(signature);
}

export function parseCanonicalObject(bytes: Uint8Array): JsonObject {
  const text = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes);
  const value = parseJSON(text);
  if (!isJsonObject(value) || canonicalJSON(value) !== text) throw new SyntaxError("Expected a canonical JSON object");
  return value;
}

async function selectKey(jwks: string, header: JsonObject, purpose: SigningPurpose): Promise<CryptoKey> {
  const duplicates = new WeakSet<JsonObject>();
  const doc = parseJSON(jwks, purpose === "audit" ? { duplicates } : {
    loneSurrogates: "replace", rejectNumberOverflow: true,
  });
  if (!isJsonObject(doc) || !Array.isArray(doc.keys) || !doc.keys.every(isJsonObject)
      || hasDuplicateMembers(doc, duplicates)) {
    throw new Error("Invalid JWKS");
  }
  const keys = doc.keys.filter((key) => {
    const matchesKid = typeof header.kid === "string" && key.kid === header.kid;
    if (purpose === "audit") return matchesKid;

    return (!Object.hasOwn(header, "kid") || matchesKid)
      && (!Object.hasOwn(key, "ext") || typeof key.ext === "boolean") && canVerify(key);
  });
  if (keys.length !== 1) throw new Error("Expected exactly one matching key");
  return importPublicJWK(keys[0], purpose);
}

// The policy and checkpoint contracts deliberately differ in their accepted JWS encodings.
export async function verifyJWS(token: string, jwks: string, purpose: SigningPurpose): Promise<Uint8Array> {
  if (purpose === "policy" && Buffer.byteLength(token) > 2 * maxBundleBytes) throw new JWSError("compact");
  const parts = token.split(".");
  if (parts.length !== 3) throw new JWSError("compact");
  const [head, body, sig] = parts;
  let auditSegments: Uint8Array<ArrayBuffer>[] = [];
  if (purpose === "audit") {
    try {
      auditSegments = parts.map((part) => {
        if (!part) throw new Error("Empty segment");
        return decodeBase64URL(part, true);
      });
    } catch (cause) {
      throw new JWSError("compact", cause);
    }
  }

  let header: JsonObject;
  let unencoded = false;
  try {
    if (purpose === "audit") {
      header = parseCanonicalObject(auditSegments[0]);
      if (Object.keys(header).length !== 3 || typeof header.kid !== "string" || !header.kid) {
        throw new Error("Invalid checkpoint header fields");
      }
    } else {
      const text = new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(policySegment(head));
      const value = parseJSON(text, { loneSurrogates: "replace" });
      if (!isJsonObject(value)) throw new Error("Expected a policy header object");
      header = value;
      if (Object.hasOwn(header, "crit")) {
        if (!Array.isArray(header.crit) || header.crit.length === 0 || !header.crit.every((name) => name === "b64")
            || typeof header.b64 !== "boolean") throw new Error("Invalid critical header fields");
        unencoded = !header.b64;
      }
    }
    if (header.alg !== "ES256" || header.typ !== tokenType[purpose]) throw new Error("Unexpected alg or typ");
  } catch (cause) {
    throw new JWSError("header", cause);
  }

  let key: CryptoKey;
  try {
    key = await selectKey(jwks, header, purpose);
  } catch (cause) {
    throw new JWSError("key", cause);
  }

  try {
    const signature = purpose === "audit" ? auditSegments[2] : policySegment(sig);
    if (signature.length !== 64
        || !await crypto.subtle.verify(signatureAlgorithm, key, signature, utf8.encode(head + "." + body))) {
      throw new Error("Invalid ES256 signature");
    }
  } catch (cause) {
    throw new JWSError("signature", cause);
  }

  if (purpose === "audit") return auditSegments[1];
  try {
    if (unencoded && /[^\x00-\x7f]/.test(body)) throw new Error("Expected an ASCII payload");
    const payload = unencoded ? utf8.encode(body) : policySegment(body);
    if (payload.length > maxBundleBytes) throw new Error("Policy payload is too large");
    return payload;
  } catch (cause) {
    throw new JWSError("payload", cause);
  }
}

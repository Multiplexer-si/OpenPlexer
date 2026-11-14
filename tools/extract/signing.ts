import type { Json } from "./output.ts";
import type { Reference } from "./reference.ts";

export const base64url = (data: string | Uint8Array): string => Buffer.from(data).toString("base64url");

/** A test-only ES256 key. The private half exists only in memory and is never written. */
export interface Signer {
  /** The public key in the form that the hub of the reference publishes. */
  publicJwk: Record<string, string>;
  sign(header: Record<string, string>, payload: string): Promise<string>;
}

export async function newSigner(reference: Reference, kid: string): Promise<Signer> {
  const { publicKey, privateKey } = await reference.jose.generateKeyPair("ES256");
  return {
    publicJwk: { ...(await reference.jose.exportJWK(publicKey)), kid, alg: "ES256", use: "sig" },
    sign: (header, payload) => new reference.jose.CompactSign(new TextEncoder().encode(payload)).setProtectedHeader(header).sign(privateKey),
  };
}

/** The protected header and the payload text of a compact JWS, or null if a part does not decode. */
export function decodeJws(jws: string): { header: Json; payload: string } | null {
  const [header, payload] = jws.split(".");
  try {
    return { header: JSON.parse(Buffer.from(header ?? "", "base64url").toString("utf8")) as Json, payload: Buffer.from(payload ?? "", "base64url").toString("utf8") };
  } catch {
    return null;
  }
}

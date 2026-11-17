import type { Json } from "./output.ts";
import type { Reference } from "./reference.ts";

export const base64url = (data: string | Uint8Array): string => Buffer.from(data).toString("base64url");

/** A test-only ES256 key. The private half exists only in memory and is never written. */
export interface Signer {
  /** The public key in the form that the hub of the reference publishes, with the members of `jwk` added. */
  publicJwk: Record<string, Json>;
  /** Signs the UTF-8 bytes of a text, or the bytes given. */
  sign(header: Record<string, string>, payload: string | Uint8Array): Promise<string>;
  /**
   * Signs a header, given as its JSON text or bytes, and a payload segment, given as the token holds it. It makes the
   * tokens that `sign` refuses, such as a header that `crit` marks.
   */
  signSegments(header: string | Uint8Array, payloadSegment: string): Promise<string>;
}

export async function newSigner(reference: Reference, kid: string, jwk: Record<string, Json> = {}): Promise<Signer> {
  const { publicKey, privateKey } = await reference.jose.generateKeyPair("ES256");
  return {
    publicJwk: { ...(await reference.jose.exportJWK(publicKey)), kid, alg: "ES256", use: "sig", ...jwk },
    sign: (header, payload) => new reference.jose.CompactSign(typeof payload === "string" ? new TextEncoder().encode(payload) : payload).setProtectedHeader(header).sign(privateKey),
    async signSegments(header, payloadSegment) {
      const input = `${base64url(header)}.${payloadSegment}`;
      const signature = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, privateKey, new TextEncoder().encode(input));
      return `${input}.${base64url(new Uint8Array(signature))}`;
    },
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

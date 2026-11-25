import { canonicalJSON } from "./canonical.ts";
import type { SigningPurpose } from "./jws.ts";

type KeyIdentity = { team: string; purpose: SigningPurpose; kid: string };
type Bytes = ArrayBuffer | Uint8Array<ArrayBuffer>;
type WrappedKey = { ciphertext: ArrayBuffer; nonce: Uint8Array<ArrayBuffer> };

function importWrapKey(raw: Bytes, usage: "encrypt" | "decrypt"): Promise<CryptoKey> {
  if (raw.byteLength !== 32) throw new Error("Expected a 32-byte AES wrapping key");
  return crypto.subtle.importKey("raw", raw, "AES-GCM", false, [usage]);
}

function parameters(nonce: Uint8Array<ArrayBuffer>, { team, purpose, kid }: KeyIdentity) {
  if (nonce.length !== 12) throw new Error("Expected a 12-byte AES-GCM nonce");
  return {
    name: "AES-GCM",
    iv: nonce,
    additionalData: new TextEncoder().encode(canonicalJSON([team, purpose, kid])),
    tagLength: 128,
  };
}

export async function wrapPKCS8(pkcs8: Bytes, rawKey: Bytes, identity: KeyIdentity): Promise<WrappedKey> {
  const key = await importWrapKey(rawKey, "encrypt");
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const ciphertext = await crypto.subtle.encrypt(parameters(nonce, identity), key, pkcs8);
  return { ciphertext, nonce };
}

export async function unwrapPKCS8(wrapped: WrappedKey, rawKey: Bytes, identity: KeyIdentity): Promise<ArrayBuffer> {
  const key = await importWrapKey(rawKey, "decrypt");
  return crypto.subtle.decrypt(parameters(wrapped.nonce, identity), key, wrapped.ciphertext);
}

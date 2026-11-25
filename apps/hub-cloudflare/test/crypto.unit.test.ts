import { Buffer } from "node:buffer";
import { createHash } from "node:crypto";
import { describe, expect, test } from "vitest";
import canonicalFixtures from "../../../spec/canonical-json/v1/fixtures/canonicalize.json?raw";
import policyFixtures from "../../../spec/policy/v1/fixtures/verify-signed-bundle.json?raw";
import keySelectionFixtures from "../../../spec/policy/v1/fixtures/verify-signed-bundle-key-selection.json?raw";
import chainFixtures from "../../../spec/audit/v1/fixtures/verify-chain.json?raw";
import checkpointFixtures from "../../../spec/audit/v1/fixtures/checkpoint.json?raw";
import { genesisHash, seal, verifyChain, verifyCheckpoint } from "../src/audit";
import { canonicalJSON, canonicalize, isJsonObject, parseJSON } from "../src/canonical";
import type { JsonObject, JsonValue } from "../src/canonical";
import { exportPKCS8, exportPrivateJWK, exportPublicJWK, generateSigningKey, importPKCS8, importPrivateJWK, JWSError, signJWS, verifyJWS } from "../src/jws";
import type { JWSStage } from "../src/jws";
import { verifyPolicyBundle } from "../src/policy";
import { unwrapPKCS8, wrapPKCS8 } from "../src/wrap";
import { verifyExportFixture } from "./fixture-verifiers";

function object(value: JsonValue): JsonObject {
  if (!isJsonObject(value)) throw new Error("Fixture must supply an object");
  return value;
}

function list(value: JsonValue): JsonValue[] {
  if (!Array.isArray(value)) throw new Error("Fixture must supply an array");
  return value;
}

function string(value: JsonValue): string {
  if (typeof value !== "string") throw new Error("Fixture must supply a string");
  return value;
}

function fixture(source: string, duplicates?: WeakSet<JsonObject>) {
  const doc = object(parseJSON(source, { duplicates }));
  return {
    shared: doc.shared,
    cases: list(doc.cases).map((item) => {
      const { name, input, expected } = object(item);
      return { name: string(name), input: object(input), expected };
    }),
  };
}

describe("canonicalize.json", () => {
  test.each(fixture(canonicalFixtures).cases)("$name", ({ input, expected }) => {
    const canonical = canonicalize(string(input.json));
    const sha256 = createHash("sha256").update(canonical).digest("hex");
    expect({ canonical, sha256 }).toEqual(expected);
  });
});

const policyRejectionStages = new Map<string, JWSStage>([
  ["b64 false without crit has no effect", "payload"],
  ["a crit that names an unknown parameter does not verify", "header"],
  ["an empty crit does not verify", "header"],
  ["a payload changed after signing does not verify", "signature"],
  ["a signature from another key with the same kid does not verify", "signature"],
  ["a kid that is not in the key set does not verify", "key"],
  ["a signature with the last character removed does not verify", "signature"],
  ["alg HS256 does not verify", "header"],
  ["alg none does not verify", "header"],
  ["a missing typ does not verify", "header"],
  ["another typ does not verify", "header"],
  ["a signed payload that is not a bundle does not verify", "payload"],
  ["a signed payload with an invalid temporary grant does not verify", "payload"],
  ["a signed payload that is not JSON does not verify", "payload"],
  ["a token with two parts does not verify", "compact"],
  ["an empty string does not verify", "compact"],
  ["a kid that names another key of the set does not verify", "signature"],
  ["a header without kid does not verify when several keys are candidates", "key"],
  ["two keys with the same kid do not verify, even when one of them signed", "key"],
  ["a key whose use is enc does not verify", "key"],
  ["a key whose key_ops lacks verify does not verify", "key"],
  ["a key whose alg is not ES256 does not verify", "key"],
  ["a key whose key_ops holds verify and sign does not verify", "key"],
  ["a kid that is a number matches no key, even a key with the same number", "key"],
]);

for (const [name, source] of [
  ["verify-signed-bundle.json", policyFixtures],
  ["verify-signed-bundle-key-selection.json", keySelectionFixtures],
]) {
  describe(name, () => {
    const { shared, cases } = fixture(source);
    const jwks = canonicalJSON(object(shared).jwks);
    test.each(cases)("$name", async ({ name, input, expected }) => {
      const ok = object(expected).ok === true;
      const stage = policyRejectionStages.get(name);
      if (!ok && !stage) throw new Error(`Missing rejection stage for ${name}`);
      const verification = verifyPolicyBundle(string(input.signature), jwks);
      if (ok) {
        const { bundle } = await verification;
        expect({ ok: true, bundle }).toEqual(expected);
      } else {
        await expect(verification).rejects.toBeInstanceOf(JWSError);
        await expect(verification).rejects.toHaveProperty("stage", stage);
      }
    });
  });
}

test("policy verification rejects overflowing numbers anywhere in a trusted JWKS", async () => {
  const { shared, cases } = fixture(policyFixtures);
  const signature = string(cases[0].input.signature);
  const keys = canonicalJSON(object(shared).jwks);
  const withMetadata = (metadata: string) => `{${metadata},${keys.slice(1)}`;
  await expect(verifyPolicyBundle(signature, withMetadata('"observation":"1e400","tiny":1e-400'))).resolves.toHaveProperty("bundle");
  for (const metadata of ['"observation":1e400', '"observation":{"sample":-1e400,"sample":0}']) {
    await expect(verifyPolicyBundle(signature, withMetadata(metadata))).rejects.toMatchObject({ stage: "key" });
  }
});

test("signed policy and checkpoint headers apply their number rules", async () => {
  const { privateKey, publicKey } = await generateSigningKey();
  const sign = async (header: string, payload: JsonValue) => {
    const input = Buffer.from(header).toString("base64url") + "."
      + Buffer.from(canonicalJSON(payload)).toString("base64url");
    const signature = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, privateKey, new TextEncoder().encode(input));
    return input + "." + Buffer.from(signature).toString("base64url");
  };
  const policyKey = await exportPublicJWK(publicKey, "policy");
  const policyKeys = canonicalJSON({ keys: [policyKey] });
  const policyHeader = `"alg":"ES256","kid":"${policyKey.kid}","typ":"mp-policy+jws"`;
  const bundle = object(fixture(policyFixtures).cases[0].expected).bundle;
  const finite = await sign(`{${policyHeader},"observation":1e3,"quoted":"1e400","tiny":1e-400}`, bundle);
  await expect(verifyPolicyBundle(finite, policyKeys)).resolves.toHaveProperty("bundle", bundle);
  for (const metadata of ['"observation":1e400', '"observation":{"sample":-1e400,"sample":0}']) {
    const token = await sign(`{${policyHeader},${metadata}}`, bundle);
    await expect(verifyPolicyBundle(token, policyKeys)).rejects.toHaveProperty("stage", "header");
  }

  const auditKey = await exportPublicJWK(publicKey, "audit");
  const auditKeys = canonicalJSON({ keys: [auditKey] });
  const auditHeader = `"alg":"ES256","kid":"${auditKey.kid}","typ":"openplexer-audit-checkpoint+jws"`;
  const checkpoint = {
    v: 1, workspace_id: "test", entries: 0, tip_hash: genesisHash, issued_at: "2026-11-25T00:00:00.000Z",
  };
  await expect(verifyCheckpoint(await sign(`{${auditHeader}}`, checkpoint), auditKeys)).resolves.toMatchObject(checkpoint);
  for (const metadata of ['"observation":1000', '"observation":1e400', '"alg":1e400']) {
    const token = await sign(`{${metadata},${auditHeader}}`, checkpoint);
    await expect(verifyCheckpoint(token, auditKeys)).rejects.toHaveProperty("stage", "header");
  }
});

describe("verify-chain.json", () => {
  test.each(fixture(chainFixtures).cases)("$name", ({ input, expected }) => {
    expect(verifyChain(list(input.records).map(object))).toEqual(expected);
  });

  test("seals each record of the committed valid chain", () => {
    const valid = fixture(chainFixtures).cases.find(({ name }) => name === "a chain of six records is valid");
    if (!valid) throw new Error("Missing valid six-record fixture");
    for (const value of list(valid.input.records)) {
      const record = object(value);
      const { prev_hash, hash: _hash, ...fields } = record;
      const nullable = (value: JsonValue) => value === null ? null : string(value);
      expect(seal({
        id: string(fields.id),
        workspace_id: string(fields.workspace_id),
        actor_member_id: nullable(fields.actor_member_id),
        agent_session_id: nullable(fields.agent_session_id),
        action: string(fields.action),
        target: string(fields.target),
        reason: nullable(fields.reason),
        meta: object(fields.meta),
        flagged: fields.flagged === true,
        created_at: string(fields.created_at),
      }, string(prev_hash))).toEqual(record);
    }
  });
});

describe("checkpoint.json", () => {
  // Read the committed source so repeated members survive as parse metadata.
  const duplicates = new WeakSet<JsonObject>();
  const { shared, cases } = fixture(checkpointFixtures, duplicates);
  test.each(cases)("$name", async ({ input, expected }) => {
    const jwks = typeof input.jwks_json === "string" ? input.jwks_json : canonicalJSON(object(shared).jwks);
    expect(await verifyExportFixture(input.export, duplicates, jwks, list(input.checkpoints).map(string))).toEqual(expected);
  });
});

test("rejects malformed JSON, malformed UTF-8, and excessive nesting", () => {
  expect(() => canonicalize('{"a":1} []')).toThrow(SyntaxError);
  expect(() => canonicalize(Uint8Array.of(34, 0xff, 34))).toThrow();
  const deepest = "[".repeat(10_000) + "0" + "]".repeat(10_000);
  expect(canonicalize(deepest)).toBe(deepest);
  expect(() => canonicalize("[" + deepest + "]")).toThrow(SyntaxError);
});

test("round trips generated P-256 keys through PKCS#8 and JWK", async () => {
  const { privateKey, publicKey } = await generateSigningKey();
  const jwk = await exportPublicJWK(publicKey, "policy");
  const jwks = canonicalJSON({ keys: [jwk] });
  const restored = [
    await importPKCS8(await exportPKCS8(privateKey)),
    await importPrivateJWK(await exportPrivateJWK(privateKey)),
  ];
  const payload = { data: "日本" };
  for (const key of restored) {
    const token = await signJWS(payload, key, jwk.kid, "policy");
    expect(new TextDecoder().decode(await verifyJWS(token, jwks, "policy"))).toBe(canonicalJSON(payload));
  }
});

test("wraps PKCS#8 with fresh nonces and authenticates the key identity and ciphertext", async () => {
  const { privateKey, publicKey } = await generateSigningKey();
  const pkcs8 = await exportPKCS8(privateKey);
  const { kid } = await exportPublicJWK(publicKey, "audit");
  const identity = { team: "demo", purpose: "audit", kid } satisfies Parameters<typeof wrapPKCS8>[2];
  const wrappingKey = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, true, ["encrypt", "decrypt"]);
  if ("privateKey" in wrappingKey) throw new Error("Expected an AES-GCM key");
  const rawKey = await crypto.subtle.exportKey("raw", wrappingKey);
  if (!(rawKey instanceof ArrayBuffer)) throw new Error("Expected raw AES-GCM key bytes");
  const key = new Uint8Array(rawKey);
  const first = await wrapPKCS8(pkcs8, key, identity);
  const second = await wrapPKCS8(pkcs8, key, identity);
  expect(first.nonce).toHaveLength(12);
  expect(first.nonce).not.toEqual(second.nonce);
  expect(new Uint8Array(first.ciphertext)).not.toEqual(new Uint8Array(second.ciphertext));
  for (const wrapped of [first, second]) {
    expect(new Uint8Array(await unwrapPKCS8(wrapped, key, identity))).toEqual(new Uint8Array(pkcs8));
  }

  for (const modified of [
    { ...identity, team: "other" },
    { ...identity, purpose: "policy" },
    { ...identity, kid: kid + "other" },
  ] satisfies Parameters<typeof unwrapPKCS8>[2][]) {
    await expect(unwrapPKCS8(first, key, modified)).rejects.toThrow();
  }
  const ciphertext = first.ciphertext.slice(0);
  const nonce = first.nonce.slice();
  new Uint8Array(ciphertext)[0] ^= 1;
  nonce[0] ^= 1;
  const wrongKey = key.slice();
  wrongKey[0] ^= 1;
  await expect(unwrapPKCS8({ ...first, ciphertext }, key, identity)).rejects.toThrow();
  await expect(unwrapPKCS8({ ...first, nonce }, key, identity)).rejects.toThrow();
  await expect(unwrapPKCS8(first, wrongKey, identity)).rejects.toThrow();
  await expect(wrapPKCS8(pkcs8, key.subarray(0, 16), identity)).rejects.toThrow();
});

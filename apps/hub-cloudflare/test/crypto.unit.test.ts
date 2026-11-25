import { createHash } from "node:crypto";
import { describe, expect, test } from "vitest";
import canonicalFixtures from "../../../spec/canonical-json/v1/fixtures/canonicalize.json?raw";
import policyFixtures from "../../../spec/policy/v1/fixtures/verify-signed-bundle.json?raw";
import keySelectionFixtures from "../../../spec/policy/v1/fixtures/verify-signed-bundle-key-selection.json?raw";
import chainFixtures from "../../../spec/audit/v1/fixtures/verify-chain.json?raw";
import checkpointFixtures from "../../../spec/audit/v1/fixtures/checkpoint.json?raw";
import { seal, verifyChain } from "../src/audit";
import { canonicalJSON, canonicalize, isJsonObject, parseJSON } from "../src/canonical";
import type { JsonObject, JsonValue } from "../src/canonical";
import { exportPKCS8, exportPrivateJWK, exportPublicJWK, generateSigningKey, importPKCS8, importPrivateJWK, JWSError, signJWS, verifyJWS } from "../src/jws";
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

for (const [name, source] of [
  ["verify-signed-bundle.json", policyFixtures],
  ["verify-signed-bundle-key-selection.json", keySelectionFixtures],
]) {
  describe(name, () => {
    const { shared, cases } = fixture(source);
    const jwks = canonicalJSON(object(shared).jwks);
    test.each(cases)("$name", async ({ input, expected }) => {
      const verification = verifyPolicyBundle(string(input.signature), jwks);
      if (object(expected).ok === true) {
        const { bundle } = await verification;
        expect({ ok: true, bundle }).toEqual(expected);
      } else {
        await expect(verification).rejects.toBeInstanceOf(JWSError);
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
  const key = crypto.getRandomValues(new Uint8Array(32));
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

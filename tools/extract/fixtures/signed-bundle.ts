import { asCommitted, explain, render, type Files, type Json, type SealedFiles } from "../output.ts";
import type { Reference } from "../reference.ts";
import { base64url, newSigner, type Signer } from "../signing.ts";
import { fixtureFile, fixturePath, type Case, type FixtureFile } from "./format.ts";

const KID = "hk_fixture";
const SOURCES = ["apps/daemon/src/policy/store.ts", "apps/hub/src/policy/canonical.ts"];

const BUNDLE: { [key: string]: Json } = {
  version: 7,
  project_id: "prj-shop",
  repos: ["api"],
  areas: [{ name: "backend", patterns: ["api:src/**"] }],
  roles: { backend: { read: [], write: ["backend"], exec: ["npm test"] } },
  members: { "mem-ana": "backend" },
};

/** The signing keys of one fixture file, by name. */
type Keys = Record<string, Signer>;

/** How to make the keys of a fixture file: the `kid` of each key, the JWK members to add, and whether the key set holds it. */
type KeyPlan = Record<string, { kid: string; jwk?: Record<string, Json>; inKeySet: boolean }>;

interface Vector {
  name: string;
  why: string;
  /** The first two parts of the result do not depend on the keys. Only the signature part does. */
  build(keys: Keys): Promise<string>;
}

/** The UTF-8 bytes of a text, with each `#` replaced by the given bytes. */
const bytesWith = (text: string, hole: number[]): Uint8Array => new Uint8Array(text.split("#").flatMap((part, i) => [...(i === 0 ? [] : hole), ...new TextEncoder().encode(part)]));

const MAIN_KEYS: KeyPlan = { hub: { kid: KID, inKeySet: true }, other: { kid: KID, inKeySet: false } };

function vectors(reference: Reference): Vector[] {
  const canonical = reference.canonicalJson(asCommitted(BUNDLE));
  const header = { alg: "ES256", kid: KID, typ: reference.policyJwsTyp };
  const signed = (payload: string, head: Record<string, string> = header) => (keys: Keys) => keys.hub!.sign(head, payload);
  const withPayload = async (keys: Keys, payload: string): Promise<string> => {
    const [head, , signature] = (await keys.hub!.sign(header, canonical)).split(".");
    return [head, base64url(payload), signature].join(".");
  };
  const raw = (head: string | Uint8Array, payloadSegment = base64url(canonical)) => (keys: Keys) => keys.hub!.signSegments(head, payloadSegment);
  const headerWith = (extra: Record<string, Json>) => JSON.stringify({ ...header, ...extra });
  const altered = (change: (signature: string) => string) => async (keys: Keys) => {
    const [head, payload, signature] = (await keys.hub!.sign(header, canonical)).split(".");
    return `${head}.${payload}.${change(signature!)}`;
  };
  const { kid: _, ...noKid } = header;
  const { typ: __, ...noTyp } = header;
  return [
    { name: "a bundle signed by a key in the key set verifies", why: "The payload of a valid signature is returned as the bundle.", build: signed(canonical) },
    {
      name: "a bundle with unknown fields keeps them",
      why: "The verified bundle is the payload, with the fields that the bundle schema does not name.",
      build: signed(reference.canonicalJson({ ...BUNDLE, note: "draft", areas: [{ name: "backend", patterns: ["api:src/**"], color: "red" }] })),
    },
    {
      name: "a payload that is not canonical JSON still verifies",
      why: "The verifier checks the signature over the bytes that it receives. It does not check that those bytes are canonical.",
      build: signed(JSON.stringify(BUNDLE, null, 2)),
    },
    { name: "a header without kid verifies when one key in the set fits", why: "Without `kid`, the key is chosen by `alg` and key type.", build: signed(canonical, noKid) },
    {
      name: "a payload that starts with a byte order mark verifies",
      why: "The payload is decoded as UTF-8 with the byte order mark removed. The twelfth open question of the README records the behavior.",
      build: (keys) => keys.hub!.sign(header, bytesWith(`#${canonical}`, [0xef, 0xbb, 0xbf])),
    },
    {
      name: "an invalid UTF-8 sequence in a payload becomes one U+FFFD",
      why: "The payload is decoded as UTF-8, and each invalid sequence becomes U+FFFD, as the WHATWG Encoding Standard decodes it. The three bytes of a truncated four-byte sequence become one U+FFFD. The twelfth open question of the README records the behavior.",
      build: (keys) => keys.hub!.sign(header, bytesWith(reference.canonicalJson({ ...BUNDLE, project_id: "prj-#" }), [0xf0, 0x9f, 0x98])),
    },
    {
      name: "a repeated key in the payload keeps the last member",
      why: "The payload is parsed as JSON, and a later member replaces an earlier one with the same key, so `version` is 7. The twelfth open question of the README records the behavior.",
      build: signed(`{"version":1,${canonical.slice(1)}`),
    },
    {
      name: "a repeated key in the header keeps the last member",
      why: "The header is parsed as JSON, and the later `alg` replaces the earlier one, so the header has the `alg` ES256. The twelfth open question of the README records the behavior.",
      build: raw(`{"alg":"none","alg":"ES256","kid":${JSON.stringify(KID)},"typ":${JSON.stringify(reference.policyJwsTyp)}}`),
    },
    {
      name: "a header that starts with a byte order mark verifies",
      why: "The header is decoded as UTF-8 with the byte order mark removed. The twelfth open question of the README records the behavior.",
      build: raw(bytesWith(`#${headerWith({})}`, [0xef, 0xbb, 0xbf])),
    },
    {
      name: "a signature with = padding verifies",
      why: "A segment is decoded with the forgiving-base64 decode, which accepts padding to a multiple of four. The signature is not part of the signing input, so the padding needs no key. The twelfth open question of the README records the behavior.",
      build: altered((signature) => `${signature}==`),
    },
    {
      name: "a signature with a space inside verifies",
      why: "The forgiving-base64 decode ignores ASCII white space anywhere in a segment. The twelfth open question of the README records the behavior.",
      build: altered((signature) => `${signature.slice(0, 40)} ${signature.slice(40)}`),
    },
    {
      name: "a line feed after the token verifies",
      why: "The line feed belongs to the signature segment, and the decode ignores it. The twelfth open question of the README records the behavior.",
      build: altered((signature) => `${signature}\n`),
    },
    {
      name: "an unencoded payload verifies when crit names b64 and b64 is false",
      why: "With `b64` false and `crit` naming it, the payload segment is the payload itself, as RFC 7797 defines. The twelfth open question of the README records the behavior.",
      build: raw(headerWith({ b64: false, crit: ["b64"] }), canonical),
    },
    {
      name: "b64 false without crit has no effect",
      why: "`b64` counts only when `crit` names it. Without `crit`, the payload segment is decoded as base64url, and the JSON text of the bundle does not decode.",
      build: raw(headerWith({ b64: false }), canonical),
    },
    {
      name: "a crit that names an unknown parameter does not verify",
      why: "`crit` may name only `b64`. A verifier rejects a header that asks it to understand a parameter that it does not know.",
      build: raw(headerWith({ crit: ["exp"], exp: 1 })),
    },
    { name: "an empty crit does not verify", why: "A `crit` that is present must name at least one parameter.", build: raw(headerWith({ crit: [] })) },
    {
      name: "a payload changed after signing does not verify",
      why: "The signature covers the payload.",
      build: (keys) => withPayload(keys, reference.canonicalJson({ ...BUNDLE, members: { "mem-ana": "backend", "mem-eve": "backend" } })),
    },
    { name: "a signature from another key with the same kid does not verify", why: "The `kid` selects the key. It proves nothing.", build: (keys) => keys.other!.sign(header, canonical) },
    { name: "a kid that is not in the key set does not verify", why: "A signature is checked only against the keys of the key set.", build: (keys) => keys.other!.sign({ ...header, kid: "hk_unknown" }, canonical) },
    {
      name: "a signature with the last character removed does not verify",
      why: "A truncated signature is not a valid ES256 signature.",
      build: async (keys) => (await keys.hub!.sign(header, canonical)).slice(0, -1),
    },
    {
      name: "alg HS256 does not verify",
      why: "Only ES256 is accepted. An HMAC keyed with the public key must not pass.",
      build: (keys) => new reference.jose.CompactSign(new TextEncoder().encode(canonical)).setProtectedHeader({ ...header, alg: "HS256" }).sign(new TextEncoder().encode(JSON.stringify(keys.hub!.publicJwk))),
    },
    {
      name: "alg none does not verify",
      why: "Only ES256 is accepted. An unsigned token must not pass.",
      build: async () => `${base64url(JSON.stringify({ ...header, alg: "none" }))}.${base64url(canonical)}.`,
    },
    { name: "a missing typ does not verify", why: "The protected header must carry the `typ` of a policy bundle.", build: signed(canonical, noTyp) },
    { name: "another typ does not verify", why: "A token that the same key signed for another purpose is not a policy bundle.", build: signed(canonical, { ...header, typ: "JWT" }) },
    {
      name: "a signed payload that is not a bundle does not verify",
      why: "The payload must validate against the bundle schema.",
      build: signed(reference.canonicalJson({ version: 7, project_id: "prj-shop" })),
    },
    {
      name: "a signed payload with an invalid temporary grant does not verify",
      why: "The whole payload is validated, including the closed temporary grant objects.",
      build: signed(reference.canonicalJson({ ...BUNDLE, temporary_grants: [{ member_id: "mem-ana", area: "backend", actions: ["write"], scope: "mission", mission_id: "mis-1", note: "x" }] })),
    },
    { name: "a signed payload that is not JSON does not verify", why: "The payload must parse as JSON.", build: signed("not json") },
    { name: "a token with two parts does not verify", why: "A compact JWS has three parts.", build: async (keys) => (await keys.hub!.sign(header, canonical)).split(".").slice(0, 2).join(".") },
    { name: "an empty string does not verify", why: "A missing signature is a failure, not an error.", build: async () => "" },
  ];
}

/** A key set with several keys, for the rule that chooses the key. */
const SELECTION_KEYS: KeyPlan = {
  a: { kid: "hk_a", inKeySet: true },
  b: { kid: "hk_b", inKeySet: true },
  twinA: { kid: "hk_twin", inKeySet: true },
  twinB: { kid: "hk_twin", inKeySet: true },
  encryption: { kid: "hk_enc", jwk: { use: "enc" }, inKeySet: true },
  signOnly: { kid: "hk_ops", jwk: { key_ops: ["sign"] }, inKeySet: true },
  es384: { kid: "hk_es384", jwk: { alg: "ES384" }, inKeySet: true },
  verifyOnly: { kid: "hk_ops_verify", jwk: { key_ops: ["verify"] }, inKeySet: true },
  verifyAndSign: { kid: "hk_ops_mixed", jwk: { key_ops: ["verify", "sign"] }, inKeySet: true },
  numberKid: { kid: "hk_number", jwk: { kid: 1 }, inKeySet: true },
};

function selectionVectors(reference: Reference): Vector[] {
  const canonical = reference.canonicalJson(asCommitted(BUNDLE));
  const header = (kid?: string) => ({ alg: "ES256", ...(kid === undefined ? {} : { kid }), typ: reference.policyJwsTyp });
  const by = (key: string, kid?: string) => (keys: Keys) => keys[key]!.sign(header(kid), canonical);
  return [
    { name: "a kid selects its key among several", why: "The key whose `kid` equals the `kid` of the header is the only candidate.", build: by("a", "hk_a") },
    { name: "a kid selects a key that is not first in the set", why: "The order of the key set does not matter.", build: by("b", "hk_b") },
    { name: "a kid that names another key of the set does not verify", why: "The signature is checked only with the key that the `kid` selects, not with every key of the set.", build: by("a", "hk_b") },
    { name: "a header without kid does not verify when several keys are candidates", why: "Without `kid`, every key of the type ES256 needs is a candidate, and more than one candidate is a failure.", build: by("a") },
    { name: "two keys with the same kid do not verify, even when one of them signed", why: "More than one candidate is a failure. A bundle verifier does not try each candidate.", build: by("twinA", "hk_twin") },
    { name: "a key whose use is enc does not verify", why: "A key with `use` other than `sig` is not a candidate.", build: by("encryption", "hk_enc") },
    { name: "a key whose key_ops lacks verify does not verify", why: "A key with `key_ops` that does not hold `verify` is not a candidate.", build: by("signOnly", "hk_ops") },
    { name: "a key whose alg is not ES256 does not verify", why: "A key with an `alg` other than `ES256` is not a candidate.", build: by("es384", "hk_es384") },
    { name: "a key whose key_ops is verify verifies", why: "A key whose `key_ops` holds only `verify` is a candidate, and it verifies.", build: by("verifyOnly", "hk_ops_verify") },
    {
      name: "a key whose key_ops holds verify and sign does not verify",
      why: "The key is the only candidate, but a candidate whose `key_ops` holds an operation other than `verify` fails.",
      build: by("verifyAndSign", "hk_ops_mixed"),
    },
    {
      name: "a kid that is a number matches no key, even a key with the same number",
      why: "A header `kid` matches a key only if it is a string.",
      build: (keys) => keys.numberKid!.signSegments(JSON.stringify({ alg: "ES256", kid: 1, typ: reference.policyJwsTyp }), base64url(canonical)),
    },
  ];
}

const unsigned = (jws: string): string => jws.split(".").slice(0, 2).join(".");

async function makeKeys(reference: Reference, plan: KeyPlan): Promise<Keys> {
  const keys: Keys = {};
  for (const [name, { kid, jwk }] of Object.entries(plan)) keys[name] = await newSigner(reference, kid, jwk);
  return keys;
}

interface SealedFixture {
  name: string;
  description: string;
  keys: KeyPlan;
  vectors: Vector[];
}

/** The signed-bundle fixture files: each holds a key set, and tokens that the reference verifier accepts or rejects. */
export function signedBundleFixtures(reference: Reference): SealedFiles[] {
  return [
    {
      name: "verify-signed-bundle",
      description: "Compact JWS tokens and the result of verifying each one against the key set in `shared`. The signing key was made for this file and then discarded.",
      keys: MAIN_KEYS,
      vectors: vectors(reference),
    },
    {
      name: "verify-signed-bundle-key-selection",
      description: "Compact JWS tokens signed with the keys of a key set that holds several keys, and the result of verifying each one against that key set. The rule that chooses the key decides each case. The signing keys were made for this file and then discarded.",
      keys: SELECTION_KEYS,
      vectors: selectionVectors(reference),
    },
  ].map((fixture) => sealedFixture(reference, fixture));
}

function sealedFixture(reference: Reference, { name, description, keys: plan, vectors: all }: SealedFixture): SealedFiles {
  const path = fixturePath("policy", name);
  const judge = async (v: Vector, signature: string, jwks: Json): Promise<Case> => {
    const result = await reference.verifyBundleJws(signature, jwks);
    const base = { name: v.name, why: v.why, input: { signature } };
    return result.ok ? { ...base, expected: { ok: true, bundle: result.bundle } } : { ...base, expected: { ok: false }, informational: { reason: result.reason } };
  };
  const document = (jwks: Json, cases: Case[]): FixtureFile =>
    fixtureFile({
      contract: "policy",
      operation: "verify-signed-bundle",
      description,
      sources: SOURCES,
      shared: { jwks },
      cases,
    });
  return {
    paths: [path],
    async create(): Promise<Files> {
      const keys = await makeKeys(reference, plan);
      const jwks = { keys: Object.entries(plan).filter(([, key]) => key.inKeySet).map(([key]) => keys[key]!.publicJwk) };
      const cases: Case[] = [];
      for (const v of all) cases.push(await judge(v, await v.build(keys), jwks));
      return new Map([[path, render(document(jwks, cases) as unknown as Json)]]);
    },
    async verify(committed: Files): Promise<string[]> {
      const text = committed.get(path)!;
      const parsed = JSON.parse(text) as FixtureFile & { shared: { jwks: { keys: Record<string, Json>[] } } };
      const { jwks } = parsed.shared;
      const problems: string[] = [];
      if (jwks.keys.some((key) => "d" in key)) problems.push(`${path}: the key set holds a private key`);
      const throwaway = await makeKeys(reference, plan);
      const byName = new Map(parsed.cases.map((c) => [c.name, c]));
      const expected: Case[] = [];
      for (const v of all) {
        const found = byName.get(v.name);
        if (!found) {
          problems.push(`${path}: case ${JSON.stringify(v.name)}: missing from the committed file; run generate --new-signatures`);
          continue;
        }
        const signature = (found.input as { signature: string }).signature;
        if (unsigned(signature) !== unsigned(await v.build(throwaway))) {
          problems.push(`${path}: case ${JSON.stringify(v.name)}: the header or the payload differs from the generator; run generate --new-signatures`);
        }
        expected.push(await judge(v, signature, jwks));
      }
      for (const name of byName.keys()) {
        if (!all.some((v) => v.name === name)) problems.push(`${path}: case ${JSON.stringify(name)}: not produced by the generator`);
      }
      return [...problems, ...explain(path, text, render(document(jwks, expected) as unknown as Json))];
    },
  };
}

import { buildCheckpoint, genesisHash, seal, signCheckpoint } from "../src/audit.ts";
import { exportPublicJWK, importPublicJWK, importPrivateJWK, signJWS } from "../src/jws.ts";

// Fixed public test scalars 1 and 2. Never use these keys outside tests.
const testKeys = {
  policy: {
    kty: "EC", crv: "P-256",
    d: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE",
    x: "axfR8uEsQkf4vOblY6RA8ncDfYEt6zOg9KE5RdiYwpY",
    y: "T-NC4v4af5uO5-tKfA-eFivOM1drMV7Oy7ZAaDe_UfU",
  },
  audit: {
    kty: "EC", crv: "P-256",
    d: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAI",
    x: "fPJ7GI0DT36KUjgDBLUaw8CJaeJ38hs1pgtI_EdmmXg",
    y: "B3dVENuO0EApPZrGn3Qw27p9reY86YIpngS3nSJ4c9E",
  },
};

export async function generateVectors() {
  const workspace_id = "wsp-ts-vectors";
  const created_at = "2026-11-25T12:00:00.000Z";
  const bundle = {
    version: 1,
    project_id: "prj-shop",
    repos: ["api"],
    areas: [],
    roles: {},
    members: {},
    note: "Zoë <&>\u2028日本",
  };

  const base = {
    workspace_id, actor_member_id: "mem-zoë", agent_session_id: null, reason: null, flagged: false, created_at,
  };
  const first = seal({ ...base, id: "1", action: "project.added", target: bundle.project_id, meta: null }, genesisHash);
  const second = seal({
    ...base, id: "2", action: "policy.updated", target: bundle.project_id,
    meta: { version: bundle.version, details: { "9": 1e-7, "10": "日本", "😀": "&", "～": "<" } },
  }, first.hash);

  const payload = buildCheckpoint({ workspace_id, entries: 2, tip_hash: second.hash, issued_at: created_at });
  const publicKey = async (purpose: keyof typeof testKeys) => {
    const { kty, crv, x, y } = testKeys[purpose];
    return exportPublicJWK(await importPublicJWK({ kty, crv, x, y }, purpose), purpose);
  };
  const policyJWK = await publicKey("policy");
  const auditJWK = await publicKey("audit");
  return {
    warning: "Public fixed TEST KEYS. Never use these private keys outside tests.",
    test_keys: testKeys,
    policy: {
      bundle,
      jwks: { keys: [policyJWK] },
      signature: await signJWS(bundle, await importPrivateJWK(testKeys.policy), policyJWK.kid, "policy"),
    },
    audit: {
      export: {
        entries: [{ ...second, result: "updated" }, { ...first, result: "added" }],
        next_cursor: null,
      },
      payload,
      jwks: { keys: [auditJWK] },
      signature: await signCheckpoint(payload, await importPrivateJWK(testKeys.audit), auditJWK.kid),
    },
  };
}

export function renderVectors(vectors: Awaited<ReturnType<typeof generateVectors>>): string {
  return JSON.stringify(vectors, null, 2) + "\n";
}

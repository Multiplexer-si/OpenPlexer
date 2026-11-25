import { expect, test } from "vitest";
import { verifyCheckpoint } from "../src/audit";
import { canonicalJSON } from "../src/canonical";
import { verifyJWS } from "../src/jws";
import { verifyPolicyBundle } from "../src/policy";
import { generateVectors, renderVectors } from "./vector-data";
import committed from "./vectors/ts-signed.json?raw";

test("the committed vectors match the generator and their signatures verify", async () => {
  const vectors: Awaited<ReturnType<typeof generateVectors>> = JSON.parse(committed);
  const generated = await generateVectors();
  const signatureBytes = (token: string) => token.split(".")[2];
  const normalized = committed
    .replace(vectors.policy.signature, (token) => token.replace(/[^.]+$/, signatureBytes(generated.policy.signature)))
    .replace(vectors.audit.signature, (token) => token.replace(/[^.]+$/, signatureBytes(generated.audit.signature)));
  expect(normalized).toBe(renderVectors(generated));

  const policyJWKS = canonicalJSON(vectors.policy.jwks);
  const auditJWKS = canonicalJSON(vectors.audit.jwks);
  const { bundle, payload } = await verifyPolicyBundle(vectors.policy.signature, policyJWKS);
  expect(bundle).toEqual(vectors.policy.bundle);
  expect(new TextDecoder().decode(payload)).toBe(canonicalJSON(vectors.policy.bundle));
  expect(await verifyCheckpoint(vectors.audit.signature, auditJWKS)).toEqual(vectors.audit.payload);
  await expect(verifyJWS(vectors.policy.signature, policyJWKS, "audit")).rejects.toMatchObject({ stage: "header" });
  await expect(verifyJWS(vectors.audit.signature, auditJWKS, "policy")).rejects.toMatchObject({ stage: "header" });
});

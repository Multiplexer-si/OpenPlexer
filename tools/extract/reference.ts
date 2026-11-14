import { execFileSync } from "node:child_process";
import type { webcrypto } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import type { Json } from "./output.ts";

/** The commit of the existing TypeScript implementation that the committed files under `spec/` come from. */
export const REFERENCE_COMMIT = "7d80045a";

/** The directories of the reference that the extractor runs code from. */
const SOURCE_DIRS = ["packages/protocol/src", "apps/hub/src", "apps/daemon/src"];

export interface ZodSchema {
  safeParse(value: unknown): { success: true; data: unknown } | { success: false; error: { issues: { path: PropertyKey[]; message: string }[] } };
  omit(keys: Record<string, true>): ZodSchema;
}

export interface ZodObject extends ZodSchema {
  shape: Record<string, ZodSchema & { value?: unknown }>;
}

export type EvalResult = { decision: "allow" | "deny"; area?: string; rule: string };
export type BundleVerification = { ok: true; bundle: Json } | { ok: false; reason: string };

type CryptoKey = webcrypto.CryptoKey;

export interface Jose {
  generateKeyPair(alg: string, options?: { extractable?: boolean }): Promise<{ publicKey: CryptoKey; privateKey: CryptoKey }>;
  exportJWK(key: CryptoKey): Promise<Record<string, string>>;
  CompactSign: new (payload: Uint8Array) => {
    setProtectedHeader(header: Record<string, string>): { sign(key: CryptoKey | Uint8Array): Promise<string> };
  };
}

export interface Reference {
  toJsonSchema(schema: ZodSchema): Record<string, Json>;
  protocol: {
    actionSchema: ZodSchema;
    policyBundleSchema: ZodSchema;
    overrideSchema: ZodSchema;
    roomEventSchema: ZodSchema;
    envelopeSchema: ZodSchema;
    eventPayloadSchemas: Record<string, ZodSchema>;
    /** The schema that the hub and the daemon of the reference parse every message with. */
    wsMessageSchema: ZodSchema & { options: ZodObject[] };
    auditEntrySchema: ZodSchema;
    auditPageSchema: ZodSchema;
    auditVerificationSchema: ZodSchema;
    EVENT_TYPES: readonly string[];
    PROTOCOL_VERSION: number;
    validateEvent(type: string, body: unknown): { ok: true } | { ok: false; reason: string };
    evaluate(bundle: unknown, query: unknown): EvalResult;
  };
  canonicalJson(value: unknown): string;
  patternsOverlap(a: string, b: string): boolean;
  pathSetsOverlap(a: readonly string[], b: readonly string[]): boolean;
  genesisHash: string;
  computeAuditHash(prevHash: string, fields: object): string;
  policyJwsTyp: string;
  verifyBundleJws(signature: string, jwks: unknown): Promise<BundleVerification>;
  jose: Jose;
}

function git(root: string, args: string[]): string {
  return execFileSync("git", ["-C", root, ...args], { encoding: "utf8", env: { ...process.env, GIT_OPTIONAL_LOCKS: "0" } }).trim();
}

/** The ES module entry of a package, as the reference's own imports resolve it. */
function esmEntry(root: string, from: string, name: string): string {
  const cjs = createRequire(join(root, from, "package.json")).resolve(name);
  let dir = dirname(cjs);
  const manifest = () => (existsSync(join(dir, "package.json")) ? JSON.parse(readFileSync(join(dir, "package.json"), "utf8")) : {});
  while (manifest().name !== name) dir = dirname(dir);
  const entry = manifest().exports["."];
  return join(dir, typeof entry === "string" ? entry : (entry.import ?? entry.default));
}

/**
 * Loads the reference code by path. The extractor never writes in the reference directory. It refuses a checkout
 * that is not at REFERENCE_COMMIT or that has uncommitted changes in the code it runs, because the committed
 * files name that commit as their source.
 */
export async function loadReference(root: string): Promise<Reference> {
  const head = git(root, ["rev-parse", "HEAD"]);
  if (!head.startsWith(REFERENCE_COMMIT)) {
    throw new Error(`the reference checkout is at ${head.slice(0, 8)}, but the contracts come from ${REFERENCE_COMMIT}`);
  }
  const dirty = git(root, ["status", "--porcelain", "--untracked-files=no", "--", ...SOURCE_DIRS]);
  if (dirty !== "") throw new Error(`the reference checkout has uncommitted changes:\n${dirty}`);

  const load = (path: string) => import(pathToFileURL(path).href);
  const protocol = await load(join(root, "packages/protocol/src/index.ts"));
  const { z } = await load(esmEntry(root, "packages/protocol", "zod"));
  const toJsonSchema = (schema: ZodSchema) => z.toJSONSchema(schema, { io: "input" }) as Record<string, Json>;

  // The daemon's verifier imports the built protocol package, not its source. A stale build would verify bundles
  // against an older bundle schema than the one the contracts are generated from.
  const built = await load(join(root, "packages/protocol/dist/index.js"));
  if (JSON.stringify(toJsonSchema(built.policyBundleSchema)) !== JSON.stringify(toJsonSchema(protocol.policyBundleSchema))) {
    throw new Error("the built protocol package of the reference differs from its source; rebuild the reference");
  }

  const canonical = await load(join(root, "apps/hub/src/policy/canonical.ts"));
  const overlap = await load(join(root, "apps/hub/src/claims/overlap.ts"));
  const audit = await load(join(root, "apps/hub/src/audit/log.ts"));
  const store = await load(join(root, "apps/daemon/src/policy/store.ts"));
  return {
    toJsonSchema,
    protocol,
    canonicalJson: canonical.canonicalJson,
    patternsOverlap: overlap.patternsOverlap,
    pathSetsOverlap: overlap.pathSetsOverlap,
    genesisHash: audit.GENESIS_HASH,
    computeAuditHash: audit.computeAuditHash,
    policyJwsTyp: store.POLICY_JWS_TYP,
    verifyBundleJws: store.verifyBundleJws,
    jose: await load(esmEntry(root, "apps/daemon", "jose")),
  };
}

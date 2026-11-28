import { DatabaseSync } from "node:sqlite";
import { expect, test } from "vitest";
import { parsePolicyBundle } from "../src/policy";
import { schemaMigrations } from "../src/storage";

test("dashboard schema installs a session digest table and expiry index at migration 5", () => {
  using db = new DatabaseSync(":memory:");
  const sessions = schemaMigrations.find(({ version }) => version === 5);
  expect(sessions?.version).toBe(5);
  if (!sessions) return;
  for (const migration of schemaMigrations) db.exec(migration.sql);
  db.exec(sessions.sql);
  expect(db.prepare("PRAGMA table_info(admin_sessions)").all().map(({ name }) => name)).toEqual([
    "session_digest", "member_id", "enrollment_id", "csrf_secret", "expires_at_ms",
  ]);
  expect(db.prepare("PRAGMA index_info(admin_sessions_expiry)").all().map(({ name }) => name)).toEqual(["expires_at_ms"]);
});

const policy = '{"version":1,"project_id":"demo","repos":["demo"],"areas":[],"roles":{},"members":{}}';

test.each([
  policy.replace('"version":1', '"version":1,"version":2'),
  policy.replace('"version":1', '"version":1,"Version":2'),
  policy.replace('"areas":[]', '"areas":[{"name":"app","patterns":[],"System":true}]'),
  policy.replace('"roles":{}', '"roles":{"dev":{"read":[],"write":[],"exec":[],"Write":[]}}'),
  policy.replace('"members":{}', '"members":{"ana":"dev","ana":"owner"}'),
])("dashboard policy validation rejects duplicate or unknown fields", (draft) => {
  expect(() => parsePolicyBundle(draft, { strict: true })).toThrow(SyntaxError);
  expect(parsePolicyBundle(draft).project).toBe("demo");
});

test("Room and audit records are rendered as literal text", async () => {
  // The dashboard uses native JavaScript modules with no browser bundler.
  // @ts-expect-error Plain browser module.
  const { renderRecords } = await import("../assets/admin/records.js");
  const malicious = "<img src=x onerror=alert(1)></pre><script>alert(1)</script>";
  const nodes: { tag: string; textContent: string; children: typeof nodes }[] = [];
  const createElement = (tag: string) => ({
    tag, textContent: "", children: [] as typeof nodes,
    append(child: (typeof nodes)[number]) { this.children.push(child); },
    set innerHTML(_html: string) { throw new Error("record used HTML"); },
  });
  const container = {
    ownerDocument: { createElement },
    replaceChildren(...items: typeof nodes) { nodes.splice(0, nodes.length, ...items); },
    set innerHTML(_html: string) { throw new Error("record used HTML"); },
  };
  renderRecords(container, [{ body: malicious }], () => malicious);
  expect(nodes[0]?.children[0]?.textContent).toBe(malicious);
  expect(nodes[0]?.children[1]?.textContent).toContain(malicious);
});

test("fingerprints the same validated policy key set as the Go invite CLI", async () => {
  const { createHash } = await import("node:crypto");
  const { policyKeyFingerprint } = await import("../src/dashboard-keys");
  const x = "g5-VyBBK1Cw0f6sC137bCeGzJ2NB4v5IQH3tSgixJbs";
  const y = "AQMp4KPB4eNUeEaJ-ohzy8_kDWFvSUEPmhTNIaraFFA";
  const key = (kid: string) => ({ crv: "P-256", kid, kty: "EC", x, y });
  const canonical = JSON.stringify({ keys: [key("a"), key("z")] });
  const fingerprint = createHash("sha256").update(canonical).digest("hex");
  const reversed = JSON.stringify({ keys: [key("z"), { ...key("a"), use: "sig", key_ops: ["verify"] }] }, null, 2);
  await expect(policyKeyFingerprint(canonical)).resolves.toBe(fingerprint);
  await expect(policyKeyFingerprint(reversed)).resolves.toBe(fingerprint);
  for (const invalid of [
    { keys: [] }, { keys: [key("a"), key("a")] }, { keys: [{ ...key("a"), d: "invalid" }] },
    { keys: [{ ...key("a"), y: "A".repeat(43) }] }, { keys: [{ ...key("a"), key_ops: ["verify", "sign"] }] },
  ]) {
    await expect(policyKeyFingerprint(JSON.stringify(invalid))).rejects.toThrow();
  }
  await expect(policyKeyFingerprint(canonical.replace('"kid":"a"', '"kid":"b","\\u006bid":"a"'))).rejects.toThrow();
  await expect(policyKeyFingerprint(canonical + " ".repeat(64 << 10))).rejects.toThrow();
});

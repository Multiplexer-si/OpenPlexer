import { DatabaseSync } from "node:sqlite";
import { expect, test, vi } from "vitest";
import { dashboardResponse } from "../src/dashboard-http";
import { parsePolicyBundle } from "../src/policy";
import { parsePublication } from "../src/publication";
import { schemaMigrations } from "../src/storage";

test("the dashboard sign-in form sends the page origin under no-referrer", async () => {
  const origin = "https://hub.example.com";
  const page = new Request(origin + "/admin/");
  const policy = dashboardResponse(page, new Response()).headers.get("Referrer-Policy");
  const button = { disabled: false };
  const tokenInput = { value: "" };
  const signInPanel = { hidden: true };
  let submit: (() => void) | undefined;
  let signInOrigin: string | undefined;
  const inert = {
    querySelectorAll: () => [], addEventListener() {}, setAttribute() {},
    toggleAttribute() {}, removeAttribute() {}, replaceChildren() {},
  };
  vi.stubGlobal("Option", class {});
  vi.stubGlobal("document", {
    getElementById(id: string) {
      if (id === "lead-token") return tokenInput;
      if (id === "sign-in-panel") return signInPanel;
      if (id === "sign-in-form") return {
        addEventListener(_type: string, listener: (event: { preventDefault(): void; submitter: typeof button }) => void) {
          submit = () => listener({ preventDefault() {}, submitter: button });
        },
      };
      return inert;
    },
  });
  vi.stubGlobal("fetch", async (path: string, options: { method: string; mode?: string }) => {
    if (path === "/admin/api/sign-in" && options.method === "POST") {
      // Fetch's Origin header algorithm for a same-origin POST with no redirects.
      signInOrigin = options.mode !== "cors" && policy === "no-referrer" ? "null" : origin;
    }
    return new Response(null, { status: 401 });
  });
  try {
    // @ts-expect-error Plain browser module.
    await import("../assets/admin/dashboard.js");
    await vi.waitFor(() => expect(signInPanel.hidden).toBe(false));
    if (!submit) throw new Error("missing dashboard sign-in handler");
    tokenInput.value = "lead credential";
    submit();
    await vi.waitFor(() => expect(button.disabled).toBe(false));
    expect(signInOrigin).toBe(origin);
  } finally {
    vi.unstubAllGlobals();
  }
});

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
  ["bundle", policy.replace('"version":1', '"version":1,"Version":2')],
  ["area", policy.replace('"areas":[]', '"areas":[{"name":"app","patterns":[],"System":true}]')],
  ["role", policy.replace('"roles":{}', '"roles":{"dev":{"read":[],"write":[],"exec":[],"Write":[]}}')],
])("dashboard publication accepts and preserves %s extensions", (_location, draft) => {
  const body = new TextEncoder().encode(draft);
  expect(parsePublication(body, true)).toEqual(parsePublication(body));
});

test.each([
  policy.replace('"version":1', '"version":1,"version":2'),
  policy.replace('"members":{}', '"members":{"ana":"dev","ana":"owner"}'),
  policy.replace('"version":1', '"version":1,"note":{"tag":1,"tag":2}'),
])("dashboard policy validation rejects duplicate fields, including inside extensions", (draft) => {
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

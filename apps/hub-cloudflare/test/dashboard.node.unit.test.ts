import { readFileSync } from "node:fs";
import { DatabaseSync } from "node:sqlite";
import { URL as FileURL } from "node:url";
import { expect, test, vi } from "vitest";
import { dashboardResponse } from "../src/dashboard-http";
import { cappedBody, HubError } from "../src/http";
import { maxBundleBytes } from "../src/jws";
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

test("the dashboard route guide explains when the session CSRF value is issued and required", () => {
  const guide = readFileSync(new FileURL("../../../docs/hub.md", import.meta.url), "utf8");
  const dashboard = guide.split("\n## Worker dashboard\n")[1]?.split("\n## ")[0] ?? "";
  expect(/^.*sign-in.*creates.*session.*returns.*`csrf`/im.test(dashboard)).toBe(true);
  expect(/Every session-authenticated write, including sign-out, requires.*`X-CSRF-Token`/.test(dashboard)).toBe(true);
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

test("the dashboard previews and publishes a draft whose indentation exceeds the policy body limit", async () => {
  const bundle = {
    version: 1, project_id: "demo", repos: ["demo"], areas: [], roles: {}, members: {},
    note: Array.from({ length: 24_000 }, () => ({ label: 'keep  "quotes" and \\ paths' })),
  };
  const compact = JSON.stringify({ ...bundle, version: 2 });
  expect(Buffer.byteLength(compact)).toBeLessThan(maxBundleBytes);

  class DashboardElement extends EventTarget {
    value = "";
    textContent = "";
    disabled = false;
    hidden = false;
    dataset = { view: "" };
    classList = { toggle() {} };
    querySelectorAll(): DashboardElement[] { return []; }
    insertCell() { return new DashboardElement(); }
    append() {}
    replaceChildren() {}
    setAttribute() {}
    toggleAttribute() {}
  }
  const elements = new Map<string, DashboardElement>();
  function element(id: string) {
    let node = elements.get(id);
    if (!node) {
      node = new DashboardElement();
      elements.set(id, node);
    }
    return node;
  }
  const tab = new DashboardElement();
  tab.dataset.view = "policy";
  element("sections").querySelectorAll = () => [tab];
  element("active-project").value = "demo";
  const path = "/admin/api/projects/demo/policy";
  const writes: { method: string; compact: boolean }[] = [];
  vi.resetModules();
  vi.stubGlobal("Option", class {});
  vi.stubGlobal("document", { getElementById: element, createElement: () => new DashboardElement() });
  vi.stubGlobal("fetch", async (url: string, options: { method: string; body?: string }) => {
    const { method, body } = options;
    if (url === "/admin/api/session") return Response.json({ member_id: "lee" });
    if (url.startsWith("/admin/api/projects?")) return Response.json({ projects: [{ id: "demo", repo: "demo" }] });
    if (url === path && method === "GET") return Response.json({ version: 1, bundle });
    if (url.startsWith(path + "/history?")) return Response.json({ versions: [{ version: 1 }] });
    if ((url === path + "/preview" && method === "POST") || (url === path && method === "PUT")) {
      writes.push({ method, compact: body === compact });
      try {
        const request = new Request("https://hub.example.com" + url, { method, body });
        const parsed = parsePublication(await cappedBody(request, maxBundleBytes), true);
        return Response.json(method === "POST" ? { roles: [], areas: [] } : parsed);
      } catch (error) {
        if (error instanceof HubError) return error.response();
        throw error;
      }
    }
    throw new Error("unexpected dashboard request");
  });
  try {
    // @ts-expect-error Plain browser module.
    await import("../assets/admin/dashboard.js");
    await vi.waitFor(() => expect(element("projects-next").hidden).toBe(true));
    tab.dispatchEvent(new Event("click"));
    await vi.waitFor(() => expect(element("policy-version").textContent).toBe("Published version 1 for demo."));
    element("next-version").dispatchEvent(new Event("click"));
    const editor = element("policy-json");
    const indented = editor.value;
    expect(Buffer.byteLength(indented)).toBeGreaterThan(maxBundleBytes);
    element("preview-policy").dispatchEvent(new Event("click"));
    await vi.waitFor(() => expect(element("preview-policy").disabled).toBe(false));
    expect(element("status").textContent).toBe("Policy draft is valid.");

    const publish = new DashboardElement();
    element("policy-form").dispatchEvent(Object.assign(new Event("submit"), { submitter: publish }));
    await vi.waitFor(() => expect(publish.disabled).toBe(false));
    expect(element("status").textContent).toBe("Published policy version 2 for demo.");
    expect(writes).toEqual([
      { method: "POST", compact: true }, { method: "POST", compact: true }, { method: "PUT", compact: true },
    ]);
    expect(editor.value === indented).toBe(true);

    editor.value = '{"version": 3, "project_id": "demo", "repos": ["demo"], "areas": [], "roles": {}, "members": {}, "note": {"tag": 1, "tag": 2}}';
    element("preview-policy").dispatchEvent(new Event("click"));
    await vi.waitFor(() => expect(element("preview-policy").disabled).toBe(false));
    expect(element("status").textContent).toBe("invalid input: policy: has a duplicate field");
  } finally {
    vi.unstubAllGlobals();
  }
});

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

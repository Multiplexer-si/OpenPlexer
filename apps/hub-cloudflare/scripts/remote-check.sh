#!/usr/bin/env bash
set -euo pipefail
set +x

worker_dir=$(cd "$(dirname "$0")/.." && pwd -P)
export WRANGLER_SEND_METRICS=false WRANGLER_WRITE_LOGS=false

exec node --input-type=module - "$worker_dir" "$@" <<'JS'
import { spawn, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { request as httpsRequest } from "node:https";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createInterface } from "node:readline";
import { setTimeout as sleep } from "node:timers/promises";
import { isDeepStrictEqual, parseArgs } from "node:util";

const [, , worker, ...args] = process.argv;
const root = resolve(worker, "../..");
const configPath = join(worker, "wrangler.toml");
const wrangler = join(worker, "node_modules/.bin/wrangler");
const usage = "Usage: remote-check.sh --hub-url HTTPS_ORIGIN --lead-token-file FILE " +
  "[--bootstrap-token-file FILE] [--budget]";
const flags = {
  "hub-url": { type: "string" },
  "lead-token-file": { type: "string" },
  "bootstrap-token-file": { type: "string" },
  budget: { type: "boolean" },
  help: { type: "boolean", short: "h" },
};
let options;
try {
  ({ values: options } = parseArgs({ args, options: flags }));
} catch {
  console.error(usage);
  process.exit(2);
}
if (options.help) {
  console.log(usage);
  process.exit(0);
}

const leadFile = options["lead-token-file"];
const bootstrapFile = options["bootstrap-token-file"];
const bootstrapID = "remote-check-lead";
const policyKeys = "/.well-known/jwks.json";
const tailDeliveryTimeout = 60_000;
const runID = randomUUID();
const project = "conformance-" + runID;
const second = "conformance-other-" + runID;
const edgeRoom = "edge-" + runID;
const member = "member-" + runID;
const children = new Set();
const redactions = new Set([
  process.env.CLOUDFLARE_API_TOKEN, process.env.CLOUDFLARE_API_KEY, process.env.CLOUDFLARE_ACCOUNT_ID,
]);
let origin;
let scratch;
let memberToken;
let cliEnv;
let failed = false;

class CheckFailure extends Error {
  name = "CheckFailure";
}
function requireCheck(condition, message) {
  if (!condition) throw new CheckFailure(message);
}

function redact(message) {
  for (const value of redactions) {
    if (value) message = message.replaceAll(value, "[redacted]");
  }
  return message;
}

async function check(name, action) {
  const start = performance.now();
  try {
    const detail = await action();
    console.log(redact(`PASS ${name}${detail ? " " + detail : ""}`));
    return true;
  } catch (error) {
    const message = redact(`${error?.name ?? "Error"}: ${error?.message ?? error}`);
    console.log(`FAIL ${name} (${message}, ${elapsed(start)} ms)`);
    failed = true;
    return false;
  }
}

function elapsed(start) {
  return Math.round(performance.now() - start);
}

function run(tool, argv, { cwd = root, env = process.env, status = 0, label = "command" } = {}) {
  const result = spawnSync(tool, argv, {
    cwd, env, stdio: ["ignore", "pipe", "pipe"], encoding: "utf8",
    maxBuffer: 16 << 20, timeout: 300_000,
  });
  if (result.error) throw result.error;
  if (status === 0 && label === "go test" && result.status === 1) {
    const tests = result.stdout.split("\n").map((line) => {
      try { return JSON.parse(line); } catch { return {}; }
    }).filter(({ Action, Test }) => Action === "fail" && /^Test\w+(?:\/[\w-]+)+$/.test(Test ?? ""));
    const names = tests.slice(0, 3).map(({ Test }) => Test).join(", ");
    throw new CheckFailure(names || "go test exited 1");
  }
  requireCheck(result.status === status, `${label} exited ${result.status ?? "without a status"}`);
  return result.stdout;
}

function startChild(tool, argv, { cwd = root, env = process.env, stdio } = {}) {
  const child = spawn(tool, argv, { cwd, env, stdio: stdio ?? ["ignore", "pipe", "ignore"] });
  const state = { child, ended: false };
  children.add(child);
  child.once("error", () => { state.ended = true; });
  child.once("close", () => {
    state.ended = true;
    children.delete(child);
  });
  return state;
}

function readTokenFile(path) {
  const info = statSync(path);
  requireCheck(info.isFile() && (info.mode & 0o077) === 0, "credential files must be owner-only regular files");
  const token = readFileSync(path, "utf8").trim();
  redactions.add(token);
  return token;
}

function bin(name) { return join(scratch, "bin", "openplexer" + name); }
function file(name) { return join(scratch, name); }
function readJSON(path) { return JSON.parse(readFileSync(path, "utf8")); }
function admin(command, ...params) {
  return run(bin("-hub"), [command, "--hub", origin, "--token-file", resolve(leadFile), ...params],
    { label: "openplexer-hub " + command });
}

function init() {
  run(bin("-hub"), ["init", "--hub", origin, "--token-file", resolve(bootstrapFile),
    "--id", bootstrapID, "--lead-token-file", resolve(leadFile)], { label: "openplexer-hub init" });
}

function publish(id, version) {
  writeFileSync(file("policy.json"), JSON.stringify({
    version, project_id: id, repos: ["R&D"], areas: [], members: {}, roles: {},
  }));
  admin("publish-policy", "--project", id, "--file", file("policy.json"));
}

function ctl(...argv) {
  return run(wrangler, [...argv, "--config", configPath], { cwd: worker, label: "wrangler " + argv[0] });
}

function validateWranglerConfig(hostname) {
  // The configuration reader can write diagnostics directly to stdout and stderr.
  run(process.execPath, ["--input-type=commonjs", "-e", `
    const { unstable_readConfig } = require("wrangler");
    const [configPath, hostname] = process.argv.slice(1);
    const config = unstable_readConfig({ config: configPath });
    if (!(config.name && config.vars?.HUB_HOSTNAME === hostname && config.vars.DEV_MODE === "")) {
      process.exitCode = 1;
    }
  `, configPath, hostname], {
    cwd: worker, label: "Wrangler configuration check (Worker name, matching HUB_HOSTNAME and empty DEV_MODE)",
  });
}

async function http(path, { token, body, signal = AbortSignal.timeout(10_000) } = {}) {
  const headers = { "Accept-Encoding": "identity" };
  if (token) headers.Authorization = "Bearer " + token;
  if (body) headers["Content-Type"] = "application/json";
  const response = await fetch(origin + path, {
    method: body ? "POST" : "GET", headers, body, signal, redirect: "manual",
  });
  requireCheck(response.status === 200, `hub returned HTTP ${response.status}`);
  return response;
}

function post(room, summary) {
  return http(`/api/v1/rooms/${room}/events`, {
    token: memberToken, body: JSON.stringify({
      type: "status", agent: { id: "remote-check", vendor: "codex" }, body: { summary },
    }),
  }).then((res) => res.json());
}

const streamPath = (room, seq) => `/api/v1/rooms/${room}/stream?last_seq=${seq}`;
async function* stream(path, signal) {
  const response = await http(path, { token: memberToken, signal });
  requireCheck(response.headers.get("Content-Type") === "application/x-ndjson", "expected NDJSON");
  const decoder = new TextDecoder();
  let pending = "";
  for await (const part of response.body) {
    pending += decoder.decode(part, { stream: true });
    let newline;
    while ((newline = pending.indexOf("\n")) >= 0) {
      const line = pending.slice(0, newline);
      pending = pending.slice(newline + 1);
      yield line;
    }
  }
  requireCheck(pending === "", "stream closed mid-frame");
}

async function until(condition, ms, message) {
  const end = performance.now() + ms;
  do {
    const result = condition();
    if (result) return result;
    await sleep(50);
  } while (performance.now() < end);
  throw new CheckFailure(message);
}

function startTail() {
  const tail = startChild(wrangler, ["tail", "--method", "GET", "--format", "json", "--config", configPath], {
    cwd: worker, env: { ...process.env, WRANGLER_LOG: "debug" }, stdio: ["ignore", "pipe", "pipe"],
  });
  tail.child.stderr.resume();
  tail.events = new Map();
  let json = "";
  createInterface({ input: tail.child.stdout }).on("line", (line) => {
    // Wrangler only announces the connection in pretty mode. Wait for its keep-alive pong in JSON mode.
    if (line === "Tail: Received pong from tail websocket") tail.ready = true;
    // Wrangler prints one indented JSON object per event.
    if (line.startsWith("{")) json = line;
    else if (json) json += "\n" + line;
    if (!json || !line.endsWith("}") || line.startsWith(" ")) return;
    let trace;
    try { trace = JSON.parse(json); } catch { return; } finally { json = ""; }
    const request = trace.event?.request;
    const url = request?.url;
    if (request?.method === "GET" && typeof url === "string" && url.startsWith(origin + "/api/v1/rooms/")) {
      if (!tail.events.has(url)) tail.events.set(url, []);
      tail.events.get(url).push({ outcome: trace.outcome, wallTime: trace.wallTime, at: performance.now() });
    }
  });
  return tail;
}

function endSeen(tail, path, timeout) {
  return until(() => {
    requireCheck(!tail.ended, "Wrangler tail exited");
    return tail.events.get(origin + path)?.[0];
  }, timeout, "no matching completion in Wrangler tail");
}

async function flush(tail) {
  let event, path, startedAt, disconnectAt;
  await check("edge frame and heartbeat flush", async () => {
    event = await post(edgeRoom, "flush");
    path = streamPath(edgeRoom, event.seq - 1) + "&probe=disconnect";
    startedAt = performance.now();
    const lines = stream(path, AbortSignal.timeout(10_000));
    let receivedHeartbeats = false;
    try {
      const first = await lines.next();
      const firstMs = elapsed(startedAt);
      requireCheck(!first.done && JSON.parse(first.value).event_id === event.event_id, "missing first event");
      requireCheck(firstMs <= 5_000, `first frame took ${firstMs} ms`);
      const gaps = [];
      let previous = performance.now();
      for (let i = 0; i < 2; i++) {
        const line = await lines.next();
        requireCheck(!line.done && line.value === "", "missing blank heartbeat");
        gaps.push(elapsed(previous));
        previous = performance.now();
      }
      receivedHeartbeats = true;
      requireCheck(gaps[0] <= 3_000 && gaps[1] >= 300 && gaps[1] <= 3_000,
        `heartbeat gaps were ${gaps.join("/")} ms`);
      return `first=${firstMs} ms heartbeat gaps=${gaps.join("/")} ms`;
    } finally {
      const closingAt = performance.now();
      await lines.return();
      if (receivedHeartbeats) disconnectAt = closingAt;
    }
  });
  await check("edge disconnect", async () => {
    requireCheck(disconnectAt !== undefined, "flush probe did not reach the disconnect step");
    requireCheck(tail?.ready, "Wrangler tail unavailable");
    const finished = await endSeen(tail, path, tailDeliveryTimeout);
    requireCheck(finished.outcome !== "ok", "server ended the stream normally");
    requireCheck(finished.outcome === "canceled" && finished.at >= disconnectAt,
      "expected a canceled invocation after the client disconnected");
    const clientDuration = Math.round(disconnectAt - startedAt);
    requireCheck(Number.isFinite(finished.wallTime) && finished.wallTime >= 0 &&
      Math.abs(finished.wallTime - clientDuration) <= 1_000,
      `invocation wall time ${finished.wallTime} ms was not within 1,000 ms of disconnect at ${clientDuration} ms`);
    return `wall time=${finished.wallTime} ms client disconnect=${clientDuration} ms`;
  });
  return event;
}

async function callBudget() {
  const marker = await post(second, "call budget start");
  const path = streamPath(second, marker.seq) + "&probe=calls";
  const start = performance.now();
  let frames = 0;
  let seq = marker.seq;
  let pending;
  // A blank frame is an empty read. Keep at most one posted event ahead of the cursor.
  for await (const line of stream(path, AbortSignal.timeout(290_000))) {
    frames++;
    if (line) {
      const event = JSON.parse(line);
      requireCheck(event.seq === ++seq && event.payload?.body?.summary === "call budget event",
        "unexpected event on the call-budget stream");
      requireCheck(pending && (await pending).seq === seq, "stream did not deliver the posted event");
      pending = undefined;
    } else if (!pending && frames < 400) {
      pending = post(second, "call budget event");
      // The POST may finish or fail while the stream is waiting for its next frame.
      pending.catch(() => {});
    }
  }
  await pending;
  requireCheck(frames === 400 && elapsed(start) < 290_000, `expected 400 reads before the deadline, observed ${frames}`);
  return `reads=${frames} EOF=${elapsed(start)} ms`;
}

function configure(keys) {
  cliEnv = { ...process.env, OPENPLEXER_HOME: file("agent-home") };
  run(bin(""), ["configure", "--hub", origin, "--project", edgeRoom, "--member", member,
    "--keys", keys, "--token-file", file("member.token")], { env: cliEnv, label: "openplexer configure" });
}

function sync(version) {
  const output = run(bin(""), ["policy", "sync"], { env: cliEnv, label: "openplexer policy sync" });
  requireCheck(output.includes("policy: verified\n") && output.includes(`version ${version} of project ${edgeRoom},`),
    "expected the signed policy version to verify under the original pin");
}

async function wallBudget(tail, lastEvent) {
  const daemon = startChild(bin(""), ["daemon"], { env: cliEnv, stdio: ["ignore", "pipe", "pipe"] });
  const daemonStart = performance.now();
  const delivered = [];
  let daemonError = false;
  daemon.child.stderr.on("data", () => { daemonError = true; });
  createInterface({ input: daemon.child.stdout }).on("line", (line) => {
    try { delivered.push(JSON.parse(line).id); } catch { daemonError = true; }
  });
  try {
    const path = streamPath(edgeRoom, lastEvent.seq) + "&probe=wall";
    const start = performance.now();
    let heartbeats = 0;
    let previous = start;
    for await (const line of stream(path, AbortSignal.timeout(315_000))) {
      requireCheck(line === "", "unexpected event on the idle stream");
      requireCheck(elapsed(previous) < 10_000, "heartbeat gap reached the daemon idle timeout");
      heartbeats++;
      previous = performance.now();
    }
    const duration = elapsed(start);
    requireCheck(duration >= 295_000 && duration <= 315_000 && heartbeats > 250,
      `expected clean five-minute EOF, got ${duration} ms and ${heartbeats} heartbeats`);

    const finished = await endSeen(tail, streamPath(edgeRoom, 0), 12_000);
    const daemonDuration = Math.round(finished.at - daemonStart);
    requireCheck(finished.outcome === "ok" && daemonDuration >= 295_000 && daemonDuration <= 320_000,
      "the daemon's first stream did not finish cleanly at the budget");
    const next = await post(edgeRoom, "after budget");
    await until(() => delivered.includes(next.event_id) || daemonError || daemon.ended, 10_000,
      "daemon did not deliver the next event");
    requireCheck(!daemonError && !daemon.ended &&
      isDeepStrictEqual(delivered, [lastEvent.event_id, next.event_id]),
      "expected the daemon to continue from its saved cursor without an error or duplicate");
    await until(() => readJSON(file("agent-home/room-cursor.json")).last_seq === next.seq, 2_000,
      "daemon did not save the resumed cursor");
    return `EOF=${duration} ms heartbeats=${heartbeats} daemon tail=${daemonDuration} ms`;
  } finally {
    daemon.child.kill();
  }
}

function secretNames() {
  return JSON.parse(ctl("secret", "list", "--format", "json")).map(({ name }) => name).sort();
}

function verify(dir, { name = "export.json", jwks = join(dir, "jwks.json"), status = 0 } = {}) {
  return JSON.parse(run(bin("-verify"), ["--export", join(dir, name), "--jwks", jwks,
    "--checkpoint", join(dir, "checkpoint.jws"), "--json"], { status, label: "openplexer-verify" }));
}

function exportAudit(name) {
  const dir = file(name);
  admin("export", "--out", dir);
  return { dir, audit: readJSON(join(dir, "export.json")) };
}

function verifyComplete(snapshot, jwks) {
  const result = verify(snapshot.dir, { jwks });
  requireCheck(result.ok && result.completeness === "through_checkpoint" &&
    result.entries === snapshot.audit.entries.length && result.checkpoint_entries === result.entries,
    "checkpoint did not verify the full export");
  return result.entries;
}

async function main() {
  if (!await check("deployment configuration and credentials", () => {
    const address = options["hub-url"] ?? "";
    requireCheck(/^https:\/\/[a-z0-9.-]+\/?$/i.test(address) && leadFile,
      "provide an HTTPS origin without a port or path and a lead token file");
    const url = new URL(address);
    origin = url.origin;
    redactions.add(url.hostname);
    redactions.add(address.slice("https://".length).replace(/\/$/, ""));
    validateWranglerConfig(url.hostname);
    if (bootstrapFile) readTokenFile(bootstrapFile);
    if (!bootstrapFile || exists(leadFile)) readTokenFile(leadFile);
  })) return;

  if (!await check("build OpenPlexer binaries", () => {
    scratch = mkdtempSync(join(tmpdir(), "openplexer-remote-"));
    mkdirSync(file("bin"));
    run("go", ["build", "-o", file("bin") + "/", "./apps/openplexer", "./apps/openplexer-hub",
      "./apps/openplexer-verify"], { label: "go build" });
  })) return;

  if (!await check("HTTPS health", async () => {
    const result = await (await http("/healthz")).json();
    requireCheck(result.ok && Number.isInteger(result.schema) && result.schema > 0, "expected a SQLite schema version");
    return `schema=${result.schema}`;
  })) return;

  if (!await check("seed disposable team", () => {
    if (bootstrapFile) {
      init();
      readTokenFile(leadFile);
    }
    writeFileSync(file("member.token"), admin("add-member", "--id", member));
    memberToken = readTokenFile(file("member.token"));
    writeFileSync(file("revoked.token"), admin("add-member", "--id", "revoked-" + runID));
    readTokenFile(file("revoked.token"));
    admin("remove-member", "--id", "revoked-" + runID);
    for (const id of [project, second, edgeRoom]) admin("add-project", "--id", id, "--repo", "R&D");
    publish(project, 1);
    publish(edgeRoom, 1);
  })) return;

  await check("external hub conformance", () => {
    run("go", ["test", "-json", "-count=1", "./test/hubconformance", "-args",
      "-hub-url", origin, "-cloudflare-edge", "-lead-token-file", resolve(leadFile), "-member-token-file", file("member.token"),
      "-revoked-token-file", file("revoked.token"), "-project", project, "-second-project", second],
      { label: "go test" });
  });

  let before;
  await check("audit export and verification", () => {
    const snapshot = exportAudit("before");
    const count = verifyComplete(snapshot);
    const actions = new Set(snapshot.audit.entries.map(({ action }) => action));
    for (const action of ["event.policy-denied", "claim.created", "claim.renewed", "claim.released", "claim.expired"]) {
      requireCheck(actions.has(action), "missing audit action " + action);
    }
    before = snapshot;
    return `records=${count}`;
  });
  await check("audit tamper rule A3", () => {
    requireCheck(before, "verified export unavailable");
    const changed = structuredClone(before.audit);
    changed.entries[0].target += "-tampered";
    writeFileSync(join(before.dir, "tampered.json"), JSON.stringify(changed));
    requireCheck(verify(before.dir, { name: "tampered.json", status: 1 }).rule === "A3",
      "verifier did not report rule A3");
  });

  await check("foreign Host", async () => {
    const response = await new Promise((resolveResponse, reject) => {
      const req = httpsRequest(origin + "/healthz", {
        headers: { Host: "foreign.invalid" }, servername: new URL(origin).hostname,
        signal: AbortSignal.timeout(10_000),
      }, resolveResponse);
      req.on("error", reject);
      req.end();
    });
    let text = "";
    for await (const chunk of response.setEncoding("utf8")) text += chunk;
    let body;
    try { body = JSON.parse(text); } catch { /* The edge can return an HTML error page. */ }
    const status = response.statusCode;
    requireCheck(status >= 400 && status < 500 && body?.ok !== true,
      `expected a 4xx rejection without hub health success, got HTTP ${status}`);
    const layer = status === 404 && body?.error === "not found" ? "Worker" : "edge";
    return `HTTP ${status} (${layer})`;
  });

  const redeployed = await check("redeploy preserves state and secret bindings", async () => {
    requireCheck(before, "verified export unavailable");
    const names = secretNames();
    requireCheck(names.includes("WRAP_KEY_V1") && (!bootstrapFile || names.includes("BOOTSTRAP_TOKEN")),
      "expected Worker secret bindings");
    const keys = file("policy.jwks.json");
    writeFileSync(keys, await (await http(policyKeys)).text());
    configure(keys);
    sync(1);
    const replay = () => http(`/api/v1/rooms/${second}/events`, { token: memberToken })
      .then((response) => response.json());
    const history = await replay();
    requireCheck(Array.isArray(history) && history.length > 0, "missing Room history before redeploy");
    const start = performance.now();
    run("npm", ["run", "deploy", "--", "--config", configPath], { cwd: worker, label: "npm run deploy" });
    const after = exportAudit("after");
    const count = verifyComplete(after, join(before.dir, "jwks.json"));
    requireCheck(isDeepStrictEqual(after.audit.entries, before.audit.entries), "audit records changed across redeploy");
    requireCheck(isDeepStrictEqual(await replay(), history), "Room history changed across redeploy");
    requireCheck(isDeepStrictEqual(secretNames(), names), "secret names changed across redeploy");
    if (bootstrapFile) init();
    publish(edgeRoom, 2);
    sync(2);
    return `records=${count} redeploy and verification=${elapsed(start)} ms`;
  });

  let tail;
  await check("Wrangler tail", async () => {
    tail = startTail();
    await until(() => {
      requireCheck(!tail.ended, "Wrangler tail exited");
      return tail.ready;
    }, 30_000, "Wrangler tail did not connect");
  });

  const event = await flush(tail);
  if (options.budget) {
    await Promise.all([
      check("400-read stream budget", callBudget),
      check("five-minute stream budget and daemon resume", () => {
        requireCheck(redeployed && tail?.ready && event, "daemon or tail prerequisites unavailable");
        return wallBudget(tail, event);
      }),
    ]);
  }
}

function exists(path) {
  return statSync(path, { throwIfNoEntry: false }) !== undefined;
}

process.umask(0o077);
process.on("exit", () => {
  for (const child of children) child.kill();
  if (scratch) rmSync(scratch, { force: true, recursive: true });
});
process.on("SIGINT", () => process.exit(130));
process.on("SIGTERM", () => process.exit(143));

await main();
process.exit(failed ? 1 : 0);
JS

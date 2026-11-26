import { evictDurableObject, listDurableObjectIds, reset, runInDurableObject } from "cloudflare:test";
import { env, exports } from "cloudflare:workers";
import { beforeEach, expect, test, vi } from "vitest";
import worker, { type Env as HubEnv } from "../src/index";
import { HubStorage, migrate, schemaMigrations } from "../src/storage";
import type { TransactionSQL } from "../src/storage";

declare global {
  namespace Cloudflare {
    interface Env extends HubEnv {}
    interface GlobalProps {
      mainModule: typeof import("../src/index");
    }
  }
}

beforeEach(reset);

function teamId(): DurableObjectId {
  return env.TEAM_HUB.idFromName(`team:${env.TEAM_ID}`);
}

function request(path: string, init?: RequestInit): Promise<Response> {
  return exports.default.fetch(new Request(`https://${env.HUB_HOSTNAME}${path}`, init));
}

test("serves uncached health with the initialized schema version", async () => {
  const response = await request("/healthz");
  expect(response.status).toBe(200);
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  expect(await response.json()).toEqual({ ok: true, schema: 1 });
});

test("returns uncached JSON for unknown paths", async () => {
  const response = await request("/api/v1/unknown");
  expect(response.status).toBe(404);
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  expect(await response.json()).toEqual({ error: "not found" });
});

test.each(["/./healthz", "/unused/../healthz", "/%2E/healthz", "/unused/%2e%2E/healthz", "/unused/.%2e/healthz"])(
  "rejects dot segments before forwarding %s",
  async (path) => {
    const incoming = new Request(`https://${env.HUB_HOSTNAME}${path}`);
    const response = await worker.fetch(incoming, env);
    expect(response.status).toBe(404);
    expect(response.headers.get("Cache-Control")).toBe("no-store");
    expect(response.headers.get("Content-Type")).toBe("application/json");
    expect(await response.json()).toEqual({ error: "not found" });
  },
);

test("rejects other hostnames before routing to storage", async () => {
  const before = (await listDurableObjectIds(env.TEAM_HUB)).length;
  for (const host of [`${env.HUB_HOSTNAME}.example`, "localhost", "127.0.0.1"]) {
    const response = await worker.fetch(new Request(`http://${host}/healthz`), { ...env, DEV_MODE: "" });
    expect(response.status).toBe(404);
    expect(await response.json()).toEqual({ error: "not found" });
  }
  // Storage is shared across tests in this file, so check that no new object appeared.
  expect((await listDurableObjectIds(env.TEAM_HUB)).length).toBe(before);
});

test("allows only localhost and IPv4 loopback in development mode", async () => {
  const localEnv = { ...env, DEV_MODE: "1" };
  for (const host of ["localhost", "127.0.0.1"]) {
    const response = await worker.fetch(new Request(`http://${host}:8787/healthz`), localEnv);
    expect(response.status).toBe(200);
    await response.json();
  }
  for (const host of ["[::1]", "localhost.example", "127.0.0.2"]) {
    const response = await worker.fetch(new Request(`http://${host}/healthz`), localEnv);
    expect(response.status).toBe(404);
    await response.json();
  }
});

test("routes all accepted paths to the same named team object", async () => {
  const idFromName = vi.fn((name: string) => env.TEAM_HUB.idFromName(name));
  const forward = vi.fn((id: DurableObjectId, req: Request) => env.TEAM_HUB.get(id).fetch(req));
  const get = vi.fn((id: DurableObjectId) => ({
    fetch: (req: Request) => forward(id, req),
    readRoomPage: env.TEAM_HUB.get(id).readRoomPage,
  }));
  const routingEnv = { ...env, TEAM_HUB: { idFromName, get } };
  const paths = ["/.well-known/jwks.json", "/.well-known/audit-jwks.json", "/healthz"];
  const id = teamId();
  for (const [i, path] of paths.entries()) {
    const incoming = new Request(`https://${env.HUB_HOSTNAME}${path}`);
    const response = await worker.fetch(incoming, routingEnv);
    await response.json();
    expect(idFromName).toHaveBeenCalledTimes(i + 1);
    expect(idFromName).toHaveBeenLastCalledWith(`team:${env.TEAM_ID}`);
    expect(get).toHaveBeenCalledTimes(i + 1);
    expect(get.mock.calls[i][0].equals(id)).toBe(true);
    expect(forward).toHaveBeenCalledTimes(i + 1);
    expect(forward.mock.calls[i][1]).toBe(incoming);
  }
});

test("applying the same migration twice preserves the schema and rows", async () => {
  await runInDurableObject(env.TEAM_HUB.get(teamId()), (_instance, ctx) => {
    const storage = new HubStorage(ctx.storage);
    const sql = ctx.storage.sql;
    storage.transaction((tx) => {
      tx.exec("INSERT INTO team (singleton, workspace_id, policy_kid, audit_kid) VALUES (1, 'workspace', 'policy', 'audit')");
    });
    const schema = () => sql.exec("SELECT name, sql FROM sqlite_schema WHERE sql IS NOT NULL ORDER BY name").toArray();
    const before = schema();
    expect(before.map((entry) => entry.name)).toEqual([
      "audit_records", "claims", "claims_expiry", "claims_room", "members", "policies", "projects",
      "projects_repo", "room_events", "room_events_idempotency", "schema_migrations", "signing_keys", "team",
    ]);
    expect(migrate(storage)).toBe(1);
    expect(migrate(storage)).toBe(1);
    expect(schema()).toEqual(before);
    expect(sql.exec("SELECT version FROM schema_migrations").toArray()).toEqual([{ version: 1 }]);
    expect(sql.exec("SELECT workspace_id, audit_seq, audit_tip FROM team").one()).toEqual({
      workspace_id: "workspace",
      audit_seq: 0,
      audit_tip: "0".repeat(64),
    });
  });
});

test("an existing object can apply a later numbered migration", async () => {
  await runInDurableObject(env.TEAM_HUB.get(teamId()), (_instance, ctx) => {
    const storage = new HubStorage(ctx.storage);
    const sql = ctx.storage.sql;
    storage.transaction((tx) => {
      tx.exec("INSERT INTO projects (project_id, repo) VALUES ('demo', 'team/demo')");
    });
    const migrations = [...schemaMigrations, { version: 2, sql: "ALTER TABLE projects ADD COLUMN label TEXT" }];
    expect(migrate(storage, migrations)).toBe(2);
    expect(migrate(storage, migrations)).toBe(2);
    expect(sql.exec("SELECT version FROM schema_migrations ORDER BY version").toArray()).toEqual([{ version: 1 }, { version: 2 }]);
    expect(sql.exec("SELECT project_id, room_seq, label FROM projects").one()).toEqual({
      project_id: "demo",
      room_seq: 0,
      label: null,
    });
  });
});

test("a thrown transaction rolls back the action and its audit record", async () => {
  await runInDurableObject(env.TEAM_HUB.get(teamId()), (_instance, ctx) => {
    const storage = new HubStorage(ctx.storage);
    const failure = new Error("stop before commit");
    expect(() => storage.transaction((sql) => {
      sql.exec("INSERT INTO projects (project_id, repo) VALUES ('demo', 'team/demo')");
      sql.exec(`INSERT INTO audit_records
        (seq, workspace_id, action, target, meta_json, flagged, created_at, prev_hash, hash)
        VALUES (1, 'workspace', 'project.added', 'demo', '{}', 0, '2026-11-25T00:00:00.000Z', 'before', 'after')`);
      throw failure;
    })).toThrow(failure);
    expect(ctx.storage.sql.exec("SELECT project_id FROM projects").toArray()).toEqual([]);
    expect(ctx.storage.sql.exec("SELECT seq FROM audit_records").toArray()).toEqual([]);
  });
});

const asynchronousWrites: [string, (sql: TransactionSQL) => void][] = [
  ["an async callback that throws after await", async (sql) => {
    sql.exec("INSERT INTO projects (project_id, repo) VALUES ('demo', 'team/demo')");
    await Promise.resolve();
    throw new Error("failed asynchronously");
  }],
  ["a callback returning a rejected Promise", (sql) => {
    sql.exec("INSERT INTO projects (project_id, repo) VALUES ('demo', 'team/demo')");
    return Promise.reject(new Error("failed asynchronously"));
  }],
  ["a callback returning a thenable", (sql) => {
    sql.exec("INSERT INTO projects (project_id, repo) VALUES ('demo', 'team/demo')");
    return { then() {} };
  }],
  ["a callback returning a callable thenable", (sql) => {
    sql.exec("INSERT INTO projects (project_id, repo) VALUES ('demo', 'team/demo')");
    return Object.assign(() => {}, { then() {} });
  }],
];

test.each(asynchronousWrites)("rolls back %s", async (_name, write) => {
  await runInDurableObject(env.TEAM_HUB.get(teamId()), (_instance, ctx) => {
    const storage = new HubStorage(ctx.storage);
    expect(() => storage.transaction(write)).toThrow("Hub transactions must be synchronous");
    expect(ctx.storage.sql.exec("SELECT project_id FROM projects").toArray()).toEqual([]);
  });
});

test.each(["promise", "return", "throw"])("blocks deferred SQL after callback exit by %s", async (exit) => {
  await runInDurableObject(env.TEAM_HUB.get(teamId()), async (_instance, ctx) => {
    const storage = new HubStorage(ctx.storage);
    const completed = Promise.withResolvers<unknown>();
    const failure = new Error("stop before commit");
    const write: (sql: TransactionSQL) => void = (sql) => {
      const exec = sql.exec.bind(sql);
      const writeLater = () => {
        try {
          exec("INSERT INTO projects (project_id, repo) VALUES ('deferred', 'team/demo')");
          completed.resolve(undefined);
        } catch (error) {
          completed.resolve(error);
        }
      };
      if (exit === "promise") {
        return (async () => {
          await Promise.resolve();
          writeLater();
        })();
      }
      queueMicrotask(writeLater);
      if (exit === "throw") throw failure;
    };

    if (exit === "promise") {
      expect(() => storage.transaction(write)).toThrow("Hub transactions must be synchronous");
    } else if (exit === "throw") {
      expect(() => storage.transaction(write)).toThrow(failure);
    } else {
      expect(storage.transaction(write)).toBeUndefined();
    }
    const error = await completed.promise;
    expect(ctx.storage.sql.exec("SELECT project_id FROM projects").toArray()).toEqual([]);
    expect(error).toBeInstanceOf(TypeError);
    expect(error).toHaveProperty("message", "Hub transaction has ended");
  });
});

test("a row survives eviction and is read in another object request", async () => {
  const id = teamId();
  const stub = env.TEAM_HUB.get(id);
  await runInDurableObject(stub, (_instance, ctx) => {
    new HubStorage(ctx.storage).transaction((sql) => {
      sql.exec("INSERT INTO projects (project_id, repo) VALUES ('demo', 'team/demo')");
    });
  });

  // This recreates the object in local workerd. Cloudflare storage across redeploys needs a live check.
  await evictDurableObject(stub);
  expect(await runInDurableObject(env.TEAM_HUB.get(id), (_instance, ctx) =>
    ctx.storage.sql.exec("SELECT project_id, repo, room_seq FROM projects").one(),
  )).toEqual({ project_id: "demo", repo: "team/demo", room_seq: 0 });
});

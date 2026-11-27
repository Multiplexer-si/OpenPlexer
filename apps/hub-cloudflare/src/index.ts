import { DurableObject } from "cloudflare:workers";
import { activateKey, addMember, addProject, initialize, removeMember, stageKey } from "./admin";
import { handleClaim } from "./claims";
import { exportAudit } from "./export";
import { handleInvite } from "./invites";
import { bearer, HubError, invalid, json, notFound, unauthorized } from "./http";
import { memberMaintenanceIntervalMs, observeMember, pruneMemberObservations, readMembers } from "./members";
import { publishPolicy, readPolicy, readProject } from "./publication";
import { eventPage, postEvent, replayCursor, replayEvents } from "./room";
import { authenticate, publicKeys } from "./state";
import { HubStorage, migrate } from "./storage";
import { roomStream, streamHeaders, streamLimits } from "./stream";
import type { RoomPage, RoomRead } from "./stream";

export interface Env {
  HUB_HOSTNAME: string;
  TEAM_ID: string;
  DEV_MODE?: string;
  TEAM_HUB: DurableObjectNamespace<TeamHub>;
  BOOTSTRAP_TOKEN?: string;
  WRAP_KEY_V1?: string;
}

type RoutingEnv = Pick<Env, "HUB_HOSTNAME" | "TEAM_ID" | "DEV_MODE"> & {
  TEAM_HUB: {
    idFromName(name: string): DurableObjectId;
    get(id: DurableObjectId): Pick<TeamHub, "fetch" | "readRoomPage">;
  };
};

const jsonHeaders = { "Cache-Control": "no-store" };

export default { fetch: (request: Request, env: RoutingEnv) => fetchHub(request, env) } satisfies ExportedHandler<Env>;

export async function fetchHub(
  request: Request, env: RoutingEnv, limits = streamLimits, now = Date.now,
): Promise<Response> {
  const deadline = now() + limits.durationMs;
  const { hostname } = new URL(request.url);
  const local = env.DEV_MODE === "1" && (hostname === "localhost" || hostname === "127.0.0.1");
  if (hostname !== env.HUB_HOSTNAME.toLowerCase() && !local) {
    return notFound().response();
  }

  // Check the original path before forwarding can normalize dot segments.
  const segments = pathSegments(request.url);
  if (segments instanceof HubError) return headResponse(request, segments.response());

  const id = env.TEAM_HUB.idFromName(`team:${env.TEAM_ID}`);
  const stub = env.TEAM_HUB.get(id);
  const [, api, version, collection, room, suffix] = segments;
  if (segments.length !== 6 || api !== "api" || version !== "v1" || collection !== "rooms" || !room || suffix !== "stream") {
    return stub.fetch(request);
  }

  try {
    allow(request.method, ["GET", "HEAD"]);
    const hash = bearer(request);
    const read = (after: number) => stub.readRoomPage({ hash, room, after });
    const remainingMs = () => deadline - now();
    const initial = await readFirstPage(() => stub.readRoomPage({
      hash, room, after: request.url, observe: request.method === "GET",
    }), remainingMs);
    if (initial === undefined) return new Response(null, { headers: streamHeaders });
    if (!initial.ok) {
      throw initial.status === 401 ? unauthorized() : new HubError(initial.status, initial.message);
    }
    const body = request.method === "HEAD" ? null : roomStream(request.signal, room, initial, read, limits, remainingMs);
    return new Response(body, { headers: streamHeaders });
  } catch (error) {
    const failure = error instanceof HubError ? error : new HubError(500, "hub state unavailable");
    return headResponse(request, failure.response());
  }
}

async function readFirstPage(read: () => Promise<RoomPage>, remainingMs: () => number): Promise<RoomPage | undefined> {
  const remaining = remainingMs();
  if (remaining <= 0) return;
  const timeout = Promise.withResolvers<undefined>();
  const timer = setTimeout(() => timeout.resolve(undefined), remaining);
  try {
    const page = await Promise.race([read(), timeout.promise]);
    if (remainingMs() > 0) return page;
  } catch (error) {
    if (remainingMs() > 0) throw error;
  } finally {
    clearTimeout(timer);
  }
}

export class TeamHub extends DurableObject<Env> {
  private readonly schemaVersion: Promise<number>;
  private readonly storage: HubStorage;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.storage = new HubStorage(ctx.storage);
    this.schemaVersion = ctx.blockConcurrencyWhile(async () => {
      const version = migrate(this.storage);
      if (await ctx.storage.getAlarm() === null) await ctx.storage.setAlarm(Date.now() + memberMaintenanceIntervalMs);
      return version;
    });
  }

  async alarm(): Promise<void> {
    await this.schemaVersion;
    this.storage.transaction((sql) => pruneMemberObservations(sql, Date.now()));
    await this.ctx.storage.setAlarm(Date.now() + memberMaintenanceIntervalMs);
  }

  async readRoomPage(read: RoomRead): Promise<RoomPage> {
    const { room, hash, after } = read;
    await this.schemaVersion;
    try {
      return this.storage.transaction((sql) => {
        const member = authenticate(sql, hash);
        readProject(sql, room);
        const cursor = typeof after === "string" ? replayCursor(after) : after;
        const frames = eventPage(sql, room, cursor);
        if ("observe" in read && read.observe) observeMember(sql, member, room, Date.now(), { kind: "room" });
        return { ok: true, cursor, frames };
      });
    } catch (error) {
      if (error instanceof HubError) return { ok: false, status: error.status, message: error.message };
      throw error;
    }
  }

  async fetch(request: Request): Promise<Response> {
    let response: Response;
    try {
      const segments = pathSegments(request.url);
      if (segments instanceof HubError) throw segments;
      await this.schemaVersion;
      response = await this.route(request, segments);
    } catch (error) {
      response = (error instanceof HubError ? error : new HubError(500, "hub state unavailable")).response();
    }
    return headResponse(request, response);
  }

  private async route(request: Request, segments: string[]): Promise<Response> {
    const { method } = request;
    const [, first, second, collection, target, suffix, ...extra] = segments;
    if (first === "healthz" && second === undefined) {
      allow(method, ["GET", "HEAD"]);
      return Response.json({ ok: true, schema: await this.schemaVersion }, { headers: jsonHeaders });
    }
    if (first === ".well-known" && (second === "jwks.json" || second === "audit-jwks.json") && collection === undefined) {
      allow(method, ["GET", "HEAD"]);
      return json(this.storage.transaction((sql) => publicKeys(sql, second === "jwks.json" ? "policy" : "audit")));
    }
    if (first === "api" && second === "v1" && collection === "admin" && target === "keys") {
      if (suffix !== "policy" && suffix !== "audit") throw notFound();
      if (extra.length === 0) {
        allow(method, ["POST"]);
        return json(await stageKey(request, this.storage, suffix, this.env));
      }
      if (extra.length === 1 && extra[0] === "active") {
        allow(method, ["PUT"]);
        return json(await activateKey(request, this.storage, suffix));
      }
      throw notFound();
    }
    if (first !== "api" || second !== "v1") throw notFound();
    if (collection === "rooms" && target) {
      if (extra.length === 0 && suffix === "events") {
        allow(method, ["GET", "HEAD", "POST"]);
        return method === "POST" ? postEvent(request, this.storage, target) : replayEvents(request, this.storage, target);
      }
      if (suffix === "claims") {
        if (extra.length === 0) {
          allow(method, ["POST"]);
          return handleClaim(request, this.storage, target, { action: "create" });
        }
        const [claim, action] = extra;
        if (extra.length === 2 && claim && (action === "renew" || action === "release")) {
          allow(method, ["POST"]);
          return handleClaim(request, this.storage, target, { action, claim });
        }
      }
    }
    if (collection === "admin" && target === "invites") {
      if (suffix === undefined) return handleInvite(request, this.storage, { kind: "collection" });
      if (suffix && extra.length === 0) return handleInvite(request, this.storage, { kind: "revoke", id: suffix });
      if (suffix && extra.length === 1 && extra[0] === "approve") {
        return handleInvite(request, this.storage, { kind: "approve", id: suffix });
      }
    }
    if (extra.length > 0) throw notFound();
    if (collection === "invites" && target === "redeem" && suffix === undefined) {
      return handleInvite(request, this.storage, { kind: "redeem" });
    }
    if (collection === "projects" && target && target !== "/") {
      if (suffix === "policy") {
        allow(method, ["PUT"]);
        return publishPolicy(request, this.storage, target, this.env.TEAM_ID, this.env.WRAP_KEY_V1);
      }
      if (suffix === undefined || suffix === "policy.bundle") {
        allow(method, ["GET", "HEAD"]);
        const hash = bearer(request);
        return this.storage.transaction((sql) => {
          const member = authenticate(sql, hash);
          return suffix ? readPolicy(sql, target, method === "GET" ? member : undefined) : json(readProject(sql, target));
        });
      }
    }
    if (collection === "admin") {
      if (suffix === undefined) {
        switch (target) {
          case "init":
            allow(method, ["POST"]);
            return json(await initialize(request, this.storage, this.env));
          case "members":
            allow(method, ["GET", "HEAD", "POST"]);
            return method === "POST" ? json(await addMember(request, this.storage)) : readMembers(request, this.storage);
          case "projects":
            allow(method, ["POST"]);
            return json(await addProject(request, this.storage));
        }
      }
      if (target === "members" && suffix && suffix !== "/") {
        allow(method, ["DELETE"]);
        return json(await removeMember(request, this.storage, suffix));
      }
    }
    if (collection === "audit" && target === "export" && suffix === undefined) {
      allow(method, ["GET", "HEAD"]);
      return exportAudit(request, this.storage, this.env.TEAM_ID, this.env.WRAP_KEY_V1);
    }
    throw notFound();
  }
}

function pathSegments(url: string): string[] | HubError {
  const path = url.replace(/^https?:\/\/[^/?#]*/i, "").split(/[?#]/, 1)[0];
  let segments: string[];
  try {
    // Decode after splitting so an escaped slash stays inside its segment.
    segments = path.split("/").map((segment) => decodeURIComponent(segment));
  } catch {
    return invalid("malformed path escape");
  }
  if (segments.some((segment) => segment === "." || segment === "..")) return notFound();
  return segments;
}

function headResponse(request: Request, response: Response): Response {
  if (request.method === "HEAD") return new Response(null, response);
  return response;
}

function allow(method: string, allowed: string[]): void {
  if (!allowed.includes(method)) throw new HubError(405, "method not allowed", { Allow: allowed.join(", ") });
}

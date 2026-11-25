import { DurableObject } from "cloudflare:workers";
import { addMember, addProject, initialize, removeMember } from "./admin";
import { bearer, HubError, json, notFound, routeID } from "./http";
import { publicKeys, publishPolicy, readPolicy, readProject } from "./publication";
import { authenticate } from "./state";
import { HubStorage, migrate } from "./storage";

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
    get(id: DurableObjectId): { fetch(request: Request): Promise<Response> };
  };
};

const jsonHeaders = { "Cache-Control": "no-store" };

export default {
  fetch(request: Request, env: RoutingEnv): Response | Promise<Response> {
    const { hostname } = new URL(request.url);
    const local = env.DEV_MODE === "1" && (hostname === "localhost" || hostname === "127.0.0.1");
    if (hostname !== env.HUB_HOSTNAME.toLowerCase() && !local) {
      return notFound().response();
    }

    const id = env.TEAM_HUB.idFromName(`team:${env.TEAM_ID}`);
    return env.TEAM_HUB.get(id).fetch(request);
  },
} satisfies ExportedHandler<Env>;

export class TeamHub extends DurableObject<Env> {
  private readonly schemaVersion: Promise<number>;
  private readonly storage: HubStorage;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.storage = new HubStorage(ctx.storage);
    this.schemaVersion = ctx.blockConcurrencyWhile(async () => migrate(this.storage));
  }

  async fetch(request: Request): Promise<Response> {
    const { pathname } = new URL(request.url);
    let response: Response;
    try {
      await this.schemaVersion;
      response = await this.route(request, pathname);
    } catch (error) {
      response = (error instanceof HubError ? error : new HubError(500, "hub state unavailable")).response();
    }
    if (request.method === "HEAD") return new Response(null, { status: response.status, headers: response.headers });
    return response;
  }

  private async route(request: Request, path: string): Promise<Response> {
    const { method } = request;
    if (path === "/healthz") {
      allow(method, ["GET", "HEAD"]);
      return Response.json({ ok: true, schema: await this.schemaVersion }, { headers: jsonHeaders });
    }
    if (path === "/.well-known/jwks.json") {
      allow(method, ["GET", "HEAD"]);
      return json(this.storage.transaction(publicKeys));
    }
    const project = /^\/api\/v1\/projects\/([^/]+)(?:\/(policy\.bundle|policy))?$/.exec(path);
    if (project) {
      if (project[2] === "policy") {
        allow(method, ["PUT"]);
        return publishPolicy(request, this.storage, routeID(project[1]), this.env.TEAM_ID, this.env.WRAP_KEY_V1);
      }
      allow(method, ["GET", "HEAD"]);
      const hash = bearer(request);
      return this.storage.transaction((sql) => {
        authenticate(sql, hash);
        const id = routeID(project[1]);
        return project[2] ? readPolicy(sql, id) : json(readProject(sql, id));
      });
    }
    switch (path) {
      case "/api/v1/admin/init":
        allow(method, ["POST"]);
        return json(await initialize(request, this.storage, this.env));
      case "/api/v1/admin/members":
        allow(method, ["POST"]);
        return json(await addMember(request, this.storage));
      case "/api/v1/admin/projects":
        allow(method, ["POST"]);
        return json(await addProject(request, this.storage));
    }
    const member = /^\/api\/v1\/admin\/members\/([^/]+)$/.exec(path);
    if (member) {
      allow(method, ["DELETE"]);
      return json(removeMember(request, this.storage, routeID(member[1])));
    }
    throw notFound();
  }
}

function allow(method: string, allowed: string[]): void {
  if (!allowed.includes(method)) throw new HubError(405, "method not allowed", { Allow: allowed.join(", ") });
}

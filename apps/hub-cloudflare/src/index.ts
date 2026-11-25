import { DurableObject } from "cloudflare:workers";
import { unescape as unescapeSegment } from "node:querystring";
import { addMember, addProject, initialize, removeMember } from "./admin";
import { bearer, HubError, json, notFound } from "./http";
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
    // ServeMux unescapes each segment after splitting, so an encoded slash stays inside its segment.
    const [, first, second, collection, target, suffix, ...extra] = path.split("/").map((segment) => unescapeSegment(segment));
    if (first === "healthz" && second === undefined) {
      allow(method, ["GET", "HEAD"]);
      return Response.json({ ok: true, schema: await this.schemaVersion }, { headers: jsonHeaders });
    }
    if (first === ".well-known" && second === "jwks.json" && collection === undefined) {
      allow(method, ["GET", "HEAD"]);
      return json(this.storage.transaction(publicKeys));
    }
    if (first !== "api" || second !== "v1" || extra.length > 0) throw notFound();
    if (collection === "projects" && target) {
      if (suffix === "policy") {
        allow(method, ["PUT"]);
        return publishPolicy(request, this.storage, target, this.env.TEAM_ID, this.env.WRAP_KEY_V1);
      }
      if (suffix === undefined || suffix === "policy.bundle") {
        allow(method, ["GET", "HEAD"]);
        const hash = bearer(request);
        return this.storage.transaction((sql) => {
          authenticate(sql, hash);
          return suffix ? readPolicy(sql, target) : json(readProject(sql, target));
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
            allow(method, ["POST"]);
            return json(await addMember(request, this.storage));
          case "projects":
            allow(method, ["POST"]);
            return json(await addProject(request, this.storage));
        }
      }
      if (target === "members" && suffix) {
        allow(method, ["DELETE"]);
        return json(removeMember(request, this.storage, suffix));
      }
    }
    throw notFound();
  }
}

function allow(method: string, allowed: string[]): void {
  if (!allowed.includes(method)) throw new HubError(405, "method not allowed", { Allow: allowed.join(", ") });
}

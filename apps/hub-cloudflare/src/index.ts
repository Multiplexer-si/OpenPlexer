import { DurableObject } from "cloudflare:workers";
import { HubStorage, migrate } from "./storage";

export interface Env {
  HUB_HOSTNAME: string;
  TEAM_ID: string;
  DEV_MODE?: string;
  TEAM_HUB: DurableObjectNamespace<TeamHub>;
  BOOTSTRAP_TOKEN?: string;
  WRAP_KEY_V1?: string;
}

const jsonHeaders = { "Cache-Control": "no-store" };

function notFound(): Response {
  return Response.json({ error: "not found" }, { status: 404, headers: jsonHeaders });
}

export default {
  fetch(request: Request, env: Env): Response | Promise<Response> {
    const { hostname } = new URL(request.url);
    const local = env.DEV_MODE === "1" && (hostname === "localhost" || hostname === "127.0.0.1");
    if (hostname !== env.HUB_HOSTNAME.toLowerCase() && !local) {
      return notFound();
    }

    const id = env.TEAM_HUB.idFromName(`team:${env.TEAM_ID}`);
    return env.TEAM_HUB.get(id).fetch(request);
  },
} satisfies ExportedHandler<Env>;

export class TeamHub extends DurableObject<Env> {
  private readonly schemaVersion: Promise<number>;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    const storage = new HubStorage(ctx.storage);
    this.schemaVersion = ctx.blockConcurrencyWhile(async () => migrate(storage));
  }

  async fetch(request: Request): Promise<Response> {
    if (request.method === "GET" && new URL(request.url).pathname === "/healthz") {
      return Response.json({ ok: true, schema: await this.schemaVersion }, { headers: jsonHeaders });
    }
    return notFound();
  }
}

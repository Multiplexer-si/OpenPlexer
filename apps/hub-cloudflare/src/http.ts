import { createHash } from "node:crypto";

export const jsonHeaders = { "Content-Type": "application/json", "Cache-Control": "no-store" };

export class HubError extends Error {
  constructor(readonly status: number, message: string, readonly headers?: HeadersInit) {
    super(message);
  }

  response(): Response {
    return json({ error: this.message }, this.status, this.headers);
  }
}

export function json(body: unknown, status = 200, extra?: HeadersInit): Response {
  return jsonText(JSON.stringify(body) + "\n", status, extra);
}

export function jsonText(body: string, status = 200, extra?: HeadersInit): Response {
  const headers = new Headers(extra);
  for (const [name, value] of Object.entries(jsonHeaders)) headers.set(name, value);
  return new Response(body, { status, headers });
}

export const unauthorized = () => new HubError(401, "invalid bearer token", { "WWW-Authenticate": "Bearer" });
export const forbidden = () => new HubError(403, "forbidden");
export const notFound = () => new HubError(404, "not found");
export const invalid = (detail: string) => new HubError(400, "invalid input: " + detail);
export const conflict = (detail: string) => new HubError(409, "already exists or conflicts with current state: " + detail);

export function tokenHash(token: string): ArrayBuffer {
  return Uint8Array.from(createHash("sha256").update(token).digest()).buffer;
}

export function bearer(request: Request, bootstrap = false): ArrayBuffer {
  const auth = request.headers.get("Authorization") ?? "";
  // Workerd combines repeated Authorization fields with a comma.
  if (auth.includes(",") || !/^Bearer /i.test(auth)) throw unauthorized();
  const token = auth.slice(7);
  if (bootstrap ? !token || /\s/u.test(token) : token.length !== 43 || /[^A-Za-z0-9_-]/.test(token)) throw unauthorized();
  return tokenHash(token);
}

export async function cappedBody(request: Request, limit: number): Promise<Uint8Array> {
  const reader = request.body?.getReader();
  if (!reader) return new Uint8Array();
  let size = 0;
  let bytes = new Uint8Array();
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      const end = size + value.byteLength;
      if (end > limit) {
        void reader.cancel().catch(() => {});
        throw new HubError(413, "request body is too large");
      }
      if (end > bytes.length) {
        const expanded = new Uint8Array(Math.min(limit, Math.max(end, bytes.length * 2)));
        expanded.set(bytes);
        bytes = expanded;
      }
      bytes.set(value, size);
      size = end;
    }
  } catch (error) {
    if (error instanceof HubError) throw error;
    throw new HubError(400, "could not read request body");
  } finally {
    reader.releaseLock();
  }
  return bytes.subarray(0, size);
}

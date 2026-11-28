import { forbidden } from "./http";

const dashboardHeaders = {
  "Cache-Control": "no-store",
  "Referrer-Policy": "no-referrer",
  "Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
    "base-uri 'none'; frame-ancestors 'none'; form-action 'none'; object-src 'none'",
  "Cross-Origin-Resource-Policy": "same-origin",
  "X-Content-Type-Options": "nosniff",
};

export function isDashboard(segments: string[]): boolean {
  return segments[1] === "admin";
}

export function dashboardResponse(request: Request, response: Response): Response {
  const headers = new Headers(response.headers);
  for (const [key, value] of Object.entries(dashboardHeaders)) headers.set(key, value);
  return new Response(request.method === "HEAD" ? null : response.body, { status: response.status, headers });
}

export function dashboardOrigin(request: Request, hostname: string, devMode?: string): string {
  const url = new URL(request.url);
  if (devMode === "1" && (url.hostname === "localhost" || url.hostname === "127.0.0.1")) return url.origin;
  const configured = "https://" + hostname.toLowerCase();
  if (url.origin !== configured) throw forbidden();
  return configured;
}

export function isMutation(method: string): boolean {
  return method !== "GET" && method !== "HEAD";
}

export function requireDashboardOrigin(request: Request, origin: string): void {
  const presented = request.headers.get("Origin");
  const site = request.headers.get("Sec-Fetch-Site");
  if (((presented !== null || isMutation(request.method)) && presented !== origin) ||
      site === "cross-site" || site === "same-site") throw forbidden();
}

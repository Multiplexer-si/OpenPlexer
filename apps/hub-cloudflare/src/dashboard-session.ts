import { strictAdminJSON } from "./admin";
import { bearer, cappedBody, forbidden, HubError, invalid, json, tokenHash } from "./http";
import { authenticateEnrollment, newToken, requireLead } from "./state";
import type { Role } from "./state";
import type { HubStorage, TransactionSQL } from "./storage";

const cookieName = "__Host-openplexer_admin";
const cookieFlags = "; Secure; HttpOnly; SameSite=Strict; Path=/";
const sessionSeconds = 8 * 60 * 60;
const secretPattern = /^[A-Za-z0-9_-]{43}$/;
const noSession = () => new HubError(401, "invalid or expired dashboard session");

type Session = { member_id: string; role: Role; csrf_secret: string; expires_at_ms: number };

export function sessionResponse(session: Session, cookie?: string): Response {
  return json({
    member_id: session.member_id, csrf: session.csrf_secret, expires_at: new Date(session.expires_at_ms).toISOString(),
  }, 200, cookie ? { "Set-Cookie": cookie } : undefined);
}

export function pruneSessions(sql: TransactionSQL, now: number): void {
  sql.exec("DELETE FROM admin_sessions WHERE expires_at_ms <= ?", now);
}

export async function signIn(request: Request, storage: HubStorage): Promise<Response> {
  const contentType = request.headers.get("Content-Type") ?? "";
  if (!/^application\/json(?:\s*;\s*charset=utf-8)?$/i.test(contentType)) {
    throw new HubError(415, "sign-in requires application/json");
  }
  const hash = bearer(request);
  const body = await cappedBody(request, 1024);
  return storage.transaction((sql) => {
    const enrollment = authenticateEnrollment(sql, hash, true);
    strictAdminJSON(body, [], "sign-in requires an empty JSON object");
    const id = newToken();
    const csrf_secret = newToken();
    const now = Date.now();
    const expires_at_ms = now + sessionSeconds * 1000;
    pruneSessions(sql, now);
    sql.exec(`INSERT INTO admin_sessions (session_digest, member_id, enrollment_id, csrf_secret, expires_at_ms)
      VALUES (?, ?, ?, ?, ?)`, tokenHash(id), enrollment.member_id, enrollment.enrollment_id, csrf_secret, expires_at_ms);
    const cookie = cookieName + "=" + id + "; Max-Age=" + sessionSeconds + cookieFlags;
    return sessionResponse({ ...enrollment, csrf_secret, expires_at_ms }, cookie);
  });
}

function sessionDigest(request: Request): ArrayBuffer {
  const cookies = (request.headers.get("Cookie") ?? "").split(";");
  const presented = cookies.map((cookie) => cookie.trim()).filter((cookie) => cookie.startsWith(cookieName + "="));
  if (presented.length !== 1) throw noSession();
  const id = presented[0].slice(cookieName.length + 1);
  if (!secretPattern.test(id)) throw noSession();
  return tokenHash(id);
}

export function dashboardSession(request: Request, mutation: boolean) {
  const digest = sessionDigest(request);
  const csrf = request.headers.get("X-CSRF-Token") ?? "";
  if (mutation && !secretPattern.test(csrf)) throw forbidden();
  const csrfHash = mutation ? tokenHash(csrf) : undefined;
  function read(sql: TransactionSQL): Session {
    const [session] = sql.exec<Session>(`SELECT s.member_id, m.role, s.csrf_secret, s.expires_at_ms
      FROM admin_sessions s JOIN members m ON m.member_id = s.member_id AND m.enrollment_id = s.enrollment_id
      WHERE s.session_digest = ? AND s.expires_at_ms > ?`, digest, Date.now());
    if (!session) throw noSession();
    requireLead(session.role);
    if (csrfHash && !crypto.subtle.timingSafeEqual(csrfHash, tokenHash(session.csrf_secret))) throw forbidden();
    return session;
  }
  return {
    read,
    authorize: (sql: TransactionSQL) => read(sql).member_id,
    async signOut(storage: HubStorage) {
      if ((await cappedBody(request, 1024)).length) throw invalid("sign-out does not accept a body");
      return storage.transaction((sql) => {
        read(sql);
        sql.exec("DELETE FROM admin_sessions WHERE session_digest = ?", digest);
        return new Response(null, { status: 204, headers: { "Set-Cookie": cookieName + "=; Max-Age=0" + cookieFlags } });
      });
    },
  };
}

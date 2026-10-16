// Worker entry: the HTTP surface. Room creation and lookup stay off the
// socket path; every WebSocket is forwarded to the Durable Object that owns
// its room, addressed by room code.
//
//   POST /rooms               -> { roomCode }
//   GET  /rooms/:code         -> 200 if live, 404 otherwise
//   GET  /ws?room=CODE        -> WebSocket (first frame: join or resume)
//   GET  /healthz, /readyz    -> probes

import { type Env, Room } from "./room";
import { isRoomCode, newRoomCode } from "./session";

export { Room };

function allowedOrigins(env: Env): string[] {
  return (env.ALLOWED_ORIGINS ?? "")
    .split(",")
    .map((s) => s.trim().toLowerCase())
    .filter(Boolean);
}

// originAllowed: an empty list means any origin (development), "*" also
// means any. A request without an Origin is a non-browser client.
function originAllowed(env: Env, origin: string | null): boolean {
  const list = allowedOrigins(env);
  if (!origin || list.length === 0 || list.includes("*")) return true;
  return list.includes(origin.toLowerCase());
}

function cors(env: Env, req: Request, res: Response): Response {
  const origin = req.headers.get("Origin");
  if (!origin || !originAllowed(env, origin)) return res;
  const out = new Response(res.body, res);
  out.headers.set("Access-Control-Allow-Origin", origin);
  out.headers.set("Vary", "Origin");
  out.headers.set("Access-Control-Allow-Methods", "GET, POST, OPTIONS");
  out.headers.set("Access-Control-Allow-Headers", "Content-Type");
  return out;
}

const stubFor = (env: Env, code: string) => env.ROOMS.get(env.ROOMS.idFromName(code));

async function route(req: Request, env: Env): Promise<Response> {
  const url = new URL(req.url);
  const path = url.pathname.replace(/\/+$/, "") || "/";

  if (req.method === "OPTIONS") return new Response(null, { status: 204 });
  if (path === "/healthz") return new Response("ok");
  if (path === "/readyz") return new Response("ready");

  if (path === "/rooms") {
    if (req.method !== "POST") return new Response(null, { status: 405 });
    for (let attempt = 0; attempt < 16; attempt++) {
      const code = newRoomCode();
      if (await stubFor(env, code).create(code)) return Response.json({ roomCode: code });
    }
    return new Response(null, { status: 503 });
  }

  if (path.startsWith("/rooms/")) {
    if (req.method !== "GET") return new Response(null, { status: 405 });
    const code = path.slice("/rooms/".length).toUpperCase();
    const live = isRoomCode(code) && (await stubFor(env, code).exists());
    return new Response(null, { status: live ? 200 : 404 });
  }

  if (path === "/ws") {
    if (req.headers.get("Upgrade")?.toLowerCase() !== "websocket") {
      return new Response("expected websocket", { status: 426 });
    }
    if (!originAllowed(env, req.headers.get("Origin"))) return new Response("forbidden origin", { status: 403 });
    const code = (url.searchParams.get("room") ?? "").trim().toUpperCase();
    // A malformed code still reaches a (empty) room object, which answers
    // the handshake with the same error an unknown room gets.
    return stubFor(env, isRoomCode(code) ? code : "INVALID").fetch(req);
  }

  return new Response("not found", { status: 404 });
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const res = await route(req, env);
    if (res.status === 101) return res;
    return cors(env, req, res);
  },
} satisfies ExportedHandler<Env>;

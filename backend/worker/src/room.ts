// The authoritative room: one Durable Object per room code. Port of
// backend/internal/room and backend/internal/transport/ws.
//
// A Durable Object processes one event at a time, so every command is
// applied serially through game.apply, just as the Go actor goroutine did.
// It uses the WebSocket Hibernation API: the object can be evicted from
// memory between messages, so all room state lives in storage and is
// reloaded on wake. Timers (turn budget, reveal window, room lifecycle) are a
// single storage alarm set to the earliest pending deadline.
//
// No gameplay logic lives here. The points of contact with the game are
// apply, view and (through eventMessage) projectEvent.

import { DurableObject } from "cloudflare:workers";
import { type Action, type GameEvent, type PlayerID, type State, GameError, apply, defaultConfig, newGame, seatOf, view } from "./game";
import {
  type Inbound,
  type Outbound,
  ProtocolError,
  claimSpaceMessage,
  closedMessage,
  codeFor,
  decodeInbound,
  decodeJoin,
  decodeResume,
  encode,
  errorMessage,
  eventMessage,
  intentFor,
  sanitizeName,
  snapshotMessage,
  versionSupported,
  viewMessage,
  welcomeMessage,
} from "./protocol";
import { bindDigest, constantTimeEqual, newPlayerId, newToken } from "./session";

export interface Env {
  ROOMS: DurableObjectNamespace<Room>;
  ALLOWED_ORIGINS?: string;
  TURN_SECONDS?: string;
  REVEAL_SECONDS?: string;
}

// Resource protection, not gameplay. The 4-9 player rule is the engine's.
const EventRing = 256;
const ResultLingerMs = 60_000;
const EmptyGraceMs = 30_000;
const IdleTimeoutMs = 30 * 60_000;
const MaxConnectionsPerRoom = 32;
const MaxConnectionsPerPlayer = 4;
const RateBurst = 20;
const RateWindowMs = 5_000;

interface RoomData {
  code: string;
  state: State;
  /** playerId -> digest of (room, player, token). */
  digests: Record<PlayerID, string>;
  /** Idempotency key per player: a repeated reqId is a retry. */
  lastReqId: Record<PlayerID, string>;
  ring: GameEvent[];
  createdAt: number;
  lastActivity: number;
  emptySince: number;
  endedAt: number;
}

/** Per-socket state that survives hibernation. */
interface Attachment {
  pid: PlayerID | null;
  count: number;
  windowAt: number;
}

const ROOM_KEY = "room";

export class Room extends DurableObject<Env> {
  #data: RoomData | null = null;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    ctx.blockConcurrencyWhile(async () => {
      this.#data = (await ctx.storage.get<RoomData>(ROOM_KEY)) ?? null;
    });
  }

  // ------------------------------------------------------------ RPC

  /** create initialises a fresh room. False if the code is already live. */
  async create(code: string): Promise<boolean> {
    if (this.#data) return false;
    const now = Date.now();
    const cfg = defaultConfig(crypto.getRandomValues(new Uint32Array(1))[0]!);
    const turn = Number(this.env.TURN_SECONDS);
    const reveal = Number(this.env.REVEAL_SECONDS);
    if (turn > 0) cfg.turnDurationMs = turn * 1000;
    if (reveal > 0) cfg.roundEndDurationMs = reveal * 1000;
    this.#data = {
      code,
      state: newGame(cfg),
      digests: {},
      lastReqId: {},
      ring: [],
      createdAt: now,
      lastActivity: now,
      emptySince: now,
      endedAt: 0,
    };
    this.#save();
    await this.#schedule();
    return true;
  }

  async exists(): Promise<boolean> {
    return this.#data !== null;
  }

  // ------------------------------------------------------------ sockets

  override async fetch(request: Request): Promise<Response> {
    if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket") {
      return new Response("expected websocket", { status: 426 });
    }
    const pair = new WebSocketPair();
    const [client, server] = [pair[0], pair[1]];
    this.ctx.acceptWebSocket(server);
    server.serializeAttachment({ pid: null, count: 0, windowAt: 0 } satisfies Attachment);
    return new Response(null, { status: 101, webSocket: client });
  }

  override async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    const att = ws.deserializeAttachment() as Attachment;
    try {
      if (att.pid === null) await this.#handshake(ws, message);
      else this.#handleFrame(ws, att, message);
    } catch (err) {
      // Contain anything unexpected to this one socket.
      console.error("room handler error", this.#data?.code, err instanceof Error ? err.message : "unknown");
      this.#fatal(ws, "", "ERR_INTERNAL");
    }
    await this.#schedule();
  }

  override async webSocketClose(ws: WebSocket): Promise<void> {
    this.#detach(ws);
    await this.#schedule();
  }

  override async webSocketError(ws: WebSocket): Promise<void> {
    this.#detach(ws);
    await this.#schedule();
  }

  override async alarm(): Promise<void> {
    if (!this.#data) return;
    this.#onTick(Date.now());
    await this.#schedule();
  }

  // ------------------------------------------------------------ handshake

  async #handshake(ws: WebSocket, message: string | ArrayBuffer) {
    let in_: Inbound;
    try {
      in_ = decodeInbound(message);
    } catch (err) {
      return this.#fatal(ws, "", codeFor(err));
    }
    if (!versionSupported(in_.v)) return this.#fatal(ws, in_.reqId, "ERR_UNSUPPORTED_VERSION");
    if (in_.type === "join") return this.#join(ws, in_);
    if (in_.type === "resume") return this.#resume(ws, in_);
    this.#fatal(ws, in_.reqId, "ERR_UNKNOWN_ACTION");
  }

  async #join(ws: WebSocket, in_: Inbound) {
    let p;
    try {
      p = decodeJoin(in_.payload);
    } catch (err) {
      return this.#fatal(ws, in_.reqId, codeFor(err));
    }
    // Credentials are minted before touching state: the digest is async, and
    // everything after the await below runs without yielding.
    const id = newPlayerId();
    const token = newToken();
    const code = p.roomCode.trim().toUpperCase();
    const digest = await bindDigest(code, id, token);

    const d = this.#data;
    if (!d || d.code !== code) return this.#fatal(ws, in_.reqId, "ERR_ROOM_NOT_FOUND");
    const now = Date.now();
    d.lastActivity = now;
    if (this.#authed().length >= MaxConnectionsPerRoom) return this.#fatal(ws, in_.reqId, "ERR_CONNECTION_LIMIT");

    let res;
    try {
      // The engine decides whether the join is legal.
      res = apply(d.state, { type: "JOIN", playerId: id, now, name: sanitizeName(p.name) });
    } catch (err) {
      return this.#fatal(ws, in_.reqId, codeFor(err));
    }
    d.state = res.state;
    d.digests[id] = digest;
    this.#record(res.events);
    this.#attach(ws, id);
    this.#send(ws, claimSpaceMessage());
    this.#send(ws, welcomeMessage(id, d.code, token, view(d.state, id)));
    this.#fanOut(res.events);
    this.#save();
  }

  async #resume(ws: WebSocket, in_: Inbound) {
    let p;
    try {
      p = decodeResume(in_.payload);
    } catch {
      return this.#fatal(ws, in_.reqId, "ERR_UNAUTHORIZED");
    }
    const code = p.roomCode.trim().toUpperCase();
    const presented = await bindDigest(code, p.playerId, p.token);

    // One undifferentiated failure for unknown room, unknown player and wrong
    // token, so the reply cannot be used to enumerate anything.
    const d = this.#data;
    const stored = d?.digests[p.playerId] ?? "";
    const ok = constantTimeEqual(stored, presented) && stored !== "" && d !== null && d.code === code;
    if (!d || !ok || seatOf(d.state, p.playerId) < 0) return this.#fatal(ws, in_.reqId, "ERR_UNAUTHORIZED");

    const authed = this.#authed();
    if (authed.length >= MaxConnectionsPerRoom) return this.#fatal(ws, in_.reqId, "ERR_CONNECTION_LIMIT");
    if (authed.filter(([, a]) => a.pid === p.playerId).length >= MaxConnectionsPerPlayer) {
      return this.#fatal(ws, in_.reqId, "ERR_CONNECTION_LIMIT");
    }

    const now = Date.now();
    d.lastActivity = now;
    let events: GameEvent[] = [];
    try {
      const res = apply(d.state, { type: "RECONNECT", playerId: p.playerId, now });
      d.state = res.state;
      events = res.events;
      this.#record(events);
    } catch {
      // A reconnect the engine refuses changes nothing.
    }

    this.#attach(ws, p.playerId);
    this.#send(ws, claimSpaceMessage());
    // The token is never reissued; the welcome on resume carries the view only.
    this.#send(ws, welcomeMessage(p.playerId, d.code, "", view(d.state, p.playerId)));
    // Replay what the ring still holds so animations can catch up. The view
    // is the authority.
    let replayed = 0;
    for (const e of d.ring) {
      if (e.seq > p.lastSeq) {
        this.#send(ws, eventMessage(e, p.playerId));
        replayed++;
      }
    }
    if (replayed === 0 && p.lastSeq > 0) this.#send(ws, snapshotMessage(view(d.state, p.playerId)));
    this.#send(ws, viewMessage(view(d.state, p.playerId)));
    if (events.length > 0) this.#fanOut(events, ws);
    this.#save();
  }

  // ------------------------------------------------------------ frames

  #handleFrame(ws: WebSocket, att: Attachment, message: string | ArrayBuffer) {
    const d = this.#data;
    if (!d) return this.#fatal(ws, "", "ERR_ROOM_CLOSED");
    const now = Date.now();

    // Fixed-window limiter applied before parsing.
    if (att.windowAt === 0 || now - att.windowAt >= RateWindowMs) {
      att.windowAt = now;
      att.count = 0;
    }
    att.count++;
    ws.serializeAttachment(att);
    if (att.count > RateBurst) return this.#fatal(ws, "", "ERR_RATE_LIMITED");

    let in_: Inbound;
    try {
      in_ = decodeInbound(message);
    } catch (err) {
      const code = codeFor(err);
      // Oversized frames are fatal; malformed content is reported and kept.
      if (code === "ERR_MESSAGE_TOO_LARGE") return this.#fatal(ws, "", code);
      return this.#send(ws, errorMessage("", code));
    }
    if (in_.type === "pong") return;
    if (in_.type === "join" || in_.type === "resume") {
      return this.#send(ws, errorMessage(in_.reqId, "ERR_UNKNOWN_ACTION"));
    }

    const pid = att.pid!;
    d.lastActivity = now;
    // A repeated reqId is a retry of one intent; answer it with the current
    // view without applying anything twice.
    if (in_.reqId && d.lastReqId[pid] === in_.reqId) {
      for (const [sock, a] of this.#authed()) if (a.pid === pid) this.#send(sock, viewMessage(view(d.state, pid)));
      return;
    }

    let res;
    try {
      // The actor and clock come from the session and the server, never
      // from the frame.
      const action = { ...intentFor(in_), playerId: pid, now } as Action;
      res = apply(d.state, action);
    } catch (err) {
      if (!(err instanceof ProtocolError) && !(err instanceof GameError)) throw err;
      return this.#send(ws, errorMessage(in_.reqId, codeFor(err)));
    }
    d.state = res.state;
    if (in_.reqId) d.lastReqId[pid] = in_.reqId;
    this.#record(res.events);
    this.#fanOut(res.events);
    this.#noteTerminal(now);
    this.#save();
  }

  // ------------------------------------------------------------ presence

  #detach(ws: WebSocket) {
    const d = this.#data;
    const att = ws.deserializeAttachment() as Attachment | null;
    try {
      ws.close(1000, "bye");
    } catch {
      // already closed
    }
    if (!d || !att?.pid) return;
    const now = Date.now();
    const remaining = this.#authed().filter(([s]) => s !== ws);
    // Only when a player has no connections left do they count as gone; the
    // engine decides what that means for the round.
    if (!remaining.some(([, a]) => a.pid === att.pid) && seatOf(d.state, att.pid) >= 0) {
      try {
        const res = apply(d.state, { type: "DISCONNECT", playerId: att.pid, now });
        d.state = res.state;
        this.#record(res.events);
        this.#fanOut(res.events, ws);
        this.#noteTerminal(now);
      } catch {
        // nothing to do
      }
    }
    if (remaining.length === 0 && d.emptySince === 0) d.emptySince = now;
    this.#save();
  }

  // ------------------------------------------------------------ time

  // onTick advances engine time and then evaluates the room lifecycle.
  // Round advancement happens only here, so no client can cut the reveal
  // window short for everybody else.
  #onTick(now: number) {
    const d = this.#data!;
    try {
      const res = apply(d.state, { type: "TICK", playerId: "", now });
      d.state = res.state;
      if (res.events.length > 0) {
        // The engine did real work, so the room is not idle.
        d.lastActivity = now;
        this.#record(res.events);
        this.#fanOut(res.events);
        this.#noteTerminal(now);
      }
    } catch (err) {
      console.error("tick failed", d.code, err instanceof Error ? err.message : "unknown");
      return this.#close("internal error");
    }
    const reason = this.#lifecycle(now);
    if (reason) return this.#close(reason);
    this.#save();
  }

  #noteTerminal(now: number) {
    const d = this.#data!;
    if (d.state.phase === "GAME_OVER" && d.endedAt === 0) d.endedAt = now;
  }

  /** Returns a close reason, or null to keep the room open. */
  #lifecycle(now: number): string | null {
    const d = this.#data!;
    if (d.state.phase === "GAME_OVER") {
      if (d.endedAt === 0) d.endedAt = now;
      // An abandoned game has nobody left to see the result.
      if (!d.state.winner) return "game abandoned";
      if (now - d.endedAt >= ResultLingerMs) return "game over";
    }
    if (this.#authed().length === 0) {
      if (d.emptySince === 0) d.emptySince = now;
      else if (now - d.emptySince >= EmptyGraceMs) return "room empty";
    } else {
      d.emptySince = 0;
    }
    if (now - d.lastActivity >= IdleTimeoutMs) return "room idle";
    return null;
  }

  // #schedule sets the single alarm to the earliest pending deadline.
  async #schedule() {
    const d = this.#data;
    if (!d) {
      await this.ctx.storage.deleteAlarm();
      return;
    }
    const s = d.state;
    const at: number[] = [d.lastActivity + IdleTimeoutMs];
    if (s.phase === "CLAIMING" && s.turnDeadline) at.push(s.turnDeadline);
    if (s.phase === "ROUND_END" && s.roundEndDeadline) at.push(s.roundEndDeadline);
    if (s.phase === "GAME_OVER") at.push(s.winner ? (d.endedAt || Date.now()) + ResultLingerMs : Date.now());
    if (this.#authed().length === 0) at.push((d.emptySince || Date.now()) + EmptyGraceMs);
    const next = Math.max(Math.min(...at), Date.now() + 50);
    const current = await this.ctx.storage.getAlarm();
    if (current !== next) await this.ctx.storage.setAlarm(next);
  }

  // #close tells every connection why, then forgets the room entirely so the
  // code can be reused.
  #close(reason: string) {
    for (const ws of this.ctx.getWebSockets()) {
      this.#send(ws, closedMessage(reason));
      try {
        ws.close(1000, reason.slice(0, 100));
      } catch {
        // already closed
      }
    }
    this.#data = null;
    void this.ctx.storage.deleteAll();
  }

  // ------------------------------------------------------------ plumbing

  #save() {
    // Not awaited: the output gate holds outgoing messages until the write
    // is durable, so clients never observe state that could be lost.
    if (this.#data) void this.ctx.storage.put(ROOM_KEY, this.#data);
  }

  #authed(): [WebSocket, Attachment][] {
    const out: [WebSocket, Attachment][] = [];
    for (const ws of this.ctx.getWebSockets()) {
      if (ws.readyState !== WebSocket.OPEN) continue;
      const a = ws.deserializeAttachment() as Attachment | null;
      if (a?.pid) out.push([ws, a]);
    }
    return out;
  }

  #attach(ws: WebSocket, pid: PlayerID) {
    ws.serializeAttachment({ pid, count: 0, windowAt: 0 } satisfies Attachment);
    this.#data!.emptySince = 0;
  }

  #record(events: GameEvent[]) {
    if (events.length === 0) return;
    const d = this.#data!;
    d.ring.push(...events);
    if (d.ring.length > EventRing) d.ring.splice(0, d.ring.length - EventRing);
  }

  // #fanOut delivers projected events and then each recipient's current
  // view, so a client that renders only views stays fully playable.
  #fanOut(events: GameEvent[], skip?: WebSocket) {
    const d = this.#data!;
    for (const [ws, a] of this.#authed()) {
      if (ws === skip) continue;
      for (const e of events) this.#send(ws, eventMessage(e, a.pid!));
      this.#send(ws, viewMessage(view(d.state, a.pid!)));
    }
  }

  #send(ws: WebSocket, m: Outbound) {
    try {
      ws.send(encode(m));
    } catch {
      // The socket is gone; webSocketClose will clean up.
    }
  }

  #fatal(ws: WebSocket, reqId: string, code: string) {
    this.#send(ws, errorMessage(reqId, code));
    try {
      ws.close(1008, code);
    } catch {
      // already closed
    }
  }
}

// The wire vocabulary. Port of backend/internal/protocol.
//
// No gameplay logic lives here. Its one substantive job is that every event
// leaving the process passes through projectEvent, which eventMessage
// enforces by projecting inside the only constructor for an event message.

import {
  type GameEvent,
  type PlayerID,
  type PlayerView,
  type ShowResult,
  ErrUnknownAction,
  GameError,
  projectEvent,
} from "./game";
import { type Card, type Claim, ClaimSpaceSize, allClaimsCopy, cardCode, claimFromJSON, claimToJSON } from "./poker";

export const MaxFrameBytes = 4096;
export const ProtocolVersion = 1;
export const MaxNameBytes = 32;

export const versionSupported = (v: number): boolean => v === 0 || v === ProtocolVersion;

// ---------------------------------------------------------------- errors

const codeMessage: Record<string, string> = {
  ERR_WRONG_PHASE: "that action is not legal in this phase",
  ERR_NOT_YOUR_TURN: "it is not your turn",
  ERR_NOT_SHOW_ELIGIBLE: "only the immediate next player may call SHOW",
  ERR_NO_STANDING_CLAIM: "there is no standing claim to challenge",
  ERR_CLAIM_NOT_HIGHER: "your claim must be strictly higher than the standing claim",
  ERR_MALFORMED_CLAIM: "that is not a well-formed claim",
  ERR_PLAYER_ELIMINATED: "you are eliminated",
  ERR_GAME_OVER: "the game is over",
  ERR_NOT_ENOUGH_PLAYERS: "not enough players to start",
  ERR_TABLE_FULL: "the table is full",
  ERR_GAME_IN_PROGRESS: "the game is already in progress",
  ERR_ALREADY_JOINED: "you have already joined",
  ERR_NOT_ALL_READY: "not all players are ready",
  ERR_INVALID_PLAYER_ID: "invalid player id",
  ERR_UNKNOWN_PLAYER: "unknown player",
  ERR_UNKNOWN_ACTION: "unknown action",
  ERR_UNAUTHORIZED: "unauthorized",
  ERR_ROOM_NOT_FOUND: "room not found",
  ERR_ROOM_CLOSED: "room closed",
  ERR_RATE_LIMITED: "too many messages",
  ERR_MESSAGE_TOO_LARGE: "message too large",
  ERR_BAD_FRAME: "malformed message",
  ERR_INTERNAL: "internal error",
  ERR_UNSUPPORTED_VERSION: "unsupported protocol version",
  ERR_ROOM_LIMIT: "the server is at its room limit",
  ERR_CONNECTION_LIMIT: "too many connections for this room",
  ERR_SERVER_BUSY: "the server is busy, try again",
};

// ProtocolError carries a wire code for transport-level failures.
export class ProtocolError extends Error {
  constructor(readonly code: string) {
    super(code);
  }
}

export const badFrame = () => new ProtocolError("ERR_BAD_FRAME");

// codeFor maps any thrown value to a stable wire code. Messages come from a
// fixed table and never from the error itself.
export function codeFor(err: unknown): string {
  if (err instanceof GameError || err instanceof ProtocolError) {
    return err.code in codeMessage ? err.code : "ERR_INTERNAL";
  }
  return "ERR_INTERNAL";
}

// ---------------------------------------------------------------- inbound

export type ClientMessageType =
  | "join" | "resume" | "set_ready" | "start_game" | "make_claim" | "call_show" | "leave" | "pong";

export interface Inbound {
  type: string;
  v: number;
  reqId: string;
  payload: unknown;
}

const isObject = (x: unknown): x is Record<string, unknown> =>
  typeof x === "object" && x !== null && !Array.isArray(x);

function rejectUnknown(o: Record<string, unknown>, allowed: string[]) {
  for (const k of Object.keys(o)) if (!allowed.includes(k)) throw badFrame();
}

// decodeInbound parses and size-checks a client frame. Unknown fields are a
// protocol violation: a client sending a playerId alongside an action is
// trying to impersonate.
export function decodeInbound(frame: string | ArrayBuffer): Inbound {
  let text: string;
  if (typeof frame === "string") {
    if (new TextEncoder().encode(frame).length > MaxFrameBytes) throw new ProtocolError("ERR_MESSAGE_TOO_LARGE");
    text = frame;
  } else {
    if (frame.byteLength > MaxFrameBytes) throw new ProtocolError("ERR_MESSAGE_TOO_LARGE");
    try {
      text = new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(frame);
    } catch {
      throw badFrame();
    }
  }
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    throw badFrame();
  }
  if (!isObject(raw)) throw badFrame();
  rejectUnknown(raw, ["type", "v", "reqId", "payload"]);
  const { type, v = 0, reqId = "", payload } = raw;
  if (typeof type !== "string" || type === "") throw badFrame();
  if (typeof v !== "number" || !Number.isInteger(v)) throw badFrame();
  if (typeof reqId !== "string" || reqId.length > 64) throw badFrame();
  return { type, v, reqId, payload: payload ?? null };
}

function payloadObject(p: unknown, allowed: string[]): Record<string, unknown> {
  if (p === null || p === undefined) return {};
  if (!isObject(p)) throw badFrame();
  rejectUnknown(p, allowed);
  return p;
}

const str = (x: unknown): string => {
  if (x === undefined || x === null) return "";
  if (typeof x !== "string") throw badFrame();
  return x;
};

export interface JoinPayload {
  roomCode: string;
  name: string;
}

export function decodeJoin(p: unknown): JoinPayload {
  const o = payloadObject(p, ["roomCode", "name"]);
  return { roomCode: str(o.roomCode), name: str(o.name) };
}

export interface ResumePayload {
  roomCode: string;
  playerId: string;
  token: string;
  lastSeq: number;
}

export function decodeResume(p: unknown): ResumePayload {
  const o = payloadObject(p, ["roomCode", "playerId", "token", "lastSeq"]);
  const lastSeq = o.lastSeq ?? 0;
  if (typeof lastSeq !== "number" || !Number.isInteger(lastSeq)) throw badFrame();
  return { roomCode: str(o.roomCode), playerId: str(o.playerId), token: str(o.token), lastSeq };
}

// sanitizeName strips control characters and lone surrogates, then truncates
// to MaxNameBytes of UTF-8 on a code point boundary.
export function sanitizeName(name: string): string {
  let out = "";
  for (const ch of name) {
    const cp = ch.codePointAt(0)!;
    if (cp < 0x20 || cp === 0x7f || (cp >= 0xd800 && cp <= 0xdfff)) continue;
    out += ch;
  }
  out = out.trim();
  const enc = new TextEncoder();
  if (enc.encode(out).length <= MaxNameBytes) return out;
  let cut = "";
  for (const ch of out) {
    if (enc.encode(cut + ch).length > MaxNameBytes) break;
    cut += ch;
  }
  return cut;
}

export type EngineIntent =
  | { type: "SET_READY"; ready: boolean }
  | { type: "START_GAME" }
  | { type: "MAKE_CLAIM"; claim: Claim }
  | { type: "CALL_SHOW" }
  | { type: "DISCONNECT" };

// intentFor turns a decoded client message into an engine action body. The
// actor and clock are added by the room, never taken from the frame. No
// rule checks happen here.
export function intentFor(in_: Inbound): EngineIntent {
  switch (in_.type) {
    case "set_ready": {
      const o = payloadObject(in_.payload, ["ready"]);
      const ready = o.ready ?? false;
      if (typeof ready !== "boolean") throw badFrame();
      return { type: "SET_READY", ready };
    }
    case "start_game":
      return { type: "START_GAME" };
    case "make_claim": {
      const o = payloadObject(in_.payload, ["claim"]);
      try {
        return { type: "MAKE_CLAIM", claim: claimFromJSON(o.claim) };
      } catch {
        throw badFrame();
      }
    }
    case "call_show":
      return { type: "CALL_SHOW" };
    case "leave":
      return { type: "DISCONNECT" };
  }
  // join/resume are connection lifecycle; tick/advance_round are internal.
  throw ErrUnknownAction();
}

// ---------------------------------------------------------------- outbound

export interface Outbound {
  type: "welcome" | "snapshot" | "event" | "view" | "error" | "claim_space" | "ping" | "closed";
  seq?: number;
  reqId?: string;
  payload?: unknown;
}

const cards = (cs: readonly Card[]): string[] => cs.map(cardCode);
const iso = (ms: number): string => new Date(ms).toISOString();

function showResultWire(r: ShowResult) {
  return {
    round: r.round,
    claimant: r.claimant,
    challenger: r.challenger,
    claim: claimToJSON(r.claim),
    claimTrue: r.claimTrue,
    witness: cards(r.witness),
    loser: r.loser,
    mode: r.mode,
    poolSize: r.poolSize,
    handSize: r.handSize,
  };
}

// viewWire converts a view to its JSON wire form: cards as codes, claims as
// tagged objects, deadlines as RFC 3339 strings and omitted when unset.
export function viewWire(v: PlayerView) {
  return {
    you: v.you,
    phase: v.phase,
    round: v.round,
    seq: v.seq,
    players: v.players.map((p) => ({
      id: p.id,
      name: p.name,
      seat: p.seat,
      handSize: p.handSize,
      cardCount: p.cardCount,
      status: p.status,
      ready: p.ready,
      isYou: p.isYou,
      isTurn: p.isTurn,
      isStarter: p.isStarter,
      ...(p.cards ? { cards: cards(p.cards) } : {}),
    })),
    poolSize: v.poolSize,
    evalMode: v.evalMode,
    handWidth: v.handWidth,
    claims: v.claims.map((c) => ({ ...c, claim: claimToJSON(c.claim) })),
    ...(v.standingClaim ? { standingClaim: claimToJSON(v.standingClaim) } : {}),
    standingOrdinal: v.standingOrdinal,
    ...(v.turnPlayer ? { turnPlayer: v.turnPlayer } : {}),
    turnSeat: v.turnSeat,
    starterSeat: v.starterSeat,
    yourTurn: v.yourTurn,
    canClaim: v.canClaim,
    canShow: v.canShow,
    ...(v.turnDeadline ? { turnDeadline: iso(v.turnDeadline) } : {}),
    ...(v.roundEndDeadline ? { roundEndDeadline: iso(v.roundEndDeadline) } : {}),
    revealed: v.revealed,
    ...(v.lastShow ? { lastShow: showResultWire(v.lastShow) } : {}),
    ...(v.winner ? { winner: v.winner } : {}),
  };
}

export function claimSpaceMessage(): Outbound {
  return { type: "claim_space", payload: { size: ClaimSpaceSize, claims: allClaimsCopy().map(claimToJSON) } };
}

export function viewMessage(v: PlayerView): Outbound {
  return { type: "view", seq: v.seq, payload: { view: viewWire(v) } };
}

export function snapshotMessage(v: PlayerView): Outbound {
  return { type: "snapshot", seq: v.seq, payload: { view: viewWire(v) } };
}

// welcomeMessage is the only server message that ever carries the token.
export function welcomeMessage(id: PlayerID, roomCode: string, token: string, v: PlayerView): Outbound {
  return {
    type: "welcome",
    seq: v.seq,
    payload: { v: ProtocolVersion, playerId: id, roomCode, token, view: viewWire(v) },
  };
}

export function errorMessage(reqId: string, code: string): Outbound {
  const known = code in codeMessage ? code : "ERR_INTERNAL";
  return {
    type: "error",
    ...(reqId ? { reqId } : {}),
    payload: { code: known, message: codeMessage[known] },
  };
}

export const closedMessage = (reason: string): Outbound => ({ type: "closed", payload: { reason } });

// eventMessage builds the event message for one recipient. Projection is
// inside, so there is no way to send an unprojected event.
export function eventMessage(e: GameEvent, viewer: PlayerID): Outbound {
  const p = projectEvent(e, viewer);
  let hands: Record<string, string[]> | null = null;
  if (p.hands) {
    hands = {};
    for (const [id, cs] of Object.entries(p.hands)) hands[id] = cards(cs);
  }
  return {
    type: "event",
    seq: p.seq,
    payload: {
      seq: p.seq,
      type: p.type,
      round: p.round,
      ...(p.player ? { player: p.player } : {}),
      seat: p.seat,
      claim: p.claim ? claimToJSON(p.claim) : null,
      hands,
      result: p.result ? showResultWire(p.result) : null,
      ...(p.handSize ? { handSize: p.handSize } : {}),
      ...(p.attemptedHandSize ? { attemptedHandSize: p.attemptedHandSize } : {}),
      ...(p.reason ? { reason: p.reason } : {}),
      deadline: p.deadline ? iso(p.deadline) : null,
      ...(p.poolSize ? { poolSize: p.poolSize } : {}),
      mode: p.mode,
    },
  };
}

export const encode = (m: Outbound): string => JSON.stringify(m);

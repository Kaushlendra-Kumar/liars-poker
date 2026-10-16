/**
 * Wire types for protocol v1.
 *
 * This file mirrors `backend/internal/protocol` and `docs/protocol.md`. It is
 * the single place the wire format is described; nothing else in the app
 * parses a raw server message.
 *
 * It contains no gameplay logic. There is no claim comparison, no poker
 * classification, no turn-order rule and no evaluation of anything. Claim
 * ordering arrives as server-assigned ordinals and legality arrives as
 * `canClaim` / `canShow` / `standingOrdinal`.
 */

/** The protocol version this client speaks. Frozen at 1. */
export const PROTOCOL_VERSION = 1;

// ---------------------------------------------------------------- cards

/**
 * A card code exactly as the server sends it: rank then suit letter, e.g.
 * "8S", "10H", "AD". The client never constructs one; it only displays what
 * it is given.
 */
export type CardCode = string;

// ---------------------------------------------------------------- claims

export type ClaimCategory =
  | "HIGH_CARD"
  | "PAIR"
  | "TWO_PAIR"
  | "THREE_OF_A_KIND"
  | "STRAIGHT"
  | "FLUSH"
  | "FULL_HOUSE"
  | "FOUR_OF_A_KIND"
  | "STRAIGHT_FLUSH"
  | "ROYAL_FLUSH";

export type SuitName = "CLUBS" | "DIAMONDS" | "HEARTS" | "SPADES";

/**
 * A claim as the server models it. Field meaning is per-category and is
 * documented on the Go type: `primary` is the principal rank (high card,
 * pair rank, trips rank, quads rank, straight high or flush high) and
 * `secondary` is the low pair or the full-house pair. Unused fields are
 * absent.
 */
export interface Claim {
  category: ClaimCategory;
  primary?: string;
  secondary?: string;
  suit?: SuitName;
}

export type EvalMode = "REDUCED_POOL" | "EXACT_FIVE";

export type Phase =
  | "WAITING_FOR_PLAYERS"
  | "CLAIMING"
  | "ROUND_END"
  | "GAME_OVER";

export type PlayerStatus = "ACTIVE" | "DISCONNECTED" | "ELIMINATED";

// ---------------------------------------------------------------- view

export interface PlayerSummary {
  id: string;
  name: string;
  seat: number;
  handSize: number;
  cardCount: number;
  status: PlayerStatus;
  ready: boolean;
  isYou: boolean;
  isTurn: boolean;
  isStarter: boolean;
  /** Present only for yourself, or for everyone once a round is revealed. */
  cards?: CardCode[];
}

export interface ClaimView {
  player: string;
  seat: number;
  claim: Claim;
  /** Human text rendered by the server. Displayed verbatim. */
  text: string;
  ordinal: number;
}

export interface ShowResult {
  round: number;
  claimant: string;
  challenger: string;
  claim: Claim;
  claimTrue: boolean;
  witness?: CardCode[];
  loser: string;
  mode: EvalMode;
  poolSize: number;
  handSize: number;
}

/**
 * The view exactly as it arrives on the wire.
 *
 * Go marshals a nil slice as `null`, not `[]`, so every array field is
 * nullable in transit: a round with no claims yet sends `"claims": null`.
 * The store normalises these on ingest, and the rest of the app sees
 * PlayerView with real arrays. Recorded in PROTOCOL_GAPS.md.
 */
export type RawPlayerView = Omit<PlayerView, "players" | "claims" | "lastShow"> & {
  players: PlayerSummary[] | null;
  claims: ClaimView[] | null;
  lastShow?: (Omit<ShowResult, "witness"> & { witness?: CardCode[] | null }) | null;
};

/**
 * The authoritative client state. Everything the UI renders comes from here.
 *
 * `standingOrdinal` is the floor a legal raise must exceed. It is -1 when no
 * claim stands. The client filters the static claim ladder against it by
 * integer comparison, which is not a rule, just a comparison on
 * server-supplied numbers.
 */
export interface PlayerView {
  you: string;
  phase: Phase;
  round: number;
  seq: number;
  players: PlayerSummary[];
  poolSize: number;
  evalMode: EvalMode;
  handWidth: number;
  claims: ClaimView[];
  standingClaim?: Claim;
  standingOrdinal: number;
  turnPlayer?: string;
  turnSeat: number;
  starterSeat: number;
  yourTurn: boolean;
  canClaim: boolean;
  canShow: boolean;
  turnDeadline?: string;
  roundEndDeadline?: string;
  revealed: boolean;
  lastShow?: ShowResult;
  winner?: string;
}

// ---------------------------------------------------------------- messages

export type ClientMessageType =
  | "join"
  | "resume"
  | "set_ready"
  | "start_game"
  | "make_claim"
  | "call_show"
  | "leave"
  | "pong";

export type ServerMessageType =
  | "welcome"
  | "snapshot"
  | "event"
  | "view"
  | "error"
  | "claim_space"
  | "ping"
  | "closed";

export interface Inbound {
  type: ClientMessageType;
  v?: number;
  reqId?: string;
  payload?: unknown;
}

export interface Outbound {
  type: ServerMessageType;
  seq?: number;
  reqId?: string;
  payload?: unknown;
}

export interface WelcomePayload {
  v: number;
  playerId: string;
  roomCode: string;
  /** Present only on join. Empty on resume: the client already holds it. */
  token: string;
  view: PlayerView;
}

export interface ViewPayload {
  view: PlayerView;
}

export interface ClaimSpacePayload {
  size: number;
  claims: Claim[];
}

export interface ErrorPayload {
  code: ErrorCode;
  message: string;
}

export interface ClosedPayload {
  reason: string;
}

export type EventType =
  | "PLAYER_JOINED"
  | "PLAYER_READY"
  | "GAME_STARTED"
  | "ROUND_STARTED"
  | "CARDS_DEALT"
  | "TURN_STARTED"
  | "CLAIM_MADE"
  | "SHOW_CALLED"
  | "CARDS_REVEALED"
  | "CLAIM_VALIDATED"
  | "PENALTY_APPLIED"
  | "PLAYER_ELIMINATED"
  | "ROUND_ENDED"
  | "GAME_ENDED"
  | "PLAYER_DISCONNECTED"
  | "PLAYER_RECONNECTED"
  | "TURN_TIMED_OUT";

export interface GameEvent {
  seq: number;
  type: EventType;
  round: number;
  player?: string;
  seat: number;
  claim: Claim | null;
  /** For CARDS_DEALT this is your own hand only; for CARDS_REVEALED, everyone. */
  hands: Record<string, CardCode[]> | null;
  result: ShowResult | null;
  handSize?: number;
  attemptedHandSize?: number;
  reason?: string;
  deadline: string | null;
  poolSize?: number;
  mode: EvalMode;
}

// ---------------------------------------------------------------- errors

export type ErrorCode =
  | "ERR_WRONG_PHASE"
  | "ERR_NOT_YOUR_TURN"
  | "ERR_NOT_SHOW_ELIGIBLE"
  | "ERR_NO_STANDING_CLAIM"
  | "ERR_CLAIM_NOT_HIGHER"
  | "ERR_MALFORMED_CLAIM"
  | "ERR_PLAYER_ELIMINATED"
  | "ERR_GAME_OVER"
  | "ERR_NOT_ENOUGH_PLAYERS"
  | "ERR_TABLE_FULL"
  | "ERR_GAME_IN_PROGRESS"
  | "ERR_ALREADY_JOINED"
  | "ERR_NOT_ALL_READY"
  | "ERR_INVALID_PLAYER_ID"
  | "ERR_UNKNOWN_PLAYER"
  | "ERR_UNKNOWN_ACTION"
  | "ERR_UNAUTHORIZED"
  | "ERR_ROOM_NOT_FOUND"
  | "ERR_ROOM_CLOSED"
  | "ERR_RATE_LIMITED"
  | "ERR_MESSAGE_TOO_LARGE"
  | "ERR_BAD_FRAME"
  | "ERR_INTERNAL"
  | "ERR_UNSUPPORTED_VERSION"
  | "ERR_ROOM_LIMIT"
  | "ERR_CONNECTION_LIMIT"
  | "ERR_SERVER_BUSY";

/** Errors after which reconnecting with the stored credential is pointless. */
export const TERMINAL_ERRORS: ReadonlySet<ErrorCode> = new Set<ErrorCode>([
  "ERR_UNAUTHORIZED",
  "ERR_ROOM_NOT_FOUND",
  "ERR_ROOM_CLOSED",
  "ERR_UNSUPPORTED_VERSION",
  "ERR_TABLE_FULL",
  "ERR_GAME_IN_PROGRESS",
]);

// ---------------------------------------------------------------- parsing

/**
 * Narrow an unknown frame to an Outbound envelope.
 *
 * Validation here is structural only. It never judges whether a claim is
 * legal or whether an action is allowed, because those are the server's to
 * decide and a second opinion would be a second engine.
 */
export function parseOutbound(raw: unknown): Outbound | null {
  if (typeof raw !== "string") return null;
  let decoded: unknown;
  try {
    decoded = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof decoded !== "object" || decoded === null) return null;
  const type = (decoded as { type?: unknown }).type;
  if (typeof type !== "string") return null;
  const known: ServerMessageType[] = [
    "welcome",
    "snapshot",
    "event",
    "view",
    "error",
    "claim_space",
    "ping",
    "closed",
  ];
  if (!known.includes(type as ServerMessageType)) return null;
  return decoded as Outbound;
}

/** claimKey is a stable identity for a claim, for React keys and lookups. */
export function claimKey(claim: Claim): string {
  return [claim.category, claim.primary ?? "", claim.secondary ?? "", claim.suit ?? ""].join(
    "|",
  );
}

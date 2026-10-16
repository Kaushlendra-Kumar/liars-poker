/**
 * The game store.
 *
 * Two rules shape this file.
 *
 * 1. **The view is the authoritative state.** Events never mutate game
 *    state; they only append to an activity log used for animation and
 *    history. A store that ignored every event and kept only the latest view
 *    would still be fully playable, which is the frontend form of the
 *    server's TestClientWithNoReducerIsPlayable.
 *
 * 2. **Hidden cards are stripped on the way in.** The server already
 *    projects per recipient, but this store removes any other player's cards
 *    from an unrevealed view anyway, and removes any hand from an event
 *    other than a reveal. So even a compromised or buggy server cannot make
 *    this UI render a hidden card: the data never reaches the components.
 *
 * There is no gameplay logic here. No claim is compared, no hand is
 * classified, no turn or timer rule is evaluated. Legality arrives as
 * `canClaim`, `canShow` and `standingOrdinal`.
 */

import type {
  Claim,
  ClaimSpacePayload,
  ClosedPayload,
  ErrorPayload,
  GameEvent,
  PlayerSummary,
  PlayerView,
  RawPlayerView,
} from "@/protocol/types";
import type { ConnectionStatus, ViewSource } from "@/protocol/client";

export interface ActivityEntry {
  seq: number;
  type: GameEvent["type"];
  round: number;
  player?: string;
  text: string;
}

export interface GameState {
  status: ConnectionStatus;
  statusDetail?: string;
  roomCode?: string;
  playerId?: string;
  view?: PlayerView;
  /** The static 367-claim ladder, ascending, exactly as the server sent it. */
  ladder: Claim[];
  ladderSize: number;
  activity: ActivityEntry[];
  lastError?: ErrorPayload;
  closedReason?: string;
}

export const initialState: GameState = {
  status: "idle",
  ladder: [],
  ladderSize: 0,
  activity: [],
};

/** Maximum activity entries kept for the history panel. */
const ACTIVITY_LIMIT = 60;

// ---------------------------------------------------------------- sanitise

/**
 * sanitizeView removes every card the viewer is not entitled to see.
 *
 * The condition is the whole hidden-information rule, and it is deliberately
 * `isYou || revealed` rather than "trust whatever arrived". A server that
 * leaked a hand would have that hand discarded here.
 */
export function sanitizeView(raw: PlayerView | RawPlayerView): PlayerView {
  // Normalise the wire's nullable arrays first. Go sends a nil slice as
  // `null`, so a round with no claims arrives as `"claims": null` and any
  // downstream `.length` would throw.
  const view: PlayerView = {
    ...(raw as PlayerView),
    players: raw.players ?? [],
    claims: raw.claims ?? [],
    lastShow: raw.lastShow
      ? { ...raw.lastShow, witness: raw.lastShow.witness ?? undefined }
      : undefined,
  };
  const players: PlayerSummary[] = view.players.map((p) => {
    if (p.isYou || view.revealed) return p;
    if (!p.cards || p.cards.length === 0) return p;
    const { cards: _dropped, ...rest } = p;
    return rest;
  });
  const out: PlayerView = { ...view, players };
  if (!view.revealed && out.lastShow?.witness) {
    // A witness is only meaningful with a reveal; before one it would be a
    // list of other players' cards.
    out.lastShow = { ...out.lastShow, witness: undefined };
  }
  return out;
}

/**
 * sanitizeEvent keeps hands only on a reveal, which is public by definition.
 * A CARDS_DEALT event legitimately carries the recipient's own hand, but the
 * view already delivers that, so the event's copy is not needed and is
 * dropped rather than trusted.
 */
export function sanitizeEvent(event: GameEvent): GameEvent {
  if (event.type === "CARDS_REVEALED") return event;
  if (!event.hands) return event;
  return { ...event, hands: null };
}

// ---------------------------------------------------------------- actions

export type StoreAction =
  | { kind: "status"; status: ConnectionStatus; detail?: string }
  | { kind: "identity"; roomCode: string; playerId: string }
  | { kind: "view"; view: PlayerView; source: ViewSource }
  | { kind: "event"; event: GameEvent }
  | { kind: "claimSpace"; payload: ClaimSpacePayload }
  | { kind: "error"; payload: ErrorPayload }
  | { kind: "closed"; payload: ClosedPayload }
  | { kind: "reset" };

export function reduce(state: GameState, action: StoreAction): GameState {
  switch (action.kind) {
    case "status":
      return { ...state, status: action.status, statusDetail: action.detail };

    case "identity":
      return { ...state, roomCode: action.roomCode, playerId: action.playerId };

    case "view": {
      const next = sanitizeView(action.view);
      // Views can arrive out of order across a reconnect, so an older one
      // never replaces a newer one. A snapshot is authoritative regardless,
      // because the protocol sends it exactly when continuity was lost.
      if (
        state.view &&
        action.source === "view" &&
        next.seq < state.view.seq &&
        next.round <= state.view.round
      ) {
        return state;
      }
      return { ...state, view: next, lastError: undefined };
    }

    case "event": {
      const event = sanitizeEvent(action.event);
      const entry = describe(event, state.view);
      if (!entry) return state;
      // A replayed event after a resume must not be logged twice.
      if (state.activity.some((a) => a.seq === entry.seq)) return state;
      const activity = [...state.activity, entry]
        .sort((a, b) => a.seq - b.seq)
        .slice(-ACTIVITY_LIMIT);
      return { ...state, activity };
    }

    case "claimSpace":
      return {
        ...state,
        ladder: action.payload.claims,
        ladderSize: action.payload.size,
      };

    case "error":
      return { ...state, lastError: action.payload };

    case "closed":
      return { ...state, closedReason: action.payload.reason };

    case "reset":
      return { ...initialState };
  }
}

/** describe renders one event as a history line. Presentation only. */
function describe(event: GameEvent, view: PlayerView | undefined): ActivityEntry | null {
  const who = (id?: string) => nameOf(view, id);
  let text: string | null = null;
  switch (event.type) {
    case "PLAYER_JOINED":
      text = `${who(event.player)} joined`;
      break;
    case "GAME_STARTED":
      text = "Game started";
      break;
    case "ROUND_STARTED":
      text = `Round ${event.round} \u2014 ${who(event.player)} starts`;
      break;
    case "CLAIM_MADE":
      text = `${who(event.player)} claimed`;
      break;
    case "SHOW_CALLED":
      text = `${who(event.player)} called SHOW`;
      break;
    case "CLAIM_VALIDATED":
      text = event.result
        ? `Claim was ${event.result.claimTrue ? "TRUE" : "FALSE"} \u2014 ${who(
            event.result.loser,
          )} loses`
        : "Claim resolved";
      break;
    case "PENALTY_APPLIED":
      text = `${who(event.player)} now holds ${event.handSize} card${
        event.handSize === 1 ? "" : "s"
      }`;
      break;
    case "PLAYER_ELIMINATED":
      text = `${who(event.player)} was eliminated`;
      break;
    case "TURN_TIMED_OUT":
      text = `${who(event.player)} ran out of time`;
      break;
    case "PLAYER_DISCONNECTED":
      text = `${who(event.player)} disconnected`;
      break;
    case "PLAYER_RECONNECTED":
      text = `${who(event.player)} reconnected`;
      break;
    case "GAME_ENDED":
      text = event.player ? `${who(event.player)} wins` : "Game abandoned";
      break;
    default:
      return null;
  }
  return {
    seq: event.seq,
    type: event.type,
    round: event.round,
    player: event.player,
    text,
  };
}

// ---------------------------------------------------------------- selectors

export function nameOf(view: PlayerView | undefined, id?: string): string {
  if (!id) return "Someone";
  const player = view?.players.find((p) => p.id === id);
  if (!player) return "Someone";
  return player.isYou ? "You" : player.name || `Seat ${player.seat + 1}`;
}

/**
 * visibleCards is the only way a component gets cards for a player.
 *
 * It repeats the sanitiser's condition on purpose. Sanitising on ingest
 * stops bad data entering the store; gating here stops a component reading
 * cards it should not, whatever is in the store.
 */
export function visibleCards(
  view: PlayerView | undefined,
  player: PlayerSummary,
): string[] {
  if (!view) return [];
  if (!player.isYou && !view.revealed) return [];
  return player.cards ?? [];
}

export function you(view: PlayerView | undefined): PlayerSummary | undefined {
  return view?.players.find((p) => p.isYou);
}

/**
 * legalLadder is the set of claims the client may offer.
 *
 * It is an integer comparison against a server-assigned floor, not a rule:
 * the ladder is the server's own ascending list, and `standingOrdinal` is
 * the server's own floor. The client never decides which claim beats which.
 * The server revalidates every claim on arrival regardless.
 */
export function legalLadder(state: GameState): Claim[] {
  const view = state.view;
  if (!view || state.ladder.length === 0) return [];
  const from = view.standingOrdinal + 1;
  if (from <= 0) return state.ladder;
  if (from >= state.ladder.length) return [];
  return state.ladder.slice(from);
}

/** ordinalOf is the claim's index in the server's ascending ladder. */
export function ordinalOf(state: GameState, claim: Claim): number {
  return state.ladder.findIndex(
    (c) =>
      c.category === claim.category &&
      c.primary === claim.primary &&
      c.secondary === claim.secondary &&
      c.suit === claim.suit,
  );
}

export interface LadderGroup {
  category: Claim["category"];
  claims: Claim[];
}

/**
 * groupLadder groups claims by category, keeping the server's order.
 *
 * Category order and rank order are taken from the ladder's own sequence, so
 * the client holds no knowledge of which category or rank outranks which.
 */
export function groupLadder(claims: Claim[]): LadderGroup[] {
  const groups: LadderGroup[] = [];
  for (const claim of claims) {
    const last = groups[groups.length - 1];
    if (last && last.category === claim.category) {
      last.claims.push(claim);
      continue;
    }
    const existing = groups.find((g) => g.category === claim.category);
    if (existing) {
      existing.claims.push(claim);
      continue;
    }
    groups.push({ category: claim.category, claims: [claim] });
  }
  return groups;
}

export function isAbandoned(view: PlayerView | undefined): boolean {
  return view?.phase === "GAME_OVER" && !view.winner;
}

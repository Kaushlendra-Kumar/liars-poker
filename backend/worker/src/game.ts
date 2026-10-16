// The deterministic Liar's Poker engine. Port of backend/internal/game.
//
// apply(state, action) -> { state, events } is a pure reducer: no I/O, no
// clock (time arrives on the action) and no ambient randomness (a seed
// arrives in the config). State is plain JSON so a Durable Object can persist
// it as-is. A rejected action throws GameError and changes nothing.

import {
  type Card,
  type Claim,
  type EvalMode,
  Deck,
  DeckSize,
  canSatisfy,
  cardKey,
  claimLegal,
  claimString,
  claimsAbove,
  cardsString,
  handSize as evalHandSize,
  higher,
  minClaim,
  modeFor,
  ordinal,
  ownershipSatisfied,
  satisfies,
  seededRng,
  sortCards,
} from "./poker";

export const MinPlayers = 4;
export const MaxPlayers = 9;
export const StartingHandSize = 1;
export const MaxHandSize = 5;
export const EliminationHandSize = 6;
export const MaxPlayerIDLength = 64;

export type PlayerID = string;
export type Phase = "WAITING_FOR_PLAYERS" | "CLAIMING" | "ROUND_END" | "GAME_OVER";
export type PlayerStatus = "ACTIVE" | "DISCONNECTED" | "ELIMINATED";

// A disconnected player is still dealt cards and still owns pool cards; only
// elimination removes them.
export const inPlay = (s: PlayerStatus): boolean => s === "ACTIVE" || s === "DISCONNECTED";

export interface Player {
  id: PlayerID;
  name: string;
  seat: number;
  /** Cards dealt next round: the penalty level. */
  handSize: number;
  status: PlayerStatus;
  ready: boolean;
  /** Private. Leaves the engine only through view(). */
  hand: Card[];
}

export type TimeoutPolicy = "AUTO_SHOW" | "MINIMUM_CLAIM" | "FORFEIT_ROUND";
export type DisconnectPolicy = "ELIMINATE_AT_ROUND_END" | "ELIMINATE_IMMEDIATELY";
export type FirstStarterRule = "RANDOM" | "SEAT_ZERO";

export interface Config {
  turnDurationMs: number;
  roundEndDurationMs: number;
  timeout: TimeoutPolicy;
  disconnect: DisconnectPolicy;
  firstStarter: FirstStarterRule;
  /** 32-bit seed; each round derives its shuffle from (seed, round). */
  seed: number;
}

export function defaultConfig(seed: number): Config {
  return {
    turnDurationMs: 60_000,
    roundEndDurationMs: 6_000,
    timeout: "AUTO_SHOW",
    disconnect: "ELIMINATE_AT_ROUND_END",
    firstStarter: "RANDOM",
    seed: seed >>> 0,
  };
}

export interface ClaimRecord {
  seat: number;
  playerId: PlayerID;
  claim: Claim;
}

export interface ShowResult {
  round: number;
  claimant: PlayerID;
  challenger: PlayerID;
  claim: Claim;
  claimTrue: boolean;
  witness: Card[];
  loser: PlayerID;
  mode: EvalMode;
  poolSize: number;
  handSize: number;
}

export interface State {
  phase: Phase;
  config: Config;
  players: Player[]; // indexed by seat
  round: number;
  starterSeat: number;
  turnSeat: number;
  claims: ClaimRecord[];
  /** Every dealt card, frozen at deal time for the rest of the round. */
  pool: Card[];
  /** cardKey -> owner */
  owner: Record<number, PlayerID>;
  deckRemaining: number;
  revealed: boolean;
  lastShow: ShowResult | null;
  winner: PlayerID;
  seq: number;
  /** Epoch ms; 0 means no deadline. */
  turnDeadline: number;
  roundEndDeadline: number;
}

export function newGame(config: Config): State {
  return {
    phase: "WAITING_FOR_PLAYERS",
    config,
    players: [],
    round: 0,
    starterSeat: 0,
    turnSeat: 0,
    claims: [],
    pool: [],
    owner: {},
    deckRemaining: 0,
    revealed: false,
    lastShow: null,
    winner: "",
    seq: 0,
    turnDeadline: 0,
    roundEndDeadline: 0,
  };
}

// ---------------------------------------------------------------- lookups

export function seatOf(s: State, id: PlayerID): number {
  return s.players.findIndex((p) => p.id === id);
}

export const inPlayCount = (s: State): number => s.players.filter((p) => inPlay(p.status)).length;

// nextInPlaySeat: seats are fixed for the game, so eliminated seats are
// skipped but keep their index, which keeps rotation independent of who was
// eliminated when.
export function nextInPlaySeat(s: State, from: number): number {
  const n = s.players.length;
  for (let i = 1; i <= n; i++) {
    const seat = (from + i) % n;
    if (inPlay(s.players[seat]!.status)) return seat;
  }
  return -1;
}

export const standingClaim = (s: State): ClaimRecord | undefined => s.claims[s.claims.length - 1];

export function canShow(s: State, id: PlayerID): boolean {
  if (s.phase !== "CLAIMING" || s.claims.length === 0) return false;
  const seat = seatOf(s, id);
  return seat >= 0 && seat === s.turnSeat && inPlay(s.players[seat]!.status);
}

export function canClaim(s: State, id: PlayerID): boolean {
  if (s.phase !== "CLAIMING") return false;
  const seat = seatOf(s, id);
  if (seat < 0 || seat !== s.turnSeat || !inPlay(s.players[seat]!.status)) return false;
  const standing = standingClaim(s);
  return standing ? claimsAbove(standing.claim).length > 0 : true;
}

// ---------------------------------------------------------------- actions

interface Meta {
  playerId: PlayerID;
  /** Epoch ms supplied by the caller; 0 disables timers. */
  now: number;
}

export type Action =
  | (Meta & { type: "JOIN"; name: string })
  | (Meta & { type: "SET_READY"; ready: boolean })
  | (Meta & { type: "START_GAME" })
  | (Meta & { type: "MAKE_CLAIM"; claim: Claim })
  | (Meta & { type: "CALL_SHOW" })
  | (Meta & { type: "ADVANCE_ROUND" })
  | (Meta & { type: "DISCONNECT" })
  | (Meta & { type: "RECONNECT" })
  | (Meta & { type: "TICK" });

// ---------------------------------------------------------------- events

export type EventType =
  | "PLAYER_JOINED" | "PLAYER_READY" | "GAME_STARTED" | "ROUND_STARTED"
  | "CARDS_DEALT" | "TURN_STARTED" | "CLAIM_MADE" | "SHOW_CALLED"
  | "CARDS_REVEALED" | "CLAIM_VALIDATED" | "PENALTY_APPLIED"
  | "PLAYER_ELIMINATED" | "ROUND_ENDED" | "GAME_ENDED"
  | "PLAYER_DISCONNECTED" | "PLAYER_RECONNECTED" | "TURN_TIMED_OUT";

// hands is populated only for CARDS_DEALT (must be projected per recipient)
// and CARDS_REVEALED (public).
export interface GameEvent {
  seq: number;
  type: EventType;
  round: number;
  player: PlayerID;
  seat: number;
  claim: Claim | null;
  hands: Record<PlayerID, Card[]> | null;
  result: ShowResult | null;
  handSize: number;
  attemptedHandSize: number;
  reason: string;
  deadline: number;
  poolSize: number;
  mode: EvalMode;
}

type EventInit = Partial<Omit<GameEvent, "seq" | "round">> & { type: EventType };

// ---------------------------------------------------------------- errors

// The code is the stable wire code, so the protocol layer needs no mapping.
export class GameError extends Error {
  constructor(readonly code: string, message: string) {
    super(message);
  }
}

export const ErrWrongPhase = () => new GameError("ERR_WRONG_PHASE", "action not legal in this phase");
const ErrUnknownPlayer = () => new GameError("ERR_UNKNOWN_PLAYER", "unknown player");
const ErrInvalidPlayerID = () => new GameError("ERR_INVALID_PLAYER_ID", "invalid player id");
const ErrNotYourTurn = () => new GameError("ERR_NOT_YOUR_TURN", "not your turn");
const ErrNotShowEligible = () => new GameError("ERR_NOT_SHOW_ELIGIBLE", "only the immediate next player may call SHOW");
const ErrNoStandingClaim = () => new GameError("ERR_NO_STANDING_CLAIM", "no standing claim to challenge");
const ErrClaimNotHigher = () => new GameError("ERR_CLAIM_NOT_HIGHER", "claim must be strictly higher");
const ErrMalformedClaim = () => new GameError("ERR_MALFORMED_CLAIM", "claim is not well-formed");
const ErrPlayerNotActive = () => new GameError("ERR_PLAYER_ELIMINATED", "player is eliminated");
const ErrGameOver = () => new GameError("ERR_GAME_OVER", "the game is over");
const ErrNotEnoughPlayers = () => new GameError("ERR_NOT_ENOUGH_PLAYERS", "not enough players");
const ErrTooManyPlayers = () => new GameError("ERR_TABLE_FULL", "table is full");
const ErrGameInProgress = () => new GameError("ERR_GAME_IN_PROGRESS", "game already in progress");
const ErrAlreadyJoined = () => new GameError("ERR_ALREADY_JOINED", "already joined");
const ErrNotAllReady = () => new GameError("ERR_NOT_ALL_READY", "not all players are ready");
export const ErrUnknownAction = () => new GameError("ERR_UNKNOWN_ACTION", "unknown action");

// ---------------------------------------------------------------- reducer

type Emit = (e: EventInit) => void;

export function apply(s: State, a: Action): { state: State; events: GameEvent[] } {
  if (
    s.phase === "GAME_OVER" &&
    (a.type === "MAKE_CLAIM" || a.type === "CALL_SHOW" || a.type === "ADVANCE_ROUND" || a.type === "START_GAME")
  ) {
    throw ErrGameOver();
  }
  const ns: State = structuredClone(s);
  const events: GameEvent[] = [];
  const emit: Emit = (e) => {
    ns.seq++;
    events.push({
      player: "",
      seat: 0,
      claim: null,
      hands: null,
      result: null,
      handSize: 0,
      attemptedHandSize: 0,
      reason: "",
      deadline: 0,
      poolSize: 0,
      mode: "EXACT_FIVE",
      ...e,
      seq: ns.seq,
      round: ns.round,
    });
  };

  switch (a.type) {
    case "JOIN":
      applyJoin(ns, a, emit);
      break;
    case "SET_READY":
      applySetReady(ns, a, emit);
      break;
    case "START_GAME":
      applyStartGame(ns, a, emit);
      break;
    case "MAKE_CLAIM":
      applyMakeClaim(ns, a, emit);
      break;
    case "CALL_SHOW":
      applyCallShow(ns, a, emit);
      break;
    case "ADVANCE_ROUND":
      applyAdvanceRound(ns, a.now, emit);
      break;
    case "DISCONNECT":
      applyDisconnect(ns, a, emit);
      break;
    case "RECONNECT":
      applyReconnect(ns, a, emit);
      break;
    case "TICK":
      applyTick(ns, a.now, emit);
      break;
    default:
      throw ErrUnknownAction();
  }
  return { state: ns, events };
}

// ---------------------------------------------------------------- lobby

function applyJoin(ns: State, a: Extract<Action, { type: "JOIN" }>, emit: Emit) {
  if (ns.phase !== "WAITING_FOR_PLAYERS") throw ErrGameInProgress();
  if (!a.playerId || a.playerId.length > MaxPlayerIDLength) throw ErrInvalidPlayerID();
  if (seatOf(ns, a.playerId) >= 0) throw ErrAlreadyJoined();
  if (ns.players.length >= MaxPlayers) throw ErrTooManyPlayers();
  const seat = ns.players.length;
  ns.players.push({
    id: a.playerId,
    name: a.name,
    seat,
    handSize: StartingHandSize,
    status: "ACTIVE",
    ready: false,
    hand: [],
  });
  emit({ type: "PLAYER_JOINED", player: a.playerId, seat });
}

function applySetReady(ns: State, a: Extract<Action, { type: "SET_READY" }>, emit: Emit) {
  if (ns.phase !== "WAITING_FOR_PLAYERS") throw ErrGameInProgress();
  const seat = seatOf(ns, a.playerId);
  if (seat < 0) throw ErrUnknownPlayer();
  ns.players[seat]!.ready = a.ready;
  emit({ type: "PLAYER_READY", player: a.playerId, seat });
}

function applyStartGame(ns: State, a: Extract<Action, { type: "START_GAME" }>, emit: Emit) {
  if (ns.phase !== "WAITING_FOR_PLAYERS") throw ErrGameInProgress();
  if (seatOf(ns, a.playerId) < 0) throw ErrUnknownPlayer();
  if (ns.players.length < MinPlayers) throw ErrNotEnoughPlayers();
  if (ns.players.length > MaxPlayers) throw ErrTooManyPlayers();
  if (!ns.players.every((p) => p.ready)) throw ErrNotAllReady();
  ns.starterSeat =
    ns.config.firstStarter === "SEAT_ZERO" ? 0 : seededRng((ns.config.seed ^ 0x5eed5eed) >>> 0).intn(ns.players.length);
  emit({ type: "GAME_STARTED", seat: ns.starterSeat, player: ns.players[ns.starterSeat]!.id });
  beginRound(ns, a.now, emit);
}

// ---------------------------------------------------------------- play

function applyMakeClaim(ns: State, a: Extract<Action, { type: "MAKE_CLAIM" }>, emit: Emit) {
  if (ns.phase !== "CLAIMING") throw ErrWrongPhase();
  const seat = seatOf(ns, a.playerId);
  if (seat < 0) throw ErrUnknownPlayer();
  if (!inPlay(ns.players[seat]!.status)) throw ErrPlayerNotActive();
  if (seat !== ns.turnSeat) throw ErrNotYourTurn();
  if (!claimLegal(a.claim)) throw ErrMalformedClaim();
  const standing = standingClaim(ns);
  if (standing && !higher(a.claim, standing.claim)) throw ErrClaimNotHigher();
  submitClaim(ns, seat, a.claim, a.now, emit);
}

function submitClaim(ns: State, seat: number, c: Claim, now: number, emit: Emit) {
  const id = ns.players[seat]!.id;
  ns.claims.push({ seat, playerId: id, claim: { ...c } });
  emit({ type: "CLAIM_MADE", player: id, seat, claim: { ...c } });
  ns.turnSeat = nextInPlaySeat(ns, seat);
  setTurnDeadline(ns, now);
  emitTurnStarted(ns, emit);
}

function applyCallShow(ns: State, a: Extract<Action, { type: "CALL_SHOW" }>, emit: Emit) {
  if (ns.phase !== "CLAIMING") throw ErrWrongPhase();
  const seat = seatOf(ns, a.playerId);
  if (seat < 0) throw ErrUnknownPlayer();
  if (!inPlay(ns.players[seat]!.status)) throw ErrPlayerNotActive();
  if (ns.claims.length === 0) throw ErrNoStandingClaim();
  // Only the immediate next player is ever the turn holder.
  if (seat !== ns.turnSeat) throw ErrNotShowEligible();
  resolveShow(ns, seat, a.now, emit);
}

// resolveShow reveals every in-play hand, validates the standing claim
// exactly, assigns the penalty and ends the round.
function resolveShow(ns: State, challengerSeat: number, now: number, emit: Emit) {
  const standing = standingClaim(ns);
  if (!standing) throw ErrNoStandingClaim();
  const claimant = ns.players[standing.seat]!.id;
  const challenger = ns.players[challengerSeat]!.id;

  emit({ type: "SHOW_CALLED", player: challenger, seat: challengerSeat, claim: { ...standing.claim } });
  ns.revealed = true;
  emit({ type: "CARDS_REVEALED", hands: handsSnapshot(ns), poolSize: ns.pool.length, mode: modeFor(ns.pool.length) });

  const res = canSatisfy(ns.pool, (c) => ns.owner[cardKey(c)] === claimant, standing.claim);
  const loser = res.satisfied ? challenger : claimant;
  const result: ShowResult = {
    round: ns.round,
    claimant,
    challenger,
    claim: { ...standing.claim },
    claimTrue: res.satisfied,
    witness: res.witness,
    loser,
    mode: res.mode,
    poolSize: res.poolSize,
    handSize: res.handSize,
  };
  ns.lastShow = result;
  emit({ type: "CLAIM_VALIDATED", player: claimant, claim: { ...standing.claim }, result: structuredClone(result) });

  applyPenalty(ns, loser, emit);
  emit({ type: "ROUND_ENDED", player: loser });

  if (inPlayCount(ns) <= 1) return endGame(ns, emit);
  ns.phase = "ROUND_END";
  ns.turnDeadline = 0;
  if (now > 0 && ns.config.roundEndDurationMs > 0) ns.roundEndDeadline = now + ns.config.roundEndDurationMs;
}

// applyPenalty gives the loser one more card next round, or eliminates them
// if that would reach six.
function applyPenalty(ns: State, loser: PlayerID, emit: Emit) {
  const seat = seatOf(ns, loser);
  if (seat < 0) return;
  const p = ns.players[seat]!;
  if (p.handSize + 1 >= EliminationHandSize) {
    p.status = "ELIMINATED";
    emit({
      type: "PLAYER_ELIMINATED",
      player: p.id,
      seat,
      handSize: p.handSize,
      attemptedHandSize: EliminationHandSize,
      reason: "SIX_CARDS",
    });
    return;
  }
  p.handSize++;
  emit({ type: "PENALTY_APPLIED", player: p.id, seat, handSize: p.handSize });
}

// endGame names a winner when exactly one player remains; zero remaining is
// an abandoned game with no winner.
function endGame(ns: State, emit: Emit) {
  ns.phase = "GAME_OVER";
  ns.turnDeadline = 0;
  ns.roundEndDeadline = 0;
  let reason = "ABANDONED";
  if (inPlayCount(ns) === 1) {
    ns.winner = ns.players.find((p) => inPlay(p.status))!.id;
    reason = "WINNER";
  }
  emit({ type: "GAME_ENDED", player: ns.winner, reason });
}

function applyAdvanceRound(ns: State, now: number, emit: Emit) {
  if (ns.phase !== "ROUND_END") throw ErrWrongPhase();
  pruneDisconnected(ns, emit);
  if (inPlayCount(ns) <= 1) return endGame(ns, emit);
  beginRound(ns, now, emit);
}

// ---------------------------------------------------------------- rounds

// beginRound reshuffles all 52, deals each in-play player their hand size,
// freezes the pool and opens claiming. From round two the starter is the
// next in-play seat clockwise from the previous starter, and nothing else.
function beginRound(ns: State, now: number, emit: Emit) {
  if (ns.round > 0) ns.starterSeat = nextInPlaySeat(ns, ns.starterSeat);
  if (ns.starterSeat < 0) throw new Error("game: no in-play seat to start round");
  ns.round++;
  ns.claims = [];
  ns.revealed = false;
  ns.lastShow = null;
  ns.pool = [];
  ns.owner = {};
  for (const p of ns.players) p.hand = [];

  const deck = new Deck(seededRng((Math.imul(ns.config.seed, 1000003) + ns.round) >>> 0));
  const hands: Record<PlayerID, Card[]> = {};
  for (const p of ns.players) {
    if (!inPlay(p.status)) continue;
    const cards = deck.deal(p.handSize);
    sortCards(cards);
    p.hand = cards;
    hands[p.id] = structuredClone(cards);
    for (const c of cards) ns.owner[cardKey(c)] = p.id;
    ns.pool.push(...structuredClone(cards));
  }
  sortCards(ns.pool);
  ns.deckRemaining = deck.remaining();

  ns.phase = "CLAIMING";
  ns.turnSeat = ns.starterSeat;
  ns.roundEndDeadline = 0;
  setTurnDeadline(ns, now);

  const poolSize = ns.pool.length;
  const mode = modeFor(poolSize);
  emit({ type: "ROUND_STARTED", player: ns.players[ns.starterSeat]!.id, seat: ns.starterSeat, poolSize, mode });
  emit({ type: "CARDS_DEALT", hands, poolSize, mode });
  emitTurnStarted(ns, emit);
}

function handsSnapshot(s: State): Record<PlayerID, Card[]> {
  const out: Record<PlayerID, Card[]> = {};
  for (const p of s.players) if (inPlay(p.status)) out[p.id] = structuredClone(p.hand);
  return out;
}

function setTurnDeadline(ns: State, now: number) {
  ns.turnDeadline = now > 0 && ns.config.turnDurationMs > 0 ? now + ns.config.turnDurationMs : 0;
}

function emitTurnStarted(ns: State, emit: Emit) {
  if (ns.turnSeat < 0 || ns.turnSeat >= ns.players.length) return;
  emit({
    type: "TURN_STARTED",
    player: ns.players[ns.turnSeat]!.id,
    seat: ns.turnSeat,
    deadline: ns.turnDeadline,
    poolSize: ns.pool.length,
    mode: modeFor(ns.pool.length),
  });
}

// ---------------------------------------------------------------- presence

function applyDisconnect(ns: State, a: Extract<Action, { type: "DISCONNECT" }>, emit: Emit) {
  const seat = seatOf(ns, a.playerId);
  if (seat < 0) throw ErrUnknownPlayer();
  const p = ns.players[seat]!;
  if (p.status === "ELIMINATED") return;
  p.status = "DISCONNECTED";
  emit({ type: "PLAYER_DISCONNECTED", player: p.id, seat });

  // Mid-round the pool is frozen and the chain is live, so immediate
  // elimination is deferred to round end even under that policy.
  if (ns.config.disconnect === "ELIMINATE_IMMEDIATELY" && ns.phase !== "CLAIMING") {
    p.status = "ELIMINATED";
    emit({ type: "PLAYER_ELIMINATED", player: p.id, seat, reason: "DISCONNECT" });
    if (ns.phase !== "WAITING_FOR_PLAYERS" && inPlayCount(ns) <= 1) endGame(ns, emit);
  }
}

function applyReconnect(ns: State, a: Extract<Action, { type: "RECONNECT" }>, emit: Emit) {
  const seat = seatOf(ns, a.playerId);
  if (seat < 0) throw ErrUnknownPlayer();
  const p = ns.players[seat]!;
  if (p.status !== "DISCONNECTED") return;
  p.status = "ACTIVE";
  emit({ type: "PLAYER_RECONNECTED", player: p.id, seat });
}

// pruneDisconnected eliminates players still disconnected at round end,
// under every policy, so nobody stays in play as a ghost.
function pruneDisconnected(ns: State, emit: Emit) {
  ns.players.forEach((p, i) => {
    if (p.status === "DISCONNECTED") {
      p.status = "ELIMINATED";
      emit({ type: "PLAYER_ELIMINATED", player: p.id, seat: i, reason: "DISCONNECT" });
    }
  });
}

// ---------------------------------------------------------------- timers

function applyTick(ns: State, now: number, emit: Emit) {
  if (now <= 0) return;
  if (ns.phase === "CLAIMING") {
    if (ns.turnDeadline === 0 || now < ns.turnDeadline) return;
    applyTimeout(ns, now, emit);
  } else if (ns.phase === "ROUND_END") {
    if (ns.roundEndDeadline === 0 || now < ns.roundEndDeadline) return;
    applyAdvanceRound(ns, now, emit);
  }
}

function applyTimeout(ns: State, now: number, emit: Emit) {
  const seat = ns.turnSeat;
  if (seat < 0 || seat >= ns.players.length) return;
  const player = ns.players[seat]!.id;
  emit({ type: "TURN_TIMED_OUT", player, seat, reason: ns.config.timeout === "AUTO_SHOW" ? "AUTO_SHOW" : ns.config.timeout === "MINIMUM_CLAIM" ? "AUTO_MINIMUM_CLAIM" : "FORFEIT_ROUND" });

  switch (ns.config.timeout) {
    case "FORFEIT_ROUND":
      applyPenalty(ns, player, emit);
      emit({ type: "ROUND_ENDED", player });
      if (inPlayCount(ns) <= 1) return endGame(ns, emit);
      ns.phase = "ROUND_END";
      ns.turnDeadline = 0;
      if (ns.config.roundEndDurationMs > 0) ns.roundEndDeadline = now + ns.config.roundEndDurationMs;
      return;
    case "MINIMUM_CLAIM":
      return timeoutClaim(ns, seat, now, emit);
    default:
      if (standingClaim(ns)) return resolveShow(ns, seat, now, emit);
      return timeoutClaim(ns, seat, now, emit);
  }
}

// timeoutClaim submits the lowest legal claim; if none exists the standing
// claim is the maximum and SHOW is taken instead.
function timeoutClaim(ns: State, seat: number, now: number, emit: Emit) {
  const standing = standingClaim(ns);
  if (standing) {
    const above = claimsAbove(standing.claim);
    if (above.length === 0) return resolveShow(ns, seat, now, emit);
    return submitClaim(ns, seat, above[0]!, now, emit);
  }
  submitClaim(ns, seat, minClaim(), now, emit);
}

// ---------------------------------------------------------------- view

export interface PlayerSummary {
  id: PlayerID;
  name: string;
  seat: number;
  handSize: number;
  cardCount: number;
  status: PlayerStatus;
  ready: boolean;
  isYou: boolean;
  isTurn: boolean;
  isStarter: boolean;
  /** Present only for the viewer, or for everyone once revealed. */
  cards?: Card[];
}

export interface ClaimView {
  player: PlayerID;
  seat: number;
  claim: Claim;
  text: string;
  ordinal: number;
}

export interface PlayerView {
  you: PlayerID;
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
  turnPlayer?: PlayerID;
  turnSeat: number;
  starterSeat: number;
  yourTurn: boolean;
  canClaim: boolean;
  canShow: boolean;
  turnDeadline?: number;
  roundEndDeadline?: number;
  revealed: boolean;
  lastShow?: ShowResult;
  winner?: PlayerID;
}

// view is the only function that returns game state to a client.
export function view(s: State, viewer: PlayerID): PlayerView {
  const standing = standingClaim(s);
  const v: PlayerView = {
    you: viewer,
    phase: s.phase,
    round: s.round,
    seq: s.seq,
    players: [],
    poolSize: s.pool.length,
    evalMode: modeFor(s.pool.length),
    handWidth: evalHandSize(s.pool.length),
    claims: [],
    standingOrdinal: -1,
    turnSeat: s.turnSeat,
    starterSeat: s.starterSeat,
    yourTurn: false,
    canClaim: canClaim(s, viewer),
    canShow: canShow(s, viewer),
    revealed: s.revealed,
  };
  if (standing) {
    v.standingClaim = structuredClone(standing.claim);
    v.standingOrdinal = ordinal(standing.claim);
  }
  // Only CLAIMING has a turn holder.
  if (s.phase === "CLAIMING" && s.turnSeat >= 0 && s.turnSeat < s.players.length) {
    v.turnPlayer = s.players[s.turnSeat]!.id;
    v.yourTurn = v.turnPlayer === viewer;
  }
  if (s.turnDeadline) v.turnDeadline = s.turnDeadline;
  if (s.roundEndDeadline) v.roundEndDeadline = s.roundEndDeadline;
  if (s.winner) v.winner = s.winner;

  for (const p of s.players) {
    const sum: PlayerSummary = {
      id: p.id,
      name: p.name,
      seat: p.seat,
      handSize: p.handSize,
      cardCount: p.hand.length,
      status: p.status,
      ready: p.ready,
      isYou: p.id === viewer,
      isTurn: s.phase === "CLAIMING" && p.seat === s.turnSeat,
      isStarter: p.seat === s.starterSeat,
    };
    // The whole hidden-information rule, in one condition.
    if ((p.id === viewer || s.revealed) && p.hand.length > 0) sum.cards = structuredClone(p.hand);
    v.players.push(sum);
  }
  for (const cr of s.claims) {
    v.claims.push({
      player: cr.playerId,
      seat: cr.seat,
      claim: structuredClone(cr.claim),
      text: claimString(cr.claim),
      ordinal: ordinal(cr.claim),
    });
  }
  if (s.lastShow) v.lastShow = structuredClone(s.lastShow);
  return v;
}

// projectEvent returns the copy of an event one viewer may see. CARDS_DEALT
// carries every hand, so it is reduced to the viewer's own; CARDS_REVEALED is
// public and passes through.
export function projectEvent(e: GameEvent, viewer: PlayerID): GameEvent {
  const out = structuredClone(e);
  if (!out.hands || e.type === "CARDS_REVEALED") return out;
  const mine = out.hands[viewer];
  out.hands = mine ? { [viewer]: mine } : {};
  return out;
}

// ---------------------------------------------------------------- invariants

// checkInvariants asserts the engine invariants. Tests call it after every
// apply during simulation.
export function checkInvariants(s: State): string | null {
  const seen = new Set<number>();
  for (const p of s.players)
    for (const c of p.hand) {
      if (seen.has(cardKey(c))) return `I1: card held twice`;
      seen.add(cardKey(c));
    }
  if (s.phase === "CLAIMING" || s.phase === "ROUND_END") {
    if (s.pool.length + s.deckRemaining !== DeckSize) return `I2: pool + deck != ${DeckSize}`;
    const held = s.players.reduce((n, p) => n + p.hand.length, 0);
    if (held !== s.pool.length) return `I2: hands hold ${held} but pool has ${s.pool.length}`;
    for (const c of s.pool) {
      const owner = s.owner[cardKey(c)];
      const p = s.players.find((x) => x.id === owner);
      if (!p || !p.hand.some((h) => cardKey(h) === cardKey(c))) return `I2: pool card not in owner's hand`;
    }
  }
  for (const p of s.players) {
    if (!inPlay(p.status)) continue;
    if (p.handSize < StartingHandSize || p.handSize > MaxHandSize) return `I3: ${p.id} has hand size ${p.handSize}`;
    if (s.phase === "CLAIMING" && p.hand.length !== p.handSize) return `I3: ${p.id} dealt wrong count`;
  }
  if (s.phase === "CLAIMING") {
    const tp = s.players[s.turnSeat];
    if (!tp || !inPlay(tp.status)) return `I4: turn seat ${s.turnSeat} invalid`;
  }
  for (let i = 1; i < s.claims.length; i++) {
    if (!higher(s.claims[i]!.claim, s.claims[i - 1]!.claim)) return `I7: claim ${i} does not exceed previous`;
  }
  if (s.lastShow?.claimTrue) {
    const w = s.lastShow.witness;
    if (w.length !== s.lastShow.handSize) return `I8: witness width`;
    if (!satisfies(w, s.lastShow.claim)) return `I8: witness [${cardsString(w)}] does not realize claim`;
    const claimant = s.lastShow.claimant;
    if (!ownershipSatisfied(w, (c) => s.owner[cardKey(c)] === claimant)) return `I9: witness has no claimant card`;
  }
  const n = inPlayCount(s);
  if (s.phase === "GAME_OVER") {
    if (n > 1) return `I13: game over with ${n} in play`;
    if (n === 1 && !s.winner) return `I13: no winner named`;
    if (n === 0 && s.winner) return `I13: winner with nobody in play`;
  } else if (s.phase !== "WAITING_FOR_PLAYERS" && n < 2) {
    return `I13: ${s.phase} with ${n} in play`;
  }
  return null;
}

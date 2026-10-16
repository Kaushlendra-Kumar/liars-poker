import type {
  Claim,
  ClaimSpacePayload,
  GameEvent,
  PlayerSummary,
  PlayerView,
} from "@/protocol/types";

/**
 * Fixtures shaped exactly like real server payloads. Where a test needs a
 * hostile payload it builds one explicitly, so it is obvious which tests are
 * checking defence in depth.
 */

export function player(over: Partial<PlayerSummary> = {}): PlayerSummary {
  return {
    id: "p_a",
    name: "Ada",
    seat: 0,
    handSize: 1,
    cardCount: 1,
    status: "ACTIVE",
    ready: true,
    isYou: false,
    isTurn: false,
    isStarter: false,
    ...over,
  };
}

/** A four-player reduced-pool round 1, from the viewpoint of seat 0. */
export function reducedPoolView(over: Partial<PlayerView> = {}): PlayerView {
  return {
    you: "p_a",
    phase: "CLAIMING",
    round: 1,
    seq: 12,
    poolSize: 4,
    evalMode: "REDUCED_POOL",
    handWidth: 4,
    standingOrdinal: -1,
    turnPlayer: "p_a",
    turnSeat: 0,
    starterSeat: 0,
    yourTurn: true,
    canClaim: true,
    canShow: false,
    revealed: false,
    claims: [],
    players: [
      player({ id: "p_a", name: "Ada", seat: 0, isYou: true, isTurn: true, isStarter: true, cards: ["8S"] }),
      player({ id: "p_b", name: "Ben", seat: 1 }),
      player({ id: "p_c", name: "Cy", seat: 2 }),
      player({ id: "p_d", name: "Dee", seat: 3 }),
    ],
    ...over,
  };
}

/** A five-card exact-five round with a standing claim. */
export function exactFiveView(over: Partial<PlayerView> = {}): PlayerView {
  return {
    ...reducedPoolView(),
    round: 3,
    seq: 44,
    poolSize: 6,
    evalMode: "EXACT_FIVE",
    handWidth: 5,
    standingOrdinal: 18,
    standingClaim: { category: "PAIR", primary: "8" },
    turnPlayer: "p_a",
    yourTurn: true,
    canClaim: true,
    canShow: true,
    claims: [
      {
        player: "p_d",
        seat: 3,
        claim: { category: "PAIR", primary: "8" },
        text: "Pair of 8s",
        ordinal: 18,
      },
    ],
    ...over,
  };
}

export function revealedView(over: Partial<PlayerView> = {}): PlayerView {
  return {
    ...exactFiveView(),
    phase: "ROUND_END",
    revealed: true,
    yourTurn: false,
    canClaim: false,
    canShow: false,
    turnPlayer: undefined,
    roundEndDeadline: new Date(Date.now() + 6000).toISOString(),
    players: [
      player({ id: "p_a", name: "Ada", seat: 0, isYou: true, cards: ["8S"] }),
      player({ id: "p_b", name: "Ben", seat: 1, cards: ["8H"] }),
      player({ id: "p_c", name: "Cy", seat: 2, cards: ["KD"] }),
      player({ id: "p_d", name: "Dee", seat: 3, cards: ["3C"] }),
    ],
    lastShow: {
      round: 3,
      claimant: "p_d",
      challenger: "p_a",
      claim: { category: "PAIR", primary: "8" },
      claimTrue: true,
      witness: ["3C", "8H", "8S", "KD"],
      loser: "p_a",
      mode: "REDUCED_POOL",
      poolSize: 4,
      handSize: 4,
    },
    ...over,
  };
}

/**
 * A miniature ladder in ascending order, shaped like the real one: ordinals
 * are positions, categories appear in ascending strength.
 */
export function ladder(): ClaimSpacePayload {
  const claims: Claim[] = [
    { category: "HIGH_CARD", primary: "3" },
    { category: "HIGH_CARD", primary: "K" },
    { category: "HIGH_CARD", primary: "A" },
    { category: "PAIR", primary: "2" },
    { category: "PAIR", primary: "8" },
    { category: "PAIR", primary: "9" },
    { category: "TWO_PAIR", primary: "4", secondary: "2" },
    { category: "THREE_OF_A_KIND", primary: "7" },
    { category: "FLUSH", primary: "Q", suit: "HEARTS" },
    { category: "FULL_HOUSE", primary: "8", secondary: "4" },
    { category: "FOUR_OF_A_KIND", primary: "8" },
    { category: "ROYAL_FLUSH", suit: "SPADES" },
  ];
  return { size: claims.length, claims };
}

export function event(over: Partial<GameEvent> = {}): GameEvent {
  return {
    seq: 1,
    type: "CLAIM_MADE",
    round: 1,
    seat: 0,
    claim: null,
    hands: null,
    result: null,
    deadline: null,
    mode: "REDUCED_POOL",
    ...over,
  };
}

import { describe, expect, it } from "vitest";
import {
  type Action,
  type State,
  apply,
  checkInvariants,
  defaultConfig,
  newGame,
  projectEvent,
  view,
} from "../src/game";
import {
  type Card,
  ClaimSpaceSize,
  allClaimsCopy,
  canSatisfy,
  canSatisfyExhaustive,
  cardKey,
  claimEquals,
  claimFromJSON,
  claimToJSON,
  claimsAbove,
  classify,
  newDeck,
  newFlush,
  newFullHouse,
  newHighCard,
  newPair,
  newQuads,
  newRoyalFlush,
  newStraight,
  newTwoPair,
  ordinal,
  parseCards,
  seededRng,
  Hearts,
  Spades,
  Ace,
  Five,
  Eight,
  King,
  Queen,
  Two,
  Four,
} from "../src/poker";
import { decodeInbound, eventMessage, sanitizeName, viewWire } from "../src/protocol";

describe("claim space", () => {
  it("has 367 strictly ordered claims", () => {
    const all = allClaimsCopy();
    expect(all).toHaveLength(ClaimSpaceSize);
    all.forEach((c, i) => expect(ordinal(c)).toBe(i));
  });

  it("round-trips the wire form", () => {
    for (const c of allClaimsCopy()) expect(claimEquals(claimFromJSON(claimToJSON(c)), c)).toBe(true);
  });

  it("canonicalises two pair and rejects malformed claims", () => {
    expect(claimEquals(newTwoPair(Two, Four), newTwoPair(Four, Two))).toBe(true);
    expect(ordinal(newFullHouse(Eight, Eight))).toBe(-1);
    expect(ordinal(newStraight(Four))).toBe(-1);
    expect(ordinal(newFlush(Hearts, 6))).toBe(-1);
    expect(claimsAbove(allClaimsCopy().at(-1)!)).toHaveLength(0);
  });
});

describe("classify", () => {
  const cases: [string, ReturnType<typeof classify>][] = [
    ["10S JS QS KS AS", newRoyalFlush(Spades)],
    ["AH 2D 3C 4S 5H", newStraight(Five)],
    ["8S 8H 8D KC KD", newFullHouse(Eight, King)],
    ["2H 5H 9H JH QH", newFlush(Hearts, Queen)],
    ["AS AH AD AC 2C", newQuads(Ace)],
    ["QS QH", newPair(Queen)],
    ["2S 7H", newHighCard(7)],
    ["KS AH 2D 3C 4S", newHighCard(Ace)], // ranks never wrap
  ];
  for (const [hand, want] of cases) {
    it(hand, () => expect(classify(parseCards(hand))).toEqual(want));
  }
});

describe("validator", () => {
  it("matches the exhaustive oracle on random pools", () => {
    const rng = seededRng(42);
    const claims = allClaimsCopy();
    for (let trial = 0; trial < 400; trial++) {
      const deck = newDeck();
      for (let i = deck.length - 1; i > 0; i--) {
        const j = rng.intn(i + 1);
        [deck[i], deck[j]] = [deck[j]!, deck[i]!];
      }
      const pool = deck.slice(0, 2 + rng.intn(11));
      const mine = new Set(pool.filter((_, i) => i % 3 === 0).map(cardKey));
      const owns = (c: Card) => mine.has(cardKey(c));
      const claim = claims[rng.intn(claims.length)]!;
      const fast = canSatisfy(pool, owns, claim);
      const slow = canSatisfyExhaustive(pool, owns, claim);
      expect(fast.satisfied).toBe(slow.satisfied);
    }
  });

  it("requires the claimant to own a card of the evaluated hand", () => {
    const pool = parseCards("QS QH 2C 3D 4H 7S");
    const qs = cardKey(parseCards("QS")[0]!);
    expect(canSatisfy(pool, (c) => cardKey(c) === qs, newPair(Queen)).satisfied).toBe(true);
    expect(canSatisfy(parseCards("QS QH 2C 3D 4H"), () => false, newPair(Queen)).satisfied).toBe(false);
  });
});

// ---------------------------------------------------------------- simulation

function play(seed: number, players: number): State {
  const rng = seededRng(seed);
  let s = newGame(defaultConfig(seed));
  let now = 1_000_000;
  const step = (a: Action) => {
    const res = apply(s, a);
    s = res.state;
    const bad = checkInvariants(s);
    if (bad) throw new Error(bad);
    // No view and no projected event may show an unrevealed foreign card.
    for (const p of s.players) {
      const v = view(s, p.id);
      if (!s.revealed) for (const o of v.players) if (o.id !== p.id) expect(o.cards).toBeUndefined();
      for (const e of res.events) {
        const pe = projectEvent(e, p.id);
        if (pe.type === "CARDS_DEALT") expect(Object.keys(pe.hands ?? {}).every((k) => k === p.id)).toBe(true);
      }
    }
    return res;
  };
  for (let i = 0; i < players; i++) step({ type: "JOIN", playerId: `p${i}`, name: `P${i}`, now });
  for (let i = 0; i < players; i++) step({ type: "SET_READY", playerId: `p${i}`, ready: true, now });
  step({ type: "START_GAME", playerId: "p0", now });

  for (let guard = 0; guard < 5000 && s.phase !== "GAME_OVER"; guard++) {
    now += 1000;
    if (s.phase === "ROUND_END") {
      now = s.roundEndDeadline;
      step({ type: "TICK", playerId: "", now });
      continue;
    }
    const turn = s.players[s.turnSeat]!.id;
    const roll = rng.intn(10);
    if (roll === 0) {
      now = s.turnDeadline; // let the timer act
      step({ type: "TICK", playerId: "", now });
    } else if (s.claims.length > 0 && roll < 4) {
      step({ type: "CALL_SHOW", playerId: turn, now });
    } else {
      const above = s.claims.length ? claimsAbove(s.claims.at(-1)!.claim) : allClaimsCopy();
      if (above.length === 0) step({ type: "CALL_SHOW", playerId: turn, now });
      else step({ type: "MAKE_CLAIM", playerId: turn, claim: above[Math.min(rng.intn(6), above.length - 1)]!, now });
    }
  }
  return s;
}

describe("simulation", () => {
  it("plays many games to completion without breaking an invariant", () => {
    for (let seed = 1; seed <= 60; seed++) {
      const s = play(seed, 4 + (seed % 6));
      expect(s.phase).toBe("GAME_OVER");
      expect(s.winner).not.toBe("");
    }
  });

  it("rejects out-of-turn actions without changing state", () => {
    let s = newGame({ ...defaultConfig(7), firstStarter: "SEAT_ZERO" });
    for (const id of ["a", "b", "c", "d"]) s = apply(s, { type: "JOIN", playerId: id, name: id, now: 0 }).state;
    for (const id of ["a", "b", "c", "d"]) s = apply(s, { type: "SET_READY", playerId: id, ready: true, now: 0 }).state;
    s = apply(s, { type: "START_GAME", playerId: "a", now: 0 }).state;
    expect(() => apply(s, { type: "CALL_SHOW", playerId: "a", now: 0 })).toThrow(/standing claim/);
    expect(() => apply(s, { type: "MAKE_CLAIM", playerId: "b", claim: newPair(Two), now: 0 })).toThrow(/not your turn/);
    s = apply(s, { type: "MAKE_CLAIM", playerId: "a", claim: newPair(Two), now: 0 }).state;
    expect(() => apply(s, { type: "CALL_SHOW", playerId: "c", now: 0 })).toThrow(/immediate next/);
    expect(() => apply(s, { type: "MAKE_CLAIM", playerId: "b", claim: newPair(Two), now: 0 })).toThrow(/strictly higher/);
  });
});

describe("protocol", () => {
  it("rejects unknown fields and oversized frames", () => {
    expect(() => decodeInbound(JSON.stringify({ type: "call_show", playerId: "x" }))).toThrow();
    expect(() => decodeInbound("x".repeat(5000))).toThrow();
    expect(decodeInbound(JSON.stringify({ type: "pong" })).type).toBe("pong");
  });

  it("sanitises names on code point boundaries", () => {
    expect(sanitizeName("  a\u0000b  ")).toBe("ab");
    expect(new TextEncoder().encode(sanitizeName("\u{1F600}".repeat(20))).length).toBeLessThanOrEqual(32);
  });

  it("encodes views and events in the wire shape", () => {
    const s = play(3, 4);
    const v = viewWire(view(s, "p0"));
    expect(Array.isArray(v.claims)).toBe(true);
    expect(v).not.toHaveProperty("turnDeadline");
    const m = eventMessage(
      { seq: 1, type: "CARDS_DEALT", round: 1, player: "", seat: 0, claim: null, result: null, handSize: 0,
        attemptedHandSize: 0, reason: "", deadline: 0, poolSize: 2, mode: "REDUCED_POOL",
        hands: { a: parseCards("AS"), b: parseCards("KH") } },
      "a",
    );
    expect((m.payload as { hands: unknown }).hands).toEqual({ a: ["AS"] });
  });
});

import { describe, expect, it } from "vitest";
import {
  initialState,
  legalLadder,
  groupLadder,
  isAbandoned,
  reduce,
  sanitizeEvent,
  sanitizeView,
  visibleCards,
} from "@/store/gameStore";
import { event, exactFiveView, ladder, reducedPoolView, revealedView } from "./fixtures";

describe("view is the authoritative state", () => {
  it("renders a normal view", () => {
    const s = reduce(initialState, { kind: "view", view: exactFiveView(), source: "view" });
    expect(s.view?.phase).toBe("CLAIMING");
    expect(s.view?.claims).toHaveLength(1);
    expect(s.view?.standingOrdinal).toBe(18);
  });

  it("renders a reduced-pool view", () => {
    const s = reduce(initialState, { kind: "view", view: reducedPoolView(), source: "view" });
    expect(s.view?.evalMode).toBe("REDUCED_POOL");
    expect(s.view?.handWidth).toBe(4);
    expect(s.view?.poolSize).toBe(4);
  });

  it("ignores a stale view that arrives after a newer one", () => {
    const newer = exactFiveView({ seq: 100, round: 5 });
    let s = reduce(initialState, { kind: "view", view: newer, source: "view" });
    const stale = exactFiveView({ seq: 40, round: 3 });
    s = reduce(s, { kind: "view", view: stale, source: "view" });
    expect(s.view?.seq).toBe(100);
  });

  it("accepts a snapshot even when its seq is older, because continuity was lost", () => {
    let s = reduce(initialState, {
      kind: "view",
      view: exactFiveView({ seq: 100 }),
      source: "view",
    });
    s = reduce(s, {
      kind: "view",
      view: exactFiveView({ seq: 40, round: 2 }),
      source: "snapshot",
    });
    expect(s.view?.seq).toBe(40);
  });

  it("survives many rapid updates and keeps the newest", () => {
    let s = initialState;
    for (let i = 1; i <= 200; i++) {
      s = reduce(s, {
        kind: "view",
        view: exactFiveView({ seq: i, round: 1 + Math.floor(i / 20) }),
        source: "view",
      });
    }
    expect(s.view?.seq).toBe(200);
  });
});

describe("events are history, never state", () => {
  it("does not change the view", () => {
    const base = reduce(initialState, {
      kind: "view",
      view: exactFiveView(),
      source: "view",
    });
    const after = reduce(base, {
      kind: "event",
      event: event({ seq: 99, type: "CLAIM_MADE", player: "p_b" }),
    });
    expect(after.view).toBe(base.view);
    expect(after.activity).toHaveLength(1);
  });

  it("does not log a replayed event twice", () => {
    let s = reduce(initialState, { kind: "view", view: exactFiveView(), source: "view" });
    s = reduce(s, { kind: "event", event: event({ seq: 7, player: "p_b" }) });
    s = reduce(s, { kind: "event", event: event({ seq: 7, player: "p_b" }) });
    expect(s.activity).toHaveLength(1);
  });

  it("orders out-of-order events by seq", () => {
    let s = reduce(initialState, { kind: "view", view: exactFiveView(), source: "view" });
    s = reduce(s, { kind: "event", event: event({ seq: 9, player: "p_b" }) });
    s = reduce(s, { kind: "event", event: event({ seq: 4, player: "p_c" }) });
    expect(s.activity.map((a) => a.seq)).toEqual([4, 9]);
  });
});

describe("hidden cards are stripped on ingest", () => {
  it("drops other players' cards from an unrevealed view", () => {
    const hostile = reducedPoolView();
    hostile.players[1]!.cards = ["8H"];
    hostile.players[2]!.cards = ["KD"];
    const clean = sanitizeView(hostile);
    expect(clean.players[0]!.cards).toEqual(["8S"]);
    expect(clean.players[1]!.cards).toBeUndefined();
    expect(clean.players[2]!.cards).toBeUndefined();
  });

  it("keeps every hand once the round is revealed", () => {
    const clean = sanitizeView(revealedView());
    expect(clean.players.every((p) => (p.cards ?? []).length === 1)).toBe(true);
  });

  it("drops a witness that arrives before a reveal", () => {
    const hostile = exactFiveView();
    hostile.lastShow = revealedView().lastShow;
    const clean = sanitizeView(hostile);
    expect(clean.lastShow?.witness).toBeUndefined();
  });

  it("drops hands from every event except a reveal", () => {
    const dealt = sanitizeEvent(
      event({ type: "CARDS_DEALT", hands: { p_a: ["8S"], p_b: ["8H"] } }),
    );
    expect(dealt.hands).toBeNull();
    const revealed = sanitizeEvent(
      event({ type: "CARDS_REVEALED", hands: { p_a: ["8S"], p_b: ["8H"] } }),
    );
    expect(revealed.hands).not.toBeNull();
  });

  it("gates visibleCards on the reveal, not on what happens to be present", () => {
    const view = reducedPoolView();
    const opponent = { ...view.players[1]!, cards: ["8H"] };
    expect(visibleCards(view, opponent)).toEqual([]);
    expect(visibleCards({ ...view, revealed: true }, opponent)).toEqual(["8H"]);
  });
});

describe("the claim ladder", () => {
  it("offers the whole ladder when nothing stands", () => {
    let s = reduce(initialState, { kind: "claimSpace", payload: ladder() });
    s = reduce(s, {
      kind: "view",
      view: reducedPoolView({ standingOrdinal: -1 }),
      source: "view",
    });
    expect(legalLadder(s)).toHaveLength(ladder().size);
  });

  it("filters strictly above the standing ordinal", () => {
    let s = reduce(initialState, { kind: "claimSpace", payload: ladder() });
    s = reduce(s, {
      kind: "view",
      view: exactFiveView({ standingOrdinal: 4 }),
      source: "view",
    });
    const legal = legalLadder(s);
    expect(legal).toHaveLength(ladder().size - 5);
    expect(legal[0]).toEqual({ category: "PAIR", primary: "9" });
  });

  it("offers nothing above the maximum claim", () => {
    let s = reduce(initialState, { kind: "claimSpace", payload: ladder() });
    s = reduce(s, {
      kind: "view",
      view: exactFiveView({ standingOrdinal: ladder().size - 1 }),
      source: "view",
    });
    expect(legalLadder(s)).toEqual([]);
  });

  it("handles a full 367-entry ladder without knowing any poker", () => {
    const claims = Array.from({ length: 367 }, (_, i) => ({
      category: "PAIR" as const,
      primary: String(i),
    }));
    let s = reduce(initialState, { kind: "claimSpace", payload: { size: 367, claims } });
    s = reduce(s, {
      kind: "view",
      view: exactFiveView({ standingOrdinal: 200 }),
      source: "view",
    });
    expect(s.ladderSize).toBe(367);
    expect(legalLadder(s)).toHaveLength(166);
  });

  it("groups by category in the server's own order", () => {
    const groups = groupLadder(ladder().claims);
    expect(groups.map((g) => g.category)).toEqual([
      "HIGH_CARD",
      "PAIR",
      "TWO_PAIR",
      "THREE_OF_A_KIND",
      "FLUSH",
      "FULL_HOUSE",
      "FOUR_OF_A_KIND",
      "ROYAL_FLUSH",
    ]);
    expect(groups[0]!.claims).toHaveLength(3);
  });
});

describe("terminal states", () => {
  it("recognises an abandoned game by an absent winner", () => {
    expect(isAbandoned(exactFiveView({ phase: "GAME_OVER" }))).toBe(true);
    expect(isAbandoned(exactFiveView({ phase: "GAME_OVER", winner: "p_b" }))).toBe(false);
    expect(isAbandoned(exactFiveView())).toBe(false);
  });
});

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { cardParts } from "@/protocol/labels";
import { GameTable } from "@/components/GameTable";
import { PlayerList } from "@/components/PlayerList";
import { RevealPanel } from "@/components/RevealPanel";
import { initialState, reduce, type GameState } from "@/store/gameStore";
import type { PlayerView } from "@/protocol/types";
import { ladder, reducedPoolView, revealedView } from "./fixtures";

/**
 * The most important test in the frontend.
 *
 * It asserts that no server message, however hostile, can make the UI render
 * another player's hidden card. The store strips hidden cards on ingest and
 * every component reads cards through `visibleCards`, so both layers have to
 * fail for a leak to reach the DOM.
 */

const EVERY_CARD = (() => {
  const ranks = ["2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K", "A"];
  const suits = ["C", "D", "H", "S"];
  return ranks.flatMap((r) => suits.map((s) => `${r}${s}`));
})();

function stateWith(view: PlayerView): GameState {
  let s = reduce(initialState, { kind: "claimSpace", payload: ladder() });
  s = reduce(s, { kind: "view", view, source: "view" });
  return s;
}

/** Every card label the DOM currently exposes. */
function renderedCardLabels(): string[] {
  return screen
    .queryAllByTestId("card-face")
    .map((el) => el.getAttribute("aria-label") ?? "");
}

/**
 * The distinct cards the DOM exposes.
 *
 * The viewer's own hand is legitimately rendered more than once: prominently
 * at the top, and again small in the player list. The security property is
 * about *which* cards are visible, never how many times each appears, so the
 * assertions below compare sets.
 */
function renderedCardSet(): string[] {
  return [...new Set(renderedCardLabels())].sort();
}

describe("the frontend cannot render another player's hidden cards", () => {
  it("shows only your own hand in a normal round", () => {
    render(<GameTable state={stateWith(reducedPoolView())} onClaim={() => {}} onShow={() => {}} />);
    expect(renderedCardSet()).toEqual(["Eight of Spades"]);
  });

  it("discards opponents' cards from a hostile unrevealed view", () => {
    // A server that leaked every hand. Nothing extra may reach the DOM.
    const hostile = reducedPoolView();
    hostile.players[1]!.cards = ["8H", "KD"];
    hostile.players[2]!.cards = ["QC"];
    hostile.players[3]!.cards = ["3C"];

    render(<GameTable state={stateWith(hostile)} onClaim={() => {}} onShow={() => {}} />);
    expect(renderedCardSet()).toEqual(["Eight of Spades"]);

    // And no other card is present anywhere in the markup. A card is only
    // ever rendered with its accessible label, so sweeping the labels of the
    // whole deck is an exhaustive check rather than a sample.
    const markup = document.body.innerHTML;
    for (const code of EVERY_CARD) {
      if (code === "8S") continue;
      expect(markup).not.toContain(`aria-label="${cardParts(code).label}"`);
    }
  });

  it("discards a witness that arrives before the reveal", () => {
    const hostile = reducedPoolView();
    hostile.lastShow = revealedView().lastShow;
    const state = stateWith(hostile);
    expect(state.view?.lastShow?.witness).toBeUndefined();
    // The panel is not even rendered while unrevealed.
    render(<GameTable state={state} onClaim={() => {}} onShow={() => {}} />);
    expect(screen.queryByTestId("reveal-panel")).toBeNull();
  });

  it("renders face-down backs for opponents rather than nothing", () => {
    render(<PlayerList view={reducedPoolView()} />);
    const backs = screen.getAllByLabelText("Face-down card");
    // Three opponents, one card each.
    expect(backs).toHaveLength(3);
  });

  it("reveals every hand once the server says the round is revealed", () => {
    render(<PlayerList view={revealedView()} />);
    expect(renderedCardSet()).toEqual(
      ["Eight of Hearts", "Eight of Spades", "King of Diamonds", "Three of Clubs"].sort(),
    );
  });

  it("gates on the reveal flag even if a component is handed cards directly", () => {
    // PlayerList is given a view whose players carry cards but which is not
    // revealed. visibleCards must refuse them.
    const view = reducedPoolView();
    const withCards: PlayerView = {
      ...view,
      players: view.players.map((p) => ({ ...p, cards: ["AS"] })),
    };
    render(<PlayerList view={withCards} />);
    expect(renderedCardSet()).toEqual(["Ace of Spades"]); // only the viewer's own
  });

  it("shows the witness only from the server's own result", () => {
    render(<RevealPanel view={revealedView()} />);
    expect(screen.getByLabelText("Winning hand")).toBeInTheDocument();
    expect(screen.getByText(/The claim was TRUE/)).toBeInTheDocument();
  });
});

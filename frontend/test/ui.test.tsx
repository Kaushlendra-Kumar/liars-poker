import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ClaimBuilder } from "@/components/ClaimBuilder";
import { ConnectionBanner } from "@/components/ConnectionBanner";
import { GameTable } from "@/components/GameTable";
import { ModeBadge } from "@/components/ModeBadge";
import { Timer } from "@/components/Timer";
import { initialState, reduce, type GameState } from "@/store/gameStore";
import type { PlayerView } from "@/protocol/types";
import { exactFiveView, ladder, reducedPoolView, revealedView } from "./fixtures";

function stateWith(view: PlayerView): GameState {
  let s = reduce(initialState, { kind: "claimSpace", payload: ladder() });
  s = reduce(s, { kind: "view", view, source: "view" });
  return s;
}

describe("the claim builder offers only what the server allows", () => {
  it("offers every claim above the standing ordinal", async () => {
    const onClaim = vi.fn();
    render(
      <ClaimBuilder
        state={stateWith(exactFiveView({ standingOrdinal: 4 }))}
        onClaim={onClaim}
        disabled={false}
      />,
    );
    // Ordinal 4 is Pair of 8s, so Pair of 9s is the lowest offer and the
    // HIGH_CARD group is gone entirely.
    expect(screen.queryByRole("button", { name: "High Card" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Pair" }));
    expect(screen.getByRole("button", { name: "Pair of 9s" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Pair of 8s" })).toBeNull();
  });

  it("sends the server's own claim object unchanged", async () => {
    const onClaim = vi.fn();
    render(
      <ClaimBuilder state={stateWith(exactFiveView({ standingOrdinal: 4 }))} onClaim={onClaim} disabled={false} />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Pair" }));
    await userEvent.click(screen.getByRole("button", { name: "Pair of 9s" }));
    expect(onClaim).toHaveBeenCalledWith({ category: "PAIR", primary: "9" });
  });

  it("offers impossible claims too, because bluffing is legal", async () => {
    // A four-card pool cannot make a flush, but the claim is still offered
    // and the note says the server is the authority.
    render(
      <ClaimBuilder state={stateWith(reducedPoolView())} onClaim={() => {}} disabled={false} />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Flush" }));
    expect(
      screen.getByRole("button", { name: "Queen-high Hearts Flush" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/bluffing is legal/i)).toBeInTheDocument();
  });

  it("says SHOW is the only option when nothing higher exists", () => {
    render(
      <ClaimBuilder
        state={stateWith(exactFiveView({ standingOrdinal: ladder().size - 1 }))}
        onClaim={() => {}}
        disabled={false}
      />,
    );
    expect(screen.getByText(/only option/i)).toBeInTheDocument();
  });
});

describe("reduced pool is explained", () => {
  it("says the whole pool is evaluated below five cards", () => {
    render(<ModeBadge view={reducedPoolView()} />);
    expect(screen.getByText(/Reduced pool/)).toBeInTheDocument();
    expect(screen.getByText(/complete available pool of 4/)).toBeInTheDocument();
  });

  it("says subsets are evaluated at five or more", () => {
    render(<ModeBadge view={exactFiveView()} />);
    expect(screen.getByText(/exact 5-card subsets/)).toBeInTheDocument();
  });

  it("states the ownership rule identically in both modes", () => {
    const { unmount } = render(
      <GameTable state={stateWith(reducedPoolView())} onClaim={() => {}} onShow={() => {}} />,
    );
    // The note contains a <strong>, so the text is split across elements and
    // has to be read as one textContent rather than matched as one node.
    const reduced = screen.getByTestId("ownership-note").textContent;
    unmount();
    render(<GameTable state={stateWith(exactFiveView())} onClaim={() => {}} onShow={() => {}} />);
    const exact = screen.getByTestId("ownership-note").textContent;
    expect(reduced).toEqual(exact);
    expect(reduced).toMatch(/does not.*have to be a card of the pattern/i);
  });
});

describe("actions reflect server permission only", () => {
  it("disables SHOW when the server says you are not eligible", () => {
    render(
      <GameTable
        state={stateWith(exactFiveView({ canShow: false }))}
        onClaim={() => {}}
        onShow={() => {}}
      />,
    );
    expect(screen.getByRole("button", { name: /Call SHOW/ })).toBeDisabled();
    expect(screen.getByText(/Only the player immediately after/)).toBeInTheDocument();
  });

  it("enables SHOW when the server says you are", async () => {
    const onShow = vi.fn();
    render(
      <GameTable state={stateWith(exactFiveView())} onClaim={() => {}} onShow={onShow} />,
    );
    const button = screen.getByRole("button", { name: /Call SHOW/ });
    expect(button).toBeEnabled();
    await userEvent.click(button);
    expect(onShow).toHaveBeenCalledOnce();
  });

  it("offers no actions once the round has ended", () => {
    render(<GameTable state={stateWith(revealedView())} onClaim={() => {}} onShow={() => {}} />);
    expect(screen.queryByRole("button", { name: /Call SHOW/ })).toBeNull();
    expect(screen.getByTestId("reveal-panel")).toBeInTheDocument();
  });
});

describe("elimination, game over and connection state", () => {
  it("labels an eliminated player in text, not only colour", () => {
    const view = exactFiveView();
    view.players[2]!.status = "ELIMINATED";
    render(<GameTable state={stateWith(view)} onClaim={() => {}} onShow={() => {}} />);
    expect(screen.getByText("Eliminated")).toBeInTheDocument();
  });

  it("announces a winner", () => {
    render(
      <ConnectionBanner
        state={stateWith(exactFiveView({ phase: "GAME_OVER", winner: "p_b" }))}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Ben wins");
  });

  it("announces an abandoned game", () => {
    render(
      <ConnectionBanner state={stateWith(exactFiveView({ phase: "GAME_OVER" }))} />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(/abandoned/i);
  });

  it("announces reconnecting and a failed reconnect", () => {
    const base = stateWith(exactFiveView());
    const { unmount } = render(
      <ConnectionBanner state={{ ...base, status: "reconnecting", statusDetail: "connection lost" }} />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(/Reconnecting/);
    unmount();
    render(<ConnectionBanner state={{ ...base, status: "failed", statusDetail: "ERR_UNAUTHORIZED" }} />);
    expect(screen.getByRole("status")).toHaveTextContent(/ERR_UNAUTHORIZED/);
  });

  it("reports a closed room", () => {
    const base = stateWith(exactFiveView());
    render(<ConnectionBanner state={{ ...base, closedReason: "game over" }} />);
    expect(screen.getByRole("status")).toHaveTextContent(/room has closed: game over/);
  });
});

describe("the timer is accessible text", () => {
  it("counts down from the server's absolute deadline", () => {
    render(<Timer deadline={new Date(Date.now() + 12_000).toISOString()} label="Turn" />);
    expect(screen.getByTestId("timer")).toHaveTextContent(/Turn: 1[12] seconds remaining/);
  });

  it("renders nothing without a deadline", () => {
    render(<Timer label="Turn" />);
    expect(screen.queryByTestId("timer")).toBeNull();
  });
});

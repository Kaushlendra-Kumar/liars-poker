"use client";

import type { GameState } from "@/store/gameStore";
import { visibleCards, you } from "@/store/gameStore";
import type { Claim } from "@/protocol/types";
import { CardRow } from "./Card";
import { ClaimBuilder } from "./ClaimBuilder";
import { ClaimHistory } from "./ClaimHistory";
import { ModeBadge, OwnershipNote } from "./ModeBadge";
import { PlayerList } from "./PlayerList";
import { RevealPanel } from "./RevealPanel";
import { Timer } from "./Timer";

/**
 * The table.
 *
 * Mobile-first ordering, which is also the priority order: your private
 * cards, the standing claim, your actions, the timer, player status, then
 * history. On a wide screen the history moves into a second column.
 */
export function GameTable({
  state,
  onClaim,
  onShow,
}: {
  state: GameState;
  onClaim: (claim: Claim) => void;
  onShow: () => void;
}) {
  const view = state.view;
  if (!view || view.phase === "WAITING_FOR_PLAYERS") return null;
  const me = you(view);
  const myCards = me ? visibleCards(view, me) : [];
  const standing = view.claims[view.claims.length - 1];
  const acting = view.phase === "CLAIMING";

  return (
    <div className="flex flex-col gap-4 lg:grid lg:grid-cols-[2fr_1fr] lg:items-start">
      <div className="flex flex-col gap-4">
        <section aria-labelledby="hand-heading">
          <h2 id="hand-heading" className="text-sm font-semibold text-slate-100">
            Your cards
          </h2>
          {myCards.length > 0 ? (
            <CardRow cards={myCards} label="Your cards" />
          ) : (
            <p className="text-sm text-slate-300">You have no cards this round.</p>
          )}
        </section>

        <section aria-labelledby="standing-heading">
          <h2 id="standing-heading" className="text-sm font-semibold text-slate-100">
            Standing claim
          </h2>
          <p data-testid="standing-claim" className="text-lg font-semibold text-slate-50">
            {standing ? standing.text : "None yet"}
          </p>
          {acting ? (
            <p className="text-sm text-slate-200">
              {view.yourTurn
                ? "It is your turn."
                : `Waiting for ${
                    view.players.find((p) => p.id === view.turnPlayer)?.name ??
                    "another player"
                  }.`}
            </p>
          ) : null}
          {acting ? <Timer deadline={view.turnDeadline} label="Turn" /> : null}
          {view.phase === "ROUND_END" ? (
            <Timer deadline={view.roundEndDeadline} label="Next round in" />
          ) : null}
        </section>

        {view.revealed ? <RevealPanel view={view} /> : null}

        {acting ? (
          <section aria-labelledby="actions-heading" className="flex flex-col gap-2">
            <h2 id="actions-heading" className="text-sm font-semibold text-slate-100">
              Your move
            </h2>
            <button
              type="button"
              onClick={onShow}
              disabled={!view.canShow}
              aria-describedby="show-help"
              className="rounded bg-amber-400 px-3 py-3 text-base font-semibold text-amber-950 disabled:cursor-not-allowed disabled:opacity-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-amber-200"
            >
              Call SHOW
            </button>
            <p id="show-help" className="text-xs text-slate-300">
              {view.canShow
                ? "Challenge the standing claim. If it holds up, you take the penalty."
                : "Only the player immediately after the claimant may call SHOW."}
            </p>
            {view.canClaim ? (
              <ClaimBuilder state={state} onClaim={onClaim} disabled={!view.yourTurn} />
            ) : null}
          </section>
        ) : null}

        <ModeBadge view={view} />
        <OwnershipNote />
      </div>

      <div className="flex flex-col gap-4">
        <section aria-labelledby="players-heading">
          <h2 id="players-heading" className="text-sm font-semibold text-slate-100">
            Players
          </h2>
          <PlayerList view={view} />
        </section>
        <ClaimHistory view={view} activity={state.activity} />
      </div>
    </div>
  );
}

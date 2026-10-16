"use client";

import type { PlayerView } from "@/protocol/types";
import { nameOf } from "@/store/gameStore";
import { CardRow } from "./Card";

/**
 * The SHOW result, rendered entirely from the server's decision.
 *
 * Nothing here works out who lost, whether the claim was true, or what the
 * witness proves. Every one of those is read off `lastShow`.
 */
export function RevealPanel({ view }: { view: PlayerView }) {
  const result = view.lastShow;
  if (!result) return null;
  const winner = result.loser === result.claimant ? result.challenger : result.claimant;

  return (
    <section
      data-testid="reveal-panel"
      aria-labelledby="reveal-heading"
      className="rounded-md border border-slate-500 bg-slate-900/90 p-3"
    >
      <h3 id="reveal-heading" className="text-base font-semibold text-slate-50">
        Round {result.round} &mdash; SHOW
      </h3>
      <p className="mt-1 text-sm text-slate-100">
        {nameOf(view, result.challenger)} challenged{" "}
        {nameOf(view, result.claimant)}.
      </p>
      <p className="mt-1 text-sm font-semibold text-slate-50">
        The claim was {result.claimTrue ? "TRUE" : "FALSE"}. {nameOf(view, winner)}{" "}
        {winner === view.you ? "win" : "wins"}, {nameOf(view, result.loser)}{" "}
        {result.loser === view.you ? "lose" : "loses"}.
      </p>
      <p className="mt-1 text-xs text-slate-200">
        Evaluated over {result.poolSize} card{result.poolSize === 1 ? "" : "s"} as a{" "}
        {result.handSize}-card hand (
        {result.mode === "REDUCED_POOL" ? "reduced pool" : "exact five"}).
      </p>
      {result.witness && result.witness.length > 0 ? (
        <div className="mt-2">
          <p className="text-xs font-semibold text-slate-100">
            The five cards the server used
          </p>
          <CardRow small cards={result.witness} label="Winning hand" />
        </div>
      ) : null}
      {view.roundEndDeadline ? (
        <p className="mt-2 text-xs text-slate-300">
          The next round starts automatically.
        </p>
      ) : null}
    </section>
  );
}

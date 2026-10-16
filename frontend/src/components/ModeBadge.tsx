"use client";

import type { PlayerView } from "@/protocol/types";

/**
 * Explains which evaluation width the round uses, because the semantics
 * genuinely differ and a player needs to know which applies.
 *
 * Descriptive only. The frontend has no evaluator of any kind.
 */
export function ModeBadge({ view }: { view: PlayerView }) {
  const reduced = view.evalMode === "REDUCED_POOL";
  return (
    <div
      data-testid="mode-badge"
      className={`rounded-md border px-3 py-2 text-sm ${
        reduced
          ? "border-amber-400 bg-amber-950/60 text-amber-50"
          : "border-emerald-500 bg-emerald-950/50 text-emerald-50"
      }`}
    >
      <p className="font-semibold">
        {reduced ? "Reduced pool" : "Standard"} &mdash; {view.poolSize} card
        {view.poolSize === 1 ? "" : "s"} in play
      </p>
      <p className="mt-1">
        {reduced
          ? `Fewer than 5 cards are in play, so the complete available pool of ${view.poolSize} is evaluated as the hand.`
          : "5 or more cards are in play, so claims are evaluated over exact 5-card subsets and extra cards can be excluded."}
      </p>
    </div>
  );
}

/**
 * The ownership rule, stated once and identically in both modes, because the
 * rule does not have a per-mode variant.
 */
export function OwnershipNote() {
  return (
    <p data-testid="ownership-note" className="text-xs text-slate-200">
      You must own at least one card of the evaluated hand. It does{" "}
      <strong>not</strong> have to be a card of the pattern you claim &mdash; a
      kicker or any other selected card counts.
    </p>
  );
}

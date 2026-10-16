"use client";

import { useMemo, useState } from "react";
import type { Claim } from "@/protocol/types";
import { CATEGORY_TITLE, claimLabel } from "@/protocol/labels";
import { groupLadder, legalLadder, type GameState } from "@/store/gameStore";

/**
 * The claim builder, driven entirely by the server's 367-element ladder.
 *
 * Everything it knows comes from server data:
 *
 *   - which categories exist, and their order: the order they appear in the
 *     ascending ladder;
 *   - which parameters a category takes, and their order: the claims the
 *     ladder contains for that category, in ladder order;
 *   - which claims are offerable: those above `standingOrdinal`.
 *
 * So there is no poker comparison in TypeScript, no rank table, no category
 * ranking. The last point is an integer comparison against a
 * server-assigned floor, and the server revalidates every claim anyway.
 */
export function ClaimBuilder({
  state,
  onClaim,
  disabled,
}: {
  state: GameState;
  onClaim: (claim: Claim) => void;
  disabled: boolean;
}) {
  const legal = useMemo(() => legalLadder(state), [state]);
  const groups = useMemo(() => groupLadder(legal), [legal]);
  const [category, setCategory] = useState<string | null>(null);

  const active = groups.find((g) => g.category === category) ?? groups[0];

  if (state.ladder.length === 0) {
    return <p className="text-sm text-slate-300">Loading the claim ladder&hellip;</p>;
  }

  if (legal.length === 0) {
    return (
      <p className="rounded border border-amber-400 bg-amber-950/60 p-2 text-sm text-amber-50">
        No higher claim exists. Calling SHOW is your only option.
      </p>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      <fieldset disabled={disabled}>
        <legend className="text-sm font-semibold text-slate-100">
          Hand type ({legal.length} of {state.ladderSize} claims still available)
        </legend>
        <div className="mt-1 flex flex-wrap gap-1" role="group" aria-label="Hand type">
          {groups.map((g) => {
            const selected = active?.category === g.category;
            return (
              <button
                key={g.category}
                type="button"
                aria-pressed={selected}
                onClick={() => setCategory(g.category)}
                className={`rounded border px-2 py-1 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-emerald-300 disabled:opacity-50 ${
                  selected
                    ? "border-emerald-300 bg-emerald-700 text-white"
                    : "border-slate-600 bg-slate-800 text-slate-100"
                }`}
              >
                {CATEGORY_TITLE[g.category]}
              </button>
            );
          })}
        </div>
      </fieldset>

      {active ? (
        <fieldset disabled={disabled}>
          <legend className="text-sm font-semibold text-slate-100">
            {CATEGORY_TITLE[active.category]} &mdash; pick one
          </legend>
          <div
            className="mt-1 flex max-h-56 flex-wrap gap-1 overflow-y-auto"
            role="group"
            aria-label={`${CATEGORY_TITLE[active.category]} options`}
          >
            {active.claims.map((claim) => {
              const label = claimLabel(claim);
              return (
                <button
                  key={label}
                  type="button"
                  data-testid="claim-option"
                  onClick={() => onClaim(claim)}
                  className="rounded border border-slate-600 bg-slate-800 px-2 py-1 text-sm text-slate-50 hover:bg-slate-700 focus:outline-none focus-visible:ring-2 focus-visible:ring-emerald-300 disabled:opacity-50"
                >
                  {label}
                </button>
              );
            })}
          </div>
        </fieldset>
      ) : null}

      <p className="text-xs text-slate-300">
        Every claim above the standing one is offered, including ones the cards
        cannot support: bluffing is legal and the server is the only authority
        on whether a claim turns out to be true.
      </p>
    </div>
  );
}

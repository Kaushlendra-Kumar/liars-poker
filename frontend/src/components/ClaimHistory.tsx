"use client";

import type { PlayerView } from "@/protocol/types";
import { nameOf, type ActivityEntry } from "@/store/gameStore";

/**
 * The claim chain and the activity log. Claim text comes from the server's
 * own `text` field, so the history cannot disagree with the server about
 * what was claimed.
 */
export function ClaimHistory({
  view,
  activity,
}: {
  view: PlayerView;
  activity: ActivityEntry[];
}) {
  return (
    <div className="flex flex-col gap-3">
      <section aria-labelledby="chain-heading">
        <h3 id="chain-heading" className="text-sm font-semibold text-slate-100">
          Claims this round
        </h3>
        {view.claims.length === 0 ? (
          <p className="text-sm text-slate-300">No claim yet.</p>
        ) : (
          <ol className="mt-1 flex flex-col gap-1">
            {view.claims.map((c, i) => (
              <li
                key={`${c.ordinal}-${i}`}
                className="rounded bg-slate-900/80 px-2 py-1 text-sm text-slate-50"
              >
                <span className="font-semibold">{nameOf(view, c.player)}</span>{" "}
                {c.text}
                {i === view.claims.length - 1 ? (
                  <span className="ml-1 text-xs text-emerald-200">(standing)</span>
                ) : null}
              </li>
            ))}
          </ol>
        )}
      </section>

      <section aria-labelledby="activity-heading">
        <h3 id="activity-heading" className="text-sm font-semibold text-slate-100">
          Activity
        </h3>
        <ol className="mt-1 flex max-h-48 flex-col gap-1 overflow-y-auto">
          {activity
            .slice()
            .reverse()
            .map((a) => (
              <li key={a.seq} className="text-xs text-slate-200">
                {a.text}
              </li>
            ))}
        </ol>
      </section>
    </div>
  );
}

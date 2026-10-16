"use client";

import type { PlayerView } from "@/protocol/types";
import { visibleCards } from "@/store/gameStore";
import { CardRow } from "./Card";

export function PlayerList({ view }: { view: PlayerView }) {
  return (
    <ul className="flex flex-col gap-2" aria-label="Players">
      {view.players.map((p) => {
        const cards = visibleCards(view, p);
        const hidden = Math.max(0, p.cardCount - cards.length);
        const eliminated = p.status === "ELIMINATED";
        return (
          <li
            key={p.id}
            data-testid={`player-${p.seat}`}
            className={`rounded-md border p-2 ${
              p.isTurn
                ? "border-emerald-400 bg-emerald-950/50"
                : "border-slate-700 bg-slate-900/70"
            } ${eliminated ? "opacity-70" : ""}`}
          >
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-semibold text-slate-50">
                {p.isYou ? `${p.name || "You"} (you)` : p.name || `Seat ${p.seat + 1}`}
              </span>
              <span className="text-xs text-slate-200">
                {p.handSize} card{p.handSize === 1 ? "" : "s"}
              </span>
              {p.isTurn ? (
                <span className="rounded bg-emerald-400 px-1.5 py-0.5 text-xs font-semibold text-emerald-950">
                  To act
                </span>
              ) : null}
              {p.isStarter ? (
                <span className="rounded bg-slate-600 px-1.5 py-0.5 text-xs text-slate-50">
                  Starter
                </span>
              ) : null}
              {eliminated ? (
                <span className="rounded bg-rose-900 px-1.5 py-0.5 text-xs text-rose-50">
                  Eliminated
                </span>
              ) : null}
              {p.status === "DISCONNECTED" ? (
                <span className="rounded bg-amber-900 px-1.5 py-0.5 text-xs text-amber-50">
                  Disconnected
                </span>
              ) : null}
              {view.phase === "WAITING_FOR_PLAYERS" ? (
                <span className="text-xs text-slate-200">
                  {p.ready ? "Ready" : "Not ready"}
                </span>
              ) : null}
            </div>
            {cards.length > 0 || hidden > 0 ? (
              <div className="mt-2">
                <CardRow
                  small
                  cards={cards}
                  hiddenCount={hidden}
                  label={`${p.isYou ? "Your" : `${p.name || "Opponent"} seat ${p.seat + 1}`} cards`}
                />
              </div>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}

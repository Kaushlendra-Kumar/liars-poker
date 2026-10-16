"use client";

import type { GameState } from "@/store/gameStore";
import { isAbandoned } from "@/store/gameStore";

/**
 * Connection and terminal state. Always text, never colour alone, and always
 * announced so a screen-reader user learns that the game has stalled.
 */
export function ConnectionBanner({ state }: { state: GameState }) {
  const { status, statusDetail, closedReason, view } = state;

  let tone = "border-slate-600 bg-slate-900/80 text-slate-100";
  let text: string | null = null;

  if (closedReason) {
    tone = "border-slate-400 bg-slate-800 text-slate-50";
    text = `The room has closed: ${closedReason}.`;
  } else if (status === "reconnecting" || status === "connecting") {
    tone = "border-amber-400 bg-amber-950/70 text-amber-50";
    text =
      status === "connecting"
        ? "Connecting to the server\u2026"
        : `Reconnecting\u2026 ${statusDetail ? `(${statusDetail})` : ""}`;
  } else if (status === "failed") {
    tone = "border-rose-400 bg-rose-950/70 text-rose-50";
    text = `Disconnected: ${statusDetail ?? "could not reconnect"}. Reload to start again.`;
  } else if (isAbandoned(view)) {
    tone = "border-slate-400 bg-slate-800 text-slate-50";
    text = "Everyone left, so the game was abandoned with no winner.";
  } else if (view?.phase === "GAME_OVER" && view.winner) {
    tone = "border-emerald-400 bg-emerald-950/70 text-emerald-50";
    text =
      view.winner === view.you
        ? "You win."
        : `${view.players.find((p) => p.id === view.winner)?.name ?? "A player"} wins.`;
  }

  if (!text) return null;
  return (
    <div role="status" aria-live="polite" className={`rounded-md border p-2 text-sm ${tone}`}>
      {text}
    </div>
  );
}

/** Errors the server rejected an action with. Advisory, not state. */
export function ErrorNotice({ state }: { state: GameState }) {
  if (!state.lastError) return null;
  return (
    <p
      role="alert"
      className="rounded-md border border-rose-400 bg-rose-950/70 p-2 text-sm text-rose-50"
    >
      {state.lastError.message}
    </p>
  );
}

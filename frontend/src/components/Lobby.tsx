"use client";

import { useState } from "react";
import type { GameState } from "@/store/gameStore";
import { you } from "@/store/gameStore";
import { PlayerList } from "./PlayerList";

const MIN_PLAYERS = 4;

/**
 * Room creation, joining, and the ready-up screen.
 *
 * The minimum-player number here is a hint for the button label only. The
 * server decides whether a game may start and answers
 * ERR_NOT_ENOUGH_PLAYERS if not.
 */
export function Lobby({
  state,
  httpBase,
  onJoin,
  onReady,
  onStart,
}: {
  state: GameState;
  httpBase: string;
  onJoin: (code: string, name: string) => void;
  onReady: (ready: boolean) => void;
  onStart: () => void;
}) {
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);

  const view = state.view;
  const me = you(view);
  const seated = view?.players.length ?? 0;

  async function createRoom() {
    setCreating(true);
    setCreateError(null);
    try {
      const res = await fetch(`${httpBase}/rooms`, { method: "POST" });
      if (!res.ok) throw new Error(`server said ${res.status}`);
      const body = (await res.json()) as { roomCode?: string };
      if (!body.roomCode) throw new Error("no room code returned");
      setCode(body.roomCode);
      // Whoever creates a room is its first player, so seat them right away
      // rather than asking for a second click.
      onJoin(body.roomCode, name.trim() || "Player");
    } catch (err) {
      setCreateError(err instanceof Error ? err.message : "could not create a room");
    } finally {
      setCreating(false);
    }
  }

  if (view && view.phase !== "WAITING_FOR_PLAYERS") return null;

  if (!view) {
    return (
      <section aria-labelledby="join-heading" className="flex flex-col gap-3">
        <h2 id="join-heading" className="text-lg font-semibold text-slate-50">
          Join a table
        </h2>
        <label className="flex flex-col gap-1 text-sm text-slate-100">
          Your name
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={32}
            autoComplete="off"
            className="rounded border border-slate-600 bg-slate-800 px-2 py-2 text-slate-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-emerald-300"
          />
        </label>
        <label className="flex flex-col gap-1 text-sm text-slate-100">
          Room code
          <input
            value={code}
            onChange={(e) => setCode(e.target.value.toUpperCase())}
            maxLength={6}
            spellCheck={false}
            autoComplete="off"
            aria-describedby="code-help"
            className="rounded border border-slate-600 bg-slate-800 px-2 py-2 font-mono tracking-widest text-slate-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-emerald-300"
          />
        </label>
        <p id="code-help" className="text-xs text-slate-300">
          Six characters, from the person who made the room.
        </p>
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            disabled={code.trim().length === 0}
            onClick={() => onJoin(code, name || "Player")}
            className="rounded bg-emerald-500 px-3 py-2 font-semibold text-emerald-950 disabled:opacity-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-emerald-200"
          >
            Join room
          </button>
          <button
            type="button"
            onClick={createRoom}
            disabled={creating}
            className="rounded border border-slate-500 px-3 py-2 text-slate-50 disabled:opacity-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-emerald-200"
          >
            {creating ? "Creating\u2026" : "Create a room"}
          </button>
        </div>
        {createError ? (
          <p role="alert" className="text-sm text-rose-300">
            {createError}
          </p>
        ) : null}
      </section>
    );
  }

  return (
    <section aria-labelledby="lobby-heading" className="flex flex-col gap-3">
      <h2 id="lobby-heading" className="text-lg font-semibold text-slate-50">
        Room {state.roomCode}
      </h2>
      <p className="text-sm text-slate-200">
        {seated} seated. The game needs at least {MIN_PLAYERS} players, all ready.
      </p>
      <PlayerList view={view} />
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          onClick={() => onReady(!me?.ready)}
          aria-pressed={Boolean(me?.ready)}
          className="rounded bg-emerald-500 px-3 py-2 font-semibold text-emerald-950 focus:outline-none focus-visible:ring-2 focus-visible:ring-emerald-200"
        >
          {me?.ready ? "Not ready" : "I am ready"}
        </button>
        <button
          type="button"
          onClick={onStart}
          className="rounded border border-slate-500 px-3 py-2 text-slate-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-emerald-200"
        >
          Start game
        </button>
      </div>
    </section>
  );
}

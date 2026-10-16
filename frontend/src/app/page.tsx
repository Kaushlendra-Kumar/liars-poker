"use client";

import { useGame } from "@/hooks/useGame";
import { ConnectionBanner, ErrorNotice } from "@/components/ConnectionBanner";
import { GameTable } from "@/components/GameTable";
import { Lobby } from "@/components/Lobby";

const WS_URL = process.env.NEXT_PUBLIC_SERVER_WS ?? "ws://localhost:8787/ws";
// Empty means same-origin, which is what both production (via nginx) and
// development (via the rewrite in next.config.mjs) provide.
const HTTP_BASE = process.env.NEXT_PUBLIC_SERVER_HTTP ?? "";

export default function Page() {
  const game = useGame(WS_URL);

  return (
    <main id="main" className="mx-auto flex max-w-5xl flex-col gap-4 p-3 sm:p-6">
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h1 className="text-xl font-bold text-slate-50">Liar&apos;s Poker</h1>
        {game.state.view ? (
          <p className="text-sm text-slate-200">
            Round {game.state.view.round} &middot; {game.state.view.phase.toLowerCase().replace(/_/g, " ")}
          </p>
        ) : null}
      </header>

      <ConnectionBanner state={game.state} />
      <ErrorNotice state={game.state} />

      <Lobby
        state={game.state}
        httpBase={HTTP_BASE}
        onJoin={game.join}
        onReady={game.setReady}
        onStart={game.startGame}
      />
      <GameTable state={game.state} onClaim={game.makeClaim} onShow={game.callShow} />
    </main>
  );
}

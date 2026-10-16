# Liar's Poker backend — Cloudflare Worker

TypeScript port of the Go server in `../` (same protocol v1, same rules).
One Durable Object (`Room`) per room code holds the authoritative game state;
the engine is the same pure reducer, now in `src/game.ts`.

| File | Ported from |
|---|---|
| `src/poker.ts` | `pkg/poker` (cards, classifier, claim space, validator) |
| `src/game.ts` | `internal/game` (reducer, view, event projection) |
| `src/protocol.ts` | `internal/protocol` |
| `src/session.ts` | `internal/session` |
| `src/room.ts` | `internal/room` + `internal/transport/ws` (Durable Object) |
| `src/index.ts` | `cmd/server` HTTP routes |

## Differences from the Go server
- WebSocket URL is `/ws?room=CODE` so the Worker can route to the right
  Durable Object (the frontend client adds this automatically).
- Timers use a Durable Object alarm instead of a 250 ms ticker.
- Unset deadlines are omitted from views instead of sent as year-0001.
- No server pings (Cloudflare detects dead sockets); `pong` frames are still accepted.
- No global room cap (each room is its own object).

## Commands
```bash
npm install
npm test            # engine + protocol tests
npm run typecheck   # TypeScript 7
npm run dev         # http://localhost:8787
npx wrangler deploy
```

Frontend e2e against the local worker:
```bash
npx wrangler dev --var TURN_SECONDS:3 --var REVEAL_SECONDS:0.3
cd ../../frontend && E2E_SERVER=http://127.0.0.1:8787 npm run e2e
```

## Deploy
1. `npx wrangler deploy` → note the `https://liars-poker.<you>.workers.dev` URL.
2. Vercel project (root directory `frontend`) env vars:
   - `NEXT_PUBLIC_SERVER_HTTP=https://liars-poker.<you>.workers.dev`
   - `NEXT_PUBLIC_SERVER_WS=wss://liars-poker.<you>.workers.dev/ws`
3. Set `ALLOWED_ORIGINS` in `wrangler.jsonc` to the Vercel URL and redeploy.

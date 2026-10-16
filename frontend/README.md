# Liar's Poker — frontend

A Next.js client for the authoritative Go server. It is a **client**: it
renders what the server says and sends intents back. It contains no game
engine.

## Verified state

```
npm run lint    tsc --noEmit            clean
npm test        54 tests, 4 files       pass
npm run e2e     3 tests, real Go server pass
npx next build  production build        succeeds
```

The e2e suite plays a full round through a real WebSocket against
`backend/cmd/server`: lobby, ready-up, the 367-claim ladder, a reduced-pool
round 1, a claim, a SHOW, the reveal, and the automatic round advance. It
also resumes a dropped session and checks that three different invalid
resumes all come back as a single undifferentiated `ERR_UNAUTHORIZED` with
no view delivered.

## Local development

You need Node 20+ and Go 1.22+.

> A stale `node_modules/` may be present from a failed install in the
> sandbox this was built in. Delete it before installing:
> `rm -rf node_modules package-lock.json && npm install`. The committed
> `package-lock.json` pins the exact versions the test run above used.

```bash
# 1. Start the authoritative server (from the repo root)
cd backend
go run ./cmd/server -origins '*'          # listens on :8080

# 2. Start the frontend
cd ../frontend
npm install
npm run dev                                # http://localhost:3000
```

### How the client reaches the server in development

`/rooms` is proxied through the Next dev server by a rewrite in
`next.config.mjs`, so the browser sees it as same-origin.

That is not cosmetic. Fetching `http://localhost:8080/rooms` directly from a
page on `localhost:3000` is cross-origin: the browser sends the POST without
a preflight, the server creates the room, and then the browser blocks the
response. The room exists but the client never learns its code, which looks
exactly like the server being down. Production has no such problem because
nginx serves both from one origin, and the rewrite makes development behave
the same way rather than adding CORS headers to the server.

A WebSocket handshake is not subject to the same-origin policy, so `/ws`
needs no proxying and goes straight to the server.

Set `BACKEND_ORIGIN` if the Go server is not on `http://localhost:8080`.

Point the client at a different server with two environment variables:

```bash
NEXT_PUBLIC_SERVER_HTTP=http://localhost:8080 \
NEXT_PUBLIC_SERVER_WS=ws://localhost:8080/ws \
npm run dev
```

Create a room with the **Create a room** button (which calls
`POST /rooms`), share the six-character code, and have four to nine players
join it.

## Commands

| Command | What it does |
|---|---|
| `npm run dev` | dev server |
| `npm run build` | production build |
| `npm run lint` | `tsc --noEmit` |
| `npm test` | unit and component tests (Vitest + Testing Library) |
| `npm run e2e` | end-to-end against a real Go server, which it builds and starts |

The e2e suite starts `backend/cmd/server` itself. To run it against a server
you already have up:

```bash
E2E_SERVER=http://127.0.0.1:8080 npm run e2e
```

## Architecture

```
WebSocket  ->  src/protocol/client.ts   (the only WebSocket in the app)
           ->  src/store/gameStore.ts   (view is authoritative; sanitises on ingest)
           ->  src/hooks/useGame.ts     (the only place the client meets React)
           ->  src/components/*         (presentation only)
```

No React component parses a raw server message, and none of them holds game
rules.

### The view is the state

`view` is the authoritative client state. Events are used only for the
activity log and for animation cues; they never mutate game state. A client
that discarded every event and rendered only the latest view would remain
fully playable, which is the frontend form of the server's
`TestClientWithNoReducerIsPlayable`. `test/store.test.ts` asserts that an
event never changes the view.

### Hidden cards cannot be rendered

Two independent layers, because one is not enough:

1. **Sanitised on ingest.** `sanitizeView` removes any other player's cards
   from an unrevealed view and drops a witness that arrives before a reveal.
   `sanitizeEvent` drops hands from every event except `CARDS_REVEALED`. A
   leaky or compromised server has its leak discarded at the door.
2. **Gated on read.** Components get cards only through `visibleCards`,
   which returns them only when `player.isYou || view.revealed`.

`test/leak.test.tsx` feeds the UI a hostile view in which every opponent has
cards while `revealed` is false, and asserts the DOM contains exactly one
card face and no other card code anywhere in the markup.

### The claim builder holds no poker knowledge

Everything it needs comes from the server's 367-element ladder:

- which categories exist, and their order — the order they appear in the
  ascending ladder;
- which parameters each category takes, and their order — the claims the
  ladder holds for that category, in ladder order;
- which claims may be offered — those above `standingOrdinal`.

So there is no rank table, no category ranking and no claim comparison in
TypeScript. The last point is an integer comparison against a server-assigned
floor, and the server revalidates every claim on arrival regardless.

**Impossible claims stay in the list.** A flush cannot be made from a
four-card pool, but the claim is still offered, because bluffing it is legal
and the server is the only authority on whether a claim turns out true. The
builder says so in as many words.

### Cross-connection ordering

Frame order holds per connection only. The UI therefore renders from its own
latest view and never infers another player's state from event timing. The
e2e suite follows the same discipline: the challenger waits until *its own*
view says `canShow` rather than assuming the claimant's action has landed.

### Reconnection, and the token

The rejoin token lives in the `useGame` hook's memory and nowhere else. It is
never written to `localStorage`, `sessionStorage`, a cookie, a URL or a log.
`Token` redacts through `toString` and `toJSON`, mirroring `session.Token` on
the server, so it cannot reach a log line by accident.

The cost is that a **page reload ends the session**: a socket drop reconnects
and resumes automatically, but a refresh has nothing to resume with.
Surviving a reload needs a browser persistence model, and `docs/protocol.md`
documents none, so inventing one would be inventing a weaker transport. See
`PROTOCOL_GAPS.md`.

## Accessibility

- every control is a real `button` or labelled `input`, reachable by keyboard,
  with a visible `focus-visible` ring;
- cards carry a screen-reader label (`"Eight of Spades"`), and a face-down
  card is labelled as such;
- status is always text as well as colour: "To act", "Eliminated",
  "Disconnected", "Ready";
- the timer is text, and is not announced on every tick because a
  per-second live region is unusable;
- connection and terminal states are in an `aria-live="polite"` region;
- a skip link, and `prefers-reduced-motion` honoured.

## Mobile

Single column by default, widening to two at `lg`. Order matches priority:
your cards, the standing claim, your actions and timer, then mode, then
players and history.

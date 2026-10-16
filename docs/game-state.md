# Game State Machine

**Revision 2** — reflects the shipped engine.

## 1. Phases

```
                      ┌──────────────────────┐
                      │ WAITING_FOR_PLAYERS  │
                      └──────────┬───────────┘
                    all ready, 4..9 players
                                 │
                                 ▼
                      ┌──────────────────────┐
              ┌──────▶│     ROUND_SETUP      │
              │       │ collect, shuffle,    │
              │       │ deal H(p), freeze    │
              │       │ pool, pick starter   │
              │       └──────────┬───────────┘
              │                  │ CardsDealt
              │                  ▼
              │       ┌──────────────────────┐
              │       │      CLAIMING        │◀────┐
              │       │ turn = current       │     │ MAKE_CLAIM
              │       │ standing claim       │─────┘ (strictly higher)
              │       └──────────┬───────────┘
              │                  │ CALL_SHOW
              │                  ▼
              │       ┌──────────────────────┐
              │       │   SHOW_RESOLUTION    │
              │       │ reveal all, validate │
              │       │ previous claim only  │
              │       └──────────┬───────────┘
              │                  │ ClaimValidated
              │                  ▼
              │       ┌──────────────────────┐
              │       │      ROUND_END       │
              │       │ +1 to loser, or      │
              │       │ eliminate at 6       │
              │       └──────┬───────────┬───┘
              │              │           │ one active player left
              └──────────────┘           ▼
               ≥2 active         ┌──────────────────────┐
                                 │      GAME_OVER       │
                                 └──────────────────────┘
```

**Implementation note.** `ROUND_SETUP` and `SHOW_RESOLUTION` are *not*
observable phases in the shipped engine. They are steps that run to
completion inside a single `Apply` call, emitting their events as they go, so
no client action can ever be legal in the middle of one and no state a client
can observe is ever mid-setup or mid-resolution. The `Phase` enum therefore
has four values:

```
WAITING_FOR_PLAYERS -> CLAIMING -> ROUND_END -> CLAIMING -> ... -> GAME_OVER
```

`ROUND_END` *is* observable, deliberately: it is the reveal window. If the
next round began inside the same `Apply` that resolved the SHOW, the revealed
hands would be replaced by a fresh deal before any client could render them.
The round advances on `ADVANCE_ROUND`, or on a `TICK` past
`RoundEndDeadline` (`Config.RoundEndDuration`, default 6s).

The only phases that accept player input are `WAITING_FOR_PLAYERS` and
`CLAIMING`.

## 2. Reducer

```
Apply(state State, action Action) (State, []Event, error)
```

Pure. No goroutines, no clock, no I/O, no global RNG.

- **Time** enters on the action. Every action embeds `Meta{PlayerID, Now}`,
  so the reducer never reads a clock and timeouts are exact in tests. A zero
  `Now` disables timers entirely, which is what unit tests use.
- **Randomness** enters as `Config.Seed`. Each round derives its shuffle
  source as `rand.NewSource(Seed*1000003 + Round)` and hands it to the deck
  through the `poker.RNG` interface. This is a deliberate change from holding
  an `RNG` interface *in* the state: an interface value in `State` would make
  the state non-copyable and non-comparable and would quietly couple two
  clones to one stream. Deriving per round keeps `State` a pure value and
  keeps the whole game reproducible from the seed alone.

Given the same `(state, action)` the output is identical, verified by a test
that calls `Apply` on one state from sixteen goroutines at once and compares
results. `go test -race ./...` is clean.

A rejected action returns the original state untouched, emits no events, and
does not consume the turn timer. Tests assert all three.

## 3. Actions

| Action | Legal phase | Legal actor |
|---|---|---|
| `JOIN` | `WAITING_FOR_PLAYERS` | any non-seated, room below 9 |
| `LEAVE` | `WAITING_FOR_PLAYERS` | seated player |
| `SET_READY` | `WAITING_FOR_PLAYERS` | seated player |
| `START_GAME` | `WAITING_FOR_PLAYERS` | host, ≥4 seated and ready |
| `MAKE_CLAIM` | `CLAIMING` | current-turn active player |
| `CALL_SHOW` | `CLAIMING` | immediate next active player, standing claim exists |
| `ADVANCE_ROUND` | `ROUND_END` | any seated player, or the room's own tick |
| `TICK` | any | engine-internal only, never client-originated |
| `DISCONNECT` / `RECONNECT` | any | transport-originated |

## 4. Rejection matrix

Every row is an explicit typed error and a test case. Your §39 list, plus
the cases the rules imply:

| Attempt | Error |
|---|---|
| `CALL_SHOW` during `ROUND_SETUP` | `ERR_WRONG_PHASE` |
| `CALL_SHOW` as round starter (no standing claim) | `ERR_NO_STANDING_CLAIM` |
| `CALL_SHOW` by a player who is not the immediate next | `ERR_NOT_SHOW_ELIGIBLE` |
| `MAKE_CLAIM` out of turn | `ERR_NOT_YOUR_TURN` |
| Claim equal to the standing claim | `ERR_CLAIM_NOT_HIGHER` |
| Claim lower than the standing claim | `ERR_CLAIM_NOT_HIGHER` |
| Second claim in the same turn | `ERR_NOT_YOUR_TURN` |
| Claim after SHOW resolution | `ERR_WRONG_PHASE` |
| Any action from an eliminated player | `ERR_PLAYER_ELIMINATED` |
| Any gameplay action after `GAME_OVER` | `ERR_GAME_OVER` |
| Structurally invalid claim (`Pair of 1s`, `8s full of 8s`, `Straight to 4`, `Straight Flush to A`) | `ERR_MALFORMED_CLAIM` |
| Unknown message type or category | `ERR_UNKNOWN_ACTION` |
| `START_GAME` with fewer than 4 players | `ERR_NOT_ENOUGH_PLAYERS` |
| `JOIN` a game already in progress | `ERR_GAME_IN_PROGRESS` |
| `MAKE_CLAIM` when the standing claim is the maximum | `ERR_CLAIM_NOT_HIGHER` (SHOW is the only legal action) |

Rejections never mutate state and never consume the turn timer.

## 5. Events

Monotonic `Seq` per room, assigned by the reducer, used for ordering,
reconnect, replay and idempotency.

| Event | Payload notes |
|---|---|
| `GameStarted` | seats, hand sizes, first starter |
| `RoundStarted` | round number, starter, per-player hand sizes |
| `CardsDealt` | **per-player**: own cards in full, others as counts only |
| `TurnStarted` | player, deadline timestamp, SHOW eligibility flag |
| `ClaimMade` | player, canonical claim, ordinal |
| `ShowCalled` | caller, the claim being challenged |
| `CardsRevealed` | every active player's full hand (all recipients) |
| `ClaimValidated` | result TRUE/FALSE, witness 5 cards if TRUE, loser |
| `PenaltyApplied` | player, old `H`, new `H` |
| `PlayerEliminated` | player, reason (`SIX_CARDS` / `DISCONNECT`) |
| `RoundEnded` | round number, loser, resulting hand sizes |
| `GameEnded` | winner |
| `PlayerDisconnected` / `PlayerReconnected` | player, grace deadline |
| `TurnTimedOut` | player, policy applied |
| `PlayerJoined` / `PlayerReady` | lobby transitions |

Every event carries `Round`, a monotonic `Seq`, and where relevant `PoolSize`
and `Mode` (`EXACT_FIVE` or `REDUCED_POOL`), so a client can display which
validation width is in force without computing it.

`CardsDealt` is the only event with per-recipient payload variation. Every
other event is identical for all recipients, so message size and timing
cannot be used to infer hidden cards. Hidden fields are sent as explicit
nulls rather than omitted, for the same reason.

## 6. Views

```
View(state State, playerID PlayerID) PlayerView
```

The single outward projection. Nothing else in the codebase is permitted to
return state to a client. The information-leak test suite therefore has one
target: for every reachable state and every player, assert that
`View(s, p)` contains no card owned by anyone other than `p`, unless the
phase is `SHOW_RESOLUTION`/`ROUND_END` for the current round.

`PlayerView` carries: own cards, all hand sizes and card counts, claim
history with ordinals, current turn and deadline, `CanClaim` / `CanShow`
flags, `StandingOrdinal`, pool size, eval mode and hand width, seat and
starter markers, status, round number, and — after SHOW only — the revealed
hands, result and witness. It has **no field** for the deck, the seed, the
pool contents or the ownership map, so those cannot leak by accident.

The claim space itself is static and public, so the view does not carry it.
`StandingOrdinal` is the floor: a client renders its claim builder over the
367-element space filtered to ordinals strictly above it, which needs no rule
knowledge on the client and is revalidated server-side regardless.

`LeakedCards(state, viewer)` is the test hook: it returns any card visible in
a view that the viewer does not own while the round is unrevealed. It is
asserted empty for every player after every action across every simulated
game.

## 7. Concurrency

One goroutine per room (actor). All actions arrive on a buffered command
channel and are applied strictly serially, so the race classes in your §12
list are structurally impossible rather than defended against. Mutexes guard
only the room registry. `Apply` being pure means `go test -race` has nothing
to find inside `internal/game` by construction.

```
WS conn ──┐
WS conn ──┼─▶ room.cmdCh ─▶ [single goroutine] ─▶ Apply ─▶ events ─▶ View(p) ─▶ per-conn send
WS conn ──┘                         │
                                    └─ recover boundary: a panic must not kill the room
```

The turn timer is a `time.Timer` owned by the room goroutine that enqueues a
`TICK` onto the same channel, so timeouts serialise with player actions and
a claim landing in the same millisecond as expiry cannot double-resolve.

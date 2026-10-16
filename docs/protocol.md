# Protocol

**Revision 2 — implemented.** Written before the transport existed; this
revision records the three clarifications implementation forced. Every
message here is a mechanical mapping of something the engine already has.
Where this document and the engine disagree, the engine wins and this
document is wrong.

---

## 1. Principles

The transport is a pump. It has exactly three points of contact with the
game, and no fourth:

```
inbound frame  ->  game.Action        (parse and authenticate; no rules)
                   game.Apply         (the only place state changes)
outbound       ->  game.View(s, p)    (the only state projection)
                   game.ProjectEvent  (the only event projection)
```

**The transport layer contains no gameplay logic.** It does not compare
claims, check turn order, evaluate a SHOW, apply a penalty, decide
eligibility, or filter cards. Every one of those is already a function in
`internal/game`, and duplicating any of them in the transport would create a
second source of truth. If a rule question arises while implementing
transport, the answer is to call the engine, not to add a branch.

Three consequences worth stating up front:

- **The client is told, never asked.** Clients send intents. They never send
  state, card values, claim validity, eligibility, timers or results.
- **Rejection is normal.** A malformed or illegal action returns a typed
  error and changes nothing. The engine already guarantees that a rejected
  action emits no events and does not consume the turn timer, so the
  transport just forwards the error.
- **Hidden information has exactly two exits.** `View` and `ProjectEvent`.
  Any code path that serialises a `game.State`, a `poker.Card` slice, a
  `State.Pool`, a `State.Owner` map or a raw `Event` straight to a socket is
  a bug, and the leak test suite exists to catch it.

## 2. Framing

- WebSocket, one connection per player per room.
- JSON text frames, one message per frame. No batching in v1.
- **Inbound frame limit 4 KiB.** No legal client message approaches this; a
  claim is a handful of short strings. Oversized frames close the connection
  with `1009`.
- Outbound frames are unbounded in principle but bounded in practice: the
  largest is a snapshot at nine players, which is a few kilobytes.
- Server sends `ping` every 15s. A connection with no `pong` for 45s is
  treated as gone and the room applies `game.ActionDisconnect`.

## 3. Identity and sessions

Three identifiers, with different secrecy:

| Identifier | Shape | Secret? | Purpose |
|---|---|---|---|
| Room code | 6 chars from `ABCDEFGHJKLMNPQRSTUVWXYZ23456789` (no `I`, `O`, `0`, `1`) | no | shareable join handle |
| Player id | server-assigned opaque string, ≤ `game.MaxPlayerIDLength` (64) | no | seat identity, appears in every event |
| Rejoin token | 256 bits of CSPRNG output, base64url | **yes** | proves you are that player |

The rejoin token is the credential that protects a player's hidden cards. It
must be:

- generated server-side from `crypto/rand`, never derived from the player id,
  room code, seat or clock;
- scoped to one `(room, player)` pair, so a token for one room is useless in
  another;
- returned exactly once, in the `welcome` message, over TLS;
- **never logged**, never included in an error message, never echoed back in
  any server→client payload, and never placed in a URL or query string.

The player id is *not* a credential. Knowing someone's player id must not let
you act as them or see their cards.

## 4. Envelope

Client → server:

```json
{ "type": "make_claim", "reqId": "c17", "payload": { } }
```

`reqId` is an opaque client string echoed in the reply so the client can
match request to outcome. The server never interprets it.

Server → client:

```json
{ "type": "event", "seq": 42, "reqId": "c17", "payload": { } }
```

`seq` is `game.Event.Seq`, assigned by the reducer. It is monotonic per room
and is the only ordering authority. `reqId` is present only on messages
caused by a specific request.

## 5. Client → server messages

Each maps to exactly one `game.Action`. There are no others, and in
particular there is no client-originated `tick`: `game.ActionTick` is
engine-internal and a client frame of that type is an immediate
`ERR_UNKNOWN_ACTION`.

| `type` | Action | Payload |
|---|---|---|
| `join` | `ActionJoin` | `{ "roomCode": "...", "name": "..." }` |
| `resume` | — (see §8) | `{ "roomCode": "...", "playerId": "...", "token": "...", "lastSeq": 41 }` |
| `set_ready` | `ActionSetReady` | `{ "ready": true }` |
| `start_game` | `ActionStartGame` | `{}` |
| `make_claim` | `ActionMakeClaim` | `{ "claim": { ...claim... } }` |
| `call_show` | `ActionCallShow` | `{}` |
| `leave` | `ActionDisconnect` | `{}` |
| `pong` | — | `{}` |

The `PlayerID` and `Now` on `game.Meta` are filled by the server from the
authenticated session and the server clock. A client cannot set either. This
is what makes the 60-second budget authoritative and impersonation
impossible.

### Claim wire form

Already defined by `poker.Claim`'s JSON marshalling, so the protocol inherits
it rather than restating it:

```json
{ "category": "FULL_HOUSE", "primary": "8", "secondary": "4" }
{ "category": "FLUSH",      "primary": "Q", "suit": "HEARTS" }
{ "category": "ROYAL_FLUSH", "suit": "SPADES" }
{ "category": "HIGH_CARD",  "primary": "3" }
```

`primary` is the claim's principal rank — high card, pair rank, trips rank,
quads rank, straight high or flush high. `secondary` is the low pair or the
full-house pair. Unused fields are omitted. Unmarshalling routes through the
constructors, so a claim arriving from the wire is canonicalised before the
engine sees it; `Claim.Validate()` then rejects anything outside the
367-claim space as `ERR_MALFORMED_CLAIM`.

### Not exposed to clients in v1

`ActionAdvanceRound` is **not** a client message. The engine lets any seated
player advance the round, which would let one player cut the reveal window
short for everyone else. The room's own timer advances the round when
`RoundEndDeadline` passes. This is a transport policy, not a rule change: the
engine behaves identically either way.

## 6. Server → client messages

| `type` | When | Payload |
|---|---|---|
| `welcome` | after `join` or `resume` | `{ "playerId": "...", "token": "...", "roomCode": "...", "view": { ...PlayerView... } }` |
| | | **`token` is present only on `join`.** On `resume` it is empty, because §3 guarantees the token is returned exactly once and the resuming client already holds it. |
| `snapshot` | after `resume`, or whenever the server cannot guarantee event continuity | `{ "view": { ...PlayerView... } }` |
| `event` | one per `game.Event`, in `seq` order | the projected event (§7) |
| `view` | after any batch of events, so the client never has to fold events into state | `{ "view": { ...PlayerView... } }` |
| `error` | a rejected request | `{ "code": "ERR_...", "message": "..." }` |
| `claim_space` | once, on connect | `{ "claims": [ ...367 claims, ascending... ] }` |
| `ping` | every 15s | `{}` |
| `closed` | room is shutting down | `{ "reason": "..." }` |

### Why both `event` and `view`

Events are for animation and narration — "B called SHOW", "A was eliminated".
The view is the truth. Sending the view after every event batch means the
client never has to implement a reducer, and therefore cannot drift from the
server. A client that ignores every `event` and renders only `view` must
still be fully playable; that is the test for whether any gameplay logic has
leaked into the client.

### `claim_space`

The 367-claim space is static and public, so it is sent once per connection
rather than per turn. Combined with `view.standingOrdinal`, the client's claim
builder offers ordinals strictly above the floor. That is an integer
comparison against a server-supplied list — not rule knowledge — and the
server revalidates every claim on arrival regardless.

## 7. Event projection is mandatory

`game.Event` carries a `Hands` map. For `EvCardsDealt` that map contains
**every player's hand**, because the reducer is a pure value and cannot emit
a different payload per recipient.

Therefore: **the raw event stream must never be broadcast.** Every outbound
event goes through `game.ProjectEvent(e, viewer)`, which strips all but the
viewer's own hand from `EvCardsDealt`, passes `EvCardsRevealed` through whole
because a reveal is public, and returns everything else unchanged. It also
deep-copies, so the transport may serialise freely.

`ProjectEvents(events, viewer)` does a batch. There is no other supported way
to get events onto a socket.

One further precaution: hidden fields are sent as explicit `null`, not
omitted, and every event other than `EvCardsDealt` is byte-identical for
every recipient. Otherwise message size and timing become a side channel for
inferring hidden cards.

## 8. Sequencing and reconnect

`Event.Seq` is monotonic per room. The client tracks the highest `seq` it has
processed.

```
client                          server
  |-- resume{lastSeq: 41} ------->|
  |                               | authenticate token against (room, player)
  |<-- welcome{view} -------------|  full current view, always
  |<-- event{seq:42} -------------|  buffered events after lastSeq, if held
  |<-- event{seq:43} -------------|
  |<-- view ---------------------|
```

Rules:

- The server **always** sends a full view on resume. The client's `lastSeq`
  is a hint for replaying animations, never a substitute for state.
- The room keeps a bounded ring of recent events (say 256). If `lastSeq` is
  older than the ring, the server sends `snapshot` and no events; the client
  renders the new state without animation.
- The reconnecting client is never trusted to describe its own state. It
  sends only `lastSeq`, and even that is advisory.
- A resume with a bad token is `ERR_UNAUTHORIZED` and closes the connection.
  The reply must be identical whether the player exists, the room exists, or
  the token is simply wrong, so the error cannot be used to enumerate rooms
  or players.
- On successful resume the room applies `game.ActionReconnect`, which returns
  the player to `ACTIVE`.

**Backpressure.** Each connection has a bounded send queue. If it fills, the
server closes that connection rather than dropping messages: a `seq` gap with
no close would leave the client silently stale. The client then resumes and
gets a snapshot. Dropping frames is never acceptable; dropping connections is.

**Draining on release.** A closing room queues its `closed` message and then
releases the subscriber. Both become ready at once, and a naive `select`
picks between them at random, so the write pump drains everything already
queued before honouring the release. Without that the client is dropped
without ever learning why. This was a real bug found by the room tests, not a
theoretical one.

**Idle is about the game, not the chatter.** A room whose players are all
silent still progresses: turns time out, SHOWs auto-resolve, rounds advance.
Any tick that produces events therefore counts as activity for the idle
timer. Counting only client commands closed rooms that were playing
perfectly well under the timeout policies.

## 9. Error codes

One stable code per engine error, so the mapping is a table lookup and not a
judgement call.

| Code | Engine error | Notes |
|---|---|---|
| `ERR_WRONG_PHASE` | `ErrWrongPhase` | |
| `ERR_NOT_YOUR_TURN` | `ErrNotYourTurn` | also a second claim in one turn |
| `ERR_NOT_SHOW_ELIGIBLE` | `ErrNotShowEligible` | |
| `ERR_NO_STANDING_CLAIM` | `ErrNoStandingClaim` | the round starter cannot SHOW |
| `ERR_CLAIM_NOT_HIGHER` | `ErrClaimNotHigher` | equal, lower, or standing claim is the maximum |
| `ERR_MALFORMED_CLAIM` | `ErrMalformedClaim` | outside the 367-claim space |
| `ERR_PLAYER_ELIMINATED` | `ErrPlayerNotActive` | |
| `ERR_GAME_OVER` | `ErrGameOver` | |
| `ERR_NOT_ENOUGH_PLAYERS` | `ErrNotEnoughPlayer` | |
| `ERR_TABLE_FULL` | `ErrTooManyPlayers` | |
| `ERR_GAME_IN_PROGRESS` | `ErrGameInProgress` | |
| `ERR_ALREADY_JOINED` | `ErrAlreadyJoined` | |
| `ERR_NOT_ALL_READY` | `ErrNotAllReady` | |
| `ERR_INVALID_PLAYER_ID` | `ErrInvalidPlayerID` | empty or over 64 bytes |
| `ERR_UNKNOWN_PLAYER` | `ErrUnknownPlayer` | |
| `ERR_UNKNOWN_ACTION` | `ErrUnknownAction` | unknown type, or a client-sent `tick` |
| `ERR_UNAUTHORIZED` | — transport | bad or missing rejoin token |
| `ERR_ROOM_NOT_FOUND` | — transport | |
| `ERR_RATE_LIMITED` | — transport | |
| `ERR_MESSAGE_TOO_LARGE` | — transport | frame over 4 KiB |

`message` is a human string for debugging. Clients branch on `code` only.

**Errors must not leak.** No error may mention a card, a pool size that is
not already public, a witness, another player's hand, or any evaluator
internal. A rejected claim says the claim was not higher; it never says why
the cards would not have supported it.

## 10. Abuse assumptions

The client is hostile. Concretely, the transport must assume and defend
against:

- **Impersonation** — every action's `PlayerID` comes from the authenticated
  session, never from the frame. A frame containing a `playerId` field for an
  action is rejected outright rather than ignored.
- **Replay** — the engine is idempotent only in the sense that a rejected
  action changes nothing. A duplicated `make_claim` will be rejected as
  `ERR_NOT_YOUR_TURN` because the turn has moved on, which is the correct
  outcome and needs no extra machinery.
- **Flooding** — per-connection token bucket, roughly 20 messages per 5
  seconds, before parsing. Exceeding it is `ERR_RATE_LIMITED`; sustained
  abuse closes the connection.
- **Malformed JSON, wrong types, unknown fields, deep nesting** — reject with
  a typed error, never panic, keep the connection. A `recover` boundary
  around the room goroutine ensures one bad room cannot take down the
  process or any other room.
- **Timing side channels** — see §7.
- **Stalling** — a client that never acts is resolved by the engine's turn
  timer under `TimeoutAutoShow`. A client that never responds to `ping` is
  disconnected. Neither can hold a round open.

## 11. Room lifecycle

One goroutine per room, owning the state exclusively. All actions arrive on a
buffered command channel and are applied strictly serially, which is what
makes concurrent mutation structurally impossible rather than merely guarded.
The turn timer is a `time.Timer` owned by that goroutine which enqueues a
`TICK` onto the same channel, so a claim landing in the same millisecond as
expiry cannot double-resolve.

```
WS conn --+
WS conn --+--> room.cmdCh --> [single goroutine] --> Apply --> events
WS conn --+                          |                           |
                                     |                  ProjectEvent(e, p)
                                     |                  View(s, p)
                                     +-- recover boundary          |
                                                                   v
                                                          per-conn send queues
```

Lifecycle states, and what closes a room:

| Trigger | Action |
|---|---|
| Last connection closes in `WAITING_FOR_PLAYERS` | close room after a short grace period |
| Game reaches `GAME_OVER` with a winner | keep the room briefly so clients can see the result, then close |
| Game reaches `GAME_OVER` abandoned (no winner) | close immediately; nobody is left to see anything |
| No action for a long idle period | close |

The abandoned case is real: round-end pruning eliminates every
still-disconnected player at once, so a table that empties during a round
ends with nobody in play and no winner, and `GameEnded` carries
`Reason: "ABANDONED"`. The room must handle that terminal state rather than
assuming a winner exists. See the rule ledger, section C5.

## 12. A worked round

Four players, round 1, a reduced pool of four cards. Server→client shown for
player B only.

```
B -> join{roomCode:"K7QMZ4", name:"B"}
B <- welcome{playerId:"p_b3f...", token:"<secret>", view:{phase:"WAITING_FOR_PLAYERS", ...}}
B <- claim_space{claims:[367 entries]}

(all four ready; A sends start_game)

B <- event{seq:9,  type:"GAME_STARTED", seat:0}
B <- event{seq:10, type:"ROUND_STARTED", round:1, poolSize:4, mode:"REDUCED_POOL"}
B <- event{seq:11, type:"CARDS_DEALT", hands:{"p_b3f...":["8H"]}}      <- projected
B <- event{seq:12, type:"TURN_STARTED", player:"p_a1c...", deadline:"..."}
B <- view{view:{round:1, poolSize:4, evalMode:"REDUCED_POOL", handWidth:4,
                standingOrdinal:-1, yourTurn:false, canShow:false,
                players:[{seat:0,handSize:1,cards:null},
                         {seat:1,handSize:1,cards:["8H"],isYou:true}, ...]}}

(A claims Pair of 8s)

B <- event{seq:13, type:"CLAIM_MADE", player:"p_a1c...", claim:{category:"PAIR",primary:"8"}}
B <- event{seq:14, type:"TURN_STARTED", player:"p_b3f...", deadline:"..."}
B <- view{view:{standingOrdinal:18, yourTurn:true, canShow:true, canClaim:true, ...}}

B -> call_show{reqId:"c4"}

B <- event{seq:15, type:"SHOW_CALLED", reqId:"c4", player:"p_b3f..."}
B <- event{seq:16, type:"CARDS_REVEALED", hands:{all four hands}}       <- public
B <- event{seq:17, type:"CLAIM_VALIDATED", result:{claimTrue:true,
              witness:["3C","8H","8S","KD"], loser:"p_b3f...",
              mode:"REDUCED_POOL"}}
B <- event{seq:18, type:"PENALTY_APPLIED", player:"p_b3f...", handSize:2}
B <- event{seq:19, type:"ROUND_ENDED", round:1, player:"p_b3f..."}
B <- view{view:{phase:"ROUND_END", revealed:true, yourTurn:false,
                turnPlayer:null, lastShow:{...}, roundEndDeadline:"..."}}

(room timer fires after RoundEndDuration)

B <- event{seq:20, type:"ROUND_STARTED", round:2, poolSize:5, mode:"EXACT_FIVE"}
B <- event{seq:21, type:"CARDS_DEALT", hands:{"p_b3f...":["QC","7D"]}}  <- projected
...
```

Note what B never receives: the deck, the seed, the pool contents before the
reveal, the ownership map, any other player's cards before the reveal, and no
`turnPlayer` at all while the phase is `ROUND_END`.

## 13. Test plan for the transport

Mirrors §9 of `test-matrix.md`, plus what this document adds:

| Test | Assertion |
|---|---|
| `TestImpersonationRejected` | an action naming another player's id is rejected, not silently reassigned |
| `TestClientTickRejected` | a client frame of type `tick` is `ERR_UNKNOWN_ACTION` |
| `TestOversizedFrameClosesConnection` | frame over 4 KiB closes with 1009 |
| `TestMalformedJSONKeepsConnection` | typed error, no panic, socket survives |
| `TestEveryOutboundEventIsProjected` | fuzz a full game and assert no socket payload contains a card the recipient does not own before the reveal |
| `TestEventPayloadsIdenticalExceptDeal` | only `CARDS_DEALT` differs per recipient |
| `TestResumeSendsFullView` | resume always yields a view, regardless of `lastSeq` |
| `TestResumeBeyondRingSendsSnapshot` | a stale `lastSeq` yields `snapshot` with no events |
| `TestBadTokenIsIndistinguishable` | wrong token, unknown player and unknown room produce the same reply |
| `TestTokenNeverAppearsOutsideWelcome` | scan every outbound frame of a full game for the token |
| `TestBackpressureClosesRatherThanDrops` | a stalled reader is closed; no `seq` gap is ever delivered |
| `TestConcurrentClaimsOneWinner` | N clients claim simultaneously, exactly one is accepted |
| `TestShowRacesTimeout` | a `call_show` arriving as the timer expires resolves exactly once |
| `TestRoomPanicContained` | injected panic kills neither the process nor another room |
| `TestClientWithNoReducerIsPlayable` | a client that ignores all `event` messages and renders only `view` can complete a game |

The last one is the real test of §1. If it fails, gameplay logic has leaked
into the client. **It passes:** a client that reads only views and the static
claim ladder plays a five-player game to completion.

Implemented in `internal/transport/ws/ws_test.go` against a substituted
socket, so framing, size limits, heartbeat expiry and backpressure are
deterministic, plus `live_test.go` which runs the gorilla adapter and the
HTTP surface over a real `httptest` server once, end to end, so the adapter
is not the untested gap between the suite and production.

## 14. Implementation decisions for Phase 3 (none are gameplay)

- **Room registry**: in-memory `map[string]*Room` behind a mutex. Nothing in
  the rules needs persistence; revisit only for match history.
- **Join flow**: HTTP `POST /rooms` to create and `GET /rooms/{code}` to
  check existence, then WebSocket upgrade at `/ws`. Keeps room creation out
  of the socket path.
- **Origin checking and TLS** are required in production; the dev server may
  relax origin only behind an explicit flag.
- **Observability**: structured logs keyed by room code and `seq`, with card
  values and tokens redacted at the logger, not at the call site. A log line
  that can contain a card value is the same bug class as a leaked event.

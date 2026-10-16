# Phase 5 — Hardening Report

Gameplay rules are unchanged. No rule ambiguity was encountered, so nothing
was decided. Everything below is transport, session, room or resource work.

---

## Defects found and fixed

Five real bugs. The first four were found by tests written to fail; the
fifth was found by the stress harness.

### 1. Two goroutines wrote to one socket (critical)

Error replies were written from the read goroutine while the write pump
wrote room updates and the heartbeat wrote pings. A WebSocket connection is
not safe for concurrent writers: two goroutines interleave two frames and
corrupt the stream.

**The race detector could not see it.** Each individual write was internally
synchronised, so there was no data race to report — just two frames spliced
together. The Phase 3 test double made it worse by serialising writes behind
a mutex, which made the code look correct.

Found by `raceConn`, a test connection that counts overlapping writes rather
than protecting against them. It reported **40 overlaps** in a few hundred
milliseconds.

Fixed by making the write pump the sole writer once it starts. Everything
else queues on an outbox. A fatal error is enqueued with a completion signal
and waited on, so the client still learns why it was dropped.

### 2. Display names were sliced mid-rune

`name[:32]` splits a multi-byte rune, producing invalid UTF-8 that then
round-tripped through JSON as replacement characters — and came out **36
bytes**, longer than the 32-byte limit it was meant to enforce. Every other
player saw the mojibake in their view.

Fixed with `protocol.SanitizeName`: coerce to valid UTF-8, strip control
characters, then truncate on a rune boundary.

### 3. Invalid UTF-8 frames were accepted

RFC 6455 requires a text frame to carry valid UTF-8, and `encoding/json` is
lenient enough not to notice. `DecodeInbound` now rejects invalid frames.

### 4. A sticky read deadline poisoned later reads

Found in my own test helpers, but it is a real property worth writing down:
in gorilla, an exceeded read deadline **permanently corrupts the
connection**, and the deadline persists after the read that set it. So
polling `ReadMessage` with a short deadline kills the connection on the
first timeout, and a blocking read started afterwards inherits the poisoned
deadline.

Two consequences. The stress client panicked with *repeated read on failed
websocket connection*. And the log-redaction audit silently captured zero
card codes, so its grep was proving nothing — caught only because the test
asserts it captured something before trusting its own result. Both now use a
blocking reader goroutine started at dial time, which is what a real client
must do.

### 5. Idle reaping could kill an active game (fixed during the Phase 3 audit)

Only client commands refreshed the activity clock, so a room progressing
purely through the engine's timeout policies was reaped mid-game. Any tick
that produces events now counts as activity.

Also fixed defensively: dead subscribers are reaped after every command, not
only after a fan-out, so a handler that emits no events cannot leave a
dropped connection in the subscriber set. A room that believes it still has
listeners never becomes empty, never reaps, and leaks its actor goroutine.

---

## Audits

### Concurrency and the room actor

| Stressed | Test |
|---|---|
| many rooms concurrently | `TestMultipleRoomsAndSessionsConcurrently`, stress harness |
| many clients per room | connection caps, stress harness |
| simultaneous commands to one room | `TestConcurrentCommandsToSameRoomYieldOneWinner`, `TestConcurrentCommandsAreSerialised` |
| simultaneous disconnect/reconnect | `TestDetachRacesCloseAndReconnect`, stress churn |
| timers firing while commands arrive | `TestResumeThreatModel/while timers fire`, stress harness |
| shutdown with commands queued | `TestQueuedActionsCannotResurrectAClosedRoom` |
| reconnect racing detach | `TestDetachRacesCloseAndReconnect` |
| duplicate connections per player | `TestOnePlayerManyConnections` |
| overlapping socket writes | `TestNoConcurrentWritesToOneConnection` |

**Exactly one state transition per accepted action** holds by construction:
`game.Apply` is called from one goroutine per room, and a rejected action
returns the prior state untouched and emits nothing. The claim chain is
asserted strictly ascending after concurrent floods, which is the
observable form of that property.

### Room lifecycle

`CREATED → ACTIVE → GAME_OVER / ABANDONED → CLOSED`, audited by
`TestQueuedActionsCannotResurrectAClosedRoom`, `TestRoomClosesWhenEmptyAndIsReaped`,
`TestAbandonedGameClosesRoomImmediately`, `TestGameOverDeliveredToEveryClient`
and `TestDeadSubscribersDoNotKeepRoomsAlive`:

- a closed room answers `ERR_ROOM_CLOSED` to every entry point, including
  `Snapshot`;
- queued commands cannot revive the actor — it has already returned, and
  `submit` is released by the closed done channel rather than hanging;
- a tick on a closed room is a no-op;
- subscribers are always released, and every one is asserted released after
  a close under load;
- the registry reference is removed exactly once, from the actor's single
  deferred shutdown;
- the close reason reaches the client **before** the socket closes, which
  needed the flush-on-release fix;
- idle reaping cannot end an active game (defect 5);
- an abandoned game closes immediately, since nobody is left to see a
  result.

### Reconnection and authentication

`TestResumeWithInvalidTokenIsIndistinguishable` covers wrong token, empty
token, unknown player, unknown room and another room's code — all producing
exactly one `ERR_UNAUTHORIZED` and no view. `TestResumeCannotObtainAnotherPlayersView`
covers a valid token presented with someone else's player id.
`TestResumeThreatModel` covers resume after game over, concurrent resumes,
and resume during a timer storm with rounds changing underneath.

The credential digest is `sha256(room ‖ player ‖ token)`, compared in
constant time, with an unknown player compared against a zero digest rather
than short-circuiting, so neither the reply nor its timing distinguishes the
failure. `obs.ResumeRejected` deliberately takes no reason argument: a log
field is one grep away from becoming an enumeration API.

`session.Token` redacts through `String`, `GoString` and `MarshalJSON`;
`TestTokenRedactsEverywhereButReveal` exercises eight formatting paths plus
JSON, including embedding in a struct and a map.

### Information leakage

`TestProtocolLeakAudit` plays a game with disconnects and a rejected resume,
then sweeps **every frame of every message type** for four properties:

1. no card a recipient does not own, before that recipient's reveal;
2. no rejoin token outside the one welcome that issued it, and never another
   player's token;
3. **no capitalised JSON key anywhere** — that is the signature of an
   untagged Go struct, and it is how internal representation leaks by
   accident. A bare `rank`/`suit` pair outside a claim is also rejected,
   which is what a `poker.Card` without `MarshalJSON` would look like;
4. no forbidden concept at all: seed, digest, secret, ownership map, deck
   order.

`TestLogsNeverCarrySecretsOrCards` greps real debug-level logs against the
issued tokens and 18 distinct observed card codes, and asserts the useful
operational events are present. `TestNoDebugEndpointsAreRegistered` checks
eight paths including `/debug/pprof/` and `/metrics` are not served.
`TestErrorMessagesCarryNoCards` sweeps every error code's payload.

Panic messages: the recovered value is never logged. It can be any value
from anywhere in the actor, which is exactly the kind of thing that carries
state.

### WebSocket robustness

Malformed JSON, unknown types, missing fields, **extra fields** (an extra
`playerId` beside an action is an impersonation attempt, so unknown fields
are rejected rather than ignored), invalid UTF-8, oversized frames,
fragmented frames (gorilla reassembles; the read limit applies to the
reassembled message), rapid action spam, duplicate actions, wrong-phase
actions, wrong-player actions, a wedged writer, and a client vanishing
without a close — all covered.

A slow client cannot block the actor: `sendTo` never blocks, and
`TestSlowClientBlocksNobody` wedges one socket and asserts the rest of the
table plays on and the timers keep firing.

### Backpressure

**Defined behaviour:** when a subscriber's outbound queue fills, the room
closes that connection. It does not drop authoritative messages. A dropped
frame would leave a `seq` gap the client cannot detect; a closed connection
is unmissable, and the client reconnects and receives a full view, which the
protocol guarantees on every resume. Dropping connections is acceptable,
dropping frames is not.

The one exception is the connection's own outbox, which carries only error
replies and pings. Those are advisory, not authoritative state, so a full
outbox drops them — and the authoritative path has its own rule above.

### Protocol consistency

`ProtocolVersion = 1`, with the strategy written down: additive changes do
not bump; removing or renaming a field, changing a type or meaning, or
changing a required order does. A client declares `v` on join or resume;
omitting it is accepted so the field's introduction locks nobody out;
anything else is `ERR_UNSUPPORTED_VERSION`. The server states its version in
every welcome. `TestProtocolVersionNegotiation` covers all three cases.

Round trips are tested for every public wire type: all 367 claims, the claim
space message, cards, eval modes, and a full `PlayerView` carrying a show
result with a witness.

### Replay and idempotency

Audited, and implemented at the session layer. `ReqID` is the idempotency
key: a repeated id from the same player is a retry of one intent, answered
by re-delivering the current view rather than applying anything twice. Keys
are **per player**, not global.

This deliberately does not live in the engine. The engine decides whether an
action is legal; remembering who asked for what is a session concern.

Worth noting the engine was already safe without it — a replayed claim is
rejected because the turn has moved on — so this is belt and braces plus a
correct ack model for clients. The one new protocol requirement: a client
must use a fresh `ReqID` per distinct action, documented on the field.

### Resource limits

Server protection only. The 4–9 player rule is untouched.

| Limit | Default |
|---|---|
| rooms per server | 512 |
| connections per room | 32 |
| connections per player | 4 |
| concurrent handshakes | 256 (refused with 503 + `Retry-After`) |
| inbound frame | 4 KiB |
| display name | 32 bytes, rune-safe |
| outbound queue per connection | 64 messages, then the connection closes |
| messages per connection | 20 per 5s, then the connection closes |
| room lifetime after a result | 60s |
| empty room grace | 30s |
| idle room | 30 minutes |

All four caps are tested in `TestResourceLimits`.

### Observability

`internal/obs` exposes a **closed set of typed events**, not a general
logger. There is no `Info(msg, args ...any)` that could be handed a card, a
token or a private view. Adding a new log line means adding a method, which
is the moment to think about whether its arguments are safe.

Fourteen counters back the stress report. Player ids and room codes are
logged because they are public by design; nothing else about a player is.

---

## Final verification

```
gofmt -l .            clean
go vet ./...          clean
go build ./...        OK
go test ./...         PASS   (all 9 packages)
go test -race ./...   PASS   (all 9 packages, run in batches for time)
```

### Stress run

`go run ./cmd/stress -rooms 24 -duration 10s -churn 12`

View-only clients over real WebSockets: they read the authoritative view and
the static claim ladder and ignore the event stream entirely, so a clean run
is also evidence no gameplay logic is needed on the client.

| | |
|---|---|
| games completed | 57 |
| actions sent | 2,714 |
| reconnects | 2,861 |
| disconnects | 2,861 |
| room closures | 81 (of 81 created) |
| players joined | 496 |
| resumes accepted / rejected | 2,861 / 0 |
| games ended / abandoned | 57 / 2 |
| protocol errors | 39 (expected: races between a client's intent and the turn moving on) |
| backpressure drops | 0 |
| limit rejections | 0 |
| actor panics | 0 |
| connections total / still open | 3,357 / 0 |
| goroutines before / after | 2 / 1 |
| live rooms after shutdown | 0 |
| unparseable frames | 0 |
| hidden-card leaks | 0 |
| failures | 0 |

`go run -race ./cmd/stress -rooms 12 -duration 8s -churn 15`: 4 games, 258
actions, 280 reconnect cycles, 16 of 16 rooms closed, 377 connections all
closed, goroutines 2 → 1, **no races reported**, zero leaks, zero panics.

### Leaked resources

None. Goroutines return to baseline, the room registry empties, every
subscriber is released, and every connection closes.
`TestNoGoroutineLeaks` independently opens and tears down 24 rooms with 96
connections and measures 2 goroutines before and 2 after.

---

## Protocol and security findings for the record

1. **A closing room's reason is delivery-ordered.** The `closed` message is
   queued before the subscriber is released, and both the write pump and any
   consumer must drain before honouring the release. This is now protocol
   text, not an implementation accident.
2. **Cross-connection ordering is not guaranteed.** Frame order holds per
   connection only. A client must act on its own view, never on the
   assumption that another client's action has landed. This bit three of my
   own tests before I wrote it down, and a real frontend will hit it too —
   it belongs in the Phase 4 client contract.
3. **`ReqID` is now semantically load-bearing.** Reusing one for a different
   action silently suppresses it. Documented on the field.
4. **No open protocol decisions.** Nothing required inventing semantics.

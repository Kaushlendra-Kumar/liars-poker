# Release Checklist

Invariants that must never regress. Each one names the tests that guard it,
so this is a runnable checklist rather than a statement of intent: if an
invariant is violated, a named test fails.

Run the whole list before any release, and again after fixing any defect the
Docker verification exposes.

```bash
cd backend  && gofmt -l . && go vet ./... && go build ./... && go test ./...
cd backend  && go test -race ./internal/protocol/ ./internal/session/ ./internal/room/ ./internal/game/
cd backend  && go test -race ./internal/transport/ws/
cd backend  && go test -race ./pkg/poker/
cd backend  && go test -race ./internal/sim/
cd frontend && npx tsc --noEmit && npm test && npm run e2e && npx next build
cd deploy   && ./smoke.sh "$BASE_URL"
```

---

## Gameplay rules

### 1. Gameplay rules are authoritative in Go

No rule is implemented, duplicated or second-guessed in TypeScript. The
client renders what the server says and sends intents back.

- `ws.TestClientWithNoReducerIsPlayable` — a client that ignores every event
  and reads only views completes a game
- `frontend/test/store.test.ts` — an event leaves `view` referentially
  unchanged
- `frontend/test/ui.test.tsx` — actions are enabled purely from `canClaim` /
  `canShow`

**Regression signal:** any claim comparison, hand classification, turn rule
or timer decision appearing in `frontend/src`.

### 2. 367 fixed public claims

The claim space is one fixed public list with no configuration switch.
High Card 3–6 are members; High Card 2 is excluded.

- `poker.TestClaimSpaceSizeAndBijection` — exactly 367, ordinals a bijection
  onto 0..366, per-category band sizes
- `game.TestClaimSpaceIsFixedAt367` — the four reduced-pool High Card claims
  present, High Card 2 absent
- `protocol.TestClaimSpaceMessageIsTheWholeLadder` — round-trips whole
- `poker.TestClassifyExhaustive` — every classification is a legal claim
- `frontend/test/store.test.ts` — a 367-entry ladder filters correctly
- `frontend/test/e2e/smoke.test.ts` and `deploy/smoke.sh` — 367 delivered
  over a real socket

`poker.ClaimSpaceSize` is a constant and `init()` panics if the enumeration
does not produce exactly 367.

### 3. One ownership rule at every pool size

The claimant must own at least one card of the evaluated hand. **Not a
pattern card — any card.** No per-mode scope, no configuration knob.

- `poker.TestOwnershipIsAnySelectedCardEverywhere` — reduced pool with the
  pair owned by others is TRUE; five cards with all four pattern cards owned
  by others is TRUE; a six-card pool whose only full house excludes the
  claimant is FALSE
- `game.TestOwnershipRuleIsUniformAcrossModes` — the same three cases
  through the engine
- `poker.TestReducedModeOwnershipIsAlwaysSatisfied` — below five cards the
  rule can never fail, so claim truth is claimant-independent
- `frontend/test/ui.test.tsx` — the UI states the rule identically in both
  modes

**Regression signal:** any reappearance of an `OwnershipScope` type, a
`ValidationConfig`, or a pattern-card requirement.

### 4. Fewer than 5 cards evaluates the complete pool

- `poker.TestReducedPoolFourCardPair`
- `poker.TestReducedPoolFourCardHighCard`
- `poker.TestReducedPoolExactClassificationStillApplies`
- `game.TestFourPlayerRoundOneUsesReducedPool` — pool of 4, mode
  `REDUCED_POOL`, and the pool's classification is true for every in-play
  player
- `sim.TestReducedRoundsOccurAtFourPlayers`

### 5. Five or more cards evaluates exact five-card subsets

- `poker.TestSixPlusCardsDoNotAllParticipate` — the extra card is excluded;
  the same three 8s are TRUE at six cards and FALSE at five
- `poker.TestTransitionToExactFiveMode` — the mode flips at five
- `poker.TestConstructiveMatchesExhaustive` — 39,636 comparisons, pool sizes
  2 to 10, constructive validator against the exhaustive oracle

`poker.HandSize(pool) = min(5, len(pool))` is the whole rule; there is one
code path, not two.

### 6. Exact classification required

A stronger available hand never validates a weaker claim. Satisfaction is
`Classify(hand) == claim`, one struct equality.

- `poker.TestClassifyExhaustive` — all 2,598,960 five-card hands; the
  category histogram matches the standard poker distribution exactly
- `poker.TestClassifyReducedWidthsAreLegalClaims` — every hand of 2, 3 and 4
  cards; five-card-only categories provably unreachable
- `poker.TestStraightShadowedByStraightFlush`
- `poker.TestFillerDistinctness`, `poker.TestFlushDeclaredHighCardIsTheMaximum`

### 7. Impossible claims remain legal where specified

Bluffing is the game. Truth is tested only at SHOW.

- `game.TestImpossibleClaimIsAccepted` — `Four Aces` accepted with no ace in
  play
- `frontend/test/ui.test.tsx` — the builder offers a Flush against a
  four-card pool and says bluffing is legal
- `protocol.TestActionForDoesNoRuleChecking` — the protocol forwards a claim
  it knows the engine may reject

Structurally malformed claims are still rejected:
`poker.TestMalformedClaimsRejected`, `game.TestMalformedClaimRejected`.

### 8. Only the immediate next player can SHOW

- `game.TestOnlyImmediateNextPlayerCanShow` — every other seat gets
  `ERR_NOT_SHOW_ELIGIBLE`
- `game.TestStarterCannotCallShow` — `ERR_NO_STANDING_CLAIM`
- `ws.TestJoinStartAndPlayThroughTheStack` — enforced through the transport

### 9. Abandoned games do not invent a winner

A table that empties out ends with nobody in play and **no winner**. The
rules define a winner only for one-player-remaining, and the engine does not
guess.

- `game.CheckInvariants` I13 — a winner is named exactly when one player
  remains
- `ws.TestAbandonedGameClosesRoomImmediately`
- `game.TestGameEndsAndRejectsActions`
- `sim.TestPresenceChurnHoldsInvariants` — abandoned games occur under churn
  and are counted, not treated as failures

---

## Information security

### 10. Hidden cards never cross the projection boundary

- `ws.TestProtocolLeakAudit` — plays a game with disconnects and a rejected
  resume, then sweeps **every frame of every message type** for: a card the
  recipient does not own before their reveal; another player's token; **any
  capitalised JSON key**, which is the signature of an untagged Go struct
  leaking its field names; and the words seed, digest, secret, ownership map
- `ws.TestNoHiddenCardsCrossTheWire`
- `game.TestViewHidesOtherPlayersCards`, `game.TestViewCarriesNoDeckOrOwnership`
- `sim.TestPoliciesNeverSeeHiddenCards` — bots receive only a `PlayerView`
- `frontend/test/leak.test.tsx` — a **hostile** view in which every opponent
  has cards while `revealed` is false yields exactly one card face in the
  DOM and no other card's accessible label anywhere in the markup

The frontend has two independent barriers: `sanitizeView` strips on ingest,
and `visibleCards` gates on `isYou || revealed` at read time. Both must
stay.

### 11. No raw `EvCardsDealt` reaches a client

`EvCardsDealt` carries every player's hand, because a pure reducer cannot
emit a different payload per recipient. It must never be broadcast.

- `protocol.TestEventMessageAlwaysProjects` — projection happens inside the
  only exported path from a `game.Event` to the wire, so a caller who forgot
  cannot leak
- `game.TestEventProjectionStripsOtherHands`
- `ws.TestNoHiddenCardsCrossTheWire` — asserts a delivered `CARDS_DEALT`
  carries exactly one hand

**Regression signal:** any code path serialising a `game.Event` without
going through `protocol.EventMessage`.

### 12. Reconnect credentials are never logged

- `session.TestTokenRedactsEverywhereButReveal` — eight formatting paths
  plus JSON, including struct and map embedding
- `ws.TestLogsNeverCarrySecretsOrCards` — greps real debug-level logs
  against the issued tokens and every observed card code
- `ws.TestTokenAppearsOnlyInJoinWelcome` — the token appears in exactly one
  frame, and no other client ever receives it
- `ws.TestResumeWithInvalidTokenIsIndistinguishable` — five failure modes,
  one undifferentiated `ERR_UNAUTHORIZED`, no view delivered
- `frontend/test/client.test.ts` — the TypeScript `Token` redacts through
  `toString` and `toJSON`

`obs.ResumeRejected` takes **no reason argument**, deliberately: a log field
is one grep away from becoming an enumeration API.

### 13. The client does not implement a reducer or game engine

- `ws.TestClientWithNoReducerIsPlayable` — the acceptance test
- `frontend/test/store.test.ts` — events append to an activity log only
- `frontend/src/protocol/labels.ts` is the only file formatting claim text,
  and it compares nothing

---

## Concurrency and robustness

### 14. The room actor serialises authoritative mutations

One goroutine per room owns the state; every command is applied serially
through `game.Apply`.

- `room.TestConcurrentCommandsAreSerialised` — the claim chain stays
  strictly ascending under a flood
- `ws.TestConcurrentCommandsToSameRoomYieldOneWinner`
- `game.TestApplyIsSafeForConcurrentUse` — sixteen concurrent `Apply` calls
  on one state produce identical results
- `room.TestManagerIsolatesRooms`

**`go test -race` must be clean.** It is the only mechanical check here.

### 15. One WebSocket writer per connection

A socket is not safe for concurrent writers. Two goroutines interleave two
frames and corrupt the stream, and **the race detector cannot see it**
because each write is internally synchronised.

- `ws.TestNoConcurrentWritesToOneConnection` — `raceConn` counts overlapping
  writes instead of protecting against them, and asserts zero

**Regression signal:** any `conn.Write` outside the write pump after
`writerUp` is set.

### 16. Slow clients cannot block the room

- `ws.TestSlowClientBlocksNobody` — a wedged socket, and the rest of the
  table plays on with timers still firing
- `room.TestBackpressureClosesSubscriberRatherThanDroppingMessages`
- `room.TestSeqIsMonotonicAcrossEveryDeliveredMessage`

The contract: **a full outbound queue closes that connection, never drops an
authoritative message.** A dropped frame leaves a sequence gap the client
cannot detect; a closed connection is unmissable and resume delivers a full
view.

### 17. Active timer-driven games are not mistaken for idle rooms

A room whose players are silent still progresses: turns time out, SHOWs
auto-resolve, rounds advance. Any tick producing events counts as activity.

- `ws.TestGameOverDeliveredToEveryClient` — a game driven entirely by timer
  expiry reaches `GAME_OVER` without being reaped
- `ws.TestTurnTimerExpiryAndRoundEndAdvance`
- `ws.TestRoomClosesWhenEmptyAndIsReaped` — genuinely empty rooms still do
  get reaped

**Regression signal:** `lastActivity` refreshed only by client commands.

### 18. Graceful shutdown closes rooms cleanly

- `room.TestClosedRoomRejectsEverything` — a closed room answers
  `ERR_ROOM_CLOSED` everywhere, and every subscriber is released
- `room.TestQueuedActionsCannotResurrectAClosedRoom`
- `room.TestDetachRacesCloseAndReconnect`
- `ws.TestNoGoroutineLeaks`
- Manual: `SIGTERM` closes every room with a reason and logs a summary

The `closed` message is queued **before** the subscriber is released, and
both the write pump and any consumer drain before honouring the release.
Without that a client is dropped without learning why.

---

## Wire representation

### 19. Public slice fields never marshal as `null`

- `protocol.TestSliceFieldInventoryIsComplete` — reflects over all seven
  public payload types and fails if a slice field is not in the guarded list.
  **This is the future-proofing: a new slice field lands here first.**
- `protocol.TestEmptySlicesMarshalAsArrays`
- `protocol.TestFalseShowResultWitnessMarshalsAsArray`
- `protocol.TestSlicesAcrossEveryObservablePhase`
- `protocol.TestCardsIsAbsentRatherThanNull` — the one exemption: `cards` is
  `omitempty`, so an empty hand is *absent*, which means "not yours to see"
  rather than "has no cards"

### 20. Protocol v1 is frozen

- `ws.TestProtocolVersionNegotiation` — version 1 and an omitted version
  accepted; anything else is `ERR_UNSUPPORTED_VERSION`
- `protocol.TestCodeForCoversEveryEngineError` — a new engine error cannot
  silently fall through to `ERR_INTERNAL`
- `protocol.TestErrorMessagesCarryNoCards`
- `protocol.TestViewRoundTripsThroughTheWire`

Additive changes — a new optional field, event type or error code — do not
bump the version. Removing or renaming a field, changing a type or meaning,
or changing a required order does.

### 21. Request ids are idempotency keys

- `ws.TestRequestIDSuppressesDuplicateActions` — a repeated id is answered
  with the current view, not applied twice, and produces no error
- `ws.TestRequestIDIsPerPlayerNotGlobal`

A client must use a fresh `reqId` per distinct action.

---

## Release gate

- [ ] `gofmt -l backend` is empty
- [ ] `go vet ./...` clean
- [ ] `go build ./...` clean
- [ ] `go test ./...` — 158 tests, 9 packages
- [ ] `go test -race` — all four batches clean
- [ ] `npx tsc --noEmit` clean
- [ ] `npm test` — 54 tests
- [ ] `npm run e2e` — 3 tests against a real server
- [ ] `npx next build` succeeds
- [ ] `./smoke.sh` passes against the deployed URL
- [ ] **`HANDOFF.md` §21: all 18 Docker items executed and passing**

The last box is the only one still open. Once it is ticked on a
Docker-capable machine, this is the v1 release candidate.

## Out of scope for v1

Do not add these because they might be useful later. Each is a separate
capability with its own design decisions:

- persistence of any kind
- spectators
- multi-replica room-aware routing
- a Prometheus or metrics endpoint
- browser-persistent token recovery (cookie or `sessionStorage`)
- `RealizableAtWidth` / `minWidth` on the wire
- claim display text on the wire

If the Docker smoke test exposes a concrete defect: **fix only that defect
and re-run this checklist in full.** Otherwise freeze the implementation.

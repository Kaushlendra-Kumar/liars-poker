# Rule Ledger — Revision 4

**All gameplay rules are locked. No open questions remain.**

| | Decision | Status |
|---|---|---|
| Q1 | **Ownership** — one rule at every pool size: the claimant must own at least one card of the evaluated hand. Not a pattern card; any card. No per-mode scope, no config knob. | **LOCKED** |
| Q2 | **Claim space** — 367 claims, fixed public list, no configuration switch. High Card 3, 4, 5 and 6 are official members because hands of width 2 to 4 are legitimate evaluated hands under the reduced-pool rule and the space must contain what they can classify as. High Card 2 stays excluded: a one-card pool cannot occur while two players remain. | **LOCKED** |

Section B below records consequences of the locked rules that the UI and
playtesting will have to account for. Section C is the full ledger.

# B. Consequences of the locked rules

These follow from the rules as you have specified them. I am not proposing
changes; they shape how the game plays and the UI will have to explain them.

## C1. Below five cards, ownership is always satisfied, so claim truth is claimant-independent

This is the sharpest consequence of the single ownership rule. In reduced
mode the evaluated hand *is* the whole pool, and every in-play player is
dealt at least one card into it. So "the claimant owns at least one card of
the evaluated hand" is true for everyone, always. It can never fail there.

Therefore a reduced round has **exactly one true claim — the pool's own
classification — and it is true for every player**:

```text
pool = 8-spade(A) 8-heart(B) K-diamond(C) 3-club(D)   ->  Pair of 8s
  Pair of 8s       -> TRUE for A, B, C and D alike
  everything else  -> FALSE for A, B, C and D alike
```

Two things follow. First, the early game is pure deduction rather than a
question of who holds what: you see one card, you infer the other three, and
if you guess the pool's classification correctly your claim is safe no matter
which card you hold. Second, a player who is challenged on a reduced pool
wins or loses entirely on whether they read the pool right, with no ownership
escape hatch and no ownership trap.

Ownership starts doing real work at five or more cards, where the evaluated
hand is a proper subset that can exclude the claimant. The clearest cases are
Full House, Straight, Flush, Straight Flush and Royal Flush: each consumes
all five cards, so there is no free slot for the claimant's own card, and a
pattern belonging wholly to other players makes the claim false.

## C2. Reduced mode recurs after eliminations, not only at the start

Elimination removes up to five cards from circulation, so the pool can drop
back below five mid-game. Dry-run scenario 15 ends a round with a nine-card
pool and begins the next with a four-card pool, because a five-card player
was eliminated. The engine derives the mode per round from the pool size, so
this is handled — but the UI will need to explain the switch, since the same
claim can be true one round and false the next for reasons that have nothing
to do with the cards.

## C3. Bluffing a five-card-only category at four cards is legal and always false

You asked me not to restrict claim types at input, and I have not: a Flush,
Straight, Full House, Straight Flush or Royal Flush claim against a four-card
pool is accepted and is always false. Correct per your instruction, but the
claim builder will happily offer a player a guaranteed loss. I would suggest
the UI grey these out with a hint rather than hide them, since claiming one
is sometimes still the right bluff against an opponent who does not know the
rule. A Phase 4 frontend decision, not an engine one.

## C4. Under the current bots, challenging beats claiming

From 2,000 simulated games: only about 20% of challenged claims held up, so
`always-show` wins roughly 22% of its seats while `bluffer` wins under 1%.
Only the claimant can be caught lying, so SHOW is strongly positive expected
value against weak claimants.

This says more about bot quality than about the rules: a bot that claimed
only what it could support would push the true rate up.

Worth recording that I predicted the single ownership rule would raise the
true rate by making more claims true, and **measurement says it did not** —
20.1% before the change and 20.1% after, over 1,200 games. Two reasons:
reduced-pool rounds are only about 7% of all rounds, and in exact-five mode
the claims these bots make are false for pattern reasons rather than
ownership reasons, so relaxing ownership changes almost nothing. The
prediction was wrong; the number is the number.

If the true rate stays this low with real players the game collapses into
"always challenge", and that is worth watching during playtesting. No rule
change proposed.

---

# C. Full ledger

| Decision | Resolution |
|---|---|
| **Ownership** | **One rule, every pool size: the claimant must own at least one card of the evaluated hand. Not a pattern card — any card. No per-mode scope, no config knob.** |
| Reduced-pool rule | `HandSize(pool) = min(5, len(pool))`; one quantifier, no separate evaluator, no short-hand hierarchy |
| Full House ordering | `(trips, pair)` lexicographic, 156 distinct claims |
| Two Pair ordering | `(high, low)` lexicographic, canonicalised so the higher pair is first |
| Flush / SF / Royal ordering | suit as least-significant key, Clubs < Diamonds < Hearts < Spades, so the order is total and Royal Flush of Spades is the unique maximum |
| Unrealizable claims | rejected as malformed by claim-space membership, before any card is consulted |
| Ace behaviour | high everywhere; low only in straights and straight flushes, where the high card is 5. Ranks never wrap |
| Kickers | unconstrained in rank; a Pair of 8s may carry an ace kicker |
| Single-suited straight selections | classify as a Straight Flush, so they do not satisfy a Straight claim. Same for a suited broadway against Straight to A |
| First-round starter | uniformly random via the injected seed, recorded in `GameStarted`; `FirstStarterSeatZero` available |
| Turn timeout | 30s budget, `TimeoutAutoShow` default: SHOW if eligible, otherwise the minimum legal claim. `TimeoutMinimumClaim` and `TimeoutForfeitRound` implemented and tested |
| Disconnect | stays in the frozen pool for the current round, eliminated at round end. `DisconnectEliminateImmediately` available |
| Round-end reveal window | `ROUND_END` is an observable phase; advances on `ADVANCE_ROUND` or a tick past `RoundEndDuration` (6s) |
| Witness determinism | the oracle returns the lexicographically smallest; the production path returns a deterministic, not necessarily minimal, witness |
| Claim representation | one validated comparable struct, not an interface, so `Classify(hand) == claim` is the whole satisfaction test |
| RNG injection | `Config.Seed`, each round deriving `rand.NewSource(Seed*1000003 + Round)`, keeping `State` a pure value |
| `PatternCards` | retained as a display helper for highlighting the pattern in a revealed witness; no role in validation |

## C5. A table that empties out has no winner

Round-end pruning eliminates every still-disconnected player at once, so a
game in which everyone leaves ends with nobody in play. The rules define a
winner only for the case where one player remains, so the engine records the
game as abandoned and leaves the winner empty rather than picking one. The
`GameEnded` event carries `Reason: "ABANDONED"` instead of `"WINNER"`.

This was found by the pre-transport audit, not designed in. It is not a
gameplay rule and needs none: what a room does with an abandoned game — close
it, keep it for reconnects, expire it on a timer — is room lifecycle, which
belongs to Phase 3. Flagging it so it is a deliberate choice there rather
than an accident.

## Deferred to later phases

- **Room codes, session tokens, reconnect auth** — Phase 3, `protocol.md`.
  Flagging now: the rejoin token is the credential protecting a player's
  hidden cards, so it must be server-generated, high-entropy, scoped to
  `(room, player)`, and never logged.
- **Spectators** — assuming none for v1. They are an additional
  information-leak surface and would need their own projection.
- **Persistence** — nothing in these rules requires a database. Rooms stay in
  memory for Phase 3; revisit only for match history.

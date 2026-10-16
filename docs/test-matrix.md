# Test Matrix

**Revision 2.** Phase 1 sections are implemented and passing; Phase 2/3
sections remain proposed. Measured results are at the bottom.

Layout follows Go convention: tests live beside the code as `_test.go`,
with whole-game scenarios in `internal/game/scenario_test.go`. The
exhaustive validator is kept permanently as a test oracle in
`pkg/poker/oracle_test.go`.

---

## 1. Cards and deck (`pkg/poker`, `internal/game/deck_test.go`)

| Test | Assertion |
|---|---|
| `TestDeckHas52UniqueCards` | 52 cards, no duplicate `(rank,suit)` |
| `TestShuffleIsPermutation` | shuffled deck is a permutation of the original multiset |
| `TestShuffleDeterministicBySeed` | same seed → identical order, different seed → different order |
| `TestShuffleUniformitySmoke` | chi-square over N shuffles, loose bound (flake-resistant) |
| `TestDealReducesDeck` | `\|deck\| + dealt == 52` after every deal |
| `TestCanonicalCardOrder` | rank asc, then `♣ < ♦ < ♥ < ♠` |

## 2. Classifier (`Classify`, exactly 5 cards)

One table-driven test per category, plus:

| Test | Assertion |
|---|---|
| `TestClassifyExhaustive` | all `C(52,5) = 2,598,960` hands classify into exactly one category; category histogram matches the known poker distribution |
| `TestClassifyAceHighStraight` | `10 J Q K A` mixed suits → Straight, high A |
| `TestClassifyAceLowStraight` | `A 2 3 4 5` mixed suits → Straight, high 5 |
| `TestClassifyNoWraparound` | `K A 2 3 4` → High Card, not a straight |
| `TestClassifyRoyalVsStraightFlush` | `10 J Q K A` suited → Royal, `9 10 J Q K` suited → Straight Flush |
| `TestClassifyFlushNotStraightFlush` | suited non-consecutive → Flush |
| `TestClassifyAceIsHighInFlush` | `A♥ 5♥ 4♥ 3♥ 2♥` → Straight Flush high 5, never "Flush, 5 high" |
| `TestClassifyReducedWidthsAreLegalClaims` | every hand of 2, 3 and 4 cards classifies into the shared hierarchy, every result is a legal claim, and Flush / Straight / Full House / SF / Royal are provably unreachable below five cards |
| `TestClassifyTable` (reduced rows) | `8 8 K 3` → Pair of 8s; `8 8 8 K` → Three 8s; `8 8 4 4` → Two Pair; `8 8 8 8` → Four 8s; `2 3 4 5` → High Card 5; four suited cards are not a flush; four consecutive are not a straight |

The exhaustive histogram test is the strongest single guarantee available for
the classifier and runs in about a second in Go.

## 3. Claim model and ordering (`claim_test.go`, `claim_order_test.go`)

| Test | Assertion |
|---|---|
| `TestClaimSpaceSizeAndBijection` | exactly 367 claims; ordinals are a bijection onto `0..366`; per-category band sizes |
| `TestOrdinalTotalOrder` | `Compare` is antisymmetric, transitive, total over the full space |
| `TestCategoryDominance` | any claim of a higher category beats any claim of a lower one |
| `TestWithinCategoryOrdering` | `Pair 3 < Pair 4 < … < Pair A`; same for trips, quads, straights |
| `TestTwoPairOrdering` | `(4,2) < (4,3) < (5,2) < …` |
| `TestFullHouseOrdering` | `(8,2) < (8,K) < (9,2)` |
| `TestFlushSuitTiebreak` | `Q♣ < Q♦ < Q♥ < Q♠` flushes; royals likewise |
| `TestMinAndMaxClaim` | min = High Card 3, max = Royal Flush of Spades; nothing above the maximum |
| `TestCanonicalisation` | `Two Pair 2s&4s` == `Two Pair 4s&2s` |
| `TestMalformedClaimsRejected` | `Pair of 1s`, `8s full of 8s`, `Two Pair 8s&8s`, `Straight to 4`, `Flush 6-high`, `High Card 2`, `Straight Flush to A`, a suit on a suitless category, an out-of-range category — all rejected; and the boundary cases `High Card 3`, `Flush 7-high`, `Straight to 5`, `SF to K` accepted |
| `TestClaimJSONRoundTrip` | marshal → unmarshal → identical canonical claim, for all 363 |
| `TestUnknownCategoryRejected` | unknown wire category → `ERR_MALFORMED_CLAIM` |

## 4. Exact claim validation (the critical suite)

Per-category tables covering your §44 list — every rank, every suit,
ace-high, ace-low, wrong suit, wrong rank, missing core card, extra
matching cards, multiple valid selections.

Plus the semantic tests that distinguish this game from ordinary poker:

| Test | Assertion |
|---|---|
| `TestStrongerHandDoesNotValidateWeakerClaim` | pool = `8♠8♥8♦K♣3♣`, claim `Pair of 8s` → FALSE |
| `TestWeakerClaimValidByExclusion` | same three 8s + a 6th card, claim `Pair of 8s` → TRUE |
| `TestOwnershipRequired` | claimed hand exists entirely in others' hands → FALSE |
| `TestOwnershipViaKicker` | claimant owns only the quads kicker → TRUE |
| `TestOwnershipCoreOnlyCategories` | full house / straight flush / royal with no claimant card in the forced core → FALSE |
| `TestFlushHighCardIsSelectionMax` | higher suited card present, 4 lower ones → FALSE; 5 lower ones → TRUE |
| `TestStraightNotSatisfiedBySuitedSelection` | single-suited only → FALSE; mixed available → TRUE |
| `TestFillerDistinctnessPair` | pair claim rejected when the only fillers form a second pair |
| `TestFillerDistinctnessTrips` | trips claim rejected when the only fillers pair up |
| `TestPoolBelowFiveAlwaysFalse` | pool sizes 0–4 × all 363 claims → FALSE, no error |
| `TestWitnessIsValid` | every TRUE result returns 5 distinct pool cards, ≥1 claimant-owned, satisfying the claim |
| `TestWitnessIsDeterministic` | witness is the lexicographically smallest satisfying subset; stable across runs |
| `TestCrossPlayerSelection` | witnesses spanning 3+ players' hands |

### 4.0 Reduced-pool regression tests (the five you required)

| Test | Assertion |
|---|---|
| `TestReducedPoolFourCardPair` | `A:8♠ B:8♥ C:K♦ D:3♣`, `Pair of 8s` → TRUE for A and for B, FALSE for C and D; mode is `REDUCED_POOL` |
| `TestReducedPoolClaimWithoutClaimantCard` | `A:K♠ B:8♥ C:8♦ D:3♣`, A claims `Pair of 8s` → FALSE, while the pool itself does classify as Pair of 8s |
| `TestReducedPoolFourCardHighCard` | `A:K♠ B:8♥ C:5♦ D:3♣`, A claims `King high` → TRUE; B claiming it → FALSE; `8 high` → FALSE |
| `TestTransitionToExactFiveMode` | at five cards the mode flips to `EXACT_FIVE`, and a kicker-only owner is TRUE, exactly as below five cards |
| `TestOwnershipIsAnySelectedCardEverywhere` | the one ownership rule at both widths: reduced pool, claimant owns only the king while the pair belongs to others → TRUE; five cards, claimant owns only the kicker while all four pattern cards belong to others → TRUE; and a six-card pool whose only full house excludes the claimant → FALSE |
| `TestReducedModeOwnershipIsAlwaysSatisfied` | over many random 2-, 3- and 4-card pools, every claim in the space checked against every card owner: the pool's own classification is TRUE for every owner and nothing else is TRUE for anyone |
| `TestSixPlusCardsDoNotAllParticipate` | six cards with three 8s → `Pair of 8s` TRUE by exclusion, witness is exactly five cards; the same three 8s in a five-card pool → FALSE |
| `TestReducedPoolExactClassificationStillApplies` | four cards `8 8 8 K` → `Pair of 8s` FALSE, `Three 8s` TRUE; `8 8 8 8` → `Four 8s` TRUE with no kicker; `8 8 4 4` → `Two Pair` TRUE with no fifth card; five-card-only categories unreachable |
| `TestPoolTooSmallForAnyClaim` | a two-card pool realizes High Card and Pair only |

### 4.1 Differential test against the oracle

| Test | Assertion |
|---|---|
| `TestConstructiveMatchesExhaustive` | all 367 claims × pool sizes 2–10: identical boolean, and both witnesses independently valid |
| `TestConstructiveMatchesExhaustiveLargePools` | sampled claims at pool sizes 12, 16, 20, 25 |
| `FuzzClaimValidation` | Go fuzzing over `(pool, ownership, claim)`; oracle is the differential target; no panics |

This is the primary correctness argument for the evaluator. Witness
*identity* is not asserted between the two paths: the oracle returns the
lexicographically smallest witness, the constructive path returns a
deterministic but not necessarily minimal one (see `show-validation.md` §4).
Both are asserted to be valid selections that satisfy the claim and the
ownership rule.

## 5. Engine, rounds, penalties, rotation

| Test | Assertion |
|---|---|
| `TestStartGameRequiresFourPlayers` | 3 → error, 4 → ok, 9 → ok, 10 → error |
| `TestInitialHandSizesAreOne` | all players `H=1` |
| `TestDealMatchesHandSizes` | each active player receives exactly `H` cards |
| `TestPoolFrozenForRound` | pool and ownership map are immutable between deal and round end |
| `TestCardsReturnedEachRound` | deck back to 52 before each deal; no card carries over |
| `TestPenaltyLadder` | 1→2→3→4→5→eliminated |
| `TestOnlyLoserPenalised` | all other hand sizes unchanged |
| `TestChallengeTrueClaim` | claim TRUE → SHOW caller penalised |
| `TestChallengeFalseClaim` | claim FALSE → claimant penalised |
| `TestOnlyPreviousClaimValidated` | a chain of 5 claims: only the last is evaluated |
| `TestStarterRotationMatchesSpec` | the §47 sequence: starter seats `0,1,2,3,0,1,3` with seat 2 eliminated in round 3 |
| `TestStarterRotationIndependentOfOutcome` | property: rotation output identical across all SHOW outcomes |
| `TestStarterCannotShow` | `ERR_NO_STANDING_CLAIM` |
| `TestOnlyNextPlayerCanShow` | every other seat → `ERR_NOT_SHOW_ELIGIBLE` |
| `TestClaimMustBeStrictlyHigher` | equal and lower → `ERR_CLAIM_NOT_HIGHER` |
| `TestMaxClaimForcesShow` | standing = max → claim rejected, SHOW accepted |
| `TestOneActionPerTurn` | second claim in a turn → `ERR_NOT_YOUR_TURN` |
| `TestEliminatedPlayerCannotAct` | `ERR_PLAYER_ELIMINATED` |
| `TestGameEndsWithOneActive` | winner declared, `GAME_OVER` |
| `TestGameOverRejectsActions` | all gameplay actions rejected after end |
| `TestImpossibleClaimAccepted` | `Four Aces` with no ace dealt is accepted at submission |
| `TestTimeoutAutoShow` | early tick is a no-op; starter timeout submits the minimum claim; a later timeout with a standing claim auto-SHOWs |
| `TestTimeoutForfeitRound` | timed-out player takes the penalty and the round ends |
| `TestRoundEndTickAdvancesRound` | a tick past `RoundEndDeadline` deals the next round |
| `TestDisconnectEliminatesAtRoundEnd` | disconnected player stays in the frozen pool for the round, is eliminated at round end, and is dealt out of the next |
| `TestReconnectRestoresPlay` | status returns to active, own hand unchanged, nothing leaked |
| `TestFourPlayerRoundOneUsesReducedPool` | pool of 4, mode `REDUCED_POOL`, and the pool's classification is a true claim for a pattern owner |
| `TestPoolFrozenAndCardsReturnedEachRound` | the pool is byte-identical across a claim and a disconnect, and conservation holds after the reshuffle |
| `TestApplyDoesNotMutateInput` / `TestApplyIsSafeForConcurrentUse` | purity, including sixteen concurrent `Apply` calls on one state producing identical results under `-race` |
| `TestFourPlayerRoundOneUsesReducedPool` | the pool's classification is a true claim for **every** in-play player, and an unrelated claim is false for every one of them |
| `TestEventSequenceIsMonotonic` | event `Seq` strictly increases and matches the state |
| `TestSimulatedGameIsReproducible` | one seed, identical deal and first starter |

## 6. Views and information leakage

| Test | Assertion |
|---|---|
| `TestViewHidesOthersCards` | for every reachable state and every player, `View(s,p)` contains no card owned by another player before SHOW |
| `TestViewRevealsAllOnShow` | after resolution, all active hands present in every view |
| `TestViewHidesDeckAndSeed` | deck contents, deck order and seed absent from every view |
| `TestViewShowsHandSizes` | all hand sizes present in every view |
| `TestViewEligibilityFlag` | only the eligible player's view has `canShow = true` |
| `TestViewLegalClaimRange` | matches `(standing.Ordinal(), 362]` |
| `TestEventPayloadsUniformExceptDeal` | only `CardsDealt` varies per recipient |
| `TestNoWitnessBeforeShow` | witness absent from all views until resolution |
| `TestReconnectViewMatchesLiveView` | reconnect snapshot byte-identical to the live view at the same `Seq` |

`TestViewHidesOthersCards` is driven by the simulator's reachable-state
corpus rather than hand-written states, so coverage grows with the
simulator.

## 7. Invariants (property tests, run over simulated games)

Each invariant `I1`–`I14` from `rules.md` §13 is asserted after **every**
`Apply` call during simulation, not just at round boundaries:

| Test | Assertion |
|---|---|
| `TestInvariantsHoldOverSimulatedGames` | all of `I1`–`I14` after every action across thousands of seeded games |
| `TestNoDuplicateCards` | `I1` |
| `TestCardConservation` | `I2`: `\|deck\| + sum(\|hands\|) == 52` |
| `TestHandSizeBounds` | `I3`: `H ∈ [1,5]` for all active players |
| `TestExactlyOneActiveTurn` | `I4` |
| `TestClaimsStrictlyIncreasing` | `I7`, per round |
| `TestRoundTerminates` | ≤ 367 claims per round |
| `TestGameTerminates` | every seeded game reaches `GAME_OVER` in bounded rounds |
| `TestReachableStateCorpusHasNoImpossibleStates` | no state with 6-card active player, no two-active-turn state, etc. |

## 8. Simulator (`cmd/sim`, Phase 2)

Bot policies: always-SHOW, minimum-legal-raise, random-legal-raise,
truth-aware (raises only to claims its own cards support), and a bluffer.
Cross-product of policies over 4–9 players, thousands of seeded games,
asserting §7 invariants throughout and reporting round-length and
elimination distributions. Any seed that trips an invariant is written out
as a permanent regression fixture.

## 9. Transport and hostile clients (Phase 3/5)

| Test | Assertion |
|---|---|
| `TestUnauthorizedAction` | claim from a non-member of the room → rejected |
| `TestImpersonation` | action with another player's id but own session → rejected |
| `TestReplayedAction` | duplicate action at a stale `Seq` → rejected idempotently |
| `TestMalformedMessages` | truncated, oversized, wrong-type, unknown-field payloads → typed error, no panic, connection survives |
| `TestConcurrentClaims` | N clients claim simultaneously → exactly one accepted |
| `TestConcurrentShowAndTimeout` | race between `CALL_SHOW` and expiry → exactly one resolution |
| `TestReconnectMidRound` | state resumes correctly, no leak |
| `TestReconnectAfterGameOver` | final view served, no actions accepted |
| `TestRoomPanicContained` | injected panic in the room goroutine does not kill the process or other rooms |
| `TestRaceDetector` | `go test -race ./...` clean |


---

## 10. Measured results (revision 2)

```
go build ./...        OK          gofmt -l .   clean        go vet ./...  clean
go test ./...         PASS        go test -race ./...       PASS
```

| Check | Scale | Result |
|---|---|---|
| `TestClassifyExhaustive` | 2,598,960 five-card hands | category histogram matches the standard poker distribution exactly; every classification is a legal claim |
| `TestClassifyReducedWidthsAreLegalClaims` | 294,151 hands of width 2–4 | all legal claims; five-card-only categories provably unreachable |
| `TestConstructiveMatchesExhaustive` | 39,636 comparisons, 108 scenarios | zero disagreements |
| `TestRealizableAtWidthMatchesEnumeration` | 367 claims × widths 2–5 | matches brute-force enumeration exactly |
| `TestSimulatedGamesHoldInvariants` | 72 games, 2,141 rounds, 9,197 actions | all invariants held after every action; zero information leaks |
| `TestApplyIsSafeForConcurrentUse` | 16 goroutines on one state | identical results, race detector clean |

The exhaustive validator is retained permanently as the differential oracle
rather than deleted after bring-up, because it is the only artefact that can
falsify the production path.

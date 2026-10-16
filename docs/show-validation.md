# SHOW Validation — Exact Semantics and Algorithm

**Revision 3** — the reduced-pool rule is incorporated, and ownership is a
single rule applied identically at every pool size.

This is the most rule-sensitive part of the project.

---

## 1. The question being answered

Not "how strong is the best hand in the pool?" but:

> Does there exist a subset `S` of the revealed round pool, of size
> `HandSize(pool) = min(5, |pool|)`, such that the claimant contributes to
> `S`, and `S` **is** the exact hand that was claimed?

`HandSize` is the entire reduced-pool rule. At five or more cards it is 5 and
extra cards may be excluded; below five it is `|pool|`, and there is exactly
one subset of that size — the whole pool. One quantifier covers both modes,
so there is no second evaluator and no separate short-hand hierarchy.

Signature:

```go
func CanSatisfy(
    pool  []Card,                  // all revealed cards, round-frozen
    owns  func(Card) bool,         // ownership as a predicate; the poker
                                   // package knows nothing about players
    claim Claim,
) Result                           // {Satisfied, Witness, Mode, PoolSize, ...}
```

## 2. Normative definition of "S is the exact hand claimed"

```go
func Satisfies(hand []Card, c Claim) bool {
    v, err := Classify(hand)
    return err == nil && v == c
}
```

That is the whole definition. `Classify` returns the single claim the hand
exactly realizes, so satisfaction is one struct equality. Parameters the
claim does not declare — a quads kicker, a straight's suits, a flush's four
low cards, a pair's three kickers — are simply absent from the value
`Classify` returns, so they are unconstrained without any special case.

This single definition is what makes your §9 / §27 rule fall out
automatically rather than needing special cases:

- A stronger hand existing in the pool does **not** validate a weaker claim,
  because `Classify(S)` returns exactly one category. If the only available
  `S` classifies as Three of a Kind, it does not satisfy a Pair claim.
- A weaker selection is still available if the extra cards can be excluded,
  because we quantify over all 5-subsets. Dropping the third 8 yields an `S`
  that classifies as Pair.
- Your §16 flush point is enforced without extra logic: `Classify` derives
  the flush's high card from `S` itself, so including a king in a
  "queen-high" selection produces a king-high flush and fails the match.

### 2.1 Consequences worth stating explicitly

| Situation | Result | Why |
|---|---|---|
| Pool has three 8s and ≥6 cards total; claim `Pair of 8s` | may be TRUE | exclude the third 8, `S` classifies as Pair |
| Pool is exactly `8♠ 8♥ 8♦ K♣ 3♣`; claim `Pair of 8s` | **FALSE** | only one 5-subset exists and it classifies as Trips |
| Pool is exactly `8♠ 8♥ 8♦ K♣` (4 cards); claim `Pair of 8s` | **FALSE** | the whole pool classifies as Trips |
| Pool is `8♠ 8♥ K♦ 3♣` (4 cards); claim `Pair of 8s` | TRUE if the claimant owns an 8 | the pool classifies as Pair of 8s |
| Pool is `K♠ 8♥ 8♦ 3♣` (4 cards); claim `Pair of 8s` by the king's owner | **TRUE** | the king is in the evaluated hand; owning a pattern card is not required |
| Pool is `K♠ 8♥ 8♦ 8♠ 8♣` (5 cards); claim `Four 8s` by the king's owner | **TRUE** | same rule: the kicker is enough |
| Pool of 6 with one full house; claim `7s full of 8s` by a player owning neither rank | **FALSE** | every realization excludes the claimant's cards |
| Pool is 4 cards `8♠ 8♥ 8♦ 8♣`; claim `Four 8s` | TRUE | quads needs no kicker, and the kicker is not part of the claim |
| Pool is 4 cards `8♠ 8♥ 4♦ 4♣`; claim `Two Pair 8s and 4s` | TRUE | two pair needs no fifth card |
| `S` would be `5♠6♠7♠8♠9♠`; claim `Straight to 9` | FALSE for that `S` | it classifies as Straight Flush; a mixed-suit selection is required |
| `S` would be `10♠J♠Q♠K♠A♠`; claim `Straight to A` | FALSE for that `S` | classifies as Royal Flush |
| Claim `Pair of 8s`, selection `8♠8♥A♦K♣3♣` | TRUE | kickers may outrank the pair; only the pair rank is declared |
| Claim `Four 8s`, any kicker | kicker irrelevant | §18 |
| Claimed hand exists but uses no claimant card | FALSE | §10 ownership rule |

The second and third rows are the exact-classification rule, and you
confirmed the four-card version of it directly. They also mean the
illustration in the original §12 (pool shown as five cards `8 8 8 K 3` with
the text "may use two of the three 8s and exclude the remaining 8") needs a
pool of at least six cards to work: excluding a card from a five-card pool
leaves four, which is not a five-card hand. §8, §26 and §43 — all of which
state *exactly five selected cards* when five or more are available — are
treated as authoritative. Both the 5-card FALSE case and the 6-card TRUE case
are pinned as regression tests.

### 2.2 Ownership

One rule, at every pool size:

> The claimant must own at least one card of the evaluated hand.

Not a card of the claimed pattern — any card of the evaluated hand. So the
complete validation predicate, in both modes, is:

```go
OwnershipSatisfied(hand, owns) && Satisfies(hand, claim)
```

`OwnershipSatisfied` is a membership scan and nothing more:

```go
func OwnershipSatisfied(hand []Card, owns Owns) bool {
    for _, c := range hand {
        if owns(c) {
            return true
        }
    }
    return false
}
```

There is deliberately no `OwnershipScope` type and no configuration knob. A
knob here would invite the two modes to drift apart again, which is the
failure this revision removes.

`PatternCards(hand, claim)` still exists — it returns the cards forming the
claimed pattern — but it is a **presentation helper** for highlighting the
pattern inside a revealed witness. It has no role in validation, and in
particular no role in the ownership check.

#### Consequence below five cards

Because the evaluated hand is the whole pool and every in-play player holds
at least one card in it, ownership can never fail in reduced mode. Claim
truth is therefore claimant-independent there: the pool's own classification
is true for every player, and nothing else is true for anyone. This is
asserted exhaustively by `TestReducedModeOwnershipIsAlwaysSatisfied`, which
checks every claim in the space against every owner of every card of many
random 2-, 3- and 4-card pools.

Ownership still does real work at five or more cards, where the evaluated
hand is a proper subset that can exclude the claimant. See the Full House
row in the consequences table above.

## 3. Two implementations, differentially tested

### 3.1 Reference implementation — exhaustive (the oracle)

Exactly your §43 procedure:

```
for each 5-subset S of pool:
    if no card in S is owned by claimant: continue
    if Satisfies(S, claim): return true, S
return false
```

Worst case pool size is 45 (9 players × 5 cards), so
`C(45,5) = 1,221,759` subsets, each classified in constant time. It is kept
**permanently** as the test oracle, and because it walks subsets in canonical
order over a canonically sorted pool, the witness it returns is the
lexicographically smallest satisfying selection.

### 3.2 Production implementation — claim-directed construction

Brute force is fine at 45 cards but it is not self-documenting, and it hides
bugs behind a correct-looking loop. The production path constructs the
required core per category, then fills free slots under the constraints that
prevent category inflation. `O(pool)` or small-constant work per claim.

Notation: `count(r)` = number of pool cards of rank `r`; `K` = claimant;
`ownsRank(r)` = K owns at least one pool card of rank `r`.

| Claim | Core requirement | Free-slot constraints (prevent inflation) | Ownership condition |
|---|---|---|---|
| **Royal Flush** s | all of `10,J,Q,K,A` in suit s | none (exact 5) | K owns one of those 5 |
| **Straight Flush** s,h | the 5 ranks of the h-straight, all suit s | none (exact 5) | K owns one of those 5 |
| **Four of a Kind** r | `count(r) ≥ 4` | one kicker, any rank ≠ r | K owns an r-card **or** any card of rank ≠ r |
| **Full House** t,p | `count(t) ≥ 3` and `count(p) ≥ 2` | none (exact 5) | `ownsRank(t)` or `ownsRank(p)` |
| **Flush** s,h | 5 suit-s cards with max rank exactly h | chosen 5 must not be 5 consecutive ranks | some valid selection includes a K-owned suit-s card of rank ≤ h |
| **Straight** h | one card of each of the 5 h-straight ranks | not all 5 of the same suit | some valid selection includes a K-owned card at one of the 5 ranks |
| **Three of a Kind** r | `count(r) ≥ 3` | 2 fillers, distinct ranks, both ≠ r, ≠ each other | K owns an r-card **or** a usable filler with a second filler available |
| **Two Pair** hi,lo | `count(hi) ≥ 2`, `count(lo) ≥ 2` | 1 filler, rank ∉ {hi,lo} | `ownsRank(hi)`, `ownsRank(lo)`, or K owns a usable filler |
| **Pair** r | `count(r) ≥ 2` | 3 fillers, all distinct ranks ≠ r and ≠ each other | K owns an r-card **or** a usable filler with two others available |
| **High Card** h | 5 distinct ranks, max exactly h | not 5 consecutive, not all one suit | some valid selection includes a K-owned card of rank ≤ h |

Notes on the non-obvious rows:

- **Flush**: candidates are the suit-s cards with rank ≤ h, and `(h,s)` must
  be present. Choose 4 from the rest; at most `C(12,4) = 495` combinations,
  so this row is enumerated directly rather than reasoned about. The only
  exclusion is the exact consecutive run `{h-4..h}`.
- **Straight / High Card**: suits and rank choices interact with the
  "not all one suit" exclusion, so these two rows enumerate over the small
  candidate set as well rather than using a closed-form ownership test.
- **Pair / Trips**: the filler distinctness rule is what prevents a Pair
  claim being satisfied by a two-pair or full-house selection. Getting this
  wrong is the single most likely evaluator bug, so it is fuzzed against the
  oracle.
- Flushes, straights and their combinations are impossible when the core
  already contains duplicate ranks, so no flush/straight exclusion is needed
  on the Pair, Two Pair, Trips, Full House or Quads rows.

### 3.3 Differential test

```
for seed in 0..N:
    pool, ownership, claim := randomScenario(seed)
    a, _ := Exhaustive(pool, ownership, claimant, claim)
    b, w := Constructive(pool, ownership, claimant, claim)
    require a == b
    if b: require Satisfies(w, claim) && w contains a claimant card
```

Run across the whole 367-claim space and pool sizes 2 to 25. Any disagreement is a bug in the constructive path, never in the
oracle. This is the primary correctness argument for the evaluator.

Measured: **39,636 comparisons across 108 scenarios, zero disagreements**,
plus a `FuzzClaimValidation` target using the oracle as the differential
reference.

There is also a structural safety property worth stating. Every candidate the
constructive search builds is verified with `Satisfies` before being
accepted, so the construction can only ever produce a **false negative**,
never a false positive. Soundness holds by construction; completeness is what
the differential test establishes.

## 4. Deterministic witness

§28 allows any valid selection but asks for determinism in tests. Two
guarantees, and they differ — this is a change from the earlier revision:

- `CanSatisfyExhaustive` returns the **lexicographically smallest**
  satisfying selection under the canonical card order (rank ascending, then
  `♣ < ♦ < ♥ < ♠`), comparing selections as sorted tuples. This is the
  definition of the canonical witness.
- `CanSatisfy`, the production path, returns a **deterministic but not
  necessarily lexicographically smallest** witness: it draws pattern cards
  and fillers from canonically ordered candidate groups and returns the first
  success, so the same input always yields the same witness.

Making the constructive path match the oracle's witness exactly would require
a lexicographic-minimal selection search, which buys nothing the game needs.
The differential test therefore asserts boolean equality plus independent
witness *validity*, rather than witness identity. The witness is included in the `ClaimValidated` event so the UI can
highlight exactly which five cards were used, and so replays are auditable.

## 5. Pools smaller than five cards

Superseded. The earlier revision of this document concluded that every claim
is FALSE when the pool holds fewer than five cards. That is **no longer
correct**. The rule is now:

> When fewer than five cards are available, the complete available card pool
> is used for claim evaluation. When five or more cards are available, claims
> are evaluated over exact five-card subsets. In both cases the claimant must
> contribute to the hand and the exact claimed poker pattern must be
> satisfied.

Implementation-wise nothing special happens: `HandSize` returns `|pool|`,
there is exactly one subset of that width, and `Classify` handles hands of
two to four cards with the same category definitions. The only mode-dependent
value is the ownership scope (§2.2).

Validation remains total at every width: no panic, no error path, and a
pool of fewer than two cards cannot arise while two or more players are in
play.

## 6. What the validator must never do

- Never call `BestFive(pool)` and compare its category to the claim.
- Never build a separate hierarchy, evaluator or claim ordering for hands
  narrower than five cards.
- Never treat a stronger available hand as validating a weaker claim.
- Never require that all revealed cards participate.
- Never require the claimed cards to belong to the claimant beyond the
  one-card minimum, and never require that one card to be part of the
  claimed pattern.
- Never make the ownership rule depend on the pool size or the evaluation
  mode.
- Never permit a witness with zero claimant-owned cards.
- Never consult the claim's truth at claim-submission time.
- Never emit the witness, or any card values, before SHOW resolution.

# Claim Data Model and Ordering

> **Implementation note (revision 2).** The shipped Go type is a single
> validated struct, `poker.Claim{Category, Primary, Secondary, Suit,
> HasSuit}`, with constructors that canonicalise and zero every unused field,
> rather than the interface sketched below. The reason is decisive:
> `Classify` returns a `Claim`, so "does this hand satisfy this claim?"
> becomes one struct equality, `Classify(hand) == claim`. That removes all
> per-category matching logic from the validator, which is where the
> exact-claim rule would otherwise be easy to get wrong. The field-presence
> safety the interface was for is provided by claim-space membership instead:
> a claim is well-formed exactly when it appears in the enumerated space.
> The rest of this section describes the interface design and is kept for the
> reasoning about why nullable pointers were rejected.

## 1. Design decision: sum type, not a bag of nullable fields

Your §41 sketch uses `Rank *Rank; Rank2 *Rank; Suit *Suit`. That
representation permits 2^3 field combinations per category, most of them
invalid, so every consumer has to nil-check and the compiler helps with
nothing. I propose a discriminated union instead — Go's closest idiom is an
interface with a closed set of implementations plus a category tag for
serialisation:

```
Claim (interface, closed set)
├── HighCardClaim     { High Rank }
├── PairClaim         { Rank Rank }
├── TwoPairClaim      { High Rank; Low Rank }        // High > Low, canonical
├── TripsClaim        { Rank Rank }
├── StraightClaim     { High Rank }                  // High in 5..A
├── FlushClaim        { Suit Suit; High Rank }
├── FullHouseClaim    { Trips Rank; Pair Rank }      // Trips != Pair
├── QuadsClaim        { Rank Rank }                  // kicker not modelled
├── StraightFlushClaim{ Suit Suit; High Rank }       // High in 5..K
└── RoyalFlushClaim   { Suit Suit }
```

Each implementation provides:

| Method | Purpose |
|---|---|
| `Category() Category` | discriminant |
| `Ordinal() int` | position in the total order, `0..362` |
| `Validate() error` | structural well-formedness |
| `Canonical() Claim` | normalised form (e.g. Two Pair reordered) |
| `String() string` | human text, e.g. `Queen-high Hearts Flush` |
| `MarshalJSON` / `UnmarshalJSON` | stable wire form |

Wire form is a tagged object, not a positional tuple:

```json
{ "category": "FULL_HOUSE", "trips": "8", "pair": "4" }
{ "category": "FLUSH", "suit": "HEARTS", "high": "Q" }
{ "category": "ROYAL_FLUSH", "suit": "SPADES" }
```

Unknown categories and out-of-range fields are rejected at the protocol
boundary before the engine ever sees them.

## 2. Canonicalisation

- `TwoPairClaim` is stored with `High > Low`. `Two Pair — 2s and 4s`
  (your §26 wording) canonicalises to `Two Pair — 4s and 2s`. Two claims
  differing only in field order are the **same** claim, therefore not a
  legal raise over each other.
- `FullHouseClaim` requires `Trips != Pair`.
- Every claim is canonicalised before comparison, storage, or logging.

## 3. Ordering

`Compare(a, b) -> -1 | 0 | +1`, implemented as `sign(a.Ordinal() - b.Ordinal())`.
No string comparison anywhere.

Within-category ordering:

| Category | Ordering key | Source |
|---|---|---|
| High Card | `high` | §11 |
| Pair | `rank` | §12 |
| Two Pair | `(high, low)` lexicographic | resolved: standard |
| Three of a Kind | `rank` | §14 |
| Straight | `high` (ace-low = 5) | §15 |
| Flush | `(high, suit)` | high from §16, suit tiebreak resolved |
| Full House | `(trips, pair)` | trips from §17, pair tiebreak resolved |
| Four of a Kind | `rank`, kicker ignored | §18 |
| Straight Flush | `(high, suit)` | high from §19, suit tiebreak resolved |
| Royal Flush | `suit` | suit tiebreak resolved |

Suit ordering, where used as a tiebreak: `Clubs < Diamonds < Hearts < Spades`.

**Why the suit tiebreaks matter.** §21 forbids equal claims. If flushes are
ordered by high card alone, `Queen-high Hearts Flush` and
`Queen-high Spades Flush` are *equal*, so a player facing the first cannot
raise to the second and must jump to a king-high flush or above. Likewise
all four royal flushes would be mutually unraisable, and the maximum claim
would be a 4-way tie. Adding suit as the least significant key makes the
order total and the "strictly higher" rule well defined everywhere. This is
a genuine decision, not an assumption — see `OPEN_QUESTIONS.md` Q3.

## 4. The claim space

With unrealizable claims excluded, the space is finite and totally ordered.
Realizability is judged across **every** hand width the game can evaluate,
which is 2 to 5 cards, because the reduced-pool rule makes narrower hands
real evaluation targets:

| Category | Parameters | Count | Ordinals |
|---|---|---|---|
| High Card | high ∈ 3..A | 12 | 0–11 |
| Pair | rank ∈ 2..A | 13 | 12–24 |
| Two Pair | high > low, both 2..A | 78 | 25–102 |
| Three of a Kind | rank ∈ 2..A | 13 | 103–115 |
| Straight | high ∈ 5..A | 10 | 116–125 |
| Flush | 4 suits × high ∈ 7..A | 32 | 126–157 |
| Full House | trips × pair, trips ≠ pair | 156 | 158–313 |
| Four of a Kind | rank ∈ 2..A | 13 | 314–326 |
| Straight Flush | 4 suits × high ∈ 5..K | 36 | 327–362 |
| Royal Flush | 4 suits | 4 | 363–366 |
| **Total** | | **367** | |

Minimum claim: `High Card 3` (ordinal 0).
Maximum claim: `Royal Flush of Spades` (ordinal 366).

**Verified exhaustively.** `tools/verify_claim_space.py` enumerates every
hand of 2, 3, 4 and 5 cards — 2,893,111 hands in total — classifies each with
the §7.2 classifier, and derives the realizable parameter set per category.
Output:

```
HIGH 1302540   PAIR 1098240   TWOPAIR 123552   TRIPS 54912   STRAIGHT 10200
FLUSH   5108   FULL    3744   QUADS      624   SFLUSH   36   ROYAL      4
total 2598960                          (matches the standard poker distribution)

realizable High Card highs    : 7 8 9 10 J Q K A
realizable Flush highs        : 7 8 9 10 J Q K A
realizable Straight highs     : 5 6 7 8 9 10 J Q K A
realizable Straight Flush highs: 5 6 7 8 9 10 J Q K      (no Ace — that is Royal)
realizable High Card highs (widths 2-5) : 3 4 5 6 7 8 9 10 J Q K A
claim space total             : 367
```

The category histogram matching the known poker distribution is independent
confirmation that the classifier's ten cases are exhaustive and mutually
exclusive. This script is ported to Go as `TestClassifyExhaustive` in
Phase 1.

### 4.1 Why some parameter values are excluded

These exclusions come from poker combinatorics, not from the dealt cards, so
they are a matter of claim *well-formedness*, not claim truth:

- **High Card 3 to 6 is reachable only in reduced-pool mode.** At five cards
  a high-card hand needs 5 distinct ranks with no straight, and max rank 6
  forces `{2,3,4,5,6}`, which is a straight — so the five-card floor is
  `{2,3,4,5,7}` → High Card 7. But a 4-card pool `{2,3,4,5}` is High Card 5,
  a 3-card pool `{2,3,4}` is High Card 4, and a 2-card pool `{2,3}` is High
  Card 3. All four are legal claims. **High Card 2 is excluded**: it needs a
  one-card hand, which cannot occur while two or more players are in play.
- **This is why the space is 367 and not 363.** The four extra High Card
  claims are the whole difference. Excluding them would leave reduced-pool
  deals in which no legal claim is true — a 4-card pool of `{2,3,4,5}`, for
  instance, has exactly one true claim and it would not be in the space —
  which recreates the forced-loss problem the reduced-pool rule was
  introduced to remove.
- **367 is locked.** The claim space is a fixed public list with no
  configuration switch. Hands of width 2 to 4 are legitimate evaluated hands
  under the reduced-pool rule, so the space must contain the claims those
  hands can classify as; otherwise a reduced pool could have a valid
  classification that no player could truthfully claim, which is a
  forced-loss state. `ClaimSpaceSize` is a constant, `init()` panics if the
  enumeration does not produce exactly 367, and a test asserts the four
  reduced-pool High Card claims are present and High Card 2 is absent.
- **Flush below 7-high is impossible.** Same argument: 5 distinct ranks of
  one suit with max 6 forces `{2,3,4,5,6}` of that suit, a straight flush.
- **Straight below 5-high is impossible.** `5` is the ace-low wheel; nothing
  lower exists, and ranks do not wrap.
- **Straight Flush to Ace does not exist** as a straight flush — it is by
  definition the Royal Flush of that suit, which §20 makes a separate
  category. The UI must not offer it.

### 4.2 Consequence: legal-claim enumeration for the UI

Because the space is a 367-element ordered list, the server can hand the
client the exact set of legal next claims as
`ordinals (standing.Ordinal(), 362]`. The client renders selectors over that
set and has zero rule knowledge. The server re-validates on receipt anyway.
This satisfies §31: the UI constrains *ordering*, never *truth*.

## 5. What the claim does not encode

- **Four of a Kind kicker.** §18: the kicker is irrelevant to both the claim
  and the ordering. It is not a field.
- **Straight suits.** §15: a straight is specified by high card only.
- **Flush's four low cards.** §16: only suit and high card are declared.
- **Full House kicker.** There is none; a full house uses all 5 cards.

Anything not encoded in the claim is free during validation, subject only to
the exact-category constraint in `show-validation.md` §2.

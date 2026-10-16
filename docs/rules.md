# Liar's Poker — Formal Rules Specification

**Status:** Phase 0, revision 2 — the reduced-pool rule is incorporated.
Derived strictly from the authoritative rules document.
Every clause below is either (a) directly specified by you, (b) marked
`[INFERRED]` with the reasoning given, or (c) marked `[UNRESOLVED]` and
deferred to `OPEN_QUESTIONS.md`. No rule has been invented silently.

---

## 1. Components

- One standard 52-card deck. Cards are unique; identity is `(Rank, Suit)`.
- Ranks: `2 3 4 5 6 7 8 9 10 J Q K A`. Numeric values `2..14`.
  Ace additionally takes value `1` **only** for straight/straight-flush
  detection (see §7.3).
- Suits: `Clubs, Diamonds, Hearts, Spades`. Suit is exact identity, never
  colour.
- No chips, no betting, no monetary stakes. The only resource is a player's
  **hand size**.

## 2. Players and seating

- Minimum 4, maximum 9 players to **start** a game.
- Seats are fixed at game start, indexed `0..n-1`, ordered clockwise. A seat
  index never changes for the lifetime of the game.
- A player is in exactly one of: `Active`, `Disconnected`, `Eliminated`.
- Elimination is permanent. Eliminated seats are retained (for rotation
  arithmetic) but skipped.
- Active-player count may fall below 4 through elimination. The 4-player
  minimum is a **start** constraint only. `[INFERRED]` — §2 states play
  continues until two players remain, which is unreachable if 4 were a
  continuation minimum.

## 3. Hand size and penalty level

- A player's **hand size** `H ∈ {1,2,3,4,5}` is the number of cards they will
  be dealt in the next round.
- Every player starts at `H = 1`.
- The loser of a SHOW resolution has `H ← H + 1`.
- If that increment would produce `H = 6`, the player is **eliminated**
  instead. They are never dealt 6 cards.
- No other player's `H` changes as a result of a SHOW.
- `H` is public information. Card values are not.

## 4. Game outcome

- Play continues while three or more players are active.
- Once exactly two players are active, play continues normally. When one of
  those two is eliminated, the other **wins** and the game ends.
- `[IMPLEMENTATION]` A table can also empty out entirely: round-end pruning
  eliminates every still-disconnected player at once, so a game in which
  everyone has left ends with **no winner**. The rules do not name a winner
  for that case and the engine does not invent one; it records the game as
  abandoned and leaves the winner empty. What a room does with an abandoned
  game is a room-lifecycle question for the transport layer.

## 5. Round lifecycle

A round is the unit of play. Cards never persist across rounds.

```
ROUND_SETUP
  1. Collect every card from every hand back into the deck.
     Invariant: |deck| == 52 before dealing.
  2. Shuffle the full 52-card deck using the injected RNG.
  3. For each active player in seat order, deal exactly H(player) cards.
  4. Freeze the round pool: POOL = multiset of all dealt cards, with an
     immutable card -> owner map. POOL does not change for the rest of the
     round, regardless of disconnects.
  5. Determine the round starter (§6).
CLAIMING
  6. The starter must make a claim. The starter may NOT call SHOW (there is
     no standing claim to challenge).
  7. Turn passes clockwise to the next active player, who must either:
       a. make a strictly higher claim (see claim-model.md), or
       b. call SHOW.
     There is no pass, no equal claim, no lower claim, and no second claim
     within a turn. One turn = exactly one action.
  8. Repeat step 7 until SHOW is called.
SHOW_RESOLUTION
  9. All active players' cards are revealed to all players.
 10. The engine validates the *immediately preceding* claim only
     (see show-validation.md). Earlier claims in the chain are never
     validated.
 11. If the claim is TRUE  -> the SHOW caller loses the round.
     If the claim is FALSE -> the claimant loses the round.
ROUND_END
 12. Apply +1 hand size to the loser, or eliminate them if it would reach 6.
 13. If a single active player remains, the game ends and they win.
 14. Otherwise advance the starter pointer (§6) and return to ROUND_SETUP.
```

Cards are always returned before the next deal. A player who lost last
round receives entirely **new** cards, one more than before.

### 5.1 Round termination is guaranteed

Claims within a round are strictly increasing over a finite, totally ordered
claim space of **367 elements** (see `claim-model.md` §4). A round therefore
ends after at most 367 claims. When the standing claim is the maximum element
(`Royal Flush of Spades`), the next player has no legal claim and SHOW is
their only legal action; the engine must enforce this rather than deadlock.

## 6. Round starter rotation

- Rotation is **completely independent** of the SHOW caller, the claimant,
  the penalty recipient, and the previous round's outcome.
- `starter(1)` is set by the configured first-round rule `[UNRESOLVED Q7]`.
- `starter(n+1)` = the next **active** seat clockwise from `starter(n)`'s
  seat, skipping eliminated seats. The pointer advances from the previous
  starter's seat index even if that player has since been eliminated.

Verified against your §47 example (A B C D, C eliminated during round 3):

| Round | Pointer advances from | Next active seat | Starter |
|---|---|---|---|
| 1 | — | — | A |
| 2 | A | B | B |
| 3 | B | C | C |
| 4 | C (now eliminated) | D | D |
| 5 | D | A | A |
| 6 | A | B | B |
| 7 | B | skip C -> D | D |

This matches your expected output exactly.

## 7. Poker semantics

### 7.1 Category order (ascending strength)

```
0 High Card
1 Pair
2 Two Pair
3 Three of a Kind
4 Straight
5 Flush
6 Full House
7 Four of a Kind
8 Straight Flush
9 Royal Flush
```

Royal Flush is a separate category strictly above Straight Flush, per §20.

### 7.2 Classification of a hand

`Classify(S)` returns the single claim that hand `S` exactly realizes, for
`1 <= |S| <= 5`. For `|S| = 5`:

- Royal Flush: all same suit, ranks exactly `{10,J,Q,K,A}`.
- Straight Flush: all same suit, 5 consecutive ranks, not royal.
- Four of a Kind: rank multiset `4+1`.
- Full House: rank multiset `3+2`.
- Flush: all same suit, not consecutive.
- Straight: 5 consecutive ranks, mixed suits.
- Three of a Kind: rank multiset `3+1+1`.
- Two Pair: rank multiset `2+2+1`.
- Pair: rank multiset `2+1+1+1`.
- High Card: 5 distinct ranks, not consecutive, not all one suit.

These ten cases are exhaustive and mutually exclusive for any 5-card set,
verified against all 2,598,960 hands.

For `|S| < 5` the **same hierarchy and the same definitions** apply. There is
no separate short-hand hierarchy. Flush, Straight, Full House, Straight Flush
and Royal Flush each require five cards by definition, so they are simply
unreachable below that width:

| Hand width | Reachable categories |
|---|---|
| 4 | High Card, Pair, Two Pair, Three of a Kind, Four of a Kind |
| 3 | High Card, Pair, Three of a Kind |
| 2 | High Card, Pair |

Two consequences worth stating: Four of a Kind at four cards has no kicker,
which is fine because the kicker is not part of the claim; and Two Pair at
four cards has no fifth card, which is likewise fine. Four suited cards are
not a flush and four consecutive cards are not a straight.

### 7.3 Ace in straights

- Ace-high: `10 J Q K A`, straight high card = `A`.
- Ace-low ("wheel"): `A 2 3 4 5`, straight high card = `5`.
- No other ace-low behaviour exists. In particular Ace is **always high**
  when determining a Flush's or High Card's high card. `[INFERRED]` — a
  "5-high flush" would necessarily be `A 2 3 4 5` of one suit, which
  classifies as a Straight Flush, so no ace-low flush exists. See
  `OPEN_QUESTIONS.md` Q5 to confirm.
- Valid straight high cards: `5,6,7,8,9,10,J,Q,K,A` (10 values). There is no
  straight to 4 or lower. `A 2 3 4` wrapping to `K A 2 3 4` is **not** a
  straight.

## 8. Claims

A claim is a structured, typed value — never free text. See
`claim-model.md` for the full model, canonical form, ordering, and the
enumerated claim space.

### 8.1 Evaluation width: the reduced-pool rule

Let `P` be the round pool. Claims are evaluated over subsets of `P` of size

```
HandSize(P) = min(5, |P|)
```

- `|P| >= 5` -> every 5-card subset is a candidate; extra cards may be
  excluded. This is the normal and primary mechanism.
- `|P| < 5`  -> subsets of size `|P|`, of which there is exactly one: the
  complete available pool. The whole pool is the hand.

This is one expression, not two code paths. The reduced case falls out of the
same quantifier, which is why no separate four-card evaluator exists.

### 8.2 Legality vs truth

These are two entirely separate operations and live in separate code paths:

- **Legality** (`rules.go`): is this claim well-formed, from the correct
  player, in the correct phase, and strictly higher than the standing claim?
  Checked at submission time. Never consults any card.
- **Truth** (`evaluator.go`): can the exact claimed 5-card hand be
  constructed from the revealed pool under the ownership rule? Checked
  **only** at SHOW resolution.

Impossible claims are legal. The engine must accept `Four Aces` when no ace
has been dealt. Bluffing is the game.

### 8.3 The ownership rule

There is exactly **one** ownership rule and it is identical at every pool
size:

> The claimant must own at least one card of the evaluated hand.

The claimant does **not** need to own any card of the claimed poker pattern.
A kicker, a filler, or any other card of the evaluated hand is enough.
Validation is therefore, in both modes:

```
claimantOwnsSomeCardOfEvaluatedHand  AND  Classify(evaluatedHand) == claim
```

where the evaluated hand is the complete pool when `|P| < 5` and one selected
5-card subset when `|P| >= 5`. There is no per-mode ownership scope and no
configuration knob for it.

Worked examples, both TRUE:

```
|P| < 5      A: K-spade   B: 8-heart   C: 8-diamond   D: 3-club
             A claims Pair of 8s.
             Evaluated hand = the whole pool, which classifies as Pair of 8s.
             A owns K-spade, which is in the evaluated hand.  -> TRUE
             A does not need to own either 8.

|P| >= 5     A: K-spade   B: 8-heart   C: 8-diamond   D: 8-spade   E: 8-club
             A claims Four 8s.
             The selected hand classifies as Four 8s and contains A's
             K-spade.  -> TRUE
             The four pattern cards belong entirely to other players.
```

#### Where the rule still bites

The rule's practical force differs between the modes, but that is geometry,
not a second rule:

- **`|P| < 5`**: the evaluated hand *is* the whole pool, and every in-play
  player is dealt at least one card into it, so ownership is always
  satisfied. Claim truth is therefore claimant-independent below five cards:
  the pool's own classification is true for every player, and nothing else is
  true for anyone.
- **`|P| >= 5`**: the evaluated hand is a proper subset that can exclude the
  claimant's cards entirely. A claim whose only realization leaves the
  claimant out is FALSE. The clearest case is a Full House, Straight, Flush,
  Straight Flush or Royal Flush, since each consumes all five cards and
  offers no free slot: if the pattern belongs wholly to other players, the
  claim fails.

## 9. SHOW eligibility

- Only the **immediate next active player** clockwise from the claimant may
  call SHOW.
- SHOW is available only when a standing claim exists (i.e. never as the
  first action of a round).
- Any SHOW from any other player, in any other phase, is rejected
  server-side with an explicit error, regardless of what the client sends.

## 10. Timer

- Each turn has a **60-second** budget, enforced authoritatively on the
  server. Client timers are display-only.
- The consequence of expiry is `[UNRESOLVED Q8]` and is modelled behind a
  `TimeoutPolicy` enum rather than hard-coded.

## 11. Disconnection

- Disconnecting does not immediately mutate the round pool. The pool is
  frozen at deal time (§5 step 4), so a disconnect never retroactively
  changes whether a claim is true.
- Exact elimination timing is `[UNRESOLVED Q9]` and is modelled behind a
  `DisconnectPolicy` enum.
- A disconnected player must never be able to stall a turn beyond the
  60-second budget.

## 12. Information visibility

| Item | Visible to owner | Visible to others |
|---|---|---|
| Own card values | yes | no (until SHOW) |
| Other players' card values | — | no (until SHOW) |
| Every player's hand size `H` | yes | yes |
| Claim history of the round | yes | yes |
| Current turn, SHOW eligibility, timer | yes | yes |
| Active/eliminated status | yes | yes |
| Deck order, remaining deck contents | no | no |
| RNG seed | no | no |
| Evaluator internals, witness before SHOW | no | no |

At SHOW resolution all active players' cards become visible to everyone. At
the next `ROUND_SETUP` all cards are hidden again.

## 13. Invariants (enforced, not assumed)

```
I1  No duplicate physical card exists anywhere in the state.
I2  |deck| + sum(|hand|) == 52 at every observable point.
I3  Every active player has H in [1,5]; no active player ever holds 6 cards.
I4  Exactly one player has the active turn during CLAIMING.
I5  Only the current-turn player can submit a claim.
I6  Only the immediate next active player can call SHOW.
I7  Every claim is strictly greater than its predecessor in the round.
I8  Claim validation uses exactly HandSize(pool) = min(5, |pool|) cards.
I9  Every valid witness contains at least one claimant-owned card.
I10 The round pool is immutable between deal and round end.
I11 Exactly one player receives a penalty per SHOW resolution.
I12 Starter rotation output is a pure function of (seats, eliminations,
    round number) and never of any SHOW outcome.
I13 A finished game rejects all gameplay actions, has at most one player in
    play, and names a winner exactly when one player remains.
I14 No player view ever contains another player's card values before SHOW.
```

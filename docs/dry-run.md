# Dry Run — Revision 3

Reduced-pool rule plus the single uniform ownership rule.

Every line below is produced by the real engine: `go run ./cmd/dryrun`.
Claims go through `Apply`, verdicts come from the validator, penalties and
rotation come from the reducer, and `CheckInvariants` runs after every single
action. Nothing here is hand-written commentary about what the engine would
do.

To regenerate:

```
cd backend && go run ./cmd/dryrun
```

Trace fields, in the order you asked for them: player card counts, actual
cards, current claimant, claim, structural legality, SHOW or raise, exact
validation, SHOW winner and loser, penalty, next-round card counts, next
starter.

```text
Liar's Poker — machine-executed dry run
Claim space: 367 claims, min Three high, max Royal Flush of Spades
Reduced-pool rule: pool < 5 → evaluate the whole pool; pool >= 5 → exact 5-card subsets
Ownership rule (identical in both modes): the claimant must own at least one
card of the evaluated hand. It need not be a card of the claimed pattern.

══════════════════════════════════════════════════════════════════════════
SCENARIO 1 — Four-player round 1 (reduced pool of 4)
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=1 D=1 — pool 4 cards, mode REDUCED_POOL, hand width 4
Actual cards        : A:8♠   B:8♥   C:K♦   D:3♣
                    : whole pool [3♣ 8♥ 8♠ K♦] classifies as Pair of 8s
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Pair of 8s
Structurally legal  : yes — ordinal 18 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [3♣ 8♥ 8♠ K♦] classifies as Pair of 8s
                    : A contributes 8♠ (part of the claimed pattern)
Mode                : REDUCED_POOL (pool 4, hand width 4)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 — pool 5 cards, mode EXACT_FIVE, hand width 5
Next starter        : B (seat 1)

══════════════════════════════════════════════════════════════════════════
SCENARIO 2 — Four-card High Card claim
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=1 D=1 — pool 4 cards, mode REDUCED_POOL, hand width 4
Actual cards        : A:K♠   B:8♥   C:5♦   D:3♣
                    : whole pool [3♣ 5♦ 8♥ K♠] classifies as King high
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : King high
Structurally legal  : yes — ordinal 10 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [3♣ 5♦ 8♥ K♠] classifies as King high
                    : A contributes K♠ (part of the claimed pattern)
Mode                : REDUCED_POOL (pool 4, hand width 4)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 — pool 5 cards, mode EXACT_FIVE, hand width 5
Next starter        : B (seat 1)

══════════════════════════════════════════════════════════════════════════
SCENARIO 3 — Four-card Pair claim — true for every in-play player
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=1 D=1 — pool 4 cards, mode REDUCED_POOL, hand width 4
Actual cards        : A:8♠   B:8♥   C:K♦   D:3♣
                    : whole pool [3♣ 8♥ 8♠ K♦] classifies as Pair of 8s
Round / starter     : round 1, starter A (seat 0)
  A claims Pair of 8s: true
  B claims Pair of 8s: true
  C claims Pair of 8s: true
  D claims Pair of 8s: true
                      (the evaluated hand is the whole pool, so every
                       player owns a card of it and ownership is satisfied)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Pair of 8s
Structurally legal  : yes — ordinal 18 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [3♣ 8♥ 8♠ K♦] classifies as Pair of 8s
                    : A contributes 8♠ (part of the claimed pattern)
Mode                : REDUCED_POOL (pool 4, hand width 4)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 (pending round advance)

══════════════════════════════════════════════════════════════════════════
SCENARIO 4 — False four-card claim — the pool does not classify as the claim
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=1 D=1 — pool 4 cards, mode REDUCED_POOL, hand width 4
Actual cards        : A:K♠   B:8♥   C:8♦   D:3♣
                    : whole pool [3♣ 8♦ 8♥ K♠] classifies as Pair of 8s
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Three 8s
Structurally legal  : yes — ordinal 109 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : FALSE — the available 4-card pool classifies as Pair of 8s, not Three 8s
Mode                : REDUCED_POOL (pool 4, hand width 4)
SHOW outcome        : claim FALSE — B wins, A loses
Penalty             : A 1 → 2 cards
Next-round counts   : A=2 B=1 C=1 D=1 — pool 5 cards, mode EXACT_FIVE, hand width 5
Next starter        : B (seat 1)

  Same pool, the claim the pool actually supports — and note A owns
  only the king, not either 8, which the ownership rule permits:
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Pair of 8s
Structurally legal  : yes — ordinal 18 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [3♣ 8♦ 8♥ K♠] classifies as Pair of 8s
                    : A contributes K♠ (outside the claimed pattern, which the ownership rule permits)
Mode                : REDUCED_POOL (pool 4, hand width 4)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 (pending round advance)

══════════════════════════════════════════════════════════════════════════
SCENARIO 5 — Four-card SHOW: who may call it, and the penalty that follows
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=1 D=1 — pool 4 cards, mode REDUCED_POOL, hand width 4
Actual cards        : A:9♠   B:9♥   C:Q♦   D:4♣
                    : whole pool [4♣ 9♥ 9♠ Q♦] classifies as Pair of 9s
Round / starter     : round 1, starter A (seat 0)
Starter may SHOW?   : false — no standing claim exists
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Pair of 9s
Structurally legal  : yes — ordinal 19 of 367, no standing claim to beat
  SHOW by A         : rejected: game: only the immediate next player may call SHOW
  SHOW by B         : accepted
  SHOW by C         : rejected: game: only the immediate next player may call SHOW
  SHOW by D         : rejected: game: only the immediate next player may call SHOW

══════════════════════════════════════════════════════════════════════════
SCENARIO 6 — Penalty after a four-card SHOW
══════════════════════════════════════════════════════════════════════════
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [4♣ 9♥ 9♠ Q♦] classifies as Pair of 9s
                    : A contributes 9♠ (part of the claimed pattern)
Mode                : REDUCED_POOL (pool 4, hand width 4)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 — pool 5 cards, mode EXACT_FIVE, hand width 5
Next starter        : B (seat 1)

══════════════════════════════════════════════════════════════════════════
SCENARIO 7 — Round 2 with five total cards — mode switches to exact five
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=2 B=1 C=1 D=1 — pool 5 cards, mode EXACT_FIVE, hand width 5
Actual cards        : A:8♥ 8♠   B:K♦   C:3♣   D:A♦
                    : whole pool [3♣ 8♥ 8♠ K♦ A♦] classifies as Pair of 8s
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Pair of 8s
Structurally legal  : yes — ordinal 18 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [3♣ 8♥ 8♠ K♦ A♦] classifies as Pair of 8s
                    : A contributes 8♥ 8♠ (part of the claimed pattern)
Mode                : EXACT_FIVE (pool 5, hand width 5)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=2 B=2 C=1 D=1 (pending round advance)

══════════════════════════════════════════════════════════════════════════
SCENARIO 8 — Normal five-card validation, five players
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=1 D=1 E=1 — pool 5 cards, mode EXACT_FIVE, hand width 5
Actual cards        : A:8♠   B:8♥   C:K♦   D:3♣   E:A♦
                    : whole pool [3♣ 8♥ 8♠ K♦ A♦] classifies as Pair of 8s
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Pair of 8s
Structurally legal  : yes — ordinal 18 of 367, no standing claim to beat
Current claimant    : B (seat 1, raise)
Claim               : Three 8s
Structurally legal  : yes — ordinal 109 of 367, beats standing Pair of 8s (ordinal 18)
                      (only the last claim before SHOW is ever validated)
Next player action  : C (seat 2) calls SHOW
Exact validation    : FALSE — no 5-card subset of the 5-card pool classifies as Three 8s
Mode                : EXACT_FIVE (pool 5, hand width 5)
SHOW outcome        : claim FALSE — C wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 E=1 — pool 6 cards, mode EXACT_FIVE, hand width 5
Next starter        : B (seat 1)

══════════════════════════════════════════════════════════════════════════
SCENARIO 9 — Extra-card exclusion — six cards, three 8s, Pair of 8s
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=1 D=1 E=1 F=1 — pool 6 cards, mode EXACT_FIVE, hand width 5
Actual cards        : A:8♠   B:8♥   C:8♦   D:K♣   E:3♣   F:A♦
                    : pool [3♣ 8♦ 8♥ 8♠ K♣ A♦] — validation quantifies over all 5-card subsets
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Pair of 8s
Structurally legal  : yes — ordinal 18 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [3♣ 8♦ 8♠ K♣ A♦] classifies as Pair of 8s
                    : A contributes 8♠ (part of the claimed pattern)
Mode                : EXACT_FIVE (pool 6, hand width 5)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 E=1 F=1 (pending round advance)

══════════════════════════════════════════════════════════════════════════
SCENARIO 10 — Stronger hand vs weaker claim — the same three 8s, five-card pool
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=1 D=1 E=1 — pool 5 cards, mode EXACT_FIVE, hand width 5
Actual cards        : A:8♠   B:8♥   C:8♦   D:K♣   E:3♣
                    : whole pool [3♣ 8♦ 8♥ 8♠ K♣] classifies as Three 8s
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Pair of 8s
Structurally legal  : yes — ordinal 18 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : FALSE — no 5-card subset of the 5-card pool classifies as Pair of 8s
Mode                : EXACT_FIVE (pool 5, hand width 5)
SHOW outcome        : claim FALSE — B wins, A loses
Penalty             : A 1 → 2 cards
Next-round counts   : A=2 B=1 C=1 D=1 E=1 (pending round advance)

  Same pool, correct claim:
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Three 8s
Structurally legal  : yes — ordinal 109 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [3♣ 8♦ 8♥ 8♠ K♣] classifies as Three 8s
                    : A contributes 8♠ (part of the claimed pattern)
Mode                : EXACT_FIVE (pool 5, hand width 5)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 E=1 (pending round advance)

  And the four-card version of the same principle:
Player card counts  : A=1 B=1 C=1 D=1 — pool 4 cards, mode REDUCED_POOL, hand width 4
Actual cards        : A:8♠   B:8♥   C:8♦   D:K♣
                    : whole pool [8♦ 8♥ 8♠ K♣] classifies as Three 8s
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Pair of 8s
Structurally legal  : yes — ordinal 18 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : FALSE — the available 4-card pool classifies as Three 8s, not Pair of 8s
Mode                : REDUCED_POOL (pool 4, hand width 4)
SHOW outcome        : claim FALSE — B wins, A loses
Penalty             : A 1 → 2 cards
Next-round counts   : A=2 B=1 C=1 D=1 (pending round advance)

══════════════════════════════════════════════════════════════════════════
SCENARIO 11 — Ownership at five or more cards — the selection can exclude the claimant
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=1 D=1 E=1 F=1 — pool 6 cards, mode EXACT_FIVE, hand width 5
Actual cards        : A:2♣   B:7♠   C:7♥   D:7♦   E:8♠   F:8♥
                    : pool [2♣ 7♦ 7♥ 7♠ 8♥ 8♠] — validation quantifies over all 5-card subsets
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : 7s full of 8s
Structurally legal  : yes — ordinal 223 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : FALSE — the hand exists as [7♦ 7♥ 7♠ 8♥ 8♠], but no selection realizing it contains a card owned by A
Mode                : EXACT_FIVE (pool 6, hand width 5)
SHOW outcome        : claim FALSE — B wins, A loses
Penalty             : A 1 → 2 cards
Next-round counts   : A=2 B=1 C=1 D=1 E=1 F=1 (pending round advance)

  Same pool, a claim that leaves a free slot for A's own card:
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Three 7s
Structurally legal  : yes — ordinal 108 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [2♣ 7♦ 7♥ 7♠ 8♥] classifies as Three 7s
                    : A contributes 2♣ (outside the claimed pattern, which the ownership rule permits)
Mode                : EXACT_FIVE (pool 6, hand width 5)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 E=1 F=1 (pending round advance)

  And a five-card pool where the claimant owns only the kicker:
Player card counts  : A=1 B=1 C=1 D=1 E=1 — pool 5 cards, mode EXACT_FIVE, hand width 5
Actual cards        : A:K♠   B:8♥   C:8♦   D:8♠   E:8♣
                    : whole pool [8♣ 8♦ 8♥ 8♠ K♠] classifies as Four 8s
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Four 8s
Structurally legal  : yes — ordinal 320 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [8♣ 8♦ 8♥ 8♠ K♠] classifies as Four 8s
                    : A contributes K♠ (outside the claimed pattern, which the ownership rule permits)
Mode                : EXACT_FIVE (pool 5, hand width 5)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 E=1 (pending round advance)

══════════════════════════════════════════════════════════════════════════
SCENARIO 12 — Multiple players contributing to one witness
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=1 D=1 E=1 F=1 — pool 6 cards, mode EXACT_FIVE, hand width 5
Actual cards        : A:7♠   B:7♥   C:7♦   D:K♣   E:3♣   F:A♦
                    : pool [3♣ 7♦ 7♥ 7♠ K♣ A♦] — validation quantifies over all 5-card subsets
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Three 7s
Structurally legal  : yes — ordinal 108 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [3♣ 7♦ 7♥ 7♠ K♣] classifies as Three 7s
                    : A contributes 7♠ (part of the claimed pattern)
Mode                : EXACT_FIVE (pool 6, hand width 5)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=1 D=1 E=1 F=1 (pending round advance)
Witness spans       : 5 different players' hands

══════════════════════════════════════════════════════════════════════════
SCENARIO 13 — Progression from a four-card pool to five and beyond
══════════════════════════════════════════════════════════════════════════
Round 1             : A=1 B=1 C=1 D=1 — pool 4 cards, mode REDUCED_POOL, hand width 4
                    : A claimed Royal Flush of Spades → FALSE → A penalised
Round 2             : A=2 B=1 C=1 D=1 — pool 5 cards, mode EXACT_FIVE, hand width 5
                    : B claimed Royal Flush of Spades → FALSE → B penalised
Round 3             : A=2 B=2 C=1 D=1 — pool 6 cards, mode EXACT_FIVE, hand width 5
                    : C claimed Royal Flush of Spades → FALSE → C penalised
Round 4             : A=2 B=2 C=2 D=1 — pool 7 cards, mode EXACT_FIVE, hand width 5
                    : D claimed Royal Flush of Spades → FALSE → D penalised
Round 5             : A=2 B=2 C=2 D=2 — pool 8 cards, mode EXACT_FIVE, hand width 5

══════════════════════════════════════════════════════════════════════════
SCENARIO 14 — A five-card player loses a SHOW
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=5 B=2 C=1 D=1 — pool 9 cards, mode EXACT_FIVE, hand width 5
Actual cards        : A:2♣ 3♦ 4♥ 6♠ 9♣   B:Q♥ K♦   C:5♣   D:7♠
                    : pool [2♣ 3♦ 4♥ 5♣ 6♠ 7♠ 9♣ Q♥ K♦] — validation quantifies over all 5-card subsets
Round / starter     : round 1, starter A (seat 0)
Note                : A is at the maximum hand size of 5; one more loss ends their game
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Royal Flush of Spades
Structurally legal  : yes — ordinal 366 of 367, no standing claim to beat

══════════════════════════════════════════════════════════════════════════
SCENARIO 15 — Attempted sixth card → elimination
══════════════════════════════════════════════════════════════════════════
Next player action  : B (seat 1) calls SHOW
Exact validation    : FALSE — no 5-card subset of the 9-card pool classifies as Royal Flush of Spades
Mode                : EXACT_FIVE (pool 9, hand width 5)
SHOW outcome        : claim FALSE — B wins, A loses
Penalty             : A would be dealt 6 cards — ELIMINATED (capped at 5)
Next-round counts   : A=out B=2 C=1 D=1 — pool 4 cards, mode REDUCED_POOL, hand width 4
Next starter        : B (seat 1)
A status            : ELIMINATED, hand size 5
Table now           : A=out B=2 C=1 D=1 — pool 4 cards, mode REDUCED_POOL, hand width 4

══════════════════════════════════════════════════════════════════════════
SCENARIO 16 — Starter rotation is independent of SHOW outcome and penalty
══════════════════════════════════════════════════════════════════════════
claimant loses      : R1 start=A lost=A | R2 start=B lost=B | R3 start=C lost=C | R4 start=D lost=D | R5 start=A lost=A
challenger loses    : R1 start=A lost=B | R2 start=B lost=C | R3 start=C lost=D | R4 start=D lost=A | R5 start=A lost=B
Conclusion          : identical sequences: rotation reads only seats, eliminations and round number

══════════════════════════════════════════════════════════════════════════
SCENARIO 17 — Two-player endgame
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=3 B=2 C=out D=out — pool 5 cards, mode EXACT_FIVE, hand width 5
Actual cards        : A:4♦ 9♥ 9♠   B:3♠ K♣
                    : whole pool [3♠ 4♦ 9♥ 9♠ K♣] classifies as Pair of 9s
Round / starter     : round 1, starter A (seat 0)
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Pair of 9s
Structurally legal  : yes — ordinal 19 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [3♠ 4♦ 9♥ 9♠ K♣] classifies as Pair of 9s
                    : A contributes 4♦ 9♥ 9♠ (part of the claimed pattern)
Mode                : EXACT_FIVE (pool 5, hand width 5)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 2 → 3 cards
Next-round counts   : A=3 B=3 C=out D=out — pool 6 cards, mode EXACT_FIVE, hand width 5
Next starter        : B (seat 1)

══════════════════════════════════════════════════════════════════════════
SCENARIO 18 — A later sub-five pool: two survivors on one card each
══════════════════════════════════════════════════════════════════════════
Player card counts  : A=1 B=1 C=out D=out — pool 2 cards, mode REDUCED_POOL, hand width 2
Actual cards        : A:K♠   B:7♥
                    : whole pool [7♥ K♠] classifies as King high
Round / starter     : round 1, starter A (seat 0)
Note                : a two-card pool can only realize High Card and Pair claims
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : King high
Structurally legal  : yes — ordinal 10 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : TRUE — witness [7♥ K♠] classifies as King high
                    : A contributes K♠ (part of the claimed pattern)
Mode                : REDUCED_POOL (pool 2, hand width 2)
SHOW outcome        : claim TRUE — A wins, B loses
Penalty             : B 1 → 2 cards
Next-round counts   : A=1 B=2 C=out D=out — pool 3 cards, mode REDUCED_POOL, hand width 3
Next starter        : B (seat 1)

  And the same pool with a claim the width cannot support:
Current claimant    : A (seat 0, round starter, opening claim)
Claim               : Straight to 9
Structurally legal  : yes — ordinal 120 of 367, no standing claim to beat
Next player action  : B (seat 1) calls SHOW
Exact validation    : FALSE — the available 2-card pool classifies as King high, not Straight to 9
Mode                : REDUCED_POOL (pool 2, hand width 2)
SHOW outcome        : claim FALSE — B wins, A loses
Penalty             : A 1 → 2 cards
Next-round counts   : A=2 B=1 C=out D=out (pending round advance)

══════════════════════════════════════════════════════════════════════════
All 18 scenarios executed by the engine. No invariant violations.
══════════════════════════════════════════════════════════════════════════
```

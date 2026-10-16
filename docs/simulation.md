# Phase 2 — Simulation

The simulator exists to find what hand-written tests miss: state-machine
holes, deck-accounting drift, evaluator gaps, and turn-order bugs that only
appear in long games with unusual hand-size distributions.

## The design constraint that makes it worth having

**A policy sees only a `game.PlayerView`, never the `State`.** It gets its own
cards, the public information, and the standing claim's ordinal — exactly what
a real client receives over the wire. A policy therefore physically cannot
consult the pool, the ownership map, the deck, or another player's hand.

That makes the simulator a standing proof that the view carries enough
information to play the game, which is the property Phase 3's transport and
Phase 4's UI both depend on. `TestPoliciesNeverSeeHiddenCards` wraps a policy
in a spy, records every view it is handed, and asserts none ever contained
another player's cards.

Legal claims are derived the same way a client will derive them: take the
static 367-element claim space and filter to ordinals above
`view.StandingOrdinal`. No rule logic on the client side.

## Policies

| Policy | Behaviour | Why it earns its place |
|---|---|---|
| `always-show` | Challenge at the first opportunity | Near-optimal against a reduced pool; the baseline |
| `min-raise` | Never challenge while a raise exists | Worst case for round length; forces the SHOW-at-maximum path every round |
| `random-raise` | Challenge with probability p, else raise within a window | General-purpose noise |
| `cautious` | Raise only to the lowest claim its own cards could contribute to at the current hand width; challenge otherwise | Exercises `RealizableAtWidth` and the truth path |
| `bluffer` | Jump up the ladder, rarely challenge | Drives claims far above what the cards support, hammering the FALSE branch |

`mixed` cycles all of them across seats. The assignment is **rotated by
seed**, so each policy visits each seat roughly equally — without that, the
wins-by-seat table silently measures policy strength instead of positional
bias.

## Usage

```
go run ./cmd/sim                            # 2000 mixed games, 4-9 players
go run ./cmd/sim -games 100000 -workers 8
go run ./cmd/sim -players 4 -policy always-show
go run ./cmd/sim -games 5 -trace            # print every SHOW resolution
```

Invariant and leak checking run after **every action** by default. Any seed
that trips an invariant, leaks a card, or fails to terminate is printed with
its seed and the process exits non-zero, so a bad seed becomes a permanent
fixture rather than a flake.

Games run concurrently across goroutines. That is deliberate: the engine is a
pure reducer, so concurrent games share nothing, and `go test -race` on this
package is a real check of that claim rather than a formality.

## Findings

Three things the simulator established that the unit tests did not:

1. **The round-termination bound is tight, not conservative.** A table of
   `min-raise` bots produced a round of exactly **367 claims** — the full
   claim space — which then resolved through the forced-SHOW-at-maximum path.
   The ceiling is reachable, so it needs to be handled, and it is.
2. **Reduced-pool mode is roughly 7% of all rounds** at mixed table sizes,
   and it recurs after eliminations rather than only in round 1.
3. **Challenging beats claiming under these bots.** `always-show` wins about
   22% of its seats while `bluffer` wins under 1%. Only the claimant can be
   caught lying, and only ~20% of challenged claims hold up, so SHOW is
   strongly positive expected value. This says more about bot quality than
   about the rules, but it is worth watching once real players are on it.

4. **Relaxing the ownership rule did not move the balance.** Moving to the
   single uniform ownership rule left the true-rate at 20.1%, identical to
   the measurement before the change. Reduced rounds are too small a share of
   play, and the claims these bots make fail on the pattern rather than on
   ownership. Recorded because the opposite was predicted.

## Latest run

```text
Liar's Poker simulator
  games=1200 players=4-9 policy=mixed workers=4 seed=1 invariants=true leaks=true
  claim space=367  min=Three high  max=Royal Flush of Spades

TOTALS
--------------------------------------------------------------
  games completed      1200
  rounds               34648  (mean 28.9 per game, min 15, max 44)
  claims               199088  (mean 5.75 per round)
  SHOWs resolved       34648
  eliminations         6600
  actions applied      267184
  longest round        325 claims (ceiling is 367)
  wall time            18.723s  (64 games/s, 14271 actions/s)

SHOW OUTCOMES
--------------------------------------------------------------
  claim held up        6962  (20.1%)  -> challenger penalised
  claim was a bluff    27686  (79.9%)  -> claimant penalised

EVALUATION MODE BY ROUND
--------------------------------------------------------------
  REDUCED_POOL (<5)    2565  (7.4%)
  EXACT_FIVE  (>=5)    32083  (92.6%)

ROUNDS PER GAME BY TABLE SIZE
--------------------------------------------------------------
  4 players              200 games, mean 16.3 rounds
  5 players              200 games, mean 21.5 rounds
  6 players              200 games, mean 26.6 rounds
  7 players              200 games, mean 31.6 rounds
  8 players              200 games, mean 36.3 rounds
  9 players              200 games, mean 40.9 rounds

VALIDATED CLAIM CATEGORIES (the claim that ended each round)
--------------------------------------------------------------
  Two Pair              14739  (42.5%)
  High Card             11224  (32.4%)
  Pair                   6209  (17.9%)
  Three of a Kind         603  (1.7%)
  Flush                   586  (1.7%)
  Full House              542  (1.6%)
  Straight                359  (1.0%)
  Royal Flush             276  (0.8%)
  Straight Flush           62  (0.2%)
  Four of a Kind           48  (0.1%)

WIN RATE BY POLICY
--------------------------------------------------------------
  always-show       868 seats,   202 wins  (23.3% of its seats)
  bluffer          1733 seats,     8 wins  (0.5% of its seats)
  cautious         1732 seats,    19 wins  (1.1% of its seats)
  min-raise         867 seats,    34 wins  (3.9% of its seats)
  random-raise     2600 seats,   937 wins  (36.0% of its seats)
  (a seat wins 1/n of the time by chance, so compare against that)

WINS BY SEAT (normalised by games in which the seat existed)
--------------------------------------------------------------
  seat 0    218 wins /  1200 games  (18.2%)
  seat 1    232 wins /  1200 games  (19.3%)
  seat 2    198 wins /  1200 games  (16.5%)
  seat 3    167 wins /  1200 games  (13.9%)
  seat 4    173 wins /  1000 games  (17.3%)
  seat 5    104 wins /   800 games  (13.0%)
  seat 6     22 wins /   600 games  (3.7%)
  seat 7     83 wins /   400 games  (20.8%)
  seat 8      3 wins /   200 games  (1.5%)
  (with n seats a fair game gives each 1/n; table sizes cycle, so
   compare seats against each other rather than a single baseline)

FAILURES
--------------------------------------------------------------
  none: every game terminated with a winner, every invariant held
  after every action, and no view ever leaked a hidden card.
```

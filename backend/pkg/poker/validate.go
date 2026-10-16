package poker

import (
	"encoding/json"
	"fmt"
)

// EvalMode names which validation width was used, for events and logs.
type EvalMode uint8

const (
	// ModeExactFive is the normal mode: the pool holds five or more cards
	// and validation quantifies over every five-card subset.
	ModeExactFive EvalMode = iota
	// ModeReducedPool is the exception: the pool holds fewer than five
	// cards, so the complete available pool is the hand.
	ModeReducedPool
)

func (m EvalMode) String() string {
	if m == ModeReducedPool {
		return "REDUCED_POOL"
	}
	return "EXACT_FIVE"
}

// MarshalJSON emits the mode name rather than its ordinal, so the wire form
// survives any future reordering of the constants.
func (m EvalMode) MarshalJSON() ([]byte, error) { return []byte(`"` + m.String() + `"`), nil }

// UnmarshalJSON accepts the mode name. Clients decode views and events that
// carry a mode, so the round trip has to close.
func (m *EvalMode) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	switch s {
	case "REDUCED_POOL":
		*m = ModeReducedPool
	case "EXACT_FIVE":
		*m = ModeExactFive
	default:
		return fmt.Errorf("poker: unknown eval mode %q", s)
	}
	return nil
}

// HandSize is the number of cards a claim is evaluated over, given the size
// of the distributed pool.
//
// This single expression is the whole of the reduced-pool rule. Validation
// always quantifies over subsets of the pool of size HandSize(pool), so:
//
//	pool >= 5 -> subsets of size 5, extra cards may be excluded
//	pool <  5 -> subsets of size len(pool), i.e. the one whole pool
//
// There is no second code path and no short-hand hierarchy: the classifier
// handles narrower hands with the same category definitions, and Flush,
// Straight, Full House, Straight Flush and Royal Flush are unreachable below
// five cards because each needs five cards by definition.
func HandSize(poolSize int) int {
	if poolSize < FullHandSize {
		return poolSize
	}
	return FullHandSize
}

// ModeFor reports which mode a pool size evaluates under.
func ModeFor(poolSize int) EvalMode {
	if poolSize < FullHandSize {
		return ModeReducedPool
	}
	return ModeExactFive
}

// There is exactly one ownership rule, and it is identical at every pool
// size:
//
//	the claimant must own at least one card of the evaluated hand.
//
// The claimant does NOT need to own any card of the claimed poker pattern. A
// kicker, a filler, or any other selected card is enough. This holds for the
// reduced-pool mode and the exact-five mode alike; there is no per-mode
// scope, and deliberately no configuration knob, because a knob here would
// invite the two modes to drift apart again.
//
// The rule's practical force does differ between modes, but that is
// geometry, not a second rule: in reduced mode the evaluated hand *is* the
// entire pool, which necessarily contains a card from every in-play player,
// so ownership is always satisfied there. At five or more cards the
// evaluated hand is a proper subset, so a claim whose only realization
// excludes the claimant's cards is false. See OwnershipSatisfied.

// Owns reports whether a card belongs to the claimant. The poker package
// deliberately knows nothing about players, so ownership arrives as a
// predicate.
type Owns func(Card) bool

// PatternCards returns the cards of hand that form the claimed pattern: the
// high card for High Card, the pair for Pair, both pairs for Two Pair, the
// trips for Three of a Kind, the four cards for Four of a Kind, and the whole
// hand for the five-card categories.
//
// This is a presentation helper, for highlighting the pattern inside a
// revealed witness. It has NO role in validation and in particular no role
// in the ownership check: the claimant need not own any of these cards.
func PatternCards(hand []Card, c Claim) []Card {
	var wanted map[Rank]bool
	switch c.Category {
	case HighCard, OnePair, ThreeOfAKind, FourOfAKind:
		wanted = map[Rank]bool{c.Primary: true}
	case TwoPair:
		wanted = map[Rank]bool{c.Primary: true, c.Secondary: true}
	default:
		out := make([]Card, len(hand))
		copy(out, hand)
		return out
	}
	out := make([]Card, 0, len(hand))
	for _, card := range hand {
		if wanted[card.Rank] {
			out = append(out, card)
		}
	}
	return out
}

// OwnershipSatisfied reports whether the claimant owns at least one card of
// the evaluated hand. That is the entire ownership rule, at every pool size.
// The claimed pattern is irrelevant here by design.
func OwnershipSatisfied(hand []Card, owns Owns) bool {
	for _, card := range hand {
		if owns(card) {
			return true
		}
	}
	return false
}

// Satisfies reports whether an exact hand is exactly the claim, ignoring
// ownership. This is one struct equality against the classifier, which is
// why a stronger hand never validates a weaker claim.
func Satisfies(hand []Card, c Claim) bool {
	v, err := Classify(hand)
	return err == nil && v == c
}

// Result is the outcome of validating a claim against a revealed pool.
type Result struct {
	Satisfied bool
	Witness   []Card // the five (or fewer) cards used; nil when not satisfied
	Mode      EvalMode
	PoolSize  int
	HandSize  int
	// Examined counts candidate hands tested, for benchmarking.
	Examined int
}

func (r Result) String() string {
	if !r.Satisfied {
		return fmt.Sprintf("FALSE (mode=%s pool=%d hand=%d examined=%d)",
			r.Mode, r.PoolSize, r.HandSize, r.Examined)
	}
	return fmt.Sprintf("TRUE via [%s] (mode=%s pool=%d hand=%d examined=%d)",
		CardsString(r.Witness), r.Mode, r.PoolSize, r.HandSize, r.Examined)
}

// ---------------------------------------------------- exhaustive oracle

// CanSatisfyExhaustive is the reference implementation: it enumerates every
// subset of the pool of the right width in canonical order and returns the
// first that is exactly the claim and satisfies ownership.
//
// Because subsets are generated in canonical index order over a canonically
// sorted pool, the witness it returns is the lexicographically smallest
// satisfying selection. That makes it the definition of the canonical
// witness, and it is kept permanently as the differential-test oracle for
// CanSatisfy.
//
// Worst case is a 45-card pool, giving C(45,5) = 1,221,759 candidates.
func CanSatisfyExhaustive(pool []Card, owns Owns, c Claim) Result {
	n := len(pool)
	res := Result{Mode: ModeFor(n), PoolSize: n, HandSize: HandSize(n)}
	if res.HandSize < MinHandSize {
		return res
	}
	sorted := SortedCopy(pool)
	forEachCombination(sorted, res.HandSize, func(hand []Card) bool {
		res.Examined++
		if Satisfies(hand, c) && OwnershipSatisfied(hand, owns) {
			res.Satisfied = true
			res.Witness = append([]Card(nil), hand...)
			return true
		}
		return false
	})
	return res
}

// ---------------------------------------------------- constructive search

// cardGroup is a set of candidate cards and how many of them a satisfying
// hand must draw.
type cardGroup struct {
	cards []Card
	take  int
}

// CanSatisfy is the production validator. It is claim-directed: rather than
// enumerating all subsets of the pool, it restricts candidates to the cards
// that could possibly participate in the claimed pattern and draws the
// pattern and its free slots from disjoint groups.
//
// Every candidate it builds is still verified with Satisfies before being
// accepted, so the construction can only ever cause a false negative, never
// a false positive. Soundness therefore holds by construction; completeness
// is what the differential fuzz test against CanSatisfyExhaustive checks.
func CanSatisfy(pool []Card, owns Owns, c Claim) Result {
	n := len(pool)
	res := Result{Mode: ModeFor(n), PoolSize: n, HandSize: HandSize(n)}
	if res.HandSize < MinHandSize {
		return res
	}
	sorted := SortedCopy(pool)

	// Reduced-pool mode: the pool is its own only selection.
	if res.Mode == ModeReducedPool {
		res.Examined = 1
		if Satisfies(sorted, c) && OwnershipSatisfied(sorted, owns) {
			res.Satisfied = true
			res.Witness = sorted
		}
		return res
	}

	groups, ok := candidateGroups(sorted, c)
	if !ok {
		return res // a necessary condition failed; no selection can exist
	}
	acc := make([]Card, 0, FullHandSize)
	searchGroups(groups, acc, func(hand []Card) bool {
		res.Examined++
		if Satisfies(hand, c) && OwnershipSatisfied(hand, owns) {
			res.Satisfied = true
			res.Witness = SortedCopy(hand)
			return true
		}
		return false
	})
	return res
}

// candidateGroups builds the disjoint draw groups for a claim, or reports
// that a necessary condition already fails.
func candidateGroups(pool []Card, c Claim) ([]cardGroup, bool) {
	byRank := map[Rank][]Card{}
	for _, card := range pool {
		byRank[card.Rank] = append(byRank[card.Rank], card)
	}
	othersExcluding := func(ranks ...Rank) []Card {
		skip := map[Rank]bool{}
		for _, r := range ranks {
			skip[r] = true
		}
		out := make([]Card, 0, len(pool))
		for _, card := range pool {
			if !skip[card.Rank] {
				out = append(out, card)
			}
		}
		return out
	}
	suited := func(s Suit, maxRank Rank) []Card {
		out := make([]Card, 0, 13)
		for _, card := range pool {
			if card.Suit == s && card.Rank <= maxRank {
				out = append(out, card)
			}
		}
		return out
	}
	ofRanks := func(rs map[Rank]bool) []Card {
		out := make([]Card, 0, len(pool))
		for _, card := range pool {
			if rs[card.Rank] {
				out = append(out, card)
			}
		}
		return out
	}

	mk := func(gs ...cardGroup) ([]cardGroup, bool) {
		for _, g := range gs {
			if len(g.cards) < g.take {
				return nil, false
			}
		}
		return gs, true
	}

	switch c.Category {
	case RoyalFlush:
		want := map[Rank]bool{Ten: true, Jack: true, Queen: true, King: true, Ace: true}
		cards := make([]Card, 0, 5)
		for _, card := range pool {
			if card.Suit == c.Suit && want[card.Rank] {
				cards = append(cards, card)
			}
		}
		return mk(cardGroup{cards, 5})

	case StraightFlush:
		want := straightRankSet(c.Primary)
		cards := make([]Card, 0, 5)
		for _, card := range pool {
			if card.Suit == c.Suit && want[card.Rank] {
				cards = append(cards, card)
			}
		}
		return mk(cardGroup{cards, 5})

	case Flush:
		// Only cards of the declared suit at or below the declared high
		// card can appear, which is what makes the declared high card the
		// maximum of the selection.
		return mk(cardGroup{suited(c.Suit, c.Primary), 5})

	case Straight:
		return mk(cardGroup{ofRanks(straightRankSet(c.Primary)), 5})

	case FullHouse:
		return mk(
			cardGroup{byRank[c.Primary], 3},
			cardGroup{byRank[c.Secondary], 2},
		)

	case FourOfAKind:
		return mk(
			cardGroup{byRank[c.Primary], 4},
			cardGroup{othersExcluding(c.Primary), 1},
		)

	case ThreeOfAKind:
		return mk(
			cardGroup{byRank[c.Primary], 3},
			cardGroup{othersExcluding(c.Primary), 2},
		)

	case TwoPair:
		return mk(
			cardGroup{byRank[c.Primary], 2},
			cardGroup{byRank[c.Secondary], 2},
			cardGroup{othersExcluding(c.Primary, c.Secondary), 1},
		)

	case OnePair:
		return mk(
			cardGroup{byRank[c.Primary], 2},
			cardGroup{othersExcluding(c.Primary), 3},
		)

	case HighCard:
		below := make([]Card, 0, len(pool))
		for _, card := range pool {
			if card.Rank < c.Primary {
				below = append(below, card)
			}
		}
		return mk(
			cardGroup{byRank[c.Primary], 1},
			cardGroup{below, 4},
		)
	}
	return nil, false
}

// straightRankSet returns the five ranks of the straight with the given high
// card. High card 5 is the ace-low wheel.
func straightRankSet(high Rank) map[Rank]bool {
	out := map[Rank]bool{}
	if high == Five {
		out[Ace] = true
		for r := Two; r <= Five; r++ {
			out[r] = true
		}
		return out
	}
	for r := high - 4; r <= high; r++ {
		out[r] = true
	}
	return out
}

// searchGroups walks the cross product of combinations across groups,
// appending to acc, and stops as soon as fn returns true.
func searchGroups(groups []cardGroup, acc []Card, fn func([]Card) bool) bool {
	if len(groups) == 0 {
		return fn(acc)
	}
	g := groups[0]
	rest := groups[1:]
	return forEachCombination(g.cards, g.take, func(chosen []Card) bool {
		next := append(acc, chosen...)
		stop := searchGroups(rest, next, fn)
		return stop
	})
}

// forEachCombination calls fn with every k-subset of cards, in canonical
// index order, stopping early if fn returns true. It reuses one buffer, so
// fn must copy anything it keeps.
func forEachCombination(cards []Card, k int, fn func([]Card) bool) bool {
	n := len(cards)
	if k < 0 || k > n {
		return false
	}
	if k == 0 {
		return fn(nil)
	}
	idx := make([]int, k)
	for i := range idx {
		idx[i] = i
	}
	buf := make([]Card, k)
	for {
		for i, j := range idx {
			buf[i] = cards[j]
		}
		if fn(buf) {
			return true
		}
		i := k - 1
		for i >= 0 && idx[i] == i+n-k {
			i--
		}
		if i < 0 {
			return false
		}
		idx[i]++
		for j := i + 1; j < k; j++ {
			idx[j] = idx[j-1] + 1
		}
	}
}

// StraightRanks returns the five ranks of the straight with the given high
// card, ascending. High card 5 is the ace-low wheel, so the ace comes last
// in rank order despite being the low end of the run.
func StraightRanks(high Rank) []Rank {
	set := straightRankSet(high)
	out := make([]Rank, 0, FullHandSize)
	for r := MinRank; r <= MaxRank; r++ {
		if set[r] {
			out = append(out, r)
		}
	}
	return out
}

// MinWidthFor is the smallest hand width at which a category can be
// realized at all. Five-card categories need all five cards by definition;
// the others need only their pattern.
func MinWidthFor(cat Category) int {
	switch cat {
	case HighCard:
		return 1
	case OnePair:
		return 2
	case ThreeOfAKind:
		return 3
	case TwoPair, FourOfAKind:
		return 4
	default:
		return FullHandSize
	}
}

// RealizableAtWidth reports whether some hand of exactly `width` cards
// realizes the claim. This is width-sensitive realizability, as distinct
// from claim-space membership, which asks whether *any* width can realize
// it.
//
// Two uses. The simulator's truth-aware bot uses it to avoid claims that
// cannot be true at the current pool size, and the claim builder can use it
// to mark such claims as guaranteed losses without hiding them, since
// bluffing them is still legal.
func RealizableAtWidth(c Claim, width int) bool {
	if !c.Legal() || width < MinHandSize || width > FullHandSize {
		return false
	}
	if width < MinWidthFor(c.Category) {
		return false
	}
	if c.Category == HighCard {
		// A high-card hand needs `width` distinct ranks at or below the
		// declared high card; ranks 2..high number high-1.
		if int(c.Primary)-1 < width {
			return false
		}
		// At full width a 6-high hand could only be 2-3-4-5-6, a straight.
		if width == FullHandSize && c.Primary < Seven {
			return false
		}
	}
	return true
}

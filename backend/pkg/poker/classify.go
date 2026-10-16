package poker

import (
	"errors"
	"fmt"
)

// Category is a poker hand category. Values ascend with strength, so
// comparing categories numerically gives the hierarchy from the rules:
// High Card lowest, Royal Flush highest and separate from Straight Flush.
type Category uint8

const (
	HighCard Category = iota
	OnePair
	TwoPair
	ThreeOfAKind
	Straight
	Flush
	FullHouse
	FourOfAKind
	StraightFlush
	RoyalFlush

	numCategories = 10
)

var categoryName = [numCategories]string{
	"HIGH_CARD", "PAIR", "TWO_PAIR", "THREE_OF_A_KIND", "STRAIGHT",
	"FLUSH", "FULL_HOUSE", "FOUR_OF_A_KIND", "STRAIGHT_FLUSH", "ROYAL_FLUSH",
}

var categoryTitle = [numCategories]string{
	"High Card", "Pair", "Two Pair", "Three of a Kind", "Straight",
	"Flush", "Full House", "Four of a Kind", "Straight Flush", "Royal Flush",
}

func (c Category) Valid() bool { return c < numCategories }
func (c Category) String() string {
	if !c.Valid() {
		return "INVALID"
	}
	return categoryName[c]
}
func (c Category) Title() string {
	if !c.Valid() {
		return "Invalid"
	}
	return categoryTitle[c]
}

func ParseCategory(s string) (Category, error) {
	for i, n := range categoryName {
		if n == s {
			return Category(i), nil
		}
	}
	return 0, fmt.Errorf("poker: unknown category %q", s)
}

// Hand size bounds. FullHandSize is the normal evaluation width. Hands
// smaller than that occur only in reduced-pool mode, where the entire
// distributed pool is evaluated as-is.
const (
	MinHandSize  = 1
	FullHandSize = 5
)

var (
	ErrHandSize      = errors.New("poker: hand must hold 1 to 5 cards")
	ErrDuplicateCard = errors.New("poker: duplicate card in hand")
	ErrInvalidCard   = errors.New("poker: invalid card")
)

// straightHigh returns the high card of the straight formed by ranks, or 0.
// A straight needs exactly five distinct ranks, so this always returns 0 for
// a hand of fewer than five cards. Ace is high in 10-J-Q-K-A and low in
// A-2-3-4-5, where the high card is 5. Ranks never wrap: K-A-2-3-4 is not a
// straight.
func straightHigh(ranks []Rank) Rank {
	if len(ranks) != FullHandSize {
		return 0
	}
	seen := map[Rank]bool{}
	for _, r := range ranks {
		if seen[r] {
			return 0
		}
		seen[r] = true
	}
	sorted := make([]Rank, 0, FullHandSize)
	for r := MinRank; r <= MaxRank; r++ {
		if seen[r] {
			sorted = append(sorted, r)
		}
	}
	if sorted[4]-sorted[0] == 4 {
		return sorted[4]
	}
	// The wheel: A 2 3 4 5, high card 5.
	if sorted[0] == Two && sorted[1] == Three && sorted[2] == Four &&
		sorted[3] == Five && sorted[4] == Ace {
		return Five
	}
	return 0
}

// Classify returns the single claim that the given hand exactly realizes.
//
// This is the heart of the engine's "exact claim" semantics. Because
// Classify returns a Claim, and Claim is a comparable struct, the question
// "does this hand satisfy this claim?" reduces to one equality:
//
//	v, err := Classify(hand); err == nil && v == claim
//
// There is no per-category matching logic anywhere else in the codebase, so
// the rule that a stronger available hand never validates a weaker claim
// holds by construction rather than by a special case: a hand has exactly
// one classification.
//
// Parameters a claim does not declare are deliberately absent from the
// returned Claim, which is what makes a Four of a Kind kicker, a straight's
// suits, a flush's four low cards and a pair's kickers irrelevant.
//
// Hands of one to four cards are classified with the same hierarchy. Flush,
// Straight, Full House, Straight Flush and Royal Flush all require five
// cards by definition and so are simply unreachable below that width; no
// separate short-hand hierarchy exists.
func Classify(hand []Card) (Claim, error) {
	n := len(hand)
	if n < MinHandSize || n > FullHandSize {
		return Claim{}, ErrHandSize
	}
	counts := map[Rank]int{}
	ranks := make([]Rank, 0, n)
	maxRank := Rank(0)
	sameSuit := true
	for i, c := range hand {
		if !c.Valid() {
			return Claim{}, ErrInvalidCard
		}
		counts[c.Rank]++
		ranks = append(ranks, c.Rank)
		if c.Rank > maxRank {
			maxRank = c.Rank
		}
		if i > 0 && c.Suit != hand[0].Suit {
			sameSuit = false
		}
	}
	if HasDuplicates(hand) {
		return Claim{}, ErrDuplicateCard
	}

	// Categories that require all five cards.
	if n == FullHandSize {
		suit := hand[0].Suit
		sh := straightHigh(ranks)
		switch {
		case sameSuit && sh == Ace:
			return NewRoyalFlush(suit), nil
		case sameSuit && sh != 0:
			return NewStraightFlush(suit, sh), nil
		}
		if trips, pair, ok := fullHouseRanks(counts); ok {
			return NewFullHouse(trips, pair), nil
		}
		if sameSuit {
			return NewFlush(suit, maxRank), nil
		}
		if sh != 0 {
			return NewStraight(sh), nil
		}
	}

	// Shape-based categories, valid at every hand width.
	var quad, trip Rank
	pairs := make([]Rank, 0, 2)
	for r := MaxRank; r >= MinRank; r-- {
		switch counts[r] {
		case 4:
			quad = r
		case 3:
			trip = r
		case 2:
			pairs = append(pairs, r) // descending, because r descends
		}
	}
	switch {
	case quad != 0:
		return NewQuads(quad), nil
	case trip != 0:
		return NewTrips(trip), nil
	case len(pairs) == 2:
		return NewTwoPair(pairs[0], pairs[1]), nil
	case len(pairs) == 1:
		return NewPair(pairs[0]), nil
	}
	return NewHighCard(maxRank), nil
}

// fullHouseRanks reports the trips and pair ranks of a 3+2 shape.
func fullHouseRanks(counts map[Rank]int) (trips, pair Rank, ok bool) {
	for r, n := range counts {
		switch n {
		case 3:
			trips = r
		case 2:
			pair = r
		}
	}
	return trips, pair, trips != 0 && pair != 0
}

// MustClassify is Classify for fixtures and tests.
func MustClassify(hand []Card) Claim {
	c, err := Classify(hand)
	if err != nil {
		panic(err)
	}
	return c
}

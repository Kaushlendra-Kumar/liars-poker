package poker

import (
	"encoding/json"
	"fmt"
)

// Claim is a structured poker claim. It is deliberately the same shape as
// the value returned by Classify, so that claim satisfaction is a struct
// equality rather than a pile of per-category matching code.
//
// Only the fields a category actually declares are populated; every other
// field is zero. Constructors enforce that, so two claims are equal exactly
// when they are the same claim.
//
//	Category        Primary          Secondary   Suit
//	HighCard        high card        -           -
//	OnePair         pair rank        -           -
//	TwoPair         high pair rank   low pair    -
//	ThreeOfAKind    trips rank       -           -
//	Straight        straight high    -           -
//	Flush           flush high       -           suit
//	FullHouse       trips rank       pair rank   -
//	FourOfAKind     quads rank       -           -
//	StraightFlush   straight high    -           suit
//	RoyalFlush      -                -           suit
//
// A Four of a Kind kicker, a straight's suits, a flush's four low cards and
// a pair's kickers are not modelled, because the rules say they do not form
// part of the claim.
type Claim struct {
	Category  Category
	Primary   Rank
	Secondary Rank
	Suit      Suit
	HasSuit   bool
}

// ------------------------------------------------------- constructors

func NewHighCard(high Rank) Claim { return Claim{Category: HighCard, Primary: high} }
func NewPair(rank Rank) Claim     { return Claim{Category: OnePair, Primary: rank} }
func NewTrips(rank Rank) Claim    { return Claim{Category: ThreeOfAKind, Primary: rank} }
func NewStraight(high Rank) Claim { return Claim{Category: Straight, Primary: high} }
func NewQuads(rank Rank) Claim    { return Claim{Category: FourOfAKind, Primary: rank} }

// NewTwoPair canonicalises so the higher pair is always Primary. "Two Pair
// 2s and 4s" and "Two Pair 4s and 2s" are therefore the same claim, and
// neither is a legal raise over the other.
func NewTwoPair(a, b Rank) Claim {
	if b > a {
		a, b = b, a
	}
	return Claim{Category: TwoPair, Primary: a, Secondary: b}
}

func NewFullHouse(trips, pair Rank) Claim {
	return Claim{Category: FullHouse, Primary: trips, Secondary: pair}
}

func NewFlush(suit Suit, high Rank) Claim {
	return Claim{Category: Flush, Primary: high, Suit: suit, HasSuit: true}
}

func NewStraightFlush(suit Suit, high Rank) Claim {
	return Claim{Category: StraightFlush, Primary: high, Suit: suit, HasSuit: true}
}

func NewRoyalFlush(suit Suit) Claim {
	return Claim{Category: RoyalFlush, Suit: suit, HasSuit: true}
}

// ------------------------------------------------------- the claim space

// The claim space is the set of claims that some hand of one to five cards
// can actually realize. Membership is what "well-formed claim" means, so
// Pair of 1s, 8s full of 8s, Straight to 4, a 6-high Flush and a Straight
// Flush to Ace (which is a Royal Flush) are all rejected structurally,
// before any card is consulted.
//
// The space is enumerated in ascending order once, at init, and an ordinal
// lookup gives a strict total order. Claim comparison is integer comparison;
// no string comparison is used anywhere.
//
// Note the High Card band runs from 3, not 7. With five cards a high-card
// hand needs five distinct non-consecutive ranks, so the lowest realizable
// is 7 (2-3-4-5-7). In reduced-pool mode fewer cards are evaluated, and two
// cards can realize High Card 3 (2 and 3). Excluding those four claims
// would leave reduced-pool rounds in which no legal claim is true, which
// would recreate exactly the forced-loss problem the reduced-pool rule was
// introduced to remove. See ClaimSpaceNote.
var (
	allClaims    []Claim
	claimOrdinal map[Claim]int
)

// ClaimSpaceSize is the number of legal claims. Verified exhaustively
// against every hand of two to five cards drawn from a 52-card deck.
const ClaimSpaceSize = 367

func init() {
	allClaims = make([]Claim, 0, ClaimSpaceSize)
	claimOrdinal = make(map[Claim]int, ClaimSpaceSize)
	add := func(c Claim) {
		if _, dup := claimOrdinal[c]; dup {
			panic(fmt.Sprintf("poker: duplicate claim in space: %v", c))
		}
		claimOrdinal[c] = len(allClaims)
		allClaims = append(allClaims, c)
	}

	// High Card: 3..A (12). Below 3 needs a single-card hand, which cannot
	// occur while two or more players are active.
	for h := Three; h <= Ace; h++ {
		add(NewHighCard(h))
	}
	// Pair: 2..A (13).
	for r := MinRank; r <= MaxRank; r++ {
		add(NewPair(r))
	}
	// Two Pair: ordered by (high, low) (78).
	for hi := Three; hi <= Ace; hi++ {
		for lo := MinRank; lo < hi; lo++ {
			add(NewTwoPair(hi, lo))
		}
	}
	// Three of a Kind: 2..A (13).
	for r := MinRank; r <= MaxRank; r++ {
		add(NewTrips(r))
	}
	// Straight: 5..A (10). 5 is the ace-low wheel; ranks do not wrap.
	for h := Five; h <= Ace; h++ {
		add(NewStraight(h))
	}
	// Flush: ordered by (high, suit), high 7..A (32). A 6-high flush would
	// have to be 2-3-4-5-6 suited, which is a straight flush.
	for h := Seven; h <= Ace; h++ {
		for s := Clubs; s <= Spades; s++ {
			add(NewFlush(s, h))
		}
	}
	// Full House: ordered by (trips, pair), trips != pair (156).
	for t := MinRank; t <= MaxRank; t++ {
		for p := MinRank; p <= MaxRank; p++ {
			if p != t {
				add(NewFullHouse(t, p))
			}
		}
	}
	// Four of a Kind: 2..A (13). Kicker is not part of the claim.
	for r := MinRank; r <= MaxRank; r++ {
		add(NewQuads(r))
	}
	// Straight Flush: ordered by (high, suit), high 5..K (36). Ace-high
	// suited is the Royal Flush of that suit, a separate category.
	for h := Five; h <= King; h++ {
		for s := Clubs; s <= Spades; s++ {
			add(NewStraightFlush(s, h))
		}
	}
	// Royal Flush: ordered by suit (4).
	for s := Clubs; s <= Spades; s++ {
		add(NewRoyalFlush(s))
	}

	if len(allClaims) != ClaimSpaceSize {
		panic(fmt.Sprintf("poker: claim space is %d, expected %d", len(allClaims), ClaimSpaceSize))
	}
}

// AllClaims returns the claim space in ascending order. The returned slice
// is a copy.
func AllClaims() []Claim {
	out := make([]Claim, len(allClaims))
	copy(out, allClaims)
	return out
}

// MinClaim and MaxClaim bound the space. MaxClaim matters to the engine: a
// player facing it has no legal raise, so SHOW becomes their only action.
func MinClaim() Claim { return allClaims[0] }
func MaxClaim() Claim { return allClaims[len(allClaims)-1] }

// Ordinal returns the claim's position in the total order. ok is false for a
// claim outside the space, which is how malformed claims are detected.
func (c Claim) Ordinal() (int, bool) {
	o, ok := claimOrdinal[c]
	return o, ok
}

// Legal reports whether the claim is well-formed, meaning some hand could in
// principle realize it. It says nothing about the cards actually in play:
// impossible-given-the-deal claims are legal, because bluffing is the game.
func (c Claim) Legal() bool {
	_, ok := claimOrdinal[c]
	return ok
}

// Validate returns a descriptive error for a malformed claim.
func (c Claim) Validate() error {
	if !c.Category.Valid() {
		return fmt.Errorf("poker: invalid category %d", c.Category)
	}
	if c.Legal() {
		return nil
	}
	return fmt.Errorf("poker: %s is not a realizable claim", c.debugString())
}

// Compare orders two claims: -1 if a is lower, 0 if equal, +1 if higher. It
// panics on a malformed claim, so callers validate first; the engine's
// action validation does exactly that.
func Compare(a, b Claim) int {
	ao, ok := a.Ordinal()
	if !ok {
		panic(fmt.Sprintf("poker: Compare on malformed claim %s", a.debugString()))
	}
	bo, ok := b.Ordinal()
	if !ok {
		panic(fmt.Sprintf("poker: Compare on malformed claim %s", b.debugString()))
	}
	switch {
	case ao < bo:
		return -1
	case ao > bo:
		return 1
	}
	return 0
}

// Higher reports whether a is strictly higher than b. Equal claims are not
// higher, which is what makes "no equal claim" enforceable.
func Higher(a, b Claim) bool { return Compare(a, b) > 0 }

// ClaimsAbove returns every legal claim strictly higher than c, ascending.
// The engine sends this range to clients so the claim builder can only offer
// legal raises, without the client holding any rule knowledge.
func ClaimsAbove(c Claim) []Claim {
	o, ok := c.Ordinal()
	if !ok {
		return nil
	}
	out := make([]Claim, 0, len(allClaims)-o-1)
	out = append(out, allClaims[o+1:]...)
	return out
}

// ------------------------------------------------------- formatting

// String renders the claim the way a player says it.
func (c Claim) String() string {
	switch c.Category {
	case HighCard:
		return fmt.Sprintf("%s high", c.Primary.Word())
	case OnePair:
		return fmt.Sprintf("Pair of %s", c.Primary.Plural())
	case TwoPair:
		return fmt.Sprintf("Two Pair \u2014 %s and %s", c.Primary.Plural(), c.Secondary.Plural())
	case ThreeOfAKind:
		return fmt.Sprintf("Three %s", c.Primary.Plural())
	case Straight:
		return fmt.Sprintf("Straight to %s", c.Primary.String())
	case Flush:
		return fmt.Sprintf("%s-high %s Flush", c.Primary.Word(), c.Suit.Title())
	case FullHouse:
		return fmt.Sprintf("%s full of %s", c.Primary.Plural(), c.Secondary.Plural())
	case FourOfAKind:
		return fmt.Sprintf("Four %s", c.Primary.Plural())
	case StraightFlush:
		return fmt.Sprintf("Straight Flush to %s of %s", c.Primary.Word(), c.Suit.Title())
	case RoyalFlush:
		return fmt.Sprintf("Royal Flush of %s", c.Suit.Title())
	}
	return c.debugString()
}

func (c Claim) debugString() string {
	return fmt.Sprintf("Claim{cat:%d primary:%d secondary:%d suit:%d hasSuit:%v}",
		c.Category, c.Primary, c.Secondary, c.Suit, c.HasSuit)
}

// ------------------------------------------------------- wire form

type claimJSON struct {
	Category  string `json:"category"`
	Primary   string `json:"primary,omitempty"`
	Secondary string `json:"secondary,omitempty"`
	Suit      string `json:"suit,omitempty"`
}

// MarshalJSON emits a tagged object. Field meaning is per-category and
// documented on Claim.
func (c Claim) MarshalJSON() ([]byte, error) {
	if !c.Category.Valid() {
		return nil, fmt.Errorf("poker: cannot marshal invalid category %d", c.Category)
	}
	out := claimJSON{Category: c.Category.String()}
	if c.Primary != 0 {
		out.Primary = c.Primary.String()
	}
	if c.Secondary != 0 {
		out.Secondary = c.Secondary.String()
	}
	if c.HasSuit {
		out.Suit = c.Suit.Name()
	}
	return json.Marshal(out)
}

// UnmarshalJSON rebuilds a claim through its constructor, so anything
// arriving from the wire is canonicalised and then checked against the claim
// space by the caller.
func (c *Claim) UnmarshalJSON(b []byte) error {
	var in claimJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	cat, err := ParseCategory(in.Category)
	if err != nil {
		return err
	}
	var primary, secondary Rank
	var suit Suit
	if in.Primary != "" {
		if primary, err = ParseRank(in.Primary); err != nil {
			return err
		}
	}
	if in.Secondary != "" {
		if secondary, err = ParseRank(in.Secondary); err != nil {
			return err
		}
	}
	if in.Suit != "" {
		if suit, err = ParseSuit(in.Suit); err != nil {
			return err
		}
	}
	switch cat {
	case HighCard:
		*c = NewHighCard(primary)
	case OnePair:
		*c = NewPair(primary)
	case TwoPair:
		*c = NewTwoPair(primary, secondary)
	case ThreeOfAKind:
		*c = NewTrips(primary)
	case Straight:
		*c = NewStraight(primary)
	case Flush:
		*c = NewFlush(suit, primary)
	case FullHouse:
		*c = NewFullHouse(primary, secondary)
	case FourOfAKind:
		*c = NewQuads(primary)
	case StraightFlush:
		*c = NewStraightFlush(suit, primary)
	case RoyalFlush:
		*c = NewRoyalFlush(suit)
	default:
		return fmt.Errorf("poker: unhandled category %s", cat)
	}
	return nil
}

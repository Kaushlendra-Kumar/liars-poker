package poker

import (
	"encoding/json"
	"math/rand"
	"testing"
)

// ---------------------------------------------------------------- deck

func TestDeckHas52UniqueCards(t *testing.T) {
	d := NewDeck()
	if len(d) != DeckSize {
		t.Fatalf("deck has %d cards, want %d", len(d), DeckSize)
	}
	if HasDuplicates(d) {
		t.Fatal("deck contains a duplicate card")
	}
	for _, c := range d {
		if !c.Valid() {
			t.Fatalf("invalid card %v", c)
		}
	}
}

func TestShuffleIsPermutationAndSeedDeterministic(t *testing.T) {
	a := NewShuffledDeck(rand.New(rand.NewSource(42)))
	b := NewShuffledDeck(rand.New(rand.NewSource(42)))
	c := NewShuffledDeck(rand.New(rand.NewSource(43)))

	ha, _ := a.Deal(DeckSize)
	hb, _ := b.Deal(DeckSize)
	hc, _ := c.Deal(DeckSize)

	for i := range ha {
		if ha[i] != hb[i] {
			t.Fatalf("same seed produced different order at %d", i)
		}
	}
	same := true
	for i := range ha {
		if ha[i] != hc[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different seeds produced identical order")
	}
	if HasDuplicates(ha) || len(ha) != DeckSize {
		t.Fatal("shuffle is not a permutation")
	}
}

func TestDealAndExhaustion(t *testing.T) {
	d := NewShuffledDeck(rand.New(rand.NewSource(1)))
	// Worst realistic case: 9 players at 5 cards each.
	got, err := d.Deal(45)
	if err != nil {
		t.Fatalf("dealing 45 cards failed: %v", err)
	}
	if len(got) != 45 || HasDuplicates(got) {
		t.Fatal("bad 45-card deal")
	}
	if d.Remaining() != 7 {
		t.Fatalf("remaining %d, want 7", d.Remaining())
	}
	if _, err := d.Deal(8); err != ErrDeckExhausted {
		t.Fatalf("over-deal error = %v, want ErrDeckExhausted", err)
	}
}

func TestCanonicalCardOrder(t *testing.T) {
	cards := MustParseCards("As 2c 2s 2h 10d Kc")
	SortCards(cards)
	want := "2\u2663 2\u2665 2\u2660 10\u2666 K\u2663 A\u2660"
	if got := CardsString(cards); got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------- classify

// TestClassifyExhaustive is the strongest single guarantee for the
// classifier: every five-card hand in the deck is classified, and the
// resulting category histogram must match the known poker distribution. If
// the ten cases were not exhaustive and mutually exclusive, these counts
// could not come out right.
func TestClassifyExhaustive(t *testing.T) {
	want := map[Category]int{
		HighCard: 1302540, OnePair: 1098240, TwoPair: 123552,
		ThreeOfAKind: 54912, Straight: 10200, Flush: 5108,
		FullHouse: 3744, FourOfAKind: 624, StraightFlush: 36, RoyalFlush: 4,
	}
	got := map[Category]int{}
	total := 0
	forEachCombination(NewDeck(), 5, func(hand []Card) bool {
		v, err := Classify(hand)
		if err != nil {
			t.Fatalf("Classify(%s) error: %v", CardsString(hand), err)
		}
		if !v.Legal() {
			t.Fatalf("Classify(%s) = %s, which is outside the claim space",
				CardsString(hand), v.debugString())
		}
		got[v.Category]++
		total++
		return false
	})
	if total != 2598960 {
		t.Fatalf("enumerated %d hands, want 2598960", total)
	}
	for cat, n := range want {
		if got[cat] != n {
			t.Errorf("%s = %d, want %d", cat, got[cat], n)
		}
	}
}

// TestClassifyReducedWidthsAreLegalClaims enumerates every two, three and
// four card hand. Each must classify into the shared hierarchy and each
// classification must be a legal claim, which is what guarantees a
// reduced-pool round always has at least one true claim available.
func TestClassifyReducedWidthsAreLegalClaims(t *testing.T) {
	deck := NewDeck()
	for _, k := range []int{2, 3, 4} {
		seen := map[Category]bool{}
		forEachCombination(deck, k, func(hand []Card) bool {
			v, err := Classify(hand)
			if err != nil {
				t.Fatalf("Classify(%s): %v", CardsString(hand), err)
			}
			if !v.Legal() {
				t.Fatalf("width %d: Classify(%s) = %s is not a legal claim",
					k, CardsString(hand), v)
			}
			seen[v.Category] = true
			return false
		})
		// Categories needing five cards must be unreachable below five.
		for _, c := range []Category{Straight, Flush, FullHouse, StraightFlush, RoyalFlush} {
			if seen[c] {
				t.Errorf("width %d produced %s, which requires five cards", k, c)
			}
		}
		if !seen[HighCard] || !seen[OnePair] {
			t.Errorf("width %d never produced High Card or Pair", k)
		}
	}
}

func TestClassifyTable(t *testing.T) {
	cases := []struct {
		hand string
		want Claim
	}{
		// Five-card categories.
		{"10s Js Qs Ks As", NewRoyalFlush(Spades)},
		{"5h 6h 7h 8h 9h", NewStraightFlush(Hearts, Nine)},
		{"Ad 2d 3d 4d 5d", NewStraightFlush(Diamonds, Five)},
		{"8s 8h 8d 8c Kc", NewQuads(Eight)},
		{"8s 8h 8d 4c 4h", NewFullHouse(Eight, Four)},
		{"Ah Qh Jh 7h 3h", NewFlush(Hearts, Ace)},
		{"Qh Jh 7h 5h 3h", NewFlush(Hearts, Queen)},
		{"5s 6h 7d 8c 9s", NewStraight(Nine)},
		{"As 2h 3d 4c 5s", NewStraight(Five)},
		{"10s Jh Qd Kc As", NewStraight(Ace)},
		{"8s 8h 8d Kc 3c", NewTrips(Eight)},
		{"8s 8h 4d 4c Ac", NewTwoPair(Eight, Four)},
		{"8s 8h Ad Kc 3c", NewPair(Eight)},
		{"Ks 8h 5d 3c 2s", NewHighCard(King)},
		{"2s 3h 4d 5c 7s", NewHighCard(Seven)},
		// Ranks never wrap: K A 2 3 4 is not a straight.
		{"Ks As 2h 3d 4c", NewHighCard(Ace)},
		// Reduced widths, same hierarchy.
		{"8s 8h Kd 3c", NewPair(Eight)},
		{"8s 8h 8d Kc", NewTrips(Eight)},
		{"8s 8h 4d 4c", NewTwoPair(Eight, Four)},
		{"8s 8h 8d 8c", NewQuads(Eight)},
		{"Ks 8h 5d 3c", NewHighCard(King)},
		{"2s 3h 4d 5c", NewHighCard(Five)},
		{"Ks 8h 5d", NewHighCard(King)},
		{"8s 8h 8d", NewTrips(Eight)},
		{"8s 8h", NewPair(Eight)},
		{"2s 3h", NewHighCard(Three)},
		// Four suited cards are not a flush; five are needed.
		{"Qh Jh 7h 3h", NewHighCard(Queen)},
		// Four consecutive cards are not a straight.
		{"6s 7h 8d 9c", NewHighCard(Nine)},
	}
	for _, tc := range cases {
		got, err := Classify(MustParseCards(tc.hand))
		if err != nil {
			t.Errorf("Classify(%s): %v", tc.hand, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Classify(%s) = %s, want %s", tc.hand, got, tc.want)
		}
	}
}

func TestClassifyRejectsBadHands(t *testing.T) {
	if _, err := Classify(nil); err != ErrHandSize {
		t.Errorf("empty hand error = %v, want ErrHandSize", err)
	}
	if _, err := Classify(MustParseCards("2s 3s 4s 5s 6s 7s")); err != ErrHandSize {
		t.Errorf("six-card hand error = %v, want ErrHandSize", err)
	}
	if _, err := Classify([]Card{{Eight, Spades}, {Eight, Spades}}); err != ErrDuplicateCard {
		t.Errorf("duplicate error = %v, want ErrDuplicateCard", err)
	}
}

// ---------------------------------------------------------------- claims

func TestClaimSpaceSizeAndBijection(t *testing.T) {
	all := AllClaims()
	if len(all) != ClaimSpaceSize {
		t.Fatalf("claim space = %d, want %d", len(all), ClaimSpaceSize)
	}
	for i, c := range all {
		o, ok := c.Ordinal()
		if !ok {
			t.Fatalf("%s has no ordinal", c)
		}
		if o != i {
			t.Fatalf("%s ordinal = %d, want %d", c, o, i)
		}
	}
	counts := map[Category]int{}
	for _, c := range all {
		counts[c.Category]++
	}
	want := map[Category]int{
		HighCard: 12, OnePair: 13, TwoPair: 78, ThreeOfAKind: 13, Straight: 10,
		Flush: 32, FullHouse: 156, FourOfAKind: 13, StraightFlush: 36, RoyalFlush: 4,
	}
	for cat, n := range want {
		if counts[cat] != n {
			t.Errorf("%s band = %d, want %d", cat, counts[cat], n)
		}
	}
}

func TestClaimTotalOrder(t *testing.T) {
	all := AllClaims()
	// Strictly ascending, antisymmetric, and category-dominant.
	for i := 1; i < len(all); i++ {
		if !Higher(all[i], all[i-1]) {
			t.Fatalf("%s is not higher than %s", all[i], all[i-1])
		}
		if Higher(all[i-1], all[i]) {
			t.Fatalf("ordering is not antisymmetric at %d", i)
		}
		if Compare(all[i], all[i]) != 0 {
			t.Fatalf("%s is not equal to itself", all[i])
		}
	}
	for _, a := range all {
		for _, b := range all {
			if a.Category < b.Category && !Higher(b, a) {
				t.Fatalf("%s (cat %s) should outrank %s (cat %s)", b, b.Category, a, a.Category)
			}
		}
	}
	if MinClaim() != NewHighCard(Three) {
		t.Errorf("min claim = %s, want High Card 3", MinClaim())
	}
	if MaxClaim() != NewRoyalFlush(Spades) {
		t.Errorf("max claim = %s, want Royal Flush of Spades", MaxClaim())
	}
	if n := len(ClaimsAbove(MaxClaim())); n != 0 {
		t.Errorf("%d claims above the maximum, want 0", n)
	}
}

func TestWithinCategoryOrdering(t *testing.T) {
	asc := []Claim{
		NewPair(Three), NewPair(Four), NewPair(Ace),
	}
	for i := 1; i < len(asc); i++ {
		if !Higher(asc[i], asc[i-1]) {
			t.Errorf("%s should beat %s", asc[i], asc[i-1])
		}
	}
	// Two Pair orders by (high, low).
	seq := []Claim{
		NewTwoPair(Four, Two), NewTwoPair(Four, Three),
		NewTwoPair(Five, Two), NewTwoPair(Ace, King),
	}
	for i := 1; i < len(seq); i++ {
		if !Higher(seq[i], seq[i-1]) {
			t.Errorf("%s should beat %s", seq[i], seq[i-1])
		}
	}
	// Full House orders by (trips, pair).
	fh := []Claim{
		NewFullHouse(Eight, Two), NewFullHouse(Eight, King),
		NewFullHouse(Nine, Two),
	}
	for i := 1; i < len(fh); i++ {
		if !Higher(fh[i], fh[i-1]) {
			t.Errorf("%s should beat %s", fh[i], fh[i-1])
		}
	}
	// Flush orders by (high, suit).
	fl := []Claim{
		NewFlush(Clubs, Queen), NewFlush(Diamonds, Queen),
		NewFlush(Hearts, Queen), NewFlush(Spades, Queen),
		NewFlush(Clubs, King),
	}
	for i := 1; i < len(fl); i++ {
		if !Higher(fl[i], fl[i-1]) {
			t.Errorf("%s should beat %s", fl[i], fl[i-1])
		}
	}
	// Straight to 5 is the wheel and the lowest straight.
	if !Higher(NewStraight(Six), NewStraight(Five)) {
		t.Error("Straight to 6 should beat Straight to 5")
	}
	// Royal flushes are separated by suit, so one strict maximum exists.
	if !Higher(NewRoyalFlush(Spades), NewRoyalFlush(Hearts)) {
		t.Error("Royal Flush of Spades should beat Royal Flush of Hearts")
	}
	if !Higher(NewRoyalFlush(Clubs), NewStraightFlush(Spades, King)) {
		t.Error("any Royal Flush should beat any Straight Flush")
	}
}

func TestTwoPairCanonicalisation(t *testing.T) {
	a := NewTwoPair(Two, Four)
	b := NewTwoPair(Four, Two)
	if a != b {
		t.Fatalf("%s and %s should be the same claim", a, b)
	}
	if Higher(a, b) || Higher(b, a) {
		t.Fatal("canonically equal claims must not raise each other")
	}
	if got := a.String(); got != "Two Pair \u2014 4s and 2s" {
		t.Errorf("String() = %q", got)
	}
}

func TestMalformedClaimsRejected(t *testing.T) {
	bad := []Claim{
		{Category: OnePair, Primary: 1},  // no rank 1
		{Category: OnePair, Primary: 15}, // above Ace
		NewFullHouse(Eight, Eight),       // trips == pair
		{Category: TwoPair, Primary: Eight, Secondary: Eight},
		NewStraight(Four), // no straight below 5
		NewStraight(Two),
		NewFlush(Hearts, Six), // 2-3-4-5-6 suited is a straight flush
		NewFlush(Hearts, Two),
		NewHighCard(Two),                                    // needs a one-card hand
		NewStraightFlush(Spades, Ace),                       // that is the Royal Flush of Spades
		{Category: HighCard, Primary: Seven, HasSuit: true}, // suit not part of the claim
		{Category: Category(99)},
	}
	for _, c := range bad {
		if c.Legal() {
			t.Errorf("%s should be malformed", c.debugString())
		}
		if err := c.Validate(); err == nil {
			t.Errorf("Validate(%s) returned nil", c.debugString())
		}
	}
	// And the boundary cases that are legal.
	for _, c := range []Claim{
		NewHighCard(Three), NewHighCard(Seven), NewFlush(Hearts, Seven),
		NewStraight(Five), NewStraightFlush(Spades, King), NewRoyalFlush(Spades),
	} {
		if !c.Legal() {
			t.Errorf("%s should be legal", c)
		}
	}
}

func TestClaimJSONRoundTrip(t *testing.T) {
	for _, c := range AllClaims() {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal %s: %v", c, err)
		}
		var back Claim
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("unmarshal %s (%s): %v", c, b, err)
		}
		if back != c {
			t.Fatalf("round trip %s -> %s -> %s", c, b, back)
		}
	}
	var junk Claim
	if err := json.Unmarshal([]byte(`{"category":"NOPE"}`), &junk); err == nil {
		t.Error("unknown category should fail to unmarshal")
	}
}

func TestClaimStrings(t *testing.T) {
	cases := map[string]Claim{
		"King high":                          NewHighCard(King),
		"Pair of 8s":                         NewPair(Eight),
		"Two Pair \u2014 8s and 4s":          NewTwoPair(Eight, Four),
		"Three 7s":                           NewTrips(Seven),
		"Straight to 9":                      NewStraight(Nine),
		"Queen-high Hearts Flush":            NewFlush(Hearts, Queen),
		"8s full of 4s":                      NewFullHouse(Eight, Four),
		"Four 8s":                            NewQuads(Eight),
		"Straight Flush to Jack of Diamonds": NewStraightFlush(Diamonds, Jack),
		"Royal Flush of Spades":              NewRoyalFlush(Spades),
	}
	for want, c := range cases {
		if got := c.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

// ---------------------------------------------------------------- validation

// scenario is a hand-built pool with explicit ownership.
type scenario struct {
	pool     []Card
	owner    map[Card]string
	claimant string
}

func newScenario(hands map[string]string) scenario {
	s := scenario{owner: map[Card]string{}}
	for player, cards := range hands {
		for _, c := range MustParseCards(cards) {
			s.pool = append(s.pool, c)
			s.owner[c] = player
		}
	}
	SortCards(s.pool)
	return s
}

func (s scenario) owns(who string) Owns {
	return func(c Card) bool { return s.owner[c] == who }
}

func (s scenario) check(t *testing.T, who string, c Claim) Result {
	t.Helper()
	got := CanSatisfy(s.pool, s.owns(who), c)
	ref := CanSatisfyExhaustive(s.pool, s.owns(who), c)
	if got.Satisfied != ref.Satisfied {
		t.Fatalf("constructive=%v exhaustive=%v for %s over [%s]",
			got.Satisfied, ref.Satisfied, c, CardsString(s.pool))
	}
	if got.Satisfied {
		if len(got.Witness) != got.HandSize {
			t.Fatalf("witness width %d, want %d", len(got.Witness), got.HandSize)
		}
		if !Satisfies(got.Witness, c) {
			t.Fatalf("witness [%s] does not realize %s", CardsString(got.Witness), c)
		}
		if !OwnershipSatisfied(got.Witness, s.owns(who)) {
			t.Fatalf("witness [%s] contains no card owned by %s",
				CardsString(got.Witness), who)
		}
		for _, w := range got.Witness {
			if _, ok := s.owner[w]; !ok {
				t.Fatalf("witness card %s is not in the pool", w)
			}
		}
	}
	return got
}

// The five regression tests required for the reduced-pool rule.

func TestReducedPoolFourCardPair(t *testing.T) {
	s := newScenario(map[string]string{"A": "8s", "B": "8h", "C": "Kd", "D": "3c"})
	if r := s.check(t, "A", NewPair(Eight)); !r.Satisfied {
		t.Fatalf("A's Pair of 8s should be TRUE: %s", r)
	}
	if r := s.check(t, "B", NewPair(Eight)); !r.Satisfied {
		t.Fatalf("B's Pair of 8s should be TRUE: %s", r)
	}
	// C and D hold no 8, but ownership asks only whether they own a card of
	// the evaluated hand, and in reduced mode the evaluated hand is the
	// whole pool. So the claim is true for every in-play player.
	if r := s.check(t, "C", NewPair(Eight)); !r.Satisfied {
		t.Fatalf("C's Pair of 8s should be TRUE (C owns K-d in the pool): %s", r)
	}
	if r := s.check(t, "D", NewPair(Eight)); !r.Satisfied {
		t.Fatalf("D's Pair of 8s should be TRUE (D owns 3-c in the pool): %s", r)
	}
	if r := s.check(t, "A", NewPair(Eight)); r.Mode != ModeReducedPool {
		t.Fatalf("mode = %s, want REDUCED_POOL", r.Mode)
	}
}

// TestOwnershipIsAnySelectedCardEverywhere pins the single ownership rule:
// the claimant must own a card of the evaluated hand, and it need not be a
// card of the claimed pattern. Identical below and above five cards.
func TestOwnershipIsAnySelectedCardEverywhere(t *testing.T) {
	// Reduced pool. A owns only the king, the pair of 8s belongs to B and C,
	// and the claim is TRUE because the king is part of the evaluated hand.
	s := newScenario(map[string]string{"A": "Ks", "B": "8h", "C": "8d", "D": "3c"})
	r := s.check(t, "A", NewPair(Eight))
	if !r.Satisfied {
		t.Fatalf("A owns K-s in the evaluated pool, so Pair of 8s is TRUE: %s", r)
	}
	if !Satisfies(s.pool, NewPair(Eight)) {
		t.Fatal("pool should classify as Pair of 8s")
	}

	// Exactly five cards, same rule. A owns only the kicker; the four 8s
	// belong entirely to other players.
	q := newScenario(map[string]string{"A": "Ks", "B": "8h", "C": "8d", "D": "8s", "E": "8c"})
	if got := q.check(t, "A", NewQuads(Eight)); !got.Satisfied {
		t.Fatalf("A owns the kicker in the selected hand, so Four 8s is TRUE: %s", got)
	}
	if got := q.check(t, "B", NewQuads(Eight)); !got.Satisfied {
		t.Fatalf("Four 8s should also be TRUE for a pattern owner: %s", got)
	}

	// Ownership still bites at five or more cards, because there the
	// evaluated hand is a proper subset that can exclude the claimant.
	f := newScenario(map[string]string{
		"A": "2c", "B": "7s", "C": "7h", "D": "7d", "E": "8s", "F": "8h"})
	if got := f.check(t, "A", NewFullHouse(Seven, Eight)); got.Satisfied {
		t.Fatalf("the only full house excludes A's 2-c, so this is FALSE: %s", got)
	}
}

func TestReducedPoolFourCardHighCard(t *testing.T) {
	s := newScenario(map[string]string{"A": "Ks", "B": "8h", "C": "5d", "D": "3c"})
	if r := s.check(t, "A", NewHighCard(King)); !r.Satisfied {
		t.Fatalf("A's King high should be TRUE: %s", r)
	}
	// B does not own the king, but B owns 8-h, which is in the evaluated
	// hand, so ownership is satisfied and the claim is TRUE.
	if r := s.check(t, "B", NewHighCard(King)); !r.Satisfied {
		t.Fatalf("B owns 8-h in the evaluated pool, so King high is TRUE: %s", r)
	}
	if r := s.check(t, "A", NewHighCard(Eight)); r.Satisfied {
		t.Fatalf("the pool is King high, not 8 high: %s", r)
	}
}

func TestReducedPoolExactClassificationStillApplies(t *testing.T) {
	// Three 8s plus a king: the pool classifies as Three of a Kind, so a
	// Pair of 8s claim is FALSE even though two 8s are present.
	s := newScenario(map[string]string{"A": "8s", "B": "8h", "C": "8d", "D": "Kc"})
	if r := s.check(t, "A", NewPair(Eight)); r.Satisfied {
		t.Fatalf("Pair of 8s must be FALSE against a four-card trips pool: %s", r)
	}
	if r := s.check(t, "A", NewTrips(Eight)); !r.Satisfied {
		t.Fatalf("Three 8s should be TRUE: %s", r)
	}
	if r := s.check(t, "D", NewTrips(Eight)); !r.Satisfied {
		t.Fatalf("D owns K-c in the evaluated pool, so Three 8s is TRUE: %s", r)
	}
	// Four of a kind with no kicker is reachable at four cards.
	q := newScenario(map[string]string{"A": "8s", "B": "8h", "C": "8d", "D": "8c"})
	if r := q.check(t, "A", NewQuads(Eight)); !r.Satisfied {
		t.Fatalf("Four 8s should be TRUE at four cards: %s", r)
	}
	// Two pair with no fifth card is reachable at four cards.
	tp := newScenario(map[string]string{"A": "8s", "B": "8h", "C": "4d", "D": "4c"})
	if r := tp.check(t, "A", NewTwoPair(Eight, Four)); !r.Satisfied {
		t.Fatalf("Two Pair 8s and 4s should be TRUE at four cards: %s", r)
	}
	// Categories needing five cards are unreachable.
	for _, c := range []Claim{
		NewStraight(Nine), NewFlush(Spades, King),
		NewFullHouse(Eight, Four), NewStraightFlush(Spades, Nine), NewRoyalFlush(Spades),
	} {
		if r := s.check(t, "A", c); r.Satisfied {
			t.Errorf("%s should be unreachable at four cards: %s", c, r)
		}
	}
}

func TestTransitionToExactFiveMode(t *testing.T) {
	// Pool of exactly five: one selection, so the claim must match the
	// classification of the whole pool.
	s := newScenario(map[string]string{"A": "8s 8h", "B": "Kd", "C": "3c", "D": "Ad"})
	r := s.check(t, "A", NewPair(Eight))
	if r.Mode != ModeExactFive {
		t.Fatalf("mode = %s, want EXACT_FIVE", r.Mode)
	}
	if !r.Satisfied {
		t.Fatalf("Pair of 8s should be TRUE: %s", r)
	}
	// A kicker owner satisfies ownership, here as everywhere.
	if r := s.check(t, "B", NewPair(Eight)); !r.Satisfied {
		t.Fatalf("B owns the king kicker, so this is TRUE: %s", r)
	}
}

func TestSixPlusCardsDoNotAllParticipate(t *testing.T) {
	// Six cards: the extra 8 can be excluded, so Pair of 8s is TRUE.
	six := newScenario(map[string]string{"A": "8s", "B": "8h", "C": "8d", "D": "Kc", "E": "3c", "F": "Ad"})
	r := six.check(t, "A", NewPair(Eight))
	if !r.Satisfied {
		t.Fatalf("Pair of 8s should be TRUE at six cards: %s", r)
	}
	if len(r.Witness) != 5 {
		t.Fatalf("witness has %d cards, want 5", len(r.Witness))
	}
	// Five cards with the same three 8s: only one selection, which is
	// trips, so the same claim is FALSE. This is the pair of cases that
	// pins the exact-classification rule.
	five := newScenario(map[string]string{"A": "8s", "B": "8h", "C": "8d", "D": "Kc", "E": "3c"})
	if r := five.check(t, "A", NewPair(Eight)); r.Satisfied {
		t.Fatalf("Pair of 8s must be FALSE when the only selection is trips: %s", r)
	}
	if r := five.check(t, "A", NewTrips(Eight)); !r.Satisfied {
		t.Fatalf("Three 8s should be TRUE: %s", r)
	}
}

func TestOwnershipRules(t *testing.T) {
	// The full house consumes all five cards and none belong to A.
	s := newScenario(map[string]string{"A": "2c", "B": "7s", "C": "7h", "D": "7d", "E": "8s", "F": "8h"})
	if r := s.check(t, "A", NewFullHouse(Seven, Eight)); r.Satisfied {
		t.Fatalf("A owns no card of the full house: %s", r)
	}
	// Three 7s leaves two free slots, one of which can be A's 2c.
	if r := s.check(t, "A", NewTrips(Seven)); !r.Satisfied {
		t.Fatalf("Three 7s with A's 2c as a filler should be TRUE: %s", r)
	}
	// Quads kicker ownership is enough: the pattern cards may belong
	// entirely to other players.
	q := newScenario(map[string]string{"A": "Kc", "B": "8s", "C": "8h", "D": "8d", "E": "8c"})
	if r := q.check(t, "A", NewQuads(Eight)); !r.Satisfied {
		t.Fatalf("A owning only the kicker should be TRUE: %s", r)
	}
}

func TestFlushDeclaredHighCardIsTheMaximum(t *testing.T) {
	// Only four hearts at or below the queen, so a queen-high heart flush
	// cannot be built even though an ace-high one can.
	s := newScenario(map[string]string{"A": "Ah", "B": "Qh", "C": "Jh", "D": "7h", "E": "3h"})
	if r := s.check(t, "A", NewFlush(Hearts, Queen)); r.Satisfied {
		t.Fatalf("queen-high should be FALSE with only four hearts below the queen: %s", r)
	}
	if r := s.check(t, "A", NewFlush(Hearts, Ace)); !r.Satisfied {
		t.Fatalf("ace-high hearts flush should be TRUE: %s", r)
	}
	// Add a fifth low heart and the queen-high claim becomes available.
	s2 := newScenario(map[string]string{"A": "Ah", "B": "Qh", "C": "Jh", "D": "7h", "E": "3h", "F": "5h"})
	r := s2.check(t, "B", NewFlush(Hearts, Queen))
	if !r.Satisfied {
		t.Fatalf("queen-high hearts flush should be TRUE now: %s", r)
	}
	for _, c := range r.Witness {
		if c.Rank > Queen {
			t.Fatalf("witness [%s] contains a card above the queen", CardsString(r.Witness))
		}
	}
	// Wrong suit is never satisfied by another suit of the same colour.
	if r := s2.check(t, "B", NewFlush(Diamonds, Queen)); r.Satisfied {
		t.Fatalf("hearts must not satisfy a diamonds flush: %s", r)
	}
}

func TestStraightShadowedByStraightFlush(t *testing.T) {
	s := newScenario(map[string]string{"A": "5s", "B": "6s", "C": "7s", "D": "8s", "E": "9s"})
	if r := s.check(t, "A", NewStraight(Nine)); r.Satisfied {
		t.Fatalf("a single-suited selection classifies as a straight flush: %s", r)
	}
	if r := s.check(t, "A", NewStraightFlush(Spades, Nine)); !r.Satisfied {
		t.Fatalf("Straight Flush to 9 of Spades should be TRUE: %s", r)
	}
	// Add an off-suit nine and a mixed-suit straight becomes available.
	s2 := newScenario(map[string]string{"A": "5s", "B": "6s", "C": "7s", "D": "8s", "E": "9s", "F": "9h"})
	if r := s2.check(t, "A", NewStraight(Nine)); !r.Satisfied {
		t.Fatalf("Straight to 9 should be TRUE with a mixed-suit selection: %s", r)
	}
	// Royal flush shadows Straight to A the same way.
	r := newScenario(map[string]string{"A": "10s", "B": "Js", "C": "Qs", "D": "Ks", "E": "As"})
	if got := r.check(t, "A", NewStraight(Ace)); got.Satisfied {
		t.Fatalf("a suited broadway selection is a royal flush: %s", got)
	}
	if got := r.check(t, "A", NewRoyalFlush(Spades)); !got.Satisfied {
		t.Fatalf("Royal Flush of Spades should be TRUE: %s", got)
	}
	// Ace-low straight needs an ace.
	w := newScenario(map[string]string{"A": "2s", "B": "3h", "C": "4d", "D": "5c", "E": "As"})
	if got := w.check(t, "A", NewStraight(Five)); !got.Satisfied {
		t.Fatalf("the wheel should be TRUE: %s", got)
	}
	nw := newScenario(map[string]string{"A": "2s", "B": "3h", "C": "4d", "D": "5c", "E": "Ks"})
	if got := nw.check(t, "A", NewStraight(Five)); got.Satisfied {
		t.Fatalf("no ace, so no wheel: %s", got)
	}
}

func TestFillerDistinctness(t *testing.T) {
	// Pair of 8s needs three fillers of distinct ranks. Here the only
	// remaining cards pair up, so every selection inflates the category.
	s := newScenario(map[string]string{"A": "8s", "B": "8h", "C": "4d", "D": "4c", "E": "Ks"})
	if r := s.check(t, "A", NewPair(Eight)); r.Satisfied {
		t.Fatalf("selection would be two pair, so Pair of 8s is FALSE: %s", r)
	}
	if r := s.check(t, "A", NewTwoPair(Eight, Four)); !r.Satisfied {
		t.Fatalf("Two Pair 8s and 4s should be TRUE: %s", r)
	}
	// Trips with two same-rank leftovers would be a full house.
	f := newScenario(map[string]string{"A": "8s", "B": "8h", "C": "8d", "D": "4c", "E": "4h"})
	if r := f.check(t, "A", NewTrips(Eight)); r.Satisfied {
		t.Fatalf("selection would be a full house, so Three 8s is FALSE: %s", r)
	}
	if r := f.check(t, "A", NewFullHouse(Eight, Four)); !r.Satisfied {
		t.Fatalf("8s full of 4s should be TRUE: %s", r)
	}
	// High card claim blocked because every selection contains a pair.
	h := newScenario(map[string]string{"A": "Ks", "B": "8h", "C": "8d", "D": "5c", "E": "3h"})
	if r := h.check(t, "A", NewHighCard(King)); r.Satisfied {
		t.Fatalf("the only selection holds a pair: %s", r)
	}
}

func TestHighCardExcludesAce(t *testing.T) {
	// Every five-card selection must contain the ace, so King high fails.
	s := newScenario(map[string]string{"A": "Ad", "B": "Ks", "C": "8h", "D": "5d", "E": "3c"})
	if r := s.check(t, "B", NewHighCard(King)); r.Satisfied {
		t.Fatalf("the ace cannot be excluded from a five-card pool: %s", r)
	}
	if r := s.check(t, "B", NewHighCard(Ace)); !r.Satisfied {
		t.Fatalf("Ace high should be TRUE: %s", r)
	}
	// With a sixth card the ace can be dropped.
	s2 := newScenario(map[string]string{"A": "Ad", "B": "Ks", "C": "8h", "D": "5d", "E": "3c", "F": "2h"})
	if r := s2.check(t, "B", NewHighCard(King)); !r.Satisfied {
		t.Fatalf("King high should be TRUE once the ace can be excluded: %s", r)
	}
}

func TestPoolTooSmallForAnyClaim(t *testing.T) {
	// A two-card pool can only realize High Card and Pair claims.
	s := newScenario(map[string]string{"A": "Ks", "B": "8h"})
	if r := s.check(t, "A", NewHighCard(King)); !r.Satisfied {
		t.Fatalf("King high should be TRUE over two cards: %s", r)
	}
	for _, c := range []Claim{NewPair(King), NewTwoPair(King, Eight), NewTrips(King), NewStraight(Nine)} {
		if r := s.check(t, "A", c); r.Satisfied {
			t.Errorf("%s should be FALSE over two cards: %s", c, r)
		}
	}
}

func TestCrossPlayerWitness(t *testing.T) {
	s := newScenario(map[string]string{
		"A": "7s", "B": "7h", "C": "7d", "D": "Kc", "E": "3c", "F": "Ad",
	})
	r := s.check(t, "A", NewTrips(Seven))
	if !r.Satisfied {
		t.Fatalf("Three 7s should be TRUE: %s", r)
	}
	owners := map[string]bool{}
	for _, c := range r.Witness {
		owners[s.owner[c]] = true
	}
	if len(owners) < 3 {
		t.Fatalf("witness [%s] spans %d players, want at least 3",
			CardsString(r.Witness), len(owners))
	}
}

// ---------------------------------------------------- differential testing

// TestConstructiveMatchesExhaustive is the primary correctness argument for
// the production validator. Every reported TRUE is already verified by
// Classify, so the construction cannot produce a false positive; this test
// is what rules out false negatives.
func TestConstructiveMatchesExhaustive(t *testing.T) {
	r := rand.New(rand.NewSource(20260916))
	deck := NewDeck()
	all := AllClaims()

	scenarios := 0
	comparisons := 0
	for poolSize := 2; poolSize <= 10; poolSize++ {
		for iter := 0; iter < 12; iter++ {
			r.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
			pool := append([]Card(nil), deck[:poolSize]...)
			owner := map[Card]int{}
			for i, c := range pool {
				owner[c] = i % 4 // spread across up to four owners
			}
			claimant := r.Intn(4)
			owns := func(c Card) bool { return owner[c] == claimant }
			scenarios++
			for _, c := range all {
				got := CanSatisfy(pool, owns, c)
				ref := CanSatisfyExhaustive(pool, owns, c)
				comparisons++
				if got.Satisfied != ref.Satisfied {
					t.Fatalf("disagreement: %s over [%s] claimant=%d: constructive=%v exhaustive=%v",
						c, CardsString(pool), claimant, got.Satisfied, ref.Satisfied)
				}
				if got.Satisfied && !Satisfies(got.Witness, c) {
					t.Fatalf("invalid witness [%s] for %s", CardsString(got.Witness), c)
				}
				if ref.Satisfied && !Satisfies(ref.Witness, c) {
					t.Fatalf("invalid oracle witness [%s] for %s", CardsString(ref.Witness), c)
				}
			}
		}
	}
	t.Logf("compared %d claim validations across %d scenarios", comparisons, scenarios)
}

// TestConstructiveMatchesExhaustiveLargePools covers wider pools with a
// sampled subset of claims, where full cross-product would be slow.
func TestConstructiveMatchesExhaustiveLargePools(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	deck := NewDeck()
	all := AllClaims()
	for _, poolSize := range []int{12, 16, 20, 25} {
		for iter := 0; iter < 3; iter++ {
			r.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
			pool := append([]Card(nil), deck[:poolSize]...)
			owner := map[Card]int{}
			for i, c := range pool {
				owner[c] = i % 5
			}
			claimant := r.Intn(5)
			owns := func(c Card) bool { return owner[c] == claimant }
			for i := 0; i < 60; i++ {
				c := all[r.Intn(len(all))]
				got := CanSatisfy(pool, owns, c)
				ref := CanSatisfyExhaustive(pool, owns, c)
				if got.Satisfied != ref.Satisfied {
					t.Fatalf("disagreement at pool %d: %s over [%s]: constructive=%v exhaustive=%v",
						poolSize, c, CardsString(pool), got.Satisfied, ref.Satisfied)
				}
			}
		}
	}
}

func FuzzClaimValidation(f *testing.F) {
	f.Add([]byte{3, 7, 11, 19, 23, 31, 40, 2}, uint8(17))
	f.Fuzz(func(t *testing.T, raw []byte, claimIdx uint8) {
		if len(raw) < 2 || len(raw) > 14 {
			return
		}
		deck := NewDeck()
		seen := map[Card]bool{}
		var pool []Card
		owner := map[Card]int{}
		for i, b := range raw {
			c := deck[int(b)%DeckSize]
			if seen[c] {
				continue
			}
			seen[c] = true
			pool = append(pool, c)
			owner[c] = i % 3
		}
		if len(pool) < 2 {
			return
		}
		owns := func(c Card) bool { return owner[c] == 0 }
		all := AllClaims()
		c := all[int(claimIdx)%len(all)]
		got := CanSatisfy(pool, owns, c)
		ref := CanSatisfyExhaustive(pool, owns, c)
		if got.Satisfied != ref.Satisfied {
			t.Fatalf("disagreement: %s over [%s]: constructive=%v exhaustive=%v",
				c, CardsString(pool), got.Satisfied, ref.Satisfied)
		}
		if got.Satisfied && !Satisfies(got.Witness, c) {
			t.Fatalf("invalid witness [%s] for %s", CardsString(got.Witness), c)
		}
	})
}

// ---------------------------------------------------------------- benchmarks

// BenchmarkValidateWorstCase uses the largest reachable pool (9 players at 5
// cards) and the claim that forces the widest search.
func BenchmarkValidateWorstCase(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	deck := NewDeck()
	r.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
	pool := deck[:45]
	owns := func(c Card) bool { return c == pool[0] }
	c := NewHighCard(Ace)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CanSatisfy(pool, owns, c)
	}
}

func BenchmarkValidateExhaustiveWorstCase(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	deck := NewDeck()
	r.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
	pool := deck[:45]
	owns := func(c Card) bool { return c == pool[0] }
	c := NewRoyalFlush(Spades) // a FALSE claim, so the full space is walked
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CanSatisfyExhaustive(pool, owns, c)
	}
}

// TestRealizableAtWidthMatchesEnumeration checks the width-sensitive
// realizability predicate against brute-force enumeration of every hand of
// every width the game can evaluate. The predicate is what lets the
// simulator and the claim builder reason about narrow pools without
// enumerating anything.
func TestRealizableAtWidthMatchesEnumeration(t *testing.T) {
	deck := NewDeck()
	all := AllClaims()
	for width := 2; width <= FullHandSize; width++ {
		actual := map[Claim]bool{}
		forEachCombination(deck, width, func(hand []Card) bool {
			v, err := Classify(hand)
			if err != nil {
				t.Fatalf("Classify(%s): %v", CardsString(hand), err)
			}
			actual[v] = true
			return false
		})
		for _, c := range all {
			want := actual[c]
			got := RealizableAtWidth(c, width)
			if got != want {
				t.Errorf("width %d: RealizableAtWidth(%s) = %v, enumeration says %v",
					width, c, got, want)
			}
		}
	}
}

func TestStraightRanks(t *testing.T) {
	if got := StraightRanks(Nine); len(got) != 5 || got[0] != Five || got[4] != Nine {
		t.Errorf("StraightRanks(9) = %v", got)
	}
	// The wheel: ace sorts last by rank but is the low end of the run.
	got := StraightRanks(Five)
	if len(got) != 5 || got[0] != Two || got[3] != Five || got[4] != Ace {
		t.Errorf("StraightRanks(5) = %v, want 2,3,4,5,A", got)
	}
}

// TestReducedModeOwnershipIsAlwaysSatisfied states the consequence of the
// single ownership rule at small pools, exhaustively over every partition of
// a small pool among players. In reduced mode the evaluated hand is the whole
// pool, and every in-play player is dealt at least one card into it, so the
// ownership condition can never fail there. Claim truth is therefore
// claimant-independent below five cards: the pool's own classification is
// true for everyone, and nothing else is true for anyone.
func TestReducedModeOwnershipIsAlwaysSatisfied(t *testing.T) {
	r := rand.New(rand.NewSource(613))
	deck := NewDeck()
	all := AllClaims()
	for poolSize := 2; poolSize <= 4; poolSize++ {
		for iter := 0; iter < 25; iter++ {
			r.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
			pool := append([]Card(nil), deck[:poolSize]...)
			truth := MustClassify(pool)
			// One card each: every card is a different owner.
			for owner := 0; owner < poolSize; owner++ {
				owns := func(c Card) bool { return c == pool[owner] }
				for _, c := range all {
					got := CanSatisfy(pool, owns, c).Satisfied
					want := c == truth
					if got != want {
						t.Fatalf("pool [%s] owner %d claim %s: got %v, want %v (pool is %s)",
							CardsString(pool), owner, c, got, want, truth)
					}
				}
			}
		}
	}
}

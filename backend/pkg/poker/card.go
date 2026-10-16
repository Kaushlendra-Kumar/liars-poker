// Package poker provides the card model, the 5-and-fewer card hand
// classifier, the claim model with its total order, and the exact claim
// validator used by the Liar's Poker engine.
//
// The package is pure: no I/O, no clock, no global randomness. Randomness
// enters through the RNG interface so shuffles are deterministic in tests.
package poker

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------- suits

// Suit is the exact suit of a card. Suit identity is never colour: a
// Hearts flush is Hearts specifically. The declaration order doubles as the
// least-significant tiebreak in the claim ordering.
type Suit uint8

const (
	Clubs Suit = iota
	Diamonds
	Hearts
	Spades
)

const numSuits = 4

var suitSymbol = [numSuits]string{"\u2663", "\u2666", "\u2665", "\u2660"}
var suitName = [numSuits]string{"CLUBS", "DIAMONDS", "HEARTS", "SPADES"}
var suitTitle = [numSuits]string{"Clubs", "Diamonds", "Hearts", "Spades"}

func (s Suit) Valid() bool { return s <= Spades }
func (s Suit) String() string {
	if !s.Valid() {
		return "?"
	}
	return suitSymbol[s]
}
func (s Suit) Name() string {
	if !s.Valid() {
		return "INVALID"
	}
	return suitName[s]
}
func (s Suit) Title() string {
	if !s.Valid() {
		return "Invalid"
	}
	return suitTitle[s]
}

// AllSuits returns the four suits in canonical order.
func AllSuits() []Suit { return []Suit{Clubs, Diamonds, Hearts, Spades} }

func ParseSuit(s string) (Suit, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "C", "CLUBS", "\u2663":
		return Clubs, nil
	case "D", "DIAMONDS", "\u2666":
		return Diamonds, nil
	case "H", "HEARTS", "\u2665":
		return Hearts, nil
	case "S", "SPADES", "\u2660":
		return Spades, nil
	}
	return 0, fmt.Errorf("poker: unknown suit %q", s)
}

// ---------------------------------------------------------------- ranks

// Rank is the rank of a card. The numeric value is the poker value, so Ace
// is 14. Ace additionally behaves as 1 for straight detection only; see
// straightHigh.
type Rank uint8

const (
	Two   Rank = 2
	Three Rank = 3
	Four  Rank = 4
	Five  Rank = 5
	Six   Rank = 6
	Seven Rank = 7
	Eight Rank = 8
	Nine  Rank = 9
	Ten   Rank = 10
	Jack  Rank = 11
	Queen Rank = 12
	King  Rank = 13
	Ace   Rank = 14

	MinRank = Two
	MaxRank = Ace
)

var rankShort = map[Rank]string{
	Two: "2", Three: "3", Four: "4", Five: "5", Six: "6", Seven: "7",
	Eight: "8", Nine: "9", Ten: "10", Jack: "J", Queen: "Q", King: "K", Ace: "A",
}

var rankWord = map[Rank]string{
	Two: "Two", Three: "Three", Four: "Four", Five: "Five", Six: "Six",
	Seven: "Seven", Eight: "Eight", Nine: "Nine", Ten: "Ten",
	Jack: "Jack", Queen: "Queen", King: "King", Ace: "Ace",
}

var rankPlural = map[Rank]string{
	Two: "2s", Three: "3s", Four: "4s", Five: "5s", Six: "6s", Seven: "7s",
	Eight: "8s", Nine: "9s", Ten: "10s", Jack: "Jacks", Queen: "Queens",
	King: "Kings", Ace: "Aces",
}

func (r Rank) Valid() bool { return r >= MinRank && r <= MaxRank }

func (r Rank) String() string {
	if s, ok := rankShort[r]; ok {
		return s
	}
	return "?"
}

// Word returns the spoken form, e.g. "Queen".
func (r Rank) Word() string {
	if s, ok := rankWord[r]; ok {
		return s
	}
	return "?"
}

// Plural returns the claim form, e.g. "Queens" or "8s".
func (r Rank) Plural() string {
	if s, ok := rankPlural[r]; ok {
		return s
	}
	return "?s"
}

// AllRanks returns 2..A ascending.
func AllRanks() []Rank {
	out := make([]Rank, 0, 13)
	for r := MinRank; r <= MaxRank; r++ {
		out = append(out, r)
	}
	return out
}

func ParseRank(s string) (Rank, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "2":
		return Two, nil
	case "3":
		return Three, nil
	case "4":
		return Four, nil
	case "5":
		return Five, nil
	case "6":
		return Six, nil
	case "7":
		return Seven, nil
	case "8":
		return Eight, nil
	case "9":
		return Nine, nil
	case "10", "T":
		return Ten, nil
	case "J", "JACK":
		return Jack, nil
	case "Q", "QUEEN":
		return Queen, nil
	case "K", "KING":
		return King, nil
	case "A", "ACE":
		return Ace, nil
	}
	return 0, fmt.Errorf("poker: unknown rank %q", s)
}

// ---------------------------------------------------------------- cards

// Card is a single physical playing card. It is comparable, so it can be a
// map key and compared with ==.
type Card struct {
	Rank Rank
	Suit Suit
}

// Key is the canonical sort key: rank ascending, then suit ascending with
// Clubs < Diamonds < Hearts < Spades. Every deterministic iteration order in
// this package derives from Key.
func (c Card) Key() int { return int(c.Rank)*numSuits + int(c.Suit) }

func (c Card) Valid() bool    { return c.Rank.Valid() && c.Suit.Valid() }
func (c Card) String() string { return c.Rank.String() + c.Suit.String() }

// ParseCard accepts forms like "8S", "10h", "Ad", "K\u2660".
func ParseCard(s string) (Card, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return Card{}, fmt.Errorf("poker: bad card %q", s)
	}
	// Suit is the final rune; rank is everything before it.
	runes := []rune(s)
	suit, err := ParseSuit(string(runes[len(runes)-1]))
	if err != nil {
		return Card{}, err
	}
	rank, err := ParseRank(string(runes[:len(runes)-1]))
	if err != nil {
		return Card{}, err
	}
	return Card{Rank: rank, Suit: suit}, nil
}

// MustParseCards parses a whitespace-separated list, panicking on error. For
// tests and fixtures only.
func MustParseCards(s string) []Card {
	fields := strings.Fields(s)
	out := make([]Card, 0, len(fields))
	for _, f := range fields {
		c, err := ParseCard(f)
		if err != nil {
			panic(err)
		}
		out = append(out, c)
	}
	return out
}

// SortCards sorts in place into canonical order.
func SortCards(cs []Card) { sort.Slice(cs, func(i, j int) bool { return cs[i].Key() < cs[j].Key() }) }

// SortedCopy returns a canonically ordered copy.
func SortedCopy(cs []Card) []Card {
	out := make([]Card, len(cs))
	copy(out, cs)
	SortCards(out)
	return out
}

// CardsString renders a canonically ordered, space-separated list.
func CardsString(cs []Card) string {
	s := SortedCopy(cs)
	parts := make([]string, len(s))
	for i, c := range s {
		parts[i] = c.String()
	}
	return strings.Join(parts, " ")
}

// HasDuplicates reports whether any physical card appears twice. Invariant I1
// is enforced with this.
func HasDuplicates(cs []Card) bool {
	seen := make(map[Card]struct{}, len(cs))
	for _, c := range cs {
		if _, ok := seen[c]; ok {
			return true
		}
		seen[c] = struct{}{}
	}
	return false
}

// ---------------------------------------------------------------- deck

// DeckSize is the number of cards in a standard deck.
const DeckSize = 52

// ErrDeckExhausted is returned when a deal would draw past the end of the
// deck. With at most 9 players holding at most 5 cards each, a maximum of 45
// cards are ever dealt, so this is defensive rather than reachable.
var ErrDeckExhausted = errors.New("poker: deck exhausted")

// RNG is the randomness a deck needs. math/rand's *Rand satisfies it, so
// tests inject a fixed seed and production injects a crypto-seeded source.
type RNG interface {
	Intn(n int) int
}

// NewDeck returns all 52 unique cards in canonical order.
func NewDeck() []Card {
	out := make([]Card, 0, DeckSize)
	for r := MinRank; r <= MaxRank; r++ {
		for s := Clubs; s <= Spades; s++ {
			out = append(out, Card{Rank: r, Suit: s})
		}
	}
	return out
}

// Deck is a shuffled stack dealt from the top.
type Deck struct {
	cards []Card
	next  int
}

// NewShuffledDeck returns a full 52-card deck shuffled with rng.
func NewShuffledDeck(rng RNG) *Deck {
	d := &Deck{cards: NewDeck()}
	d.Shuffle(rng)
	return d
}

// Shuffle resets the deck to a full 52 cards and applies an unbiased
// Fisher-Yates shuffle. Resetting here is what makes the round lifecycle's
// "return all cards, reshuffle" step a single call.
func (d *Deck) Shuffle(rng RNG) {
	d.cards = NewDeck()
	d.next = 0
	for i := len(d.cards) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		d.cards[i], d.cards[j] = d.cards[j], d.cards[i]
	}
}

// Deal draws n cards from the top.
func (d *Deck) Deal(n int) ([]Card, error) {
	if n < 0 {
		return nil, fmt.Errorf("poker: negative deal %d", n)
	}
	if d.Remaining() < n {
		return nil, ErrDeckExhausted
	}
	out := make([]Card, n)
	copy(out, d.cards[d.next:d.next+n])
	d.next += n
	return out, nil
}

// Remaining reports how many cards are still undealt.
func (d *Deck) Remaining() int { return len(d.cards) - d.next }

// Dealt reports how many cards have left the deck.
func (d *Deck) Dealt() int { return d.next }

// ---------------------------------------------------------------- wire form

var suitLetter = [numSuits]string{"C", "D", "H", "S"}

// Code is the transport-safe rendering of a card: rank then suit letter, for
// example "8S", "10H", "AD". Suit symbols are reserved for display.
func (c Card) Code() string {
	if !c.Valid() {
		return "??"
	}
	return c.Rank.String() + suitLetter[c.Suit]
}

// MarshalJSON emits the card code. Without this a card would serialise as
// its internal numeric fields, which would leak the representation into the
// protocol and make the wire form fragile.
func (c Card) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, fmt.Errorf("poker: cannot marshal invalid card %d/%d", c.Rank, c.Suit)
	}
	return []byte(`"` + c.Code() + `"`), nil
}

// UnmarshalJSON parses a card code through ParseCard, so anything arriving
// from the wire is validated.
func (c *Card) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := ParseCard(s)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

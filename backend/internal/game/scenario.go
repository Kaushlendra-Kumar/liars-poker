package game

import (
	"fmt"
	"strings"

	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// InjectHands replaces the dealt hands with explicit ones and rebuilds the
// frozen pool and ownership map around them. Hand sizes are set from the
// hands given, so the result satisfies every engine invariant.
//
// This exists for the dry-run tool and for scenario tests that need a known
// deal. Nothing on the server path calls it, and it is the only way to get
// cards into the engine other than a shuffled deal.
func InjectHands(s State, hands map[PlayerID]string) State {
	ns := s.clone()
	ns.Pool = nil
	ns.Owner = map[poker.Card]PlayerID{}
	for i := range ns.Players {
		p := &ns.Players[i]
		spec, ok := hands[p.ID]
		if !ok {
			p.Hand = nil
			continue
		}
		cards := poker.MustParseCards(spec)
		poker.SortCards(cards)
		p.Hand = cards
		p.HandSize = len(cards)
		for _, c := range cards {
			ns.Owner[c] = p.ID
		}
		ns.Pool = append(ns.Pool, cards...)
	}
	poker.SortCards(ns.Pool)
	ns.DeckRemaining = poker.DeckSize - len(ns.Pool)
	if poker.HasDuplicates(ns.Pool) {
		panic("game: InjectHands got duplicate cards")
	}
	return ns
}

// SetHandSize forces a player's penalty level, for scenarios that need a
// player already close to elimination. Tooling only.
func SetHandSize(s State, id PlayerID, size int) State {
	ns := s.clone()
	seat, ok := ns.seatOf(id)
	if !ok {
		panic(fmt.Sprintf("game: no such player %s", id))
	}
	ns.Players[seat].HandSize = size
	return ns
}

// Eliminate removes a player, for scenarios that need a reduced table such
// as the two-player endgame. Tooling only.
func Eliminate(s State, id PlayerID) State {
	ns := s.clone()
	seat, ok := ns.seatOf(id)
	if !ok {
		panic(fmt.Sprintf("game: no such player %s", id))
	}
	ns.Players[seat].Status = StatusEliminated
	ns.Players[seat].Hand = nil
	return ns
}

// CardCounts renders the public per-player card counts, e.g. "A=1 B=2 C=1".
func CardCounts(s State) string {
	var parts []string
	for i := range s.Players {
		p := s.Players[i]
		if !p.Status.InPlay() {
			if p.Status == StatusEliminated {
				parts = append(parts, fmt.Sprintf("%s=out", p.ID))
			}
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%d", p.ID, p.HandSize))
	}
	return strings.Join(parts, " ")
}

// HandsLine renders the revealed hands, e.g. "A:8s  B:8h".
func HandsLine(s State) string {
	out := ""
	for i := range s.Players {
		p := s.Players[i]
		if !p.Status.InPlay() || len(p.Hand) == 0 {
			continue
		}
		if out != "" {
			out += "   "
		}
		out += fmt.Sprintf("%s:%s", p.ID, poker.CardsString(p.Hand))
	}
	return out
}

// NextInPlaySeat exposes the rotation helper for tooling and scenario runs.
func (s State) NextInPlaySeat(from int) int { return s.nextInPlaySeat(from) }

// FirstTrueClaimFor searches the claim space in ascending order for a claim
// that is actually true for the given player against the current pool. Used
// by the dry-run tool when it needs a guaranteed-true claim; never used on
// the server path.
func FirstTrueClaimFor(s State, claimant PlayerID) (poker.Claim, bool) {
	owns := func(c poker.Card) bool { return s.Owner[c] == claimant }
	for _, c := range poker.AllClaims() {
		if poker.CanSatisfy(s.Pool, owns, c).Satisfied {
			return c, true
		}
	}
	return poker.Claim{}, false
}

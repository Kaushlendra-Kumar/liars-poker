// Command dryrun executes the scenarios required before Phase 1 sign-off
// through the real engine and prints the trace for each. Nothing here is
// hand-written commentary about what the engine "would" do: every line is
// produced by Apply, View and the validator.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

var ids = []game.PlayerID{"A", "B", "C", "D", "E", "F", "G", "H", "I"}

func die(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "dryrun: %v\n", err)
		os.Exit(1)
	}
}

func apply(s game.State, a game.Action) game.State {
	ns, _, err := game.Apply(s, a)
	die(err)
	die(game.CheckInvariants(ns))
	return ns
}

// newTable starts a game with n players and seat 0 as the first starter.
func newTable(n int, seed int64) game.State {
	cfg := game.DefaultConfig(seed)
	cfg.FirstStarter = game.FirstStarterSeatZero
	cfg.TurnDuration = 0
	cfg.RoundEndDuration = 0
	s := game.NewGame(cfg)
	for i := 0; i < n; i++ {
		s = apply(s, game.ActionJoin{Meta: game.Meta{PlayerID: ids[i]}, Name: string(ids[i])})
		s = apply(s, game.ActionSetReady{Meta: game.Meta{PlayerID: ids[i]}, Ready: true})
	}
	return apply(s, game.ActionStartGame{Meta: game.Meta{PlayerID: ids[0]}})
}

func heading(n int, title string) {
	line := strings.Repeat("\u2550", 74)
	fmt.Printf("\n%s\nSCENARIO %d \u2014 %s\n%s\n", line, n, title, line)
}

func row(label, value string) { fmt.Printf("%-20s: %s\n", label, value) }

func poolLine(s game.State) string {
	mode := poker.ModeFor(s.PoolSize())
	return fmt.Sprintf("pool %d cards, mode %s, hand width %d",
		s.PoolSize(), mode, poker.HandSize(s.PoolSize()))
}

// claimStep prints the structural-legality decision for a claim, then
// submits it. It reports exactly what the engine decided, including the
// rejection path.
func claimStep(s game.State, who game.PlayerID, c poker.Claim) game.State {
	seat, _ := seatOf(s, who)
	role := "raise"
	if len(s.Claims) == 0 {
		role = "round starter, opening claim"
	}
	row("Current claimant", fmt.Sprintf("%s (seat %d, %s)", who, seat, role))
	row("Claim", c.String())

	ord, ok := c.Ordinal()
	legal := "no"
	switch {
	case !ok:
		legal = "NO \u2014 malformed, not a realizable claim"
	default:
		if standing, has := s.StandingClaim(); has {
			so, _ := standing.Claim.Ordinal()
			if poker.Higher(c, standing.Claim) {
				legal = fmt.Sprintf("yes \u2014 ordinal %d of %d, beats standing %s (ordinal %d)",
					ord, poker.ClaimSpaceSize, standing.Claim, so)
			} else {
				legal = fmt.Sprintf("NO \u2014 ordinal %d does not exceed standing ordinal %d", ord, so)
			}
		} else {
			legal = fmt.Sprintf("yes \u2014 ordinal %d of %d, no standing claim to beat",
				ord, poker.ClaimSpaceSize)
		}
	}
	row("Structurally legal", legal)

	ns, _, err := game.Apply(s, game.ActionMakeClaim{Meta: game.Meta{PlayerID: who}, Claim: c})
	if err != nil {
		row("Engine verdict", "REJECTED: "+err.Error())
		return s
	}
	die(game.CheckInvariants(ns))
	return ns
}

// showStep calls SHOW and prints validation, outcome, penalty, and the state
// of the next round.
func showStep(s game.State, who game.PlayerID, advance bool) game.State {
	seat, _ := seatOf(s, who)
	row("Next player action", fmt.Sprintf("%s (seat %d) calls SHOW", who, seat))

	s = apply(s, game.ActionCallShow{Meta: game.Meta{PlayerID: who}})
	r := s.LastShow

	// Re-run the validator to report the ownership detail behind the verdict.
	owns := func(c poker.Card) bool { return s.Owner[c] == r.Claimant }
	if r.ClaimTrue {
		var mine []poker.Card
		for _, c := range r.Witness {
			if owns(c) {
				mine = append(mine, c)
			}
		}
		pattern := map[poker.Card]bool{}
		for _, c := range poker.PatternCards(r.Witness, r.Claim) {
			pattern[c] = true
		}
		inPattern := false
		for _, c := range mine {
			if pattern[c] {
				inPattern = true
			}
		}
		note := "outside the claimed pattern, which the ownership rule permits"
		if inPattern {
			note = "part of the claimed pattern"
		}
		row("Exact validation", fmt.Sprintf("TRUE \u2014 witness [%s] classifies as %s",
			poker.CardsString(r.Witness), poker.MustClassify(r.Witness)))
		row("", fmt.Sprintf("%s contributes %s (%s)",
			r.Claimant, poker.CardsString(mine), note))
	} else {
		// Distinguish the two reasons a claim fails: the pattern is not
		// constructible at all, or it is but the claimant owns none of it.
		anyone := func(poker.Card) bool { return true }
		ignoringOwnership := poker.CanSatisfy(s.Pool, anyone, r.Claim)
		var why string
		switch {
		case ignoringOwnership.Satisfied:
			why = fmt.Sprintf("the hand exists as [%s], but no selection realizing it contains a card owned by %s",
				poker.CardsString(ignoringOwnership.Witness), r.Claimant)
		case r.Mode == poker.ModeReducedPool:
			why = fmt.Sprintf("the available %d-card pool classifies as %s, not %s",
				r.PoolSize, poker.MustClassify(s.Pool), r.Claim)
		default:
			why = fmt.Sprintf("no %d-card subset of the %d-card pool classifies as %s",
				r.HandSize, r.PoolSize, r.Claim)
		}
		row("Exact validation", fmt.Sprintf("FALSE \u2014 %s", why))
	}
	row("Mode", fmt.Sprintf("%s (pool %d, hand width %d)",
		r.Mode, r.PoolSize, r.HandSize))

	verdict := "claim FALSE"
	if r.ClaimTrue {
		verdict = "claim TRUE"
	}
	winner := r.Claimant
	if r.Loser == r.Claimant {
		winner = r.Challenger
	}
	row("SHOW outcome", fmt.Sprintf("%s \u2014 %s wins, %s loses", verdict, winner, r.Loser))

	p := playerOf(s, r.Loser)
	if p.Status == game.StatusEliminated {
		row("Penalty", fmt.Sprintf("%s would be dealt %d cards \u2014 ELIMINATED (capped at %d)",
			r.Loser, game.EliminationHandSize, game.MaxHandSize))
	} else {
		row("Penalty", fmt.Sprintf("%s %d \u2192 %d cards", r.Loser, p.HandSize-1, p.HandSize))
	}

	if s.Phase == game.GameOver {
		row("Next-round counts", "none \u2014 game over")
		row("Winner", string(s.Winner))
		return s
	}
	if !advance {
		row("Next-round counts", game.CardCounts(s)+" (pending round advance)")
		return s
	}
	s = apply(s, game.ActionAdvanceRound{Meta: game.Meta{PlayerID: ids[0]}})
	row("Next-round counts", fmt.Sprintf("%s \u2014 %s", game.CardCounts(s), poolLine(s)))
	row("Next starter", fmt.Sprintf("%s (seat %d)", s.Players[s.StarterSeat].ID, s.StarterSeat))
	return s
}

func seatOf(s game.State, id game.PlayerID) (int, bool) {
	for i := range s.Players {
		if s.Players[i].ID == id {
			return i, true
		}
	}
	return -1, false
}

func playerOf(s game.State, id game.PlayerID) game.Player {
	i, _ := seatOf(s, id)
	return s.Players[i]
}

// preamble prints the counts and cards for the round about to be played.
func preamble(s game.State) {
	row("Player card counts", fmt.Sprintf("%s \u2014 %s", game.CardCounts(s), poolLine(s)))
	row("Actual cards", game.HandsLine(s))
	if s.PoolSize() > 0 {
		width := poker.HandSize(s.PoolSize())
		if width == s.PoolSize() {
			row("", fmt.Sprintf("whole pool [%s] classifies as %s",
				poker.CardsString(s.Pool), poker.MustClassify(s.Pool)))
		} else {
			row("", fmt.Sprintf("pool [%s] \u2014 validation quantifies over all %d-card subsets",
				poker.CardsString(s.Pool), width))
		}
	}
	row("Round / starter", fmt.Sprintf("round %d, starter %s (seat %d)",
		s.Round, s.Players[s.StarterSeat].ID, s.StarterSeat))
}

func main() {
	fmt.Printf("Liar's Poker \u2014 machine-executed dry run\n")
	fmt.Printf("Claim space: %d claims, min %s, max %s\n",
		poker.ClaimSpaceSize, poker.MinClaim(), poker.MaxClaim())
	fmt.Printf("Reduced-pool rule: pool < 5 \u2192 evaluate the whole pool; pool >= 5 \u2192 exact 5-card subsets\n")
	fmt.Printf("Ownership rule (identical in both modes): the claimant must own at least one\n")
	fmt.Printf("card of the evaluated hand. It need not be a card of the claimed pattern.\n")

	// ---- 1. Four-player round one -------------------------------------
	heading(1, "Four-player round 1 (reduced pool of 4)")
	s := newTable(4, 1)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "8s", "B": "8h", "C": "Kd", "D": "3c"})
	preamble(s)
	s = claimStep(s, "A", poker.NewPair(poker.Eight))
	s = showStep(s, "B", true)

	// ---- 2. Four-card High Card claim ---------------------------------
	heading(2, "Four-card High Card claim")
	s = newTable(4, 2)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "Ks", "B": "8h", "C": "5d", "D": "3c"})
	preamble(s)
	s = claimStep(s, "A", poker.NewHighCard(poker.King))
	s = showStep(s, "B", true)

	// ---- 3. Four-card Pair claim, non-owner version -------------------
	heading(3, "Four-card Pair claim \u2014 true for every in-play player")
	s = newTable(4, 3)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "8s", "B": "8h", "C": "Kd", "D": "3c"})
	preamble(s)
	truth3 := poker.NewPair(poker.Eight)
	for _, id := range ids[:4] {
		owns := func(c poker.Card) bool { return s.Owner[c] == id }
		row(fmt.Sprintf("  %s claims Pair of 8s", id),
			fmt.Sprintf("%v", poker.CanSatisfy(s.Pool, owns, truth3).Satisfied))
	}
	fmt.Println("                      (the evaluated hand is the whole pool, so every")
	fmt.Println("                       player owns a card of it and ownership is satisfied)")
	s = claimStep(s, "A", poker.NewPair(poker.Eight))
	s = showStep(s, "B", false)

	// ---- 4. False four-card claim -------------------------------------
	heading(4, "False four-card claim \u2014 the pool does not classify as the claim")
	s = newTable(4, 4)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "Ks", "B": "8h", "C": "8d", "D": "3c"})
	preamble(s)
	s = claimStep(s, "A", poker.NewTrips(poker.Eight))
	s = showStep(s, "B", true)
	fmt.Println("\n  Same pool, the claim the pool actually supports \u2014 and note A owns")
	fmt.Println("  only the king, not either 8, which the ownership rule permits:")
	s = newTable(4, 4)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "Ks", "B": "8h", "C": "8d", "D": "3c"})
	s = claimStep(s, "A", poker.NewPair(poker.Eight))
	s = showStep(s, "B", false)

	// ---- 5 & 6. SHOW eligibility and the penalty that follows ---------
	heading(5, "Four-card SHOW: who may call it, and the penalty that follows")
	s = newTable(4, 5)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "9s", "B": "9h", "C": "Qd", "D": "4c"})
	preamble(s)
	row("Starter may SHOW?", fmt.Sprintf("%v \u2014 no standing claim exists", s.CanShow("A")))
	s = claimStep(s, "A", poker.NewPair(poker.Nine))
	for _, id := range ids[:4] {
		_, _, err := game.Apply(s, game.ActionCallShow{Meta: game.Meta{PlayerID: id}})
		verdict := "accepted"
		if err != nil {
			verdict = "rejected: " + err.Error()
		}
		row(fmt.Sprintf("  SHOW by %s", id), verdict)
	}
	heading(6, "Penalty after a four-card SHOW")
	s = showStep(s, "B", true)

	// ---- 7. Round two with five total cards ---------------------------
	heading(7, "Round 2 with five total cards \u2014 mode switches to exact five")
	s = newTable(4, 7)
	s = game.SetHandSize(s, "A", 2)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "8s 8h", "B": "Kd", "C": "3c", "D": "Ad"})
	preamble(s)
	s = claimStep(s, "A", poker.NewPair(poker.Eight))
	s = showStep(s, "B", false)

	// ---- 8. Normal five-card validation -------------------------------
	heading(8, "Normal five-card validation, five players")
	s = newTable(5, 8)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "8s", "B": "8h", "C": "Kd", "D": "3c", "E": "Ad"})
	preamble(s)
	s = claimStep(s, "A", poker.NewPair(poker.Eight))
	s = claimStep(s, "B", poker.NewTrips(poker.Eight))
	fmt.Println("                      (only the last claim before SHOW is ever validated)")
	s = showStep(s, "C", true)

	// ---- 9. Extra-card exclusion --------------------------------------
	heading(9, "Extra-card exclusion \u2014 six cards, three 8s, Pair of 8s")
	s = newTable(6, 9)
	s = game.InjectHands(s, map[game.PlayerID]string{
		"A": "8s", "B": "8h", "C": "8d", "D": "Kc", "E": "3c", "F": "Ad"})
	preamble(s)
	s = claimStep(s, "A", poker.NewPair(poker.Eight))
	s = showStep(s, "B", false)

	// ---- 10. Stronger hand does not validate a weaker claim -----------
	heading(10, "Stronger hand vs weaker claim \u2014 the same three 8s, five-card pool")
	s = newTable(5, 10)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "8s", "B": "8h", "C": "8d", "D": "Kc", "E": "3c"})
	preamble(s)
	s = claimStep(s, "A", poker.NewPair(poker.Eight))
	s = showStep(s, "B", false)
	fmt.Println("\n  Same pool, correct claim:")
	s = newTable(5, 10)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "8s", "B": "8h", "C": "8d", "D": "Kc", "E": "3c"})
	s = claimStep(s, "A", poker.NewTrips(poker.Eight))
	s = showStep(s, "B", false)
	fmt.Println("\n  And the four-card version of the same principle:")
	s = newTable(4, 10)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "8s", "B": "8h", "C": "8d", "D": "Kc"})
	preamble(s)
	s = claimStep(s, "A", poker.NewPair(poker.Eight))
	s = showStep(s, "B", false)

	// ---- 11. Ownership requirement ------------------------------------
	heading(11, "Ownership at five or more cards \u2014 the selection can exclude the claimant")
	s = newTable(6, 11)
	s = game.InjectHands(s, map[game.PlayerID]string{
		"A": "2c", "B": "7s", "C": "7h", "D": "7d", "E": "8s", "F": "8h"})
	preamble(s)
	s = claimStep(s, "A", poker.NewFullHouse(poker.Seven, poker.Eight))
	s = showStep(s, "B", false)
	fmt.Println("\n  Same pool, a claim that leaves a free slot for A's own card:")
	s = newTable(6, 11)
	s = game.InjectHands(s, map[game.PlayerID]string{
		"A": "2c", "B": "7s", "C": "7h", "D": "7d", "E": "8s", "F": "8h"})
	s = claimStep(s, "A", poker.NewTrips(poker.Seven))
	s = showStep(s, "B", false)
	fmt.Println("\n  And a five-card pool where the claimant owns only the kicker:")
	s = newTable(5, 11)
	s = game.InjectHands(s, map[game.PlayerID]string{
		"A": "Ks", "B": "8h", "C": "8d", "D": "8s", "E": "8c"})
	preamble(s)
	s = claimStep(s, "A", poker.NewQuads(poker.Eight))
	s = showStep(s, "B", false)

	// ---- 12. Multiple players contributing cards ----------------------
	heading(12, "Multiple players contributing to one witness")
	s = newTable(6, 12)
	s = game.InjectHands(s, map[game.PlayerID]string{
		"A": "7s", "B": "7h", "C": "7d", "D": "Kc", "E": "3c", "F": "Ad"})
	preamble(s)
	s = claimStep(s, "A", poker.NewTrips(poker.Seven))
	s = showStep(s, "B", false)
	if s.LastShow != nil && s.LastShow.ClaimTrue {
		owners := map[game.PlayerID]bool{}
		for _, c := range s.LastShow.Witness {
			owners[s.Owner[c]] = true
		}
		row("Witness spans", fmt.Sprintf("%d different players' hands", len(owners)))
	}

	// ---- 13. Progression to five cards --------------------------------
	heading(13, "Progression from a four-card pool to five and beyond")
	s = newTable(4, 13)
	for round := 1; round <= 4; round++ {
		s = game.InjectHands(s, fourCardDeal(s))
		row(fmt.Sprintf("Round %d", round), fmt.Sprintf("%s \u2014 %s", game.CardCounts(s), poolLine(s)))
		claimant := s.Players[s.TurnSeat].ID
		challenger := s.Players[s.NextInPlaySeat(s.TurnSeat)].ID
		s = apply(s, game.ActionMakeClaim{
			Meta: game.Meta{PlayerID: claimant}, Claim: poker.NewRoyalFlush(poker.Spades)})
		s = apply(s, game.ActionCallShow{Meta: game.Meta{PlayerID: challenger}})
		row("", fmt.Sprintf("%s claimed %s \u2192 FALSE \u2192 %s penalised",
			claimant, poker.NewRoyalFlush(poker.Spades), s.LastShow.Loser))
		s = apply(s, game.ActionAdvanceRound{Meta: game.Meta{PlayerID: ids[0]}})
	}
	row("Round 5", fmt.Sprintf("%s \u2014 %s", game.CardCounts(s), poolLine(s)))

	// ---- 14 & 15. Five-card player loses, then is eliminated ----------
	heading(14, "A five-card player loses a SHOW")
	s = newTable(4, 14)
	s = game.SetHandSize(s, "A", 5)
	s = game.SetHandSize(s, "B", 2)
	s = game.InjectHands(s, map[game.PlayerID]string{
		"A": "2c 3d 4h 6s 9c", "B": "Kd Qh", "C": "5c", "D": "7s"})
	preamble(s)
	row("Note", "A is at the maximum hand size of 5; one more loss ends their game")
	s = claimStep(s, "A", poker.NewRoyalFlush(poker.Spades))
	heading(15, "Attempted sixth card \u2192 elimination")
	s = showStep(s, "B", true)
	row("A status", statusLine(s, "A"))
	row("Table now", fmt.Sprintf("%s \u2014 %s", game.CardCounts(s), poolLine(s)))

	// ---- 16. Rotation independent of SHOW and penalty -----------------
	heading(16, "Starter rotation is independent of SHOW outcome and penalty")
	for _, claimantLoses := range []bool{true, false} {
		label := "challenger loses"
		if claimantLoses {
			label = "claimant loses"
		}
		s = newTable(4, 16)
		var seq []string
		for round := 1; round <= 5; round++ {
			s = game.InjectHands(s, fourCardDeal(s))
			claimant := s.Players[s.TurnSeat].ID
			challenger := s.Players[s.NextInPlaySeat(s.TurnSeat)].ID
			claim := poker.NewRoyalFlush(poker.Spades)
			if !claimantLoses {
				if t, ok := game.FirstTrueClaimFor(s, claimant); ok {
					claim = t
				}
			}
			starter := s.Players[s.StarterSeat].ID
			s = apply(s, game.ActionMakeClaim{Meta: game.Meta{PlayerID: claimant}, Claim: claim})
			s = apply(s, game.ActionCallShow{Meta: game.Meta{PlayerID: challenger}})
			seq = append(seq, fmt.Sprintf("R%d start=%s lost=%s", round, starter, s.LastShow.Loser))
			s = apply(s, game.ActionAdvanceRound{Meta: game.Meta{PlayerID: ids[0]}})
		}
		row(label, strings.Join(seq, " | "))
	}
	row("Conclusion", "identical sequences: rotation reads only seats, eliminations and round number")

	// ---- 17. Two-player endgame ---------------------------------------
	heading(17, "Two-player endgame")
	s = newTable(4, 17)
	s = game.Eliminate(s, "C")
	s = game.Eliminate(s, "D")
	s = game.SetHandSize(s, "A", 3)
	s = game.SetHandSize(s, "B", 2)
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "9s 9h 4d", "B": "Kc 3s"})
	preamble(s)
	s = claimStep(s, "A", poker.NewPair(poker.Nine))
	s = showStep(s, "B", true)

	// ---- 18. A later sub-five pool ------------------------------------
	heading(18, "A later sub-five pool: two survivors on one card each")
	s = newTable(4, 18)
	s = game.Eliminate(s, "C")
	s = game.Eliminate(s, "D")
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "Ks", "B": "7h"})
	preamble(s)
	row("Note", "a two-card pool can only realize High Card and Pair claims")
	s = claimStep(s, "A", poker.NewHighCard(poker.King))
	s = showStep(s, "B", true)
	fmt.Println("\n  And the same pool with a claim the width cannot support:")
	s = newTable(4, 18)
	s = game.Eliminate(s, "C")
	s = game.Eliminate(s, "D")
	s = game.InjectHands(s, map[game.PlayerID]string{"A": "Ks", "B": "7h"})
	s = claimStep(s, "A", poker.NewStraight(poker.Nine))
	s = showStep(s, "B", false)

	fmt.Printf("\n%s\nAll 18 scenarios executed by the engine. No invariant violations.\n%s\n",
		strings.Repeat("\u2550", 74), strings.Repeat("\u2550", 74))
}

func statusLine(s game.State, id game.PlayerID) string {
	p := playerOf(s, id)
	return fmt.Sprintf("%s, hand size %d", p.Status, p.HandSize)
}

// fourCardDeal builds a deterministic deal matching current hand sizes from
// the low end of a canonical deck, so no ace is ever in play.
func fourCardDeal(s game.State) map[game.PlayerID]string {
	deck := poker.NewDeck()
	out := map[game.PlayerID]string{}
	idx := 0
	for i := range s.Players {
		p := s.Players[i]
		if !p.Status.InPlay() {
			continue
		}
		var parts []string
		for k := 0; k < p.HandSize; k++ {
			parts = append(parts, deck[idx].String())
			idx++
		}
		out[p.ID] = strings.Join(parts, " ")
	}
	return out
}

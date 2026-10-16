package game

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// ---------------------------------------------------------------- helpers

var names = []PlayerID{"A", "B", "C", "D", "E", "F", "G", "H", "I"}

func must(t *testing.T, s State, a Action) State {
	t.Helper()
	ns, _, err := Apply(s, a)
	if err != nil {
		t.Fatalf("%s rejected: %v", a.name(), err)
	}
	if err := CheckInvariants(ns); err != nil {
		t.Fatalf("invariant broken after %s: %v", a.name(), err)
	}
	return ns
}

func mustFail(t *testing.T, s State, a Action, want error) {
	t.Helper()
	ns, ev, err := Apply(s, a)
	if !errors.Is(err, want) {
		t.Fatalf("%s error = %v, want %v", a.name(), err, want)
	}
	if len(ev) != 0 {
		t.Fatalf("%s was rejected but emitted %d events", a.name(), len(ev))
	}
	if ns.Seq != s.Seq || ns.Phase != s.Phase || len(ns.Claims) != len(s.Claims) {
		t.Fatalf("%s was rejected but mutated state", a.name())
	}
}

// table builds a started game with n players.
func table(t *testing.T, n int, cfg Config) State {
	t.Helper()
	s := NewGame(cfg)
	for i := 0; i < n; i++ {
		s = must(t, s, ActionJoin{Meta: Meta{PlayerID: names[i]}, Name: string(names[i])})
		s = must(t, s, ActionSetReady{Meta: Meta{PlayerID: names[i]}, Ready: true})
	}
	return must(t, s, ActionStartGame{Meta: Meta{PlayerID: names[0]}})
}

func testConfig(seed int64) Config {
	cfg := DefaultConfig(seed)
	cfg.FirstStarter = FirstStarterSeatZero
	return cfg
}

// dealSequential replaces the shuffled deal with the lowest cards in
// canonical order, so a test pool is fully deterministic. The first 45 cards
// of a canonical deck contain no ace, so a Royal Flush claim is always false
// against them, which gives tests a guaranteed-false claim.
func dealSequential(s *State) {
	deck := poker.NewDeck()
	idx := 0
	s.Pool = nil
	s.Owner = map[poker.Card]PlayerID{}
	for i := range s.Players {
		p := &s.Players[i]
		if !p.Status.InPlay() {
			p.Hand = nil
			continue
		}
		p.Hand = append([]poker.Card(nil), deck[idx:idx+p.HandSize]...)
		idx += p.HandSize
		for _, c := range p.Hand {
			s.Owner[c] = p.ID
		}
		s.Pool = append(s.Pool, p.Hand...)
	}
	poker.SortCards(s.Pool)
	s.DeckRemaining = poker.DeckSize - len(s.Pool)
}

// setHands installs explicit hands, e.g. {"A": "8s", "B": "8h"}.
func setHands(s *State, hands map[PlayerID]string) {
	s.Pool = nil
	s.Owner = map[poker.Card]PlayerID{}
	for i := range s.Players {
		p := &s.Players[i]
		spec, ok := hands[p.ID]
		if !ok {
			p.Hand = nil
			continue
		}
		p.Hand = poker.MustParseCards(spec)
		poker.SortCards(p.Hand)
		p.HandSize = len(p.Hand)
		for _, c := range p.Hand {
			s.Owner[c] = p.ID
		}
		s.Pool = append(s.Pool, p.Hand...)
	}
	poker.SortCards(s.Pool)
	s.DeckRemaining = poker.DeckSize - len(s.Pool)
}

func turnPlayer(s State) PlayerID { return s.Players[s.TurnSeat].ID }
func nextPlayer(s State) PlayerID { return s.Players[s.nextInPlaySeat(s.TurnSeat)].ID }
func handSizeOf(s State, id PlayerID) int {
	p, _ := s.player(id)
	return p.HandSize
}
func statusOf(s State, id PlayerID) PlayerStatus {
	p, _ := s.player(id)
	return p.Status
}

// starterLoses drives the current round so the round starter loses: they
// claim a guaranteed-false maximum claim and the next player challenges.
func starterLoses(t *testing.T, s State) State {
	t.Helper()
	dealSequential(&s)
	claimant := turnPlayer(s)
	challenger := nextPlayer(s)
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: claimant}, Claim: poker.NewRoyalFlush(poker.Spades)})
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: challenger}})
	if s.LastShow == nil || s.LastShow.Loser != claimant {
		t.Fatalf("expected %s to lose the SHOW, got %+v", claimant, s.LastShow)
	}
	return s
}

// ---------------------------------------------------------------- lobby

func TestStartGameRequiresPlayerCount(t *testing.T) {
	cfg := testConfig(1)
	s := NewGame(cfg)
	for i := 0; i < 3; i++ {
		s = must(t, s, ActionJoin{Meta: Meta{PlayerID: names[i]}, Name: "x"})
		s = must(t, s, ActionSetReady{Meta: Meta{PlayerID: names[i]}, Ready: true})
	}
	mustFail(t, s, ActionStartGame{Meta: Meta{PlayerID: "A"}}, ErrNotEnoughPlayer)

	s = must(t, s, ActionJoin{Meta: Meta{PlayerID: "D"}, Name: "x"})
	mustFail(t, s, ActionStartGame{Meta: Meta{PlayerID: "A"}}, ErrNotAllReady)
	s = must(t, s, ActionSetReady{Meta: Meta{PlayerID: "D"}, Ready: true})
	s = must(t, s, ActionStartGame{Meta: Meta{PlayerID: "A"}})
	if s.Phase != Claiming {
		t.Fatalf("phase = %s, want CLAIMING", s.Phase)
	}
	mustFail(t, s, ActionJoin{Meta: Meta{PlayerID: "Z"}}, ErrGameInProgress)
}

func TestTableFull(t *testing.T) {
	s := NewGame(testConfig(1))
	for i := 0; i < MaxPlayers; i++ {
		s = must(t, s, ActionJoin{Meta: Meta{PlayerID: names[i]}, Name: "x"})
	}
	mustFail(t, s, ActionJoin{Meta: Meta{PlayerID: "Z"}}, ErrTooManyPlayers)
	mustFail(t, s, ActionJoin{Meta: Meta{PlayerID: "A"}}, ErrAlreadyJoined)
}

// ---------------------------------------------------------------- dealing

func TestInitialDealAndConservation(t *testing.T) {
	for n := MinPlayers; n <= MaxPlayers; n++ {
		s := table(t, n, testConfig(int64(n)))
		for i := range s.Players {
			if s.Players[i].HandSize != StartingHandSize {
				t.Fatalf("%d players: %s hand size %d, want 1", n, s.Players[i].ID, s.Players[i].HandSize)
			}
			if len(s.Players[i].Hand) != StartingHandSize {
				t.Fatalf("%d players: %s dealt %d cards", n, s.Players[i].ID, len(s.Players[i].Hand))
			}
		}
		if s.PoolSize() != n {
			t.Fatalf("%d players: pool %d, want %d", n, s.PoolSize(), n)
		}
		if s.PoolSize()+s.DeckRemaining != poker.DeckSize {
			t.Fatalf("card conservation broken: %d + %d", s.PoolSize(), s.DeckRemaining)
		}
	}
}

// TestFourPlayerRoundOneUsesReducedPool pins the reduced-pool rule at the
// point it was introduced for: the opening round of a minimum table.
func TestFourPlayerRoundOneUsesReducedPool(t *testing.T) {
	s := table(t, 4, testConfig(99))
	if s.PoolSize() != 4 {
		t.Fatalf("pool = %d, want 4", s.PoolSize())
	}
	if s.EvalMode() != poker.ModeReducedPool {
		t.Fatalf("mode = %s, want REDUCED_POOL", s.EvalMode())
	}
	// The pool classifies as something, and that something is a true claim
	// for whoever owns a card in its pattern. So the opening round is
	// playable rather than a forced loss.
	truth := poker.MustClassify(s.Pool)
	if !truth.Legal() {
		t.Fatalf("pool classification %s is not a legal claim", truth)
	}
	must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: truth})

	// Below five cards the evaluated hand is the whole pool, so every
	// in-play player owns a card of it. The pool's own classification is
	// therefore a true claim for every player, and nothing else is true for
	// anyone: claim truth is claimant-independent in reduced mode.
	for i := range s.Players {
		id := s.Players[i].ID
		owns := func(c poker.Card) bool { return s.Owner[c] == id }
		if res := poker.CanSatisfy(s.Pool, owns, truth); !res.Satisfied {
			t.Fatalf("%s should be TRUE for %s over [%s]: %s",
				truth, id, poker.CardsString(s.Pool), res)
		}
		other := poker.NewQuads(poker.Ace)
		if other == truth {
			other = poker.NewHighCard(poker.Three)
		}
		if res := poker.CanSatisfy(s.Pool, owns, other); res.Satisfied {
			t.Fatalf("%s should be FALSE for %s over [%s]: %s",
				other, id, poker.CardsString(s.Pool), res)
		}
	}

	// And the pattern-card helper stays a display aid with no bearing on
	// the verdict.
	if len(poker.PatternCards(s.Pool, truth)) == 0 {
		t.Fatal("PatternCards returned nothing for the pool's own classification")
	}
}

func TestFiveOrMoreCardsUseExactFive(t *testing.T) {
	s := table(t, 5, testConfig(5))
	if s.PoolSize() != 5 || s.EvalMode() != poker.ModeExactFive {
		t.Fatalf("pool %d mode %s, want 5 / EXACT_FIVE", s.PoolSize(), s.EvalMode())
	}
	s = table(t, 6, testConfig(6))
	if s.PoolSize() != 6 || s.EvalMode() != poker.ModeExactFive {
		t.Fatalf("pool %d mode %s, want 6 / EXACT_FIVE", s.PoolSize(), s.EvalMode())
	}
}

func TestPoolFrozenAndCardsReturnedEachRound(t *testing.T) {
	s := table(t, 5, testConfig(11))
	round1 := append([]poker.Card(nil), s.Pool...)

	before := append([]poker.Card(nil), s.Pool...)
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: poker.NewHighCard(poker.Three)})
	if poker.CardsString(s.Pool) != poker.CardsString(before) {
		t.Fatal("pool changed during the round")
	}
	s = must(t, s, ActionDisconnect{Meta: Meta{PlayerID: names[3]}})
	if poker.CardsString(s.Pool) != poker.CardsString(before) {
		t.Fatal("a disconnect changed the frozen pool")
	}
	s = must(t, s, ActionReconnect{Meta: Meta{PlayerID: names[3]}})

	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: turnPlayer(s)}})
	s = must(t, s, ActionAdvanceRound{Meta: Meta{PlayerID: "A"}})

	if s.Round != 2 {
		t.Fatalf("round = %d, want 2", s.Round)
	}
	if s.PoolSize()+s.DeckRemaining != poker.DeckSize {
		t.Fatal("card conservation broken after reshuffle")
	}
	// Round 2 is dealt from a freshly shuffled full deck, so the pool is
	// drawn from all 52 cards again rather than the 47 that were left.
	if len(round1) == 0 {
		t.Fatal("round 1 pool was empty")
	}
}

// ---------------------------------------------------------------- turn rules

func TestStarterCannotCallShow(t *testing.T) {
	s := table(t, 4, testConfig(3))
	mustFail(t, s, ActionCallShow{Meta: Meta{PlayerID: turnPlayer(s)}}, ErrNoStandingClaim)
}

func TestOnlyImmediateNextPlayerCanShow(t *testing.T) {
	s := table(t, 5, testConfig(4))
	claimant := turnPlayer(s)
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: claimant}, Claim: poker.NewPair(poker.Two)})
	eligible := turnPlayer(s)
	for i := range s.Players {
		id := s.Players[i].ID
		if id == eligible {
			continue
		}
		mustFail(t, s, ActionCallShow{Meta: Meta{PlayerID: id}}, ErrNotShowEligible)
	}
	if !s.CanShow(eligible) {
		t.Fatalf("%s should be SHOW eligible", eligible)
	}
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: eligible}})
	if s.Phase != RoundEnd && s.Phase != GameOver {
		t.Fatalf("phase = %s after SHOW", s.Phase)
	}
}

func TestClaimMustBeStrictlyHigher(t *testing.T) {
	s := table(t, 4, testConfig(5))
	first := turnPlayer(s)
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: first}, Claim: poker.NewPair(poker.Eight)})
	second := turnPlayer(s)
	mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: second}, Claim: poker.NewPair(poker.Eight)}, ErrClaimNotHigher)
	mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: second}, Claim: poker.NewPair(poker.Seven)}, ErrClaimNotHigher)
	mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: second}, Claim: poker.NewHighCard(poker.Ace)}, ErrClaimNotHigher)
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: second}, Claim: poker.NewPair(poker.Nine)})
	if len(s.Claims) != 2 {
		t.Fatalf("claims = %d, want 2", len(s.Claims))
	}
}

func TestOneActionPerTurn(t *testing.T) {
	s := table(t, 4, testConfig(6))
	p := turnPlayer(s)
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: p}, Claim: poker.NewPair(poker.Two)})
	mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: p}, Claim: poker.NewPair(poker.Three)}, ErrNotYourTurn)
}

func TestMaxClaimLeavesOnlyShow(t *testing.T) {
	s := table(t, 4, testConfig(7))
	dealSequential(&s)
	claimant := turnPlayer(s)
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: claimant}, Claim: poker.MaxClaim()})
	next := turnPlayer(s)
	if s.CanClaim(next) {
		t.Fatal("no claim can exceed the maximum, so CanClaim must be false")
	}
	if !s.CanShow(next) {
		t.Fatal("SHOW must be available against the maximum claim")
	}
	mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: next}, Claim: poker.MaxClaim()}, ErrClaimNotHigher)
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: next}})
	if s.LastShow.ClaimTrue {
		t.Fatal("Royal Flush of Spades cannot be true against a no-ace pool")
	}
}

func TestImpossibleClaimIsAccepted(t *testing.T) {
	s := table(t, 4, testConfig(8))
	setHands(&s, map[PlayerID]string{"A": "2c", "B": "3d", "C": "4h", "D": "5s"})
	s.TurnSeat = 0
	// No ace is in play, yet the claim must be accepted: bluffing is legal
	// and truth is only tested at SHOW.
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "A"}, Claim: poker.NewQuads(poker.Ace)})
	if len(s.Claims) != 1 {
		t.Fatal("impossible claim should have been recorded")
	}
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: "B"}})
	if s.LastShow.ClaimTrue {
		t.Fatal("Four Aces should be false")
	}
	if s.LastShow.Loser != "A" {
		t.Fatalf("loser = %s, want A", s.LastShow.Loser)
	}
}

func TestMalformedClaimRejected(t *testing.T) {
	s := table(t, 4, testConfig(9))
	p := turnPlayer(s)
	for _, c := range []poker.Claim{
		poker.NewFullHouse(poker.Eight, poker.Eight),
		poker.NewStraight(poker.Four),
		poker.NewFlush(poker.Hearts, poker.Six),
		poker.NewStraightFlush(poker.Spades, poker.Ace),
		poker.NewHighCard(poker.Two),
	} {
		mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: p}, Claim: c}, ErrMalformedClaim)
	}
}

// ---------------------------------------------------------------- SHOW

func TestChallengeTrueClaimLosesForChallenger(t *testing.T) {
	s := table(t, 5, testConfig(12))
	setHands(&s, map[PlayerID]string{"A": "8s", "B": "8h", "C": "Kd", "D": "3c", "E": "Ad"})
	s.TurnSeat = 0
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "A"}, Claim: poker.NewPair(poker.Eight)})
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: "B"}})

	if !s.LastShow.ClaimTrue {
		t.Fatalf("Pair of 8s should be TRUE: %+v", s.LastShow)
	}
	if s.LastShow.Loser != "B" {
		t.Fatalf("loser = %s, want the challenger B", s.LastShow.Loser)
	}
	if handSizeOf(s, "B") != 2 {
		t.Fatalf("B hand size = %d, want 2", handSizeOf(s, "B"))
	}
	for _, id := range []PlayerID{"A", "C", "D", "E"} {
		if handSizeOf(s, id) != 1 {
			t.Fatalf("%s hand size = %d, want 1", id, handSizeOf(s, id))
		}
	}
}

func TestChallengeFalseClaimLosesForClaimant(t *testing.T) {
	s := table(t, 5, testConfig(13))
	setHands(&s, map[PlayerID]string{"A": "Ks", "B": "8h", "C": "8d", "D": "3c", "E": "2d"})
	s.TurnSeat = 0
	// The pool holds a pair of 8s that A does not own, but A's king is in
	// the only five-card selection, so ownership is satisfied and Pair of
	// 8s would be TRUE. This test wants the FALSE branch, so A claims a
	// hand that is not in the pool at all.
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "A"}, Claim: poker.NewTrips(poker.Eight)})
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: "B"}})
	if s.LastShow.ClaimTrue {
		t.Fatalf("Three 8s should be FALSE: %+v", s.LastShow)
	}
	if s.LastShow.Loser != "A" {
		t.Fatalf("loser = %s, want the claimant A", s.LastShow.Loser)
	}
	if handSizeOf(s, "A") != 2 {
		t.Fatalf("A hand size = %d, want 2", handSizeOf(s, "A"))
	}
}

func TestOnlyPreviousClaimIsValidated(t *testing.T) {
	s := table(t, 5, testConfig(14))
	setHands(&s, map[PlayerID]string{"A": "8s", "B": "8h", "C": "Kd", "D": "3c", "E": "Ad"})
	s.TurnSeat = 0
	// A's opening claim is true, but the chain moves on; only the last
	// claim before SHOW is ever evaluated.
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "A"}, Claim: poker.NewPair(poker.Eight)})
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "B"}, Claim: poker.NewTrips(poker.Eight)})
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: "C"}})
	if s.LastShow.Claim != poker.NewTrips(poker.Eight) {
		t.Fatalf("validated %s, want Three 8s", s.LastShow.Claim)
	}
	if s.LastShow.ClaimTrue || s.LastShow.Loser != "B" {
		t.Fatalf("expected B to lose a false Three 8s: %+v", s.LastShow)
	}
	if handSizeOf(s, "A") != 1 {
		t.Fatal("A should not be penalised for an earlier claim")
	}
}

func TestWitnessIsRecordedAndValid(t *testing.T) {
	s := table(t, 6, testConfig(15))
	setHands(&s, map[PlayerID]string{
		"A": "2s", "B": "2h", "C": "Ac", "D": "4d", "E": "4s", "F": "4c",
	})
	s.TurnSeat = 0
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "A"}, Claim: poker.NewTwoPair(poker.Four, poker.Two)})
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: "B"}})
	if !s.LastShow.ClaimTrue {
		t.Fatalf("Two Pair 4s and 2s should be TRUE: %+v", s.LastShow)
	}
	if len(s.LastShow.Witness) != 5 {
		t.Fatalf("witness = %s", poker.CardsString(s.LastShow.Witness))
	}
	if !poker.Satisfies(s.LastShow.Witness, s.LastShow.Claim) {
		t.Fatalf("witness [%s] does not realize the claim", poker.CardsString(s.LastShow.Witness))
	}
}

// ---------------------------------------------------------------- penalties

func TestPenaltyLadderAndElimination(t *testing.T) {
	s := table(t, 4, testConfig(16))
	noop := func(Event) {}
	for want := 2; want <= MaxHandSize; want++ {
		applyPenalty(&s, "A", noop)
		if handSizeOf(s, "A") != want {
			t.Fatalf("A hand size = %d, want %d", handSizeOf(s, "A"), want)
		}
		if statusOf(s, "A") != StatusActive {
			t.Fatalf("A eliminated too early at hand size %d", want)
		}
	}
	// The sixth card can never be dealt.
	applyPenalty(&s, "A", noop)
	if statusOf(s, "A") != StatusEliminated {
		t.Fatalf("A status = %s, want ELIMINATED", statusOf(s, "A"))
	}
	if handSizeOf(s, "A") != MaxHandSize {
		t.Fatalf("A hand size = %d, want it capped at %d", handSizeOf(s, "A"), MaxHandSize)
	}
	for _, id := range []PlayerID{"B", "C", "D"} {
		if handSizeOf(s, id) != 1 {
			t.Fatalf("%s hand size = %d, want 1: only the loser is penalised", id, handSizeOf(s, id))
		}
	}
	// The engine always leaves CLAIMING when it applies a penalty, so
	// mirror that before asserting invariants: a claiming phase whose turn
	// holder has just been eliminated is exactly what I4 forbids.
	s.Phase = RoundEnd
	if err := CheckInvariants(s); err != nil {
		t.Fatalf("invariant broken: %v", err)
	}
}

func TestEliminatedPlayerCannotAct(t *testing.T) {
	s := table(t, 5, testConfig(17))
	s.Players[2].Status = StatusEliminated
	s.TurnSeat = 2
	mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "C"}, Claim: poker.NewPair(poker.Two)}, ErrPlayerNotActive)
	s.TurnSeat = 0
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "A"}, Claim: poker.NewPair(poker.Two)})
	mustFail(t, s, ActionCallShow{Meta: Meta{PlayerID: "C"}}, ErrPlayerNotActive)
}

func TestGameEndsAndRejectsActions(t *testing.T) {
	s := table(t, 4, testConfig(18))
	// Put three players one loss from elimination and drive them out.
	for _, id := range []PlayerID{"A", "B", "C"} {
		p, _ := s.player(id)
		p.HandSize = MaxHandSize
	}
	noop := func(Event) {}
	applyPenalty(&s, "A", noop)
	applyPenalty(&s, "B", noop)
	applyPenalty(&s, "C", noop)
	if s.InPlayCount() != 1 {
		t.Fatalf("in play = %d, want 1", s.InPlayCount())
	}
	_ = endGame(&s, noop)
	if s.Phase != GameOver || s.Winner != "D" {
		t.Fatalf("phase %s winner %s, want GAME_OVER / D", s.Phase, s.Winner)
	}
	mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "D"}, Claim: poker.NewPair(poker.Two)}, ErrGameOver)
	mustFail(t, s, ActionCallShow{Meta: Meta{PlayerID: "D"}}, ErrGameOver)
	mustFail(t, s, ActionAdvanceRound{Meta: Meta{PlayerID: "D"}}, ErrGameOver)
	if err := CheckInvariants(s); err != nil {
		t.Fatalf("invariant broken: %v", err)
	}
}

// ---------------------------------------------------------------- rotation

// TestStarterRotationMatchesSpec reproduces the rotation sequence from the
// rules: A B C, C eliminated, then D A B D.
func TestStarterRotationMatchesSpec(t *testing.T) {
	s := table(t, 4, testConfig(21))
	var starters []int

	for round := 1; round <= 7; round++ {
		starters = append(starters, s.StarterSeat)
		if round == 3 {
			// Put C one loss from elimination so the round-3 loss removes
			// them, exactly as the specification's example does.
			s.Players[2].HandSize = MaxHandSize
		}
		s = starterLoses(t, s)
		if round == 3 && statusOf(s, "C") != StatusEliminated {
			t.Fatalf("C should be eliminated at the end of round 3, status %s", statusOf(s, "C"))
		}
		if s.Phase == GameOver {
			t.Fatalf("game ended early at round %d", round)
		}
		s = must(t, s, ActionAdvanceRound{Meta: Meta{PlayerID: "A"}})
	}

	want := []int{0, 1, 2, 3, 0, 1, 3}
	for i := range want {
		if starters[i] != want[i] {
			t.Fatalf("round %d starter seat = %d, want %d (full sequence %v, want %v)",
				i+1, starters[i], want[i], starters, want)
		}
	}
}

// TestStarterRotationIndependentOfOutcome runs the same opening from the
// same seed twice: once where the claimant loses and once where the
// challenger loses. With no elimination in either line, the starter
// sequence must be identical.
func TestStarterRotationIndependentOfOutcome(t *testing.T) {
	run := func(claimantLoses bool) []int {
		s := table(t, 4, testConfig(22))
		var starters []int
		for round := 1; round <= 4; round++ {
			starters = append(starters, s.StarterSeat)
			dealSequential(&s)
			claimant := turnPlayer(s)
			challenger := nextPlayer(s)
			claim := poker.NewRoyalFlush(poker.Spades) // always false
			if !claimantLoses {
				// The reduced or exact classification of a sequential deal
				// is always a true claim for a pattern owner, so use it.
				truth := poker.MustClassify(s.Pool[:poker.HandSize(s.PoolSize())])
				owns := func(c poker.Card) bool { return s.Owner[c] == claimant }
				if poker.CanSatisfy(s.Pool, owns, truth).Satisfied {
					claim = truth
				}
			}
			s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: claimant}, Claim: claim})
			s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: challenger}})
			wantLoser := claimant
			if !claimantLoses && s.LastShow.ClaimTrue {
				wantLoser = challenger
			}
			if s.LastShow.Loser != wantLoser {
				t.Fatalf("round %d loser = %s, want %s", round, s.LastShow.Loser, wantLoser)
			}
			s = must(t, s, ActionAdvanceRound{Meta: Meta{PlayerID: "A"}})
		}
		return starters
	}
	a := run(true)
	b := run(false)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("rotation depends on the SHOW outcome: %v vs %v", a, b)
		}
	}
	if want := []int{0, 1, 2, 3}; fmt.Sprint(a) != fmt.Sprint(want) {
		t.Fatalf("starter sequence = %v, want %v", a, want)
	}
}

// ---------------------------------------------------------------- views

func TestViewHidesOtherPlayersCards(t *testing.T) {
	s := table(t, 5, testConfig(31))
	for i := range s.Players {
		viewer := s.Players[i].ID
		v := View(s, viewer)
		if leaked := LeakedCards(s, viewer); len(leaked) != 0 {
			t.Fatalf("%s can see %s", viewer, poker.CardsString(leaked))
		}
		own := 0
		for _, p := range v.Players {
			if p.ID == viewer {
				own = len(p.Cards)
			} else if len(p.Cards) != 0 {
				t.Fatalf("%s sees %s's cards", viewer, p.ID)
			}
			if p.HandSize == 0 {
				t.Fatalf("hand sizes are public and must be present")
			}
		}
		if own != 1 {
			t.Fatalf("%s sees %d of their own cards, want 1", viewer, own)
		}
		if v.PoolSize != 5 {
			t.Fatalf("pool size = %d, want 5", v.PoolSize)
		}
	}
}

func TestViewRevealsEveryHandAfterShow(t *testing.T) {
	s := table(t, 5, testConfig(32))
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: poker.NewPair(poker.Two)})
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: turnPlayer(s)}})
	for i := range s.Players {
		v := View(s, s.Players[i].ID)
		for _, p := range v.Players {
			if len(p.Cards) == 0 {
				t.Fatalf("%s's cards are still hidden after SHOW", p.ID)
			}
		}
		if v.LastShow == nil {
			t.Fatal("the result should be visible after SHOW")
		}
	}
	// And hidden again next round.
	s = must(t, s, ActionAdvanceRound{Meta: Meta{PlayerID: "A"}})
	for i := range s.Players {
		if leaked := LeakedCards(s, s.Players[i].ID); len(leaked) != 0 {
			t.Fatalf("cards leaked in round %d: %s", s.Round, poker.CardsString(leaked))
		}
	}
}

func TestViewCarriesNoDeckOrOwnership(t *testing.T) {
	s := table(t, 5, testConfig(33))
	v := View(s, "A")
	// The view type has no field for the deck, the seed, the pool contents
	// or the ownership map. Only the pool's size is public.
	if v.PoolSize != len(s.Pool) {
		t.Fatalf("pool size = %d, want %d", v.PoolSize, len(s.Pool))
	}
	if v.StandingOrdinal != -1 {
		t.Fatalf("standing ordinal = %d with no claim yet", v.StandingOrdinal)
	}
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: poker.NewPair(poker.Eight)})
	v = View(s, "A")
	o, _ := poker.NewPair(poker.Eight).Ordinal()
	if v.StandingOrdinal != o {
		t.Fatalf("standing ordinal = %d, want %d", v.StandingOrdinal, o)
	}
}

func TestViewEligibilityFlags(t *testing.T) {
	s := table(t, 4, testConfig(34))
	claimant := turnPlayer(s)
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: claimant}, Claim: poker.NewPair(poker.Two)})
	eligible := turnPlayer(s)
	for i := range s.Players {
		id := s.Players[i].ID
		v := View(s, id)
		if v.CanShow != (id == eligible) {
			t.Fatalf("%s CanShow = %v, want %v", id, v.CanShow, id == eligible)
		}
		if v.YourTurn != (id == eligible) {
			t.Fatalf("%s YourTurn = %v", id, v.YourTurn)
		}
	}
}

// ---------------------------------------------------------------- timers

func TestTimeoutAutoShow(t *testing.T) {
	cfg := testConfig(41)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s := NewGame(cfg)
	for i := 0; i < 4; i++ {
		s = must(t, s, ActionJoin{Meta: Meta{PlayerID: names[i], Now: now}})
		s = must(t, s, ActionSetReady{Meta: Meta{PlayerID: names[i], Now: now}, Ready: true})
	}
	s = must(t, s, ActionStartGame{Meta: Meta{PlayerID: "A", Now: now}})
	if s.TurnDeadline.IsZero() {
		t.Fatal("turn deadline should be set when a clock is supplied")
	}
	// Before the deadline nothing happens.
	s2, ev, err := Apply(s, ActionTick{Meta: Meta{Now: now.Add(10 * time.Second)}})
	if err != nil || len(ev) != 0 || s2.Phase != Claiming {
		t.Fatalf("early tick changed something: %d events", len(ev))
	}
	// The starter times out with no standing claim, so the minimum claim is
	// submitted on their behalf.
	s = must(t, s, ActionTick{Meta: Meta{Now: now.Add(31 * time.Second)}})
	if len(s.Claims) != 1 || s.Claims[0].Claim != poker.MinClaim() {
		t.Fatalf("claims after starter timeout: %+v", s.Claims)
	}
	// Now the next player times out with a standing claim, so SHOW fires.
	s = must(t, s, ActionTick{Meta: Meta{Now: now.Add(70 * time.Second)}})
	if s.LastShow == nil {
		t.Fatal("a timeout with a standing claim should auto-SHOW")
	}
}

func TestTimeoutForfeitRound(t *testing.T) {
	cfg := testConfig(42)
	cfg.Timeout = TimeoutForfeitRound
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s := NewGame(cfg)
	for i := 0; i < 4; i++ {
		s = must(t, s, ActionJoin{Meta: Meta{PlayerID: names[i], Now: now}})
		s = must(t, s, ActionSetReady{Meta: Meta{PlayerID: names[i], Now: now}, Ready: true})
	}
	s = must(t, s, ActionStartGame{Meta: Meta{PlayerID: "A", Now: now}})
	victim := turnPlayer(s)
	s = must(t, s, ActionTick{Meta: Meta{Now: now.Add(31 * time.Second)}})
	if handSizeOf(s, victim) != 2 {
		t.Fatalf("%s hand size = %d, want 2 after forfeiting", victim, handSizeOf(s, victim))
	}
	if s.Phase != RoundEnd {
		t.Fatalf("phase = %s, want ROUND_END", s.Phase)
	}
}

func TestRoundEndTickAdvancesRound(t *testing.T) {
	cfg := testConfig(43)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s := NewGame(cfg)
	for i := 0; i < 4; i++ {
		s = must(t, s, ActionJoin{Meta: Meta{PlayerID: names[i], Now: now}})
		s = must(t, s, ActionSetReady{Meta: Meta{PlayerID: names[i], Now: now}, Ready: true})
	}
	s = must(t, s, ActionStartGame{Meta: Meta{PlayerID: "A", Now: now}})
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s), Now: now}, Claim: poker.NewPair(poker.Two)})
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: turnPlayer(s), Now: now}})
	if s.Phase != RoundEnd {
		t.Fatalf("phase = %s, want ROUND_END", s.Phase)
	}
	s = must(t, s, ActionTick{Meta: Meta{Now: now.Add(10 * time.Second)}})
	if s.Phase != Claiming || s.Round != 2 {
		t.Fatalf("phase %s round %d, want CLAIMING round 2", s.Phase, s.Round)
	}
}

// ---------------------------------------------------------------- presence

func TestDisconnectEliminatesAtRoundEnd(t *testing.T) {
	s := table(t, 5, testConfig(51))
	s = must(t, s, ActionDisconnect{Meta: Meta{PlayerID: "E"}})
	if statusOf(s, "E") != StatusDisconnected {
		t.Fatalf("E status = %s", statusOf(s, "E"))
	}
	// Still dealt into this round: the pool is frozen.
	if len(s.Pool) != 5 {
		t.Fatalf("pool = %d, want 5", len(s.Pool))
	}
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: poker.NewPair(poker.Two)})
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: turnPlayer(s)}})
	s = must(t, s, ActionAdvanceRound{Meta: Meta{PlayerID: "A"}})
	if statusOf(s, "E") != StatusEliminated {
		t.Fatalf("E status = %s, want ELIMINATED at round end", statusOf(s, "E"))
	}
	// E is dealt out entirely; the pool is exactly the in-play hand sizes,
	// which now include the +1 the round's loser picked up.
	if e, _ := s.player("E"); len(e.Hand) != 0 {
		t.Fatalf("E still holds %d cards", len(e.Hand))
	}
	want := 0
	for i := range s.Players {
		if s.Players[i].Status.InPlay() {
			want += s.Players[i].HandSize
		}
	}
	if len(s.Pool) != want || want != 5 {
		t.Fatalf("pool = %d, want %d (four players, one on two cards)", len(s.Pool), want)
	}
}

func TestReconnectRestoresPlay(t *testing.T) {
	s := table(t, 5, testConfig(52))
	before := View(s, "E")
	s = must(t, s, ActionDisconnect{Meta: Meta{PlayerID: "E"}})
	s = must(t, s, ActionReconnect{Meta: Meta{PlayerID: "E"}})
	if statusOf(s, "E") != StatusActive {
		t.Fatalf("E status = %s, want ACTIVE", statusOf(s, "E"))
	}
	after := View(s, "E")
	if poker.CardsString(before.Players[4].Cards) != poker.CardsString(after.Players[4].Cards) {
		t.Fatal("reconnect changed the player's own hand")
	}
	if leaked := LeakedCards(s, "E"); len(leaked) != 0 {
		t.Fatalf("reconnect leaked %s", poker.CardsString(leaked))
	}
}

// ------------------------------------------------- property / simulation

// simulate plays one complete game with simple bots, asserting every
// invariant after every action and checking for information leaks
// throughout.
func simulate(t *testing.T, players int, seed int64) (rounds int, actions int) {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	cfg := DefaultConfig(seed)
	s := NewGame(cfg)
	for i := 0; i < players; i++ {
		s, _, _ = Apply(s, ActionJoin{Meta: Meta{PlayerID: names[i]}, Name: string(names[i])})
		s, _, _ = Apply(s, ActionSetReady{Meta: Meta{PlayerID: names[i]}, Ready: true})
	}
	var err error
	s, _, err = Apply(s, ActionStartGame{Meta: Meta{PlayerID: names[0]}})
	if err != nil {
		t.Fatalf("seed %d: start failed: %v", seed, err)
	}

	const maxActions = 200000
	for s.Phase != GameOver {
		actions++
		if actions > maxActions {
			t.Fatalf("seed %d: game did not terminate in %d actions", seed, maxActions)
		}
		var act Action
		switch s.Phase {
		case RoundEnd:
			rounds++
			act = ActionAdvanceRound{Meta: Meta{PlayerID: names[0]}}
		case Claiming:
			me := turnPlayer(s)
			legal := LegalClaims(s)
			if len(s.Claims) > 0 && (r.Intn(100) < 45 || len(legal) == 0) {
				act = ActionCallShow{Meta: Meta{PlayerID: me}}
			} else {
				// Raise modestly so rounds explore the ladder rather than
				// jumping straight to the ceiling.
				window := 25
				if len(legal) < window {
					window = len(legal)
				}
				act = ActionMakeClaim{Meta: Meta{PlayerID: me}, Claim: legal[r.Intn(window)]}
			}
		default:
			t.Fatalf("seed %d: unexpected phase %s", seed, s.Phase)
		}

		ns, _, err := Apply(s, act)
		if err != nil {
			t.Fatalf("seed %d: %s rejected: %v", seed, act.name(), err)
		}
		s = ns
		if err := CheckInvariants(s); err != nil {
			t.Fatalf("seed %d: invariant broken after %s: %v", seed, act.name(), err)
		}
		for i := range s.Players {
			if leaked := LeakedCards(s, s.Players[i].ID); len(leaked) != 0 {
				t.Fatalf("seed %d: %s can see %s", seed, s.Players[i].ID, poker.CardsString(leaked))
			}
		}
	}
	if s.Winner == "" {
		t.Fatalf("seed %d: game over with no winner", seed)
	}
	return rounds, actions
}

func TestSimulatedGamesHoldInvariants(t *testing.T) {
	totalRounds, totalActions, games := 0, 0, 0
	for players := MinPlayers; players <= MaxPlayers; players++ {
		for seed := int64(0); seed < 12; seed++ {
			rounds, actions := simulate(t, players, seed*100+int64(players))
			totalRounds += rounds
			totalActions += actions
			games++
		}
	}
	t.Logf("%d games, %d rounds, %d actions, all invariants held", games, totalRounds, totalActions)
}

func TestSimulatedGameIsReproducible(t *testing.T) {
	play := func() State {
		cfg := DefaultConfig(4242)
		s := NewGame(cfg)
		for i := 0; i < 6; i++ {
			s, _, _ = Apply(s, ActionJoin{Meta: Meta{PlayerID: names[i]}})
			s, _, _ = Apply(s, ActionSetReady{Meta: Meta{PlayerID: names[i]}, Ready: true})
		}
		s, _, _ = Apply(s, ActionStartGame{Meta: Meta{PlayerID: "A"}})
		return s
	}
	a, b := play(), play()
	if poker.CardsString(a.Pool) != poker.CardsString(b.Pool) {
		t.Fatalf("same seed dealt different pools:\n%s\n%s",
			poker.CardsString(a.Pool), poker.CardsString(b.Pool))
	}
	if a.StarterSeat != b.StarterSeat {
		t.Fatal("same seed chose different first starters")
	}
}

func TestApplyDoesNotMutateInput(t *testing.T) {
	s := table(t, 5, testConfig(61))
	snapshotPool := poker.CardsString(s.Pool)
	snapshotClaims := len(s.Claims)
	snapshotSeq := s.Seq
	_, _, err := Apply(s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: poker.NewPair(poker.Eight)})
	if err != nil {
		t.Fatal(err)
	}
	if poker.CardsString(s.Pool) != snapshotPool || len(s.Claims) != snapshotClaims || s.Seq != snapshotSeq {
		t.Fatal("Apply mutated the state it was given")
	}
}

func TestEventSequenceIsMonotonic(t *testing.T) {
	s := table(t, 4, testConfig(62))
	last := s.Seq
	for _, act := range []Action{
		ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: poker.NewPair(poker.Two)},
	} {
		ns, events, err := Apply(s, act)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Seq <= last {
				t.Fatalf("event %s has seq %d, not above %d", e.Type, e.Seq, last)
			}
			last = e.Seq
		}
		s = ns
	}
	if s.Seq != last {
		t.Fatalf("state seq %d, last event seq %d", s.Seq, last)
	}
}

// TestApplyIsSafeForConcurrentUse gives the race detector something real to
// look at. Apply is documented as pure, so calling it on the same state from
// many goroutines at once must be safe and must produce identical results.
// If clone() ever shared a backing array with its input, this is where it
// would surface.
func TestApplyIsSafeForConcurrentUse(t *testing.T) {
	s := table(t, 6, testConfig(71))
	act := ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: poker.NewPair(poker.Eight)}

	const workers = 16
	results := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ns, ev, err := Apply(s, act)
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = CheckInvariants(ns)
			results[i] = fmt.Sprintf("seq=%d phase=%s pool=[%s] claims=%d events=%d",
				ns.Seq, ns.Phase, poker.CardsString(ns.Pool), len(ns.Claims), len(ev))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	for i := 1; i < workers; i++ {
		if results[i] != results[0] {
			t.Fatalf("concurrent Apply diverged:\n  %s\n  %s", results[0], results[i])
		}
	}
	if len(s.Claims) != 0 {
		t.Fatal("Apply mutated the shared input state")
	}
}

package game

import (
	"testing"
	"time"

	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// This file is the pre-transport audit. Everything here is a property the
// WebSocket layer will lean on, so it is asserted against the engine before
// any transport exists.

// ---------------------------------------------------- locked rule: ownership

// TestOwnershipRuleIsUniformAcrossModes is the engine-level pin for the
// locked ownership rule: own a card of the evaluated hand, pattern card or
// not, at every pool size.
func TestOwnershipRuleIsUniformAcrossModes(t *testing.T) {
	// Reduced pool. A owns only the king; the pair belongs to B and C.
	s := table(t, 4, testConfig(801))
	s = InjectHands(s, map[PlayerID]string{"A": "Ks", "B": "8h", "C": "8d", "D": "3c"})
	s.TurnSeat = 0
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "A"}, Claim: poker.NewPair(poker.Eight)})
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: "B"}})
	if !s.LastShow.ClaimTrue {
		t.Fatalf("reduced pool: A owns K-s in the evaluated hand, so Pair of 8s is TRUE: %+v", s.LastShow)
	}
	if s.LastShow.Loser != "B" {
		t.Fatalf("challenger should lose, got %s", s.LastShow.Loser)
	}

	// Exactly five cards, same rule: A owns only the kicker.
	q := table(t, 5, testConfig(802))
	q = InjectHands(q, map[PlayerID]string{"A": "Ks", "B": "8h", "C": "8d", "D": "8s", "E": "8c"})
	q.TurnSeat = 0
	q = must(t, q, ActionMakeClaim{Meta: Meta{PlayerID: "A"}, Claim: poker.NewQuads(poker.Eight)})
	q = must(t, q, ActionCallShow{Meta: Meta{PlayerID: "B"}})
	if !q.LastShow.ClaimTrue {
		t.Fatalf("five cards: A owns the kicker, so Four 8s is TRUE: %+v", q.LastShow)
	}

	// Ownership still bites where the selection can exclude the claimant.
	f := table(t, 6, testConfig(803))
	f = InjectHands(f, map[PlayerID]string{
		"A": "2c", "B": "7s", "C": "7h", "D": "7d", "E": "8s", "F": "8h"})
	f.TurnSeat = 0
	f = must(t, f, ActionMakeClaim{Meta: Meta{PlayerID: "A"}, Claim: poker.NewFullHouse(poker.Seven, poker.Eight)})
	f = must(t, f, ActionCallShow{Meta: Meta{PlayerID: "B"}})
	if f.LastShow.ClaimTrue {
		t.Fatalf("the only full house excludes A's 2-c, so this is FALSE: %+v", f.LastShow)
	}
	if f.LastShow.Loser != "A" {
		t.Fatalf("claimant should lose, got %s", f.LastShow.Loser)
	}
}

// TestClaimSpaceIsFixedAt367 pins the locked claim space, including the four
// reduced-pool High Card claims and the exclusion of High Card 2.
func TestClaimSpaceIsFixedAt367(t *testing.T) {
	if poker.ClaimSpaceSize != 367 {
		t.Fatalf("claim space is %d, must be 367", poker.ClaimSpaceSize)
	}
	if n := len(poker.AllClaims()); n != 367 {
		t.Fatalf("AllClaims returned %d", n)
	}
	for _, r := range []poker.Rank{poker.Three, poker.Four, poker.Five, poker.Six} {
		if !poker.NewHighCard(r).Legal() {
			t.Errorf("High Card %s must be in the claim space", r)
		}
	}
	if poker.NewHighCard(poker.Two).Legal() {
		t.Error("High Card 2 must stay excluded")
	}
	// Every reachable reduced-pool classification must be claimable, which
	// is the whole reason the space is 367.
	for width := 2; width <= 4; width++ {
		found := false
		for _, c := range poker.AllClaims() {
			if c.Category == poker.HighCard && poker.RealizableAtWidth(c, width) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no High Card claim is realizable at width %d", width)
		}
	}
}

// ---------------------------------------------------- presence policies

// TestImmediateDisconnectKeepsStateConsistent exercises the non-default
// presence policy, which no existing test covered.
func TestImmediateDisconnectKeepsStateConsistent(t *testing.T) {
	cfg := testConfig(811)
	cfg.Disconnect = DisconnectEliminateImmediately

	// Case 1: the disconnecting player is the current turn holder. Mid-round
	// the policy defers to round end, so they stay in play as DISCONNECTED
	// and keep the turn, which the timer then resolves. What must never
	// happen is an eliminated player holding the turn.
	s := table(t, 5, cfg)
	victim := turnPlayer(s)
	s = must(t, s, ActionDisconnect{Meta: Meta{PlayerID: victim}})
	if !s.Players[s.TurnSeat].Status.InPlay() {
		t.Fatalf("turn seat %d holds a player who is not in play", s.TurnSeat)
	}
	if statusOf(s, victim) != StatusDisconnected {
		t.Fatalf("%s status = %s mid-round, want DISCONNECTED", victim, statusOf(s, victim))
	}
	if len(s.Pool) != 5 {
		t.Fatalf("pool = %d; a disconnect must not change the frozen pool", len(s.Pool))
	}
	// The timer still fires against them, so nothing stalls.
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s.TurnDeadline = now
	s = must(t, s, ActionTick{Meta: Meta{Now: now.Add(time.Second)}})
	if len(s.Claims) == 0 && s.Phase == Claiming {
		t.Fatal("a timed-out disconnected player blocked the round")
	}

	// Case 1b: outside an active round the policy eliminates immediately.
	out := table(t, 5, cfg)
	out = must(t, out, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(out)}, Claim: poker.NewPair(poker.Two)})
	out = must(t, out, ActionCallShow{Meta: Meta{PlayerID: turnPlayer(out)}})
	if out.Phase != RoundEnd {
		t.Fatalf("phase = %s, want ROUND_END", out.Phase)
	}
	out = must(t, out, ActionDisconnect{Meta: Meta{PlayerID: "E"}})
	if statusOf(out, "E") != StatusEliminated {
		t.Fatalf("E status = %s during ROUND_END, want ELIMINATED", statusOf(out, "E"))
	}

	// Case 2: the disconnecting player made the standing claim.
	s2 := table(t, 5, cfg)
	claimant := turnPlayer(s2)
	s2 = must(t, s2, ActionMakeClaim{Meta: Meta{PlayerID: claimant}, Claim: poker.NewPair(poker.Two)})
	challenger := turnPlayer(s2)
	s2 = must(t, s2, ActionDisconnect{Meta: Meta{PlayerID: claimant}})
	// Whatever the policy does, the round must still be resolvable and the
	// penalty must land on a player the engine can still account for.
	s2 = must(t, s2, ActionCallShow{Meta: Meta{PlayerID: challenger}})
	if s2.LastShow == nil {
		t.Fatal("SHOW did not resolve after the claimant disconnected")
	}
	loser, ok := s2.player(s2.LastShow.Loser)
	if !ok {
		t.Fatalf("loser %s is not a seated player", s2.LastShow.Loser)
	}
	if loser.HandSize < StartingHandSize || loser.HandSize > MaxHandSize {
		t.Fatalf("loser %s ended on hand size %d", loser.ID, loser.HandSize)
	}
	// And the deferred elimination lands when the round advances.
	s2 = must(t, s2, ActionAdvanceRound{Meta: Meta{PlayerID: "A"}})
	if statusOf(s2, claimant) != StatusEliminated {
		t.Fatalf("%s status = %s after the round advanced, want ELIMINATED",
			claimant, statusOf(s2, claimant))
	}
}

// ---------------------------------------------------- hostile input

func TestJoinRejectsBadPlayerIDs(t *testing.T) {
	s := NewGame(testConfig(821))
	if _, _, err := Apply(s, ActionJoin{Meta: Meta{PlayerID: ""}, Name: "nobody"}); err != ErrInvalidPlayerID {
		t.Fatalf("empty player id error = %v, want ErrInvalidPlayerID", err)
	}
	long := PlayerID(make([]byte, MaxPlayerIDLength+1))
	if _, _, err := Apply(s, ActionJoin{Meta: Meta{PlayerID: long}}); err != ErrInvalidPlayerID {
		t.Fatalf("oversized player id error = %v, want ErrInvalidPlayerID", err)
	}
	s = must(t, s, ActionJoin{Meta: Meta{PlayerID: "A"}})
	mustFail(t, s, ActionJoin{Meta: Meta{PlayerID: "A"}}, ErrAlreadyJoined)
}

// TestHostileClaimsCannotPanic feeds the reducer claims that are outside the
// claim space, including values that would panic poker.Compare if they ever
// reached it. Claim validation must run before any comparison.
func TestHostileClaimsCannotPanic(t *testing.T) {
	s := table(t, 4, testConfig(822))
	who := turnPlayer(s)
	hostile := []poker.Claim{
		{Category: poker.Category(200), Primary: 99, Secondary: 99, Suit: 9, HasSuit: true},
		{Category: poker.OnePair, Primary: 0},
		{Category: poker.OnePair, Primary: 255},
		{Category: poker.HighCard, Primary: poker.Two},
		{Category: poker.RoyalFlush},
		{Category: poker.Flush, Primary: poker.Six, Suit: poker.Hearts, HasSuit: true},
		{Category: poker.FullHouse, Primary: poker.Eight, Secondary: poker.Eight},
	}
	for _, c := range hostile {
		mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: who}, Claim: c}, ErrMalformedClaim)
	}
	// And with a standing claim in place, so the comparison path is live.
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: who}, Claim: poker.NewPair(poker.Eight)})
	next := turnPlayer(s)
	for _, c := range hostile {
		mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: next}, Claim: c}, ErrMalformedClaim)
	}
}

func TestActionsFromNonMembersRejected(t *testing.T) {
	s := table(t, 4, testConfig(823))
	mustFail(t, s, ActionMakeClaim{Meta: Meta{PlayerID: "Z"}, Claim: poker.NewPair(poker.Two)}, ErrUnknownPlayer)
	// Identity is checked before eligibility, so a non-member is reported as
	// unknown rather than being told anything about the round.
	mustFail(t, s, ActionCallShow{Meta: Meta{PlayerID: "Z"}}, ErrUnknownPlayer)
	if _, _, err := Apply(s, nil); err != ErrUnknownAction {
		t.Fatalf("nil action error = %v", err)
	}
}

// ---------------------------------------------------- event projection

// TestEventProjectionStripsOtherHands is the property the transport depends
// on. EvCardsDealt carries every player's hand so the engine stays a pure
// value, so the projection must be the only way events reach a client.
func TestEventProjectionStripsOtherHands(t *testing.T) {
	s := NewGame(testConfig(831))
	for i := 0; i < 5; i++ {
		s = must(t, s, ActionJoin{Meta: Meta{PlayerID: names[i]}})
		s = must(t, s, ActionSetReady{Meta: Meta{PlayerID: names[i]}, Ready: true})
	}
	s, events, err := Apply(s, ActionStartGame{Meta: Meta{PlayerID: "A"}})
	if err != nil {
		t.Fatal(err)
	}

	sawDeal := false
	for _, e := range events {
		if e.Type != EvCardsDealt {
			continue
		}
		sawDeal = true
		if len(e.Hands) != 5 {
			t.Fatalf("raw deal event carries %d hands, want 5", len(e.Hands))
		}
		for i := range s.Players {
			viewer := s.Players[i].ID
			p := ProjectEvent(e, viewer)
			if len(p.Hands) != 1 {
				t.Fatalf("projected deal for %s carries %d hands, want 1", viewer, len(p.Hands))
			}
			if _, ok := p.Hands[viewer]; !ok {
				t.Fatalf("projected deal for %s does not contain their own hand", viewer)
			}
			// And the projection must not alias the engine's map.
			p.Hands[viewer] = nil
			if len(e.Hands[viewer]) == 0 {
				t.Fatal("ProjectEvent aliased the engine's hand slice")
			}
		}
	}
	if !sawDeal {
		t.Fatal("no CardsDealt event was emitted")
	}

	// A reveal is public, so projection must leave it intact.
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: poker.NewPair(poker.Two)})
	s2, revealEvents, err := Apply(s, ActionCallShow{Meta: Meta{PlayerID: turnPlayer(s)}})
	if err != nil {
		t.Fatal(err)
	}
	_ = s2
	sawReveal := false
	for _, e := range revealEvents {
		if e.Type != EvCardsRevealed {
			continue
		}
		sawReveal = true
		p := ProjectEvent(e, "A")
		if len(p.Hands) != len(e.Hands) {
			t.Fatalf("reveal projection dropped hands: %d of %d", len(p.Hands), len(e.Hands))
		}
	}
	if !sawReveal {
		t.Fatal("no CardsRevealed event was emitted")
	}
}

// ---------------------------------------------------- view consistency

// TestViewReportsNoTurnOutsideClaiming stops the client from rendering an
// active turn during the reveal window or after the game ends.
func TestViewReportsNoTurnOutsideClaiming(t *testing.T) {
	s := table(t, 4, testConfig(841))
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: poker.NewPair(poker.Two)})
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: turnPlayer(s)}})
	if s.Phase != RoundEnd {
		t.Fatalf("phase = %s, want ROUND_END", s.Phase)
	}
	for i := range s.Players {
		v := View(s, s.Players[i].ID)
		if v.YourTurn {
			t.Fatalf("%s sees YourTurn during %s", s.Players[i].ID, s.Phase)
		}
		if v.TurnPlayer != "" {
			t.Fatalf("view reports turn player %s during %s", v.TurnPlayer, s.Phase)
		}
		if v.CanClaim || v.CanShow {
			t.Fatalf("%s sees an action available during %s", s.Players[i].ID, s.Phase)
		}
	}
}

// TestViewLegalRangeMatchesEngine checks that the ordinal floor a client
// filters against agrees with what the engine will actually accept.
func TestViewLegalRangeMatchesEngine(t *testing.T) {
	s := table(t, 4, testConfig(842))
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s)}, Claim: poker.NewTrips(poker.Nine)})
	who := turnPlayer(s)
	v := View(s, who)
	all := poker.AllClaims()
	for i, c := range all {
		accepted := true
		if _, _, err := Apply(s, ActionMakeClaim{Meta: Meta{PlayerID: who}, Claim: c}); err != nil {
			accepted = false
		}
		clientWouldOffer := i > v.StandingOrdinal
		if accepted != clientWouldOffer {
			t.Fatalf("%s (ordinal %d, floor %d): engine accepts=%v, client offers=%v",
				c, i, v.StandingOrdinal, accepted, clientWouldOffer)
		}
	}
}

// TestDeadlinesAreAbsoluteAndMonotonic matters because clients render a
// countdown from them.
func TestDeadlinesAreAbsoluteAndMonotonic(t *testing.T) {
	cfg := testConfig(843)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s := NewGame(cfg)
	for i := 0; i < 4; i++ {
		s = must(t, s, ActionJoin{Meta: Meta{PlayerID: names[i], Now: now}})
		s = must(t, s, ActionSetReady{Meta: Meta{PlayerID: names[i], Now: now}, Ready: true})
	}
	s = must(t, s, ActionStartGame{Meta: Meta{PlayerID: "A", Now: now}})
	if got := s.TurnDeadline.Sub(now); got != cfg.TurnDuration {
		t.Fatalf("turn deadline is %s after now, want %s", got, cfg.TurnDuration)
	}
	later := now.Add(5 * time.Second)
	s = must(t, s, ActionMakeClaim{Meta: Meta{PlayerID: turnPlayer(s), Now: later}, Claim: poker.NewPair(poker.Two)})
	if !s.TurnDeadline.After(now.Add(cfg.TurnDuration)) {
		t.Fatal("the next turn's deadline did not move forward")
	}
	s = must(t, s, ActionCallShow{Meta: Meta{PlayerID: turnPlayer(s), Now: later}})
	if !s.TurnDeadline.IsZero() {
		t.Fatal("turn deadline must be cleared once claiming ends")
	}
	if s.RoundEndDeadline.IsZero() {
		t.Fatal("round-end deadline must be set for the reveal window")
	}
}

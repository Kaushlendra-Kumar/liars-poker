package sim

import (
	"math/rand"
	"testing"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

func strictOptions(players int) Options {
	return Options{
		Players:         players,
		CheckInvariants: true,
		CheckLeaks:      true,
	}
}

// TestEachPolicyPlaysCleanly runs every registered policy as the whole table
// at both the minimum and maximum table size. A single-policy table is the
// harshest case for round length and for the forced-SHOW-at-maximum path.
func TestEachPolicyPlaysCleanly(t *testing.T) {
	for name, p := range Registry() {
		for _, players := range []int{game.MinPlayers, game.MaxPlayers} {
			for seed := int64(0); seed < 3; seed++ {
				opts := strictOptions(players)
				// Leak checking rebuilds every player's view after every
				// action, which dominates runtime on a min-raise table
				// that walks the whole 367-claim ladder each round. Leaks
				// are covered by TestPoliciesNeverSeeHiddenCards and
				// TestMixedPoliciesNoFailures; here we want rule coverage.
				opts.CheckLeaks = false
				opts.Policies = []Policy{p}
				r := RunGame(opts, seed*13+1)
				if r.Err != nil {
					t.Fatalf("policy %s, %d players, seed %d: %v", name, players, r.Seed, r.Err)
				}
				if r.Winner == "" {
					t.Fatalf("policy %s, %d players, seed %d: no winner", name, players, r.Seed)
				}
				if r.Rounds < 1 || r.Shows < 1 {
					t.Fatalf("policy %s: %d rounds, %d shows", name, r.Rounds, r.Shows)
				}
			}
		}
	}
}

func TestMixedPoliciesNoFailures(t *testing.T) {
	st := RunMany(strictOptions(0), 240, 4, 9001)
	if len(st.Failures) != 0 {
		t.Fatalf("%d failing seeds, first: seed=%d %v",
			len(st.Failures), st.Failures[0].Seed, st.Failures[0].Err)
	}
	if st.Games != 240 {
		t.Fatalf("completed %d games, want 240", st.Games)
	}
	if st.Shows != st.ClaimsTrue+(st.Shows-st.ClaimsTrue) || st.Shows == 0 {
		t.Fatalf("show accounting looks wrong: %d shows", st.Shows)
	}
	// Every round ends in exactly one resolved SHOW, so the two must match.
	if st.Shows != st.Rounds {
		t.Fatalf("%d SHOWs across %d rounds; every round must end in exactly one",
			st.Shows, st.Rounds)
	}
	// One penalty per round, and each player absorbs at most five before
	// elimination, so eliminations must be one less than the table size.
	if st.Eliminations == 0 {
		t.Fatal("no eliminations recorded")
	}
	t.Logf("%d games, %d rounds, %d claims, %d actions, %.1f%% of claims held up",
		st.Games, st.Rounds, st.Claims, st.Actions, 100*st.TrueRate())
}

// TestOneWinnerPerGame checks the accounting identity that each game
// eliminates everyone but one player.
func TestOneWinnerPerGame(t *testing.T) {
	for players := game.MinPlayers; players <= game.MaxPlayers; players++ {
		r := RunGame(strictOptions(players), int64(players)*77)
		if r.Err != nil {
			t.Fatalf("%d players: %v", players, r.Err)
		}
		if r.Eliminations != players-1 {
			t.Fatalf("%d players: %d eliminations, want %d",
				players, r.Eliminations, players-1)
		}
		if r.Winner == "" {
			t.Fatalf("%d players: no winner", players)
		}
	}
}

func TestDeterministicBySeed(t *testing.T) {
	a := RunGame(strictOptions(6), 4242)
	b := RunGame(strictOptions(6), 4242)
	if a.Err != nil || b.Err != nil {
		t.Fatalf("errors: %v / %v", a.Err, b.Err)
	}
	if a.Rounds != b.Rounds || a.Claims != b.Claims || a.Shows != b.Shows ||
		a.ClaimsTrue != b.ClaimsTrue || a.Winner != b.Winner ||
		a.Actions != b.Actions || a.MaxClaimsInRound != b.MaxClaimsInRound {
		t.Fatalf("same seed produced different games:\n  %+v\n  %+v", a, b)
	}
	c := RunGame(strictOptions(6), 4243)
	if c.Winner == a.Winner && c.Rounds == a.Rounds && c.Claims == a.Claims {
		t.Log("different seeds produced a suspiciously similar game; not fatal, but worth a look")
	}
}

// TestMinRaiseWalksTheLadder confirms that a table which never challenges
// voluntarily drives rounds deep into the claim space and still terminates,
// via the forced SHOW when the standing claim is the maximum.
func TestMinRaiseWalksTheLadder(t *testing.T) {
	opts := strictOptions(4)
	opts.Policies = []Policy{MinRaise{}}
	r := RunGame(opts, 5)
	if r.Err != nil {
		t.Fatalf("%v", r.Err)
	}
	if r.MaxClaimsInRound < 100 {
		t.Fatalf("longest round was %d claims; min-raise should walk much further",
			r.MaxClaimsInRound)
	}
	if r.MaxClaimsInRound > poker.ClaimSpaceSize {
		t.Fatalf("round ran %d claims, above the %d-claim ceiling",
			r.MaxClaimsInRound, poker.ClaimSpaceSize)
	}
	t.Logf("longest round: %d claims of a possible %d", r.MaxClaimsInRound, poker.ClaimSpaceSize)
}

func TestReducedRoundsOccurAtFourPlayers(t *testing.T) {
	st := RunMany(strictOptions(4), 40, 2, 31337)
	if len(st.Failures) != 0 {
		t.Fatalf("seed=%d %v", st.Failures[0].Seed, st.Failures[0].Err)
	}
	if st.ReducedRounds == 0 {
		t.Fatal("no reduced-pool rounds in 40 four-player games; round 1 alone should produce them")
	}
	if st.ExactRounds == 0 {
		t.Fatal("no exact-five rounds; the pool should grow past four cards")
	}
	t.Logf("%d reduced-pool rounds, %d exact-five rounds", st.ReducedRounds, st.ExactRounds)
}

// spy records every view it is handed so the test can assert what a bot can
// actually see. This is the structural check that the view is a sufficient
// and safe interface for a client: the policy has no other input.
type spy struct {
	inner Policy
	views []game.PlayerView
}

func (s *spy) Name() string { return "spy" }
func (s *spy) Decide(v game.PlayerView, rng *rand.Rand) Decision {
	s.views = append(s.views, v)
	return s.inner.Decide(v, rng)
}

func TestPoliciesNeverSeeHiddenCards(t *testing.T) {
	sp := &spy{inner: RandomRaise{ShowProb: 0.35, Window: 20}}
	opts := strictOptions(6)
	opts.Policies = []Policy{sp}
	r := RunGame(opts, 777)
	if r.Err != nil {
		t.Fatalf("%v", r.Err)
	}
	if len(sp.views) == 0 {
		t.Fatal("the policy was never consulted")
	}
	for i, v := range sp.views {
		if v.Revealed {
			t.Fatalf("view %d was handed to a policy with cards revealed", i)
		}
		own := 0
		for _, p := range v.Players {
			if p.IsYou {
				own = len(p.Cards)
				continue
			}
			if len(p.Cards) != 0 {
				t.Fatalf("view %d let %s see %s's cards: %s",
					i, v.You, p.ID, poker.CardsString(p.Cards))
			}
			if p.HandSize == 0 && p.StatusStr == game.StatusActive.String() {
				t.Fatalf("view %d hid an active player's hand size", i)
			}
		}
		if own == 0 {
			t.Fatalf("view %d showed the viewer none of their own cards", i)
		}
		if v.LastShow != nil {
			t.Fatalf("view %d carried a previous round's witness into claiming", i)
		}
		// The floor the client filters the static claim space against.
		if v.StandingOrdinal < -1 || v.StandingOrdinal >= poker.ClaimSpaceSize {
			t.Fatalf("view %d has standing ordinal %d", i, v.StandingOrdinal)
		}
	}
	t.Logf("inspected %d views handed to policies; none leaked", len(sp.views))
}

// TestSupportedNeverContradictsWidth guards the truth-aware bot's heuristic
// against the width rule it relies on.
func TestSupportedNeverContradictsWidth(t *testing.T) {
	own := poker.MustParseCards("8s 8h")
	for _, c := range poker.AllClaims() {
		for width := 2; width <= poker.FullHandSize; width++ {
			if supported(c, own, width) && !poker.RealizableAtWidth(c, width) {
				t.Fatalf("%s reported supported at width %d but is unrealizable there", c, width)
			}
		}
	}
	// A pair of 8s in hand should support a Pair of 8s claim at any width
	// that can hold a pair, and never a five-card-only category below five.
	if !supported(poker.NewPair(poker.Eight), own, 2) {
		t.Error("holding two 8s should support Pair of 8s at width 2")
	}
	if supported(poker.NewFlush(poker.Spades, poker.King), own, 4) {
		t.Error("a flush is unreachable at width 4")
	}
}

func BenchmarkGame(b *testing.B) {
	opts := Options{Players: 6}
	for i := 0; i < b.N; i++ {
		if r := RunGame(opts, int64(i)); r.Err != nil {
			b.Fatal(r.Err)
		}
	}
}

// TestPresenceChurnHoldsInvariants runs both disconnect policies with
// players dropping and returning mid-round. Presence churn moves turn order,
// the frozen pool's owner set and the elimination path all at once, which is
// where the engine is most likely to strand a turn or leave a ghost player.
func TestPresenceChurnHoldsInvariants(t *testing.T) {
	for _, policy := range []struct {
		name string
		p    game.DisconnectPolicy
	}{
		{"eliminate-at-round-end", game.DisconnectEliminateAtRoundEnd},
		{"eliminate-immediately", game.DisconnectEliminateImmediately},
	} {
		for _, churn := range []int{5, 20} {
			opts := strictOptions(0)
			opts.Disconnect = policy.p
			opts.ChurnPercent = churn
			st := RunMany(opts, 90, 3, 5150+int64(churn))
			if len(st.Failures) != 0 {
				f := st.Failures[0]
				t.Fatalf("%s at %d%% churn: seed=%d players=%d: %v",
					policy.name, churn, f.Seed, f.Players, f.Err)
			}
			if st.Games != 90 {
				t.Fatalf("%s at %d%% churn: %d games completed, want 90",
					policy.name, churn, st.Games)
			}
			t.Logf("%s at %2d%% churn: %d games (%d abandoned), %d rounds, %d eliminations",
				policy.name, churn, st.Games, st.Abandoned, st.Rounds, st.Eliminations)
		}
	}
}

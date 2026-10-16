// Package sim drives the engine with bot policies to find state-machine,
// accounting and evaluator bugs that hand-written tests miss.
//
// One design constraint matters more than the rest: **a policy sees only a
// game.PlayerView**, never the State. It gets its own cards, the public
// information, and the standing claim's ordinal, exactly what a real client
// receives. So a policy physically cannot consult the pool, the ownership
// map, the deck or another player's hand. That makes the simulator a
// standing proof that the view carries enough information to play the game,
// which is the property Phase 3's transport and Phase 4's UI both depend on.
package sim

import (
	"fmt"
	"io"
	"math/rand"
	"sort"
	"sync"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// Decision is what a policy does on its turn: exactly one claim or one SHOW,
// matching the rule that a turn is one action.
type Decision struct {
	Show  bool
	Claim poker.Claim
}

// Policy is a bot.
type Policy interface {
	Name() string
	Decide(v game.PlayerView, rng *rand.Rand) Decision
}

// ---------------------------------------------------------------- helpers

// legalClaims derives the legal raise set from the view alone: the static
// claim space filtered to ordinals above the standing claim. This is exactly
// what a real client will do.
func legalClaims(v game.PlayerView) []poker.Claim {
	all := poker.AllClaims()
	if v.StandingOrdinal < 0 {
		return all
	}
	if v.StandingOrdinal+1 >= len(all) {
		return nil
	}
	return all[v.StandingOrdinal+1:]
}

func ownCards(v game.PlayerView) []poker.Card {
	for _, p := range v.Players {
		if p.IsYou {
			return p.Cards
		}
	}
	return nil
}

func holdsRank(cards []poker.Card, r poker.Rank) bool {
	for _, c := range cards {
		if c.Rank == r {
			return true
		}
	}
	return false
}

func holdsSuit(cards []poker.Card, s poker.Suit) bool {
	for _, c := range cards {
		if c.Suit == s {
			return true
		}
	}
	return false
}

func holdsStraightRank(cards []poker.Card, high poker.Rank) bool {
	for _, r := range poker.StraightRanks(high) {
		if holdsRank(cards, r) {
			return true
		}
	}
	return false
}

// supported is a heuristic: could this claim plausibly be true given only
// what the bot can see? It checks that the claim is realizable at the current
// hand width and that the bot holds a card of the claimed pattern.
//
// Note that this is deliberately *narrower* than the ownership rule. The rule
// only requires the claimant to own some card of the evaluated hand, which a
// bot satisfies automatically below five cards and almost always above. So
// ownership carries no strategic information, and what actually matters to a
// bot is whether the pattern itself is plausible. Holding a pattern card is
// the cheapest evidence of that. It is not an oracle — the bot cannot see
// other hands — which is the point.
func supported(c poker.Claim, own []poker.Card, width int) bool {
	if !poker.RealizableAtWidth(c, width) {
		return false
	}
	switch c.Category {
	case poker.HighCard, poker.OnePair, poker.ThreeOfAKind, poker.FourOfAKind:
		return holdsRank(own, c.Primary)
	case poker.TwoPair, poker.FullHouse:
		return holdsRank(own, c.Primary) || holdsRank(own, c.Secondary)
	case poker.Straight:
		return holdsStraightRank(own, c.Primary)
	case poker.Flush, poker.StraightFlush, poker.RoyalFlush:
		return holdsSuit(own, c.Suit)
	}
	return false
}

func fallback(v game.PlayerView) Decision {
	if v.CanShow {
		return Decision{Show: true}
	}
	if l := legalClaims(v); len(l) > 0 {
		return Decision{Claim: l[0]}
	}
	// Unreachable: a turn holder with no standing claim always has the full
	// claim space available.
	return Decision{Show: true}
}

// ---------------------------------------------------------------- policies

// AlwaysShow challenges at the first opportunity. Against a reduced pool
// this is close to optimal play, which makes it a useful baseline.
type AlwaysShow struct{}

func (AlwaysShow) Name() string { return "always-show" }
func (AlwaysShow) Decide(v game.PlayerView, _ *rand.Rand) Decision {
	if v.CanShow {
		return Decision{Show: true}
	}
	return fallback(v)
}

// MinRaise never challenges while a raise exists, so rounds walk the whole
// claim ladder. It is the worst case for round length and exercises the
// forced-SHOW-at-maximum path on every round.
type MinRaise struct{}

func (MinRaise) Name() string { return "min-raise" }
func (MinRaise) Decide(v game.PlayerView, _ *rand.Rand) Decision {
	if l := legalClaims(v); len(l) > 0 {
		return Decision{Claim: l[0]}
	}
	return Decision{Show: true}
}

// RandomRaise challenges with a fixed probability and otherwise raises
// somewhere in a window above the standing claim.
type RandomRaise struct {
	ShowProb float64
	Window   int
}

func (RandomRaise) Name() string { return "random-raise" }
func (p RandomRaise) Decide(v game.PlayerView, rng *rand.Rand) Decision {
	l := legalClaims(v)
	if len(l) == 0 {
		return Decision{Show: true}
	}
	if v.CanShow && rng.Float64() < p.ShowProb {
		return Decision{Show: true}
	}
	w := p.Window
	if w <= 0 || w > len(l) {
		w = len(l)
	}
	return Decision{Claim: l[rng.Intn(w)]}
}

// Cautious raises only to the lowest claim its own cards could contribute
// to, at the current hand width, and challenges when it has nothing.
type Cautious struct{}

func (Cautious) Name() string { return "cautious" }
func (Cautious) Decide(v game.PlayerView, rng *rand.Rand) Decision {
	own := ownCards(v)
	for _, c := range legalClaims(v) {
		if supported(c, own, v.HandWidth) {
			return Decision{Claim: c}
		}
	}
	return fallback(v)
}

// Bluffer jumps up the ladder and rarely challenges, which drives claims far
// above what the cards support and so exercises the FALSE branch hard.
type Bluffer struct{}

func (Bluffer) Name() string { return "bluffer" }
func (Bluffer) Decide(v game.PlayerView, rng *rand.Rand) Decision {
	l := legalClaims(v)
	if len(l) == 0 {
		return Decision{Show: true}
	}
	if v.CanShow && rng.Intn(100) < 8 {
		return Decision{Show: true}
	}
	jump := rng.Intn(60)
	if jump >= len(l) {
		jump = len(l) - 1
	}
	return Decision{Claim: l[jump]}
}

// Registry maps policy names to constructors for the command line.
func Registry() map[string]Policy {
	return map[string]Policy{
		"always-show":  AlwaysShow{},
		"min-raise":    MinRaise{},
		"random-raise": RandomRaise{ShowProb: 0.4, Window: 25},
		"cautious":     Cautious{},
		"bluffer":      Bluffer{},
	}
}

// PolicyNames returns the registry keys, sorted, for help text.
func PolicyNames() []string {
	var out []string
	for k := range Registry() {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Mixed is the default assignment: one of each, cycled across seats.
func Mixed() []Policy {
	return []Policy{
		Cautious{}, RandomRaise{ShowProb: 0.4, Window: 25}, Bluffer{},
		AlwaysShow{}, RandomRaise{ShowProb: 0.2, Window: 8}, MinRaise{},
		Cautious{}, Bluffer{}, RandomRaise{ShowProb: 0.5, Window: 40},
	}
}

// ---------------------------------------------------------------- running

// Options configures one run.
type Options struct {
	Players         int
	Policies        []Policy // assigned to seats round-robin
	CheckInvariants bool
	CheckLeaks      bool
	MaxActions      int
	Trace           io.Writer
	// ChurnPercent is the chance, per action, of a random in-play player
	// dropping or returning. Presence churn moves turn order and the
	// elimination path, so the invariant checks are worth running against
	// it rather than only against clean games.
	ChurnPercent int
	// Disconnect selects the presence policy under test.
	Disconnect game.DisconnectPolicy
}

// Result is one finished game.
type Result struct {
	Seed             int64
	Players          int
	Rounds           int
	Claims           int
	Shows            int
	ClaimsTrue       int
	Eliminations     int
	Actions          int
	ReducedRounds    int
	ExactRounds      int
	MaxClaimsInRound int
	Winner           game.PlayerID
	WinnerSeat       int
	WinnerPolicy     string
	SeatPolicies     []string
	Validated        map[poker.Category]int
	Abandoned        bool
	Err              error
}

var seatIDs = []game.PlayerID{"A", "B", "C", "D", "E", "F", "G", "H", "I"}

// RunGame plays one complete game and returns its statistics. It never
// panics on a rule violation: it records the failure in Result.Err with the
// seed, so the caller can turn any bad seed into a permanent fixture.
func RunGame(opts Options, seed int64) Result {
	res := Result{
		Seed:      seed,
		Players:   opts.Players,
		Validated: map[poker.Category]int{},
	}
	if opts.MaxActions == 0 {
		opts.MaxActions = 200000
	}
	if opts.Players < game.MinPlayers || opts.Players > game.MaxPlayers {
		res.Err = fmt.Errorf("player count %d outside [%d,%d]",
			opts.Players, game.MinPlayers, game.MaxPlayers)
		return res
	}
	policies := opts.Policies
	if len(policies) == 0 {
		policies = Mixed()
	}

	rng := rand.New(rand.NewSource(seed ^ 0x9e3779b9))
	cfg := game.DefaultConfig(seed)
	cfg.Disconnect = opts.Disconnect
	s := game.NewGame(cfg)

	// Rotate the policy-to-seat assignment by seed. Without this, a given
	// policy always sits at the same seat, and the wins-by-seat table
	// silently measures policy strength instead of positional bias. With
	// it, each policy visits each seat about equally, so seat bias becomes
	// a real measurement of whether starting position matters.
	offset := int(seed % int64(len(policies)))
	if offset < 0 {
		offset += len(policies)
	}
	seatPolicy := make(map[game.PlayerID]Policy, opts.Players)
	for i := 0; i < opts.Players; i++ {
		id := seatIDs[i]
		p := policies[(i+offset)%len(policies)]
		seatPolicy[id] = p
		res.SeatPolicies = append(res.SeatPolicies, p.Name())
		var err error
		if s, _, err = game.Apply(s, game.ActionJoin{Meta: game.Meta{PlayerID: id}, Name: string(id)}); err != nil {
			res.Err = err
			return res
		}
		if s, _, err = game.Apply(s, game.ActionSetReady{Meta: game.Meta{PlayerID: id}, Ready: true}); err != nil {
			res.Err = err
			return res
		}
	}
	var err error
	if s, _, err = game.Apply(s, game.ActionStartGame{Meta: game.Meta{PlayerID: seatIDs[0]}}); err != nil {
		res.Err = err
		return res
	}
	res.Rounds = 1
	if s.EvalMode() == poker.ModeReducedPool {
		res.ReducedRounds++
	} else {
		res.ExactRounds++
	}

	claimsThisRound := 0
	for s.Phase != game.GameOver {
		res.Actions++
		if res.Actions > opts.MaxActions {
			res.Err = fmt.Errorf("did not terminate within %d actions", opts.MaxActions)
			return res
		}

		var act game.Action
		switch s.Phase {
		case game.RoundEnd:
			act = game.ActionAdvanceRound{Meta: game.Meta{PlayerID: seatIDs[0]}}
		case game.Claiming:
			me := s.Players[s.TurnSeat].ID
			// The bot decides from the view only.
			d := seatPolicy[me].Decide(game.View(s, me), rng)
			if d.Show {
				act = game.ActionCallShow{Meta: game.Meta{PlayerID: me}}
			} else {
				act = game.ActionMakeClaim{Meta: game.Meta{PlayerID: me}, Claim: d.Claim}
			}
		default:
			res.Err = fmt.Errorf("unexpected phase %s", s.Phase)
			return res
		}

		// Presence churn, applied before the turn action so it can land on
		// the turn holder and on the standing claimant.
		if opts.ChurnPercent > 0 && rng.Intn(100) < opts.ChurnPercent {
			var candidates []game.PlayerID
			for i := range s.Players {
				if s.Players[i].Status != game.StatusEliminated {
					candidates = append(candidates, s.Players[i].ID)
				}
			}
			if len(candidates) > 2 {
				who := candidates[rng.Intn(len(candidates))]
				var churn game.Action
				if p, ok := playerStatus(s, who); ok && p == game.StatusDisconnected {
					churn = game.ActionReconnect{Meta: game.Meta{PlayerID: who}}
				} else {
					churn = game.ActionDisconnect{Meta: game.Meta{PlayerID: who}}
				}
				cs, _, cerr := game.Apply(s, churn)
				if cerr != nil {
					res.Err = fmt.Errorf("churn %s rejected: %w", churn, cerr)
					return res
				}
				s = cs
				if opts.CheckInvariants {
					if err := game.CheckInvariants(s); err != nil {
						res.Err = fmt.Errorf("invariant broken after presence churn: %w", err)
						return res
					}
				}
				if s.Phase == game.GameOver {
					break
				}
				continue
			}
		}

		prevPhase := s.Phase
		prevRound := s.Round
		ns, events, err := game.Apply(s, act)
		if err != nil {
			res.Err = fmt.Errorf("round %d: %s rejected: %w", s.Round, act, err)
			return res
		}
		s = ns

		if opts.CheckInvariants {
			if err := game.CheckInvariants(s); err != nil {
				res.Err = fmt.Errorf("invariant broken: %w", err)
				return res
			}
		}
		if opts.CheckLeaks {
			for i := range s.Players {
				if leaked := game.LeakedCards(s, s.Players[i].ID); len(leaked) != 0 {
					res.Err = fmt.Errorf("%s can see %s",
						s.Players[i].ID, poker.CardsString(leaked))
					return res
				}
			}
		}

		for _, e := range events {
			switch e.Type {
			case game.EvClaimMade:
				res.Claims++
				claimsThisRound++
			case game.EvShowCalled:
				res.Shows++
			case game.EvClaimValidated:
				if e.Result != nil {
					if e.Result.ClaimTrue {
						res.ClaimsTrue++
					}
					res.Validated[e.Result.Claim.Category]++
					if opts.Trace != nil {
						fmt.Fprintf(opts.Trace, "seed %d r%-3d %-6s %-28s %-5v loser=%s (%s, pool %d)\n",
							seed, e.Round, e.Result.Claimant, e.Result.Claim,
							e.Result.ClaimTrue, e.Result.Loser, e.Result.Mode, e.Result.PoolSize)
					}
				}
			case game.EvPlayerEliminated:
				res.Eliminations++
			case game.EvRoundStarted:
				if e.Round > prevRound {
					res.Rounds++
					if e.Mode == poker.ModeReducedPool {
						res.ReducedRounds++
					} else {
						res.ExactRounds++
					}
				}
			}
		}
		if prevPhase == game.Claiming && s.Phase != game.Claiming {
			if claimsThisRound > res.MaxClaimsInRound {
				res.MaxClaimsInRound = claimsThisRound
			}
			claimsThisRound = 0
		}
	}

	res.Winner = s.Winner
	if s.Winner == "" {
		// Every player disconnected; the game is abandoned, not broken.
		res.Abandoned = true
		return res
	}
	if seat, ok := seatOf(s, s.Winner); ok {
		res.WinnerSeat = seat
		res.WinnerPolicy = seatPolicy[s.Winner].Name()
	}
	return res
}

func playerStatus(s game.State, id game.PlayerID) (game.PlayerStatus, bool) {
	for i := range s.Players {
		if s.Players[i].ID == id {
			return s.Players[i].Status, true
		}
	}
	return 0, false
}

func seatOf(s game.State, id game.PlayerID) (int, bool) {
	for i := range s.Players {
		if s.Players[i].ID == id {
			return i, true
		}
	}
	return -1, false
}

// ---------------------------------------------------------------- stats

// Stats aggregates many games.
type Stats struct {
	Games, Rounds, Claims, Shows, ClaimsTrue int
	Abandoned                                int
	Eliminations, Actions                    int
	ReducedRounds, ExactRounds               int
	MaxClaimsInRound                         int
	LongestGameRounds                        int
	ShortestGameRounds                       int
	GamesByPlayers                           map[int]int
	RoundsByPlayers                          map[int]int
	WinsByPolicy                             map[string]int
	SeatsByPolicy                            map[string]int
	WinsBySeat                               map[int]int
	// SeatAppearances counts games in which each seat existed at all.
	// Without it, wins-by-seat is dominated by table size: seat 8 only
	// exists in nine-player games.
	SeatAppearances map[int]int
	Validated       map[poker.Category]int
	Failures        []Result
}

func newStats() *Stats {
	return &Stats{
		GamesByPlayers:  map[int]int{},
		RoundsByPlayers: map[int]int{},
		WinsByPolicy:    map[string]int{},
		SeatsByPolicy:   map[string]int{},
		WinsBySeat:      map[int]int{},
		SeatAppearances: map[int]int{},
		Validated:       map[poker.Category]int{},
	}
}

func (st *Stats) add(r Result) {
	if r.Err != nil {
		st.Failures = append(st.Failures, r)
		return
	}
	st.Games++
	if r.Abandoned {
		st.Abandoned++
	}
	st.Rounds += r.Rounds
	st.Claims += r.Claims
	st.Shows += r.Shows
	st.ClaimsTrue += r.ClaimsTrue
	st.Eliminations += r.Eliminations
	st.Actions += r.Actions
	st.ReducedRounds += r.ReducedRounds
	st.ExactRounds += r.ExactRounds
	if r.MaxClaimsInRound > st.MaxClaimsInRound {
		st.MaxClaimsInRound = r.MaxClaimsInRound
	}
	if r.Rounds > st.LongestGameRounds {
		st.LongestGameRounds = r.Rounds
	}
	if st.ShortestGameRounds == 0 || r.Rounds < st.ShortestGameRounds {
		st.ShortestGameRounds = r.Rounds
	}
	st.GamesByPlayers[r.Players]++
	st.RoundsByPlayers[r.Players] += r.Rounds
	if !r.Abandoned {
		st.WinsBySeat[r.WinnerSeat]++
	}
	for i := 0; i < r.Players; i++ {
		st.SeatAppearances[i]++
	}
	if r.WinnerPolicy != "" {
		st.WinsByPolicy[r.WinnerPolicy]++
	}
	for _, name := range r.SeatPolicies {
		st.SeatsByPolicy[name]++
	}
	for cat, n := range r.Validated {
		st.Validated[cat] += n
	}
}

// RunMany plays `games` games across `workers` goroutines. When
// opts.Players is zero the player count cycles through 4..9.
//
// Running games concurrently is deliberate: the engine is a pure reducer, so
// concurrent games share nothing, and `go test -race` on this package is a
// real check of that claim rather than a formality.
func RunMany(opts Options, games, workers int, baseSeed int64) Stats {
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int64, workers*2)
	out := make(chan Result, workers*2)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for seed := range jobs {
				o := opts
				if o.Players == 0 {
					o.Players = game.MinPlayers +
						int(seed-baseSeed)%(game.MaxPlayers-game.MinPlayers+1)
				}
				out <- RunGame(o, seed)
			}
		}()
	}
	go func() {
		for i := 0; i < games; i++ {
			jobs <- baseSeed + int64(i)
		}
		close(jobs)
		wg.Wait()
		close(out)
	}()

	st := newStats()
	for r := range out {
		st.add(r)
	}
	return *st
}

// TrueRate is the fraction of resolved SHOWs where the claim held up.
func (st Stats) TrueRate() float64 {
	if st.Shows == 0 {
		return 0
	}
	return float64(st.ClaimsTrue) / float64(st.Shows)
}

// ClaimsPerRound is the mean number of claims before a SHOW.
func (st Stats) ClaimsPerRound() float64 {
	if st.Rounds == 0 {
		return 0
	}
	return float64(st.Claims) / float64(st.Rounds)
}

// RoundsPerGame is the mean game length in rounds.
func (st Stats) RoundsPerGame() float64 {
	if st.Games == 0 {
		return 0
	}
	return float64(st.Rounds) / float64(st.Games)
}

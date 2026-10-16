// Command sim runs many simulated Liar's Poker games to hunt for
// state-machine, accounting and evaluator bugs, and to report balance
// statistics.
//
//	go run ./cmd/sim                          # 2000 mixed games, 4-9 players
//	go run ./cmd/sim -games 100000 -workers 8
//	go run ./cmd/sim -players 4 -policy always-show
//	go run ./cmd/sim -games 5 -trace
//
// Any seed that trips an invariant, leaks a card or fails to terminate is
// printed with its seed so it can be turned into a permanent regression
// fixture.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/internal/sim"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

func main() {
	games := flag.Int("games", 2000, "number of games to play")
	players := flag.Int("players", 0, "players per game; 0 cycles 4..9")
	seed := flag.Int64("seed", 1, "base seed; game i uses seed+i")
	workers := flag.Int("workers", runtime.NumCPU(), "concurrent games")
	policy := flag.String("policy", "mixed",
		"policy for every seat, or \"mixed\"; one of: "+strings.Join(sim.PolicyNames(), ", "))
	invariants := flag.Bool("invariants", true, "check all engine invariants after every action")
	leaks := flag.Bool("leaks", true, "check every player view for leaked cards after every action")
	trace := flag.Bool("trace", false, "print every SHOW resolution")
	flag.Parse()

	opts := sim.Options{
		Players:         *players,
		CheckInvariants: *invariants,
		CheckLeaks:      *leaks,
	}
	if *policy != "mixed" {
		p, ok := sim.Registry()[*policy]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown policy %q; choose from %s\n",
				*policy, strings.Join(sim.PolicyNames(), ", "))
			os.Exit(2)
		}
		opts.Policies = []sim.Policy{p}
	}
	if *trace {
		opts.Trace = os.Stdout
		*workers = 1 // keep trace lines in order
	}

	fmt.Printf("Liar's Poker simulator\n")
	fmt.Printf("  games=%d players=%s policy=%s workers=%d seed=%d invariants=%v leaks=%v\n",
		*games, playersLabel(*players), *policy, *workers, *seed, *invariants, *leaks)
	fmt.Printf("  claim space=%d  min=%s  max=%s\n\n",
		poker.ClaimSpaceSize, poker.MinClaim(), poker.MaxClaim())

	start := time.Now()
	st := sim.RunMany(opts, *games, *workers, *seed)
	elapsed := time.Since(start)

	report(st, elapsed)
	if len(st.Failures) > 0 {
		os.Exit(1)
	}
}

func playersLabel(n int) string {
	if n == 0 {
		return fmt.Sprintf("%d-%d", game.MinPlayers, game.MaxPlayers)
	}
	return fmt.Sprint(n)
}

func report(st sim.Stats, elapsed time.Duration) {
	line := strings.Repeat("-", 62)

	fmt.Println("TOTALS")
	fmt.Println(line)
	fmt.Printf("  games completed      %d\n", st.Games)
	fmt.Printf("  rounds               %d  (mean %.1f per game, min %d, max %d)\n",
		st.Rounds, st.RoundsPerGame(), st.ShortestGameRounds, st.LongestGameRounds)
	fmt.Printf("  claims               %d  (mean %.2f per round)\n", st.Claims, st.ClaimsPerRound())
	fmt.Printf("  SHOWs resolved       %d\n", st.Shows)
	fmt.Printf("  eliminations         %d\n", st.Eliminations)
	fmt.Printf("  actions applied      %d\n", st.Actions)
	fmt.Printf("  longest round        %d claims (ceiling is %d)\n",
		st.MaxClaimsInRound, poker.ClaimSpaceSize)
	fmt.Printf("  wall time            %s  (%.0f games/s, %.0f actions/s)\n",
		elapsed.Round(time.Millisecond),
		float64(st.Games)/elapsed.Seconds(), float64(st.Actions)/elapsed.Seconds())

	fmt.Printf("\nSHOW OUTCOMES\n%s\n", line)
	fmt.Printf("  claim held up        %d  (%.1f%%)  -> challenger penalised\n",
		st.ClaimsTrue, 100*st.TrueRate())
	fmt.Printf("  claim was a bluff    %d  (%.1f%%)  -> claimant penalised\n",
		st.Shows-st.ClaimsTrue, 100*(1-st.TrueRate()))

	fmt.Printf("\nEVALUATION MODE BY ROUND\n%s\n", line)
	total := st.ReducedRounds + st.ExactRounds
	if total > 0 {
		fmt.Printf("  REDUCED_POOL (<5)    %d  (%.1f%%)\n",
			st.ReducedRounds, 100*float64(st.ReducedRounds)/float64(total))
		fmt.Printf("  EXACT_FIVE  (>=5)    %d  (%.1f%%)\n",
			st.ExactRounds, 100*float64(st.ExactRounds)/float64(total))
	}

	fmt.Printf("\nROUNDS PER GAME BY TABLE SIZE\n%s\n", line)
	var sizes []int
	for n := range st.GamesByPlayers {
		sizes = append(sizes, n)
	}
	sort.Ints(sizes)
	for _, n := range sizes {
		g := st.GamesByPlayers[n]
		fmt.Printf("  %d players            %5d games, mean %.1f rounds\n",
			n, g, float64(st.RoundsByPlayers[n])/float64(g))
	}

	fmt.Printf("\nVALIDATED CLAIM CATEGORIES (the claim that ended each round)\n%s\n", line)
	type kv struct {
		cat poker.Category
		n   int
	}
	var cats []kv
	for c, n := range st.Validated {
		cats = append(cats, kv{c, n})
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].n > cats[j].n })
	for _, c := range cats {
		fmt.Printf("  %-18s   %6d  (%.1f%%)\n",
			c.cat.Title(), c.n, 100*float64(c.n)/float64(st.Shows))
	}

	if len(st.WinsByPolicy) > 0 {
		fmt.Printf("\nWIN RATE BY POLICY\n%s\n", line)
		var pols []string
		for p := range st.SeatsByPolicy {
			pols = append(pols, p)
		}
		sort.Strings(pols)
		for _, p := range pols {
			seats := st.SeatsByPolicy[p]
			wins := st.WinsByPolicy[p]
			fmt.Printf("  %-14s  %5d seats, %5d wins  (%.1f%% of its seats)\n",
				p, seats, wins, 100*float64(wins)/float64(seats))
		}
		fmt.Printf("  (a seat wins 1/n of the time by chance, so compare against that)\n")
	}

	fmt.Printf("\nWINS BY SEAT (normalised by games in which the seat existed)\n%s\n", line)
	var seats []int
	for s := range st.SeatAppearances {
		seats = append(seats, s)
	}
	sort.Ints(seats)
	for _, s := range seats {
		appear := st.SeatAppearances[s]
		fmt.Printf("  seat %d  %5d wins / %5d games  (%.1f%%)\n",
			s, st.WinsBySeat[s], appear, 100*float64(st.WinsBySeat[s])/float64(appear))
	}
	fmt.Printf("  (with n seats a fair game gives each 1/n; table sizes cycle, so\n")
	fmt.Printf("   compare seats against each other rather than a single baseline)\n")

	fmt.Printf("\nFAILURES\n%s\n", line)
	if len(st.Failures) == 0 {
		fmt.Printf("  none: every game terminated with a winner, every invariant held\n")
		fmt.Printf("  after every action, and no view ever leaked a hidden card.\n")
		return
	}
	fmt.Printf("  %d FAILING SEEDS - each of these should become a fixture:\n", len(st.Failures))
	for i, f := range st.Failures {
		if i >= 20 {
			fmt.Printf("  ... and %d more\n", len(st.Failures)-20)
			break
		}
		fmt.Printf("  seed=%d players=%d round=%d: %v\n", f.Seed, f.Players, f.Rounds, f.Err)
	}
}

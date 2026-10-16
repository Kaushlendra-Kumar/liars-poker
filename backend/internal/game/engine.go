package game

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// Apply is the reducer. It never mutates the state it is given; it returns a
// new state, the events the transition produced, and an error for any
// rejected action. A rejected action changes nothing at all, including the
// turn timer.
func Apply(s State, a Action) (State, []Event, error) {
	if a == nil {
		return s, nil, ErrUnknownAction
	}
	// Gameplay actions are never legal after the game ends.
	switch a.(type) {
	case ActionMakeClaim, ActionCallShow, ActionAdvanceRound, ActionStartGame:
		if s.Phase == GameOver {
			return s, nil, ErrGameOver
		}
	}

	ns := s.clone()
	var events []Event
	emit := func(e Event) {
		ns.Seq++
		e.Seq = ns.Seq
		e.Round = ns.Round
		events = append(events, e)
	}
	now := a.meta().Now

	switch act := a.(type) {
	case ActionJoin:
		if err := applyJoin(&ns, act, emit); err != nil {
			return s, nil, err
		}
	case ActionSetReady:
		if err := applySetReady(&ns, act, emit); err != nil {
			return s, nil, err
		}
	case ActionStartGame:
		if err := applyStartGame(&ns, act, emit); err != nil {
			return s, nil, err
		}
	case ActionMakeClaim:
		if err := applyMakeClaim(&ns, act, emit); err != nil {
			return s, nil, err
		}
	case ActionCallShow:
		if err := applyCallShow(&ns, act, emit); err != nil {
			return s, nil, err
		}
	case ActionAdvanceRound:
		if err := applyAdvanceRound(&ns, now, emit); err != nil {
			return s, nil, err
		}
	case ActionDisconnect:
		if err := applyDisconnect(&ns, act, emit); err != nil {
			return s, nil, err
		}
	case ActionReconnect:
		if err := applyReconnect(&ns, act, emit); err != nil {
			return s, nil, err
		}
	case ActionTick:
		if err := applyTick(&ns, now, emit); err != nil {
			return s, nil, err
		}
	default:
		return s, nil, ErrUnknownAction
	}
	return ns, events, nil
}

// ---------------------------------------------------------------- lobby

func applyJoin(ns *State, act ActionJoin, emit func(Event)) error {
	if ns.Phase != WaitingForPlayers {
		return ErrGameInProgress
	}
	// The id is a map key, a seat identity and an ownership tag, so it is
	// validated here rather than trusted from the transport.
	if act.PlayerID == "" || len(act.PlayerID) > MaxPlayerIDLength {
		return ErrInvalidPlayerID
	}
	if _, ok := ns.seatOf(act.PlayerID); ok {
		return ErrAlreadyJoined
	}
	if len(ns.Players) >= MaxPlayers {
		return ErrTooManyPlayers
	}
	seat := len(ns.Players)
	ns.Players = append(ns.Players, Player{
		ID:       act.PlayerID,
		Name:     act.Name,
		Seat:     seat,
		HandSize: StartingHandSize,
		Status:   StatusActive,
	})
	emit(Event{Type: EvPlayerJoined, Player: act.PlayerID, Seat: seat})
	return nil
}

func applySetReady(ns *State, act ActionSetReady, emit func(Event)) error {
	if ns.Phase != WaitingForPlayers {
		return ErrGameInProgress
	}
	seat, ok := ns.seatOf(act.PlayerID)
	if !ok {
		return ErrUnknownPlayer
	}
	ns.Players[seat].Ready = act.Ready
	emit(Event{Type: EvPlayerReady, Player: act.PlayerID, Seat: seat})
	return nil
}

func applyStartGame(ns *State, act ActionStartGame, emit func(Event)) error {
	if ns.Phase != WaitingForPlayers {
		return ErrGameInProgress
	}
	if _, ok := ns.seatOf(act.PlayerID); !ok {
		return ErrUnknownPlayer
	}
	if len(ns.Players) < MinPlayers {
		return ErrNotEnoughPlayer
	}
	if len(ns.Players) > MaxPlayers {
		return ErrTooManyPlayers
	}
	for i := range ns.Players {
		if !ns.Players[i].Ready {
			return ErrNotAllReady
		}
	}
	switch ns.Config.FirstStarter {
	case FirstStarterSeatZero:
		ns.StarterSeat = 0
	default:
		// Randomness is injected as a seed, so the first starter is
		// reproducible from (Seed) alone and is recorded in GameStarted.
		r := rand.New(rand.NewSource(ns.Config.Seed ^ 0x5eed5eed))
		ns.StarterSeat = r.Intn(len(ns.Players))
	}
	emit(Event{Type: EvGameStarted, Seat: ns.StarterSeat, Player: ns.Players[ns.StarterSeat].ID})
	return beginRound(ns, act.Now, emit)
}

// ---------------------------------------------------------------- play

func applyMakeClaim(ns *State, act ActionMakeClaim, emit func(Event)) error {
	if ns.Phase != Claiming {
		return ErrWrongPhase
	}
	seat, ok := ns.seatOf(act.PlayerID)
	if !ok {
		return ErrUnknownPlayer
	}
	if !ns.Players[seat].Status.InPlay() {
		return ErrPlayerNotActive
	}
	if seat != ns.TurnSeat {
		return ErrNotYourTurn
	}
	if err := act.Claim.Validate(); err != nil {
		return fmt.Errorf("%w: %s", ErrMalformedClaim, err)
	}
	if standing, ok := ns.StandingClaim(); ok && !poker.Higher(act.Claim, standing.Claim) {
		return ErrClaimNotHigher
	}
	submitClaim(ns, seat, act.Claim, act.Now, emit)
	return nil
}

func submitClaim(ns *State, seat int, c poker.Claim, now time.Time, emit func(Event)) {
	ns.Claims = append(ns.Claims, ClaimRecord{Seat: seat, PlayerID: ns.Players[seat].ID, Claim: c})
	cc := c
	emit(Event{Type: EvClaimMade, Player: ns.Players[seat].ID, Seat: seat, Claim: &cc})
	ns.TurnSeat = ns.nextInPlaySeat(seat)
	setTurnDeadline(ns, now)
	emitTurnStarted(ns, emit)
}

func applyCallShow(ns *State, act ActionCallShow, emit func(Event)) error {
	if ns.Phase != Claiming {
		return ErrWrongPhase
	}
	seat, ok := ns.seatOf(act.PlayerID)
	if !ok {
		return ErrUnknownPlayer
	}
	if !ns.Players[seat].Status.InPlay() {
		return ErrPlayerNotActive
	}
	if len(ns.Claims) == 0 {
		// The round starter has nothing to challenge.
		return ErrNoStandingClaim
	}
	if seat != ns.TurnSeat {
		// Only the immediate next player is ever the turn holder, so this
		// single check rejects SHOW from every other seat.
		return ErrNotShowEligible
	}
	return resolveShow(ns, seat, act.Now, emit)
}

// resolveShow reveals every in-play hand, validates the standing claim
// exactly, assigns the penalty and ends the round.
func resolveShow(ns *State, challengerSeat int, now time.Time, emit func(Event)) error {
	standing, ok := ns.StandingClaim()
	if !ok {
		return ErrNoStandingClaim
	}
	claimant := ns.Players[standing.Seat].ID
	challenger := ns.Players[challengerSeat].ID

	sc := standing.Claim
	emit(Event{Type: EvShowCalled, Player: challenger, Seat: challengerSeat, Claim: &sc})

	ns.Revealed = true
	emit(Event{
		Type:     EvCardsRevealed,
		Hands:    ns.handsSnapshot(),
		PoolSize: len(ns.Pool),
		Mode:     ns.EvalMode(),
	})

	owns := func(c poker.Card) bool { return ns.Owner[c] == claimant }
	res := poker.CanSatisfy(ns.Pool, owns, standing.Claim)

	loser := claimant
	if res.Satisfied {
		loser = challenger
	}
	result := &ShowResult{
		Round:      ns.Round,
		Claimant:   claimant,
		Challenger: challenger,
		Claim:      standing.Claim,
		ClaimTrue:  res.Satisfied,
		Witness:    res.Witness,
		Loser:      loser,
		Mode:       res.Mode,
		PoolSize:   res.PoolSize,
		HandSize:   res.HandSize,
	}
	ns.LastShow = result
	emit(Event{Type: EvClaimValidated, Player: claimant, Claim: &sc, Result: result})

	applyPenalty(ns, loser, emit)
	emit(Event{Type: EvRoundEnded, Player: loser})

	if ns.InPlayCount() <= 1 {
		return endGame(ns, emit)
	}
	ns.Phase = RoundEnd
	ns.TurnDeadline = time.Time{}
	if !now.IsZero() && ns.Config.RoundEndDuration > 0 {
		ns.RoundEndDeadline = now.Add(ns.Config.RoundEndDuration)
	}
	return nil
}

// applyPenalty gives the loser one more card next round, or eliminates them
// if that would reach six. No other player's hand size changes.
func applyPenalty(ns *State, loser PlayerID, emit func(Event)) {
	seat, ok := ns.seatOf(loser)
	if !ok {
		return
	}
	p := &ns.Players[seat]
	if p.HandSize+1 >= EliminationHandSize {
		p.Status = StatusEliminated
		emit(Event{
			Type:              EvPlayerEliminated,
			Player:            p.ID,
			Seat:              seat,
			HandSize:          p.HandSize,
			AttemptedHandSize: EliminationHandSize,
			Reason:            "SIX_CARDS",
		})
		return
	}
	p.HandSize++
	emit(Event{Type: EvPenaltyApplied, Player: p.ID, Seat: seat, HandSize: p.HandSize})
}

// endGame closes the game. A winner exists when exactly one player is still
// in play, which is the outcome the rules describe.
//
// Zero players can also remain: round-end pruning eliminates every
// still-disconnected player at once, so a table that empties out during a
// round ends with nobody left. The rules do not name a winner for that case
// and this function does not invent one — it records the game as abandoned
// and leaves Winner empty. Deciding what a room does with an abandoned game
// is a room-lifecycle question for the transport layer, not a game rule.
func endGame(ns *State, emit func(Event)) error {
	ns.Phase = GameOver
	ns.TurnDeadline = time.Time{}
	ns.RoundEndDeadline = time.Time{}
	reason := "ABANDONED"
	if ns.InPlayCount() == 1 {
		for i := range ns.Players {
			if ns.Players[i].Status.InPlay() {
				ns.Winner = ns.Players[i].ID
				break
			}
		}
		reason = "WINNER"
	}
	emit(Event{Type: EvGameEnded, Player: ns.Winner, Reason: reason})
	return nil
}

func applyAdvanceRound(ns *State, now time.Time, emit func(Event)) error {
	if ns.Phase != RoundEnd {
		return ErrWrongPhase
	}
	pruneDisconnected(ns, emit)
	if ns.InPlayCount() <= 1 {
		return endGame(ns, emit)
	}
	return beginRound(ns, now, emit)
}

// ---------------------------------------------------------------- rounds

// beginRound returns every card to the deck, reshuffles all 52, deals each
// in-play player their current hand size, freezes the pool and opens the
// claiming phase.
//
// Starter rotation lives here and nowhere else: from round two onward the
// starter is the next in-play seat clockwise from the previous starter's
// seat. It reads nothing about the SHOW caller, the claimant, the penalty
// recipient or the previous result, so rotation cannot depend on them.
func beginRound(ns *State, now time.Time, emit func(Event)) error {
	if ns.Round > 0 {
		ns.StarterSeat = ns.nextInPlaySeat(ns.StarterSeat)
	}
	if ns.StarterSeat < 0 {
		return fmt.Errorf("game: no in-play seat to start round")
	}
	ns.Round++
	ns.Claims = nil
	ns.Revealed = false
	ns.LastShow = nil
	ns.Pool = nil
	ns.Owner = map[poker.Card]PlayerID{}
	for i := range ns.Players {
		ns.Players[i].Hand = nil
	}

	// Each round derives its own shuffle source, so the whole game is
	// reproducible from the seed alone while Apply stays a pure function.
	r := rand.New(rand.NewSource(ns.Config.Seed*1000003 + int64(ns.Round)))
	deck := poker.NewShuffledDeck(r)

	hands := map[PlayerID][]poker.Card{}
	for i := range ns.Players {
		p := &ns.Players[i]
		if !p.Status.InPlay() {
			continue
		}
		cards, err := deck.Deal(p.HandSize)
		if err != nil {
			return fmt.Errorf("game: dealing round %d: %w", ns.Round, err)
		}
		poker.SortCards(cards)
		p.Hand = cards
		hands[p.ID] = append([]poker.Card(nil), cards...)
		for _, c := range cards {
			ns.Owner[c] = p.ID
		}
		ns.Pool = append(ns.Pool, cards...)
	}
	poker.SortCards(ns.Pool)
	ns.DeckRemaining = deck.Remaining()

	ns.Phase = Claiming
	ns.TurnSeat = ns.StarterSeat
	ns.RoundEndDeadline = time.Time{}
	setTurnDeadline(ns, now)

	emit(Event{
		Type:     EvRoundStarted,
		Player:   ns.Players[ns.StarterSeat].ID,
		Seat:     ns.StarterSeat,
		PoolSize: len(ns.Pool),
		Mode:     ns.EvalMode(),
	})
	emit(Event{
		Type:     EvCardsDealt,
		Hands:    hands,
		PoolSize: len(ns.Pool),
		Mode:     ns.EvalMode(),
	})
	emitTurnStarted(ns, emit)
	return nil
}

func (s State) handsSnapshot() map[PlayerID][]poker.Card {
	out := map[PlayerID][]poker.Card{}
	for i := range s.Players {
		if s.Players[i].Status.InPlay() {
			out[s.Players[i].ID] = append([]poker.Card(nil), s.Players[i].Hand...)
		}
	}
	return out
}

func setTurnDeadline(ns *State, now time.Time) {
	if now.IsZero() || ns.Config.TurnDuration <= 0 {
		ns.TurnDeadline = time.Time{}
		return
	}
	ns.TurnDeadline = now.Add(ns.Config.TurnDuration)
}

func emitTurnStarted(ns *State, emit func(Event)) {
	if ns.TurnSeat < 0 || ns.TurnSeat >= len(ns.Players) {
		return
	}
	emit(Event{
		Type:     EvTurnStarted,
		Player:   ns.Players[ns.TurnSeat].ID,
		Seat:     ns.TurnSeat,
		Deadline: ns.TurnDeadline,
		PoolSize: len(ns.Pool),
		Mode:     ns.EvalMode(),
	})
}

// ---------------------------------------------------------------- presence

func applyDisconnect(ns *State, act ActionDisconnect, emit func(Event)) error {
	seat, ok := ns.seatOf(act.PlayerID)
	if !ok {
		return ErrUnknownPlayer
	}
	p := &ns.Players[seat]
	if p.Status == StatusEliminated {
		return nil
	}
	p.Status = StatusDisconnected
	emit(Event{Type: EvPlayerDisconnected, Player: p.ID, Seat: seat})

	// Immediate elimination is only safe outside an active claiming round.
	// Mid-round the pool is frozen and the claim chain is live, so removing
	// a participant would either strand the turn on an eliminated seat or
	// strand a standing claim whose author can no longer be penalised.
	// During CLAIMING the player therefore stays in play as DISCONNECTED and
	// is eliminated by pruneDisconnected at round end, which is also what
	// the default policy does. Nothing stalls: the turn timer still fires
	// against them.
	if ns.Config.Disconnect == DisconnectEliminateImmediately && ns.Phase != Claiming {
		p.Status = StatusEliminated
		emit(Event{Type: EvPlayerEliminated, Player: p.ID, Seat: seat, Reason: "DISCONNECT"})
		if ns.Phase != WaitingForPlayers && ns.InPlayCount() <= 1 {
			return endGame(ns, emit)
		}
	}
	return nil
}

func applyReconnect(ns *State, act ActionReconnect, emit func(Event)) error {
	seat, ok := ns.seatOf(act.PlayerID)
	if !ok {
		return ErrUnknownPlayer
	}
	p := &ns.Players[seat]
	if p.Status != StatusDisconnected {
		return nil
	}
	p.Status = StatusActive
	emit(Event{Type: EvPlayerReconnected, Player: p.ID, Seat: seat})
	return nil
}

// pruneDisconnected eliminates players still disconnected at round end.
// Doing it here rather than mid-round is what keeps the frozen pool honest.
//
// This runs under every disconnect policy, not just the round-end default.
// The immediate policy defers mid-round disconnects to here (see
// applyDisconnect), so gating this on the policy would leave those players
// in play as permanent ghosts, which is precisely what the rules forbid.
func pruneDisconnected(ns *State, emit func(Event)) {
	for i := range ns.Players {
		p := &ns.Players[i]
		if p.Status == StatusDisconnected {
			p.Status = StatusEliminated
			emit(Event{Type: EvPlayerEliminated, Player: p.ID, Seat: i, Reason: "DISCONNECT"})
		}
	}
}

// ---------------------------------------------------------------- timers

func applyTick(ns *State, now time.Time, emit func(Event)) error {
	if now.IsZero() {
		return nil
	}
	switch ns.Phase {
	case Claiming:
		if ns.TurnDeadline.IsZero() || now.Before(ns.TurnDeadline) {
			return nil
		}
		return applyTimeout(ns, now, emit)
	case RoundEnd:
		if ns.RoundEndDeadline.IsZero() || now.Before(ns.RoundEndDeadline) {
			return nil
		}
		return applyAdvanceRound(ns, now, emit)
	}
	return nil
}

// applyTimeout enacts the configured consequence of exceeding the turn
// budget. The consequence is a policy because the rules fix the 30-second
// budget but leave the consequence to the specification.
func applyTimeout(ns *State, now time.Time, emit func(Event)) error {
	seat := ns.TurnSeat
	if seat < 0 || seat >= len(ns.Players) {
		return nil
	}
	player := ns.Players[seat].ID
	emit(Event{
		Type:   EvTurnTimedOut,
		Player: player,
		Seat:   seat,
		Reason: timeoutReason(ns.Config.Timeout),
	})
	_, hasStanding := ns.StandingClaim()

	switch ns.Config.Timeout {
	case TimeoutForfeitRound:
		applyPenalty(ns, player, emit)
		emit(Event{Type: EvRoundEnded, Player: player})
		if ns.InPlayCount() <= 1 {
			return endGame(ns, emit)
		}
		ns.Phase = RoundEnd
		ns.TurnDeadline = time.Time{}
		if ns.Config.RoundEndDuration > 0 {
			ns.RoundEndDeadline = now.Add(ns.Config.RoundEndDuration)
		}
		return nil

	case TimeoutMinimumClaim:
		return timeoutClaim(ns, seat, now, emit)

	default: // TimeoutAutoShow
		if hasStanding {
			return resolveShow(ns, seat, now, emit)
		}
		return timeoutClaim(ns, seat, now, emit)
	}
}

// timeoutClaim submits the lowest legal claim on the player's behalf. If no
// legal raise exists the standing claim is already the maximum, so SHOW is
// the only remaining action and it is taken instead.
func timeoutClaim(ns *State, seat int, now time.Time, emit func(Event)) error {
	if standing, ok := ns.StandingClaim(); ok {
		above := poker.ClaimsAbove(standing.Claim)
		if len(above) == 0 {
			return resolveShow(ns, seat, now, emit)
		}
		submitClaim(ns, seat, above[0], now, emit)
		return nil
	}
	submitClaim(ns, seat, poker.MinClaim(), now, emit)
	return nil
}

func timeoutReason(p TimeoutPolicy) string {
	switch p {
	case TimeoutMinimumClaim:
		return "AUTO_MINIMUM_CLAIM"
	case TimeoutForfeitRound:
		return "FORFEIT_ROUND"
	}
	return "AUTO_SHOW"
}

// ---------------------------------------------------------------- invariants

// CheckInvariants asserts every engine invariant. Tests call it after every
// single Apply during simulation, not just at round boundaries, so a
// violation is caught on the action that caused it.
func CheckInvariants(s State) error {
	// I1: no duplicate physical cards anywhere.
	all := append([]poker.Card(nil), s.Pool...)
	for i := range s.Players {
		all = append(all, s.Players[i].Hand...)
	}
	seen := map[poker.Card]int{}
	for i := range s.Players {
		for _, c := range s.Players[i].Hand {
			seen[c]++
			if seen[c] > 1 {
				return fmt.Errorf("I1: card %s held twice", c)
			}
		}
	}

	// I2: card conservation. Pool plus undealt deck is the whole deck.
	if s.Phase == Claiming || s.Phase == RoundEnd {
		if len(s.Pool)+s.DeckRemaining != poker.DeckSize {
			return fmt.Errorf("I2: pool %d + deck %d != %d",
				len(s.Pool), s.DeckRemaining, poker.DeckSize)
		}
		held := 0
		for i := range s.Players {
			held += len(s.Players[i].Hand)
		}
		if held != len(s.Pool) {
			return fmt.Errorf("I2: hands hold %d but pool has %d", held, len(s.Pool))
		}
		for _, c := range s.Pool {
			owner, ok := s.Owner[c]
			if !ok {
				return fmt.Errorf("I2: pool card %s has no owner", c)
			}
			p, ok := s.player(owner)
			if !ok {
				return fmt.Errorf("I2: pool card %s owned by unknown player", c)
			}
			found := false
			for _, h := range p.Hand {
				if h == c {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("I2: %s not in %s's hand", c, owner)
			}
		}
	}

	// I3: hand size bounds; no in-play player ever holds six.
	for i := range s.Players {
		p := s.Players[i]
		if !p.Status.InPlay() {
			continue
		}
		if p.HandSize < StartingHandSize || p.HandSize > MaxHandSize {
			return fmt.Errorf("I3: %s has hand size %d", p.ID, p.HandSize)
		}
		if s.Phase == Claiming && len(p.Hand) != p.HandSize {
			return fmt.Errorf("I3: %s dealt %d cards, hand size %d",
				p.ID, len(p.Hand), p.HandSize)
		}
	}

	// I4/I5: exactly one in-play turn holder while claiming.
	if s.Phase == Claiming {
		if s.TurnSeat < 0 || s.TurnSeat >= len(s.Players) {
			return fmt.Errorf("I4: turn seat %d out of range", s.TurnSeat)
		}
		if !s.Players[s.TurnSeat].Status.InPlay() {
			return fmt.Errorf("I4: turn seat %d is not in play", s.TurnSeat)
		}
	}

	// I7: claims strictly increase within a round.
	for i := 1; i < len(s.Claims); i++ {
		if !poker.Higher(s.Claims[i].Claim, s.Claims[i-1].Claim) {
			return fmt.Errorf("I7: claim %d (%s) does not exceed %s",
				i, s.Claims[i].Claim, s.Claims[i-1].Claim)
		}
	}
	for _, cr := range s.Claims {
		if !cr.Claim.Legal() {
			return fmt.Errorf("I7: illegal claim recorded: %s", cr.Claim)
		}
	}

	// I8/I9: a recorded witness is the right width and includes a claimant
	// card under the active scope.
	if s.LastShow != nil && s.LastShow.ClaimTrue {
		w := s.LastShow.Witness
		if len(w) != s.LastShow.HandSize {
			return fmt.Errorf("I8: witness has %d cards, expected %d", len(w), s.LastShow.HandSize)
		}
		if !poker.Satisfies(w, s.LastShow.Claim) {
			return fmt.Errorf("I8: witness [%s] does not realize %s",
				poker.CardsString(w), s.LastShow.Claim)
		}
		owns := func(c poker.Card) bool { return s.Owner[c] == s.LastShow.Claimant }
		if !poker.OwnershipSatisfied(w, owns) {
			return fmt.Errorf("I9: witness [%s] contains no card owned by %s",
				poker.CardsString(w), s.LastShow.Claimant)
		}
	}

	// I13: a finished game has at most one player in play, and names a
	// winner exactly when one player remains. Zero remaining is an
	// abandoned game and correctly has no winner.
	if s.Phase == GameOver {
		if s.InPlayCount() > 1 {
			return fmt.Errorf("I13: game over with %d players in play", s.InPlayCount())
		}
		if s.InPlayCount() == 1 && s.Winner == "" {
			return fmt.Errorf("I13: one player remains but no winner is named")
		}
		if s.InPlayCount() == 0 && s.Winner != "" {
			return fmt.Errorf("I13: no players remain but %s is named winner", s.Winner)
		}
	} else if s.Phase != WaitingForPlayers && s.InPlayCount() < 2 {
		return fmt.Errorf("I13: %s with %d players in play", s.Phase, s.InPlayCount())
	}
	return nil
}

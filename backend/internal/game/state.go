// Package game is the deterministic Liar's Poker engine.
//
// The engine is a pure reducer: Apply(state, action) -> (state, events,
// error). It performs no I/O, owns no goroutines and reads no clock; time
// arrives on the action and randomness arrives as a seed in the config. The
// same (state, action) pair therefore always produces the same result, which
// is what makes simulation, replay and property testing possible.
package game

import (
	"errors"
	"time"

	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// Table limits from the rules.
const (
	MinPlayers = 4
	MaxPlayers = 9

	// StartingHandSize is what every player is dealt in round 1.
	StartingHandSize = 1
	// MaxHandSize is the largest hand a player may still play with.
	MaxHandSize = 5
	// EliminationHandSize is the hand size a player can never be dealt;
	// reaching it eliminates them instead.
	EliminationHandSize = 6

	// MaxPlayerIDLength bounds the identity a client may claim.
	MaxPlayerIDLength = 64
)

// PlayerID identifies a player within a room.
type PlayerID string

// Phase is the observable game phase.
//
// Round setup and show resolution are not phases: they are steps that run to
// completion inside a single Apply call, emitting their events as they go, so
// no client action can ever be legal in the middle of one. The only phases
// that accept player input are WaitingForPlayers and Claiming.
type Phase uint8

const (
	WaitingForPlayers Phase = iota
	Claiming
	RoundEnd
	GameOver
)

var phaseName = [...]string{"WAITING_FOR_PLAYERS", "CLAIMING", "ROUND_END", "GAME_OVER"}

func (p Phase) String() string {
	if int(p) < len(phaseName) {
		return phaseName[p]
	}
	return "INVALID"
}

// PlayerStatus tracks participation.
type PlayerStatus uint8

const (
	StatusActive PlayerStatus = iota
	StatusDisconnected
	StatusEliminated
)

var statusName = [...]string{"ACTIVE", "DISCONNECTED", "ELIMINATED"}

func (s PlayerStatus) String() string {
	if int(s) < len(statusName) {
		return statusName[s]
	}
	return "INVALID"
}

// InPlay reports whether the player still participates in rounds. A
// disconnected player is still dealt cards and still owns pool cards; only
// elimination removes them.
func (s PlayerStatus) InPlay() bool { return s == StatusActive || s == StatusDisconnected }

// Player is a seat at the table.
type Player struct {
	ID   PlayerID
	Name string
	Seat int
	// HandSize is the number of cards dealt next round: the penalty level.
	HandSize int
	Status   PlayerStatus
	Ready    bool
	// Hand is private. It leaves the engine only through View.
	Hand []poker.Card
}

// TimeoutPolicy is the consequence of exceeding the turn budget. The rules
// fix the budget at 30 seconds but leave the consequence open, so it is
// explicit and configurable rather than invented.
type TimeoutPolicy uint8

const (
	// TimeoutAutoShow calls SHOW for the player when they are eligible, and
	// otherwise submits the lowest legal claim. Default.
	TimeoutAutoShow TimeoutPolicy = iota
	// TimeoutMinimumClaim always submits the lowest legal claim.
	TimeoutMinimumClaim
	// TimeoutForfeitRound penalises the timed-out player and ends the round.
	TimeoutForfeitRound
)

// DisconnectPolicy is when a disconnected player leaves the game.
type DisconnectPolicy uint8

const (
	// DisconnectEliminateAtRoundEnd keeps the player in the current round,
	// so the frozen pool never changes mid-round, and eliminates them at
	// round end if they have not returned. Default.
	DisconnectEliminateAtRoundEnd DisconnectPolicy = iota
	// DisconnectEliminateImmediately removes them as soon as they drop.
	DisconnectEliminateImmediately
)

// FirstStarterRule picks the round-1 starter. Every later round rotates
// clockwise regardless of anything that happens in a round.
type FirstStarterRule uint8

const (
	FirstStarterRandom FirstStarterRule = iota
	FirstStarterSeatZero
)

// Config is the rule configuration. Defaults follow the rules as written.
type Config struct {
	TurnDuration     time.Duration
	RoundEndDuration time.Duration
	Timeout          TimeoutPolicy
	Disconnect       DisconnectPolicy
	FirstStarter     FirstStarterRule
	// Seed is the randomness injection point. Each round derives its
	// shuffle source from (Seed, Round), keeping State a pure value while
	// remaining fully reproducible.
	Seed int64
}

// DefaultConfig returns the configuration the rules specify.
func DefaultConfig(seed int64) Config {
	return Config{
		TurnDuration:     30 * time.Second,
		RoundEndDuration: 6 * time.Second,
		Timeout:          TimeoutAutoShow,
		Disconnect:       DisconnectEliminateAtRoundEnd,
		FirstStarter:     FirstStarterRandom,
		Seed:             seed,
	}
}

// ClaimRecord is one claim in the current round's chain.
type ClaimRecord struct {
	Seat     int
	PlayerID PlayerID
	Claim    poker.Claim
}

// ShowResult is the outcome of a resolved SHOW.
type ShowResult struct {
	Round      int         `json:"round"`
	Claimant   PlayerID    `json:"claimant"`
	Challenger PlayerID    `json:"challenger"`
	Claim      poker.Claim `json:"claim"`
	ClaimTrue  bool        `json:"claimTrue"`
	// Witness is always an array on the wire, empty when the claim was
	// false and there is nothing to show. Go marshals a nil slice as null,
	// and a client should not have to handle two spellings of "empty".
	Witness  []poker.Card   `json:"witness"`
	Loser    PlayerID       `json:"loser"`
	Mode     poker.EvalMode `json:"mode"`
	PoolSize int            `json:"poolSize"`
	HandSize int            `json:"handSize"`
}

// State is the complete authoritative game state. Everything private lives
// here; View is the only way any of it reaches a client.
type State struct {
	Phase   Phase
	Config  Config
	Players []Player // indexed by seat

	Round       int
	StarterSeat int
	TurnSeat    int
	Claims      []ClaimRecord

	// Pool is every dealt card, frozen at deal time. It does not change for
	// the rest of the round, so a disconnect can never retroactively alter
	// whether a claim is true.
	Pool  []poker.Card
	Owner map[poker.Card]PlayerID

	DeckRemaining int
	Revealed      bool
	LastShow      *ShowResult
	Winner        PlayerID

	Seq              int
	TurnDeadline     time.Time
	RoundEndDeadline time.Time
}

// NewGame returns an empty room in WaitingForPlayers.
func NewGame(cfg Config) State {
	return State{Phase: WaitingForPlayers, Config: cfg, Owner: map[poker.Card]PlayerID{}}
}

func (s State) clone() State {
	ns := s
	ns.Players = make([]Player, len(s.Players))
	copy(ns.Players, s.Players)
	for i := range ns.Players {
		ns.Players[i].Hand = append([]poker.Card(nil), s.Players[i].Hand...)
	}
	ns.Claims = append([]ClaimRecord(nil), s.Claims...)
	ns.Pool = append([]poker.Card(nil), s.Pool...)
	ns.Owner = make(map[poker.Card]PlayerID, len(s.Owner))
	for k, v := range s.Owner {
		ns.Owner[k] = v
	}
	if s.LastShow != nil {
		cp := *s.LastShow
		cp.Witness = append([]poker.Card(nil), s.LastShow.Witness...)
		ns.LastShow = &cp
	}
	return ns
}

// ---------------------------------------------------------------- lookups

func (s State) seatOf(id PlayerID) (int, bool) {
	for i := range s.Players {
		if s.Players[i].ID == id {
			return i, true
		}
	}
	return -1, false
}

func (s State) player(id PlayerID) (*Player, bool) {
	i, ok := s.seatOf(id)
	if !ok {
		return nil, false
	}
	return &s.Players[i], true
}

// InPlayCount is the number of players still in the game.
func (s State) InPlayCount() int {
	n := 0
	for i := range s.Players {
		if s.Players[i].Status.InPlay() {
			n++
		}
	}
	return n
}

// nextInPlaySeat returns the next seat clockwise from `from` that is still in
// play. Seats are fixed for the game's lifetime, so eliminated seats are
// skipped but still occupy an index; that is what keeps starter rotation
// independent of who was eliminated when.
func (s State) nextInPlaySeat(from int) int {
	n := len(s.Players)
	if n == 0 {
		return -1
	}
	for i := 1; i <= n; i++ {
		seat := (from + i) % n
		if s.Players[seat].Status.InPlay() {
			return seat
		}
	}
	return -1
}

// StandingClaim returns the claim that the current player must beat or
// challenge.
func (s State) StandingClaim() (ClaimRecord, bool) {
	if len(s.Claims) == 0 {
		return ClaimRecord{}, false
	}
	return s.Claims[len(s.Claims)-1], true
}

// PoolSize is the number of cards in play this round.
func (s State) PoolSize() int { return len(s.Pool) }

// EvalMode reports which validation width this round's pool uses.
func (s State) EvalMode() poker.EvalMode { return poker.ModeFor(len(s.Pool)) }

// CanShow reports whether the given player may call SHOW right now.
func (s State) CanShow(id PlayerID) bool {
	if s.Phase != Claiming || len(s.Claims) == 0 {
		return false
	}
	seat, ok := s.seatOf(id)
	return ok && seat == s.TurnSeat && s.Players[seat].Status.InPlay()
}

// CanClaim reports whether the given player may claim right now.
func (s State) CanClaim(id PlayerID) bool {
	if s.Phase != Claiming {
		return false
	}
	seat, ok := s.seatOf(id)
	if !ok || seat != s.TurnSeat || !s.Players[seat].Status.InPlay() {
		return false
	}
	if standing, ok := s.StandingClaim(); ok {
		return len(poker.ClaimsAbove(standing.Claim)) > 0
	}
	return true
}

// ---------------------------------------------------------------- actions

// Meta is the common action envelope. Now is supplied by the caller so the
// reducer never reads a clock; a zero Now disables timers, which is what
// tests use.
type Meta struct {
	PlayerID PlayerID
	Now      time.Time
}

// Action is a client or system intent. Clients send intents, never state.
type Action interface {
	meta() Meta
	name() string
}

type ActionJoin struct {
	Meta
	Name string
}
type ActionSetReady struct {
	Meta
	Ready bool
}
type ActionStartGame struct{ Meta }
type ActionMakeClaim struct {
	Meta
	Claim poker.Claim
}
type ActionCallShow struct{ Meta }
type ActionAdvanceRound struct{ Meta }
type ActionDisconnect struct{ Meta }
type ActionReconnect struct{ Meta }
type ActionTick struct{ Meta }

func (m Meta) meta() Meta { return m }

func (ActionJoin) name() string         { return "JOIN" }
func (ActionSetReady) name() string     { return "SET_READY" }
func (ActionStartGame) name() string    { return "START_GAME" }
func (ActionMakeClaim) name() string    { return "MAKE_CLAIM" }
func (ActionCallShow) name() string     { return "CALL_SHOW" }
func (ActionAdvanceRound) name() string { return "ADVANCE_ROUND" }
func (ActionDisconnect) name() string   { return "DISCONNECT" }
func (ActionReconnect) name() string    { return "RECONNECT" }
func (ActionTick) name() string         { return "TICK" }

// ---------------------------------------------------------------- events

// EventType names a state transition.
type EventType uint8

const (
	EvPlayerJoined EventType = iota
	EvPlayerReady
	EvGameStarted
	EvRoundStarted
	EvCardsDealt
	EvTurnStarted
	EvClaimMade
	EvShowCalled
	EvCardsRevealed
	EvClaimValidated
	EvPenaltyApplied
	EvPlayerEliminated
	EvRoundEnded
	EvGameEnded
	EvPlayerDisconnected
	EvPlayerReconnected
	EvTurnTimedOut
)

var eventName = [...]string{
	"PLAYER_JOINED", "PLAYER_READY", "GAME_STARTED", "ROUND_STARTED",
	"CARDS_DEALT", "TURN_STARTED", "CLAIM_MADE", "SHOW_CALLED",
	"CARDS_REVEALED", "CLAIM_VALIDATED", "PENALTY_APPLIED",
	"PLAYER_ELIMINATED", "ROUND_ENDED", "GAME_ENDED",
	"PLAYER_DISCONNECTED", "PLAYER_RECONNECTED", "TURN_TIMED_OUT",
}

func (e EventType) String() string {
	if int(e) < len(eventName) {
		return eventName[e]
	}
	return "INVALID"
}

// Event is an observable transition with a monotonic sequence number.
//
// Hands is populated only for EvCardsDealt and EvCardsRevealed. For
// EvCardsDealt the transport layer must project it per recipient; every
// other event is identical for every recipient, so message size and timing
// cannot be used to infer hidden cards.
type Event struct {
	Seq      int
	Type     EventType
	Round    int
	Player   PlayerID
	Seat     int
	Claim    *poker.Claim
	Hands    map[PlayerID][]poker.Card
	Result   *ShowResult
	HandSize int
	// AttemptedHandSize records the hand size a player would have been
	// dealt had they not been eliminated: always EliminationHandSize.
	AttemptedHandSize int
	Reason            string
	Deadline          time.Time
	PoolSize          int
	Mode              poker.EvalMode
}

// ---------------------------------------------------------------- errors

var (
	ErrWrongPhase      = errors.New("game: action not legal in this phase")
	ErrUnknownPlayer   = errors.New("game: unknown player")
	ErrInvalidPlayerID = errors.New("game: invalid player id")
	ErrNotYourTurn     = errors.New("game: not your turn")
	ErrNotShowEligible = errors.New("game: only the immediate next player may call SHOW")
	ErrNoStandingClaim = errors.New("game: no standing claim to challenge")
	ErrClaimNotHigher  = errors.New("game: claim must be strictly higher than the standing claim")
	ErrMalformedClaim  = errors.New("game: claim is not well-formed")
	ErrPlayerNotActive = errors.New("game: player is eliminated")
	ErrGameOver        = errors.New("game: the game is over")
	ErrNotEnoughPlayer = errors.New("game: not enough players")
	ErrTooManyPlayers  = errors.New("game: table is full")
	ErrGameInProgress  = errors.New("game: game already in progress")
	ErrAlreadyJoined   = errors.New("game: already joined")
	ErrNotAllReady     = errors.New("game: not all players are ready")
	ErrUnknownAction   = errors.New("game: unknown action")
)

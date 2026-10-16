package game

import (
	"time"

	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// PlayerSummary is what one player is allowed to know about another.
//
// Cards is populated only for the viewer themselves, or for everyone once
// the round's cards have been revealed by a SHOW. HandSize and CardCount are
// public: the penalty level is public information, the card values are not.
type PlayerSummary struct {
	ID        PlayerID     `json:"id"`
	Name      string       `json:"name"`
	Seat      int          `json:"seat"`
	HandSize  int          `json:"handSize"`
	CardCount int          `json:"cardCount"`
	Status    PlayerStatus `json:"-"`
	StatusStr string       `json:"status"`
	Ready     bool         `json:"ready"`
	IsYou     bool         `json:"isYou"`
	IsTurn    bool         `json:"isTurn"`
	IsStarter bool         `json:"isStarter"`
	Cards     []poker.Card `json:"cards,omitempty"`
}

// PlayerView is the sanitized state for one player.
//
// The claim space itself is static and public, so it is not carried here.
// StandingOrdinal is the floor: a client renders its claim builder over the
// static space filtered to ordinals strictly above it, which needs no rule
// knowledge on the client and is revalidated server-side regardless.
type PlayerView struct {
	You   PlayerID `json:"you"`
	Phase string   `json:"phase"`
	Round int      `json:"round"`
	Seq   int      `json:"seq"`

	Players []PlayerSummary `json:"players"`

	PoolSize  int    `json:"poolSize"`
	EvalMode  string `json:"evalMode"`
	HandWidth int    `json:"handWidth"`

	Claims          []ClaimView  `json:"claims"`
	StandingClaim   *poker.Claim `json:"standingClaim,omitempty"`
	StandingOrdinal int          `json:"standingOrdinal"`

	TurnPlayer  PlayerID `json:"turnPlayer,omitempty"`
	TurnSeat    int      `json:"turnSeat"`
	StarterSeat int      `json:"starterSeat"`
	YourTurn    bool     `json:"yourTurn"`
	CanClaim    bool     `json:"canClaim"`
	CanShow     bool     `json:"canShow"`

	TurnDeadline     time.Time `json:"turnDeadline,omitempty"`
	RoundEndDeadline time.Time `json:"roundEndDeadline,omitempty"`

	Revealed bool        `json:"revealed"`
	LastShow *ShowResult `json:"lastShow,omitempty"`
	Winner   PlayerID    `json:"winner,omitempty"`
}

// ClaimView is one claim in the round's chain, with its ordinal so clients
// can order and filter without knowing the rules.
type ClaimView struct {
	Player  PlayerID    `json:"player"`
	Seat    int         `json:"seat"`
	Claim   poker.Claim `json:"claim"`
	Text    string      `json:"text"`
	Ordinal int         `json:"ordinal"`
}

// View is the only function in the codebase that returns game state to a
// client. Concentrating the projection here means the information-leak test
// suite has exactly one target.
func View(s State, viewer PlayerID) PlayerView {
	standing, hasStanding := s.StandingClaim()
	v := PlayerView{
		You:              viewer,
		Phase:            s.Phase.String(),
		Round:            s.Round,
		Seq:              s.Seq,
		PoolSize:         len(s.Pool),
		EvalMode:         poker.ModeFor(len(s.Pool)).String(),
		HandWidth:        poker.HandSize(len(s.Pool)),
		StandingOrdinal:  -1,
		TurnSeat:         s.TurnSeat,
		StarterSeat:      s.StarterSeat,
		Revealed:         s.Revealed,
		Winner:           s.Winner,
		TurnDeadline:     s.TurnDeadline,
		RoundEndDeadline: s.RoundEndDeadline,
	}
	if hasStanding {
		c := standing.Claim
		v.StandingClaim = &c
		if o, ok := c.Ordinal(); ok {
			v.StandingOrdinal = o
		}
	}
	// Only CLAIMING has a turn holder. Reporting one during the reveal
	// window or after the game ends would have clients render an active
	// turn that no action can satisfy.
	if s.Phase == Claiming && s.TurnSeat >= 0 && s.TurnSeat < len(s.Players) {
		v.TurnPlayer = s.Players[s.TurnSeat].ID
		v.YourTurn = v.TurnPlayer == viewer
	}
	v.CanClaim = s.CanClaim(viewer)
	v.CanShow = s.CanShow(viewer)

	for i := range s.Players {
		p := s.Players[i]
		sum := PlayerSummary{
			ID:        p.ID,
			Name:      p.Name,
			Seat:      p.Seat,
			HandSize:  p.HandSize,
			CardCount: len(p.Hand),
			Status:    p.Status,
			StatusStr: p.Status.String(),
			Ready:     p.Ready,
			IsYou:     p.ID == viewer,
			IsTurn:    s.Phase == Claiming && p.Seat == s.TurnSeat,
			IsStarter: p.Seat == s.StarterSeat,
		}
		// The whole hidden-information rule, in one condition.
		if p.ID == viewer || s.Revealed {
			sum.Cards = append([]poker.Card(nil), p.Hand...)
		}
		v.Players = append(v.Players, sum)
	}

	for _, cr := range s.Claims {
		cv := ClaimView{Player: cr.PlayerID, Seat: cr.Seat, Claim: cr.Claim, Text: cr.Claim.String(), Ordinal: -1}
		if o, ok := cr.Claim.Ordinal(); ok {
			cv.Ordinal = o
		}
		v.Claims = append(v.Claims, cv)
	}

	if s.LastShow != nil {
		cp := *s.LastShow
		cp.Witness = copyCards(s.LastShow.Witness)
		v.LastShow = &cp
	}
	return normalizeView(v)
}

// normalizeView makes every array field in the projection an actual array.
//
// Go marshals a nil slice as JSON null, so an empty claim chain would arrive
// as `"claims": null` and a client indexing it would fail on an ordinary
// empty round rather than on an unusual one. Fixing it here, at the only
// projection boundary, makes the wire contract right for every client in
// every language rather than pushing the problem into each of them.
//
// PlayerSummary.Cards is deliberately left alone: it is tagged omitempty, so
// an empty hand is *absent* rather than null, and absence carries meaning
// there. It says "not yours to see", which is not the same statement as "has
// no cards".
func normalizeView(v PlayerView) PlayerView {
	if v.Players == nil {
		v.Players = []PlayerSummary{}
	}
	if v.Claims == nil {
		v.Claims = []ClaimView{}
	}
	if v.LastShow != nil && v.LastShow.Witness == nil {
		v.LastShow.Witness = []poker.Card{}
	}
	return v
}

// copyCards returns a non-nil copy, so an empty source yields an empty array
// rather than nil.
func copyCards(in []poker.Card) []poker.Card {
	out := make([]poker.Card, len(in))
	copy(out, in)
	return out
}

// LegalClaims returns the claims the current player may legally make. Used
// by the simulator and by bots; clients derive the same set by filtering the
// static claim space on StandingOrdinal.
func LegalClaims(s State) []poker.Claim {
	if s.Phase != Claiming {
		return nil
	}
	if standing, ok := s.StandingClaim(); ok {
		return poker.ClaimsAbove(standing.Claim)
	}
	return poker.AllClaims()
}

// LeakedCards returns any card visible in a view that the viewer does not
// own, when the round has not been revealed. It must always be empty; the
// leak test asserts that over every reachable state.
func LeakedCards(s State, viewer PlayerID) []poker.Card {
	if s.Revealed {
		return nil
	}
	v := View(s, viewer)
	var leaked []poker.Card
	for _, p := range v.Players {
		if p.ID == viewer {
			continue
		}
		leaked = append(leaked, p.Cards...)
	}
	if v.LastShow != nil {
		leaked = append(leaked, v.LastShow.Witness...)
	}
	return leaked
}

// ProjectEvent returns the copy of an event that may be sent to one viewer.
//
// This exists because EvCardsDealt carries every player's hand: the reducer
// is a pure value producing one event stream, so it cannot emit a different
// payload per recipient. That makes the raw stream unsafe to broadcast, and
// this function the only safe way to fan it out.
//
// EvCardsRevealed is public by definition and passes through whole. Every
// other event carrying Hands is reduced to the viewer's own hand. Events
// without Hands are returned unchanged.
//
// The returned event never aliases the input's slices or map, so a transport
// can mutate or serialise it freely.
func ProjectEvent(e Event, viewer PlayerID) Event {
	out := e

	// A ShowResult reaches the wire through an event as well as through a
	// view, so its witness is normalised on both paths.
	if e.Result != nil {
		cp := *e.Result
		cp.Witness = copyCards(e.Result.Witness)
		out.Result = &cp
	}

	if e.Hands == nil {
		return out
	}
	if e.Type == EvCardsRevealed {
		out.Hands = make(map[PlayerID][]poker.Card, len(e.Hands))
		for id, hand := range e.Hands {
			out.Hands[id] = copyCards(hand)
		}
		return out
	}
	out.Hands = map[PlayerID][]poker.Card{}
	if hand, ok := e.Hands[viewer]; ok {
		out.Hands[viewer] = copyCards(hand)
	}
	return out
}

// ProjectEvents projects a whole batch for one viewer.
func ProjectEvents(events []Event, viewer PlayerID) []Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]Event, len(events))
	for i, e := range events {
		out[i] = ProjectEvent(e, viewer)
	}
	return out
}

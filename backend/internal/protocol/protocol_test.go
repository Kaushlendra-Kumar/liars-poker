package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// TestEventMessageAlwaysProjects is the structural guarantee the transport
// leans on: there is no way to turn a game.Event into a wire message without
// projection happening, so a caller who forgot to project cannot leak.
func TestEventMessageAlwaysProjects(t *testing.T) {
	raw := game.Event{
		Seq:   7,
		Type:  game.EvCardsDealt,
		Round: 1,
		Hands: map[game.PlayerID][]poker.Card{
			"A": poker.MustParseCards("8s"),
			"B": poker.MustParseCards("8h"),
			"C": poker.MustParseCards("Kd"),
		},
		PoolSize: 3,
		Mode:     poker.ModeReducedPool,
	}

	for _, viewer := range []game.PlayerID{"A", "B", "C"} {
		msg := EventMessage(raw, viewer)
		payload := msg.Payload.(EventPayload)
		if len(payload.Hands) != 1 {
			t.Fatalf("viewer %s received %d hands, want 1", viewer, len(payload.Hands))
		}
		if _, ok := payload.Hands[viewer]; !ok {
			t.Fatalf("viewer %s did not receive their own hand", viewer)
		}
		// And the serialised form must not mention anyone else's card.
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		for other, hand := range raw.Hands {
			if other == viewer {
				continue
			}
			if strings.Contains(string(b), `"`+hand[0].Code()+`"`) {
				t.Fatalf("viewer %s frame leaked %s: %s", viewer, hand[0].Code(), b)
			}
		}
	}

	// A non-participant sees no hands at all.
	if n := len(EventMessage(raw, "Z").Payload.(EventPayload).Hands); n != 0 {
		t.Fatalf("a stranger received %d hands", n)
	}

	// A reveal is public and must pass through whole.
	reveal := raw
	reveal.Type = game.EvCardsRevealed
	if n := len(EventMessage(reveal, "A").Payload.(EventPayload).Hands); n != 3 {
		t.Fatalf("reveal delivered %d hands, want 3", n)
	}
}

func TestEventMessagesProjectsEveryElement(t *testing.T) {
	events := []game.Event{
		{Seq: 1, Type: game.EvRoundStarted},
		{Seq: 2, Type: game.EvCardsDealt, Hands: map[game.PlayerID][]poker.Card{
			"A": poker.MustParseCards("8s"), "B": poker.MustParseCards("8h")}},
		{Seq: 3, Type: game.EvTurnStarted, Deadline: time.Now()},
	}
	msgs := EventMessages(events, "A")
	if len(msgs) != 3 {
		t.Fatalf("got %d messages", len(msgs))
	}
	if n := len(msgs[1].Payload.(EventPayload).Hands); n != 1 {
		t.Fatalf("deal in a batch was not projected: %d hands", n)
	}
	if msgs[2].Payload.(EventPayload).Deadline == nil {
		t.Fatal("a set deadline should survive projection")
	}
	if msgs[0].Payload.(EventPayload).Deadline != nil {
		t.Fatal("a zero deadline should serialise as null, not a zero time")
	}
}

// TestCodeForCoversEveryEngineError makes sure a new engine error cannot
// quietly fall through to ERR_INTERNAL.
func TestCodeForCoversEveryEngineError(t *testing.T) {
	cases := map[error]Code{
		game.ErrWrongPhase:      CodeWrongPhase,
		game.ErrNotYourTurn:     CodeNotYourTurn,
		game.ErrNotShowEligible: CodeNotShowEligible,
		game.ErrNoStandingClaim: CodeNoStandingClaim,
		game.ErrClaimNotHigher:  CodeClaimNotHigher,
		game.ErrMalformedClaim:  CodeMalformedClaim,
		game.ErrPlayerNotActive: CodePlayerOut,
		game.ErrGameOver:        CodeGameOver,
		game.ErrNotEnoughPlayer: CodeNotEnough,
		game.ErrTooManyPlayers:  CodeTableFull,
		game.ErrGameInProgress:  CodeInProgress,
		game.ErrAlreadyJoined:   CodeAlreadyJoined,
		game.ErrNotAllReady:     CodeNotAllReady,
		game.ErrInvalidPlayerID: CodeInvalidPlayerID,
		game.ErrUnknownPlayer:   CodeUnknownPlayer,
		game.ErrUnknownAction:   CodeUnknownAction,
		ErrUnauthorized:         CodeUnauthorized,
		ErrRoomNotFound:         CodeRoomNotFound,
		ErrRoomClosed:           CodeRoomClosed,
		ErrRateLimited:          CodeRateLimited,
		ErrFrameTooLarge:        CodeTooLarge,
		ErrBadFrame:             CodeBadFrame,
	}
	for err, want := range cases {
		if got := CodeFor(err); got != want {
			t.Errorf("CodeFor(%v) = %s, want %s", err, got, want)
		}
		// Wrapping must not break the mapping.
		wrapped := errors.Join(errors.New("context"), err)
		if got := CodeFor(wrapped); got != want {
			t.Errorf("CodeFor(wrapped %v) = %s, want %s", err, got, want)
		}
		if want.Message() == "" {
			t.Errorf("%s has no message", want)
		}
	}
	if got := CodeFor(errors.New("something else")); got != CodeInternal {
		t.Errorf("unknown error mapped to %s", got)
	}
}

// TestErrorMessagesCarryNoCards guards the rule that an error never explains
// itself with hidden state.
func TestErrorMessagesCarryNoCards(t *testing.T) {
	for code := range codeMessage {
		msg := ErrorMessage("r1", code)
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		for _, card := range poker.NewDeck() {
			if strings.Contains(body, `"`+card.Code()+`"`) {
				t.Fatalf("%s mentions a card: %s", code, body)
			}
		}
		for _, banned := range []string{"witness", "pool", "owner", "seed", "deck"} {
			if strings.Contains(strings.ToLower(body), banned) {
				t.Fatalf("%s mentions %q: %s", code, banned, body)
			}
		}
	}
}

func TestDecodeInboundRejectsBadFrames(t *testing.T) {
	bad := []string{
		"",
		"not json",
		"{",
		"[]",
		`{"type":123}`,
		`{}`,
		`{"type":"join","unexpected":1}`,
		`{"type":"call_show","playerId":"p_other"}`,
		`{"type":"join","reqId":"` + strings.Repeat("x", 65) + `"}`,
	}
	for _, frame := range bad {
		if _, err := DecodeInbound([]byte(frame)); err == nil {
			t.Errorf("frame %q was accepted", frame)
		}
	}
	if _, err := DecodeInbound([]byte(`{"type":"call_show","reqId":"r"}`)); err != nil {
		t.Errorf("a valid frame was rejected: %v", err)
	}
	huge := make([]byte, MaxFrameBytes+1)
	if _, err := DecodeInbound(huge); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("oversized frame error = %v", err)
	}
}

func TestActionForMapsIntentsAndRejectsTheRest(t *testing.T) {
	now := time.Now()
	ok := []struct {
		in   Inbound
		want string
	}{
		{Inbound{Type: MsgSetReady, Payload: json.RawMessage(`{"ready":true}`)}, "SET_READY"},
		{Inbound{Type: MsgStartGame}, "START_GAME"},
		{Inbound{Type: MsgMakeClaim,
			Payload: json.RawMessage(`{"claim":{"category":"PAIR","primary":"8"}}`)}, "MAKE_CLAIM"},
		{Inbound{Type: MsgCallShow}, "CALL_SHOW"},
		{Inbound{Type: MsgLeave}, "DISCONNECT"},
	}
	for _, tc := range ok {
		act, err := ActionFor(tc.in, "p_a", now)
		if err != nil {
			t.Fatalf("%s: %v", tc.in.Type, err)
		}
		if act == nil {
			t.Fatalf("%s produced no action", tc.in.Type)
		}
	}

	// Anything that is not an engine intent, including a client-sent tick or
	// advance_round, is unknown. Round advancement is the room timer's job.
	for _, typ := range []ClientMessageType{
		"tick", "advance_round", "join", "resume", "pong", "apply", "",
	} {
		if _, err := ActionFor(Inbound{Type: typ}, "p_a", now); !errors.Is(err, game.ErrUnknownAction) {
			t.Errorf("%q produced %v, want ErrUnknownAction", typ, err)
		}
	}

	// The actor comes from the caller, never from the frame.
	act, err := ActionFor(Inbound{Type: MsgCallShow}, "p_authenticated", now)
	if err != nil {
		t.Fatal(err)
	}
	show, ok2 := act.(game.ActionCallShow)
	if !ok2 {
		t.Fatalf("got %T", act)
	}
	if show.PlayerID != "p_authenticated" || !show.Now.Equal(now) {
		t.Fatalf("action metadata = %v / %v", show.PlayerID, show.Now)
	}
}

// TestActionForDoesNoRuleChecking pins the separation of concerns: a claim
// that the engine will reject must still be built here, because deciding
// legality is the engine's job and duplicating it would create a second
// source of truth.
func TestActionForDoesNoRuleChecking(t *testing.T) {
	in := Inbound{Type: MsgMakeClaim,
		Payload: json.RawMessage(`{"claim":{"category":"ROYAL_FLUSH","suit":"SPADES"}}`)}
	act, err := ActionFor(in, "p_a", time.Now())
	if err != nil {
		t.Fatalf("the protocol refused a claim it should have forwarded: %v", err)
	}
	claim := act.(game.ActionMakeClaim).Claim
	if claim != poker.NewRoyalFlush(poker.Spades) {
		t.Fatalf("claim decoded as %s", claim)
	}
	// A structurally impossible claim also gets through; only the engine
	// rejects it, and it does so as ERR_MALFORMED_CLAIM.
	bad := Inbound{Type: MsgMakeClaim,
		Payload: json.RawMessage(`{"claim":{"category":"HIGH_CARD","primary":"2"}}`)}
	act2, err := ActionFor(bad, "p_a", time.Now())
	if err != nil {
		t.Fatalf("the protocol pre-judged a malformed claim: %v", err)
	}
	if act2.(game.ActionMakeClaim).Claim.Legal() {
		t.Fatal("High Card 2 should not be a legal claim")
	}
}

func TestClaimSpaceMessageIsTheWholeLadder(t *testing.T) {
	msg := ClaimSpaceMessage()
	p := msg.Payload.(ClaimSpacePayload)
	if p.Size != poker.ClaimSpaceSize || len(p.Claims) != poker.ClaimSpaceSize {
		t.Fatalf("claim space message carries %d/%d entries, want %d",
			p.Size, len(p.Claims), poker.ClaimSpaceSize)
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Payload ClaimSpacePayload `json:"payload"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	for i := range p.Claims {
		if back.Payload.Claims[i] != p.Claims[i] {
			t.Fatalf("claim %d did not round trip: %s vs %s", i, back.Payload.Claims[i], p.Claims[i])
		}
	}
}

func TestViewRoundTripsThroughTheWire(t *testing.T) {
	s := game.NewGame(game.DefaultConfig(1))
	for _, id := range []game.PlayerID{"a", "b", "c", "d"} {
		var err error
		s, _, err = game.Apply(s, game.ActionJoin{Meta: game.Meta{PlayerID: id}, Name: string(id)})
		if err != nil {
			t.Fatal(err)
		}
		s, _, err = game.Apply(s, game.ActionSetReady{Meta: game.Meta{PlayerID: id}, Ready: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	s, _, err := game.Apply(s, game.ActionStartGame{Meta: game.Meta{PlayerID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	// Resolve a SHOW so the view carries a result with a witness and a mode.
	turn := s.Players[s.TurnSeat].ID
	s, _, err = game.Apply(s, game.ActionMakeClaim{
		Meta: game.Meta{PlayerID: turn}, Claim: poker.NewPair(poker.Two)})
	if err != nil {
		t.Fatal(err)
	}
	next := s.Players[s.TurnSeat].ID
	s, _, err = game.Apply(s, game.ActionCallShow{Meta: game.Meta{PlayerID: next}})
	if err != nil {
		t.Fatal(err)
	}

	msg := ViewMessage(game.View(s, "a"))
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Payload ViewPayload `json:"payload"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("a view did not survive the wire: %v", err)
	}
	v := back.Payload.View
	if v.Phase != game.RoundEnd.String() {
		t.Fatalf("phase decoded as %q", v.Phase)
	}
	if v.LastShow == nil {
		t.Fatal("the show result did not survive")
	}
	if v.LastShow.Mode.String() == "" {
		t.Fatal("the eval mode did not survive")
	}
	if len(v.Players) != 4 {
		t.Fatalf("decoded %d players", len(v.Players))
	}
}

// Package protocol is the wire vocabulary. It defines the envelope, the
// client and server message sets, and the mapping from engine errors to
// stable wire codes.
//
// It contains no gameplay logic. It does not compare claims, evaluate hands,
// decide eligibility or filter cards by rule. Its one substantive
// responsibility is that every event leaving the process passes through
// game.ProjectEvent, which it enforces by making projection part of the only
// exported constructor for an event message.
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// MaxFrameBytes is the inbound frame limit. No legal client message comes
// close: the largest is a claim, which is a few short strings.
const MaxFrameBytes = 4096

// ProtocolVersion is the wire contract version.
//
// Versioning strategy. Adding an optional field, a new event type or a new
// error code is backwards compatible and does NOT bump the version: clients
// ignore what they do not know. Removing or renaming a field, changing a
// field's type or meaning, changing an error code's meaning, or changing the
// ordering of a required sequence does bump it.
//
// A client declares the version it speaks as "v" on its opening join or
// resume. Omitting it means "unspecified" and is accepted, so an old client
// is not locked out by the field's introduction; declaring anything other
// than an accepted version is rejected with ERR_UNSUPPORTED_VERSION. The
// server always states its own version in the welcome payload.
const ProtocolVersion = 1

// VersionSupported reports whether the server speaks a client's declared
// version. Zero means the client did not declare one.
func VersionSupported(v int) bool { return v == 0 || v == ProtocolVersion }

// ---------------------------------------------------------------- types

// ClientMessageType is a client intent. This is the complete set; anything
// else is ErrUnknownAction.
//
// There is deliberately no "tick" and no "advance_round". game.ActionTick is
// engine-internal, and round advancement belongs to the room's timer so that
// one player cannot cut the reveal window short for everybody else.
type ClientMessageType string

const (
	MsgJoin      ClientMessageType = "join"
	MsgResume    ClientMessageType = "resume"
	MsgSetReady  ClientMessageType = "set_ready"
	MsgStartGame ClientMessageType = "start_game"
	MsgMakeClaim ClientMessageType = "make_claim"
	MsgCallShow  ClientMessageType = "call_show"
	MsgLeave     ClientMessageType = "leave"
	MsgPong      ClientMessageType = "pong"
)

// ServerMessageType is a server message.
type ServerMessageType string

const (
	MsgWelcome    ServerMessageType = "welcome"
	MsgSnapshot   ServerMessageType = "snapshot"
	MsgEvent      ServerMessageType = "event"
	MsgView       ServerMessageType = "view"
	MsgError      ServerMessageType = "error"
	MsgClaimSpace ServerMessageType = "claim_space"
	MsgPing       ServerMessageType = "ping"
	MsgClosed     ServerMessageType = "closed"
)

// ---------------------------------------------------------------- inbound

// Inbound is the client envelope. ReqID is opaque and echoed back so a
// client can match a request to its outcome; the server never interprets it.
type Inbound struct {
	Type ClientMessageType `json:"type"`
	// Version is declared on the opening join or resume and ignored
	// elsewhere. Zero means unspecified.
	Version int `json:"v,omitempty"`
	// ReqID is opaque and echoed back. It is also the idempotency key: see
	// the note on duplicate suppression in protocol.md. A client must use a
	// fresh ReqID per distinct action, because replaying one is treated as a
	// retry of the same intent and is not applied twice.
	ReqID   string          `json:"reqId,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type JoinPayload struct {
	RoomCode string `json:"roomCode"`
	Name     string `json:"name"`
}

type ResumePayload struct {
	RoomCode string        `json:"roomCode"`
	PlayerID game.PlayerID `json:"playerId"`
	Token    string        `json:"token"`
	LastSeq  int           `json:"lastSeq"`
}

type SetReadyPayload struct {
	Ready bool `json:"ready"`
}

type MakeClaimPayload struct {
	Claim poker.Claim `json:"claim"`
}

// MaxNameBytes bounds a display name.
const MaxNameBytes = 32

// SanitizeName makes a client-supplied display name safe to echo to other
// players.
//
// Slicing to a byte limit is not enough: cutting a multi-byte rune in half
// produces invalid UTF-8, which then round-trips through JSON as replacement
// characters and can come out longer than the limit it was meant to enforce.
// So the name is made valid first, stripped of control characters, and only
// then truncated on a rune boundary.
func SanitizeName(name string) string {
	name = strings.ToValidUTF8(name, "")
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if len(name) <= MaxNameBytes {
		return name
	}
	cut := MaxNameBytes
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}

// DecodeInbound parses and size-checks a client frame.
func DecodeInbound(frame []byte) (Inbound, error) {
	if len(frame) > MaxFrameBytes {
		return Inbound{}, ErrFrameTooLarge
	}
	// A WebSocket text frame must carry valid UTF-8. Accepting invalid bytes
	// lets mojibake into names and views, and encoding/json is lenient
	// enough not to catch it.
	if !utf8.Valid(frame) {
		return Inbound{}, fmt.Errorf("%w: invalid UTF-8", ErrBadFrame)
	}
	var in Inbound
	dec := json.NewDecoder(bytes.NewReader(frame))
	// Unknown fields are a protocol violation, not something to ignore: a
	// client sending a playerId alongside an action is trying to
	// impersonate, and silently dropping it would hide that.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return Inbound{}, fmt.Errorf("%w: %v", ErrBadFrame, err)
	}
	if in.Type == "" {
		return Inbound{}, ErrBadFrame
	}
	if len(in.ReqID) > 64 {
		return Inbound{}, ErrBadFrame
	}
	return in, nil
}

// DecodePayload unmarshals a typed payload, rejecting unknown fields for the
// same reason as above.
func DecodePayload[T any](raw json.RawMessage, out *T) error {
	if len(raw) == 0 {
		return nil // an empty payload is legal for start_game, call_show, leave, pong
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", ErrBadFrame, err)
	}
	return nil
}

// ---------------------------------------------------------------- outbound

// Outbound is the server envelope.
type Outbound struct {
	Type    ServerMessageType `json:"type"`
	Seq     int               `json:"seq,omitempty"`
	ReqID   string            `json:"reqId,omitempty"`
	Payload any               `json:"payload,omitempty"`
}

// Encode serialises a server message.
func Encode(m Outbound) ([]byte, error) { return json.Marshal(m) }

type WelcomePayload struct {
	// Version is the contract the server speaks.
	Version  int             `json:"v"`
	PlayerID game.PlayerID   `json:"playerId"`
	RoomCode string          `json:"roomCode"`
	Token    string          `json:"token"`
	View     game.PlayerView `json:"view"`
}

type SnapshotPayload struct {
	View game.PlayerView `json:"view"`
}

type ViewPayload struct {
	View game.PlayerView `json:"view"`
}

type ClaimSpacePayload struct {
	Size   int           `json:"size"`
	Claims []poker.Claim `json:"claims"`
}

type ErrorPayload struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

type ClosedPayload struct {
	Reason string `json:"reason"`
}

// ClaimSpaceMessage is the static claim ladder, sent once per connection.
// Because it never changes, views carry only StandingOrdinal and a client
// filters this list by integer comparison rather than by knowing any rule.
func ClaimSpaceMessage() Outbound {
	claims := poker.AllClaims()
	if claims == nil {
		// Defensive: the ladder is never empty, but the wire contract says
		// this field is an array, so it must not be able to marshal as null.
		claims = []poker.Claim{}
	}
	return Outbound{
		Type:    MsgClaimSpace,
		Payload: ClaimSpacePayload{Size: poker.ClaimSpaceSize, Claims: claims},
	}
}

func ViewMessage(v game.PlayerView) Outbound {
	return Outbound{Type: MsgView, Seq: v.Seq, Payload: ViewPayload{View: v}}
}

func SnapshotMessage(v game.PlayerView) Outbound {
	return Outbound{Type: MsgSnapshot, Seq: v.Seq, Payload: SnapshotPayload{View: v}}
}

// WelcomeMessage carries the rejoin token. This is the only server message
// that ever contains it.
func WelcomeMessage(id game.PlayerID, roomCode, token string, v game.PlayerView) Outbound {
	return Outbound{
		Type: MsgWelcome,
		Seq:  v.Seq,
		Payload: WelcomePayload{Version: ProtocolVersion, PlayerID: id,
			RoomCode: roomCode, Token: token, View: v},
	}
}

func ErrorMessage(reqID string, code Code) Outbound {
	return Outbound{
		Type:    MsgError,
		ReqID:   reqID,
		Payload: ErrorPayload{Code: code, Message: code.Message()},
	}
}

func PingMessage() Outbound { return Outbound{Type: MsgPing} }

func ClosedMessage(reason string) Outbound {
	return Outbound{Type: MsgClosed, Payload: ClosedPayload{Reason: reason}}
}

// ---------------------------------------------------------------- events

// EventPayload is the wire form of one engine event. It exists so the
// protocol has stable field names independent of the engine's struct layout.
type EventPayload struct {
	Seq               int                            `json:"seq"`
	Type              string                         `json:"type"`
	Round             int                            `json:"round"`
	Player            game.PlayerID                  `json:"player,omitempty"`
	Seat              int                            `json:"seat"`
	Claim             *poker.Claim                   `json:"claim"`
	Hands             map[game.PlayerID][]poker.Card `json:"hands"`
	Result            *game.ShowResult               `json:"result"`
	HandSize          int                            `json:"handSize,omitempty"`
	AttemptedHandSize int                            `json:"attemptedHandSize,omitempty"`
	Reason            string                         `json:"reason,omitempty"`
	Deadline          *time.Time                     `json:"deadline"`
	PoolSize          int                            `json:"poolSize,omitempty"`
	Mode              poker.EvalMode                 `json:"mode"`
}

// EventMessage builds the event message for one recipient.
//
// Projection happens here, inside the only exported path from a game.Event to
// the wire. game.ProjectEvent is idempotent, so a caller that has already
// projected loses nothing, and a caller that forgot cannot leak: there is no
// way to construct an event message from an unprojected event.
func EventMessage(e game.Event, viewer game.PlayerID) Outbound {
	p := game.ProjectEvent(e, viewer)
	payload := EventPayload{
		Seq:               p.Seq,
		Type:              p.Type.String(),
		Round:             p.Round,
		Player:            p.Player,
		Seat:              p.Seat,
		Claim:             p.Claim,
		Hands:             p.Hands,
		Result:            p.Result,
		HandSize:          p.HandSize,
		AttemptedHandSize: p.AttemptedHandSize,
		Reason:            p.Reason,
		PoolSize:          p.PoolSize,
		Mode:              p.Mode,
	}
	if !p.Deadline.IsZero() {
		d := p.Deadline
		payload.Deadline = &d
	}
	return Outbound{Type: MsgEvent, Seq: p.Seq, Payload: payload}
}

// EventMessages projects and wraps a batch for one recipient.
func EventMessages(events []game.Event, viewer game.PlayerID) []Outbound {
	if len(events) == 0 {
		return nil
	}
	out := make([]Outbound, 0, len(events))
	for _, e := range events {
		out = append(out, EventMessage(e, viewer))
	}
	return out
}

// ---------------------------------------------------------------- errors

// Code is a stable wire error code. Clients branch on these, never on the
// message text.
type Code string

const (
	CodeWrongPhase      Code = "ERR_WRONG_PHASE"
	CodeNotYourTurn     Code = "ERR_NOT_YOUR_TURN"
	CodeNotShowEligible Code = "ERR_NOT_SHOW_ELIGIBLE"
	CodeNoStandingClaim Code = "ERR_NO_STANDING_CLAIM"
	CodeClaimNotHigher  Code = "ERR_CLAIM_NOT_HIGHER"
	CodeMalformedClaim  Code = "ERR_MALFORMED_CLAIM"
	CodePlayerOut       Code = "ERR_PLAYER_ELIMINATED"
	CodeGameOver        Code = "ERR_GAME_OVER"
	CodeNotEnough       Code = "ERR_NOT_ENOUGH_PLAYERS"
	CodeTableFull       Code = "ERR_TABLE_FULL"
	CodeInProgress      Code = "ERR_GAME_IN_PROGRESS"
	CodeAlreadyJoined   Code = "ERR_ALREADY_JOINED"
	CodeNotAllReady     Code = "ERR_NOT_ALL_READY"
	CodeInvalidPlayerID Code = "ERR_INVALID_PLAYER_ID"
	CodeUnknownPlayer   Code = "ERR_UNKNOWN_PLAYER"
	CodeUnknownAction   Code = "ERR_UNKNOWN_ACTION"
	CodeUnauthorized    Code = "ERR_UNAUTHORIZED"
	CodeRoomNotFound    Code = "ERR_ROOM_NOT_FOUND"
	CodeRoomClosed      Code = "ERR_ROOM_CLOSED"
	CodeRateLimited     Code = "ERR_RATE_LIMITED"
	CodeTooLarge        Code = "ERR_MESSAGE_TOO_LARGE"
	CodeBadFrame        Code = "ERR_BAD_FRAME"
	CodeInternal        Code = "ERR_INTERNAL"

	CodeUnsupportedVersion Code = "ERR_UNSUPPORTED_VERSION"
	CodeRoomLimit          Code = "ERR_ROOM_LIMIT"
	CodeConnectionLimit    Code = "ERR_CONNECTION_LIMIT"
	CodeBusy               Code = "ERR_SERVER_BUSY"
)

// Transport-level errors, which have no engine equivalent.
var (
	ErrFrameTooLarge = errors.New("protocol: frame exceeds size limit")
	ErrBadFrame      = errors.New("protocol: malformed frame")
	ErrUnauthorized  = errors.New("protocol: unauthorized")
	ErrRoomNotFound  = errors.New("protocol: room not found")
	ErrRoomClosed    = errors.New("protocol: room closed")
	ErrRateLimited   = errors.New("protocol: rate limited")

	ErrUnsupportedVersion = errors.New("protocol: unsupported version")
	ErrRoomLimit          = errors.New("protocol: room limit reached")
	ErrConnectionLimit    = errors.New("protocol: connection limit reached")
	ErrBusy               = errors.New("protocol: server busy")
)

// codeMessage is a fixed, safe string per code.
//
// The message never derives from the error value. Engine errors can wrap a
// description of the offending claim, and while that is the client's own
// input rather than hidden state, a fixed table removes the whole question
// of whether any error text could ever carry a card, a witness or an
// evaluator internal.
var codeMessage = map[Code]string{
	CodeWrongPhase:      "that action is not legal in this phase",
	CodeNotYourTurn:     "it is not your turn",
	CodeNotShowEligible: "only the immediate next player may call SHOW",
	CodeNoStandingClaim: "there is no standing claim to challenge",
	CodeClaimNotHigher:  "your claim must be strictly higher than the standing claim",
	CodeMalformedClaim:  "that is not a well-formed claim",
	CodePlayerOut:       "you are eliminated",
	CodeGameOver:        "the game is over",
	CodeNotEnough:       "not enough players to start",
	CodeTableFull:       "the table is full",
	CodeInProgress:      "the game is already in progress",
	CodeAlreadyJoined:   "you have already joined",
	CodeNotAllReady:     "not all players are ready",
	CodeInvalidPlayerID: "invalid player id",
	CodeUnknownPlayer:   "unknown player",
	CodeUnknownAction:   "unknown action",
	CodeUnauthorized:    "unauthorized",
	CodeRoomNotFound:    "room not found",
	CodeRoomClosed:      "room closed",
	CodeRateLimited:     "too many messages",
	CodeTooLarge:        "message too large",
	CodeBadFrame:        "malformed message",
	CodeInternal:        "internal error",

	CodeUnsupportedVersion: "unsupported protocol version",
	CodeRoomLimit:          "the server is at its room limit",
	CodeConnectionLimit:    "too many connections for this room",
	CodeBusy:               "the server is busy, try again",
}

// Message returns the safe human string for a code.
func (c Code) Message() string {
	if m, ok := codeMessage[c]; ok {
		return m
	}
	return codeMessage[CodeInternal]
}

// CodeFor maps an error to its wire code. Engine errors map one to one; the
// mapping is a table lookup rather than a judgement.
func CodeFor(err error) Code {
	switch {
	case err == nil:
		return CodeInternal
	case errors.Is(err, game.ErrWrongPhase):
		return CodeWrongPhase
	case errors.Is(err, game.ErrNotYourTurn):
		return CodeNotYourTurn
	case errors.Is(err, game.ErrNotShowEligible):
		return CodeNotShowEligible
	case errors.Is(err, game.ErrNoStandingClaim):
		return CodeNoStandingClaim
	case errors.Is(err, game.ErrClaimNotHigher):
		return CodeClaimNotHigher
	case errors.Is(err, game.ErrMalformedClaim):
		return CodeMalformedClaim
	case errors.Is(err, game.ErrPlayerNotActive):
		return CodePlayerOut
	case errors.Is(err, game.ErrGameOver):
		return CodeGameOver
	case errors.Is(err, game.ErrNotEnoughPlayer):
		return CodeNotEnough
	case errors.Is(err, game.ErrTooManyPlayers):
		return CodeTableFull
	case errors.Is(err, game.ErrGameInProgress):
		return CodeInProgress
	case errors.Is(err, game.ErrAlreadyJoined):
		return CodeAlreadyJoined
	case errors.Is(err, game.ErrNotAllReady):
		return CodeNotAllReady
	case errors.Is(err, game.ErrInvalidPlayerID):
		return CodeInvalidPlayerID
	case errors.Is(err, game.ErrUnknownPlayer):
		return CodeUnknownPlayer
	case errors.Is(err, game.ErrUnknownAction):
		return CodeUnknownAction
	case errors.Is(err, ErrUnauthorized):
		return CodeUnauthorized
	case errors.Is(err, ErrRoomNotFound):
		return CodeRoomNotFound
	case errors.Is(err, ErrRoomClosed):
		return CodeRoomClosed
	case errors.Is(err, ErrRateLimited):
		return CodeRateLimited
	case errors.Is(err, ErrFrameTooLarge):
		return CodeTooLarge
	case errors.Is(err, ErrBadFrame):
		return CodeBadFrame
	case errors.Is(err, ErrUnsupportedVersion):
		return CodeUnsupportedVersion
	case errors.Is(err, ErrRoomLimit):
		return CodeRoomLimit
	case errors.Is(err, ErrConnectionLimit):
		return CodeConnectionLimit
	case errors.Is(err, ErrBusy):
		return CodeBusy
	}
	return CodeInternal
}

// ActionFor turns a decoded client message into an engine action. The actor
// and the clock are supplied by the caller from the authenticated session and
// the server clock, never from the frame, which is what makes the turn budget
// authoritative and impersonation impossible.
//
// This function performs no rule checks. Everything it produces goes to
// game.Apply, which is the only thing that decides whether an action is legal.
func ActionFor(in Inbound, actor game.PlayerID, now time.Time) (game.Action, error) {
	meta := game.Meta{PlayerID: actor, Now: now}
	switch in.Type {
	case MsgSetReady:
		var p SetReadyPayload
		if err := DecodePayload(in.Payload, &p); err != nil {
			return nil, err
		}
		return game.ActionSetReady{Meta: meta, Ready: p.Ready}, nil
	case MsgStartGame:
		return game.ActionStartGame{Meta: meta}, nil
	case MsgMakeClaim:
		var p MakeClaimPayload
		if err := DecodePayload(in.Payload, &p); err != nil {
			return nil, err
		}
		return game.ActionMakeClaim{Meta: meta, Claim: p.Claim}, nil
	case MsgCallShow:
		return game.ActionCallShow{Meta: meta}, nil
	case MsgLeave:
		return game.ActionDisconnect{Meta: meta}, nil
	}
	// join and resume are connection lifecycle, not engine actions. Anything
	// else, including a client-sent tick or advance_round, is unknown.
	return nil, game.ErrUnknownAction
}

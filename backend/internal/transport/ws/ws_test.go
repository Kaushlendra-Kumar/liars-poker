package ws

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/internal/protocol"
	"github.com/kaushlendra/liars-poker/backend/internal/room"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// These are the Phase 3 acceptance tests. They exercise the real transport,
// the real room actor and the real engine; only the socket is substituted.
// That keeps malformed frames, oversized frames, heartbeat expiry and
// backpressure deterministic instead of timing-dependent.

// ---------------------------------------------------------------- fake conn

type fakeConn struct {
	mu      sync.Mutex
	in      chan []byte
	out     [][]byte
	closed  bool
	closeCh chan struct{}
	once    sync.Once
	stall   bool
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan []byte, 64), closeCh: make(chan struct{})}
}

func (f *fakeConn) Read() ([]byte, error) {
	select {
	case b, ok := <-f.in:
		if !ok {
			return nil, io.EOF
		}
		return b, nil
	case <-f.closeCh:
		return nil, io.EOF
	}
}

func (f *fakeConn) Write(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return io.ErrClosedPipe
	}
	if f.stall {
		return io.ErrClosedPipe
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	f.out = append(f.out, cp)
	return nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	f.once.Do(func() { close(f.closeCh) })
	return nil
}

func (f *fakeConn) frames() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.out))
	copy(out, f.out)
	return out
}

// framesFrom returns only the frames written since start.
//
// Polling helpers used to rescan and re-unmarshal every frame ever written,
// which made a long game quadratic in the number of frames. That is
// invisible without the race detector and painful with it.
func (f *fakeConn) framesFrom(start int) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if start >= len(f.out) {
		return nil
	}
	out := make([][]byte, len(f.out)-start)
	copy(out, f.out[start:])
	return out
}

func (f *fakeConn) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// ---------------------------------------------------------------- clock

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// ---------------------------------------------------------------- harness

type serverMsg struct {
	Type    string          `json:"type"`
	Seq     int             `json:"seq"`
	ReqID   string          `json:"reqId"`
	Payload json.RawMessage `json:"payload"`
}

type harness struct {
	t     *testing.T
	mgr   *room.Manager
	clock *fakeClock
	opts  Options
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clock := newClock()
	cfg := game.DefaultConfig(20260916)
	cfg.FirstStarter = game.FirstStarterSeatZero
	mgr := room.NewManager(room.Options{
		Game:         cfg,
		TickInterval: 0, // tests drive time
		Now:          clock.Now,
		ResultLinger: 30 * time.Second,
		EmptyGrace:   10 * time.Second,
		IdleTimeout:  10 * time.Minute,
	})
	h := &harness{t: t, mgr: mgr, clock: clock}
	h.opts = Options{
		Manager:      mgr,
		Now:          clock.Now,
		PingInterval: 0, // heartbeat off unless a test wants it
		RateBurst:    1000,
		RateWindow:   time.Second,
	}
	t.Cleanup(func() { mgr.Shutdown("test over") })
	return h
}

func (h *harness) newRoom() *room.Room {
	h.t.Helper()
	rm, err := h.mgr.Create()
	if err != nil {
		h.t.Fatalf("create room: %v", err)
	}
	return rm
}

type client struct {
	t    *testing.T
	conn *fakeConn
	gone chan struct{}

	// cursor is how far into the frame log this client has parsed, so
	// repeated polling costs only the new frames.
	cursor   int
	view     game.PlayerView
	haveView bool
}

func (h *harness) connect() *client {
	return h.connectWith(h.opts)
}

func (h *harness) connectWith(opts Options) *client {
	c := &client{t: h.t, conn: newFakeConn(), gone: make(chan struct{})}
	go func() {
		defer close(c.gone)
		Serve(c.conn, opts)
	}()
	return c
}

func (c *client) send(typ protocol.ClientMessageType, payload any, reqID string) {
	c.t.Helper()
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			c.t.Fatalf("marshal payload: %v", err)
		}
		raw = b
	}
	frame, err := json.Marshal(protocol.Inbound{Type: typ, ReqID: reqID, Payload: raw})
	if err != nil {
		c.t.Fatalf("marshal frame: %v", err)
	}
	c.sendRaw(frame)
}

func (c *client) sendRaw(frame []byte) {
	select {
	case c.conn.in <- frame:
	case <-c.conn.closeCh:
	case <-time.After(2 * time.Second):
		c.t.Fatal("send blocked")
	}
}

func (c *client) msgs() []serverMsg {
	c.t.Helper()
	frames := c.conn.frames()
	out := make([]serverMsg, 0, len(frames))
	for _, f := range frames {
		var m serverMsg
		if err := json.Unmarshal(f, &m); err != nil {
			c.t.Fatalf("server sent unparseable frame %q: %v", f, err)
		}
		out = append(out, m)
	}
	return out
}

func (c *client) await(kind string, d time.Duration) serverMsg {
	c.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, m := range c.msgs() {
			if m.Type == kind {
				return m
			}
		}
		time.Sleep(time.Millisecond)
	}
	c.t.Fatalf("timed out waiting for %q; got %v", kind, c.types())
	return serverMsg{}
}

func (c *client) types() []string {
	var out []string
	for _, m := range c.msgs() {
		out = append(out, m.Type)
	}
	return out
}

func (c *client) lastView() (game.PlayerView, bool) {
	c.t.Helper()
	return c.lastViewNoFatal()
}

func (c *client) awaitView(pred func(game.PlayerView) bool, d time.Duration) game.PlayerView {
	c.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if v, ok := c.lastView(); ok && pred(v) {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	v, _ := c.lastView()
	c.t.Fatalf("view predicate never satisfied; last view phase=%s round=%d yourTurn=%v",
		v.Phase, v.Round, v.YourTurn)
	return v
}

func (c *client) errorCodes() []protocol.Code {
	c.t.Helper()
	var out []protocol.Code
	for _, m := range c.msgs() {
		if m.Type != string(protocol.MsgError) {
			continue
		}
		var p protocol.ErrorPayload
		if err := json.Unmarshal(m.Payload, &p); err != nil {
			c.t.Fatalf("bad error payload: %v", err)
		}
		out = append(out, p.Code)
	}
	return out
}

func (c *client) welcome() protocol.WelcomePayload {
	c.t.Helper()
	m := c.await(string(protocol.MsgWelcome), 2*time.Second)
	var p protocol.WelcomePayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		c.t.Fatalf("bad welcome: %v", err)
	}
	return p
}

// seatTable joins n players and readies them, returning their clients.
func (h *harness) seatTable(rm *room.Room, n int) []*client {
	h.t.Helper()
	clients := make([]*client, 0, n)
	for i := 0; i < n; i++ {
		c := h.connect()
		c.join(rm.Code(), fmt.Sprintf("P%d", i))
		clients = append(clients, c)
	}
	for _, c := range clients {
		c.send(protocol.MsgSetReady, protocol.SetReadyPayload{Ready: true}, "")
	}
	// Wait until every client sees all n players ready.
	for _, c := range clients {
		c.awaitView(func(v game.PlayerView) bool {
			if len(v.Players) != n {
				return false
			}
			for _, p := range v.Players {
				if !p.Ready {
					return false
				}
			}
			return true
		}, 2*time.Second)
	}
	return clients
}

func (c *client) join(code, name string) protocol.WelcomePayload {
	c.t.Helper()
	c.send(protocol.MsgJoin, protocol.JoinPayload{RoomCode: code, Name: name}, "j")
	return c.welcome()
}

// startGame starts and waits for round 1.
func (h *harness) startGame(rm *room.Room, clients []*client) {
	h.t.Helper()
	clients[0].send(protocol.MsgStartGame, nil, "s")
	for _, c := range clients {
		c.awaitView(func(v game.PlayerView) bool {
			return v.Phase == game.Claiming.String() && v.Round == 1
		}, 2*time.Second)
	}
}

// ---------------------------------------------------------------- basics

func TestJoinStartAndPlayThroughTheStack(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	// Each client sees exactly its own cards.
	for i, c := range clients {
		v, _ := c.lastView()
		own := 0
		for _, p := range v.Players {
			if p.IsYou {
				own = len(p.Cards)
			} else if len(p.Cards) != 0 {
				t.Fatalf("client %d sees %s's cards", i, p.ID)
			}
		}
		if own != 1 {
			t.Fatalf("client %d sees %d of its own cards, want 1", i, own)
		}
		if v.EvalMode != poker.ModeReducedPool.String() {
			t.Fatalf("round 1 with four players should be %s, got %s",
				poker.ModeReducedPool, v.EvalMode)
		}
	}

	// The claim ladder arrives once, in full.
	m := clients[0].await(string(protocol.MsgClaimSpace), time.Second)
	var cs protocol.ClaimSpacePayload
	if err := json.Unmarshal(m.Payload, &cs); err != nil {
		t.Fatal(err)
	}
	if cs.Size != poker.ClaimSpaceSize || len(cs.Claims) != poker.ClaimSpaceSize {
		t.Fatalf("claim space size %d / %d claims, want %d",
			cs.Size, len(cs.Claims), poker.ClaimSpaceSize)
	}

	// Play one full round: claim then SHOW.
	turn := whoseTurn(t, clients)
	turn.send(protocol.MsgMakeClaim,
		protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Two)}, "c1")
	shower := awaitShower(t, clients)
	shower.send(protocol.MsgCallShow, nil, "c2")

	for _, c := range clients {
		v := c.awaitView(func(v game.PlayerView) bool {
			return v.Phase == game.RoundEnd.String()
		}, 2*time.Second)
		if v.LastShow == nil {
			t.Fatal("round end without a result")
		}
		if !v.Revealed {
			t.Fatal("round end without a reveal")
		}
		if v.TurnPlayer != "" || v.YourTurn {
			t.Fatalf("view names a turn holder during %s", v.Phase)
		}
	}
}

func whoseTurn(t *testing.T, clients []*client) *client {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range clients {
			if v, ok := c.lastView(); ok && v.YourTurn && v.CanClaim {
				return c
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no client reports having the turn")
	return nil
}

func awaitShower(t *testing.T, clients []*client) *client {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range clients {
			if v, ok := c.lastView(); ok && v.CanShow {
				return c
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no client is SHOW eligible")
	return nil
}

// ---------------------------------------------------------------- projection

// TestNoHiddenCardsCrossTheWire scans every byte the server ever wrote to
// one client and asserts no card it does not own appears before the reveal.
// This is the property the whole projection boundary exists for.
func TestNoHiddenCardsCrossTheWire(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 5)
	h.startGame(rm, clients)

	victim := clients[0]
	v, _ := victim.lastView()
	var mine []string
	for _, p := range v.Players {
		if p.IsYou {
			for _, c := range p.Cards {
				mine = append(mine, c.Code())
			}
		}
	}
	if len(mine) == 0 {
		t.Fatal("victim has no cards")
	}
	isMine := func(code string) bool {
		for _, m := range mine {
			if m == code {
				return true
			}
		}
		return false
	}

	// Every frame so far predates any reveal.
	for _, f := range victim.conn.frames() {
		body := string(f)
		for _, card := range poker.NewDeck() {
			code := card.Code()
			if isMine(code) {
				continue
			}
			// Cards appear only as quoted codes, so this cannot false-match
			// a rank or suit letter appearing elsewhere.
			if strings.Contains(body, `"`+code+`"`) {
				t.Fatalf("pre-reveal frame leaked %s: %s", code, body)
			}
		}
	}

	// And CARDS_DEALT specifically carries exactly one hand.
	dealt := 0
	for _, m := range victim.msgs() {
		if m.Type != string(protocol.MsgEvent) {
			continue
		}
		var ev protocol.EventPayload
		if err := json.Unmarshal(m.Payload, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != game.EvCardsDealt.String() {
			continue
		}
		dealt++
		if len(ev.Hands) != 1 {
			t.Fatalf("CARDS_DEALT reached the client with %d hands; raw events must never be sent",
				len(ev.Hands))
		}
		if _, ok := ev.Hands[victim.welcome().PlayerID]; !ok {
			t.Fatal("CARDS_DEALT does not contain the recipient's own hand")
		}
	}
	if dealt == 0 {
		t.Fatal("no CARDS_DEALT event was delivered")
	}
}

func TestRevealIsPublicAfterShow(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	whoseTurn(t, clients).send(protocol.MsgMakeClaim,
		protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Two)}, "")
	awaitShower(t, clients).send(protocol.MsgCallShow, nil, "")

	v := clients[0].awaitView(func(v game.PlayerView) bool { return v.Revealed }, 2*time.Second)
	for _, p := range v.Players {
		if len(p.Cards) == 0 {
			t.Fatalf("%s's hand is still hidden after the reveal", p.ID)
		}
	}
}

// ---------------------------------------------------------------- reconnect

func TestResumeWithValidTokenRestoresPrivateView(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	target := clients[2]
	w := target.welcome()
	before, _ := target.lastView()
	var beforeCards []string
	for _, p := range before.Players {
		if p.IsYou {
			for _, c := range p.Cards {
				beforeCards = append(beforeCards, c.Code())
			}
		}
	}
	target.conn.Close()
	<-target.gone

	back := h.connect()
	back.send(protocol.MsgResume, protocol.ResumePayload{
		RoomCode: rm.Code(), PlayerID: w.PlayerID, Token: w.Token, LastSeq: before.Seq,
	}, "r")
	rw := back.welcome()
	if rw.PlayerID != w.PlayerID {
		t.Fatalf("resumed as %s, want %s", rw.PlayerID, w.PlayerID)
	}
	if rw.Token != "" {
		t.Fatal("resume must not reissue the rejoin token")
	}
	after := back.awaitView(func(v game.PlayerView) bool { return v.Round >= 1 }, 2*time.Second)

	var afterCards []string
	for _, p := range after.Players {
		if p.IsYou {
			for _, c := range p.Cards {
				afterCards = append(afterCards, c.Code())
			}
		} else if len(p.Cards) != 0 && !after.Revealed {
			t.Fatalf("resumed view exposes %s's cards", p.ID)
		}
	}
	if strings.Join(beforeCards, ",") != strings.Join(afterCards, ",") {
		t.Fatalf("resumed hand %v does not match %v", afterCards, beforeCards)
	}
}

func TestResumeWithInvalidTokenIsIndistinguishable(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	w := clients[1].welcome()

	cases := []struct {
		name    string
		payload protocol.ResumePayload
	}{
		{"wrong token", protocol.ResumePayload{RoomCode: rm.Code(), PlayerID: w.PlayerID, Token: "nope"}},
		{"empty token", protocol.ResumePayload{RoomCode: rm.Code(), PlayerID: w.PlayerID}},
		{"unknown player", protocol.ResumePayload{RoomCode: rm.Code(), PlayerID: "p_ghost", Token: w.Token}},
		{"unknown room", protocol.ResumePayload{RoomCode: "ZZZZZZ", PlayerID: w.PlayerID, Token: w.Token}},
		{"other room's code", protocol.ResumePayload{RoomCode: "AAAAAA", PlayerID: w.PlayerID, Token: w.Token}},
	}
	for _, tc := range cases {
		c := h.connect()
		c.send(protocol.MsgResume, tc.payload, "r")
		<-c.gone
		codes := c.errorCodes()
		if len(codes) != 1 || codes[0] != protocol.CodeUnauthorized {
			t.Fatalf("%s: error codes = %v, want exactly [%s]",
				tc.name, codes, protocol.CodeUnauthorized)
		}
		if !c.conn.isClosed() {
			t.Fatalf("%s: connection was left open", tc.name)
		}
		for _, m := range c.msgs() {
			if m.Type == string(protocol.MsgWelcome) || m.Type == string(protocol.MsgView) {
				t.Fatalf("%s: server leaked a %s after a failed resume", tc.name, m.Type)
			}
		}
	}
}

func TestResumeCannotObtainAnotherPlayersView(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	mine := clients[0].welcome()
	theirs := clients[3].welcome()

	// My token, their player id.
	c := h.connect()
	c.send(protocol.MsgResume, protocol.ResumePayload{
		RoomCode: rm.Code(), PlayerID: theirs.PlayerID, Token: mine.Token,
	}, "r")
	<-c.gone
	codes := c.errorCodes()
	if len(codes) != 1 || codes[0] != protocol.CodeUnauthorized {
		t.Fatalf("cross-player resume gave %v, want unauthorized", codes)
	}
	if _, ok := c.lastView(); ok {
		t.Fatal("a rejected resume delivered a view")
	}
}

func TestTokenAppearsOnlyInJoinWelcome(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	target := clients[0]
	token := target.welcome().Token
	if token == "" {
		t.Fatal("join welcome carried no token")
	}
	// Play a round so plenty of traffic flows.
	whoseTurn(t, clients).send(protocol.MsgMakeClaim,
		protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Two)}, "")
	awaitShower(t, clients).send(protocol.MsgCallShow, nil, "")
	target.awaitView(func(v game.PlayerView) bool { return v.Phase == game.RoundEnd.String() },
		2*time.Second)

	hits := 0
	for _, f := range target.conn.frames() {
		if strings.Contains(string(f), token) {
			hits++
			var m serverMsg
			_ = json.Unmarshal(f, &m)
			if m.Type != string(protocol.MsgWelcome) {
				t.Fatalf("token appeared in a %s message", m.Type)
			}
		}
	}
	if hits != 1 {
		t.Fatalf("token appeared in %d frames, want exactly 1", hits)
	}
	// And no other client ever sees it.
	for i, c := range clients[1:] {
		for _, f := range c.conn.frames() {
			if strings.Contains(string(f), token) {
				t.Fatalf("client %d received another player's token", i+1)
			}
		}
	}
}

// ---------------------------------------------------------------- hostile

func TestMalformedFramesDoNotKillTheConnection(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	c := h.connect()
	c.join(rm.Code(), "A")

	bad := [][]byte{
		[]byte(`not json`),
		[]byte(`{`),
		[]byte(`[]`),
		[]byte(`{"type":123}`),
		[]byte(`{}`),
		[]byte(`{"type":"make_claim","payload":{"claim":{"category":"NOPE"}}}`),
		[]byte(`{"type":"make_claim","payload":{"claim":{"category":"PAIR","primary":"1"}}}`),
		// An extra field is an impersonation attempt, not something to ignore.
		[]byte(`{"type":"call_show","playerId":"p_someoneelse"}`),
		[]byte(`{"type":"set_ready","payload":{"ready":true,"extra":1}}`),
	}
	for _, frame := range bad {
		c.sendRaw(frame)
	}
	// The connection must survive and still work.
	c.send(protocol.MsgSetReady, protocol.SetReadyPayload{Ready: true}, "ok")
	c.awaitView(func(v game.PlayerView) bool {
		for _, p := range v.Players {
			if p.IsYou && p.Ready {
				return true
			}
		}
		return false
	}, 2*time.Second)
	if c.conn.isClosed() {
		t.Fatal("malformed content should not close the connection")
	}
	if len(c.errorCodes()) < len(bad) {
		t.Fatalf("got %d errors for %d malformed frames", len(c.errorCodes()), len(bad))
	}
}

func TestOversizedFrameClosesConnection(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	c := h.connect()
	c.join(rm.Code(), "A")

	huge := append([]byte(`{"type":"set_ready","reqId":"`),
		[]byte(strings.Repeat("x", protocol.MaxFrameBytes+64))...)
	huge = append(huge, []byte(`"}`)...)
	c.sendRaw(huge)
	<-c.gone

	codes := c.errorCodes()
	if len(codes) == 0 || codes[len(codes)-1] != protocol.CodeTooLarge {
		t.Fatalf("error codes = %v, want a trailing %s", codes, protocol.CodeTooLarge)
	}
	if !c.conn.isClosed() {
		t.Fatal("an oversized frame must close the connection")
	}
}

func TestClientTickAndAdvanceRoundAreRejected(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	c := h.connect()
	c.join(rm.Code(), "A")

	for _, typ := range []protocol.ClientMessageType{"tick", "advance_round", "apply", ""} {
		c.sendRaw([]byte(fmt.Sprintf(`{"type":%q}`, typ)))
	}
	c.send(protocol.MsgSetReady, protocol.SetReadyPayload{Ready: true}, "ok")
	c.awaitView(func(v game.PlayerView) bool {
		for _, p := range v.Players {
			if p.IsYou && p.Ready {
				return true
			}
		}
		return false
	}, 2*time.Second)
	for _, code := range c.errorCodes() {
		if code != protocol.CodeUnknownAction && code != protocol.CodeBadFrame {
			t.Fatalf("unexpected error code %s", code)
		}
	}
	if len(c.errorCodes()) < 4 {
		t.Fatalf("expected an error per rejected type, got %v", c.errorCodes())
	}
}

func TestRateLimitClosesFloodingConnection(t *testing.T) {
	h := newHarness(t)
	opts := h.opts
	opts.RateBurst = 5
	opts.RateWindow = time.Minute
	rm := h.newRoom()
	c := h.connectWith(opts)
	c.join(rm.Code(), "A")

	for i := 0; i < 40; i++ {
		c.send(protocol.MsgSetReady, protocol.SetReadyPayload{Ready: true}, "")
	}
	<-c.gone
	codes := c.errorCodes()
	if len(codes) == 0 || codes[len(codes)-1] != protocol.CodeRateLimited {
		t.Fatalf("error codes = %v, want a trailing %s", codes, protocol.CodeRateLimited)
	}
}

func TestDuplicateActionIsRejectedByTheEngine(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	turn := whoseTurn(t, clients)
	claim := protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Eight)}
	turn.send(protocol.MsgMakeClaim, claim, "first")
	turn.awaitView(func(v game.PlayerView) bool { return len(v.Claims) == 1 }, 2*time.Second)

	// Replaying the same frame must be rejected: the turn has moved on.
	turn.send(protocol.MsgMakeClaim, claim, "replay")
	deadline := time.Now().Add(2 * time.Second)
	saw := false
	for time.Now().Before(deadline) && !saw {
		for _, code := range turn.errorCodes() {
			if code == protocol.CodeNotYourTurn {
				saw = true
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !saw {
		t.Fatalf("replayed claim was not rejected; errors=%v", turn.errorCodes())
	}
	v, _ := turn.lastView()
	if len(v.Claims) != 1 {
		t.Fatalf("replay changed the claim chain: %d claims", len(v.Claims))
	}
}

// ---------------------------------------------------------------- timers

func TestTurnTimerExpiryAndRoundEndAdvance(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	// Nobody acts. The turn budget expires and the engine's AutoShow policy
	// submits the minimum claim for the starter.
	h.clock.Advance(31 * time.Second)
	rm.Tick(h.clock.Now())
	clients[0].awaitView(func(v game.PlayerView) bool { return len(v.Claims) == 1 }, 2*time.Second)

	// The next player also times out, which auto-SHOWs and ends the round.
	h.clock.Advance(31 * time.Second)
	rm.Tick(h.clock.Now())
	clients[0].awaitView(func(v game.PlayerView) bool {
		return v.Phase == game.RoundEnd.String()
	}, 2*time.Second)

	// The reveal window is the room's to close, not a client's.
	h.clock.Advance(7 * time.Second)
	rm.Tick(h.clock.Now())
	for _, c := range clients {
		c.awaitView(func(v game.PlayerView) bool {
			return v.Phase == game.Claiming.String() && v.Round == 2
		}, 2*time.Second)
	}
}

func TestHeartbeatTimeoutClosesConnection(t *testing.T) {
	h := newHarness(t)
	opts := h.opts
	opts.PingInterval = 5 * time.Millisecond
	opts.PongTimeout = time.Millisecond
	rm := h.newRoom()
	c := h.connectWith(opts)
	c.join(rm.Code(), "A")

	h.clock.Advance(time.Minute) // no pong ever arrives
	select {
	case <-c.gone:
	case <-time.After(2 * time.Second):
		t.Fatal("a silent client was never disconnected")
	}
}

// ---------------------------------------------------------------- lifecycle

func TestDisconnectInEveryPhase(t *testing.T) {
	phases := []string{"lobby", "claiming", "round-end", "game-over"}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			rm := h.newRoom()
			clients := h.seatTable(rm, 4)

			switch phase {
			case "claiming":
				h.startGame(rm, clients)
			case "round-end":
				h.startGame(rm, clients)
				whoseTurn(t, clients).send(protocol.MsgMakeClaim,
					protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Two)}, "")
				awaitShower(t, clients).send(protocol.MsgCallShow, nil, "")
				clients[0].awaitView(func(v game.PlayerView) bool {
					return v.Phase == game.RoundEnd.String()
				}, 2*time.Second)
			case "game-over":
				h.startGame(rm, clients)
				drivePhaseToGameOver(t, h, rm, clients)
			}

			// Drop a client and make sure the room stays coherent.
			victim := clients[3]
			victim.conn.Close()
			<-victim.gone

			survivor := clients[0]
			h.clock.Advance(time.Second)
			rm.Tick(h.clock.Now())
			v := survivor.awaitView(func(v game.PlayerView) bool { return true }, 2*time.Second)
			if len(v.Players) != 4 {
				t.Fatalf("seat count changed to %d after a disconnect", len(v.Players))
			}
			if rm.IsClosed() && phase != "game-over" {
				t.Fatalf("room closed on a single disconnect during %s", phase)
			}
		})
	}
}

// drivePhaseToGameOver plays with timer expiry until the game ends.
func drivePhaseToGameOver(t *testing.T, h *harness, rm *room.Room, clients []*client) {
	t.Helper()
	for i := 0; i < 4000; i++ {
		if v, ok := clients[0].lastView(); ok && v.Phase == game.GameOver.String() {
			return
		}
		h.clock.Advance(31 * time.Second)
		rm.Tick(h.clock.Now())
		if rm.IsClosed() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	v, _ := clients[0].lastView()
	t.Fatalf("game did not reach GAME_OVER; phase=%s round=%d", v.Phase, v.Round)
}

func TestGameOverDeliveredToEveryClient(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)
	drivePhaseToGameOver(t, h, rm, clients)

	for i, c := range clients {
		v, ok := c.lastView()
		if !ok || v.Phase != game.GameOver.String() {
			t.Fatalf("client %d never saw GAME_OVER (phase=%s)", i, v.Phase)
		}
		if v.Winner == "" {
			t.Fatalf("client %d saw GAME_OVER with no winner", i)
		}
		if v.YourTurn || v.CanClaim || v.CanShow {
			t.Fatalf("client %d still has an action after GAME_OVER", i)
		}
	}
	// Actions after the end are rejected.
	clients[0].send(protocol.MsgCallShow, nil, "late")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, code := range clients[0].errorCodes() {
			if code == protocol.CodeGameOver || code == protocol.CodeRoomClosed {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("a post-game action was not rejected; errors=%v", clients[0].errorCodes())
}

func TestRoomClosesWhenEmptyAndIsReaped(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	c := h.connect()
	c.join(rm.Code(), "A")
	if h.mgr.Count() != 1 {
		t.Fatalf("manager holds %d rooms", h.mgr.Count())
	}

	c.conn.Close()
	<-c.gone

	h.clock.Advance(11 * time.Second) // past EmptyGrace
	rm.Tick(h.clock.Now())
	select {
	case <-rm.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an empty room was never closed")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.mgr.Count() != 0 {
		time.Sleep(time.Millisecond)
	}
	if h.mgr.Count() != 0 {
		t.Fatalf("closed room was not reaped; manager holds %d", h.mgr.Count())
	}
	if _, err := h.mgr.Get(rm.Code()); err == nil {
		t.Fatal("a closed room is still resolvable by code")
	}
}

func TestAbandonedGameClosesRoomImmediately(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	// Everybody leaves mid-round. Round-end pruning then eliminates every
	// disconnected player at once, so the game ends with nobody in play and
	// no winner, and the room has nothing to linger for.
	for _, c := range clients {
		c.conn.Close()
		<-c.gone
	}
	for i := 0; i < 20 && !rm.IsClosed(); i++ {
		h.clock.Advance(31 * time.Second)
		rm.Tick(h.clock.Now())
		time.Sleep(time.Millisecond)
	}
	select {
	case <-rm.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an abandoned game never closed its room")
	}
}

// ---------------------------------------------------------------- concurrency

func TestMultipleRoomsAndSessionsConcurrently(t *testing.T) {
	h := newHarness(t)
	const rooms = 8

	var wg sync.WaitGroup
	for i := 0; i < rooms; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rm := h.newRoom()
			clients := h.seatTable(rm, 4)
			h.startGame(rm, clients)
			turn := whoseTurn(t, clients)
			turn.send(protocol.MsgMakeClaim,
				protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Two)}, "")
			awaitShower(t, clients).send(protocol.MsgCallShow, nil, "")
			clients[0].awaitView(func(v game.PlayerView) bool {
				return v.Phase == game.RoundEnd.String()
			}, 5*time.Second)
		}()
	}
	wg.Wait()
	if h.mgr.Count() != rooms {
		t.Fatalf("manager holds %d rooms, want %d", h.mgr.Count(), rooms)
	}
}

// TestConcurrentCommandsToSameRoomYieldOneWinner has every seated client
// fire the same claim at once. The actor serialises them, so exactly one can
// be accepted.
func TestConcurrentCommandsToSameRoomYieldOneWinner(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *client) {
			defer wg.Done()
			for i := 0; i < 12; i++ {
				c.send(protocol.MsgMakeClaim,
					protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Eight)}, "")
			}
		}(c)
	}
	wg.Wait()

	// Exactly one Pair of 8s can ever be in the chain: it is a single claim
	// and no later claim may equal it.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := clients[0].lastView(); ok && len(v.Claims) >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	v, _ := clients[0].lastView()
	pairEights := 0
	for _, cv := range v.Claims {
		if cv.Claim == poker.NewPair(poker.Eight) {
			pairEights++
		}
	}
	if pairEights != 1 {
		t.Fatalf("Pair of 8s appears %d times in the chain %v", pairEights, v.Claims)
	}
	if err := checkClaimsAscend(v); err != nil {
		t.Fatal(err)
	}
}

func checkClaimsAscend(v game.PlayerView) error {
	for i := 1; i < len(v.Claims); i++ {
		if v.Claims[i].Ordinal <= v.Claims[i-1].Ordinal {
			return fmt.Errorf("claim chain is not strictly increasing at %d: %v", i, v.Claims)
		}
	}
	return nil
}

// ---------------------------------------------------------------- acceptance

// TestClientWithNoReducerIsPlayable is the acceptance test for the
// architecture. This client ignores every event message and looks only at
// views. If it can play a game to completion, no gameplay logic has leaked
// out of the engine.
func TestClientWithNoReducerIsPlayable(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 5)
	h.startGame(rm, clients)

	// The only client-side knowledge permitted: the static claim ladder.
	ladder := poker.AllClaims()

	rounds := 0
	for step := 0; step < 6000; step++ {
		if done, winner := viewOnlyGameOver(t, clients); done {
			if winner == "" {
				t.Fatal("game over with no winner")
			}
			t.Logf("view-only client finished a %d-round game; winner %s", rounds, winner)
			return
		}

		acted := false
		for _, c := range clients {
			v, ok := c.lastViewNoFatal()
			if !ok {
				continue
			}
			switch {
			case v.Phase == game.Claiming.String() && v.CanShow:
				c.send(protocol.MsgCallShow, nil, "")
				acted = true
			case v.Phase == game.Claiming.String() && v.YourTurn && v.CanClaim:
				// Pick the lowest legal claim purely from the ordinal floor.
				idx := v.StandingOrdinal + 1
				if idx < 0 || idx >= len(ladder) {
					c.send(protocol.MsgCallShow, nil, "")
				} else {
					c.send(protocol.MsgMakeClaim,
						protocol.MakeClaimPayload{Claim: ladder[idx]}, "")
				}
				acted = true
			}
			if acted {
				break
			}
		}
		if !acted {
			// Either the reveal window is open or we are waiting on the
			// server; let the room's timer move things along.
			if v, ok := clients[0].lastViewNoFatal(); ok && v.Phase == game.RoundEnd.String() {
				rounds++
			}
			h.clock.Advance(7 * time.Second)
			rm.Tick(h.clock.Now())
		}
		time.Sleep(200 * time.Microsecond)
		if rm.IsClosed() {
			t.Fatal("room closed before the game finished")
		}
	}
	v, _ := clients[0].lastViewNoFatal()
	t.Fatalf("view-only client did not finish; phase=%s round=%d", v.Phase, v.Round)
}

func viewOnlyGameOver(t *testing.T, clients []*client) (bool, game.PlayerID) {
	for _, c := range clients {
		if v, ok := c.lastViewNoFatal(); ok && v.Phase == game.GameOver.String() {
			return true, v.Winner
		}
	}
	return false, ""
}

// lastViewNoFatal returns the newest view delivered so far, parsing only
// frames it has not already seen.
func (c *client) lastViewNoFatal() (game.PlayerView, bool) {
	for _, f := range c.conn.framesFrom(c.cursor) {
		c.cursor++
		var m serverMsg
		if json.Unmarshal(f, &m) != nil {
			continue
		}
		switch m.Type {
		case string(protocol.MsgView), string(protocol.MsgWelcome), string(protocol.MsgSnapshot):
		default:
			continue
		}
		var wrapper struct {
			View game.PlayerView `json:"view"`
		}
		if json.Unmarshal(m.Payload, &wrapper) != nil {
			continue
		}
		c.view = wrapper.View
		c.haveView = true
	}
	return c.view, c.haveView
}

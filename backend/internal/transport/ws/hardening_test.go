package ws

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/internal/protocol"
	"github.com/kaushlendra/liars-poker/backend/internal/room"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// Phase 5 hardening. These tests were written to fail against the Phase 3
// implementation, on the principle that a hardening pass which only adds
// passing tests has not hardened anything.

// ---------------------------------------------------- concurrent writes

// raceConn reports any overlapping Write. The ordinary fakeConn serialises
// writes behind a mutex, which makes it *look* safe and hides the fact that
// two goroutines are writing at all. A real WebSocket connection is not
// write-safe for concurrent use, so overlapping writes corrupt the frame
// stream even though the race detector sees nothing.
type raceConn struct {
	mu       sync.Mutex
	out      [][]byte
	closed   bool
	closeCh  chan struct{}
	once     sync.Once
	in       chan []byte
	inflight atomic.Int32
	overlaps atomic.Int32
}

func newRaceConn() *raceConn {
	return &raceConn{in: make(chan []byte, 64), closeCh: make(chan struct{})}
}

func (f *raceConn) Read() ([]byte, error) {
	select {
	case b, ok := <-f.in:
		if !ok {
			return nil, errClosed
		}
		return b, nil
	case <-f.closeCh:
		return nil, errClosed
	}
}

func (f *raceConn) Write(b []byte) error {
	if n := f.inflight.Add(1); n > 1 {
		f.overlaps.Add(1)
	}
	// Widen the window so an overlap is observed rather than merely
	// possible.
	time.Sleep(300 * time.Microsecond)
	f.mu.Lock()
	if !f.closed {
		cp := make([]byte, len(b))
		copy(cp, b)
		f.out = append(f.out, cp)
	}
	f.mu.Unlock()
	f.inflight.Add(-1)
	return nil
}

func (f *raceConn) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	f.once.Do(func() { close(f.closeCh) })
	return nil
}

func (f *raceConn) frames() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.out))
	copy(out, f.out)
	return out
}

// TestNoConcurrentWritesToOneConnection drives error replies from the read
// goroutine while the room pushes views from the write pump and the
// heartbeat pushes pings from a third. Every write must be serialised by the
// transport, because the socket is not.
func TestNoConcurrentWritesToOneConnection(t *testing.T) {
	h := newHarness(t)
	opts := h.opts
	opts.PingInterval = time.Millisecond
	opts.PongTimeout = time.Hour
	opts.RateBurst = 100000

	rm := h.newRoom()
	// Three ordinary clients generate room traffic.
	others := h.seatTable(rm, 3)

	victim := &client{t: t, conn: nil, gone: make(chan struct{})}
	rc := newRaceConn()
	go func() {
		defer close(victim.gone)
		Serve(rc, opts)
	}()
	send := func(frame []byte) {
		select {
		case rc.in <- frame:
		case <-rc.closeCh:
		case <-time.After(time.Second):
		}
	}
	joinFrame, _ := json.Marshal(protocol.Inbound{
		Type:    protocol.MsgJoin,
		Payload: mustRaw(t, protocol.JoinPayload{RoomCode: rm.Code(), Name: "victim"}),
	})
	send(joinFrame)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Malformed frames provoke an error reply per frame, written from
		// the read goroutine.
		for i := 0; i < 250; i++ {
			send([]byte(`{"type":"make_claim","payload":{"claim":{"category":"NOPE"}}}`))
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Meanwhile the room keeps producing views for everyone.
		for i := 0; i < 60; i++ {
			for _, c := range others {
				c.send(protocol.MsgSetReady, protocol.SetReadyPayload{Ready: i%2 == 0}, "")
			}
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()
	time.Sleep(50 * time.Millisecond)
	rc.Close()
	<-victim.gone

	if n := rc.overlaps.Load(); n != 0 {
		t.Fatalf("%d overlapping writes to one connection; the socket is not write-safe", n)
	}
	// And every frame must still be a complete, parseable message.
	for _, f := range rc.frames() {
		var m serverMsg
		if err := json.Unmarshal(f, &m); err != nil {
			t.Fatalf("interleaved writes corrupted a frame: %q", f)
		}
	}
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---------------------------------------------------- dead subscriber reaping

// TestDeadSubscribersDoNotKeepRoomsAlive covers the resource leak where a
// subscriber dropped for backpressure stays in the room's subscriber set. A
// room that believes it still has listeners never becomes empty, so it never
// reaps, and the actor goroutine lives forever.
func TestDeadSubscribersDoNotKeepRoomsAlive(t *testing.T) {
	clk := newClock()
	cfg := game.DefaultConfig(5)
	cfg.FirstStarter = game.FirstStarterSeatZero
	opts := room.Options{
		Game:         cfg,
		TickInterval: 0,
		Now:          clk.Now,
		SendQueue:    1, // every subscriber overflows immediately
		EmptyGrace:   5 * time.Second,
		IdleTimeout:  time.Hour,
		ResultLinger: time.Minute,
	}
	closed := make(chan string, 1)
	rm := room.New("ROOMAA", opts, func(code string) { closed <- code })

	for i := 0; i < 3; i++ {
		sub, _, _, err := rm.Join("slow")
		if err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
		// Nobody drains, so the room drops each one for backpressure.
		select {
		case <-sub.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d was never dropped", i)
		}
	}

	// No live connections remain, so the room must reap itself.
	clk.Advance(6 * time.Second)
	rm.Tick(clk.Now())
	select {
	case <-rm.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a room whose subscribers were all dropped never reaped itself")
	}
	select {
	case code := <-closed:
		if code != "ROOMAA" {
			t.Fatalf("close callback reported %q", code)
		}
	case <-time.After(time.Second):
		t.Fatal("the close callback never fired")
	}
}

// ---------------------------------------------------- text safety

// TestMultibyteNameIsNotCorrupted catches byte-slicing a name at a fixed
// length, which splits a multi-byte rune and emits invalid UTF-8 into every
// other player's view.
func TestMultibyteNameIsNotCorrupted(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()

	long := strings.Repeat("\u65e5", 40) // 40 three-byte runes
	c := h.connect()
	c.join(rm.Code(), long)
	observer := h.connect()
	observer.join(rm.Code(), "obs")

	v := observer.awaitView(func(v game.PlayerView) bool { return len(v.Players) == 2 }, 2*time.Second)
	for _, p := range v.Players {
		if !utf8.ValidString(p.Name) {
			t.Fatalf("name %q is not valid UTF-8; a rune was split", p.Name)
		}
		if len(p.Name) > protocol.MaxNameBytes {
			t.Fatalf("name is %d bytes, over the %d limit", len(p.Name), protocol.MaxNameBytes)
		}
	}
	// The frames themselves must be valid UTF-8, since text frames require it.
	for _, f := range observer.conn.frames() {
		if !utf8.Valid(f) {
			t.Fatalf("server emitted a frame that is not valid UTF-8: %q", f)
		}
	}
}

// TestInvalidUTF8FrameIsRejected covers RFC 6455's requirement that a text
// frame carry valid UTF-8. Accepting one lets mojibake into names and views.
func TestInvalidUTF8FrameIsRejected(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	c := h.connect()
	c.join(rm.Code(), "A")

	bad := []byte(`{"type":"set_ready","payload":{"ready":true},"reqId":"` + "\xff\xfe\xfa" + `"}`)
	if utf8.Valid(bad) {
		t.Fatal("test fixture is valid UTF-8")
	}
	c.sendRaw(bad)

	// Follow with something valid so we can tell the connection reacted.
	c.send(protocol.MsgSetReady, protocol.SetReadyPayload{Ready: true}, "ok")
	deadline := time.Now().Add(2 * time.Second)
	sawBadFrame := false
	for time.Now().Before(deadline) && !sawBadFrame {
		for _, code := range c.errorCodes() {
			if code == protocol.CodeBadFrame {
				sawBadFrame = true
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !sawBadFrame {
		t.Fatalf("an invalid UTF-8 frame was accepted; errors=%v", c.errorCodes())
	}
}

// ---------------------------------------------------- slow client isolation

// TestSlowClientBlocksNobody is the backpressure contract: one stalled
// socket must not stop the actor, the timers, or anybody else's updates.
func TestSlowClientBlocksNobody(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	// Wedge one client's socket: writes now fail and nothing is consumed.
	stalled := clients[3]
	stalled.conn.mu.Lock()
	stalled.conn.stall = true
	stalled.conn.mu.Unlock()

	// The rest of the table must keep playing to a resolved round.
	whoseTurn(t, clients[:3]).send(protocol.MsgMakeClaim,
		protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Two)}, "")
	sh := awaitShowerAmong(t, clients[:3])
	if sh == nil {
		// The eligible player may be the stalled one; push the turn along
		// with the timer instead.
		h.clock.Advance(31 * time.Second)
		rm.Tick(h.clock.Now())
	} else {
		sh.send(protocol.MsgCallShow, nil, "")
	}

	for i, c := range clients[:3] {
		v := c.awaitView(func(v game.PlayerView) bool {
			return v.Phase == game.RoundEnd.String() || v.Round > 1
		}, 5*time.Second)
		if v.Round < 1 {
			t.Fatalf("client %d stalled behind a slow peer", i)
		}
	}

	// Timers must still fire.
	h.clock.Advance(10 * time.Second)
	rm.Tick(h.clock.Now())
	clients[0].awaitView(func(v game.PlayerView) bool { return v.Round >= 2 }, 5*time.Second)
	if rm.IsClosed() {
		t.Fatal("one slow client closed the room")
	}
}

func awaitShowerAmong(t *testing.T, clients []*client) *client {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, c := range clients {
			if v, ok := c.lastViewNoFatal(); ok && v.CanShow {
				return c
			}
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

// errClosed is the read error a closed test connection returns.
var errClosed = errors.New("ws: test connection closed")

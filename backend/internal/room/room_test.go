package room

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/internal/protocol"
	"github.com/kaushlendra/liars-poker/backend/internal/session"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
}
func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testOptions(clk *clock) Options {
	cfg := game.DefaultConfig(777)
	cfg.FirstStarter = game.FirstStarterSeatZero
	return Options{
		Game:         cfg,
		TickInterval: 0,
		Now:          clk.Now,
		SendQueue:    256,
		EventRing:    256,
		ResultLinger: time.Minute,
		EmptyGrace:   10 * time.Second,
		IdleTimeout:  time.Hour,
	}
}

// drain consumes a subscriber's queue in the background so the room never
// hits backpressure, recording what arrived.
type sink struct {
	mu   sync.Mutex
	msgs []protocol.Outbound
	stop chan struct{}
}

func drain(sub *Subscriber) *sink {
	s := &sink{stop: make(chan struct{})}
	go func() {
		record := func(m protocol.Outbound) {
			s.mu.Lock()
			s.msgs = append(s.msgs, m)
			s.mu.Unlock()
		}
		for {
			// Prefer queued messages over the release signal, for the same
			// reason the write pump does: a closing room queues its final
			// message before releasing the subscriber.
			select {
			case m := <-sub.Out():
				record(m)
				continue
			default:
			}
			select {
			case <-s.stop:
				return
			case m := <-sub.Out():
				record(m)
			case <-sub.Done():
				for {
					select {
					case m := <-sub.Out():
						record(m)
					default:
						return
					}
				}
			}
		}
	}()
	return s
}

func (s *sink) all() []protocol.Outbound {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]protocol.Outbound, len(s.msgs))
	copy(out, s.msgs)
	return out
}

func (s *sink) countOf(kind protocol.ServerMessageType) int {
	n := 0
	for _, m := range s.all() {
		if m.Type == kind {
			n++
		}
	}
	return n
}

func (s *sink) await(t *testing.T, kind protocol.ServerMessageType, d time.Duration) protocol.Outbound {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, m := range s.all() {
			if m.Type == kind {
				return m
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", kind)
	return protocol.Outbound{}
}

// awaitView polls until a delivered view satisfies pred.
func (s *sink) awaitView(t *testing.T, pred func(game.PlayerView) bool, d time.Duration) game.PlayerView {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if v, ok := s.viewIfAny(); ok && pred(v) {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no delivered view satisfied the condition")
	return game.PlayerView{}
}

func (s *sink) viewIfAny() (game.PlayerView, bool) {
	var v game.PlayerView
	found := false
	for _, m := range s.all() {
		switch p := m.Payload.(type) {
		case protocol.ViewPayload:
			v, found = p.View, true
		case protocol.WelcomePayload:
			v, found = p.View, true
		case protocol.SnapshotPayload:
			v, found = p.View, true
		}
	}
	return v, found
}

func (s *sink) lastView(t *testing.T) game.PlayerView {
	t.Helper()
	v, found := s.viewIfAny()
	if !found {
		t.Fatal("no view was delivered")
	}
	return v
}

// ---------------------------------------------------------------- tests

func TestBackpressureClosesSubscriberRatherThanDroppingMessages(t *testing.T) {
	clk := newClock()
	opts := testOptions(clk)
	opts.SendQueue = 1 // room for one message only
	r := New("ROOMAA", opts, nil)
	defer r.Close("test over")

	sub, _, _, err := r.Join("slow")
	if err != nil {
		t.Fatal(err)
	}
	// Nothing drains the queue. The room sends claim_space, then welcome,
	// then the join batch, so the queue overflows almost immediately.
	select {
	case <-sub.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a stalled subscriber was never closed")
	}
	// The room must still be alive and serving after dropping a connection.
	if _, _, _, err := r.Join("fast"); err != nil {
		t.Fatalf("the room broke after dropping a slow subscriber: %v", err)
	}
	v, err := r.Snapshot("nobody")
	if err != nil {
		t.Fatalf("the room stopped answering: %v", err)
	}
	if len(v.Players) != 2 {
		t.Fatalf("room holds %d players, want 2", len(v.Players))
	}
	if r.IsClosed() {
		t.Fatal("backpressure on one connection closed the whole room")
	}
}

func TestSeqIsMonotonicAcrossEveryDeliveredMessage(t *testing.T) {
	clk := newClock()
	r := New("ROOMAA", testOptions(clk), nil)
	defer r.Close("test over")

	subs := make([]*Subscriber, 0, 4)
	sinks := make([]*sink, 0, 4)
	for i := 0; i < 4; i++ {
		sub, _, _, err := r.Join(fmt.Sprintf("P%d", i))
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, sub)
		s := drain(sub)
		defer close(s.stop)
		sinks = append(sinks, s)
	}
	for _, sub := range subs {
		if err := r.Do(sub.PlayerID(), protocol.Inbound{
			Type: protocol.MsgSetReady, Payload: []byte(`{"ready":true}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Do(subs[0].PlayerID(), protocol.Inbound{Type: protocol.MsgStartGame}); err != nil {
		t.Fatal(err)
	}

	for i, s := range sinks {
		s.awaitView(t, func(v game.PlayerView) bool { return v.Round == 1 }, 2*time.Second)
		last := 0
		for _, m := range s.all() {
			if m.Type != protocol.MsgEvent {
				continue
			}
			if m.Seq <= last {
				t.Fatalf("client %d saw seq %d after %d", i, m.Seq, last)
			}
			last = m.Seq
		}
		if last == 0 {
			t.Fatalf("client %d received no events", i)
		}
	}
}

// TestOnePlayerManyConnections covers a player with two tabs open. Losing one
// connection must not disconnect them.
func TestOnePlayerManyConnections(t *testing.T) {
	clk := newClock()
	r := New("ROOMAA", testOptions(clk), nil)
	defer r.Close("test over")

	first, id, token, err := r.Join("two-tabs")
	if err != nil {
		t.Fatal(err)
	}
	s1 := drain(first)
	defer close(s1.stop)

	second, err := r.Resume(id, token, 0)
	if err != nil {
		t.Fatalf("second connection for the same player: %v", err)
	}
	s2 := drain(second)
	defer close(s2.stop)

	// Both connections see the same public state.
	if err := r.Do(id, protocol.Inbound{
		Type: protocol.MsgSetReady, Payload: []byte(`{"ready":true}`)}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*sink{s1, s2} {
		s.awaitView(t, func(v game.PlayerView) bool {
			for _, p := range v.Players {
				if p.IsYou && p.Ready {
					return true
				}
			}
			return false
		}, 2*time.Second)
	}

	// Dropping one connection leaves the player present.
	r.Detach(first)
	v, err := r.Snapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range v.Players {
		if p.ID == id && p.StatusStr != game.StatusActive.String() {
			t.Fatalf("player went %s while a second connection was open", p.StatusStr)
		}
	}

	// Dropping the last one marks them gone.
	r.Detach(second)
	v, err = r.Snapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range v.Players {
		if p.ID == id && p.StatusStr == game.StatusActive.String() {
			t.Fatal("player is still active with no connections")
		}
	}
}

func TestResumeFallsBackToSnapshotWhenReplayIsUnavailable(t *testing.T) {
	clk := newClock()
	opts := testOptions(clk)
	opts.EventRing = 1 // keep only the newest event
	r := New("ROOMAA", opts, nil)
	defer r.Close("test over")

	sub, id, token, err := r.Join("A")
	if err != nil {
		t.Fatal(err)
	}
	s := drain(sub)
	defer close(s.stop)
	v := s.awaitView(t, func(v game.PlayerView) bool { return v.Seq > 0 }, 2*time.Second)

	// Ask to resume from a point the ring can no longer cover.
	back, err := r.Resume(id, token, v.Seq+1000)
	if err != nil {
		t.Fatal(err)
	}
	s2 := drain(back)
	defer close(s2.stop)
	s2.await(t, protocol.MsgSnapshot, 2*time.Second)
	if s2.countOf(protocol.MsgView) == 0 {
		t.Fatal("a resume must always end with a current view")
	}
}

func TestClosedRoomRejectsEverything(t *testing.T) {
	clk := newClock()
	r := New("ROOMAA", testOptions(clk), nil)
	sub, id, token, err := r.Join("A")
	if err != nil {
		t.Fatal(err)
	}
	s := drain(sub)
	defer close(s.stop)

	r.Close("shutting down")
	<-r.Done()

	// Connected clients are told why.
	s.await(t, protocol.MsgClosed, 2*time.Second)
	select {
	case <-sub.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("subscribers were not released when the room closed")
	}

	if err := r.Do(id, protocol.Inbound{Type: protocol.MsgStartGame}); err != protocol.ErrRoomClosed {
		t.Fatalf("Do on a closed room = %v, want ErrRoomClosed", err)
	}
	if _, _, _, err := r.Join("late"); err != protocol.ErrRoomClosed {
		t.Fatalf("Join on a closed room = %v", err)
	}
	if _, err := r.Resume(id, token, 0); err != protocol.ErrRoomClosed {
		t.Fatalf("Resume on a closed room = %v", err)
	}
	// Detach and Tick must not block or panic.
	r.Detach(sub)
	r.Tick(clk.Now())
}

func TestResumeRequiresASeatedPlayer(t *testing.T) {
	clk := newClock()
	r := New("ROOMAA", testOptions(clk), nil)
	defer r.Close("test over")

	// A credential that was never issued here.
	other := session.NewRegistry("ROOMAA", nil)
	tok, err := other.Issue("p_ghost")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resume("p_ghost", tok, 0); err != protocol.ErrUnauthorized {
		t.Fatalf("resume with a foreign credential = %v, want unauthorized", err)
	}
}

// TestConcurrentCommandsAreSerialised fires commands from many goroutines at
// one room. The actor is the only writer, so the outcome must be coherent:
// exactly one claim chain, strictly ascending.
func TestConcurrentCommandsAreSerialised(t *testing.T) {
	clk := newClock()
	r := New("ROOMAA", testOptions(clk), nil)
	defer r.Close("test over")

	ids := make([]game.PlayerID, 0, 5)
	for i := 0; i < 5; i++ {
		sub, id, _, err := r.Join(fmt.Sprintf("P%d", i))
		if err != nil {
			t.Fatal(err)
		}
		s := drain(sub)
		defer close(s.stop)
		ids = append(ids, id)
		if err := r.Do(id, protocol.Inbound{
			Type: protocol.MsgSetReady, Payload: []byte(`{"ready":true}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Do(ids[0], protocol.Inbound{Type: protocol.MsgStartGame}); err != nil {
		t.Fatal(err)
	}

	claims := poker.AllClaims()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id game.PlayerID) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				c := claims[i%60]
				payload := fmt.Sprintf(`{"claim":%s}`, mustJSON(t, c))
				_ = r.Do(id, protocol.Inbound{
					Type: protocol.MsgMakeClaim, Payload: []byte(payload)})
				_ = r.Do(id, protocol.Inbound{Type: protocol.MsgCallShow})
			}
		}(id)
	}
	wg.Wait()

	v, err := r.Snapshot(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(v.Claims); i++ {
		if v.Claims[i].Ordinal <= v.Claims[i-1].Ordinal {
			t.Fatalf("claim chain not strictly ascending at %d: %v", i, v.Claims)
		}
	}
	if v.Phase != game.Claiming.String() && v.Phase != game.RoundEnd.String() &&
		v.Phase != game.GameOver.String() {
		t.Fatalf("room ended in phase %s", v.Phase)
	}
}

func mustJSON(t *testing.T, c poker.Claim) string {
	t.Helper()
	b, err := c.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestManagerIsolatesRooms checks that rooms created concurrently get unique
// codes and do not see each other.
func TestManagerIsolatesRooms(t *testing.T) {
	clk := newClock()
	mgr := NewManager(testOptions(clk))
	defer mgr.Shutdown("test over")

	const n = 24
	codes := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rm, err := mgr.Create()
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			if _, _, _, err := rm.Join("solo"); err != nil {
				t.Errorf("join: %v", err)
				return
			}
			codes <- rm.Code()
		}()
	}
	wg.Wait()
	close(codes)

	seen := map[string]struct{}{}
	for code := range codes {
		if _, dup := seen[code]; dup {
			t.Fatalf("duplicate room code %q", code)
		}
		seen[code] = struct{}{}
		rm, err := mgr.Get(code)
		if err != nil {
			t.Fatalf("room %q not resolvable: %v", code, err)
		}
		v, err := rm.Snapshot("nobody")
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Players) != 1 {
			t.Fatalf("room %q holds %d players; rooms are leaking into each other",
				code, len(v.Players))
		}
	}
	if mgr.Count() != n {
		t.Fatalf("manager holds %d rooms, want %d", mgr.Count(), n)
	}
	if _, err := mgr.Get("NOPE12"); err != protocol.ErrRoomNotFound {
		t.Fatalf("unknown room = %v", err)
	}
}

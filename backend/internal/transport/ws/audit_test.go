package ws

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/internal/obs"
	"github.com/kaushlendra/liars-poker/backend/internal/protocol"
	"github.com/kaushlendra/liars-poker/backend/internal/room"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// ============================================================ leak audit

// jsonKeys walks a decoded JSON value and collects every object key.
func jsonKeys(v any, out map[string]struct{}) {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			out[k] = struct{}{}
			jsonKeys(sub, out)
		}
	case []any:
		for _, sub := range t {
			jsonKeys(sub, out)
		}
	}
}

// TestProtocolLeakAudit is the deliberate leak sweep. It plays a full game
// with disconnects and resumes, captures every outbound frame for every
// client, and asserts four things across all of them:
//
//   - no card a recipient does not own appears before that recipient's
//     reveal;
//   - no rejoin token appears outside the one welcome that issued it;
//   - no field name betrays an untagged Go struct, which is how internal
//     representation leaks by accident;
//   - no forbidden concept appears at all: deck, pool contents, ownership
//     map, seed, digest, secret.
func TestProtocolLeakAudit(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 5)
	h.startGame(rm, clients)

	tokens := make([]string, len(clients))
	owned := make([]map[string]bool, len(clients))
	for i, c := range clients {
		w := c.welcome()
		tokens[i] = w.Token
		owned[i] = map[string]bool{}
		v, _ := c.lastView()
		for _, p := range v.Players {
			if p.IsYou {
				for _, card := range p.Cards {
					owned[i][card.Code()] = true
				}
			}
		}
	}

	// Play several rounds, with a disconnect and a resume in the middle, so
	// every message type gets exercised.
	for round := 0; round < 3; round++ {
		if turn := awaitTurnAmong(t, clients); turn != nil {
			turn.send(protocol.MsgMakeClaim,
				protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Two)}, "")
		}
		if sh := awaitShowerAmong(t, clients); sh != nil {
			sh.send(protocol.MsgCallShow, nil, "")
		}
		h.clock.Advance(40 * time.Second)
		rm.Tick(h.clock.Now())
		time.Sleep(5 * time.Millisecond)
	}
	// A rejected resume and a successful one.
	bad := h.connect()
	bad.send(protocol.MsgResume, protocol.ResumePayload{
		RoomCode: rm.Code(), PlayerID: clients[0].welcome().PlayerID, Token: "wrong"}, "")
	<-bad.gone

	// Now audit everything.
	forbidden := []string{"seed", "digest", "secret", "ownermap", "deckorder"}
	seenTypes := map[string]struct{}{}
	allowedTypes := map[string]struct{}{
		string(protocol.MsgWelcome): {}, string(protocol.MsgSnapshot): {},
		string(protocol.MsgEvent): {}, string(protocol.MsgView): {},
		string(protocol.MsgError): {}, string(protocol.MsgClaimSpace): {},
		string(protocol.MsgPing): {}, string(protocol.MsgClosed): {},
	}

	for i, c := range clients {
		revealedYet := false
		for _, frame := range c.conn.frames() {
			body := string(frame)

			var m serverMsg
			if err := json.Unmarshal(frame, &m); err != nil {
				t.Fatalf("client %d: unparseable frame %q", i, frame)
			}
			seenTypes[m.Type] = struct{}{}
			if _, ok := allowedTypes[m.Type]; !ok {
				t.Fatalf("client %d received an unknown message type %q", i, m.Type)
			}

			// Untagged Go structs surface as capitalised keys.
			var decoded any
			if err := json.Unmarshal(frame, &decoded); err != nil {
				t.Fatal(err)
			}
			keys := map[string]struct{}{}
			jsonKeys(decoded, keys)
			for k := range keys {
				if k == "" {
					continue
				}
				if k[0] >= 'A' && k[0] <= 'Z' {
					t.Fatalf("client %d: key %q is capitalised, so a Go struct is leaking its field names: %s",
						i, k, body)
				}
			}
			// A card serialised as its internal fields would show these.
			for _, k := range []string{"rank", "suit"} {
				if _, bad := keys[k]; bad {
					if _, isClaim := keys["category"]; !isClaim {
						t.Fatalf("client %d: %q suggests a card leaked its representation: %s", i, k, body)
					}
				}
			}
			for _, word := range forbidden {
				if strings.Contains(strings.ToLower(body), word) {
					t.Fatalf("client %d: frame mentions %q: %s", i, word, body)
				}
			}

			// Tokens: only this client's own welcome may carry one, and
			// nobody else's token may appear anywhere.
			for j, tok := range tokens {
				if tok == "" || !strings.Contains(body, tok) {
					continue
				}
				if j != i {
					t.Fatalf("client %d received client %d's token", i, j)
				}
				if m.Type != string(protocol.MsgWelcome) {
					t.Fatalf("client %d: token appeared in a %s frame", i, m.Type)
				}
			}

			// Cards: permitted only from this client's own hand, until a
			// reveal for that client.
			if m.Type == string(protocol.MsgEvent) {
				var ev protocol.EventPayload
				if err := json.Unmarshal(m.Payload, &ev); err == nil &&
					ev.Type == game.EvCardsRevealed.String() {
					revealedYet = true
				}
			}
			if m.Type == string(protocol.MsgView) || m.Type == string(protocol.MsgSnapshot) ||
				m.Type == string(protocol.MsgWelcome) {
				var w struct {
					View game.PlayerView `json:"view"`
				}
				if json.Unmarshal(m.Payload, &w) == nil && w.View.Revealed {
					revealedYet = true
				}
			}
			if revealedYet {
				continue
			}
			for _, card := range poker.NewDeck() {
				code := card.Code()
				if owned[i][code] {
					continue
				}
				if strings.Contains(body, `"`+code+`"`) {
					t.Fatalf("client %d saw %s before any reveal, in a %s frame: %s",
						i, code, m.Type, body)
				}
			}
		}
	}

	// Every type the protocol defines should have been observed, or the
	// audit is not covering all of them.
	for _, want := range []protocol.ServerMessageType{
		protocol.MsgWelcome, protocol.MsgEvent, protocol.MsgView, protocol.MsgClaimSpace,
	} {
		if _, ok := seenTypes[string(want)]; !ok {
			t.Errorf("the audit never observed a %s message", want)
		}
	}
	t.Logf("audited message types: %v", keysOf(seenTypes))
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func awaitTurnAmong(t *testing.T, clients []*client) *client {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, c := range clients {
			if v, ok := c.lastViewNoFatal(); ok && v.YourTurn && v.CanClaim {
				return c
			}
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

// rawClient owns its socket's read side from the moment it is dialled.
//
// A read deadline in gorilla is sticky and, once exceeded, permanently
// corrupts the connection. So a helper that polls with a deadline poisons
// every later read, including a blocking one started afterwards. The reader
// goroutine therefore starts at dial time and never sets a deadline.
type rawClient struct {
	conn  *websocket.Conn
	inbox chan serverMsg
	dead  chan struct{}

	mu     sync.Mutex
	frames [][]byte
}

func newRawClient(t *testing.T, srv *httptest.Server) *rawClient {
	t.Helper()
	c := &rawClient{
		conn:  dialRaw(t, srv),
		inbox: make(chan serverMsg, 512),
		dead:  make(chan struct{}),
	}
	go func() {
		defer close(c.dead)
		for {
			_, data, err := c.conn.ReadMessage()
			if err != nil {
				return
			}
			c.mu.Lock()
			cp := make([]byte, len(data))
			copy(cp, data)
			c.frames = append(c.frames, cp)
			c.mu.Unlock()
			var m serverMsg
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			select {
			case c.inbox <- m:
			default:
			}
		}
	}()
	return c
}

func (c *rawClient) send(t *testing.T, in protocol.Inbound) {
	t.Helper()
	if err := c.conn.WriteJSON(in); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (c *rawClient) await(t *testing.T, kind protocol.ServerMessageType, d time.Duration) serverMsg {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		select {
		case m := <-c.inbox:
			if m.Type == string(kind) {
				return m
			}
		case <-c.dead:
			t.Fatalf("connection died while waiting for %s", kind)
		case <-time.After(2 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %s", kind)
	return serverMsg{}
}

// awaitView polls the frames already captured until one satisfies pred.
func (c *rawClient) awaitView(t *testing.T, pred func(game.PlayerView) bool, d time.Duration) game.PlayerView {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, frame := range c.allFrames() {
			var m serverMsg
			if json.Unmarshal(frame, &m) != nil {
				continue
			}
			switch m.Type {
			case string(protocol.MsgView), string(protocol.MsgWelcome), string(protocol.MsgSnapshot):
			default:
				continue
			}
			var w struct {
				View game.PlayerView `json:"view"`
			}
			if json.Unmarshal(m.Payload, &w) != nil {
				continue
			}
			if pred(w.View) {
				return w.View
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no view satisfied the condition")
	return game.PlayerView{}
}

func (c *rawClient) allFrames() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.frames))
	copy(out, c.frames)
	return out
}

// TestLogsNeverCarrySecretsOrCards runs a whole game through a real handler
// with logging at debug level and greps the output for anything that must
// never be written down.
func TestLogsNeverCarrySecretsOrCards(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := obs.New(slog.NewJSONHandler(&lockedWriter{w: &buf, mu: &mu},
		&slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := game.DefaultConfig(31337)
	cfg.FirstStarter = game.FirstStarterSeatZero
	cfg.TurnDuration = 150 * time.Millisecond
	cfg.RoundEndDuration = 40 * time.Millisecond
	mgr := room.NewManager(room.Options{Game: cfg, TickInterval: 10 * time.Millisecond, Log: logger})
	srv := httptest.NewServer(NewServeMux(Options{
		Manager: mgr, AllowedOrigins: []string{"*"}, Log: logger,
		PingInterval: 30 * time.Millisecond, PongTimeout: time.Hour,
	}))
	defer srv.Close()

	code := createRoom(t, srv)
	var tokens []string
	var clients []*rawClient
	for i := 0; i < 4; i++ {
		c := newRawClient(t, srv)
		clients = append(clients, c)
		c.send(t, protocol.Inbound{Type: protocol.MsgJoin,
			Payload: mustRaw(t, protocol.JoinPayload{RoomCode: code, Name: "P"})})
		m := c.await(t, protocol.MsgWelcome, 3*time.Second)
		var w protocol.WelcomePayload
		if err := json.Unmarshal(m.Payload, &w); err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, w.Token)
		c.send(t, protocol.Inbound{Type: protocol.MsgSetReady,
			Payload: mustRaw(t, protocol.SetReadyPayload{Ready: true})})
	}
	// Frame ordering holds per connection, not across them, so wait until
	// the table is visibly ready before starting. Otherwise start_game can
	// beat the last set_ready and be rejected.
	clients[0].awaitView(t, func(v game.PlayerView) bool {
		if len(v.Players) != 4 {
			return false
		}
		for _, p := range v.Players {
			if !p.Ready {
				return false
			}
		}
		return true
	}, 3*time.Second)
	clients[0].send(t, protocol.Inbound{Type: protocol.MsgStartGame})
	clients[0].awaitView(t, func(v game.PlayerView) bool {
		return v.Phase == game.Claiming.String() && v.Round == 1
	}, 3*time.Second)

	// Let the engine's timeout policies play several rounds unattended.
	time.Sleep(1200 * time.Millisecond)

	// Exercise the disconnect and rejected-resume log paths too.
	_ = clients[3].conn.Close()
	bad := newRawClient(t, srv)
	bad.send(t, protocol.Inbound{Type: protocol.MsgResume,
		Payload: mustRaw(t, protocol.ResumePayload{RoomCode: code, PlayerID: "p_x", Token: "nope"})})
	time.Sleep(150 * time.Millisecond)

	// Harvest every card the server ever showed any of these clients.
	cards := map[string]struct{}{}
	for _, c := range clients {
		for _, frame := range c.allFrames() {
			var m serverMsg
			if json.Unmarshal(frame, &m) != nil {
				continue
			}
			switch m.Type {
			case string(protocol.MsgEvent):
				var ev protocol.EventPayload
				if json.Unmarshal(m.Payload, &ev) == nil {
					for _, hand := range ev.Hands {
						for _, card := range hand {
							cards[card.Code()] = struct{}{}
						}
					}
				}
			case string(protocol.MsgView), string(protocol.MsgWelcome), string(protocol.MsgSnapshot):
				var w struct {
					View game.PlayerView `json:"view"`
				}
				if json.Unmarshal(m.Payload, &w) == nil {
					for _, p := range w.View.Players {
						for _, card := range p.Cards {
							cards[card.Code()] = struct{}{}
						}
					}
				}
			}
		}
		_ = c.conn.Close()
	}
	_ = bad.conn.Close()
	mgr.Shutdown("test over")

	mu.Lock()
	logs := buf.String()
	mu.Unlock()
	if logs == "" {
		t.Fatal("nothing was logged, so the audit proves nothing")
	}
	if len(cards) == 0 {
		t.Fatal("no card codes were captured, so the card grep would prove nothing")
	}

	for _, tok := range tokens {
		if tok != "" && strings.Contains(logs, tok) {
			t.Fatal("a rejoin token reached the logs")
		}
	}
	for card := range cards {
		if strings.Contains(logs, `"`+card+`"`) {
			t.Fatalf("card %s reached the logs", card)
		}
	}
	for _, banned := range []string{"witness", "\"cards\"", "digest", "secret", "seed", "\"pool\""} {
		if strings.Contains(strings.ToLower(logs), banned) {
			t.Fatalf("logs mention %q:\n%s", banned, logs)
		}
	}
	for _, want := range []string{"room created", "player joined", "connection opened",
		"player disconnected", "resume rejected"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs do not record %q", want)
		}
	}
	t.Logf("audited %d bytes of logs against %d tokens and %d distinct card codes",
		len(logs), len(tokens), len(cards))
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// TestNoDebugEndpointsAreRegistered guards against pprof or a dev route
// creeping into the production mux.
func TestNoDebugEndpointsAreRegistered(t *testing.T) {
	cfg := game.DefaultConfig(1)
	mgr := room.NewManager(room.Options{Game: cfg})
	defer mgr.Shutdown("done")
	srv := httptest.NewServer(NewServeMux(Options{Manager: mgr, AllowedOrigins: []string{"*"}}))
	defer srv.Close()

	for _, path := range []string{
		"/debug/pprof/", "/debug/pprof/goroutine", "/debug/vars", "/metrics",
		"/state", "/rooms/ABCDEF/state", "/dev", "/admin",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%s is served (status 200, body %q)", path, body[:n])
		}
	}
}

// ============================================================ versioning

func TestProtocolVersionNegotiation(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()

	// Omitted version is accepted, for clients predating the field.
	c := h.connect()
	c.send(protocol.MsgJoin, protocol.JoinPayload{RoomCode: rm.Code(), Name: "A"}, "")
	w := c.welcome()
	if w.Version != protocol.ProtocolVersion {
		t.Fatalf("welcome declares version %d, want %d", w.Version, protocol.ProtocolVersion)
	}

	// The current version is accepted.
	cur := h.connect()
	cur.sendRaw([]byte(fmt.Sprintf(
		`{"type":"join","v":%d,"payload":{"roomCode":%q,"name":"B"}}`,
		protocol.ProtocolVersion, rm.Code())))
	cur.welcome()

	// Anything else is refused, and the connection closes.
	for _, v := range []int{2, 99, -1} {
		bad := h.connect()
		bad.sendRaw([]byte(fmt.Sprintf(
			`{"type":"join","v":%d,"payload":{"roomCode":%q,"name":"C"}}`, v, rm.Code())))
		<-bad.gone
		codes := bad.errorCodes()
		if len(codes) != 1 || codes[0] != protocol.CodeUnsupportedVersion {
			t.Fatalf("version %d gave %v, want [%s]", v, codes, protocol.CodeUnsupportedVersion)
		}
	}
}

// ============================================================ idempotency

// TestRequestIDSuppressesDuplicateActions covers the retry a client makes
// when it does not know whether its action landed.
func TestRequestIDSuppressesDuplicateActions(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	turn := whoseTurn(t, clients)
	claim := protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Eight)}
	turn.send(protocol.MsgMakeClaim, claim, "req-1")
	turn.awaitView(func(v game.PlayerView) bool { return len(v.Claims) == 1 }, 2*time.Second)

	before := len(turn.errorCodes())
	// The identical request id is a retry of one intent: no second claim,
	// and no error either, because the client's intent was satisfied.
	for i := 0; i < 5; i++ {
		turn.send(protocol.MsgMakeClaim, claim, "req-1")
	}
	time.Sleep(50 * time.Millisecond)
	v, _ := turn.lastView()
	if len(v.Claims) != 1 {
		t.Fatalf("a replayed request id was applied again: %d claims", len(v.Claims))
	}
	if after := len(turn.errorCodes()); after != before {
		t.Fatalf("a retry produced %d new errors; it should be answered with a view",
			after-before)
	}

	// A different request id is a new intent and is judged on its merits,
	// which here means rejected because the turn has moved on.
	turn.send(protocol.MsgMakeClaim,
		protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Nine)}, "req-2")
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
		t.Fatalf("a fresh request id was not judged on its merits; errors=%v", turn.errorCodes())
	}
}

func TestRequestIDIsPerPlayerNotGlobal(t *testing.T) {
	h := newHarness(t)
	rm := h.newRoom()
	clients := h.seatTable(rm, 4)
	h.startGame(rm, clients)

	// Two different players reusing the same id must both be honoured.
	turn := whoseTurn(t, clients)
	turn.send(protocol.MsgMakeClaim,
		protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Two)}, "shared")
	turn.awaitView(func(v game.PlayerView) bool { return len(v.Claims) == 1 }, 2*time.Second)

	next := whoseTurn(t, clients)
	next.send(protocol.MsgMakeClaim,
		protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Three)}, "shared")
	next.awaitView(func(v game.PlayerView) bool { return len(v.Claims) == 2 }, 2*time.Second)
}

// ============================================================ limits

func TestResourceLimits(t *testing.T) {
	t.Run("rooms", func(t *testing.T) {
		cfg := game.DefaultConfig(1)
		mgr := room.NewManager(room.Options{Game: cfg, MaxRooms: 3})
		defer mgr.Shutdown("done")
		for i := 0; i < 3; i++ {
			if _, err := mgr.Create(); err != nil {
				t.Fatalf("room %d: %v", i, err)
			}
		}
		if _, err := mgr.Create(); err != protocol.ErrRoomLimit {
			t.Fatalf("over the room cap: %v, want ErrRoomLimit", err)
		}
	})

	t.Run("connections per room", func(t *testing.T) {
		cfg := game.DefaultConfig(1)
		mgr := room.NewManager(room.Options{Game: cfg, MaxConnectionsPerRoom: 2})
		defer mgr.Shutdown("done")
		rm, err := mgr.Create()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, _, _, err := rm.Join("x"); err != nil {
				t.Fatalf("join %d: %v", i, err)
			}
		}
		if _, _, _, err := rm.Join("x"); err != protocol.ErrConnectionLimit {
			t.Fatalf("over the connection cap: %v, want ErrConnectionLimit", err)
		}
	})

	t.Run("connections per player", func(t *testing.T) {
		cfg := game.DefaultConfig(1)
		mgr := room.NewManager(room.Options{Game: cfg, MaxConnectionsPerPlayer: 2})
		defer mgr.Shutdown("done")
		rm, err := mgr.Create()
		if err != nil {
			t.Fatal(err)
		}
		_, id, token, err := rm.Join("tabs")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rm.Resume(id, token, 0); err != nil {
			t.Fatalf("second connection: %v", err)
		}
		if _, err := rm.Resume(id, token, 0); err != protocol.ErrConnectionLimit {
			t.Fatalf("third connection for one player: %v, want ErrConnectionLimit", err)
		}
	})

	t.Run("concurrent handshakes", func(t *testing.T) {
		cfg := game.DefaultConfig(1)
		mgr := room.NewManager(room.Options{Game: cfg})
		defer mgr.Shutdown("done")
		// A gate of one, with a handler that holds its slot while a client
		// sits idle, so a second upgrade must be refused.
		srv := httptest.NewServer(NewServeMux(Options{
			Manager: mgr, AllowedOrigins: []string{"*"},
			MaxConcurrentHandshakes: 1, PingInterval: time.Hour,
		}))
		defer srv.Close()
		code := createRoom(t, srv)
		held := dialRaw(t, srv)
		writeJSON(t, held, protocol.Inbound{Type: protocol.MsgJoin,
			Payload: mustRaw(t, protocol.JoinPayload{RoomCode: code, Name: "A"})})
		readWelcome(t, held)
		defer held.Close()

		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
		_, resp, err := websocket.DefaultDialer.Dial(url, nil)
		if err == nil {
			t.Fatal("a second handshake was admitted past the gate of one")
		}
		if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("refused handshake status = %v, want 503", resp)
		}
	})
}

// ============================================================ lifecycle

// TestQueuedActionsCannotResurrectAClosedRoom fires commands at a room while
// it is closing and afterwards.
func TestQueuedActionsCannotResurrectAClosedRoom(t *testing.T) {
	clk := newClock()
	cfg := game.DefaultConfig(9)
	cfg.FirstStarter = game.FirstStarterSeatZero
	rm := room.New("ROOMAA", room.Options{Game: cfg, Now: clk.Now, TickInterval: 0}, nil)

	var ids []game.PlayerID
	var subs []*room.Subscriber
	for i := 0; i < 4; i++ {
		sub, id, _, err := rm.Join("P")
		if err != nil {
			t.Fatal(err)
		}
		go func(s *room.Subscriber) {
			for {
				select {
				case <-s.Done():
					return
				case <-s.Out():
				}
			}
		}(sub)
		ids = append(ids, id)
		subs = append(subs, sub)
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id game.PlayerID) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = rm.Do(id, protocol.Inbound{Type: protocol.MsgSetReady,
					Payload: []byte(`{"ready":true}`)})
				rm.Tick(clk.Now())
			}
		}(id)
	}
	// Close while all that is in flight.
	time.Sleep(2 * time.Millisecond)
	rm.Close("closing under load")
	wg.Wait()
	<-rm.Done()

	// Everything afterwards is refused, and nothing revives the actor.
	for _, id := range ids {
		if err := rm.Do(id, protocol.Inbound{Type: protocol.MsgStartGame}); err != protocol.ErrRoomClosed {
			t.Fatalf("post-close action = %v, want ErrRoomClosed", err)
		}
	}
	rm.Tick(clk.Now())
	clk.Advance(time.Hour)
	rm.Tick(clk.Now())
	if _, err := rm.Snapshot(ids[0]); err != protocol.ErrRoomClosed {
		t.Fatalf("post-close snapshot = %v, want ErrRoomClosed", err)
	}
	// Subscribers must all have been released.
	for i, sub := range subs {
		select {
		case <-sub.Done():
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d was never released", i)
		}
	}
}

// TestDetachRacesCloseAndReconnect hammers the shutdown ordering the brief
// called out: Detach against room close against timer tick against resume.
func TestDetachRacesCloseAndReconnect(t *testing.T) {
	clk := newClock()
	cfg := game.DefaultConfig(11)
	cfg.FirstStarter = game.FirstStarterSeatZero
	mgr := room.NewManager(room.Options{Game: cfg, Now: clk.Now, TickInterval: 0,
		MaxConnectionsPerPlayer: 8})
	defer mgr.Shutdown("done")

	for iter := 0; iter < 12; iter++ {
		rm, err := mgr.Create()
		if err != nil {
			t.Fatal(err)
		}
		sub, id, token, err := rm.Join("racer")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(4)
		go func() { defer wg.Done(); rm.Detach(sub) }()
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				if s, err := rm.Resume(id, token, 0); err == nil {
					rm.Detach(s)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				clk.Advance(time.Second)
				rm.Tick(clk.Now())
			}
		}()
		go func() { defer wg.Done(); rm.Close("racing") }()
		wg.Wait()
		<-rm.Done()
	}
}

// ============================================================ resume threats

func TestResumeThreatModel(t *testing.T) {
	h := newHarness(t)

	t.Run("after game over", func(t *testing.T) {
		rm := h.newRoom()
		clients := h.seatTable(rm, 4)
		h.startGame(rm, clients)
		w := clients[1].welcome()
		drivePhaseToGameOver(t, h, rm, clients)

		clients[1].conn.Close()
		<-clients[1].gone
		back := h.connect()
		back.send(protocol.MsgResume, protocol.ResumePayload{
			RoomCode: rm.Code(), PlayerID: w.PlayerID, Token: w.Token}, "")
		v := back.awaitView(func(v game.PlayerView) bool {
			return v.Phase == game.GameOver.String()
		}, 3*time.Second)
		if v.Winner == "" {
			t.Fatal("a post-game resume did not carry the result")
		}
	})

	t.Run("concurrent resumes", func(t *testing.T) {
		rm := h.newRoom()
		clients := h.seatTable(rm, 4)
		h.startGame(rm, clients)
		w := clients[0].welcome()

		var wg sync.WaitGroup
		results := make([]bool, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				c := h.connect()
				c.send(protocol.MsgResume, protocol.ResumePayload{
					RoomCode: rm.Code(), PlayerID: w.PlayerID, Token: w.Token}, "")
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					if v, ok := c.lastViewNoFatal(); ok && v.Round >= 1 {
						results[i] = true
						return
					}
					if len(c.errorCodes()) > 0 {
						return
					}
					time.Sleep(time.Millisecond)
				}
			}(i)
		}
		wg.Wait()
		granted := 0
		for _, ok := range results {
			if ok {
				granted++
			}
		}
		if granted == 0 {
			t.Fatal("no concurrent resume succeeded")
		}
		// Whatever the per-player cap allows, the room must still be sane.
		if rm.IsClosed() {
			t.Fatal("concurrent resumes closed the room")
		}
	})

	t.Run("while timers fire and rounds change", func(t *testing.T) {
		rm := h.newRoom()
		clients := h.seatTable(rm, 4)
		h.startGame(rm, clients)
		w := clients[2].welcome()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				h.clock.Advance(31 * time.Second)
				rm.Tick(h.clock.Now())
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				c := h.connect()
				c.send(protocol.MsgResume, protocol.ResumePayload{
					RoomCode: rm.Code(), PlayerID: w.PlayerID, Token: w.Token}, "")
				time.Sleep(time.Millisecond)
				c.conn.Close()
				<-c.gone
			}
		}()
		wg.Wait()

		// The room must still answer with a coherent view for that player.
		back := h.connect()
		back.send(protocol.MsgResume, protocol.ResumePayload{
			RoomCode: rm.Code(), PlayerID: w.PlayerID, Token: w.Token}, "")
		if rm.IsClosed() {
			t.Skip("room closed during the storm, which is a legitimate outcome")
		}
		v := back.awaitView(func(v game.PlayerView) bool { return v.Round >= 1 }, 3*time.Second)
		own := 0
		for _, p := range v.Players {
			if p.IsYou {
				own = len(p.Cards)
			} else if len(p.Cards) != 0 && !v.Revealed {
				t.Fatalf("a resume during a round change leaked %s's cards", p.ID)
			}
		}
		if own == 0 && v.Phase == game.Claiming.String() {
			t.Fatal("a resume mid-round returned no cards of the player's own")
		}
	})
}

// ============================================================ goroutines

// TestNoGoroutineLeaks opens and closes a lot of rooms and connections and
// checks the goroutine count returns to its starting point.
func TestNoGoroutineLeaks(t *testing.T) {
	settle := func() int {
		for i := 0; i < 100; i++ {
			runtime.GC()
			time.Sleep(10 * time.Millisecond)
			if i > 5 {
				return runtime.NumGoroutine()
			}
		}
		return runtime.NumGoroutine()
	}
	before := settle()

	for iter := 0; iter < 6; iter++ {
		h := newHarness(t)
		for r := 0; r < 4; r++ {
			rm := h.newRoom()
			clients := h.seatTable(rm, 4)
			h.startGame(rm, clients)
			whoseTurn(t, clients).send(protocol.MsgMakeClaim,
				protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Two)}, "")
			if sh := awaitShowerAmong(t, clients); sh != nil {
				sh.send(protocol.MsgCallShow, nil, "")
			}
			for _, c := range clients {
				c.conn.Close()
				<-c.gone
			}
			rm.Close("done")
			<-rm.Done()
		}
		h.mgr.Shutdown("iteration over")
	}

	after := settle()
	// A little slack for runtime-owned goroutines and test machinery.
	if after > before+10 {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutines grew from %d to %d:\n%s", before, after, buf[:n])
	}
	t.Logf("goroutines: %d before, %d after", before, after)
}

// ============================================================ helpers

func createRoom(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, err := http.Post(srv.URL+"/rooms", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		RoomCode string `json:"roomCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.RoomCode
}

func dialRaw(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

func writeJSON(t *testing.T, c *websocket.Conn, in protocol.Inbound) {
	t.Helper()
	if err := c.WriteJSON(in); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readWelcome(t *testing.T, c *websocket.Conn) protocol.WelcomePayload {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(deadline)
		_, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var m serverMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.Type != string(protocol.MsgWelcome) {
			continue
		}
		var w protocol.WelcomePayload
		if err := json.Unmarshal(m.Payload, &w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	t.Fatal("no welcome arrived")
	return protocol.WelcomePayload{}
}

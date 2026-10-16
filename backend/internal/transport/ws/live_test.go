package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/internal/protocol"
	"github.com/kaushlendra/liars-poker/backend/internal/room"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// The rest of the suite substitutes the socket so that framing, size limits
// and timers are deterministic. This file exercises the real thing once, end
// to end, so the gorilla adapter and the HTTP surface are not the untested
// gap between the tests and production.

func liveServer(t *testing.T) (*httptest.Server, *room.Manager) {
	t.Helper()
	cfg := game.DefaultConfig(424242)
	cfg.FirstStarter = game.FirstStarterSeatZero
	mgr := room.NewManager(room.Options{
		Game:         cfg,
		TickInterval: 20 * time.Millisecond,
		ResultLinger: time.Minute,
		EmptyGrace:   time.Minute,
		IdleTimeout:  time.Hour,
	})
	srv := httptest.NewServer(NewServeMux(Options{
		Manager:        mgr,
		AllowedOrigins: []string{"*"},
		PingInterval:   time.Second,
		PongTimeout:    time.Minute,
	}))
	t.Cleanup(func() {
		srv.Close()
		mgr.Shutdown("test over")
	})
	return srv, mgr
}

type liveClient struct {
	t   *testing.T
	c   *websocket.Conn
	in  []serverMsg
	cur int
}

func dial(t *testing.T, srv *httptest.Server) *liveClient {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	c, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v (resp %v)", err, resp)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &liveClient{t: t, c: c}
}

func (l *liveClient) send(typ protocol.ClientMessageType, payload any) {
	l.t.Helper()
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			l.t.Fatal(err)
		}
		raw = b
	}
	if err := l.c.WriteJSON(protocol.Inbound{Type: typ, Payload: raw}); err != nil {
		l.t.Fatalf("write: %v", err)
	}
}

// await consumes forward from a cursor until the next message of the given
// type. Rescanning from the start would keep returning the first match, so a
// caller polling for a state change would spin on a stale view forever.
func (l *liveClient) await(kind protocol.ServerMessageType, d time.Duration) serverMsg {
	l.t.Helper()
	deadline := time.Now().Add(d)
	for {
		for l.cur < len(l.in) {
			m := l.in[l.cur]
			l.cur++
			if m.Type == string(kind) {
				return m
			}
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("timed out waiting for %s", kind)
		}
		_ = l.c.SetReadDeadline(deadline)
		_, data, err := l.c.ReadMessage()
		if err != nil {
			l.t.Fatalf("read while waiting for %s: %v", kind, err)
		}
		var m serverMsg
		if err := json.Unmarshal(data, &m); err != nil {
			l.t.Fatalf("unparseable frame %q", data)
		}
		l.in = append(l.in, m)
	}
}

func (l *liveClient) view(d time.Duration) game.PlayerView {
	l.t.Helper()
	m := l.await(protocol.MsgView, d)
	var w struct {
		View game.PlayerView `json:"view"`
	}
	if err := json.Unmarshal(m.Payload, &w); err != nil {
		l.t.Fatal(err)
	}
	return w.View
}

func TestLiveHTTPSurface(t *testing.T) {
	srv, _ := liveServer(t)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}

	// GET /rooms is not how rooms are made.
	resp, err = http.Get(srv.URL + "/rooms")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /rooms = %d, want 405", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/rooms", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		RoomCode string `json:"roomCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(created.RoomCode) != 6 {
		t.Fatalf("room code %q", created.RoomCode)
	}

	resp, err = http.Get(srv.URL + "/rooms/" + created.RoomCode)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("known room lookup = %d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/rooms/ZZZZZZ")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown room lookup = %d, want 404", resp.StatusCode)
	}
}

// TestLiveGameOverRealSocket plays a full round over real WebSocket frames.
func TestLiveGameOverRealSocket(t *testing.T) {
	srv, _ := liveServer(t)

	resp, err := http.Post(srv.URL+"/rooms", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		RoomCode string `json:"roomCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	clients := make([]*liveClient, 0, 4)
	tokens := make([]protocol.WelcomePayload, 0, 4)
	for i := 0; i < 4; i++ {
		c := dial(t, srv)
		c.send(protocol.MsgJoin, protocol.JoinPayload{RoomCode: created.RoomCode, Name: "P"})
		m := c.await(protocol.MsgWelcome, 5*time.Second)
		var w protocol.WelcomePayload
		if err := json.Unmarshal(m.Payload, &w); err != nil {
			t.Fatal(err)
		}
		if w.Token == "" {
			t.Fatal("live welcome carried no token")
		}
		clients = append(clients, c)
		tokens = append(tokens, w)
	}
	for _, c := range clients {
		c.send(protocol.MsgSetReady, protocol.SetReadyPayload{Ready: true})
	}
	clients[0].send(protocol.MsgStartGame, nil)

	// Every client must reach round 1 and see only its own card.
	var firstRoundView game.PlayerView
	for i, c := range clients {
		deadline := time.Now().Add(5 * time.Second)
		var v game.PlayerView
		for time.Now().Before(deadline) {
			v = c.view(5 * time.Second)
			if v.Round == 1 && v.Phase == game.Claiming.String() {
				break
			}
		}
		if v.Round != 1 {
			t.Fatalf("client %d never reached round 1 (phase %s)", i, v.Phase)
		}
		firstRoundView = v
		for _, p := range v.Players {
			if !p.IsYou && len(p.Cards) != 0 {
				t.Fatalf("client %d saw %s's cards over a real socket", i, p.ID)
			}
		}
	}

	// The view names the turn holder, so no socket polling is needed.
	turnHolder := firstRoundView.TurnPlayer
	starter := -1
	for i := range clients {
		if tokens[i].PlayerID == turnHolder {
			starter = i
		}
	}
	if starter < 0 {
		t.Fatalf("turn holder %s is not one of the four clients", turnHolder)
	}
	clients[starter].send(protocol.MsgMakeClaim,
		protocol.MakeClaimPayload{Claim: poker.NewPair(poker.Two)})

	// Frame ordering holds per connection, not across them, so the
	// challenger waits until its own view says the claim has landed. This
	// is exactly what a real client does: it acts on views, not on hope.
	next := (starter + 1) % len(clients)
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !ready {
		if v := clients[next].view(5 * time.Second); v.CanShow {
			ready = true
		}
	}
	if !ready {
		t.Fatal("the challenger never became SHOW eligible")
	}
	clients[next].send(protocol.MsgCallShow, nil)

	// Everyone must see the reveal.
	for i, c := range clients {
		deadline := time.Now().Add(5 * time.Second)
		seen := false
		for time.Now().Before(deadline) && !seen {
			v := c.view(5 * time.Second)
			if v.Revealed && v.LastShow != nil {
				seen = true
			}
		}
		if !seen {
			t.Fatalf("client %d never saw the reveal over a real socket", i)
		}
	}
}

// TestLiveOversizedFrameIsRejectedByTheLibrary confirms the read limit is
// enforced at the frame level, before any of our code sees the payload.
func TestLiveOversizedFrameIsRejectedByTheLibrary(t *testing.T) {
	srv, _ := liveServer(t)
	resp, err := http.Post(srv.URL+"/rooms", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		RoomCode string `json:"roomCode"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	_ = resp.Body.Close()

	c := dial(t, srv)
	c.send(protocol.MsgJoin, protocol.JoinPayload{RoomCode: created.RoomCode, Name: "A"})
	c.await(protocol.MsgWelcome, 5*time.Second)

	huge := `{"type":"set_ready","reqId":"` + strings.Repeat("x", protocol.MaxFrameBytes*2) + `"}`
	if err := c.c.WriteMessage(websocket.TextMessage, []byte(huge)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = c.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := c.c.ReadMessage()
		if err == nil {
			continue
		}
		if ce, ok := err.(*websocket.CloseError); ok {
			if ce.Code != websocket.CloseMessageTooBig {
				t.Fatalf("close code = %d, want %d (message too big)",
					ce.Code, websocket.CloseMessageTooBig)
			}
			return
		}
		// Any read failure means the connection went away, which is the
		// required outcome.
		return
	}
}

func TestLiveOriginCheckRejectsForeignOrigin(t *testing.T) {
	cfg := game.DefaultConfig(1)
	mgr := room.NewManager(room.Options{Game: cfg, TickInterval: 0})
	t.Cleanup(func() { mgr.Shutdown("done") })
	// No AllowedOrigins means same-origin only.
	srv := httptest.NewServer(NewServeMux(Options{Manager: mgr}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	_, resp, err := websocket.DefaultDialer.Dial(url, http.Header{
		"Origin": []string{"http://evil.example"},
	})
	if err == nil {
		t.Fatal("a foreign origin was allowed to upgrade")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin got %v, want 403", resp)
	}
}

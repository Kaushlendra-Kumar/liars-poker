// Command stress drives many concurrent rooms and real WebSocket clients
// against the authoritative server, to shake out concurrency, lifecycle and
// resource problems that a unit test will not reach.
//
//	go run ./cmd/stress
//	go run -race ./cmd/stress -rooms 40 -duration 20s
//
// Clients here are view-only on purpose: they read the authoritative
// PlayerView and the static claim ladder and ignore the event stream
// entirely. If the stress run completes, it is also evidence that no
// gameplay logic is required on the client.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/internal/obs"
	"github.com/kaushlendra/liars-poker/backend/internal/protocol"
	"github.com/kaushlendra/liars-poker/backend/internal/room"
	"github.com/kaushlendra/liars-poker/backend/internal/transport/ws"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

type counters struct {
	games       atomic.Int64
	actions     atomic.Int64
	reconnects  atomic.Int64
	disconnects atomic.Int64
	failures    atomic.Int64
	leaks       atomic.Int64
	badFrames   atomic.Int64
}

type serverMsg struct {
	Type    string          `json:"type"`
	Seq     int             `json:"seq"`
	Payload json.RawMessage `json:"payload"`
}

func main() {
	rooms := flag.Int("rooms", 24, "concurrent rooms")
	players := flag.Int("players", 0, "players per room; 0 cycles 4..9")
	duration := flag.Duration("duration", 12*time.Second, "how long to run")
	churn := flag.Int("churn", 12, "percent chance per action of a disconnect/resume cycle")
	seed := flag.Int64("seed", 1, "base seed")
	flag.Parse()

	logger := obs.Discard() // metrics only; stress output is the report
	cfg := game.DefaultConfig(*seed)
	cfg.TurnDuration = 400 * time.Millisecond
	cfg.RoundEndDuration = 50 * time.Millisecond

	mgr := room.NewManager(room.Options{
		Game:                    cfg,
		TickInterval:            10 * time.Millisecond,
		Log:                     logger,
		MaxRooms:                4096,
		MaxConnectionsPerRoom:   64,
		MaxConnectionsPerPlayer: 4,
		ResultLinger:            200 * time.Millisecond,
		EmptyGrace:              200 * time.Millisecond,
		IdleTimeout:             30 * time.Second,
	})
	srv := httptest.NewServer(ws.NewServeMux(ws.Options{
		Manager:                 mgr,
		AllowedOrigins:          []string{"*"},
		Log:                     logger,
		PingInterval:            500 * time.Millisecond,
		PongTimeout:             30 * time.Second,
		RateBurst:               2000,
		RateWindow:              time.Second,
		MaxConcurrentHandshakes: 512,
	}))

	fmt.Printf("stress: rooms=%d players=%s duration=%s churn=%d%% race=%v\n",
		*rooms, playersLabel(*players), *duration, *churn, raceEnabled)
	runtime.GC()
	before := runtime.NumGoroutine()
	fmt.Printf("goroutines before: %d\n\n", before)

	var c counters
	deadline := time.Now().Add(*duration)
	var wg sync.WaitGroup
	for i := 0; i < *rooms; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := *players
			if n == 0 {
				n = game.MinPlayers + i%(game.MaxPlayers-game.MinPlayers+1)
			}
			rng := rand.New(rand.NewSource(*seed + int64(i)*7919))
			for time.Now().Before(deadline) {
				runRoom(srv, n, rng, *churn, deadline, &c)
			}
		}(i)
	}
	wg.Wait()

	srv.Close()
	mgr.Shutdown("stress over")

	// Give connection goroutines a moment to unwind, then measure.
	for i := 0; i < 60; i++ {
		runtime.GC()
		time.Sleep(25 * time.Millisecond)
		if runtime.NumGoroutine() <= before+2 {
			break
		}
	}
	after := runtime.NumGoroutine()
	m := logger.Metrics().Snapshot()

	fmt.Println(strings.Repeat("-", 62))
	fmt.Printf("games completed        %d\n", c.games.Load())
	fmt.Printf("actions sent           %d\n", c.actions.Load())
	fmt.Printf("reconnects             %d\n", c.reconnects.Load())
	fmt.Printf("disconnects            %d\n", c.disconnects.Load())
	fmt.Printf("room closures          %d\n", m.RoomsClosed)
	fmt.Printf("rooms created          %d\n", m.RoomsCreated)
	fmt.Printf("players joined         %d\n", m.PlayersJoined)
	fmt.Printf("resumes accepted       %d\n", m.Resumes)
	fmt.Printf("resumes rejected       %d\n", m.ResumesRejected)
	fmt.Printf("games ended            %d\n", m.GamesEnded)
	fmt.Printf("games abandoned        %d\n", m.GamesAbandoned)
	fmt.Printf("protocol errors        %d\n", m.ProtocolErrors)
	fmt.Printf("backpressure drops     %d\n", m.Backpressure)
	fmt.Printf("limit rejections       %d\n", m.LimitRejections)
	fmt.Printf("actor panics           %d\n", m.Panics)
	fmt.Printf("connections total      %d\n", m.ConnectionsTotal)
	fmt.Printf("connections still open %d\n", m.ConnectionsOpen)
	fmt.Println(strings.Repeat("-", 62))
	fmt.Printf("goroutines before      %d\n", before)
	fmt.Printf("goroutines after       %d\n", after)
	fmt.Printf("live rooms after       %d\n", mgr.Count())
	fmt.Printf("unparseable frames     %d\n", c.badFrames.Load())
	fmt.Printf("hidden-card leaks      %d\n", c.leaks.Load())
	fmt.Printf("failures               %d\n", c.failures.Load())

	problems := 0
	if after > before+8 {
		fmt.Printf("\nGOROUTINE LEAK: %d -> %d\n", before, after)
		problems++
	}
	if mgr.Count() != 0 {
		fmt.Printf("\nROOM LEAK: %d rooms still registered after shutdown\n", mgr.Count())
		problems++
	}
	for name, n := range map[string]int64{
		"failures": c.failures.Load(), "leaks": c.leaks.Load(),
		"unparseable frames": c.badFrames.Load(), "actor panics": m.Panics,
	} {
		if n != 0 {
			fmt.Printf("\nFAILURE: %s = %d\n", name, n)
			problems++
		}
	}
	if problems == 0 {
		fmt.Printf("\nclean: no leaks, no panics, no failures, no hidden-card exposure\n")
		return
	}
	os.Exit(1)
}

func playersLabel(n int) string {
	if n == 0 {
		return fmt.Sprintf("%d-%d", game.MinPlayers, game.MaxPlayers)
	}
	return fmt.Sprint(n)
}

// player is a view-only client.
type player struct {
	conn     *websocket.Conn
	url      string
	roomCode string
	id       game.PlayerID
	token    string
	view     game.PlayerView
	own      map[string]bool
	inbox    chan serverMsg
	dead     chan struct{}
	c        *counters
}

func runRoom(srv *httptest.Server, n int, rng *rand.Rand, churn int,
	deadline time.Time, c *counters) {

	code, err := createRoom(srv)
	if err != nil {
		c.failures.Add(1)
		return
	}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	players := make([]*player, 0, n)
	for i := 0; i < n; i++ {
		p, err := joinPlayer(wsURL, code, c)
		if err != nil {
			c.failures.Add(1)
			closeAll(players)
			return
		}
		players = append(players, p)
	}
	defer closeAll(players)

	for _, p := range players {
		p.send(protocol.MsgSetReady, protocol.SetReadyPayload{Ready: true}, "")
	}
	players[0].send(protocol.MsgStartGame, nil, "")

	ladder := poker.AllClaims()
	for time.Now().Before(deadline) {
		progressed := false
		for _, p := range players {
			if !p.alive() {
				p.reconnect(wsURL)
				continue
			}
			if !p.pump() {
				continue
			}
			v := p.view
			if v.Phase == game.GameOver.String() {
				c.games.Add(1)
				return
			}
			switch {
			case v.CanShow:
				p.send(protocol.MsgCallShow, nil, reqID(rng))
				c.actions.Add(1)
				progressed = true
			case v.YourTurn && v.CanClaim:
				idx := v.StandingOrdinal + 1
				if idx < 0 || idx >= len(ladder) {
					p.send(protocol.MsgCallShow, nil, reqID(rng))
				} else {
					p.send(protocol.MsgMakeClaim,
						protocol.MakeClaimPayload{Claim: ladder[idx]}, reqID(rng))
				}
				c.actions.Add(1)
				progressed = true
			}
			// Presence churn: drop and come back on the same credential.
			if rng.Intn(100) < churn {
				p.cycle(wsURL)
			}
		}
		if !progressed {
			time.Sleep(time.Millisecond)
		}
	}
}

func reqID(rng *rand.Rand) string { return fmt.Sprintf("r%d", rng.Int63()) }

func createRoom(srv *httptest.Server) (string, error) {
	resp, err := http.Post(srv.URL+"/rooms", "application/json", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		RoomCode string `json:"roomCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.RoomCode, nil
}

func joinPlayer(wsURL, code string, c *counters) (*player, error) {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}
	p := &player{conn: conn, url: wsURL, roomCode: code, own: map[string]bool{}, c: c,
		inbox: make(chan serverMsg, 256), dead: make(chan struct{})}
	p.startReader()
	p.send(protocol.MsgJoin, protocol.JoinPayload{RoomCode: code, Name: "bot"}, "")
	m, ok := p.await(protocol.MsgWelcome, 3*time.Second)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("no welcome")
	}
	var w protocol.WelcomePayload
	if err := json.Unmarshal(m.Payload, &w); err != nil {
		_ = conn.Close()
		return nil, err
	}
	p.id, p.token, p.view = w.PlayerID, w.Token, w.View
	return p, nil
}

func (p *player) send(typ protocol.ClientMessageType, payload any, reqID string) {
	if p.conn == nil {
		return
	}
	var raw json.RawMessage
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	_ = p.conn.WriteJSON(protocol.Inbound{
		Type: typ, Version: protocol.ProtocolVersion, ReqID: reqID, Payload: raw})
}

// startReader owns the socket's read side.
//
// gorilla treats an expired read deadline as permanent corruption of the
// connection, so polling ReadMessage with a short deadline is not a valid
// pattern: the first timeout kills the connection and every later read
// panics. A dedicated blocking reader per connection is the only correct
// shape, and it is what a real client would do anyway.
func (p *player) startReader() {
	conn := p.conn
	inbox := p.inbox
	dead := p.dead
	go func() {
		defer close(dead)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m serverMsg
			if err := json.Unmarshal(data, &m); err != nil {
				p.c.badFrames.Add(1)
				continue
			}
			select {
			case inbox <- m:
			default:
				// A view-only client only needs the newest view, so
				// shedding the oldest queued message is harmless.
				select {
				case <-inbox:
				default:
				}
				select {
				case inbox <- m:
				default:
				}
			}
		}
	}()
}

// alive reports whether the reader is still running.
func (p *player) alive() bool {
	if p.conn == nil {
		return false
	}
	select {
	case <-p.dead:
		_ = p.conn.Close()
		p.conn = nil
		return false
	default:
		return true
	}
}

// await waits for a message of the given type.
func (p *player) await(kind protocol.ServerMessageType, d time.Duration) (serverMsg, bool) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		select {
		case m := <-p.inbox:
			if m.Type == string(kind) {
				return m, true
			}
		case <-p.dead:
			return serverMsg{}, false
		case <-time.After(2 * time.Millisecond):
		}
	}
	return serverMsg{}, false
}

// pump drains whatever has arrived and keeps the latest view, checking for
// hidden-card exposure as it goes.
func (p *player) pump() bool {
	if !p.alive() {
		return false
	}
	got := false
	for i := 0; i < 64; i++ {
		var m serverMsg
		select {
		case m = <-p.inbox:
		default:
			return got
		}
		switch m.Type {
		case string(protocol.MsgView), string(protocol.MsgSnapshot), string(protocol.MsgWelcome):
			var w struct {
				View game.PlayerView `json:"view"`
			}
			if json.Unmarshal(m.Payload, &w) != nil {
				p.c.badFrames.Add(1)
				continue
			}
			p.view = w.View
			got = true
			p.own = map[string]bool{}
			for _, pl := range w.View.Players {
				if pl.IsYou {
					for _, card := range pl.Cards {
						p.own[card.Code()] = true
					}
				}
			}
			if !w.View.Revealed {
				for _, pl := range w.View.Players {
					if !pl.IsYou && len(pl.Cards) > 0 {
						p.c.leaks.Add(1)
					}
				}
			}
		case string(protocol.MsgEvent):
			var ev protocol.EventPayload
			if json.Unmarshal(m.Payload, &ev) != nil {
				p.c.badFrames.Add(1)
				continue
			}
			if ev.Type == game.EvCardsDealt.String() && len(ev.Hands) > 1 {
				p.c.leaks.Add(1)
			}
		}
	}
	return got
}

// cycle drops the connection and resumes on the same credential.
func (p *player) cycle(wsURL string) {
	if p.conn == nil || p.token == "" {
		return
	}
	_ = p.conn.Close()
	<-p.dead
	p.conn = nil
	p.c.disconnects.Add(1)
	p.reconnect(wsURL)
}

// reconnect resumes on the stored credential.
func (p *player) reconnect(wsURL string) {
	if p.conn != nil || p.token == "" {
		return
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return
	}
	p.conn = conn
	p.inbox = make(chan serverMsg, 256)
	p.dead = make(chan struct{})
	p.startReader()
	p.send(protocol.MsgResume, protocol.ResumePayload{
		RoomCode: p.roomCode, PlayerID: p.id, Token: p.token, LastSeq: p.view.Seq}, "")
	if _, ok := p.await(protocol.MsgWelcome, time.Second); ok {
		p.c.reconnects.Add(1)
		return
	}
	_ = conn.Close()
	p.conn = nil
}

func closeAll(players []*player) {
	for _, p := range players {
		if p.conn != nil {
			_ = p.conn.Close()
		}
	}
}

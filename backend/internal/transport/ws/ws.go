// Package ws is the WebSocket transport. It is a pump and nothing more.
//
// It does not evaluate hands, compare claims, decide SHOW truth, implement
// turn order or elimination, or mutate game state. Every client intent goes
// to room.Do, which serialises it into game.Apply; every outbound message
// comes off a room.Subscriber queue that the room already projected.
//
// Conn abstracts the socket so the entire stack above it can be tested
// deterministically, including malformed frames, oversized frames,
// backpressure and heartbeat expiry, without opening a port.
package ws

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/internal/obs"
	"github.com/kaushlendra/liars-poker/backend/internal/protocol"
	"github.com/kaushlendra/liars-poker/backend/internal/room"
	"github.com/kaushlendra/liars-poker/backend/internal/session"
)

// Conn is one bidirectional message stream.
type Conn interface {
	// Read returns the next message, or an error when the peer is gone.
	Read() ([]byte, error)
	// Write sends one message.
	Write([]byte) error
	// Close stops the connection. Safe to call more than once.
	Close() error
}

// Options configures the transport.
type Options struct {
	Manager      *room.Manager
	Now          func() time.Time
	PingInterval time.Duration
	PongTimeout  time.Duration
	RateBurst    int
	RateWindow   time.Duration
	WriteTimeout time.Duration
	// MaxConcurrentHandshakes bounds how many upgrades may be in flight at
	// once, so a reconnect storm cannot exhaust the process before any
	// per-room limit applies.
	MaxConcurrentHandshakes int
	Log                     *obs.Logger
	// AllowedOrigins gates the upgrade. Empty means same-origin only; a
	// single "*" entry allows any origin and is for development only.
	AllowedOrigins []string
}

func (o Options) withDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PingInterval == 0 {
		o.PingInterval = 15 * time.Second
	}
	if o.PongTimeout == 0 {
		o.PongTimeout = 45 * time.Second
	}
	if o.RateBurst == 0 {
		o.RateBurst = 20
	}
	if o.RateWindow == 0 {
		o.RateWindow = 5 * time.Second
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.MaxConcurrentHandshakes == 0 {
		o.MaxConcurrentHandshakes = 256
	}
	if o.Log == nil {
		o.Log = obs.Discard()
	}
	return o
}

// ---------------------------------------------------------------- limiter

// limiter is a fixed-window counter applied before parsing, so a flood costs
// a length check rather than a JSON decode.
type limiter struct {
	burst    int
	window   time.Duration
	count    int
	windowAt time.Time
}

func (l *limiter) allow(now time.Time) bool {
	if l.windowAt.IsZero() || now.Sub(l.windowAt) >= l.window {
		l.windowAt = now
		l.count = 0
	}
	l.count++
	return l.count <= l.burst
}

// ---------------------------------------------------------------- serve

// Serve runs one connection to completion. It returns when the peer
// disconnects, the room closes, or the connection is dropped for abuse.
func Serve(conn Conn, opts Options) {
	opts = opts.withDefaults()
	c := &connection{conn: conn, opts: opts,
		outbox: make(chan outMsg, 32),
		lim:    &limiter{burst: opts.RateBurst, window: opts.RateWindow}}
	c.run()
}

// outMsg is a queued write. done, when set, is closed once the write pump
// has actually written it, which lets a fatal error be flushed before the
// connection closes.
type outMsg struct {
	m    protocol.Outbound
	done chan struct{}
}

type connection struct {
	conn Conn
	opts Options
	lim  *limiter

	rm  *room.Room
	sub *room.Subscriber
	me  game.PlayerID

	closeOnce sync.Once
	done      chan struct{}

	// outbox carries writes that do not come from the room: error replies
	// from the read goroutine and pings from the heartbeat.
	//
	// A WebSocket connection is not safe for concurrent writers, and the
	// race detector cannot see the problem because each individual write is
	// internally synchronised: two goroutines simply interleave two frames
	// and corrupt the stream. So the write pump is the only writer once it
	// starts, and everything else queues here.
	outbox chan outMsg
	// writerUp is read and written only by the read goroutine, which is the
	// sole writer before the pump starts.
	writerUp bool

	mu       sync.Mutex
	lastPong time.Time
}

func (c *connection) run() {
	c.done = make(chan struct{})
	c.lastPong = c.opts.Now()
	defer c.shutdown()

	// The first frame must establish identity.
	if !c.handshake() {
		return
	}

	// From here the write pump owns the socket.
	c.writerUp = true
	go c.writePump()
	go c.heartbeat()

	for {
		frame, err := c.conn.Read()
		if err != nil {
			return
		}
		if !c.handleFrame(frame) {
			return
		}
		select {
		case <-c.done:
			return
		case <-c.sub.Done():
			return
		default:
		}
	}
}

// handshake reads and processes the opening join or resume.
func (c *connection) handshake() bool {
	frame, err := c.conn.Read()
	if err != nil {
		return false
	}
	in, derr := protocol.DecodeInbound(frame)
	if derr != nil {
		c.fatal("", derr)
		return false
	}
	if !protocol.VersionSupported(in.Version) {
		c.fatal(in.ReqID, protocol.ErrUnsupportedVersion)
		return false
	}
	switch in.Type {
	case protocol.MsgJoin:
		return c.doJoin(in)
	case protocol.MsgResume:
		return c.doResume(in)
	}
	c.fatal(in.ReqID, game.ErrUnknownAction)
	return false
}

func (c *connection) doJoin(in protocol.Inbound) bool {
	var p protocol.JoinPayload
	if err := protocol.DecodePayload(in.Payload, &p); err != nil {
		c.fatal(in.ReqID, err)
		return false
	}
	rm, err := c.opts.Manager.Get(strings.ToUpper(strings.TrimSpace(p.RoomCode)))
	if err != nil {
		c.fatal(in.ReqID, err)
		return false
	}
	sub, id, _, err := rm.Join(p.Name)
	if err != nil {
		c.fatal(in.ReqID, err)
		return false
	}
	c.rm, c.sub, c.me = rm, sub, id
	return true
}

func (c *connection) doResume(in protocol.Inbound) bool {
	var p protocol.ResumePayload
	if err := protocol.DecodePayload(in.Payload, &p); err != nil {
		c.fatal(in.ReqID, err)
		return false
	}
	code := strings.ToUpper(strings.TrimSpace(p.RoomCode))
	rm, err := c.opts.Manager.Get(code)
	if err != nil {
		// An unknown room must look exactly like a bad token, otherwise the
		// reply enumerates rooms.
		c.fatal(in.ReqID, protocol.ErrUnauthorized)
		return false
	}
	sub, err := rm.Resume(p.PlayerID, session.ParseToken(p.Token), p.LastSeq)
	if err != nil {
		c.fatal(in.ReqID, protocol.ErrUnauthorized)
		return false
	}
	c.rm, c.sub, c.me = rm, sub, p.PlayerID
	return true
}

// handleFrame processes one post-handshake frame. It returns false when the
// connection must close.
func (c *connection) handleFrame(frame []byte) bool {
	now := c.opts.Now()
	if !c.lim.allow(now) {
		c.fatal("", protocol.ErrRateLimited)
		return false
	}
	if len(frame) > protocol.MaxFrameBytes {
		// Oversized frames are fatal: a client that ignores the limit is
		// either broken or hostile.
		c.fatal("", protocol.ErrFrameTooLarge)
		return false
	}
	in, err := protocol.DecodeInbound(frame)
	if err != nil {
		// Malformed content is not fatal. Report it and keep the socket.
		code := protocol.CodeFor(err)
		c.opts.Log.ProtocolError("", string(c.me), string(code))
		c.send(protocol.ErrorMessage("", code))
		return true
	}

	switch in.Type {
	case protocol.MsgPong:
		c.mu.Lock()
		c.lastPong = now
		c.mu.Unlock()
		return true
	case protocol.MsgJoin, protocol.MsgResume:
		// Identity is established once per connection.
		c.send(protocol.ErrorMessage(in.ReqID, protocol.CodeUnknownAction))
		return true
	}

	// Everything else is an engine intent. The room stamps the actor from
	// this connection's authenticated identity and the server clock, so
	// nothing in the frame can influence either.
	if err := c.rm.Do(c.me, in); err != nil {
		if errors.Is(err, protocol.ErrRoomClosed) {
			c.send(protocol.ErrorMessage(in.ReqID, protocol.CodeRoomClosed))
			return false
		}
		code := protocol.CodeFor(err)
		c.opts.Log.ProtocolError("", string(c.me), string(code))
		c.send(protocol.ErrorMessage(in.ReqID, code))
	}
	return true
}

// writePump is the only writer. Serialising here means the Conn never needs
// to be write-safe for concurrent use.
func (c *connection) writePump() {
	for {
		select {
		case <-c.done:
			return
		case <-c.sub.Done():
			// The room dropped us, for backpressure or because it closed.
			// Drain whatever is already queued first: a closing room queues
			// its ClosedMessage and then releases the subscriber, and
			// select would otherwise pick the release at random and the
			// client would never learn why it was dropped.
			c.flush()
			c.close()
			return
		case om := <-c.outbox:
			ok := c.write(om.m)
			if om.done != nil {
				close(om.done)
			}
			if !ok {
				c.close()
				return
			}
		case m, ok := <-c.sub.Out():
			if !ok {
				c.close()
				return
			}
			if !c.write(m) {
				c.close()
				return
			}
		}
	}
}

func (c *connection) heartbeat() {
	if c.opts.PingInterval <= 0 {
		return
	}
	t := time.NewTicker(c.opts.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			now := c.opts.Now()
			c.mu.Lock()
			silent := now.Sub(c.lastPong)
			c.mu.Unlock()
			if silent > c.opts.PongTimeout {
				c.close()
				return
			}
			c.send(protocol.PingMessage())
		}
	}
}

// flush writes everything already queued, without blocking for more. The
// outbox goes first so a queued error or close reason is not lost behind
// state updates.
func (c *connection) flush() {
	for {
		select {
		case om := <-c.outbox:
			c.write(om.m)
			if om.done != nil {
				close(om.done)
			}
			continue
		default:
		}
		select {
		case m, ok := <-c.sub.Out():
			if !ok {
				return
			}
			if !c.write(m) {
				return
			}
			continue
		default:
		}
		return
	}
}

func (c *connection) write(m protocol.Outbound) bool {
	frame, err := protocol.Encode(m)
	if err != nil {
		return false
	}
	return c.conn.Write(frame) == nil
}

// send queues a message that did not come from the room: an error reply or a
// ping. Before the write pump exists this goroutine is the only writer, so it
// writes directly; afterwards it must queue.
//
// If the outbox is full the message is dropped. That is safe because nothing
// queued here is authoritative state: errors and pings are advisory, and the
// authoritative stream has its own backpressure rule of closing the
// connection so the client resumes and receives a fresh view.
func (c *connection) send(m protocol.Outbound) {
	if !c.writerUp {
		c.write(m)
		return
	}
	select {
	case c.outbox <- outMsg{m: m}:
	case <-c.done:
	default:
	}
}

// fatal reports an error and closes, waiting for the error to reach the wire
// first so the client learns why it was dropped.
func (c *connection) fatal(reqID string, err error) {
	m := protocol.ErrorMessage(reqID, protocol.CodeFor(err))
	if !c.writerUp {
		c.write(m)
		c.close()
		return
	}
	done := make(chan struct{})
	select {
	case c.outbox <- outMsg{m: m, done: done}:
		select {
		case <-done:
		case <-c.done:
		case <-time.After(c.opts.WriteTimeout):
		}
	case <-c.done:
	}
	c.close()
}

func (c *connection) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}

func (c *connection) shutdown() {
	if c.rm != nil && c.sub != nil {
		c.rm.Detach(c.sub)
	}
	c.close()
}

// ---------------------------------------------------------------- gorilla

type gorillaConn struct {
	c            *websocket.Conn
	writeTimeout time.Duration
}

func (g *gorillaConn) Read() ([]byte, error) {
	_, data, err := g.c.ReadMessage()
	return data, err
}

func (g *gorillaConn) Write(b []byte) error {
	if g.writeTimeout > 0 {
		_ = g.c.SetWriteDeadline(time.Now().Add(g.writeTimeout))
	}
	return g.c.WriteMessage(websocket.TextMessage, b)
}

func (g *gorillaConn) Close() error { return g.c.Close() }

// Handler upgrades an HTTP request and serves it.
func Handler(opts Options) http.Handler {
	opts = opts.withDefaults()
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin:     originChecker(opts.AllowedOrigins),
	}
	// One slot per in-flight handshake. Acquired before the upgrade so a
	// reconnect storm is refused cheaply rather than accepted and then
	// starved.
	gate := make(chan struct{}, opts.MaxConcurrentHandshakes)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			opts.Log.LimitRejected("concurrent handshakes")
			w.Header().Set("Retry-After", "1")
			http.Error(w, string(protocol.CodeBusy), http.StatusServiceUnavailable)
			return
		}
		raw, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return // Upgrade already wrote the response
		}
		opts.Log.ConnectionOpened(req.RemoteAddr)
		defer opts.Log.ConnectionClosed(req.RemoteAddr, "handler returned")
		// The library enforces the inbound limit at the frame level, so an
		// oversized frame closes the connection with 1009 before any of our
		// code sees it.
		raw.SetReadLimit(protocol.MaxFrameBytes)
		Serve(&gorillaConn{c: raw, writeTimeout: opts.WriteTimeout}, opts)
	})
}

func originChecker(allowed []string) func(*http.Request) bool {
	for _, a := range allowed {
		if a == "*" {
			// Development only.
			return func(*http.Request) bool { return true }
		}
	}
	set := map[string]struct{}{}
	for _, a := range allowed {
		set[strings.ToLower(a)] = struct{}{}
	}
	return func(req *http.Request) bool {
		origin := req.Header.Get("Origin")
		if origin == "" {
			return true // non-browser client
		}
		if len(set) == 0 {
			return strings.EqualFold(origin, "http://"+req.Host) ||
				strings.EqualFold(origin, "https://"+req.Host)
		}
		_, ok := set[strings.ToLower(origin)]
		return ok
	}
}

// ---------------------------------------------------------------- http

type createRoomResponse struct {
	RoomCode string `json:"roomCode"`
}

// NewServeMux wires the HTTP surface: room creation and lookup stay off the
// socket path, so the WebSocket handler only ever deals with an existing
// room.
func NewServeMux(opts Options) *http.ServeMux {
	opts = opts.withDefaults()
	mux := http.NewServeMux()

	// Liveness: the process is up and serving. It says nothing about
	// whether this instance should receive new traffic.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Readiness: this instance can take new rooms. A full instance reports
	// not-ready so a load balancer stops sending it joins, while existing
	// rooms keep playing. Neither probe exposes any game state.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if opts.Manager == nil || opts.Manager.AtCapacity() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("at capacity"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	mux.HandleFunc("/rooms", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rm, err := opts.Manager.Create()
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(createRoomResponse{RoomCode: rm.Code()})
	})

	mux.HandleFunc("/rooms/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		code := strings.ToUpper(strings.TrimPrefix(req.URL.Path, "/rooms/"))
		if _, err := opts.Manager.Get(code); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.Handle("/ws", Handler(opts))
	return mux
}

// Package room is the authoritative actor. One goroutine per room owns the
// game state exclusively; every command is applied serially through
// game.Apply, so concurrent mutation is structurally impossible rather than
// merely guarded.
//
// It contains no gameplay logic. It does not evaluate hands, compare claims,
// decide SHOW truth, implement turn order or elimination, or mutate state
// directly. Its three points of contact with the game are game.Apply,
// game.View and game.ProjectEvent (reached through protocol.EventMessage).
package room

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/internal/obs"
	"github.com/kaushlendra/liars-poker/backend/internal/protocol"
	"github.com/kaushlendra/liars-poker/backend/internal/session"
)

// Options configures a room. Zero values get sensible defaults, except
// TickInterval, where zero deliberately disables the internal ticker so
// tests can drive time exactly.
type Options struct {
	Game         game.Config
	TickInterval time.Duration
	SendQueue    int
	EventRing    int
	ResultLinger time.Duration
	EmptyGrace   time.Duration
	IdleTimeout  time.Duration
	Entropy      io.Reader
	Now          func() time.Time

	// Resource protection, not gameplay. The 4-9 player rule is unchanged;
	// these bound what one hostile or broken client can cost the server.
	MaxRooms                int
	MaxConnectionsPerRoom   int
	MaxConnectionsPerPlayer int

	Log *obs.Logger
}

func (o Options) withDefaults() Options {
	if o.SendQueue == 0 {
		o.SendQueue = 64
	}
	if o.EventRing == 0 {
		o.EventRing = 256
	}
	if o.ResultLinger == 0 {
		o.ResultLinger = 60 * time.Second
	}
	if o.EmptyGrace == 0 {
		o.EmptyGrace = 30 * time.Second
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = 30 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxRooms == 0 {
		o.MaxRooms = 512
	}
	if o.MaxConnectionsPerRoom == 0 {
		// Nine players with a few tabs each, plus headroom.
		o.MaxConnectionsPerRoom = 32
	}
	if o.MaxConnectionsPerPlayer == 0 {
		o.MaxConnectionsPerPlayer = 4
	}
	if o.Log == nil {
		o.Log = obs.Discard()
	}
	return o
}

// ---------------------------------------------------------------- subscriber

// Subscriber is one connection's outbound queue. The transport reads Out and
// writes frames; it never touches room state.
type Subscriber struct {
	playerID game.PlayerID
	out      chan protocol.Outbound
	done     chan struct{}
	once     sync.Once
}

func (s *Subscriber) PlayerID() game.PlayerID       { return s.playerID }
func (s *Subscriber) Out() <-chan protocol.Outbound { return s.out }
func (s *Subscriber) Done() <-chan struct{}         { return s.done }

// close stops the subscriber. Safe to call repeatedly and from any
// goroutine.
func (s *Subscriber) close() { s.once.Do(func() { close(s.done) }) }

// ---------------------------------------------------------------- commands

type cmdKind int

const (
	cmdJoin cmdKind = iota
	cmdResume
	cmdAction
	cmdDetach
	cmdTick
	cmdSnapshot
	cmdClose
)

type command struct {
	kind     cmdKind
	name     string
	playerID game.PlayerID
	token    session.Token
	lastSeq  int
	inbound  protocol.Inbound
	sub      *Subscriber
	now      time.Time
	reason   string
	reply    chan result
}

type result struct {
	playerID game.PlayerID
	token    session.Token
	sub      *Subscriber
	view     game.PlayerView
	err      error
}

// ---------------------------------------------------------------- room

// Room is a single game and its connections.
type Room struct {
	code    string
	opts    Options
	reg     *session.Registry
	cmds    chan command
	done    chan struct{}
	once    sync.Once
	onClose func(code string)

	// Everything below is touched only by the run goroutine.
	state     game.State
	subs      map[*Subscriber]struct{}
	perPlayer map[game.PlayerID]int
	ring      []game.Event
	dead      []*Subscriber
	// lastReqID is the idempotency key per player: a repeated request id is
	// a retry of the same intent, not a new one.
	lastReqID      map[game.PlayerID]string
	createdAt      time.Time
	lastActivity   time.Time
	emptySince     time.Time
	endedAt        time.Time
	closing        bool
	shutdownReason string
}

// New starts a room and its actor goroutine.
func New(code string, opts Options, onClose func(string)) *Room {
	opts = opts.withDefaults()
	r := &Room{
		code:      code,
		opts:      opts,
		reg:       session.NewRegistry(code, opts.Entropy),
		cmds:      make(chan command, 128),
		done:      make(chan struct{}),
		onClose:   onClose,
		state:     game.NewGame(opts.Game),
		subs:      map[*Subscriber]struct{}{},
		perPlayer: map[game.PlayerID]int{},
		lastReqID: map[game.PlayerID]string{},
	}
	r.createdAt = opts.Now()
	r.lastActivity = r.createdAt
	opts.Log.RoomCreated(code)
	go r.run()
	return r
}

func (r *Room) Code() string          { return r.code }
func (r *Room) Done() <-chan struct{} { return r.done }

// IsClosed reports whether the actor has stopped.
func (r *Room) IsClosed() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// submit hands a command to the actor and waits for its reply. A closed room
// answers ErrRoomClosed rather than blocking forever.
func (r *Room) submit(c command) result {
	c.reply = make(chan result, 1)
	select {
	case r.cmds <- c:
	case <-r.done:
		return result{err: protocol.ErrRoomClosed}
	}
	select {
	case res := <-c.reply:
		return res
	case <-r.done:
		return result{err: protocol.ErrRoomClosed}
	}
}

// Join seats a new player and returns their connection, identity and rejoin
// token. The token is returned here and placed in the welcome message; it is
// never returned again.
func (r *Room) Join(name string) (*Subscriber, game.PlayerID, session.Token, error) {
	res := r.submit(command{kind: cmdJoin, name: name})
	return res.sub, res.playerID, res.token, res.err
}

// Resume reattaches an existing player after verifying their credential.
func (r *Room) Resume(id game.PlayerID, token session.Token, lastSeq int) (*Subscriber, error) {
	res := r.submit(command{kind: cmdResume, playerID: id, token: token, lastSeq: lastSeq})
	return res.sub, res.err
}

// Do applies one client intent. The actor stamps the actor id and the server
// clock onto the action, so neither can come from the frame.
func (r *Room) Do(id game.PlayerID, in protocol.Inbound) error {
	return r.submit(command{kind: cmdAction, playerID: id, inbound: in}).err
}

// Detach removes a connection. When a player has no connections left the
// room applies game.ActionDisconnect on their behalf.
//
// It waits for the actor to process the removal. Returning before that would
// leave callers unable to reason about what happens next: lifecycle timers
// key off the moment the last connection went away, so a detach still
// sitting in the queue would be timestamped later than it happened.
func (r *Room) Detach(sub *Subscriber) {
	if sub == nil {
		return
	}
	sub.close()
	r.submit(command{kind: cmdDetach, sub: sub})
}

// Tick advances time explicitly. Production uses the internal ticker; tests
// call this so timer behaviour is exact.
func (r *Room) Tick(now time.Time) {
	r.submit(command{kind: cmdTick, now: now})
}

// Close shuts the room down, telling every connection why.
func (r *Room) Close(reason string) {
	select {
	case r.cmds <- command{kind: cmdClose, reason: reason}:
	case <-r.done:
	}
}

// Snapshot returns a view for out-of-band inspection. It goes through the
// actor, so it never races the state.
func (r *Room) Snapshot(id game.PlayerID) (game.PlayerView, error) {
	res := r.submit(command{kind: cmdSnapshot, playerID: id})
	if res.err != nil {
		return game.PlayerView{}, res.err
	}
	return res.view, nil
}

// ---------------------------------------------------------------- actor loop

func (r *Room) run() {
	var tick <-chan time.Time
	if r.opts.TickInterval > 0 {
		t := time.NewTicker(r.opts.TickInterval)
		defer t.Stop()
		tick = t.C
	}
	defer r.shutdown("room stopped")

	for {
		select {
		case c := <-r.cmds:
			if c.kind == cmdClose {
				r.reply(c, result{})
				r.beginClose(c.reason)
				return
			}
			r.handle(c)
			if r.closing {
				return
			}
		case now := <-tick:
			r.onTick(now)
			if r.closing {
				return
			}
		}
	}
}

// handle dispatches one command. A panic here must not take down the process
// or any other room, so it is contained and the room closes.
func (r *Room) handle(c command) {
	defer func() {
		if rec := recover(); rec != nil {
			// The recovered value is not logged: it can be anything from
			// anywhere in the actor, which is exactly the sort of thing
			// that carries game state.
			r.opts.Log.Panicked(r.code)
			r.reply(c, result{err: protocol.ErrRoomClosed})
			r.beginClose("internal error")
		}
	}()

	switch c.kind {
	case cmdJoin:
		r.handleJoin(c)
	case cmdResume:
		r.handleResume(c)
	case cmdAction:
		r.handleAction(c)
	case cmdDetach:
		r.handleDetach(c)
	case cmdTick:
		r.handleTickCmd(c)
	case cmdSnapshot:
		r.reply(c, result{view: game.View(r.state, c.playerID)})
	}
	// Reaping here as well as in the fan-out means a subscriber dropped by
	// a handler that emitted no events cannot linger in the subscriber set.
	// A room that believes it still has listeners never becomes empty, so
	// it would never reap and its actor goroutine would live forever.
	r.reapDead()
}

func (r *Room) reply(c command, res result) {
	if c.reply == nil {
		return
	}
	select {
	case c.reply <- res:
	default:
	}
}

// ---------------------------------------------------------------- handlers

func (r *Room) handleJoin(c command) {
	r.lastActivity = r.opts.Now()

	if len(r.subs) >= r.opts.MaxConnectionsPerRoom {
		r.opts.Log.LimitRejected("connections per room")
		r.reply(c, result{err: protocol.ErrConnectionLimit})
		return
	}
	id, err := session.NewPlayerID(r.opts.Entropy)
	if err != nil {
		r.reply(c, result{err: err})
		return
	}
	name := protocol.SanitizeName(c.name)

	// The engine decides whether the join is legal. The room does not
	// pre-check the player count, the phase, or anything else.
	next, events, err := game.Apply(r.state, game.ActionJoin{
		Meta: game.Meta{PlayerID: id, Now: r.opts.Now()},
		Name: name,
	})
	if err != nil {
		r.reply(c, result{err: err})
		return
	}
	r.state = next
	r.recordEvents(events)

	token, err := r.reg.Issue(id)
	if err != nil {
		r.reply(c, result{err: err})
		return
	}

	sub := r.attach(id)
	r.sendTo(sub, protocol.ClaimSpaceMessage())
	r.sendTo(sub, protocol.WelcomeMessage(id, r.code, token.Reveal(), game.View(r.state, id)))
	r.fanOut(events)
	if seat, ok := r.seatOf(id); ok {
		r.opts.Log.PlayerJoined(r.code, string(id), seat, len(r.state.Players))
	}
	r.reply(c, result{sub: sub, playerID: id, token: token})
}

func (r *Room) handleResume(c command) {
	r.lastActivity = r.opts.Now()

	// One undifferentiated failure for unknown room, unknown player and
	// wrong token, so the reply cannot be used to enumerate anything.
	if err := r.reg.Verify(r.code, c.playerID, c.token); err != nil {
		r.opts.Log.ResumeRejected(r.code)
		r.reply(c, result{err: protocol.ErrUnauthorized})
		return
	}
	if _, ok := r.seatOf(c.playerID); !ok {
		r.opts.Log.ResumeRejected(r.code)
		r.reply(c, result{err: protocol.ErrUnauthorized})
		return
	}
	if len(r.subs) >= r.opts.MaxConnectionsPerRoom {
		r.opts.Log.LimitRejected("connections per room")
		r.reply(c, result{err: protocol.ErrConnectionLimit})
		return
	}
	if r.perPlayer[c.playerID] >= r.opts.MaxConnectionsPerPlayer {
		r.opts.Log.LimitRejected("connections per player")
		r.reply(c, result{err: protocol.ErrConnectionLimit})
		return
	}

	next, events, err := game.Apply(r.state, game.ActionReconnect{
		Meta: game.Meta{PlayerID: c.playerID, Now: r.opts.Now()},
	})
	if err == nil {
		r.state = next
		r.recordEvents(events)
	}

	sub := r.attach(c.playerID)
	r.sendTo(sub, protocol.ClaimSpaceMessage())
	// The token is not reissued: it was returned once, on join. The welcome
	// on resume carries the view only.
	r.sendTo(sub, protocol.WelcomeMessage(c.playerID, r.code, "", game.View(r.state, c.playerID)))

	// Replay what the ring still holds after the client's last seq, purely
	// so animations can catch up. The view above is the authority.
	replayed := 0
	for _, e := range r.ring {
		if e.Seq > c.lastSeq {
			r.sendTo(sub, protocol.EventMessage(e, c.playerID))
			replayed++
		}
	}
	if replayed == 0 && c.lastSeq > 0 {
		// Nothing replayable: say so explicitly with a snapshot so the
		// client knows to render without animation.
		r.sendTo(sub, protocol.SnapshotMessage(game.View(r.state, c.playerID)))
	}
	r.sendTo(sub, protocol.ViewMessage(game.View(r.state, c.playerID)))

	if len(events) > 0 {
		r.fanOutExcept(events, sub)
	}
	r.opts.Log.PlayerResumed(r.code, string(c.playerID), replayed)
	r.reply(c, result{sub: sub, playerID: c.playerID})
}

func (r *Room) handleAction(c command) {
	r.lastActivity = r.opts.Now()

	// Duplicate suppression. A repeated request id from the same player is a
	// retry of one intent, which happens when a client resends after a
	// reconnect without knowing whether the original landed. Re-delivering
	// the current view answers the retry without applying anything twice.
	//
	// This lives here, at the session layer, and deliberately not in the
	// engine: the engine's job is to decide whether an action is legal, not
	// to remember who asked for what.
	if c.inbound.ReqID != "" && r.lastReqID[c.playerID] == c.inbound.ReqID {
		r.sendViewTo(c.playerID)
		r.reply(c, result{})
		return
	}

	action, err := protocol.ActionFor(c.inbound, c.playerID, r.opts.Now())
	if err != nil {
		r.reply(c, result{err: err})
		return
	}
	next, events, err := game.Apply(r.state, action)
	if err != nil {
		// A rejected action changes nothing and emits nothing; the engine
		// guarantees that, so there is nothing to undo here.
		r.reply(c, result{err: err})
		return
	}
	r.state = next
	if c.inbound.ReqID != "" {
		r.lastReqID[c.playerID] = c.inbound.ReqID
	}
	r.recordEvents(events)
	r.fanOut(events)
	r.noteTerminal()
	r.reply(c, result{})
}

// sendViewTo re-delivers the current view to every connection a player has.
func (r *Room) sendViewTo(id game.PlayerID) {
	for sub := range r.subs {
		if sub.playerID == id {
			r.sendTo(sub, protocol.ViewMessage(game.View(r.state, id)))
		}
	}
	r.reapDead()
}

func (r *Room) handleDetach(c command) {
	if _, ok := r.subs[c.sub]; !ok {
		r.reply(c, result{})
		return
	}
	delete(r.subs, c.sub)
	id := c.sub.playerID
	r.perPlayer[id]--
	if r.perPlayer[id] <= 0 {
		delete(r.perPlayer, id)
		// Only when the player has no connections left do they count as
		// gone. The engine decides what that means for the round.
		if _, ok := r.seatOf(id); ok {
			next, events, err := game.Apply(r.state, game.ActionDisconnect{
				Meta: game.Meta{PlayerID: id, Now: r.opts.Now()},
			})
			if err == nil {
				r.state = next
				r.recordEvents(events)
				r.fanOut(events)
				r.noteTerminal()
				r.opts.Log.PlayerDisconnected(r.code, string(id), "last connection closed")
			}
		}
	}
	if len(r.subs) == 0 {
		r.emptySince = r.opts.Now()
	}
	r.reply(c, result{})
}

func (r *Room) handleTickCmd(c command) {
	r.onTick(c.now)
	r.reply(c, result{})
}

// onTick advances engine time and then evaluates room lifecycle.
//
// Round advancement happens here, through game.ActionTick, which the engine
// turns into a round advance once RoundEndDeadline passes. The room never
// constructs game.ActionAdvanceRound and no client can reach it, so one
// player cannot cut the reveal window short for everybody else.
func (r *Room) onTick(now time.Time) {
	next, events, err := game.Apply(r.state, game.ActionTick{Meta: game.Meta{Now: now}})
	if err == nil {
		r.state = next
		if len(events) > 0 {
			// A tick that produced events means the engine did real work:
			// a turn timed out, a SHOW auto-resolved, a round advanced. The
			// room is therefore not idle. Counting only client commands as
			// activity would close a room that is progressing perfectly
			// well under the timeout policies.
			r.lastActivity = now
			r.recordEvents(events)
			r.fanOut(events)
			r.noteTerminal()
		}
	}
	r.evaluateLifecycle(now)
}

// ---------------------------------------------------------------- lifecycle

func (r *Room) noteTerminal() {
	if r.state.Phase == game.GameOver && r.endedAt.IsZero() {
		r.endedAt = r.opts.Now()
		if r.state.Winner == "" {
			r.opts.Log.GameAbandoned(r.code, r.state.Round)
		} else {
			r.opts.Log.GameEnded(r.code, string(r.state.Winner), r.state.Round)
		}
	}
}

func (r *Room) evaluateLifecycle(now time.Time) {
	if r.state.Phase == game.GameOver {
		if r.endedAt.IsZero() {
			r.endedAt = now
		}
		// An abandoned game has nobody left to see the result, so there is
		// nothing to linger for.
		if r.state.Winner == "" {
			r.beginClose("game abandoned")
			return
		}
		if now.Sub(r.endedAt) >= r.opts.ResultLinger {
			r.beginClose("game over")
			return
		}
	}
	if len(r.subs) == 0 {
		if r.emptySince.IsZero() {
			r.emptySince = now
		} else if now.Sub(r.emptySince) >= r.opts.EmptyGrace {
			r.beginClose("room empty")
			return
		}
	} else {
		r.emptySince = time.Time{}
	}
	if now.Sub(r.lastActivity) >= r.opts.IdleTimeout {
		r.beginClose("room idle")
	}
}

func (r *Room) beginClose(reason string) {
	r.closing = true
	r.shutdownReason = reason
}

// shutdown runs once, as the actor goroutine exits.
func (r *Room) shutdown(fallback string) {
	reason := r.shutdownReason
	if reason == "" {
		reason = fallback
	}
	for sub := range r.subs {
		r.sendTo(sub, protocol.ClosedMessage(reason))
		sub.close()
	}
	r.subs = map[*Subscriber]struct{}{}
	r.perPlayer = map[game.PlayerID]int{}
	r.once.Do(func() { close(r.done) })
	r.opts.Log.RoomClosed(r.code, reason, r.state.Round, r.opts.Now().Sub(r.createdAt))
	if r.onClose != nil {
		r.onClose(r.code)
	}
}

// ---------------------------------------------------------------- plumbing

func (r *Room) attach(id game.PlayerID) *Subscriber {
	sub := &Subscriber{
		playerID: id,
		out:      make(chan protocol.Outbound, r.opts.SendQueue),
		done:     make(chan struct{}),
	}
	r.subs[sub] = struct{}{}
	r.perPlayer[id]++
	r.emptySince = time.Time{}
	return sub
}

func (r *Room) seatOf(id game.PlayerID) (int, bool) {
	for i := range r.state.Players {
		if r.state.Players[i].ID == id {
			return i, true
		}
	}
	return -1, false
}

func (r *Room) recordEvents(events []game.Event) {
	if len(events) == 0 {
		return
	}
	r.ring = append(r.ring, events...)
	if over := len(r.ring) - r.opts.EventRing; over > 0 {
		r.ring = append(r.ring[:0], r.ring[over:]...)
	}
}

// fanOut delivers a batch to every connection: projected events first, then
// that recipient's current view.
//
// The view is sent after every batch so a client never has to fold events
// into state. A client that ignores the event stream entirely and renders
// only views must remain fully playable; that is the test for whether any
// gameplay logic has leaked outward.
func (r *Room) fanOut(events []game.Event) { r.fanOutExcept(events, nil) }

func (r *Room) fanOutExcept(events []game.Event, skip *Subscriber) {
	for sub := range r.subs {
		if sub == skip {
			continue
		}
		for _, e := range events {
			r.sendTo(sub, protocol.EventMessage(e, sub.playerID))
		}
		r.sendTo(sub, protocol.ViewMessage(game.View(r.state, sub.playerID)))
	}
	r.reapDead()
}

// sendTo queues one message. If the queue is full the connection is closed
// rather than the message dropped: a seq gap with no close would leave a
// client silently stale, so dropping connections is acceptable and dropping
// frames is not.
func (r *Room) sendTo(sub *Subscriber, m protocol.Outbound) {
	select {
	case <-sub.done:
		r.dead = append(r.dead, sub)
	default:
		select {
		case sub.out <- m:
		default:
			r.opts.Log.BackpressureDrop(r.code, string(sub.playerID))
			sub.close()
			r.dead = append(r.dead, sub)
		}
	}
}

func (r *Room) reapDead() {
	if len(r.dead) == 0 {
		return
	}
	for _, sub := range r.dead {
		if _, ok := r.subs[sub]; !ok {
			continue
		}
		delete(r.subs, sub)
		r.perPlayer[sub.playerID]--
		if r.perPlayer[sub.playerID] <= 0 {
			delete(r.perPlayer, sub.playerID)
		}
	}
	r.dead = r.dead[:0]
	if len(r.subs) == 0 && r.emptySince.IsZero() {
		r.emptySince = r.opts.Now()
	}
}

// ---------------------------------------------------------------- manager

var ErrCodeExhausted = errors.New("room: could not allocate a unique room code")

// Manager is the room registry. Rooms live in memory; nothing in the rules
// requires persistence.
type Manager struct {
	opts Options

	mu    sync.Mutex
	rooms map[string]*Room
}

func NewManager(opts Options) *Manager {
	return &Manager{opts: opts.withDefaults(), rooms: map[string]*Room{}}
}

// Create allocates a room with a fresh code, subject to the room cap.
func (m *Manager) Create() (*Room, error) {
	m.mu.Lock()
	atCap := len(m.rooms) >= m.opts.MaxRooms
	m.mu.Unlock()
	if atCap {
		m.opts.Log.LimitRejected("rooms")
		return nil, protocol.ErrRoomLimit
	}
	for attempt := 0; attempt < 16; attempt++ {
		code, err := session.NewRoomCode(m.opts.Entropy)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		if _, taken := m.rooms[code]; taken {
			m.mu.Unlock()
			continue
		}
		room := New(code, m.opts, m.forget)
		m.rooms[code] = room
		m.mu.Unlock()
		return room, nil
	}
	return nil, ErrCodeExhausted
}

// Get looks up a live room.
func (m *Manager) Get(code string) (*Room, error) {
	m.mu.Lock()
	room, ok := m.rooms[code]
	m.mu.Unlock()
	if !ok {
		return nil, protocol.ErrRoomNotFound
	}
	if room.IsClosed() {
		return nil, protocol.ErrRoomNotFound
	}
	return room, nil
}

// forget is the room's close callback.
func (m *Manager) forget(code string) {
	m.mu.Lock()
	delete(m.rooms, code)
	m.mu.Unlock()
}

// AtCapacity reports whether the room cap has been reached, so a readiness
// probe can take an instance out of rotation before it starts refusing
// joins. Operational only: it does not change what the engine allows.
func (m *Manager) AtCapacity() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rooms) >= m.opts.MaxRooms
}

// Capacity is the configured room cap.
func (m *Manager) Capacity() int { return m.opts.MaxRooms }

// Count is the number of live rooms.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rooms)
}

// Shutdown closes every room.
func (m *Manager) Shutdown(reason string) {
	m.mu.Lock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.Unlock()
	for _, r := range rooms {
		r.Close(reason)
		<-r.Done()
	}
}

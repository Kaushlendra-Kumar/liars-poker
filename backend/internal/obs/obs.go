// Package obs is operational logging and metrics for the server.
//
// The interface is a closed set of typed events rather than a general
// logger. That is the point: there is no Info(msg string, args ...any) that
// could be handed a hidden card, a rejoin token, a private PlayerView or a
// credential digest. To log something new you add a method here, which is
// exactly the moment to think about whether its arguments are safe.
//
// Player ids and room codes are public by design — ids appear in every event
// and room codes are shareable — so they are safe to log. Nothing else about
// a player is.
package obs

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// Logger emits operational events. A nil *Logger is usable and discards
// everything, so callers never need a nil check.
type Logger struct {
	l *slog.Logger
	m *Metrics
}

// New wraps a slog handler.
func New(h slog.Handler) *Logger {
	return &Logger{l: slog.New(h), m: &Metrics{}}
}

// Discard returns a logger that records metrics but writes nothing.
func Discard() *Logger { return &Logger{m: &Metrics{}} }

func (lg *Logger) log(level slog.Level, msg string, args ...any) {
	if lg == nil || lg.l == nil {
		return
	}
	lg.l.Log(context.Background(), level, msg, args...)
}

// Metrics returns the counters, or nil for a nil logger.
func (lg *Logger) Metrics() *Metrics {
	if lg == nil {
		return nil
	}
	return lg.m
}

// ---------------------------------------------------------------- metrics

// Metrics is a set of monotonic counters, safe for concurrent use.
type Metrics struct {
	RoomsCreated     atomic.Int64
	RoomsClosed      atomic.Int64
	PlayersJoined    atomic.Int64
	Disconnects      atomic.Int64
	Resumes          atomic.Int64
	ResumesRejected  atomic.Int64
	GamesEnded       atomic.Int64
	GamesAbandoned   atomic.Int64
	ProtocolErrors   atomic.Int64
	ConnectionsOpen  atomic.Int64
	ConnectionsTotal atomic.Int64
	Panics           atomic.Int64
	LimitRejections  atomic.Int64
	Backpressure     atomic.Int64
}

// Snapshot is a point-in-time copy for reporting.
type Snapshot struct {
	RoomsCreated     int64
	RoomsClosed      int64
	PlayersJoined    int64
	Disconnects      int64
	Resumes          int64
	ResumesRejected  int64
	GamesEnded       int64
	GamesAbandoned   int64
	ProtocolErrors   int64
	ConnectionsOpen  int64
	ConnectionsTotal int64
	Panics           int64
	LimitRejections  int64
	Backpressure     int64
}

func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	return Snapshot{
		RoomsCreated:     m.RoomsCreated.Load(),
		RoomsClosed:      m.RoomsClosed.Load(),
		PlayersJoined:    m.PlayersJoined.Load(),
		Disconnects:      m.Disconnects.Load(),
		Resumes:          m.Resumes.Load(),
		ResumesRejected:  m.ResumesRejected.Load(),
		GamesEnded:       m.GamesEnded.Load(),
		GamesAbandoned:   m.GamesAbandoned.Load(),
		ProtocolErrors:   m.ProtocolErrors.Load(),
		ConnectionsOpen:  m.ConnectionsOpen.Load(),
		ConnectionsTotal: m.ConnectionsTotal.Load(),
		Panics:           m.Panics.Load(),
		LimitRejections:  m.LimitRejections.Load(),
		Backpressure:     m.Backpressure.Load(),
	}
}

// ---------------------------------------------------------------- events

func (lg *Logger) RoomCreated(code string) {
	if lg == nil {
		return
	}
	lg.m.RoomsCreated.Add(1)
	lg.log(slog.LevelInfo, "room created", "room", code)
}

func (lg *Logger) RoomClosed(code, reason string, rounds int, lifetime time.Duration) {
	if lg == nil {
		return
	}
	lg.m.RoomsClosed.Add(1)
	lg.log(slog.LevelInfo, "room closed",
		"room", code, "reason", reason, "rounds", rounds, "lifetime", lifetime.String())
}

// PlayerJoined logs the id and seat. The id is public: it appears in every
// event every other player receives.
func (lg *Logger) PlayerJoined(code, playerID string, seat, seated int) {
	if lg == nil {
		return
	}
	lg.m.PlayersJoined.Add(1)
	lg.log(slog.LevelInfo, "player joined",
		"room", code, "player", playerID, "seat", seat, "seated", seated)
}

func (lg *Logger) PlayerDisconnected(code, playerID, reason string) {
	if lg == nil {
		return
	}
	lg.m.Disconnects.Add(1)
	lg.log(slog.LevelInfo, "player disconnected",
		"room", code, "player", playerID, "reason", reason)
}

func (lg *Logger) PlayerResumed(code, playerID string, replayed int) {
	if lg == nil {
		return
	}
	lg.m.Resumes.Add(1)
	lg.log(slog.LevelInfo, "player resumed",
		"room", code, "player", playerID, "replayedEvents", replayed)
}

// ResumeRejected deliberately takes no reason. Distinguishing unknown room
// from unknown player from wrong token is exactly the enumeration oracle the
// protocol avoids being, and a log field is one grep away from becoming an
// API.
func (lg *Logger) ResumeRejected(code string) {
	if lg == nil {
		return
	}
	lg.m.ResumesRejected.Add(1)
	lg.log(slog.LevelWarn, "resume rejected", "room", code)
}

func (lg *Logger) GameEnded(code, winnerID string, rounds int) {
	if lg == nil {
		return
	}
	lg.m.GamesEnded.Add(1)
	lg.log(slog.LevelInfo, "game ended", "room", code, "winner", winnerID, "rounds", rounds)
}

func (lg *Logger) GameAbandoned(code string, rounds int) {
	if lg == nil {
		return
	}
	lg.m.GamesAbandoned.Add(1)
	lg.log(slog.LevelInfo, "game abandoned", "room", code, "rounds", rounds)
}

// ProtocolError logs only the stable wire code, never the offending payload.
func (lg *Logger) ProtocolError(code, playerID, errCode string) {
	if lg == nil {
		return
	}
	lg.m.ProtocolErrors.Add(1)
	lg.log(slog.LevelDebug, "protocol error",
		"room", code, "player", playerID, "code", errCode)
}

func (lg *Logger) ConnectionOpened(remote string) {
	if lg == nil {
		return
	}
	lg.m.ConnectionsOpen.Add(1)
	lg.m.ConnectionsTotal.Add(1)
	lg.log(slog.LevelDebug, "connection opened", "remote", remote)
}

func (lg *Logger) ConnectionClosed(remote, reason string) {
	if lg == nil {
		return
	}
	lg.m.ConnectionsOpen.Add(-1)
	lg.log(slog.LevelDebug, "connection closed", "remote", remote, "reason", reason)
}

// Panicked records a contained panic. The recovered value is not logged: it
// can be an arbitrary value from anywhere in the actor, which is precisely
// the kind of thing that carries state.
func (lg *Logger) Panicked(code string) {
	if lg == nil {
		return
	}
	lg.m.Panics.Add(1)
	lg.log(slog.LevelError, "room actor panic contained", "room", code)
}

func (lg *Logger) LimitRejected(what string) {
	if lg == nil {
		return
	}
	lg.m.LimitRejections.Add(1)
	lg.log(slog.LevelWarn, "resource limit rejected a request", "limit", what)
}

func (lg *Logger) BackpressureDrop(code, playerID string) {
	if lg == nil {
		return
	}
	lg.m.Backpressure.Add(1)
	lg.log(slog.LevelWarn, "connection dropped for backpressure",
		"room", code, "player", playerID)
}

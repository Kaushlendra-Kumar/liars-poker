// Command server runs the authoritative Liar's Poker server.
//
// It is the imperative shell and nothing more: it turns configuration into a
// room manager and a WebSocket handler, then serves. All game rules live in
// internal/game, and the transport reaches them only through game.Apply,
// game.View and game.ProjectEvent.
//
// Every setting can come from an environment variable, with a flag override,
// so the same binary runs in a container without a bespoke entrypoint.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/internal/obs"
	"github.com/kaushlendra/liars-poker/backend/internal/room"
	"github.com/kaushlendra/liars-poker/backend/internal/transport/ws"
)

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(env(key, "")); err == nil {
		return d
	}
	return fallback
}

func main() {
	var (
		addr     = flag.String("addr", env("LP_ADDR", ":8080"), "listen address")
		origins  = flag.String("origins", env("LP_ORIGINS", ""), "comma-separated allowed browser origins; \"*\" for local development only")
		seed     = flag.Int64("seed", int64(envInt("LP_SEED", 0)), "deck seed; 0 uses the wall clock")
		logLevel = flag.String("log-level", env("LP_LOG_LEVEL", "info"), "debug, info, warn or error")
		logText  = flag.Bool("log-text", env("LP_LOG_FORMAT", "json") == "text", "human-readable logs instead of JSON")

		// Gameplay timings. These are the values the rules specify; they are
		// exposed only so a test or demo can run faster, never to change a
		// rule.
		turn   = flag.Duration("turn", envDuration("LP_TURN_DURATION", 30*time.Second), "per-turn budget")
		reveal = flag.Duration("reveal", envDuration("LP_REVEAL_DURATION", 6*time.Second), "reveal window after a SHOW")

		// Resource protection. Not gameplay: the 4-9 player rule is fixed.
		maxRooms      = flag.Int("max-rooms", envInt("LP_MAX_ROOMS", 512), "rooms per instance")
		maxConnsRoom  = flag.Int("max-conns-per-room", envInt("LP_MAX_CONNS_PER_ROOM", 32), "connections per room")
		maxConnsPlyr  = flag.Int("max-conns-per-player", envInt("LP_MAX_CONNS_PER_PLAYER", 4), "connections per player")
		maxHandshakes = flag.Int("max-handshakes", envInt("LP_MAX_HANDSHAKES", 256), "concurrent websocket upgrades")
		idleTimeout   = flag.Duration("idle-timeout", envDuration("LP_IDLE_TIMEOUT", 30*time.Minute), "close a room with no activity")
		emptyGrace    = flag.Duration("empty-grace", envDuration("LP_EMPTY_GRACE", 30*time.Second), "close a room with no connections")
		resultLinger  = flag.Duration("result-linger", envDuration("LP_RESULT_LINGER", 60*time.Second), "keep a finished room so clients can read the result")

		shutdownGrace = flag.Duration("shutdown-grace", envDuration("LP_SHUTDOWN_GRACE", 15*time.Second), "time allowed for in-flight requests on shutdown")
	)
	flag.Parse()

	logger := obs.New(newHandler(*logLevel, *logText))

	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	cfg := game.DefaultConfig(*seed)
	cfg.TurnDuration = *turn
	cfg.RoundEndDuration = *reveal

	mgr := room.NewManager(room.Options{
		Game:                    cfg,
		TickInterval:            250 * time.Millisecond,
		Log:                     logger,
		MaxRooms:                *maxRooms,
		MaxConnectionsPerRoom:   *maxConnsRoom,
		MaxConnectionsPerPlayer: *maxConnsPlyr,
		IdleTimeout:             *idleTimeout,
		EmptyGrace:              *emptyGrace,
		ResultLinger:            *resultLinger,
	})

	var allowed []string
	for _, o := range strings.Split(*origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed = append(allowed, o)
		}
	}
	if len(allowed) == 1 && allowed[0] == "*" {
		slog.Warn("origin checking disabled; do not do this in production")
	}

	mux := ws.NewServeMux(ws.Options{
		Manager:                 mgr,
		Log:                     logger,
		AllowedOrigins:          allowed,
		MaxConcurrentHandshakes: *maxHandshakes,
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout or IdleTimeout: a WebSocket is a long-lived
		// hijacked connection, and either would sever live games. The
		// transport's own heartbeat and turn timer bound how long a dead
		// peer can hold a connection.
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening",
			"addr", *addr, "maxRooms", *maxRooms, "origins", len(allowed))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		slog.Error("serve failed", "err", err)
		os.Exit(1)
	case sig := <-stop:
		slog.Info("shutting down", "signal", sig.String())
	}

	// Graceful shutdown, in this order on purpose:
	//
	//  1. stop accepting new HTTP connections and let in-flight requests
	//     finish, so a join mid-handshake is not cut off;
	//  2. close every room, which tells each connected client why through
	//     the protocol's `closed` message before its socket goes away.
	//
	// Rooms are in memory, so anything still in progress ends here. That is
	// by design; see DEPLOYMENT.md on ephemeral state.
	ctx, cancel := context.WithTimeout(context.Background(), *shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("http shutdown did not finish cleanly", "err", err)
	}
	mgr.Shutdown("server shutting down")

	m := logger.Metrics().Snapshot()
	slog.Info("stopped",
		"roomsCreated", m.RoomsCreated, "roomsClosed", m.RoomsClosed,
		"gamesEnded", m.GamesEnded, "gamesAbandoned", m.GamesAbandoned,
		"connectionsTotal", m.ConnectionsTotal, "panics", m.Panics)
}

func newHandler(level string, text bool) slog.Handler {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	if text {
		return slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.NewJSONHandler(os.Stderr, opts)
}

# Deployment

Phase 6. Operational layer only: no gameplay rule and no protocol message
changed. The engine, the frozen v1 protocol and the 367-claim ladder are
exactly as they were.

What did change is operational, and all of it is listed here:

- `cmd/server` reads configuration from environment variables with flag
  overrides, so one image runs anywhere;
- **structured logging is now actually wired up.** `internal/obs` existed
  since Phase 5 but `cmd/server` never passed a logger, so every operational
  event was being discarded in production. That was a real gap;
- a `/readyz` readiness probe joins the existing `/healthz` liveness probe;
- shutdown ordering is explicit and documented.

---

## 1. Production architecture

```
                         ┌──────────────┐
      browser  ─ HTTPS ─▶│              │─ HTTP ──▶  web    (Next.js, :3000)
               ─  WSS  ─▶│  nginx :443  │
                         │  TLS + route │─ HTTP ──▶  server (Go, :8080)
                         └──────────────┘            │
                                                     └── room actors, in memory
```

Three containers. nginx terminates TLS and routes: `/ws` and `/rooms` to the
game server, everything else to the client. The game server owns all state.

**The game server is a single replica, deliberately.** A player's session is
pinned to the instance holding their room: the room code, the room actor, the
credential digest and the cards all live in that process's memory. Two
replicas behind a round-robin balancer would scatter players across rooms
that do not exist from their peer's point of view.

Scaling out therefore needs **room-aware routing**, not more replicas. The
room code is the natural shard key, and it is already in the client's join
and resume payloads, so a router could hash it. That is a real piece of work
and this document does not pretend otherwise. One instance holds 512 rooms by
default, which is several thousand concurrent players, so the single-replica
ceiling is high enough to defer the decision honestly.

The client is stateless and can scale freely; it holds no session.

## 2. Environment variables

Every value has a safe default, so an empty environment starts a working
server. `deploy/.env.example` is the template.

### Deployment-wide

| Variable | Default | Purpose |
|---|---|---|
| `PUBLIC_HOST` | — | hostname, for TLS and the proxy |
| `PUBLIC_ORIGIN` | — | e.g. `https://liars-poker.example.com`. The server's `Origin` allow-list **and** baked into the client bundle |
| `PUBLIC_WS_ORIGIN` | — | e.g. `wss://liars-poker.example.com/ws` |
| `VERSION` | `dev` | image tag, stamped into the binary, used for rollback |
| `TLS_CERT_DIR` | `./nginx/certs` | holds `fullchain.pem` and `privkey.pem` |

### Game server

| Variable | Flag | Default | Notes |
|---|---|---|---|
| `LP_ADDR` | `-addr` | `:8080` | listen address |
| `LP_ORIGINS` | `-origins` | empty | comma-separated allowed origins. Empty means same-origin only. `*` disables the check and logs a warning; development only |
| `LP_SEED` | `-seed` | `0` | `0` seeds from the clock. Set it only to reproduce a deal |
| `LP_LOG_LEVEL` | `-log-level` | `info` | `debug` logs per-connection and protocol-error events |
| `LP_LOG_FORMAT` | `-log-text` | `json` | `text` for human reading |
| `LP_TURN_DURATION` | `-turn` | `30s` | the rules' turn budget. Exposed so a demo can run fast, never to change a rule |
| `LP_REVEAL_DURATION` | `-reveal` | `6s` | reveal window after a SHOW |
| `LP_MAX_ROOMS` | `-max-rooms` | `512` | see §11 |
| `LP_MAX_CONNS_PER_ROOM` | `-max-conns-per-room` | `32` | |
| `LP_MAX_CONNS_PER_PLAYER` | `-max-conns-per-player` | `4` | one player, several tabs |
| `LP_MAX_HANDSHAKES` | `-max-handshakes` | `256` | concurrent upgrades; excess gets 503 + `Retry-After` |
| `LP_IDLE_TIMEOUT` | `-idle-timeout` | `30m` | close a room with no activity |
| `LP_EMPTY_GRACE` | `-empty-grace` | `30s` | close a room with no connections |
| `LP_RESULT_LINGER` | `-result-linger` | `60s` | keep a finished room so clients can read the result |
| `LP_SHUTDOWN_GRACE` | `-shutdown-grace` | `15s` | see §9 |

**There are no secrets.** The server holds no API key, no database password
and no signing key. The only secret in the system is the per-player rejoin
token, minted at runtime from `crypto/rand`, never written to disk and never
logged.

### Client

`NEXT_PUBLIC_SERVER_HTTP` and `NEXT_PUBLIC_SERVER_WS`. These are **inlined
into the JavaScript bundle at build time**, which has a consequence worth
being explicit about: a different environment needs a different image. The
alternative is serving the client from the same origin as the server and
using relative URLs, which the nginx config already makes possible and which
removes the coupling entirely. For a second environment, prefer that over
rebuilding.

## 3. Docker

`deploy/Dockerfile.backend` — Go build stage, then
`gcr.io/distroless/static-debian12:nonroot`. The runtime image has **no
shell, no package manager and no writable filesystem**: the binary is static,
runs as uid 65532, and the container is `read_only` with all capabilities
dropped. Rooms are in memory so nothing needs disk.

`deploy/Dockerfile.frontend` — `npm ci` against the committed lockfile, then
Next's `standalone` output, run as the `node` user.

**Both images run the test suite in the build stage.** `go vet && go test
./...` for the server; `tsc --noEmit && npm test && next build` for the
client. An image cannot be built from code that does not pass. If CI already
runs the same suites, drop those lines to save build time — but drop them
deliberately, not by accident.

```bash
cd deploy
cp .env.example .env && $EDITOR .env
docker compose build
docker compose up -d
```

## 4. WebSocket configuration

The settings that matter, and why:

- **`proxy_read_timeout` / `proxy_send_timeout` are 3600s.** A player can sit
  silent for a whole 30-second turn, and the server's heartbeat is every 15s
  with a 45s tolerance. A proxy timeout anywhere near those would sever
  healthy games. **The server decides when a player has gone, not nginx** —
  it has the turn timer, the heartbeat and the disconnect policy, and nginx
  has none of that context.
- **`proxy_buffering off`.** Buffering a live stream adds latency for nothing.
- **`Upgrade` and `Connection` via the `$connection_upgrade` map.** Resolves
  to `close` for ordinary requests, so the same server block serves both.
- **`Origin` is forwarded intact.** The server performs its own origin check
  and needs the real header; stripping it would silently disable the check.
- **No `WriteTimeout` or `IdleTimeout` on the Go `http.Server`.** A WebSocket
  is a hijacked long-lived connection and either would cut live games.
  `ReadHeaderTimeout` is set, which is the one that guards against slowloris
  on the handshake.

## 5. Reverse proxy

Needed, for three reasons: TLS termination, serving the client and the server
from one origin (which removes the CORS question entirely and lets the client
use relative URLs), and keeping the probes off the public internet.

`deploy/nginx/nginx.conf`. Probes are restricted to loopback and RFC1918
ranges. The access log records the upgrade header and request duration —
useful when a game drops — and never a request body, because the rejoin token
travels in one.

## 6. CORS and origin

There is **no CORS configuration, on purpose.** The client and server are
served from one origin, so no request is cross-origin.

What does exist is the WebSocket origin check, which CORS does not cover:
browsers do not apply the same-origin policy to WebSocket handshakes, so
without it any page on the internet could open a socket to the server on a
visitor's behalf.

- `LP_ORIGINS` empty → same-origin only, derived from the `Host` header.
- `LP_ORIGINS=https://app.example.com` → exactly that origin.
- `LP_ORIGINS=*` → check disabled, warning logged. **Development only.**
- A non-browser client sends no `Origin` and is allowed, because the header is
  a browser mechanism and cannot be a security boundary against a script.

## 7. TLS

**Required, not optional.** The rejoin token is a bearer credential that
grants access to a player's hidden cards. On plain HTTP it is readable by
anyone on the path, so the transport must be encrypted.

- TLS 1.2 and 1.3 only.
- HSTS with a one-year max-age. Port 80 exists only to redirect and to serve
  ACME challenges.
- Certificates from Let's Encrypt via certbot into `TLS_CERT_DIR`; the proxy
  mounts them read-only. Reload with `docker compose exec proxy nginx -s
  reload` after renewal — no game is interrupted, because nginx reloads
  without dropping established connections.
- TLS terminates at the proxy. The hop to the server is plain HTTP over a
  private network. On an untrusted network, terminate at the server instead.

## 8. Health and readiness

Two probes, answering different questions. Neither exposes any game state.

| Endpoint | Question | 200 | 503 |
|---|---|---|---|
| `/healthz` | is the process alive? | always, while serving | never |
| `/readyz` | should this instance get new rooms? | below the room cap | at capacity |

The distinction matters during a load spike: a full instance reports
not-ready so a balancer stops sending it joins, **while its existing rooms
keep playing normally**. A liveness probe that failed there would restart the
process and kill live games.

```
livenessProbe:  GET /healthz  period 10s  failureThreshold 3
readinessProbe: GET /readyz   period 5s   failureThreshold 2
```

Verified: with `LP_MAX_ROOMS=3`, `/readyz` returns 200 while empty and 503
once three rooms exist, and the fourth `POST /rooms` is refused with 503.

## 9. Graceful shutdown

On `SIGTERM` or `SIGINT`, in this order:

1. **`srv.Shutdown(ctx)`** — stop accepting new connections, let in-flight
   HTTP requests finish. A join mid-handshake completes rather than being cut
   off.
2. **`mgr.Shutdown(reason)`** — close every room. Each room queues its
   protocol `closed` message and drains it **before** releasing the
   subscriber, so every connected player learns why their game ended instead
   of seeing a bare socket drop. That ordering was a Phase 5 bug fix and it
   is load-bearing here.
3. Log a metrics summary: rooms created and closed, games ended and
   abandoned, connections, panics.

`LP_SHUTDOWN_GRACE` (15s) bounds step 1. Set the orchestrator's
`terminationGracePeriodSeconds` above it — 30s is comfortable.

**Games in progress are lost on shutdown**, because rooms are in memory. See
§15. Deploy during quiet periods, or accept it: a round is short and the
protocol tells players what happened.

Verified: `SIGTERM` closes every room with reason `"server shutting down"`
and logs the summary.

## 10. Logging and metrics

`internal/obs` exposes a **closed set of typed events**, not a general
logger. There is no `Info(msg, args ...any)` that could be handed a card, a
token or a private view. Adding a log line means adding a method, which is
the moment to consider whether its arguments are safe.

**Never logged, structurally:** rejoin tokens (the `Token` type redacts
through `String`, `GoString` and `MarshalJSON`), hidden cards, private
`PlayerView`s, credential digests, RNG seeds, or a recovered panic value —
that last one because it can be any value from anywhere in the actor, which
is exactly the sort of thing that carries state.

Logged: room created and closed, player joined, disconnected, resumed,
resume rejected, game ended, game abandoned, protocol error (the stable code
only, never the payload), connection opened and closed, contained panic,
limit rejection, backpressure drop.

`resume rejected` deliberately carries **no reason**, because distinguishing
unknown-room from unknown-player from wrong-token is the enumeration oracle
the protocol avoids being, and a log field is one grep away from becoming an
API.

JSON to stderr by default, for a log shipper. Fourteen counters are kept in
memory and dumped at shutdown. **There is no `/metrics` endpoint**, and the
smoke test asserts there isn't: exposing one would be a new public surface,
and the audit that says no debug endpoints are served is worth keeping true.
Adding Prometheus later means a separate listener on a private port, not a
route on the public mux.

## 11. Resource limits

Server protection. **The 4–9 player rule is in the engine and is not
configurable.**

| Limit | Default | Bounds |
|---|---|---|
| rooms per instance | 512 | memory; readiness flips at the cap |
| connections per room | 32 | nine players with a few tabs, plus headroom |
| connections per player | 4 | one player opening tabs |
| concurrent handshakes | 256 | a reconnect storm, refused cheaply pre-upgrade |
| inbound frame | 4 KiB | no legal message approaches it |
| messages per connection | 20 per 5s | then the connection closes |
| outbound queue per connection | 64 | then the connection closes; see below |
| room lifetime after a result | 60s | |
| empty room grace | 30s | |
| idle room | 30m | activity includes engine timer events |

Container limits in `docker-compose.yml`: server 2 CPU / 512 MB, client
1 CPU / 256 MB, proxy 1 CPU / 128 MB. A room is a goroutine plus a small
state value, so 512 rooms is comfortable inside 512 MB.

**Backpressure is a closed connection, never a dropped message.** A dropped
frame would leave a `seq` gap the client cannot detect; a closed connection
is unmissable, and the client reconnects and receives a full view, which the
protocol guarantees on every resume.

## 12. Deployment

```bash
git clone <repo> && cd liars-poker/deploy
cp .env.example .env && $EDITOR .env          # set PUBLIC_* and VERSION

# TLS, once
docker run --rm -p 80:80 -v "$PWD/nginx/certs:/etc/letsencrypt" \
  certbot/certbot certonly --standalone -d "$PUBLIC_HOST"

docker compose build                          # runs both test suites
docker compose up -d
docker compose ps

./smoke.sh "https://$PUBLIC_HOST"             # §14
```

Upgrading:

```bash
git fetch && git checkout v1.1.0
VERSION=v1.1.0 docker compose build
VERSION=v1.1.0 docker compose up -d server web
./smoke.sh "https://$PUBLIC_HOST"
```

`docker compose up -d server web` replaces those containers and leaves the
proxy alone, so TLS and established connections are untouched. Games on the
old server container still end when it stops — there is no way around that
while rooms are in memory, and pretending otherwise would be worse than
saying so.

## 13. Rollback

Rollback is retagging, because there is no schema and no migration. Nothing
persists, so there is nothing to undo.

```bash
VERSION=v1.0.0 docker compose up -d server web
./smoke.sh "https://$PUBLIC_HOST"
```

If an image is already built, this is a container restart — tens of seconds.
Keep the previous tag in the registry.

**What rollback costs:** every game in progress, same as any deploy. **What
it cannot break:** stored data, because there is none. This is the upside of
the ephemeral design, and it is the reason not to add a database without a
requirement that needs one.

If the proxy itself is at fault, `docker compose up -d proxy` after fixing
`nginx.conf`; validate first with
`docker compose exec proxy nginx -t`.

## 14. Smoke test

`deploy/smoke.sh <base-url>`. It exercises the paths that matter and nothing
else:

1. `/healthz` returns 200; `/readyz` returns 200 or a warned 503.
2. `/debug/pprof/`, `/metrics` and `/debug/vars` are **not** served.
3. `POST /rooms` returns a six-character code; the code resolves; an unknown
   code is 404.
4. A real four-player game over a real WebSocket: the **367-claim ladder
   arrives complete**, the game starts, and then the assertion that matters
   most — **each player sees exactly one card of their own and zero of
   anyone else's before the reveal**. Then a claim, a SHOW, and every hand
   revealed with `lastShow.witness` present as an array.

Verified against a live server:

```
  ok  healthz
  ok  readyz (ready)
  ok  no debug endpoints exposed
  ok  created room P5KERM
  ok  room lookup
  ok  367-claim ladder delivered
  ok  game started
  ok  each player sees only its own hand
  ok  SHOW resolved and every hand revealed
smoke test passed
```

Needs only `curl` and Node 20+ (for the global `WebSocket`). It skips the
socket round and still passes the HTTP checks if Node is absent.

## 15. Ephemeral state

**Everything is ephemeral. There is no persistence anywhere.**

| State | Lives in | Lost when |
|---|---|---|
| Active rooms | `room.Manager` map + one goroutine each | process exits |
| Game state | the actor's `game.State` value | process exits, or the room closes |
| Sessions | a `*Subscriber` per connection | the connection closes |
| Reconnect credentials | `session.Registry`, **digest only** | the room closes |
| Room codes | the manager's map | process exits |
| Deck and RNG | derived per round from the config seed | never stored |
| Metrics | `obs.Metrics` counters | process exits; dumped at shutdown |

Consequences, stated plainly:

- **A restart ends every game in progress.** Clients are told through the
  protocol's `closed` message rather than being dropped silently.
- **A room code is not durable.** A player who reloads their browser cannot
  rejoin, because the token was only ever in that tab's memory — a
  deliberate v1 decision, see below.
- **Credentials are not recoverable even server-side.** Only
  `sha256(room ‖ player ‖ token)` is stored, so a memory dump yields nothing
  replayable.

### No database

**None is being added, because nothing requires one.** The rules define a
game that starts, runs and ends inside one session: no accounts, no history,
no matchmaking, no leaderboard, no cross-session recovery. A database would
add an operational dependency, a backup story, a migration story and a new
failure mode, in exchange for nothing the requirements ask for.

It becomes necessary only if a requirement appears that genuinely needs it:
persistent accounts, match history, cross-instance room routing, or
reconnection surviving a server restart. Until then, the ephemeral design is
why rollback is a retag and why there is no data to leak.

### Token persistence, restated for v1

Unchanged from Phase 4 and deliberate:

- **a temporary WebSocket disconnect can resume** — the token is in memory
  and the client reconnects and resumes automatically;
- **a browser page reload loses the in-memory session credential**, so that
  player cannot rejoin their room;
- **persistent browser-session recovery is deferred** until the cookie/auth
  flow is designed on purpose.

`sessionStorage` would fix the reload case and hand any XSS a credential that
grants another player's hidden cards. That is a materially weaker model than
the one `docs/protocol.md` describes, and reload convenience is not worth it.
An `httpOnly`, `Secure`, `SameSite=Strict` cookie set by the server is the
only option that gets both, and it is a protocol and security change rather
than a frontend one.

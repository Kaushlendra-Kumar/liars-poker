# Deployment Handoff

Liar's Poker, v1 release candidate. Feature-complete and gameplay-locked.

Status of verification, stated up front:

| Area | Status |
|---|---|
| Go build, vet, gofmt, tests, race | **VERIFIED** on this machine |
| Frontend typecheck, unit tests, e2e, production build | **VERIFIED** on this machine |
| Server binary: env config, probes, SIGTERM, smoke script | **VERIFIED** against a live local server |
| Everything involving Docker | **UNVERIFIED** — Docker was unavailable; see §21 |

---

## 1. Repository structure

```
liars-poker/
├── HANDOFF.md                  this document
├── RELEASE_CHECKLIST.md        invariants that must never regress
├── DEPLOYMENT.md               full deployment reference
├── HARDENING.md                Phase 5 audit report
├── OPEN_QUESTIONS.md           rule ledger; all rules LOCKED
│
├── backend/                    Go 1.22, module github.com/kaushlendra/liars-poker/backend
│   ├── go.mod  go.sum          one dependency: github.com/gorilla/websocket
│   ├── cmd/
│   │   ├── server/main.go      the production binary
│   │   ├── dryrun/main.go      18 rule scenarios traced through the engine
│   │   ├── sim/main.go         bot simulator with statistics
│   │   └── stress/main.go      concurrent-room stress harness
│   ├── internal/
│   │   ├── game/               the engine: rules, reducer, views, projection
│   │   │   ├── state.go engine.go view.go scenario.go
│   │   │   └── engine_test.go audit_test.go
│   │   ├── protocol/           wire vocabulary, error codes, event projection
│   │   │   └── protocol.go protocol_test.go serialization_test.go
│   │   ├── room/               the authoritative actor and the room manager
│   │   ├── session/            room codes, player ids, rejoin tokens
│   │   ├── obs/                typed operational events and counters
│   │   ├── sim/                bot policies and the simulation runner
│   │   └── transport/ws/       WebSocket transport, HTTP mux, gorilla adapter
│   │       └── ws.go ws_test.go audit_test.go hardening_test.go live_test.go
│   └── pkg/poker/              cards, classifier, claim space, validator
│       └── card.go claim.go classify.go validate.go poker_test.go
│
├── frontend/                   Next.js 14 (app router), React 18, TypeScript
│   ├── package.json  package-lock.json
│   ├── next.config.mjs         output: "standalone"
│   ├── src/protocol/           types.ts client.ts labels.ts
│   ├── src/store/gameStore.ts  view is authoritative; sanitises on ingest
│   ├── src/hooks/useGame.ts    the only place the client meets React
│   ├── src/components/         10 presentational components
│   ├── src/app/                layout.tsx page.tsx globals.css
│   ├── test/                   store, ui, leak, client (54 tests)
│   ├── test/e2e/smoke.test.ts  3 tests against the real Go server
│   ├── README.md               local development
│   └── PROTOCOL_GAPS.md        4 findings, reported not worked around
│
├── deploy/
│   ├── .env.example
│   ├── Dockerfile.backend      distroless static, nonroot
│   ├── Dockerfile.frontend     Next standalone, node user
│   ├── docker-compose.yml      server + web + proxy
│   ├── nginx/nginx.conf        TLS, WebSocket upgrade, routing
│   └── smoke.sh                post-deploy smoke test
│
├── docs/                       rules, claim-model, show-validation,
│   └──                         game-state, protocol, edge-cases,
│                               test-matrix, dry-run, simulation
└── tools/verify_claim_space.py exhaustive claim-space verifier
```

## 2. Exact commands to run locally

Requires Go 1.22+ and Node 20+ (Node 22 used here). No Docker needed for this
path.

```bash
# Terminal 1 — the authoritative server
cd backend
go run ./cmd/server -origins '*'            # listens on :8080

# Terminal 2 — the client
cd frontend
npm ci
npm run dev                                 # http://localhost:3000
```

Open `http://localhost:3000`, press **Create a room**, share the six-character
code, and have four to nine players join it.

`-origins '*'` disables the WebSocket origin check and logs a warning. It is
for local development only; see §13.

Useful extras:

```bash
cd backend
go run ./cmd/dryrun                         # 18 rule scenarios, traced
go run ./cmd/sim -games 2000                # bot simulation + statistics
go run ./cmd/stress -rooms 24 -duration 10s # concurrency stress
python3 ../tools/verify_claim_space.py      # re-derive the 367-claim space
```

## 3. Docker build commands

**UNVERIFIED — must be executed on a Docker-capable machine.**

Run from the repository root; both Dockerfiles expect that context.

```bash
# Backend. Runs `go vet ./... && go test ./...` inside the build stage.
docker build -f deploy/Dockerfile.backend \
  --build-arg VERSION=v1.0.0 \
  -t liars-poker-server:v1.0.0 .

# Frontend. Runs `tsc --noEmit && npm test && next build` inside the build
# stage. The NEXT_PUBLIC_* values are inlined into the bundle, so they must
# be the real public URLs.
docker build -f deploy/Dockerfile.frontend \
  --build-arg NEXT_PUBLIC_SERVER_HTTP=https://liars-poker.example.com \
  --build-arg NEXT_PUBLIC_SERVER_WS=wss://liars-poker.example.com/ws \
  -t liars-poker-web:v1.0.0 .
```

## 4. Docker Compose startup

**UNVERIFIED.**

```bash
cd deploy
cp .env.example .env && $EDITOR .env        # §6
docker compose config                       # validate before building
docker compose build
docker compose up -d
docker compose ps
docker compose logs -f server
```

## 5. Docker Compose shutdown

```bash
cd deploy

# Graceful: SIGTERM to each container, then remove them.
docker compose down

# Keep TLS material and volumes; this stack has no volumes to lose.
docker compose stop                         # stop without removing

# Allow the server its full shutdown grace before SIGKILL.
docker compose down --timeout 40
```

`--timeout 40` matters: the server's own `LP_SHUTDOWN_GRACE` is 15s and it
then closes every room. A shorter Docker timeout would `SIGKILL` mid-shutdown
and clients would lose the `closed` message that tells them why.

## 6. Required `.env` values

`deploy/.env.example` is the template. **There are no secrets**: the server
holds no API key, database password or signing key. The only secret in the
system is the per-player rejoin token, minted at runtime and never stored.

| Variable | Required | Safe production default | Notes |
|---|---|---|---|
| `PUBLIC_HOST` | **yes** | — | `liars-poker.example.com` |
| `PUBLIC_ORIGIN` | **yes** | — | `https://liars-poker.example.com`. Must match what a browser sends, exactly |
| `PUBLIC_WS_ORIGIN` | **yes** | — | `wss://liars-poker.example.com/ws` |
| `VERSION` | **yes** | — | `v1.0.0`. Stamped into the binary; the rollback handle |
| `TLS_CERT_DIR` | yes | `./nginx/certs` | must hold `fullchain.pem` and `privkey.pem` |
| `LOG_LEVEL` | no | `info` | `debug` adds per-connection and protocol-error lines |
| `MAX_ROOMS` | no | `512` | readiness flips to 503 at this cap |
| `MAX_CONNS_PER_ROOM` | no | `32` | |
| `MAX_CONNS_PER_PLAYER` | no | `4` | one player, several tabs |
| `MAX_HANDSHAKES` | no | `256` | concurrent upgrades; excess gets 503 |
| `IDLE_TIMEOUT` | no | `30m` | |
| `SHUTDOWN_GRACE` | no | `15s` | keep Docker's stop timeout above this |

The server binary also accepts, all with working defaults, each settable as
`LP_*` or as a flag: `LP_ADDR`, `LP_ORIGINS`, `LP_SEED`, `LP_LOG_LEVEL`,
`LP_LOG_FORMAT`, `LP_TURN_DURATION`, `LP_REVEAL_DURATION`, `LP_MAX_ROOMS`,
`LP_MAX_CONNS_PER_ROOM`, `LP_MAX_CONNS_PER_PLAYER`, `LP_MAX_HANDSHAKES`,
`LP_IDLE_TIMEOUT`, `LP_EMPTY_GRACE`, `LP_RESULT_LINGER`, `LP_SHUTDOWN_GRACE`.

`LP_TURN_DURATION` and `LP_REVEAL_DURATION` are the rules' own timings,
exposed only so a demo can run fast. **Leave them at 30s and 6s in
production.**

## 7. Backend health and readiness URLs

| URL | Question | 200 | 503 |
|---|---|---|---|
| `/healthz` | is the process alive? | always, while serving | never |
| `/readyz` | should this instance take new rooms? | below the room cap | at capacity |

```
http://server:8080/healthz          inside the compose network
http://server:8080/readyz
https://PUBLIC_HOST/healthz         via the proxy, RFC1918 + loopback only
https://PUBLIC_HOST/readyz
```

The distinction matters under load: a full instance reports not-ready so a
balancer stops sending it joins, **while its existing rooms keep playing**. A
liveness probe failing there would restart the process and kill live games.

```
livenessProbe:  GET /healthz  period 10s  failureThreshold 3
readinessProbe: GET /readyz   period  5s  failureThreshold 2
```

Neither probe exposes any game state.

## 8. Frontend URL

```
https://PUBLIC_HOST/            production, via the proxy
http://localhost:3000/          local development
```

Single page. The proxy routes everything that is not `/ws`, `/rooms`,
`/healthz` or `/readyz` to the Next.js container.

## 9. WebSocket endpoint

```
wss://PUBLIC_HOST/ws            production
ws://localhost:8080/ws          direct at the server, local
```

Protocol **v1, frozen**. The client declares `"v": 1` on its opening `join`
or `resume`; omitting it is accepted, anything else is
`ERR_UNSUPPORTED_VERSION`.

Supporting HTTP endpoints on the same origin:

```
POST /rooms                     create a room -> {"roomCode":"ABCDEF"}
GET  /rooms/{code}              200 if it exists, 404 if not
```

## 10. nginx configuration assumptions

`deploy/nginx/nginx.conf` assumes:

1. **Client and server share one origin.** That is why there is no CORS
   configuration: no request is cross-origin. See §13.
2. **TLS terminates at the proxy.** The hop to the server is plain HTTP over
   the compose network. On an untrusted network, terminate at the server.
3. **Certificates are mounted read-only** at `/etc/nginx/certs` as
   `fullchain.pem` and `privkey.pem`.
4. **Upstream names are `server:8080` and `web:3000`**, which is what compose
   provides. Change them for any other orchestrator.
5. **`Origin` is forwarded intact.** The server performs its own origin
   check; stripping the header would silently disable it.
6. **Read and send timeouts are 3600s.** A player can sit silent for a whole
   30-second turn, and the server's heartbeat is every 15s with a 45s
   tolerance. **The server decides when a player has gone, not nginx** — it
   has the turn timer, the heartbeat and the disconnect policy, and nginx has
   none of that context.
7. **`proxy_buffering off`** on `/ws`. Buffering a live stream adds latency
   for nothing.
8. **Probes are restricted** to loopback and RFC1918.
9. **Port 80 exists only** to redirect and to serve ACME challenges.

Validate before reloading:

```bash
docker compose exec proxy nginx -t
docker compose exec proxy nginx -s reload      # does not drop live sockets
```

## 11. Required DNS and TLS setup

DNS: one `A` record (and `AAAA` if you have IPv6) for `PUBLIC_HOST` pointing
at the host's public address. No wildcard, no subdomains, no SRV records.

TLS is **required, not optional.** The rejoin token is a bearer credential
granting access to a player's hidden cards; on plain HTTP it is readable by
anyone on the path.

```bash
cd deploy
docker run --rm -p 80:80 -v "$PWD/nginx/certs:/etc/letsencrypt" \
  certbot/certbot certonly --standalone -d "$PUBLIC_HOST"
```

The config expects `fullchain.pem` and `privkey.pem` directly in
`TLS_CERT_DIR`; symlink them from certbot's `live/$PUBLIC_HOST/` if you keep
its layout. Renewal is a cron job plus `nginx -s reload`, which does not
interrupt a game.

TLS 1.2 and 1.3 only. HSTS is set with a one-year max-age, so be sure before
first release.

## 12. Firewall and network ports

| Port | Exposure | Purpose |
|---|---|---|
| 443/tcp | **public** | HTTPS and WSS. The only port users need |
| 80/tcp | **public** | redirect to 443, and ACME challenges |
| 8080/tcp | **private** | game server. `expose`d in compose, never published |
| 3000/tcp | **private** | Next.js. `expose`d in compose, never published |

```bash
ufw default deny incoming
ufw allow 22/tcp          # keep your own access
ufw allow 80/tcp
ufw allow 443/tcp
ufw enable
```

8080 and 3000 must not be reachable from the internet. In compose they use
`expose`, not `ports`, so they are only on the bridge network — but if you
run the binaries directly, bind them to loopback.

## 13. Production origin configuration

Two separate things, easily confused:

**CORS: none, deliberately.** The client and server are served from one
origin, so no request is cross-origin and there is no CORS config to get
wrong.

**WebSocket origin check: required, and CORS does not cover it.** Browsers do
not apply the same-origin policy to WebSocket handshakes, so without this any
page on the internet could open a socket to the server on a visitor's behalf.

| `LP_ORIGINS` | Behaviour |
|---|---|
| unset or empty | **same-origin only**, derived from the `Host` header. The safe default |
| `https://liars-poker.example.com` | exactly that origin |
| comma-separated list | each listed origin |
| `*` | **check disabled**, warning logged. Local development only |

Compose sets `LP_ORIGINS` from `PUBLIC_ORIGIN`. It must match what a browser
sends byte for byte: scheme, host and non-default port, no trailing slash.

A non-browser client sends no `Origin` and is allowed, because the header is
a browser mechanism and cannot be a security boundary against a script.

## 14. Resource limits and initial sizing

Compose limits, which are the recommended starting point:

| Service | CPU | Memory |
|---|---|---|
| `server` | 2.0 | 512 MB |
| `web` | 1.0 | 256 MB |
| `proxy` | 1.0 | 128 MB |

**Recommended initial VM: 2 vCPU, 2 GB RAM, 20 GB disk.** That is comfortable
for the stack plus the OS and image layers. Disk holds only images and logs;
the application writes nothing.

Application-level limits, all configurable, all server protection rather than
gameplay — **the 4–9 player rule is fixed in the engine**:

| Limit | Default | What it bounds |
|---|---|---|
| rooms per instance | 512 | memory; readiness flips at the cap |
| connections per room | 32 | nine players with a few tabs, plus headroom |
| connections per player | 4 | one player opening tabs |
| concurrent handshakes | 256 | a reconnect storm, refused cheaply pre-upgrade |
| inbound frame | 4 KiB | no legal message approaches it |
| messages per connection | 20 per 5s | then the connection closes |
| outbound queue per connection | 64 | then the connection closes |
| room lifetime after a result | 60s | |
| empty room grace | 30s | |
| idle room | 30m | engine timer events count as activity |

A room is one goroutine plus a small state value, so 512 rooms — several
thousand concurrent players — fits inside 512 MB. Raise `MAX_ROOMS` only
alongside the memory limit.

**Backpressure is a closed connection, never a dropped message.** A dropped
frame would leave a sequence gap the client cannot detect; a closed
connection is unmissable, and resume always delivers a full view.

## 15. Graceful shutdown behaviour

On `SIGTERM` or `SIGINT`, in this order:

1. **`srv.Shutdown(ctx)`** — stop accepting new connections; let in-flight
   HTTP requests finish, so a join mid-handshake completes.
2. **`mgr.Shutdown(reason)`** — close every room. Each room queues its
   protocol `closed` message and **drains it before releasing the
   subscriber**, so every connected player learns why their game ended
   instead of seeing a bare socket drop.
3. Log a summary: rooms created and closed, games ended and abandoned,
   connections, panics.

`LP_SHUTDOWN_GRACE` (15s) bounds step 1. Keep the orchestrator's grace above
it: `terminationGracePeriodSeconds: 30`, or `docker compose down --timeout 40`.

**Games in progress end at shutdown**, because rooms are in memory. Deploy
during quiet periods, or accept it: a round is short and the protocol tells
players what happened.

**VERIFIED locally:** `SIGTERM` closed every room with reason
`"server shutting down"` and logged the metrics summary.

## 16. Rollback procedure

Rollback is retagging. There is no schema, no migration and nothing
persisted, so there is nothing to undo.

```bash
cd deploy
VERSION=v1.0.0 docker compose up -d server web    # the previous tag
docker compose ps
./smoke.sh "https://$PUBLIC_HOST"
```

If the image is still in the registry this is a container restart — tens of
seconds. Keep the previous tag.

If the proxy is at fault, fix `nginx.conf`, then:

```bash
docker compose exec proxy nginx -t                # validate first
docker compose up -d proxy
```

**What rollback costs:** every game in progress, same as any deploy. **What
it cannot break:** stored data, because there is none.

## 17. Smoke-test procedure

```bash
cd deploy
./smoke.sh "https://$PUBLIC_HOST"       # production, through the proxy
./smoke.sh http://localhost:8080        # direct at the server
```

Needs `curl` and Node 20+ (for the global `WebSocket`). Without Node it skips
the socket round and still runs the HTTP checks.

It asserts, in order:

1. `/healthz` is 200; `/readyz` is 200 or a warned 503.
2. `/debug/pprof/`, `/metrics` and `/debug/vars` are **not** served.
3. `POST /rooms` returns a six-character code; the code resolves; an unknown
   code is 404.
4. A real four-player game over a real WebSocket: the **367-claim ladder
   arrives complete**, the game starts, and then the assertion that matters
   most — **each player sees exactly one card of their own and zero of
   anyone else's before the reveal** — then a claim, a SHOW, and every hand
   revealed with `lastShow.witness` present as an array.

**VERIFIED against a live local server:**

```
smoke test against http://127.0.0.1:18097
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

## 18. Known limitations

Each is a deliberate v1 boundary, not an oversight.

1. **Single game-server replica.** A player's session is pinned to the
   instance holding their room: the room code, the actor, the credential
   digest and the cards are all in that process's memory. Two replicas behind
   round-robin would scatter players. Scaling out needs room-aware routing
   keyed on the room code, which is already in the join and resume payloads.
2. **A browser reload ends that player's session.** The rejoin token lives in
   page memory only. A dropped socket reconnects and resumes automatically; a
   refresh has nothing to resume with. `sessionStorage` would fix it and hand
   any XSS a credential granting another player's hidden cards.
3. **A restart or deploy ends every game in progress.** Clients are told
   through the protocol's `closed` message.
4. **No persistence at all.** See §19.
5. **No `/metrics` endpoint.** Counters are in memory and dumped at
   shutdown. Adding one would contradict the audit asserting no debug
   endpoints are served, which `smoke.sh` checks.
6. **`NEXT_PUBLIC_*` is baked into the client bundle at build time**, so a
   second environment needs a second image — or serve the client from the
   same origin and use relative URLs, which the nginx config already allows.
7. **No spectators.** They would be an extra information-leak surface needing
   their own projection.
8. **Claim realizability at the current pool width is not on the wire**, so
   the builder offers every claim above the standing ordinal and says
   bluffing is legal. Computing it client-side would be a second engine.
9. **The claim ladder carries no display text**, so the frontend formats its
   own labels in one isolated file. Cosmetic drift risk only.
10. **`EventPayload.Hands` is a map, so a nil map still marshals as `null`.**
    Outside the slice fix's scope; the frontend types it nullable.
11. **Architecture is documented across `DEPLOYMENT.md` §1 and
    `docs/game-state.md` §7** rather than a separate `architecture.md`.
12. **The delivered `frontend/node_modules` may be a broken partial install**
    from the sandbox. Run `rm -rf node_modules && npm ci` first.

## 19. Explicitly NOT persisted

**Nothing is persisted. There is no database, no file storage and no volume.**

| State | Lives in | Lost when |
|---|---|---|
| Active rooms | the manager's map, one goroutine each | process exits |
| Game state | the actor's `game.State` value | process exits, or the room closes |
| Sessions | one subscriber per connection | the connection closes |
| Reconnect credentials | `session.Registry`, **digest only** | the room closes |
| Room codes | the manager's map | process exits |
| Deck and RNG | derived per round from the config seed | never stored |
| Metrics | in-memory counters | process exits; dumped at shutdown |
| Player names | the room's state | the room closes |
| Match history | **never recorded** | — |
| Accounts | **do not exist** | — |

Credentials are not recoverable even server-side: only
`sha256(room ‖ player ‖ token)` is stored, so a memory dump yields nothing
replayable.

**No database is being added**, because nothing in the rules needs one: no
accounts, no history, no matchmaking, no cross-session recovery. It becomes
necessary only if such a requirement appears. Until then, this is why
rollback is a retag and why there is no data to leak.

## 20. Exact verification commands

### Backend — VERIFIED

```bash
cd backend
gofmt -l .                      # expect no output
go vet ./...
go build ./...
go test ./...                   # 158 tests, 9 packages
go test -race ./...             # run in batches; poker ~55s, sim ~107s
```

Race in batches, because the full run exceeds a five-minute window:

```bash
go test -race ./internal/protocol/ ./internal/session/ ./internal/room/ ./internal/game/
go test -race ./internal/transport/ws/
go test -race ./pkg/poker/
go test -race ./internal/sim/
```

### Frontend — VERIFIED

```bash
cd frontend
rm -rf node_modules && npm ci
npx tsc --noEmit
npm test                        # 54 tests, 4 files
npm run e2e                     # 3 tests; builds and starts the Go server
npx next build
```

Against an already-running server:

```bash
E2E_SERVER=http://127.0.0.1:8080 npm run e2e
```

### Server binary — VERIFIED

```bash
cd backend && go build -o /tmp/liars-server ./cmd/server
LP_ADDR=127.0.0.1:8080 LP_ORIGINS='*' LP_MAX_ROOMS=3 /tmp/liars-server &

curl -i http://127.0.0.1:8080/healthz                    # 200
curl -i http://127.0.0.1:8080/readyz                     # 200
for i in 1 2 3; do curl -sX POST http://127.0.0.1:8080/rooms; done
curl -i http://127.0.0.1:8080/readyz                     # 503, at capacity
curl -i -X POST http://127.0.0.1:8080/rooms              # 503, refused
kill -TERM %1                                            # rooms closed, summary logged
```

### Deploy artifacts — UNVERIFIED

```bash
bash -n deploy/smoke.sh                 # VERIFIED: syntax clean
cd deploy && docker compose config      # UNVERIFIED
```

## 21. Docker verification checklist

**Every item below is UNVERIFIED — must be executed on a Docker-capable
machine.** Docker was not available in the environment this was built in, so
none of these has been run. Do not treat any of them as passing until it has
been.

| # | Item | Status |
|---|---|---|
| 1 | `docker build -f deploy/Dockerfile.backend` | UNVERIFIED |
| 2 | `docker build -f deploy/Dockerfile.frontend` | UNVERIFIED |
| 3 | `docker compose config` | UNVERIFIED |
| 4 | `docker compose build` | UNVERIFIED |
| 5 | `docker compose up` | UNVERIFIED |
| 6 | nginx routes to the frontend | UNVERIFIED |
| 7 | nginx routes to the backend | UNVERIFIED |
| 8 | WebSocket upgrade through nginx | UNVERIFIED |
| 9 | `/healthz` through the stack | UNVERIFIED |
| 10 | `/readyz` through the stack | UNVERIFIED |
| 11 | room creation through the stack | UNVERIFIED |
| 12 | joining multiple players | UNVERIFIED |
| 13 | 367-claim ladder delivered through nginx | UNVERIFIED |
| 14 | reduced-pool round (4 players, round 1) | UNVERIFIED |
| 15 | SHOW and reveal | UNVERIFIED |
| 16 | reconnect after a dropped socket | UNVERIFIED |
| 17 | graceful `SIGTERM` via `docker compose down` | UNVERIFIED |
| 18 | complete `./smoke.sh` through the proxy | UNVERIFIED |

Suggested order: 3 (cheapest failure) → 1, 2 → 4 → 5 → 9, 10 → 6, 7 → 18,
which covers 8, 11, 12, 13, 14 and 15 in one run → 16 by closing a browser
tab's socket and watching it resume → 17 last.

Most likely first failure: the frontend build stage, since `npm ci` requires
the committed lockfile to match `package.json` and the stage runs the test
suite inside the image. Second most likely: `PUBLIC_ORIGIN` not matching the
browser's `Origin` byte for byte, which shows up as a refused upgrade with a
403.

**If a Docker item exposes a concrete defect: fix only that defect, then
re-run `RELEASE_CHECKLIST.md` in full.** Otherwise freeze.

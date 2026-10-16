# Liar's Poker

A multiplayer bluffing card game: authoritative Go engine, Next.js client.
All rules live in Go; the client renders what the server says.

## Start here

| Document | What it is for |
|---|---|
| **`HANDOFF.md`** | **Read first.** Repo layout, every command, deployment, the open Docker checklist |
| `RELEASE_CHECKLIST.md` | Invariants that must never regress, each mapped to the test that guards it |
| `DEPLOYMENT.md` | Full deployment reference |
| `docs/rules.md` | The game rules, formally |
| `docs/protocol.md` | The frozen v1 wire protocol |
| `frontend/README.md` | Frontend development |
| `frontend/PROTOCOL_GAPS.md` | Three deferred decisions worth reading before release |
| `HARDENING.md` | Security and concurrency audit, and the five defects it found |
| `OPEN_QUESTIONS.md` | Rule ledger. Every rule is LOCKED |

## Run it locally

Needs Go 1.22+ and Node 20+.

```bash
# Frontend deps. node_modules is intentionally absent from this archive.
cd frontend && npm ci

# Terminal 1 — the authoritative server
cd backend && go run ./cmd/server -origins '*'     # :8080

# Terminal 2 — the client
cd frontend && npm run dev                          # :3000
```

Open http://localhost:3000, press **Create a room**, share the six-character
code, and join from four browser tabs.

`-origins '*'` disables the WebSocket origin check and logs a warning. Local
development only.

## Verify it

```bash
cd backend  && gofmt -l . && go vet ./... && go build ./... && go test ./...
cd frontend && npx tsc --noEmit && npm test && npm run e2e && npx next build
```

Expect 158 Go tests across 9 packages, 54 frontend unit tests, and 3 e2e
tests that build and start the real Go server.

## Status

Phases 0-6 complete and verified, with one exception: **nothing involving
Docker has been executed**, because Docker was unavailable in the build
environment. `HANDOFF.md` §21 lists the 18 items to run on a Docker-capable
machine. That is the only open gate before v1.

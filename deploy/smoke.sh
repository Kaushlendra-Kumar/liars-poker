#!/usr/bin/env bash
# Post-deploy smoke test.
#
# Exercises the paths that matter and nothing else: the probes, room
# creation, a real WebSocket upgrade, and a full four-player round through to
# a SHOW and a reveal. It asserts a player never receives another player's
# cards before the reveal.
#
# Usage:
#   ./smoke.sh https://liars-poker.example.com
#   ./smoke.sh http://localhost:8080          # direct at the server
set -euo pipefail

BASE="${1:-http://localhost:8080}"
WS="$(printf '%s' "$BASE" | sed -e 's|^http|ws|')/ws"
fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok  $*"; }

echo "smoke test against $BASE"

# 1. Probes. /healthz says the process is up; /readyz says it will take work.
code=$(curl -fsS -o /dev/null -w '%{http_code}' "$BASE/healthz") || fail "healthz unreachable"
[ "$code" = "200" ] || fail "healthz returned $code"
ok "healthz"

code=$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/readyz") || true
case "$code" in
  200) ok "readyz (ready)";;
  503) echo "  warn readyz says at capacity";;
  *)   fail "readyz returned $code";;
esac

# 2. Debug surfaces must not be served.
for path in /debug/pprof/ /metrics /debug/vars; do
  code=$(curl -sS -o /dev/null -w '%{http_code}' "$BASE$path") || true
  [ "$code" = "200" ] && fail "$path is exposed"
done
ok "no debug endpoints exposed"

# 3. Room creation.
room=$(curl -fsS -X POST "$BASE/rooms" | sed -n 's/.*"roomCode":"\([A-Z0-9]*\)".*/\1/p')
[ ${#room} -eq 6 ] || fail "room creation returned '$room'"
ok "created room $room"

code=$(curl -fsS -o /dev/null -w '%{http_code}' "$BASE/rooms/$room") || fail "room lookup failed"
[ "$code" = "200" ] || fail "room lookup returned $code"
code=$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/rooms/ZZZZZZ") || true
[ "$code" = "404" ] || fail "unknown room returned $code, want 404"
ok "room lookup"

# 4. A real game over a real socket. Node 20+ has a global WebSocket.
command -v node >/dev/null || { echo "  skip websocket round (node not installed)"; exit 0; }
WS_URL="$WS" ROOM="$room" node - <<'NODE' 
const url = process.env.WS_URL, room = process.env.ROOM;
const die = (m) => { console.error("SMOKE FAIL: " + m); process.exit(1); };

class Seat {
  constructor(name) {
    this.name = name; this.view = null; this.ladder = 0; this.token = null;
    this.ws = new WebSocket(url);
    this.ws.onmessage = (e) => {
      const m = JSON.parse(e.data);
      if (m.type === "ping") return this.send({ type: "pong" });
      if (m.type === "claim_space") this.ladder = m.payload.size;
      if (m.type === "welcome") { this.token = m.payload.token; this.view = m.payload.view; this.id = m.payload.playerId; }
      if (m.type === "view" || m.type === "snapshot") this.view = m.payload.view;
      if (m.type === "error") console.error("  server error:", m.payload.code);
    };
  }
  send(o) { if (this.ws.readyState === 1) this.ws.send(JSON.stringify(o)); }
  open() { return new Promise((r, j) => { this.ws.onopen = r; this.ws.onerror = () => j(new Error("dial failed")); }); }
  async until(pred, ms = 20000) {
    const end = Date.now() + ms;
    while (Date.now() < end) { if (this.view && pred(this.view)) return this.view; await new Promise(r => setTimeout(r, 25)); }
    die(`${this.name}: timed out (phase=${this.view && this.view.phase})`);
  }
}

(async () => {
  const seats = ["A","B","C","D"].map(n => new Seat(n));
  await Promise.all(seats.map(s => s.open()));
  for (const s of seats) s.send({ type: "join", v: 1, payload: { roomCode: room, name: s.name } });
  for (const s of seats) await s.until(v => v.players.length === 4);
  if (seats[0].ladder !== 367) die(`claim ladder is ${seats[0].ladder}, want 367`);
  console.log("  ok  367-claim ladder delivered");

  for (const s of seats) s.send({ type: "set_ready", payload: { ready: true } });
  for (const s of seats) await s.until(v => v.players.every(p => p.ready));
  seats[0].send({ type: "start_game" });
  for (const s of seats) await s.until(v => v.phase === "CLAIMING" && v.round === 1);
  console.log("  ok  game started");

  for (const s of seats) {
    const mine = s.view.players.filter(p => p.isYou).flatMap(p => p.cards || []);
    const others = s.view.players.filter(p => !p.isYou).flatMap(p => p.cards || []);
    if (mine.length !== 1) die(`${s.name} sees ${mine.length} of its own cards`);
    if (others.length !== 0) die(`${s.name} can see another player's cards before the reveal`);
    if (!Array.isArray(s.view.claims)) die("claims is not an array");
  }
  console.log("  ok  each player sees only its own hand");

  const turn = seats.find(s => s.view.yourTurn) || die("nobody has the turn");
  turn.send({ type: "make_claim", reqId: "smoke-1", payload: { claim: { category: "HIGH_CARD", primary: "3" } } });
  await turn.until(v => v.claims.length === 1);

  let shower = null;
  for (let i = 0; i < 400 && !shower; i++) { shower = seats.find(s => s.view.canShow); await new Promise(r => setTimeout(r, 25)); }
  if (!shower) die("nobody became SHOW eligible");
  shower.send({ type: "call_show", reqId: "smoke-2" });

  for (const s of seats) {
    const v = await s.until(x => x.revealed && x.lastShow);
    if (!Array.isArray(v.lastShow.witness)) die("lastShow.witness is not an array");
    if (!v.players.every(p => (p.cards || []).length > 0)) die("a hand stayed hidden after the reveal");
  }
  console.log("  ok  SHOW resolved and every hand revealed");

  for (const s of seats) s.ws.close();
  console.log("smoke test passed");
  process.exit(0);
})().catch(e => die(e.message));
NODE

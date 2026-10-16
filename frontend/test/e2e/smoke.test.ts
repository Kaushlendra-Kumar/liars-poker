import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { spawn, type ChildProcess } from "node:child_process";
import path from "node:path";
import { ProtocolClient, Token } from "@/protocol/client";
import { initialState, reduce, visibleCards, legalLadder, type GameState } from "@/store/gameStore";
import type { Claim, PlayerView } from "@/protocol/types";

/**
 * End-to-end smoke test against the real Go server.
 *
 * It drives the real ProtocolClient and the real store over a real
 * WebSocket, so the whole boundary is exercised: handshake, claim ladder,
 * views, projection, reconnect and resume.
 *
 * Deliberately no browser. The value here is the protocol boundary, and a
 * headless browser would add a large dependency without testing anything
 * the component tests do not already cover.
 *
 * Set E2E_SERVER to point at an already-running server, otherwise the suite
 * builds and starts one from ../backend.
 */

const BACKEND = path.resolve(__dirname, "../../../backend");
const PORT = Number(process.env.E2E_PORT ?? 18080);
const HTTP = process.env.E2E_SERVER ?? `http://127.0.0.1:${PORT}`;
const WS = HTTP.replace(/^http/, "ws") + "/ws";

let server: ChildProcess | null = null;

async function waitForHealth(timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${HTTP}/healthz`);
      if (res.ok) return;
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`server at ${HTTP} never became healthy`);
}

beforeAll(async () => {
  if (process.env.E2E_SERVER) {
    await waitForHealth();
    return;
  }
  server = spawn(
    "go",
    ["run", "./cmd/server", "-addr", `127.0.0.1:${PORT}`, "-origins", "*", "-turn", "3s", "-reveal", "300ms"],
    { cwd: BACKEND, stdio: "inherit" },
  );
  await waitForHealth();
}, 60_000);

afterAll(() => {
  server?.kill("SIGTERM");
});

async function createRoom(): Promise<string> {
  const res = await fetch(`${HTTP}/rooms`, { method: "POST" });
  expect(res.ok).toBe(true);
  const body = (await res.json()) as { roomCode: string };
  expect(body.roomCode).toHaveLength(6);
  return body.roomCode;
}

/** A seat: the real client plus the real store, nothing else. */
class Seat {
  state: GameState = initialState;
  client: ProtocolClient;
  credential: { roomCode: string; playerId: string; token: Token } | null = null;

  constructor(private name: string) {
    this.client = new ProtocolClient({
      url: WS,
      handlers: {
        onStatus: (status, detail) =>
          (this.state = reduce(this.state, { kind: "status", status, detail })),
        onWelcome: (payload) => {
          this.state = reduce(this.state, {
            kind: "identity",
            roomCode: payload.roomCode,
            playerId: payload.playerId,
          });
          if (payload.token) {
            this.credential = {
              roomCode: payload.roomCode,
              playerId: payload.playerId,
              token: new Token(payload.token),
            };
          }
        },
        onView: (view, source) =>
          (this.state = reduce(this.state, { kind: "view", view, source })),
        onEvent: (event) => (this.state = reduce(this.state, { kind: "event", event })),
        onClaimSpace: (payload) =>
          (this.state = reduce(this.state, { kind: "claimSpace", payload })),
        onError: (payload) => (this.state = reduce(this.state, { kind: "error", payload })),
        onClosed: (payload) => (this.state = reduce(this.state, { kind: "closed", payload })),
      },
    });
  }

  join(code: string): void {
    this.client.join(code, this.name);
  }

  get view(): PlayerView | undefined {
    return this.state.view;
  }

  async until(pred: (v: PlayerView) => boolean, ms = 15_000): Promise<PlayerView> {
    const deadline = Date.now() + ms;
    while (Date.now() < deadline) {
      const v = this.state.view;
      if (v && pred(v)) return v;
      await new Promise((r) => setTimeout(r, 20));
    }
    throw new Error(
      `${this.name}: condition never met (phase=${this.state.view?.phase} round=${this.state.view?.round})`,
    );
  }

  close(): void {
    this.client.close();
  }
}

describe("end to end against the real server", () => {
  it("plays a round from the lobby through a SHOW, seeing only its own cards", async () => {
    const code = await createRoom();
    const seats = ["Ada", "Ben", "Cy", "Dee"].map((n) => new Seat(n));
    try {
      for (const s of seats) s.join(code);
      for (const s of seats) await s.until((v) => v.players.length === 4);

      // The static ladder arrives once, in full.
      for (const s of seats) {
        expect(s.state.ladderSize).toBe(367);
        expect(s.state.ladder).toHaveLength(367);
      }

      for (const s of seats) s.client.setReady(true);
      for (const s of seats) {
        await s.until((v) => v.players.every((p) => p.ready));
      }
      seats[0]!.client.startGame();
      for (const s of seats) await s.until((v) => v.phase === "CLAIMING" && v.round === 1);

      // Four players on one card each is a reduced pool, and each seat sees
      // exactly one card: its own.
      for (const s of seats) {
        const v = s.view!;
        expect(v.evalMode).toBe("REDUCED_POOL");
        expect(v.poolSize).toBe(4);
        const mine = v.players.filter((p) => p.isYou).flatMap((p) => visibleCards(v, p));
        expect(mine).toHaveLength(1);
        const others = v.players.filter((p) => !p.isYou).flatMap((p) => visibleCards(v, p));
        expect(others).toEqual([]);
      }

      // The player the server says is on turn claims the lowest legal claim.
      const turn = seats.find((s) => s.view?.yourTurn)!;
      expect(turn).toBeDefined();
      const claim = legalLadder(turn.state)[0] as Claim;
      turn.client.makeClaim(claim);
      await turn.until((v) => v.claims.length === 1);

      // Whoever the server says may challenge, does. Note it waits on its
      // own view rather than assuming the claim has landed globally.
      const challenger = await (async () => {
        const deadline = Date.now() + 10_000;
        while (Date.now() < deadline) {
          const s = seats.find((x) => x.view?.canShow);
          if (s) return s;
          await new Promise((r) => setTimeout(r, 20));
        }
        throw new Error("nobody became SHOW eligible");
      })();
      challenger.client.callShow();

      for (const s of seats) {
        const v = await s.until((x) => x.revealed && x.lastShow !== undefined);
        // Every hand is public now, and the result is the server's.
        expect(v.players.every((p) => (visibleCards(v, p) ?? []).length > 0)).toBe(true);
        expect(typeof v.lastShow!.claimTrue).toBe("boolean");
        expect(v.lastShow!.loser).toBeTruthy();
      }

      // The round advances on the server's own timer.
      for (const s of seats) await s.until((v) => v.round === 2, 20_000);
    } finally {
      for (const s of seats) s.close();
    }
  });

  it("resumes with a valid credential and restores the private view", async () => {
    const code = await createRoom();
    const seats = ["Ada", "Ben", "Cy", "Dee"].map((n) => new Seat(n));
    try {
      for (const s of seats) s.join(code);
      for (const s of seats) await s.until((v) => v.players.length === 4);
      for (const s of seats) s.client.setReady(true);
      for (const s of seats) await s.until((v) => v.players.every((p) => p.ready));
      seats[0]!.client.startGame();
      for (const s of seats) await s.until((v) => v.phase === "CLAIMING");

      const target = seats[2]!;
      const before = visibleCards(target.view!, target.view!.players.find((p) => p.isYou)!);
      expect(before).toHaveLength(1);
      const credential = target.credential!;

      target.client.close();
      const back = new Seat("Cy");
      try {
        back.client.resume(credential);
        const v = await back.until((x) => x.round >= 1 && x.phase !== "WAITING_FOR_PLAYERS");
        const after = visibleCards(v, v.players.find((p) => p.isYou)!);
        expect(after).toEqual(before);
        const others = v.players.filter((p) => !p.isYou).flatMap((p) => visibleCards(v, p));
        if (!v.revealed) expect(others).toEqual([]);
      } finally {
        back.close();
      }
    } finally {
      for (const s of seats) s.close();
    }
  });

  it("refuses an invalid resume without saying which part was wrong", async () => {
    const code = await createRoom();
    const seat = new Seat("Ada");
    try {
      seat.join(code);
      await seat.until((v) => v.players.length === 1);
      const good = seat.credential!;

      const attempts = [
        { label: "wrong token", cred: { ...good, token: new Token("not-the-token") } },
        { label: "wrong player", cred: { ...good, playerId: "p_ghost" } },
        { label: "wrong room", cred: { ...good, roomCode: "ZZZZZZ" } },
      ];
      for (const attempt of attempts) {
        const s = new Seat("Intruder");
        try {
          s.client.resume(attempt.cred);
          const deadline = Date.now() + 8_000;
          while (Date.now() < deadline && !s.state.lastError) {
            await new Promise((r) => setTimeout(r, 20));
          }
          expect(s.state.lastError?.code, attempt.label).toBe("ERR_UNAUTHORIZED");
          // No view may be delivered on a rejected resume.
          expect(s.state.view, attempt.label).toBeUndefined();
        } finally {
          s.close();
        }
      }
    } finally {
      seat.close();
    }
  });
});

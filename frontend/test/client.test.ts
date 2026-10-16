import { describe, expect, it, vi } from "vitest";
import { ProtocolClient, Token, type SocketLike } from "@/protocol/client";
import { PROTOCOL_VERSION } from "@/protocol/types";

/** A scriptable socket, so connection behaviour is deterministic. */
class FakeSocket implements SocketLike {
  sent: string[] = [];
  onopen: ((ev: unknown) => void) | null = null;
  onclose: ((ev: unknown) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  closed = false;

  send(data: string): void {
    this.sent.push(data);
  }
  close(): void {
    this.closed = true;
  }
  open(): void {
    this.onopen?.({});
  }
  deliver(msg: unknown): void {
    this.onmessage?.({ data: JSON.stringify(msg) });
  }
  drop(): void {
    this.onclose?.({});
  }
  frames(): Array<Record<string, unknown>> {
    return this.sent.map((s) => JSON.parse(s) as Record<string, unknown>);
  }
}

function harness() {
  const sockets: FakeSocket[] = [];
  const timers: Array<() => void> = [];
  const handlers = {
    onStatus: vi.fn(),
    onWelcome: vi.fn(),
    onView: vi.fn(),
    onEvent: vi.fn(),
    onError: vi.fn(),
    onClaimSpace: vi.fn(),
    onClosed: vi.fn(),
  };
  const client = new ProtocolClient({
    url: "ws://test/ws",
    handlers,
    socketFactory: () => {
      const s = new FakeSocket();
      sockets.push(s);
      return s;
    },
    backoff: [1],
    setTimeoutFn: (fn) => {
      timers.push(fn);
      return timers.length;
    },
    clearTimeoutFn: () => {},
  });
  return { client, sockets, handlers, runTimers: () => timers.splice(0).forEach((f) => f()) };
}

const welcome = (token: string) => ({
  type: "welcome",
  seq: 3,
  payload: {
    v: PROTOCOL_VERSION,
    playerId: "p_a",
    roomCode: "ABCDEF",
    token,
    view: { seq: 3, phase: "WAITING_FOR_PLAYERS", players: [], claims: [] },
  },
});

describe("the token never leaks", () => {
  it("redacts through every stringification path", () => {
    const t = new Token("super-secret-value");
    expect(String(t)).toBe("[redacted]");
    expect(`${t}`).toBe("[redacted]");
    expect(JSON.stringify(t)).toBe('"[redacted]"');
    expect(JSON.stringify({ t })).not.toContain("super-secret");
    expect(t.reveal()).toBe("super-secret-value");
  });
});

describe("handshake and actions", () => {
  it("declares the protocol version on join", () => {
    const { client, sockets } = harness();
    client.join("abcdef", "Ada");
    sockets[0]!.open();
    const frame = sockets[0]!.frames()[0]!;
    expect(frame.type).toBe("join");
    expect(frame.v).toBe(PROTOCOL_VERSION);
    expect(frame.payload).toEqual({ roomCode: "abcdef", name: "Ada" });
  });

  it("uses a fresh request id per action, never a repeat", () => {
    const { client, sockets } = harness();
    client.join("ABCDEF", "Ada");
    sockets[0]!.open();
    sockets[0]!.deliver(welcome("tok"));
    client.setReady(true);
    client.callShow();
    client.makeClaim({ category: "PAIR", primary: "8" });
    const ids = sockets[0]!.frames().map((f) => f.reqId);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("answers a ping with a pong", () => {
    const { client, sockets } = harness();
    client.join("ABCDEF", "Ada");
    sockets[0]!.open();
    sockets[0]!.deliver({ type: "ping" });
    expect(sockets[0]!.frames().some((f) => f.type === "pong")).toBe(true);
  });

  it("ignores unparseable and unknown frames instead of crashing", () => {
    const { client, sockets, handlers } = harness();
    client.join("ABCDEF", "Ada");
    sockets[0]!.open();
    sockets[0]!.onmessage?.({ data: "not json" });
    sockets[0]!.onmessage?.({ data: 42 });
    sockets[0]!.deliver({ type: "something_new", payload: {} });
    expect(handlers.onError).not.toHaveBeenCalled();
    expect(client.status).toBe("connecting");
  });
});

describe("reconnect and resume", () => {
  it("resumes with the stored credential and last seq after a drop", () => {
    const { client, sockets, runTimers } = harness();
    client.join("ABCDEF", "Ada");
    sockets[0]!.open();
    sockets[0]!.deliver(welcome("tok-1"));
    sockets[0]!.deliver({ type: "view", seq: 9, payload: { view: { seq: 9 } } });

    sockets[0]!.drop();
    runTimers();
    expect(sockets).toHaveLength(2);
    sockets[1]!.open();

    const resume = sockets[1]!.frames()[0]!;
    expect(resume.type).toBe("resume");
    const payload = resume.payload as Record<string, unknown>;
    expect(payload.playerId).toBe("p_a");
    expect(payload.token).toBe("tok-1");
    expect(payload.lastSeq).toBe(9);
  });

  it("keeps the original credential when a resume welcome omits the token", () => {
    const { client, sockets, runTimers } = harness();
    client.join("ABCDEF", "Ada");
    sockets[0]!.open();
    sockets[0]!.deliver(welcome("tok-1"));
    sockets[0]!.drop();
    runTimers();
    sockets[1]!.open();
    sockets[1]!.deliver(welcome(""));
    sockets[1]!.drop();
    runTimers();
    sockets[2]!.open();
    const payload = sockets[2]!.frames()[0]!.payload as Record<string, unknown>;
    expect(payload.token).toBe("tok-1");
  });

  it("stops retrying on a terminal error", () => {
    const { client, sockets, handlers, runTimers } = harness();
    client.join("ABCDEF", "Ada");
    sockets[0]!.open();
    sockets[0]!.deliver({
      type: "error",
      payload: { code: "ERR_UNAUTHORIZED", message: "unauthorized" },
    });
    expect(client.status).toBe("failed");
    expect(handlers.onError).toHaveBeenCalledWith({
      code: "ERR_UNAUTHORIZED",
      message: "unauthorized",
    });
    sockets[0]!.drop();
    runTimers();
    expect(sockets).toHaveLength(1);
  });

  it("keeps retrying on a non-terminal error", () => {
    const { client, sockets, runTimers } = harness();
    client.join("ABCDEF", "Ada");
    sockets[0]!.open();
    sockets[0]!.deliver(welcome("tok"));
    sockets[0]!.deliver({
      type: "error",
      payload: { code: "ERR_NOT_YOUR_TURN", message: "not your turn" },
    });
    expect(client.status).toBe("open");
    sockets[0]!.drop();
    runTimers();
    expect(sockets).toHaveLength(2);
  });

  it("gives up after the attempt limit", () => {
    const { client, sockets, runTimers } = harness();
    client.join("ABCDEF", "Ada");
    sockets[0]!.open();
    sockets[0]!.deliver(welcome("tok"));
    for (let i = 0; i < 12; i++) {
      sockets[sockets.length - 1]!.drop();
      runTimers();
    }
    expect(client.status).toBe("failed");
  });

  it("stops for good when the room closes", () => {
    const { client, sockets, handlers, runTimers } = harness();
    client.join("ABCDEF", "Ada");
    sockets[0]!.open();
    sockets[0]!.deliver(welcome("tok"));
    sockets[0]!.deliver({ type: "closed", payload: { reason: "game over" } });
    expect(handlers.onClosed).toHaveBeenCalledWith({ reason: "game over" });
    sockets[0]!.drop();
    runTimers();
    expect(sockets).toHaveLength(1);
    expect(client.status).toBe("closed");
  });
});

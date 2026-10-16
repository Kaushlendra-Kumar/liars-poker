/**
 * The protocol client. It owns the WebSocket and is the only thing in the
 * app that touches one.
 *
 * Responsibilities: connect, hand raw frames to a parser, keep the rejoin
 * credential, reconnect and resume, answer pings. It makes no gameplay
 * decision and holds no game state beyond the last sequence number it saw,
 * which the protocol uses only to decide whether animations can be replayed.
 */

import {
  PROTOCOL_VERSION,
  TERMINAL_ERRORS,
  type ClaimSpacePayload,
  type Claim,
  type ClientMessageType,
  type ClosedPayload,
  type ErrorCode,
  type ErrorPayload,
  type GameEvent,
  type Outbound,
  type PlayerView,
  type ViewPayload,
  type WelcomePayload,
  parseOutbound,
} from "./types";

/**
 * Token wraps the rejoin credential so it cannot reach a log line, a URL or
 * a JSON payload by accident. Getting the secret out requires calling
 * reveal(), which happens in exactly one place: building a resume frame.
 *
 * This mirrors `session.Token` on the server, for the same reason.
 */
export class Token {
  #secret: string;

  constructor(secret: string) {
    this.#secret = secret;
  }

  toString(): string {
    return "[redacted]";
  }

  toJSON(): string {
    return "[redacted]";
  }

  reveal(): string {
    return this.#secret;
  }

  get present(): boolean {
    return this.#secret.length > 0;
  }
}

export interface Credentials {
  roomCode: string;
  playerId: string;
  token: Token;
}

export type ConnectionStatus =
  | "idle"
  | "connecting"
  | "open"
  | "reconnecting"
  | "closed"
  | "failed";

export type ViewSource = "welcome" | "snapshot" | "view";

export interface ClientHandlers {
  onStatus?(status: ConnectionStatus, detail?: string): void;
  onWelcome?(payload: WelcomePayload): void;
  onView?(view: PlayerView, source: ViewSource): void;
  onEvent?(event: GameEvent): void;
  onError?(payload: ErrorPayload): void;
  onClaimSpace?(payload: ClaimSpacePayload): void;
  onClosed?(payload: ClosedPayload): void;
}

/** Minimal socket shape, so tests can substitute one. */
export interface SocketLike {
  send(data: string): void;
  close(): void;
  onopen: ((ev: unknown) => void) | null;
  onclose: ((ev: unknown) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
}

export type SocketFactory = (url: string) => SocketLike;

export interface ClientOptions {
  url: string;
  handlers: ClientHandlers;
  socketFactory?: SocketFactory;
  /** Backoff schedule in ms. The last value repeats. */
  backoff?: number[];
  maxAttempts?: number;
  setTimeoutFn?: (fn: () => void, ms: number) => unknown;
  clearTimeoutFn?: (handle: unknown) => void;
}

type Intent =
  | { kind: "join"; roomCode: string; name: string }
  | { kind: "resume" };

export class ProtocolClient {
  #url: string;
  #handlers: ClientHandlers;
  #factory: SocketFactory;
  #backoff: number[];
  #maxAttempts: number;
  #setTimeout: (fn: () => void, ms: number) => unknown;
  #clearTimeout: (handle: unknown) => void;

  #socket: SocketLike | null = null;
  #credentials: Credentials | null = null;
  #intent: Intent | null = null;
  #lastSeq = 0;
  #attempt = 0;
  #retryHandle: unknown = null;
  #status: ConnectionStatus = "idle";
  #stopped = false;
  #reqCounter = 0;

  constructor(opts: ClientOptions) {
    this.#url = opts.url;
    this.#handlers = opts.handlers;
    this.#factory = opts.socketFactory ?? defaultSocketFactory;
    this.#backoff = opts.backoff ?? [500, 1000, 2000, 4000, 8000];
    this.#maxAttempts = opts.maxAttempts ?? 8;
    this.#setTimeout =
      opts.setTimeoutFn ?? ((fn, ms) => setTimeout(fn, ms) as unknown);
    this.#clearTimeout =
      opts.clearTimeoutFn ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));
  }

  get status(): ConnectionStatus {
    return this.#status;
  }

  get lastSeq(): number {
    return this.#lastSeq;
  }

  /** credentials are exposed without the secret, for display and tests. */
  get identity(): { roomCode: string; playerId: string } | null {
    if (!this.#credentials) return null;
    return {
      roomCode: this.#credentials.roomCode,
      playerId: this.#credentials.playerId,
    };
  }

  join(roomCode: string, name: string): void {
    this.#stopped = false;
    this.#intent = { kind: "join", roomCode, name };
    this.#attempt = 0;
    this.#open("connecting");
  }

  /** Resume an existing session, for example after a page-level retry. */
  resume(credentials: Credentials): void {
    this.#stopped = false;
    this.#credentials = credentials;
    this.#intent = { kind: "resume" };
    this.#attempt = 0;
    this.#open("connecting");
  }

  setReady(ready: boolean): void {
    this.#action("set_ready", { ready });
  }

  startGame(): void {
    this.#action("start_game");
  }

  makeClaim(claim: Claim): void {
    this.#action("make_claim", { claim });
  }

  callShow(): void {
    this.#action("call_show");
  }

  leave(): void {
    this.#action("leave");
  }

  /** close stops the client for good; it will not reconnect. */
  close(): void {
    this.#stopped = true;
    this.#cancelRetry();
    this.#teardown();
    this.#setStatus("closed");
  }

  // ------------------------------------------------------------ internals

  #open(status: ConnectionStatus): void {
    this.#teardown();
    this.#setStatus(status);
    let socket: SocketLike;
    try {
      socket = this.#factory(this.#socketUrl());
    } catch {
      this.#scheduleRetry("could not open a connection");
      return;
    }
    this.#socket = socket;

    socket.onopen = () => {
      if (this.#socket !== socket) return;
      this.#sendHandshake();
    };
    socket.onmessage = (ev) => {
      if (this.#socket !== socket) return;
      this.#receive(ev.data);
    };
    socket.onerror = () => {
      // onclose always follows, so the retry is scheduled there and not
      // twice.
    };
    socket.onclose = () => {
      if (this.#socket !== socket) return;
      this.#socket = null;
      if (this.#stopped) {
        this.#setStatus("closed");
        return;
      }
      this.#scheduleRetry("connection lost");
    };
  }

  // The server routes each socket to its room before the first frame
  // arrives, so the room code also travels in the URL. The handshake frame
  // remains the authority on identity.
  #socketUrl(): string {
    const code =
      this.#intent?.kind === "join" ? this.#intent.roomCode : this.#credentials?.roomCode;
    if (!code) return this.#url;
    const sep = this.#url.includes("?") ? "&" : "?";
    return `${this.#url}${sep}room=${encodeURIComponent(code.trim().toUpperCase())}`;
  }

  #sendHandshake(): void {
    if (!this.#intent) return;
    if (this.#intent.kind === "join") {
      this.#send({
        type: "join",
        v: PROTOCOL_VERSION,
        reqId: this.#nextReqId(),
        payload: { roomCode: this.#intent.roomCode, name: this.#intent.name },
      });
      return;
    }
    const creds = this.#credentials;
    if (!creds) {
      this.#setStatus("failed", "no credential to resume with");
      return;
    }
    this.#send({
      type: "resume",
      v: PROTOCOL_VERSION,
      reqId: this.#nextReqId(),
      payload: {
        roomCode: creds.roomCode,
        playerId: creds.playerId,
        // The only place the secret is read.
        token: creds.token.reveal(),
        lastSeq: this.#lastSeq,
      },
    });
  }

  #receive(data: unknown): void {
    const msg = parseOutbound(data);
    if (!msg) return; // unparseable or unknown type: ignore, never crash
    if (typeof msg.seq === "number" && msg.seq > this.#lastSeq) {
      this.#lastSeq = msg.seq;
    }

    switch (msg.type) {
      case "ping":
        this.#send({ type: "pong" });
        return;
      case "welcome": {
        const payload = msg.payload as WelcomePayload | undefined;
        if (!payload?.view) return;
        // A welcome carries a token only on join. On resume it is empty and
        // the credential we already hold stays in force.
        if (payload.token) {
          this.#credentials = {
            roomCode: payload.roomCode,
            playerId: payload.playerId,
            token: new Token(payload.token),
          };
          // Once a credential exists, every later reconnect resumes.
          this.#intent = { kind: "resume" };
        }
        this.#attempt = 0;
        this.#setStatus("open");
        this.#handlers.onWelcome?.(payload);
        this.#handlers.onView?.(payload.view, "welcome");
        return;
      }
      case "snapshot": {
        const payload = msg.payload as ViewPayload | undefined;
        if (payload?.view) this.#handlers.onView?.(payload.view, "snapshot");
        return;
      }
      case "view": {
        const payload = msg.payload as ViewPayload | undefined;
        if (payload?.view) this.#handlers.onView?.(payload.view, "view");
        return;
      }
      case "event": {
        const payload = msg.payload as GameEvent | undefined;
        if (payload) this.#handlers.onEvent?.(payload);
        return;
      }
      case "claim_space": {
        const payload = msg.payload as ClaimSpacePayload | undefined;
        if (payload) this.#handlers.onClaimSpace?.(payload);
        return;
      }
      case "error": {
        const payload = msg.payload as ErrorPayload | undefined;
        if (!payload) return;
        this.#handlers.onError?.(payload);
        if (TERMINAL_ERRORS.has(payload.code as ErrorCode)) {
          // Retrying with the same credential cannot help.
          this.#stopped = true;
          this.#cancelRetry();
          this.#setStatus("failed", payload.code);
        }
        return;
      }
      case "closed": {
        const payload = msg.payload as ClosedPayload | undefined;
        this.#stopped = true;
        this.#cancelRetry();
        this.#handlers.onClosed?.(payload ?? { reason: "closed" });
        this.#setStatus("closed", payload?.reason);
        return;
      }
    }
  }

  #action(type: ClientMessageType, payload?: unknown): void {
    this.#send({
      type,
      reqId: this.#nextReqId(),
      ...(payload === undefined ? {} : { payload }),
    });
  }

  #send(frame: { type: ClientMessageType; [k: string]: unknown }): void {
    const socket = this.#socket;
    if (!socket) return;
    try {
      socket.send(JSON.stringify(frame));
    } catch {
      // The close handler will schedule a retry.
    }
  }

  /**
   * A fresh request id per action. The server treats a repeated id as a
   * retry of the same intent and does not apply it twice, so ids must never
   * be reused for a different action.
   */
  #nextReqId(): string {
    this.#reqCounter += 1;
    return `c${this.#reqCounter}`;
  }

  #scheduleRetry(detail: string): void {
    if (this.#stopped) return;
    if (!this.#credentials && this.#intent?.kind === "resume") {
      this.#setStatus("failed", "no credential to resume with");
      return;
    }
    if (this.#attempt >= this.#maxAttempts) {
      this.#setStatus("failed", "could not reconnect");
      return;
    }
    const delay =
      this.#backoff[Math.min(this.#attempt, this.#backoff.length - 1)] ?? 1000;
    this.#attempt += 1;
    this.#setStatus("reconnecting", detail);
    this.#cancelRetry();
    this.#retryHandle = this.#setTimeout(() => {
      this.#retryHandle = null;
      if (this.#stopped) return;
      this.#open("reconnecting");
    }, delay);
  }

  #cancelRetry(): void {
    if (this.#retryHandle !== null) {
      this.#clearTimeout(this.#retryHandle);
      this.#retryHandle = null;
    }
  }

  #teardown(): void {
    const socket = this.#socket;
    this.#socket = null;
    if (!socket) return;
    socket.onopen = null;
    socket.onmessage = null;
    socket.onerror = null;
    socket.onclose = null;
    try {
      socket.close();
    } catch {
      // already gone
    }
  }

  #setStatus(status: ConnectionStatus, detail?: string): void {
    this.#status = status;
    this.#handlers.onStatus?.(status, detail);
  }
}

function defaultSocketFactory(url: string): SocketLike {
  return new WebSocket(url) as unknown as SocketLike;
}

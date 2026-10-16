"use client";

import { useCallback, useEffect, useMemo, useReducer, useRef } from "react";
import { ProtocolClient, type Credentials } from "@/protocol/client";
import type { Claim } from "@/protocol/types";
import { initialState, reduce, type GameState } from "@/store/gameStore";

/**
 * Wires the protocol client to the store. This is the only place a
 * WebSocket-shaped thing meets React.
 *
 * The rejoin token lives in this hook's memory and nowhere else. It is never
 * written to localStorage, sessionStorage, a cookie, a URL or a log. That
 * costs a page reload, which drops the session, and `docs/protocol.md`
 * documents no browser persistence model, so inventing one would be
 * inventing a weaker transport. See PROTOCOL_GAPS.md.
 */
export interface GameApi {
  state: GameState;
  join(roomCode: string, name: string): void;
  setReady(ready: boolean): void;
  startGame(): void;
  makeClaim(claim: Claim): void;
  callShow(): void;
  leave(): void;
}

export function useGame(wsUrl: string): GameApi {
  const [state, dispatch] = useReducer(reduce, initialState);
  const clientRef = useRef<ProtocolClient | null>(null);

  const client = useMemo(() => {
    const c = new ProtocolClient({
      url: wsUrl,
      handlers: {
        onStatus: (status, detail) => dispatch({ kind: "status", status, detail }),
        onWelcome: (payload) =>
          dispatch({
            kind: "identity",
            roomCode: payload.roomCode,
            playerId: payload.playerId,
          }),
        onView: (view, source) => dispatch({ kind: "view", view, source }),
        onEvent: (event) => dispatch({ kind: "event", event }),
        onClaimSpace: (payload) => dispatch({ kind: "claimSpace", payload }),
        onError: (payload) => dispatch({ kind: "error", payload }),
        onClosed: (payload) => dispatch({ kind: "closed", payload }),
      },
    });
    clientRef.current = c;
    return c;
  }, [wsUrl]);

  useEffect(() => () => client.close(), [client]);

  return {
    state,
    join: useCallback((code, name) => client.join(code.trim().toUpperCase(), name), [client]),
    setReady: useCallback((ready: boolean) => client.setReady(ready), [client]),
    startGame: useCallback(() => client.startGame(), [client]),
    makeClaim: useCallback((claim: Claim) => client.makeClaim(claim), [client]),
    callShow: useCallback(() => client.callShow(), [client]),
    leave: useCallback(() => client.leave(), [client]),
  };
}

/** Exposed for tests that need to drive a client directly. */
export type { Credentials };

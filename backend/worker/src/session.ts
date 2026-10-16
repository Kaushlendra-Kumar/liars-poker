// Identity: room codes, player ids, and the rejoin token that proves a
// reconnecting client is who it says it is. Port of backend/internal/session.
//
// Only a SHA-256 digest bound to (room, player, token) is stored, so the
// room's storage holds nothing replayable.

// Omits I, O, 0 and 1 so a code read aloud is unambiguous. 32 characters
// divide 256 exactly, so byte % 32 has no modulo bias.
const roomCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
export const RoomCodeLength = 6;
const playerIdBytes = 9;
const tokenBytes = 32;

const randomBytes = (n: number): Uint8Array => crypto.getRandomValues(new Uint8Array(n));

function base64url(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function newRoomCode(): string {
  return Array.from(randomBytes(RoomCodeLength), (b) => roomCodeAlphabet[b % roomCodeAlphabet.length]).join("");
}

export const isRoomCode = (s: string): boolean => new RegExp(`^[${roomCodeAlphabet}]{${RoomCodeLength}}$`).test(s);

/** The id is public: knowing it must not let anyone act as that player. */
export const newPlayerId = (): string => "p_" + base64url(randomBytes(playerIdBytes));

export const newToken = (): string => base64url(randomBytes(tokenBytes));

export async function bindDigest(roomCode: string, playerId: string, token: string): Promise<string> {
  const data = new TextEncoder().encode(`${roomCode}\u0000${playerId}\u0000${token}`);
  return base64url(new Uint8Array(await crypto.subtle.digest("SHA-256", data)));
}

// constantTimeEqual compares without short-circuiting on the first
// difference, so timing does not reveal how much of a digest matched.
export function constantTimeEqual(a: string, b: string): boolean {
  const len = Math.max(a.length, b.length);
  let diff = a.length ^ b.length;
  for (let i = 0; i < len; i++) diff |= (a.charCodeAt(i) || 0) ^ (b.charCodeAt(i) || 0);
  return diff === 0;
}

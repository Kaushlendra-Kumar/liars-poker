/**
 * Presentation only. Nothing here decides anything about the game.
 *
 * Two jobs: turn a server claim into a human label, and turn a server card
 * code into a rank and a suit for display. Neither compares claims, orders
 * them, or judges whether one is legal or true. Claim ordering comes from
 * server-assigned ordinals and legality comes from the view.
 *
 * The claim history is rendered from the server's own `text` field; these
 * labels exist because the static claim ladder is sent as structured claims
 * without text, so the builder needs to render its options.
 */

import type { Claim, ClaimCategory, SuitName } from "./types";

const RANK_WORD: Record<string, string> = {
  "2": "Two",
  "3": "Three",
  "4": "Four",
  "5": "Five",
  "6": "Six",
  "7": "Seven",
  "8": "Eight",
  "9": "Nine",
  "10": "Ten",
  J: "Jack",
  Q: "Queen",
  K: "King",
  A: "Ace",
};

const RANK_PLURAL: Record<string, string> = {
  "2": "2s",
  "3": "3s",
  "4": "4s",
  "5": "5s",
  "6": "6s",
  "7": "7s",
  "8": "8s",
  "9": "9s",
  "10": "10s",
  J: "Jacks",
  Q: "Queens",
  K: "Kings",
  A: "Aces",
};

export const SUIT_TITLE: Record<SuitName, string> = {
  CLUBS: "Clubs",
  DIAMONDS: "Diamonds",
  HEARTS: "Hearts",
  SPADES: "Spades",
};

export const SUIT_SYMBOL: Record<SuitName, string> = {
  CLUBS: "\u2663",
  DIAMONDS: "\u2666",
  HEARTS: "\u2665",
  SPADES: "\u2660",
};

export const CATEGORY_TITLE: Record<ClaimCategory, string> = {
  HIGH_CARD: "High Card",
  PAIR: "Pair",
  TWO_PAIR: "Two Pair",
  THREE_OF_A_KIND: "Three of a Kind",
  STRAIGHT: "Straight",
  FLUSH: "Flush",
  FULL_HOUSE: "Full House",
  FOUR_OF_A_KIND: "Four of a Kind",
  STRAIGHT_FLUSH: "Straight Flush",
  ROYAL_FLUSH: "Royal Flush",
};

function word(rank: string | undefined): string {
  if (!rank) return "?";
  return RANK_WORD[rank] ?? rank;
}

function plural(rank: string | undefined): string {
  if (!rank) return "?s";
  return RANK_PLURAL[rank] ?? `${rank}s`;
}

/** claimLabel renders a claim the way a player says it. */
export function claimLabel(claim: Claim): string {
  switch (claim.category) {
    case "HIGH_CARD":
      return `${word(claim.primary)} high`;
    case "PAIR":
      return `Pair of ${plural(claim.primary)}`;
    case "TWO_PAIR":
      return `Two Pair \u2014 ${plural(claim.primary)} and ${plural(claim.secondary)}`;
    case "THREE_OF_A_KIND":
      return `Three ${plural(claim.primary)}`;
    case "STRAIGHT":
      return `Straight to ${claim.primary ?? "?"}`;
    case "FLUSH":
      return `${word(claim.primary)}-high ${claim.suit ? SUIT_TITLE[claim.suit] : "?"} Flush`;
    case "FULL_HOUSE":
      return `${plural(claim.primary)} full of ${plural(claim.secondary)}`;
    case "FOUR_OF_A_KIND":
      return `Four ${plural(claim.primary)}`;
    case "STRAIGHT_FLUSH":
      return `Straight Flush to ${word(claim.primary)} of ${
        claim.suit ? SUIT_TITLE[claim.suit] : "?"
      }`;
    case "ROYAL_FLUSH":
      return `Royal Flush of ${claim.suit ? SUIT_TITLE[claim.suit] : "?"}`;
  }
}

export interface CardParts {
  rank: string;
  suit: SuitName | null;
  symbol: string;
  /** Screen-reader text, e.g. "Eight of Spades". */
  label: string;
  red: boolean;
}

const SUIT_BY_LETTER: Record<string, SuitName> = {
  C: "CLUBS",
  D: "DIAMONDS",
  H: "HEARTS",
  S: "SPADES",
};

/**
 * cardParts splits a server card code for rendering. An unrecognised code is
 * rendered as-is rather than throwing: the server is the authority on what a
 * card is, and a display helper should never be able to break the UI.
 */
export function cardParts(code: string): CardParts {
  const letter = code.slice(-1).toUpperCase();
  const rank = code.slice(0, -1);
  const suit = SUIT_BY_LETTER[letter] ?? null;
  if (!suit || !rank) {
    return { rank: code, suit: null, symbol: "", label: code, red: false };
  }
  return {
    rank,
    suit,
    symbol: SUIT_SYMBOL[suit],
    label: `${word(rank)} of ${SUIT_TITLE[suit]}`,
    red: suit === "DIAMONDS" || suit === "HEARTS",
  };
}

// Card model, the 1-to-5 card hand classifier, the claim model with its
// total order, and the exact claim validator. Port of backend/pkg/poker.
//
// The module is pure: no I/O, no clock, no global randomness. Randomness
// enters through the RNG type so shuffles are deterministic in tests.

// ---------------------------------------------------------------- suits

// Suit declaration order doubles as the least-significant claim tiebreak.
export const Clubs = 0;
export const Diamonds = 1;
export const Hearts = 2;
export const Spades = 3;
export type Suit = number;

const suitSymbol = ["♣", "♦", "♥", "♠"];
const suitName = ["CLUBS", "DIAMONDS", "HEARTS", "SPADES"];
const suitTitle = ["Clubs", "Diamonds", "Hearts", "Spades"];
const suitLetter = ["C", "D", "H", "S"];

export const suitValid = (s: Suit): boolean => Number.isInteger(s) && s >= Clubs && s <= Spades;
export const suitNameOf = (s: Suit): string => (suitValid(s) ? suitName[s]! : "INVALID");
export const suitTitleOf = (s: Suit): string => (suitValid(s) ? suitTitle[s]! : "Invalid");

export function parseSuit(raw: string): Suit {
  switch (raw.trim().toUpperCase()) {
    case "C":
    case "CLUBS":
    case "♣":
      return Clubs;
    case "D":
    case "DIAMONDS":
    case "♦":
      return Diamonds;
    case "H":
    case "HEARTS":
    case "♥":
      return Hearts;
    case "S":
    case "SPADES":
    case "♠":
      return Spades;
  }
  throw new Error(`poker: unknown suit ${JSON.stringify(raw)}`);
}

// ---------------------------------------------------------------- ranks

// The numeric value is the poker value, so Ace is 14. Ace additionally acts
// as 1 for straight detection only; see straightHigh.
export const Two = 2;
export const Three = 3;
export const Four = 4;
export const Five = 5;
export const Six = 6;
export const Seven = 7;
export const Eight = 8;
export const Nine = 9;
export const Ten = 10;
export const Jack = 11;
export const Queen = 12;
export const King = 13;
export const Ace = 14;
export const MinRank = Two;
export const MaxRank = Ace;
export type Rank = number;

const rankShort: Record<number, string> = {
  2: "2", 3: "3", 4: "4", 5: "5", 6: "6", 7: "7", 8: "8", 9: "9", 10: "10",
  11: "J", 12: "Q", 13: "K", 14: "A",
};
const rankWord: Record<number, string> = {
  2: "Two", 3: "Three", 4: "Four", 5: "Five", 6: "Six", 7: "Seven", 8: "Eight",
  9: "Nine", 10: "Ten", 11: "Jack", 12: "Queen", 13: "King", 14: "Ace",
};
const rankPlural: Record<number, string> = {
  2: "2s", 3: "3s", 4: "4s", 5: "5s", 6: "6s", 7: "7s", 8: "8s", 9: "9s",
  10: "10s", 11: "Jacks", 12: "Queens", 13: "Kings", 14: "Aces",
};

export const rankValid = (r: Rank): boolean => Number.isInteger(r) && r >= MinRank && r <= MaxRank;
export const rankString = (r: Rank): string => rankShort[r] ?? "?";
export const rankWordOf = (r: Rank): string => rankWord[r] ?? "?";
export const rankPluralOf = (r: Rank): string => rankPlural[r] ?? "?s";

export function parseRank(raw: string): Rank {
  const s = raw.trim().toUpperCase();
  switch (s) {
    case "10":
    case "T":
      return Ten;
    case "J":
    case "JACK":
      return Jack;
    case "Q":
    case "QUEEN":
      return Queen;
    case "K":
    case "KING":
      return King;
    case "A":
    case "ACE":
      return Ace;
  }
  if (/^[2-9]$/.test(s)) return Number(s);
  throw new Error(`poker: unknown rank ${JSON.stringify(raw)}`);
}

// ---------------------------------------------------------------- cards

export interface Card {
  rank: Rank;
  suit: Suit;
}

export const NumSuits = 4;

// cardKey is the canonical sort key and identity: rank ascending, then suit.
// It is also how a card is used as a map key.
export const cardKey = (c: Card): number => c.rank * NumSuits + c.suit;
export const cardFromKey = (k: number): Card => ({ rank: Math.floor(k / NumSuits), suit: k % NumSuits });
export const cardValid = (c: Card): boolean => rankValid(c.rank) && suitValid(c.suit);
export const sameCard = (a: Card, b: Card): boolean => a.rank === b.rank && a.suit === b.suit;
export const cardString = (c: Card): string => rankString(c.rank) + (suitSymbol[c.suit] ?? "?");

// cardCode is the transport-safe form: "8S", "10H", "AD".
export const cardCode = (c: Card): string => (cardValid(c) ? rankString(c.rank) + suitLetter[c.suit] : "??");

// parseCard accepts forms like "8S", "10h", "Ad", "K♠".
export function parseCard(raw: string): Card {
  const s = raw.trim();
  const chars = Array.from(s);
  if (chars.length < 2) throw new Error(`poker: bad card ${JSON.stringify(raw)}`);
  const suit = parseSuit(chars[chars.length - 1]!);
  const rank = parseRank(chars.slice(0, -1).join(""));
  return { rank, suit };
}

export function parseCards(s: string): Card[] {
  return s.split(/\s+/).filter(Boolean).map(parseCard);
}

export function sortCards(cs: Card[]): void {
  cs.sort((a, b) => cardKey(a) - cardKey(b));
}

export function sortedCopy(cs: readonly Card[]): Card[] {
  const out = cs.map((c) => ({ ...c }));
  sortCards(out);
  return out;
}

export function cardsString(cs: readonly Card[]): string {
  return sortedCopy(cs).map(cardString).join(" ");
}

export function hasDuplicates(cs: readonly Card[]): boolean {
  const seen = new Set<number>();
  for (const c of cs) {
    const k = cardKey(c);
    if (seen.has(k)) return true;
    seen.add(k);
  }
  return false;
}

// ---------------------------------------------------------------- deck

export const DeckSize = 52;

/** RNG returns an integer in [0, n). */
export interface RNG {
  intn(n: number): number;
}

// seededRng is a deterministic 32-bit PRNG (mulberry32). Seeds are folded to
// 32 bits, which is plenty for shuffling a deck.
export function seededRng(seed: number): RNG {
  let a = (Math.trunc(seed) ^ Math.trunc(seed / 0x100000000)) >>> 0;
  const next = (): number => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  return { intn: (n) => Math.floor(next() * n) };
}

export function newDeck(): Card[] {
  const out: Card[] = [];
  for (let r = MinRank; r <= MaxRank; r++) {
    for (let s = Clubs; s <= Spades; s++) out.push({ rank: r, suit: s });
  }
  return out;
}

export class Deck {
  #cards: Card[] = newDeck();
  #next = 0;

  constructor(rng: RNG) {
    this.shuffle(rng);
  }

  // shuffle resets to a full deck and applies an unbiased Fisher-Yates.
  shuffle(rng: RNG): void {
    this.#cards = newDeck();
    this.#next = 0;
    for (let i = this.#cards.length - 1; i > 0; i--) {
      const j = rng.intn(i + 1);
      [this.#cards[i], this.#cards[j]] = [this.#cards[j]!, this.#cards[i]!];
    }
  }

  deal(n: number): Card[] {
    if (n < 0) throw new Error(`poker: negative deal ${n}`);
    if (this.remaining() < n) throw new Error("poker: deck exhausted");
    const out = this.#cards.slice(this.#next, this.#next + n).map((c) => ({ ...c }));
    this.#next += n;
    return out;
  }

  remaining(): number {
    return this.#cards.length - this.#next;
  }
}

// ---------------------------------------------------------------- categories

// Values ascend with strength. Royal Flush is separate from Straight Flush.
export const HighCard = 0;
export const OnePair = 1;
export const TwoPair = 2;
export const ThreeOfAKind = 3;
export const Straight = 4;
export const Flush = 5;
export const FullHouse = 6;
export const FourOfAKind = 7;
export const StraightFlush = 8;
export const RoyalFlush = 9;
export type Category = number;
const NumCategories = 10;

const categoryName = [
  "HIGH_CARD", "PAIR", "TWO_PAIR", "THREE_OF_A_KIND", "STRAIGHT",
  "FLUSH", "FULL_HOUSE", "FOUR_OF_A_KIND", "STRAIGHT_FLUSH", "ROYAL_FLUSH",
];

export const categoryValid = (c: Category): boolean => Number.isInteger(c) && c >= 0 && c < NumCategories;
export const categoryString = (c: Category): string => (categoryValid(c) ? categoryName[c]! : "INVALID");

export function parseCategory(s: string): Category {
  const i = categoryName.indexOf(s);
  if (i < 0) throw new Error(`poker: unknown category ${JSON.stringify(s)}`);
  return i;
}

// ---------------------------------------------------------------- claims

// Claim has the same shape Classify returns, so claim satisfaction is a
// structural equality. Only fields a category declares are populated; all
// others are zero.
//
//	Category        primary          secondary   suit
//	HighCard        high card        -           -
//	OnePair         pair rank        -           -
//	TwoPair         high pair rank   low pair    -
//	ThreeOfAKind    trips rank       -           -
//	Straight        straight high    -           -
//	Flush           flush high       -           suit
//	FullHouse       trips rank       pair rank   -
//	FourOfAKind     quads rank       -           -
//	StraightFlush   straight high    -           suit
//	RoyalFlush      -                -           suit
export interface Claim {
  category: Category;
  primary: Rank;
  secondary: Rank;
  suit: Suit;
  hasSuit: boolean;
}

const mk = (category: Category, primary = 0, secondary = 0, suit?: Suit): Claim => ({
  category,
  primary,
  secondary,
  suit: suit ?? 0,
  hasSuit: suit !== undefined,
});

export const newHighCard = (h: Rank): Claim => mk(HighCard, h);
export const newPair = (r: Rank): Claim => mk(OnePair, r);
export const newTrips = (r: Rank): Claim => mk(ThreeOfAKind, r);
export const newStraight = (h: Rank): Claim => mk(Straight, h);
export const newQuads = (r: Rank): Claim => mk(FourOfAKind, r);
// newTwoPair canonicalises so the higher pair is always primary.
export const newTwoPair = (a: Rank, b: Rank): Claim => (b > a ? mk(TwoPair, b, a) : mk(TwoPair, a, b));
export const newFullHouse = (t: Rank, p: Rank): Claim => mk(FullHouse, t, p);
export const newFlush = (s: Suit, h: Rank): Claim => mk(Flush, h, 0, s);
export const newStraightFlush = (s: Suit, h: Rank): Claim => mk(StraightFlush, h, 0, s);
export const newRoyalFlush = (s: Suit): Claim => mk(RoyalFlush, 0, 0, s);

export const claimKey = (c: Claim): string =>
  `${c.category}|${c.primary}|${c.secondary}|${c.suit}|${c.hasSuit ? 1 : 0}`;

export const claimEquals = (a: Claim, b: Claim): boolean => claimKey(a) === claimKey(b);

// The claim space: every claim some hand of one to five cards can realize,
// in ascending order. Ordinal lookup gives a strict total order. High Card
// runs from 3 because reduced-pool rounds can evaluate two cards.
export const ClaimSpaceSize = 367;
const allClaims: Claim[] = [];
const claimOrdinal = new Map<string, number>();

(function buildClaimSpace() {
  const add = (c: Claim) => {
    const k = claimKey(c);
    if (claimOrdinal.has(k)) throw new Error(`poker: duplicate claim ${k}`);
    claimOrdinal.set(k, allClaims.length);
    allClaims.push(c);
  };
  for (let h = Three; h <= Ace; h++) add(newHighCard(h));
  for (let r = MinRank; r <= MaxRank; r++) add(newPair(r));
  for (let hi = Three; hi <= Ace; hi++) for (let lo = MinRank; lo < hi; lo++) add(newTwoPair(hi, lo));
  for (let r = MinRank; r <= MaxRank; r++) add(newTrips(r));
  for (let h = Five; h <= Ace; h++) add(newStraight(h));
  for (let h = Seven; h <= Ace; h++) for (let s = Clubs; s <= Spades; s++) add(newFlush(s, h));
  for (let t = MinRank; t <= MaxRank; t++)
    for (let p = MinRank; p <= MaxRank; p++) if (p !== t) add(newFullHouse(t, p));
  for (let r = MinRank; r <= MaxRank; r++) add(newQuads(r));
  for (let h = Five; h <= King; h++) for (let s = Clubs; s <= Spades; s++) add(newStraightFlush(s, h));
  for (let s = Clubs; s <= Spades; s++) add(newRoyalFlush(s));
  if (allClaims.length !== ClaimSpaceSize) {
    throw new Error(`poker: claim space is ${allClaims.length}, expected ${ClaimSpaceSize}`);
  }
})();

export const allClaimsCopy = (): Claim[] => allClaims.map((c) => ({ ...c }));
export const minClaim = (): Claim => ({ ...allClaims[0]! });
export const maxClaim = (): Claim => ({ ...allClaims[allClaims.length - 1]! });

/** ordinal returns the position in the total order, or -1 if malformed. */
export const ordinal = (c: Claim): number => claimOrdinal.get(claimKey(c)) ?? -1;
export const claimLegal = (c: Claim): boolean => claimOrdinal.has(claimKey(c));

export function compareClaims(a: Claim, b: Claim): number {
  const ao = ordinal(a);
  const bo = ordinal(b);
  if (ao < 0 || bo < 0) throw new Error("poker: compare on malformed claim");
  return Math.sign(ao - bo);
}

export const higher = (a: Claim, b: Claim): boolean => compareClaims(a, b) > 0;

export function claimsAbove(c: Claim): Claim[] {
  const o = ordinal(c);
  if (o < 0) return [];
  return allClaims.slice(o + 1).map((x) => ({ ...x }));
}

export function claimString(c: Claim): string {
  switch (c.category) {
    case HighCard:
      return `${rankWordOf(c.primary)} high`;
    case OnePair:
      return `Pair of ${rankPluralOf(c.primary)}`;
    case TwoPair:
      return `Two Pair — ${rankPluralOf(c.primary)} and ${rankPluralOf(c.secondary)}`;
    case ThreeOfAKind:
      return `Three ${rankPluralOf(c.primary)}`;
    case Straight:
      return `Straight to ${rankString(c.primary)}`;
    case Flush:
      return `${rankWordOf(c.primary)}-high ${suitTitleOf(c.suit)} Flush`;
    case FullHouse:
      return `${rankPluralOf(c.primary)} full of ${rankPluralOf(c.secondary)}`;
    case FourOfAKind:
      return `Four ${rankPluralOf(c.primary)}`;
    case StraightFlush:
      return `Straight Flush to ${rankWordOf(c.primary)} of ${suitTitleOf(c.suit)}`;
    case RoyalFlush:
      return `Royal Flush of ${suitTitleOf(c.suit)}`;
  }
  return `Claim{${claimKey(c)}}`;
}

// ---------------------------------------------------------------- wire form

export interface ClaimJSON {
  category: string;
  primary?: string;
  secondary?: string;
  suit?: string;
}

export function claimToJSON(c: Claim): ClaimJSON {
  const out: ClaimJSON = { category: categoryString(c.category) };
  if (c.primary !== 0) out.primary = rankString(c.primary);
  if (c.secondary !== 0) out.secondary = rankString(c.secondary);
  if (c.hasSuit) out.suit = suitNameOf(c.suit);
  return out;
}

// claimFromJSON rebuilds a claim through its constructor, so wire input is
// canonicalised. Membership in the claim space is checked by the caller.
// Unknown fields are rejected.
export function claimFromJSON(raw: unknown): Claim {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) throw new Error("poker: claim must be an object");
  const allowed = new Set(["category", "primary", "secondary", "suit"]);
  for (const k of Object.keys(raw)) if (!allowed.has(k)) throw new Error(`poker: unknown claim field ${k}`);
  const r = raw as Record<string, unknown>;
  const str = (k: string): string => {
    const v = r[k];
    if (v === undefined || v === null) return "";
    if (typeof v !== "string") throw new Error(`poker: claim field ${k} must be a string`);
    return v;
  };
  const cat = parseCategory(str("category"));
  const p = str("primary");
  const s2 = str("secondary");
  const su = str("suit");
  const primary = p ? parseRank(p) : 0;
  const secondary = s2 ? parseRank(s2) : 0;
  const suit = su ? parseSuit(su) : 0;
  switch (cat) {
    case HighCard:
      return newHighCard(primary);
    case OnePair:
      return newPair(primary);
    case TwoPair:
      return newTwoPair(primary, secondary);
    case ThreeOfAKind:
      return newTrips(primary);
    case Straight:
      return newStraight(primary);
    case Flush:
      return newFlush(suit, primary);
    case FullHouse:
      return newFullHouse(primary, secondary);
    case FourOfAKind:
      return newQuads(primary);
    case StraightFlush:
      return newStraightFlush(suit, primary);
    default:
      return newRoyalFlush(suit);
  }
}

// ---------------------------------------------------------------- classify

export const MinHandSize = 1;
export const FullHandSize = 5;

// straightHigh returns the high card of the straight formed by exactly five
// distinct ranks, or 0. The wheel A-2-3-4-5 is 5-high; ranks never wrap.
function straightHigh(ranks: Rank[]): Rank {
  if (ranks.length !== FullHandSize) return 0;
  const set = new Set(ranks);
  if (set.size !== FullHandSize) return 0;
  const sorted = [...set].sort((a, b) => a - b);
  if (sorted[4]! - sorted[0]! === 4) return sorted[4]!;
  if (sorted[0] === Two && sorted[1] === Three && sorted[2] === Four && sorted[3] === Five && sorted[4] === Ace) {
    return Five;
  }
  return 0;
}

// classify returns the single claim that a hand exactly realizes. Because a
// hand has exactly one classification, a stronger hand never validates a
// weaker claim. Five-card categories are unreachable below five cards.
export function classify(hand: readonly Card[]): Claim {
  const n = hand.length;
  if (n < MinHandSize || n > FullHandSize) throw new Error("poker: hand must hold 1 to 5 cards");
  const counts = new Map<Rank, number>();
  const ranks: Rank[] = [];
  let maxRank = 0;
  let sameSuit = true;
  hand.forEach((c, i) => {
    if (!cardValid(c)) throw new Error("poker: invalid card");
    counts.set(c.rank, (counts.get(c.rank) ?? 0) + 1);
    ranks.push(c.rank);
    if (c.rank > maxRank) maxRank = c.rank;
    if (i > 0 && c.suit !== hand[0]!.suit) sameSuit = false;
  });
  if (hasDuplicates(hand)) throw new Error("poker: duplicate card in hand");

  if (n === FullHandSize) {
    const suit = hand[0]!.suit;
    const sh = straightHigh(ranks);
    if (sameSuit && sh === Ace) return newRoyalFlush(suit);
    if (sameSuit && sh !== 0) return newStraightFlush(suit, sh);
    let trips = 0;
    let pair = 0;
    for (const [r, k] of counts) {
      if (k === 3) trips = r;
      if (k === 2) pair = r;
    }
    if (trips && pair) return newFullHouse(trips, pair);
    if (sameSuit) return newFlush(suit, maxRank);
    if (sh !== 0) return newStraight(sh);
  }

  let quad = 0;
  let trip = 0;
  const pairs: Rank[] = [];
  for (let r = MaxRank; r >= MinRank; r--) {
    const k = counts.get(r) ?? 0;
    if (k === 4) quad = r;
    else if (k === 3) trip = r;
    else if (k === 2) pairs.push(r);
  }
  if (quad) return newQuads(quad);
  if (trip) return newTrips(trip);
  if (pairs.length === 2) return newTwoPair(pairs[0]!, pairs[1]!);
  if (pairs.length === 1) return newPair(pairs[0]!);
  return newHighCard(maxRank);
}

// ---------------------------------------------------------------- validate

export type EvalMode = "EXACT_FIVE" | "REDUCED_POOL";

// handSize is the whole reduced-pool rule: below five cards, the entire pool
// is the hand; otherwise every five-card subset is a candidate.
export const handSize = (pool: number): number => (pool < FullHandSize ? pool : FullHandSize);
export const modeFor = (pool: number): EvalMode => (pool < FullHandSize ? "REDUCED_POOL" : "EXACT_FIVE");

export type Owns = (c: Card) => boolean;

// The single ownership rule at every pool size: the claimant must own at
// least one card of the evaluated hand (not necessarily of the pattern).
export const ownershipSatisfied = (hand: readonly Card[], owns: Owns): boolean => hand.some(owns);

export function satisfies(hand: readonly Card[], c: Claim): boolean {
  try {
    return claimEquals(classify(hand), c);
  } catch {
    return false;
  }
}

export interface Result {
  satisfied: boolean;
  witness: Card[];
  mode: EvalMode;
  poolSize: number;
  handSize: number;
  examined: number;
}

// forEachCombination calls fn with every k-subset in canonical index order,
// stopping when fn returns true. The buffer is reused; copy what you keep.
function forEachCombination(cards: readonly Card[], k: number, fn: (h: Card[]) => boolean): boolean {
  const n = cards.length;
  if (k < 0 || k > n) return false;
  if (k === 0) return fn([]);
  const idx = Array.from({ length: k }, (_, i) => i);
  const buf: Card[] = new Array(k);
  for (;;) {
    for (let i = 0; i < k; i++) buf[i] = cards[idx[i]!]!;
    if (fn(buf)) return true;
    let i = k - 1;
    while (i >= 0 && idx[i] === i + n - k) i--;
    if (i < 0) return false;
    idx[i]!++;
    for (let j = i + 1; j < k; j++) idx[j] = idx[j - 1]! + 1;
  }
}

// canSatisfyExhaustive is the reference oracle: first satisfying subset in
// canonical order. Kept for tests.
export function canSatisfyExhaustive(pool: readonly Card[], owns: Owns, c: Claim): Result {
  const n = pool.length;
  const res: Result = { satisfied: false, witness: [], mode: modeFor(n), poolSize: n, handSize: handSize(n), examined: 0 };
  if (res.handSize < MinHandSize) return res;
  forEachCombination(sortedCopy(pool), res.handSize, (hand) => {
    res.examined++;
    if (satisfies(hand, c) && ownershipSatisfied(hand, owns)) {
      res.satisfied = true;
      res.witness = hand.map((x) => ({ ...x }));
      return true;
    }
    return false;
  });
  return res;
}

interface Group {
  cards: Card[];
  take: number;
}

function straightRankSet(high: Rank): Set<Rank> {
  if (high === Five) return new Set([Ace, Two, Three, Four, Five]);
  const out = new Set<Rank>();
  for (let r = high - 4; r <= high; r++) out.add(r);
  return out;
}

function candidateGroups(pool: Card[], c: Claim): Group[] | null {
  const byRank = (r: Rank) => pool.filter((x) => x.rank === r);
  const othersExcluding = (...rs: Rank[]) => pool.filter((x) => !rs.includes(x.rank));
  const check = (...gs: Group[]) => (gs.every((g) => g.cards.length >= g.take) ? gs : null);
  switch (c.category) {
    case RoyalFlush: {
      const want = new Set([Ten, Jack, Queen, King, Ace]);
      return check({ cards: pool.filter((x) => x.suit === c.suit && want.has(x.rank)), take: 5 });
    }
    case StraightFlush: {
      const want = straightRankSet(c.primary);
      return check({ cards: pool.filter((x) => x.suit === c.suit && want.has(x.rank)), take: 5 });
    }
    case Flush:
      return check({ cards: pool.filter((x) => x.suit === c.suit && x.rank <= c.primary), take: 5 });
    case Straight: {
      const want = straightRankSet(c.primary);
      return check({ cards: pool.filter((x) => want.has(x.rank)), take: 5 });
    }
    case FullHouse:
      return check({ cards: byRank(c.primary), take: 3 }, { cards: byRank(c.secondary), take: 2 });
    case FourOfAKind:
      return check({ cards: byRank(c.primary), take: 4 }, { cards: othersExcluding(c.primary), take: 1 });
    case ThreeOfAKind:
      return check({ cards: byRank(c.primary), take: 3 }, { cards: othersExcluding(c.primary), take: 2 });
    case TwoPair:
      return check(
        { cards: byRank(c.primary), take: 2 },
        { cards: byRank(c.secondary), take: 2 },
        { cards: othersExcluding(c.primary, c.secondary), take: 1 },
      );
    case OnePair:
      return check({ cards: byRank(c.primary), take: 2 }, { cards: othersExcluding(c.primary), take: 3 });
    case HighCard:
      return check({ cards: byRank(c.primary), take: 1 }, { cards: pool.filter((x) => x.rank < c.primary), take: 4 });
  }
  return null;
}

function searchGroups(groups: Group[], acc: Card[], fn: (h: Card[]) => boolean): boolean {
  if (groups.length === 0) return fn(acc);
  const [g, ...rest] = groups;
  return forEachCombination(g!.cards, g!.take, (chosen) => searchGroups(rest, [...acc, ...chosen], fn));
}

// canSatisfy is the production validator. It is claim-directed, and every
// candidate is still verified with satisfies, so it can never return a false
// positive.
export function canSatisfy(pool: readonly Card[], owns: Owns, c: Claim): Result {
  const n = pool.length;
  const res: Result = { satisfied: false, witness: [], mode: modeFor(n), poolSize: n, handSize: handSize(n), examined: 0 };
  if (res.handSize < MinHandSize) return res;
  const sorted = sortedCopy(pool);

  if (res.mode === "REDUCED_POOL") {
    res.examined = 1;
    if (satisfies(sorted, c) && ownershipSatisfied(sorted, owns)) {
      res.satisfied = true;
      res.witness = sorted;
    }
    return res;
  }

  const groups = candidateGroups(sorted, c);
  if (!groups) return res;
  searchGroups(groups, [], (hand) => {
    res.examined++;
    if (satisfies(hand, c) && ownershipSatisfied(hand, owns)) {
      res.satisfied = true;
      res.witness = sortedCopy(hand);
      return true;
    }
    return false;
  });
  return res;
}

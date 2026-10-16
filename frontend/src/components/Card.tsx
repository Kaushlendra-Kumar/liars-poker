"use client";

import { cardParts } from "@/protocol/labels";

/**
 * A card face, or a face-down back when the value is not ours to see.
 *
 * The component cannot be made to render a hidden card by accident: a back
 * is what you get when no code is supplied, and the store never supplies a
 * code the viewer is not entitled to.
 */
export function Card({ code, small }: { code?: string; small?: boolean }) {
  const size = small ? "h-12 w-9 text-sm" : "h-20 w-14 text-lg";
  if (!code) {
    return (
      <span
        role="img"
        aria-label="Face-down card"
        className={`${size} inline-flex items-center justify-center rounded-md border-2 border-slate-500 bg-slate-700 font-bold text-slate-300`}
      >
        ?
      </span>
    );
  }
  const { rank, symbol, label, red } = cardParts(code);
  return (
    <span
      role="img"
      aria-label={label}
      data-testid="card-face"
      className={`${size} inline-flex flex-col items-center justify-center rounded-md border-2 border-slate-300 bg-white font-bold ${
        red ? "text-rose-700" : "text-slate-900"
      }`}
    >
      <span>{rank}</span>
      <span aria-hidden="true">{symbol}</span>
    </span>
  );
}

/** CardRow renders a hand, using backs for any cards we cannot see. */
export function CardRow({
  cards,
  hiddenCount = 0,
  small,
  label,
}: {
  cards: string[];
  hiddenCount?: number;
  small?: boolean;
  label: string;
}) {
  return (
    <ul aria-label={label} className="flex flex-wrap gap-1">
      {cards.map((code, i) => (
        <li key={`${code}-${i}`}>
          <Card code={code} small={small} />
        </li>
      ))}
      {Array.from({ length: hiddenCount }).map((_, i) => (
        <li key={`back-${i}`}>
          <Card small={small} />
        </li>
      ))}
    </ul>
  );
}

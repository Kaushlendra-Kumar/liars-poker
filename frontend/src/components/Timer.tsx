"use client";

import { useEffect, useState } from "react";

/**
 * A countdown to a server-supplied absolute deadline.
 *
 * The server owns the clock. This displays the remaining time and never acts
 * on reaching zero; if it disagrees with the server, the next view wins.
 */
export function Timer({ deadline, label }: { deadline?: string; label: string }) {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!deadline) return;
    const id = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(id);
  }, [deadline]);

  if (!deadline) return null;
  const ms = new Date(deadline).getTime() - now;
  const seconds = Math.max(0, Math.ceil(ms / 1000));
  const urgent = seconds <= 5;

  return (
    <p
      data-testid="timer"
      className={`text-sm font-semibold ${urgent ? "text-rose-300" : "text-emerald-200"}`}
      aria-live="off"
    >
      {label}: {seconds} seconds remaining
      {urgent ? <span className="sr-only"> (running out)</span> : null}
    </p>
  );
}

import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Liar's Poker",
  description: "A multiplayer bluffing card game.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen bg-felt-dark text-slate-50 antialiased">
        <a
          href="#main"
          className="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:z-50 focus:rounded focus:bg-white focus:px-3 focus:py-2 focus:text-slate-900"
        >
          Skip to content
        </a>
        {children}
      </body>
    </html>
  );
}

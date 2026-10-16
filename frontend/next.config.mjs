/** @type {import('next').NextConfig} */

// Where the Go server lives. Used only to proxy during development; in
// production nginx already puts the client and the server on one origin.
const BACKEND = process.env.BACKEND_ORIGIN ?? "http://localhost:8787";

const nextConfig = {
  reactStrictMode: true,
  // Emits .next/standalone with a minimal server and only the node_modules
  // it actually needs, which is what the container image copies.
  output: "standalone",

  env: {
    // Empty means "same origin", so the client fetches /rooms relatively.
    //
    // Pointing it straight at http://localhost:8080 would make the call
    // cross-origin, and the browser would block the response: the room is
    // created but the reply is unreadable, which looks exactly like the
    // server being down. Production has no such problem because nginx
    // serves both from one origin, so development mirrors that with the
    // rewrite below rather than by adding CORS headers to the server.
    NEXT_PUBLIC_SERVER_HTTP: process.env.NEXT_PUBLIC_SERVER_HTTP ?? "",
    NEXT_PUBLIC_SERVER_WS:
      process.env.NEXT_PUBLIC_SERVER_WS ?? "ws://localhost:8787/ws",
  },

  // Development only: nginx does this in production. A WebSocket handshake
  // is not subject to the same-origin policy, so /ws needs no proxying and
  // continues to go straight to the server.
  async rewrites() {
    return [
      { source: "/rooms", destination: `${BACKEND}/rooms` },
      { source: "/rooms/:code", destination: `${BACKEND}/rooms/:code` },
    ];
  },
};
export default nextConfig;

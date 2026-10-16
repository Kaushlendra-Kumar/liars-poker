import { defineConfig } from "vitest/config";
import path from "node:path";

// The e2e suite talks to the real Go server over a real WebSocket, so it
// runs in Node rather than jsdom and has a longer timeout.
export default defineConfig({
  resolve: { alias: { "@": path.resolve(__dirname, "./src") } },
  test: {
    environment: "node",
    globals: true,
    include: ["test/e2e/**/*.test.ts"],
    testTimeout: 60_000,
    hookTimeout: 60_000,
  },
});

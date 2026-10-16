import type { Config } from "tailwindcss";

const config: Config = {
  content: ["./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        felt: { DEFAULT: "#123c2a", dark: "#0c2a1d", light: "#1b5138" },
      },
    },
  },
  plugins: [],
};
export default config;

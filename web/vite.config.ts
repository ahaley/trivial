import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The production build lands in dist/, which the Go binary embeds (SPEC.md §8).
// In development, Vite serves the UI and proxies the API to a running
// `trivial serve`, so both sides reload independently.
const apiTarget = process.env.TRIVIAL_DEV_API ?? "http://localhost:1234";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // No source maps. This output is committed and embedded into the binary,
    // where a map would be four times the size of the bundle it describes and
    // would dominate every commit. Debug against `npm run dev`, which has full
    // source mapping and hot reload.
    sourcemap: false,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": { target: apiTarget, changeOrigin: true },
    },
  },
});

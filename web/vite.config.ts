import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    // In development the client runs on Vite's port and the API on the Go
    // server's, so same-origin fetches are proxied rather than requiring CORS.
    proxy: {
      "/api": { target: "http://localhost:8080", changeOrigin: true },
      "/healthz": { target: "http://localhost:8080", changeOrigin: true },
    },
  },
  build: { outDir: "dist" },
});

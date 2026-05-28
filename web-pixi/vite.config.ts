import { defineConfig } from "vite";

export default defineConfig({
  server: {
    allowedHosts: ["spacemmo-josh.ngrok.app", "localhost"],
    proxy: {
      "/ws": {
        target: "http://localhost:8080",
        ws: true,
      },
      // /auth/* HTTPS endpoints live on the Go backend; vite needs to
      // proxy them so credentials:'same-origin' fetches from the SPA
      // page (served by vite) reach the right host. Without this every
      // /auth/login etc. returns 404 from vite's static server.
      "/auth": {
        target: "http://localhost:8080",
      },
      // Diagnostic endpoints — heartbeat WebSocket + write-path stats.
      // Used by the in-browser dev overlay (toggle ~) to A/B compare
      // game frames against a synthetic heartbeat. Without these
      // entries, the SPA page (served by vite) can't reach either.
      "/probe-ws": {
        target: "http://localhost:8080",
        ws: true,
      },
      "/debug": {
        target: "http://localhost:8080",
      },
    },
    fs: {
      allow: [".."],
    },
  },
  resolve: {
    dedupe: ["@bufbuild/protobuf"],
  },
});

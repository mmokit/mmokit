import { defineConfig } from "vite";

export default defineConfig({
  server: {
    proxy: {
      "/ws": {
        target: "ws://localhost:8080",
        ws: true,
      },
      // /auth/* HTTPS endpoints live on the Go backend; vite needs to
      // proxy them so credentials:'same-origin' fetches from the SPA
      // page (served by vite) reach the right host. Without this every
      // /auth/login etc. returns 404 from vite's static server.
      "/auth": {
        target: "http://localhost:8080",
      },
    },
    fs: {
      allow: ["../../.."],
    },
  },
  resolve: {
    dedupe: ["@bufbuild/protobuf"],
  },
});

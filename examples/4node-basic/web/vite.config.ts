import { defineConfig } from "vite";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

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
    alias: {
      "@gen/enginepb/engine_pb.js": path.resolve(__dirname, "../../../gen/es/enginepb/engine_pb.js"),
      "@gen/basicpb/basic_pb.js": path.resolve(__dirname, "../../../gen/es/basicpb/basic_pb.js"),
    },
    dedupe: ["@bufbuild/protobuf"],
  },
});

import { defineConfig } from "vite";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  server: {
    allowedHosts: ["spacemmo-josh.ngrok.app", "localhost"],
    proxy: {
      "/ws": {
        target: "http://localhost:8080",
        ws: true,
      },
    },
    fs: {
      allow: [".."],
    },
  },
  resolve: {
    alias: {
      "@gen/engine_pb.js": path.resolve(__dirname, "../gen/es/enginepb/engine_pb.js"),
      "@gen/game_pb.js": path.resolve(__dirname, "../gen/es/gamepb/game_pb.js"),
    },
    dedupe: ["@bufbuild/protobuf"],
  },
});

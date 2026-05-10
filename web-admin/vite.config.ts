import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { resolve } from "node:path";

export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: resolve(__dirname, "../pkg/admin/static/dist"),
    emptyOutDir: true,
  },
  base: "/admin/",
  server: {
    port: 5173,
    proxy: {
      "/admin/api": "http://localhost:9101",
    },
  },
});

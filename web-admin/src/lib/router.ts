// Minimal hash-based router. Routes are flat strings; the panel registry
// (Task 13) drives navigation. We avoid svelte-spa-router's external dep
// because our routing surface is small and stable.

import { writable, type Readable } from "./writable";

const path = writable<string>(initialPath());

function initialPath(): string {
  if (typeof window === "undefined") return "/cluster";
  return parsePath(window.location.hash);
}

function parsePath(hash: string): string {
  if (!hash || hash === "#" || hash === "#/") return "/cluster";
  if (hash.startsWith("#")) return hash.slice(1);
  return hash;
}

if (typeof window !== "undefined") {
  window.addEventListener("hashchange", () => path.set(parsePath(window.location.hash)));
}

export function navigate(to: string): void {
  if (typeof window !== "undefined") {
    window.location.hash = to;
  }
  path.set(to);
}

export const route: Readable<string> = path;

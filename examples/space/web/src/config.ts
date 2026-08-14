// Runtime client configuration. The web client is served as a standalone
// bundle (no longer by the Go server), so it learns the backend's
// host:port at boot from /config.json on its own origin. In dev
// (import.meta.env.DEV) the vite proxy makes /ws and /auth same-origin,
// so backendUrl stays empty and relative URLs are used.

export interface RuntimeConfig {
  // Absolute backend origin, e.g. "https://api.play.example.com" or
  // "http://localhost:8080". Empty string = same-origin (dev / proxy).
  backendUrl: string;
}

let cfg: RuntimeConfig = { backendUrl: "" };

// loadRuntimeConfig fetches /config.json once at startup. Safe to call
// before any network/auth code. Tolerates a missing or malformed file by
// falling back to same-origin.
export async function loadRuntimeConfig(): Promise<void> {
  if (import.meta.env.DEV) {
    cfg = { backendUrl: "" };
    return;
  }
  try {
    const res = await fetch("/config.json", { cache: "no-store" });
    if (res.ok) {
      const parsed = (await res.json()) as Partial<RuntimeConfig>;
      cfg = { backendUrl: (parsed.backendUrl ?? "").replace(/\/+$/, "") };
    } else {
      console.warn(
        `config.json fetch returned ${res.status}; falling back to same-origin`,
      );
    }
  } catch (err) {
    console.warn("config.json load failed; falling back to same-origin", err);
  }
}

// backendBase returns the absolute backend origin (no trailing slash), or
// "" for same-origin. Assumes loadRuntimeConfig has already resolved; returns
// the same-origin default ("") otherwise. Prefix HTTP paths with this:
// `${backendBase()}/auth/me`.
export function backendBase(): string {
  return cfg.backendUrl;
}

// backendWsUrl returns the WebSocket URL for /ws, derived from the backend
// origin (http→ws, https→wss). Assumes loadRuntimeConfig has already resolved;
// falls back to the current page origin when backendUrl is empty (dev /
// same-origin via proxy).
export function backendWsUrl(): string {
  const base = cfg.backendUrl;
  if (base) {
    const wsBase = base.replace(/^http/, "ws"); // http→ws, https→wss
    return `${wsBase}/ws`;
  }
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/ws`;
}

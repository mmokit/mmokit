# Decouple Web-Client Serving from mmokit

**Date:** 2026-05-30
**Status:** Approved — ready for implementation planning
**Motivation:** A Unity client is coming. The Go game server should not be coupled to
serving a web client. mmokit must stop providing the static webserver for the game
client; the client bundle is served by a separate process (vite in dev, an off-the-shelf
static host in build/run). Both clients (web today, Unity next) dial the gateway directly
at a configured backend address.

## Principle

The game server (gateway) exposes **only protocol/server endpoints**:

- `/ws` — WebSocket game transport
- `/auth/*` — auth endpoints
- diagnostics — `/metrics`, `/commands`, `/commands/`, `/events`, `/probe-ws`, `/debug/*`

Serving the **client bundle** is a deployment concern handled by a separate static-host
process. The gateway HTTP listener still binds (for the endpoints above) — it simply no
longer serves files.

## Decisions (from brainstorming)

1. **Scope:** Only static-bundle serving leaves mmokit. `/ws`, `/auth`, and diagnostics
   stay on the gateway.
2. **Prod routing:** The web client dials the backend directly (cross-origin), not via a
   reverse proxy. This matches Unity, which also dials a configured backend address.
3. **Auth model:** Keep httpOnly cookies; add a CORS cross-site mode (CORS headers +
   `SameSite=None; Secure` cookies). Unity-specific auth is a separate future concern.
4. **Backend URL discovery:** Runtime `config.json` fetched at boot. One build deploys
   anywhere. Dev falls back to same-origin relative URLs (vite proxy).
5. **Static host:** Off-the-shelf static file server (`bunx serve`). No new Go binary.
6. **Surfaces:** All of them — `cmd/server` + `web-pixi`, `examples/simple`, and
   `examples/4node-basic`.

## Current State (for reference)

The gateway HTTP listener in [`pkg/universe/bootstrap.go`](../../../pkg/universe/bootstrap.go)
(`startHTTPListener`) mounts `/ws`, `/probe-ws`, `/debug/conn-stats`, `/metrics`,
`/commands`, `/commands/`, `/events`, the game `HTTPRoutes` hook (which mounts `/auth/*`
via the auth service), and — the part being removed — a `/` static file server driven by
`Config.WebDir` / `Config.StaticFS` / `Config.StaticFSPrefix`.

- `cmd/server` embeds `web-pixi/dist` via `webpixi.FS` and passes it as `StaticFS`.
- `examples/simple` embeds `index.html` directly in `main.go`.
- `examples/4node-basic` embeds `web/dist`; its `just dev` already runs vite with
  `--web-dir=disabled`.
- The WebSocket handler already accepts any origin (`InsecureSkipVerify: true` in
  [`pkg/net/server.go`](../../../pkg/net/server.go)), so `/ws` is already cross-origin-safe.
- There are **no** CORS headers anywhere today.
- Auth cookies default to `HttpOnly=true, Secure=true, SameSite=Strict`
  ([`pkg/services/auth/cookie.go`](../../../pkg/services/auth/cookie.go)); `--dev-insecure-cookie`
  disables `Secure` for the plain-http same-origin dev path.
- In dev, both vite configs proxy `/ws`, `/auth`, `/probe-ws`, `/debug` to `:8080`, so the
  dev path is same-origin and needs no CORS.

## Changes

### 1. Engine — remove static serving (`pkg/universe`)

- Delete `Config.WebDir`, `Config.StaticFS`, `Config.StaticFSPrefix` from
  `coordinator.go`.
- Delete the `--web-dir` flag in `bootstrap.go`.
- Delete the static `switch c.cfg.WebDir { … }` block and the `/` `http.FileServer` mount
  in `startHTTPListener`. The listener otherwise unchanged — it still binds for `/ws`,
  `/auth`, and diagnostics. (`--port` / `Config.HTTPPort` stays.)
- Remove the `WebDir = "embed"` default-setting in `coordinator.go` defaults.
- Remove now-unused imports (`io/fs`, etc.) surfaced by `go vet`.

### 2. Engine — add CORS (`pkg/universe`)

- New `Config.CORSOrigins []string` + `--cors-origins` flag (comma-separated allowlist).
- A CORS middleware wraps the gateway mux in `startHTTPListener`:
  - If the request `Origin` is in the allowlist, set `Access-Control-Allow-Origin: <origin>`
    (echo, not `*` — required with credentials), `Access-Control-Allow-Credentials: true`,
    `Access-Control-Allow-Methods`, `Access-Control-Allow-Headers: Content-Type`, and
    `Vary: Origin`.
  - Short-circuit `OPTIONS` preflight with `204 No Content`.
  - When the allowlist is empty, emit no CORS headers — the dev/same-origin path is
    completely unaffected.
- The middleware wraps the whole gateway mux so `/auth`, `/debug/conn-stats`, etc. are all
  covered uniformly. `/ws` upgrades are unaffected (origin already open).

### 3. Engine — cross-site cookie mode (`pkg/services/auth` + wiring)

- When `CORSOrigins` is non-empty (cross-origin serving), auth cookies must be
  `SameSite=None; Secure` so the browser sends them on cross-site `fetch(credentials:'include')`.
- Derive the cross-site cookie policy from `CORSOrigins` being non-empty, plumbed into the
  auth `HTTPOpts` (`SameSite = http.SameSiteNoneMode`, `CookieSecure = true`) at the point
  the auth service is wired (`pkg/mmokit/auth.go` / `pkg/universe/gateway.go`).
- **Caveat (documented):** `SameSite=None` requires `Secure`, which requires HTTPS.
  Browsers treat `localhost` as a secure context, so local plain-http cross-origin testing
  works; production must use HTTPS. The canonical dev path remains the vite proxy
  (same-origin, no CORS, no cross-site cookies).

### 4. Web client — runtime config + backend URL (`web-pixi`)

- Add `loadRuntimeConfig(): Promise<{ backendUrl: string }>`:
  - In a production build, `fetch('/config.json')` from the client's own origin.
  - In dev (`import.meta.env.DEV`), skip the fetch and return `{ backendUrl: "" }` →
    same-origin relative URLs (vite proxy).
  - Tolerate a missing/!ok `config.json` by falling back to `{ backendUrl: "" }`.
- `network.ts`: build the ws URL from `backendUrl` when set
  (`http(s)://host → ws(s)://host/ws`), else fall back to `window.location.host`.
- Auth fetches: prefix request URLs with `backendUrl` and use `credentials:'include'`
  (works for both same-origin dev and cross-origin prod).
- Ship `web-pixi/public/config.json` with default `{ "backendUrl": "" }` (same-origin) as
  the deployable template. Operators overwrite the file per environment without rebuilding.
  Vite copies `public/` into `dist/` at build, so the served bundle carries it.
- Delete `web-pixi/embed.go` (the `//go:embed all:dist`).

### 5. `cmd/server`

- Remove the `webpixi` import and the `StaticFS` / `StaticFSPrefix` config fields. The
  server no longer embeds or serves the client.

### 6. Examples (all converted)

- **`examples/4node-basic`:** remove `//go:embed all:web/dist` + `StaticFS` /
  `StaticFSPrefix` from `main.go`; drop the now-removed `--web-dir=disabled` arg from its
  justfile. Its vite proxy dev path is unchanged. Add a `config.json` template +
  serve target paralleling the root setup.
- **`examples/simple`:** remove `//go:embed index.html` + `WebDir` / `StaticFS` from
  `main.go`; `index.html` is served by the separate static host (`bunx serve examples/simple`).
  Update its justfile `run` to launch the static host alongside the server.

### 7. Static host + tooling (`bunx serve`)

- Serve the built client with `bunx serve <dir> --single` (serves `index.html` +
  `config.json`; `--single` provides SPA fallback).
- Root `justfile`:
  - `build-web` still builds the client (`vite build`) but is **no longer** a prerequisite
    of `build-go` (the client isn't embedded anymore). Keep it in the top-level `build`
    aggregate so `dist/` stays current.
  - New `web-serve` target: `cd web-pixi && bunx serve dist -l 5174` (port TBD during
    implementation), runnable as its own tmux process.
  - Optional combined target that launches the Go server + `bunx serve` in tmux panes for a
    full build-and-run experience.
  - `dev` (vite proxy, same-origin) unchanged.
  - Update the `distributed-space` comment: the browser no longer hits `:8080` for assets;
    it hits the static host. Backend stays `:8080`.

## Testing

- **Engine (CORS middleware unit test):** allowlisted `Origin` → response carries
  `Access-Control-Allow-Origin: <origin>` + `Access-Control-Allow-Credentials: true`;
  `OPTIONS` preflight → `204`; non-allowlisted `Origin` → no `Access-Control-Allow-Origin`
  header; empty allowlist → no CORS headers at all.
- **Engine (listener):** with static serving removed, the gateway listener still serves
  `/ws` and `/auth/*` (smoke/existing gateway tests stay green).
- **Auth (cookie policy):** in cross-site mode the session cookie is
  `SameSite=None; Secure`; in default mode it remains `SameSite=Strict`.
- **Manual smoke:**
  - `just dev` still works end-to-end (vite proxy, same-origin) — no regression.
  - Build the bundle, run `bunx serve web-pixi/dist -l 5174`, run the server with
    `--cors-origins=http://localhost:5174`, point `config.json` at `http://localhost:8080`
    → cross-origin login + gameplay succeed.

## Out of Scope / Follow-ups

- Unity-specific auth (token/bearer model for a non-browser client) — tracked separately.
- Relocating `/auth` or diagnostics off the gateway — explicitly kept on the gateway.
- Reverse-proxy production topology — rejected in favor of direct cross-origin dialing.
- HTTPS/TLS termination for production cross-site cookies — deployment concern, not engine.

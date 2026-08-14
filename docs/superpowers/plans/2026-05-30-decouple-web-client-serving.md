# Decouple Web-Client Serving from mmokit — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove static web-client serving from the mmokit engine so the game server exposes only protocol endpoints (`/ws`, `/auth/*`, diagnostics); serve the client bundle from a separate process, with the client dialing the backend directly (cross-origin) via runtime config + CORS.

**Architecture:** The gateway HTTP listener keeps binding for `/ws`, `/auth/*`, and diagnostics but drops the `/` static file mount and the `WebDir`/`StaticFS` config. A new CORS middleware (allowlist from `--cors-origins`) wraps the gateway mux, and auth cookies switch to `SameSite=None; Secure` when CORS is active. The web client fetches `/config.json` at boot to learn its backend URL; in dev it falls back to same-origin relative URLs (vite proxy). The built bundle is served by `bunx serve`.

**Tech Stack:** Go 1.x (`pkg/universe`, `pkg/services/auth`), TypeScript + Vite + PixiJS (`web-pixi`), `bunx serve`, `just`/tmux.

**Reference spec:** [docs/superpowers/specs/2026-05-30-decouple-web-client-serving-design.md](../specs/2026-05-30-decouple-web-client-serving-design.md)

**Codebase conventions (read first):**
- Never `go build ./...` (drops binaries in package dirs). Use `go vet ./...` to check compilation.
- Game code imports `mmokit` only; `pkg/` internals are engine code. `mmokit.Config` is a re-export of `universe.Config`.
- Comma-separated CLI list flags follow the `LogCategories` pattern: a `string` Config field bound via `stringFlag`, split at use-site.
- JS/TS uses `bun`, never `npm`.

---

## File Structure

**Engine (Go):**
- `pkg/universe/coordinator.go` — remove `WebDir`/`StaticFS`/`StaticFSPrefix` fields + the `WebDir="embed"` default; add `CORSOrigins string` field.
- `pkg/universe/bootstrap.go` — remove `--web-dir` flag + the static `switch` block in `startHTTPListener`; add `--cors-origins` flag; wrap mux with CORS middleware; drop unused `io/fs` import.
- `pkg/universe/cors.go` — **new**: `corsMiddleware(originsCSV string, next http.Handler) http.Handler`.
- `pkg/universe/cors_test.go` — **new**: middleware unit tests.
- `pkg/services/auth/cookie.go` — add `CrossSiteHTTPOpts(HTTPOpts) HTTPOpts` helper.
- `pkg/services/auth/cookie_test.go` — **new**: cross-site opts test.
- `pkg/mmokit/auth.go` — apply cross-site cookie mode when `CORSOrigins` is set.

**Callers (Go):**
- `cmd/server/main.go` — drop `webpixi` import + `StaticFS`/`StaticFSPrefix`.
- `web-pixi/embed.go` — **delete**.
- `examples/simple/main.go` — drop `//go:embed index.html` + `WebDir`/`StaticFS`.
- `examples/4node-basic/main.go` — drop `//go:embed all:web/dist` + `StaticFS`/`StaticFSPrefix`.

**Web client (TS):**
- `web-pixi/src/config.ts` — **new**: `loadRuntimeConfig()` + `backendBase()`.
- `web-pixi/public/config.json` — **new**: `{ "backendUrl": "" }`.
- `web-pixi/src/network.ts` — derive ws URL from `backendBase()`.
- `web-pixi/src/auth.ts` — prefix fetches with `backendBase()`, use `credentials:'include'`.
- `web-pixi/src/main.ts` — `await loadRuntimeConfig()` before connect/auth.

**Tooling:**
- `justfile` (root) — `web-serve` target; decouple `build-web` from `build-go`; fix `distributed-space` comment.
- `examples/simple/justfile`, `examples/4node-basic/justfile` — serve client separately; drop `--web-dir`.
- `examples/4node-basic/web/public/config.json` — **new** (parity).

---

## Task 1: Remove static serving from the engine

Removes the static-asset capability and fixes every caller so the tree compiles. Pure refactor — verified by `go vet ./...`.

**Files:**
- Modify: `pkg/universe/coordinator.go` (fields ~196-210; default ~726)
- Modify: `pkg/universe/bootstrap.go` (flag ~97-99; static block ~187-209; import line 8)
- Modify: `cmd/server/main.go` (import line 18; config lines 41-42; comment 27-28)
- Modify: `examples/simple/main.go` (embed + config)
- Modify: `examples/4node-basic/main.go` (embed + config)
- Delete: `web-pixi/embed.go`

- [ ] **Step 1: Remove the three Config fields in `coordinator.go`**

Delete this block (currently ~lines 192-210):

```go
	// WebDir selects the static-asset source for the engine HTTP server:
	//   "embed"     → serve from Config.StaticFS (sub by StaticFSPrefix if set)
	//   ""          → no static serving
	//   "disabled"  → no static serving
	//   <fs-path>   → http.FileServer(http.Dir(path)) for dev iteration
	// Bound by --web-dir. Default: "embed".
	WebDir string

	// StaticFS is the embedded web-asset filesystem the engine serves when
	// WebDir == "embed". Games typically pass the raw //go:embed FS; set
	// StaticFSPrefix to the subdirectory inside that FS (e.g. "web/dist")
	// and the engine calls fs.Sub for you. Nil is tolerated: the engine
	// logs a warning and skips static serving.
	StaticFS fs.FS

	// StaticFSPrefix is the optional sub-path inside StaticFS. When non-empty
	// the engine calls fs.Sub(StaticFS, StaticFSPrefix) before mounting the
	// file server. Example: "web/dist".
	StaticFSPrefix string
```

Then update the `HTTPRoutes` doc comment just below it: change `(/ws, /metrics, static)` to `(/ws, /metrics, /auth, diagnostics)`.

- [ ] **Step 2: Add the `CORSOrigins` field in `coordinator.go`**

Immediately after the `DevInsecureCookie bool` field (~line 73), add:

```go
	// CORSOrigins is a comma-separated allowlist of browser origins
	// permitted to make credentialed cross-origin requests to the gateway
	// HTTP endpoints (/auth/*, diagnostics). Empty = no CORS headers
	// (same-origin / vite-proxy dev path). When non-empty, auth cookies are
	// also switched to SameSite=None; Secure so they ride cross-site
	// requests. Bound by --cors-origins.
	CORSOrigins string
```

- [ ] **Step 3: Remove the `WebDir="embed"` default in `coordinator.go`**

Delete (~lines 726-728):

```go
	if cfg.WebDir == "" {
		cfg.WebDir = "embed"
	}
```

- [ ] **Step 4: Remove `--web-dir` flag, add `--cors-origins` in `bootstrap.go`**

Replace the `--web-dir` block (~lines 97-99):

```go
	stringFlag("web-dir",
		"web asset source: 'embed' (Config.StaticFS), '' or 'disabled' (off), or a filesystem path",
		"embed", &c.WebDir)
```

with:

```go
	stringFlag("cors-origins",
		"comma-separated allowlist of browser origins for credentialed cross-origin requests (empty = none)",
		"", &c.CORSOrigins)
```

Also update the `--port` flag help string (~line 101) from `/ws, /metrics, and static assets` to `/ws, /metrics, /auth (gateway role only; -1 disables)`.

- [ ] **Step 5: Remove the static `switch` block in `startHTTPListener`**

Delete the entire block (~lines 187-209):

```go
	switch c.cfg.WebDir {
	case "", "disabled":
		c.Log.Log(CatMeshCell, "http: static asset serving disabled")
	case "embed":
		if c.cfg.StaticFS == nil {
			c.Log.Log(CatMeshCell, "http: WebDir=embed but Config.StaticFS is nil — skipping static serving")
			break
		}
		rootFS := c.cfg.StaticFS
		if c.cfg.StaticFSPrefix != "" {
			sub, err := fs.Sub(c.cfg.StaticFS, c.cfg.StaticFSPrefix)
			if err != nil {
				c.Log.Log(CatMeshCell, "http: fs.Sub(%q) failed: %v — skipping static serving", c.cfg.StaticFSPrefix, err)
				break
			}
			rootFS = sub
		}
		mux.Handle("/", http.FileServer(http.FS(rootFS)))
		c.Log.Log(CatMeshCell, "http: serving embedded static assets from StaticFS")
	default:
		mux.Handle("/", http.FileServer(http.Dir(c.cfg.WebDir)))
		c.Log.Log(CatMeshCell, "http: serving static assets from %s", c.cfg.WebDir)
	}
```

Leave the `if c.cfg.HTTPRoutes != nil { c.cfg.HTTPRoutes(mux) }` block that follows it untouched. Also update the `startHTTPListener` doc comment (~line 161-162): change `serves /ws, /metrics, and static assets` to `serves /ws, /auth, /metrics, and diagnostics`.

- [ ] **Step 6: Drop the now-unused `io/fs` import in `bootstrap.go`**

Remove `"io/fs"` (line 8). (`coordinator.go` still uses `fs.FS` for `ExtraMigrations` — leave its import.)

- [ ] **Step 7: Fix `cmd/server/main.go`**

Remove the import (line 18):

```go
	webpixi "github.com/mmokit/mmokit/web-pixi"
```

In the `mmokit.Config{...}` literal, remove these two lines (41-42):

```go
		StaticFS:       webpixi.FS,
		StaticFSPrefix: "dist",
```

Replace the comment block at lines 26-33 (the `// The engine's startHTTPListener owns /ws, /metrics, and static asset ...` paragraph) with:

```go
	// The engine's startHTTPListener owns /ws, /auth, and /metrics on any
	// gateway-role process. The web client is no longer embedded — it is
	// built into web-pixi/dist and served by a separate static host (see
	// the root justfile's `web-serve` target). UDP is started separately
	// below — different protocol, same ConnManager.
```

- [ ] **Step 8: Delete `web-pixi/embed.go`**

```bash
rm web-pixi/embed.go
```

- [ ] **Step 9: Fix `examples/simple/main.go`**

Remove the import block's `"embed"` and the embed var:

```go
//go:embed index.html
var webFS embed.FS
```

Change the `import` to drop `"embed"` (leaving only the mmokit import). In the `mmokit.Config{...}`, remove:

```go
		WebDir:        "embed",
		StaticFS:      webFS,
```

Update the file's doc comment line `// Open:  http://localhost:8080            game` to:

```go
// Open:  http://localhost:5174            game client (run `just web-serve`)
```

- [ ] **Step 10: Fix `examples/4node-basic/main.go`**

Remove `"embed"` from the import block and delete:

```go
//go:embed all:web/dist
var webDist embed.FS
```

In the `mmokit.Config{...}`, remove:

```go
		StaticFS:         webDist,
		StaticFSPrefix:   "web/dist",
```

- [ ] **Step 11: Verify the tree compiles**

Run: `go vet ./...`
Expected: no output (exit 0). No `WebDir`/`StaticFS`/`webpixi`/`webFS`/`webDist` undefined errors, no "imported and not used".

- [ ] **Step 12: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/bootstrap.go cmd/server/main.go \
        examples/simple/main.go examples/4node-basic/main.go
git rm web-pixi/embed.go
git commit -m "universe: remove static web-client serving from the engine

Drop WebDir/StaticFS/StaticFSPrefix config, the --web-dir flag, and the /
file-server mount. The gateway listener still serves /ws, /auth, and
diagnostics. Add a CORSOrigins config field (wired in a follow-up commit).
Callers stop embedding their client bundles."
```

---

## Task 2: CORS middleware

A standalone, unit-testable middleware that adds credentialed-CORS headers for allowlisted origins and answers preflight. No-op when the allowlist is empty.

**Files:**
- Create: `pkg/universe/cors.go`
- Create: `pkg/universe/cors_test.go`
- Modify: `pkg/universe/bootstrap.go` (wrap mux in `startHTTPListener`)

- [ ] **Step 1: Write the failing test**

Create `pkg/universe/cors_test.go`:

```go
package universe

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestCORS_AllowlistedOrigin(t *testing.T) {
	h := corsMiddleware("http://localhost:5174, https://play.example.com", okHandler())

	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.Header.Set("Origin", "http://localhost:5174")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5174" {
		t.Fatalf("ACAO = %q, want echoed origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("ACAC = %q, want true", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestCORS_Preflight(t *testing.T) {
	h := corsMiddleware("http://localhost:5174", okHandler())

	req := httptest.NewRequest("OPTIONS", "/auth/login", nil)
	req.Header.Set("Origin", "http://localhost:5174")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatalf("missing Access-Control-Allow-Methods on preflight")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("preflight body = %q, want empty", rec.Body.String())
	}
}

func TestCORS_DisallowedOrigin(t *testing.T) {
	h := corsMiddleware("http://localhost:5174", okHandler())

	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("ACAO = %q, want empty for disallowed origin", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (request still served)", rec.Code)
	}
}

func TestCORS_EmptyAllowlistIsPassthrough(t *testing.T) {
	inner := okHandler()
	h := corsMiddleware("", inner)
	if h != inner {
		t.Fatalf("empty allowlist must return the inner handler unchanged")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/universe/ -run TestCORS -v`
Expected: FAIL — `undefined: corsMiddleware`.

- [ ] **Step 3: Implement the middleware**

Create `pkg/universe/cors.go`:

```go
package universe

import (
	"net/http"
	"strings"
)

// corsMiddleware wraps next with credentialed-CORS handling for the
// gateway HTTP listener. originsCSV is a comma-separated allowlist of
// browser origins (e.g. "http://localhost:5174,https://play.example.com").
//
// For an allowlisted Origin the middleware echoes it in
// Access-Control-Allow-Origin (echo, not "*", is required alongside
// Access-Control-Allow-Credentials: true) and answers OPTIONS preflight
// with 204. Requests from non-allowlisted origins are passed through
// untouched (the browser blocks the response, but server logic still runs
// for same-origin/non-browser callers). When the allowlist is empty the
// inner handler is returned verbatim — zero overhead on the dev path.
func corsMiddleware(originsCSV string, next http.Handler) http.Handler {
	allowed := parseOrigins(originsCSV)
	if len(allowed) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// parseOrigins splits a comma-separated origin list into a set, trimming
// whitespace and dropping empties.
func parseOrigins(csv string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(csv, ",") {
		if s := strings.TrimSpace(p); s != "" {
			out[s] = true
		}
	}
	return out
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./pkg/universe/ -run TestCORS -v`
Expected: PASS (all four).

- [ ] **Step 5: Wire the middleware into `startHTTPListener`**

In `pkg/universe/bootstrap.go`, find (~line 215-216):

```go
	addr := fmt.Sprintf(":%d", c.cfg.HTTPPort)
	c.httpServer = &http.Server{Addr: addr, Handler: mux}
```

Change the `Handler` to wrap the mux:

```go
	addr := fmt.Sprintf(":%d", c.cfg.HTTPPort)
	c.httpServer = &http.Server{Addr: addr, Handler: corsMiddleware(c.cfg.CORSOrigins, mux)}
	if c.cfg.CORSOrigins != "" {
		c.Log.Log(CatMeshCell, "http: CORS enabled for origins: %s", c.cfg.CORSOrigins)
	}
```

- [ ] **Step 6: Verify compile + tests**

Run: `go vet ./pkg/universe/ && go test ./pkg/universe/ -run TestCORS`
Expected: no vet output; `ok` for the test run.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/cors.go pkg/universe/cors_test.go pkg/universe/bootstrap.go
git commit -m "universe: add CORS middleware for cross-origin gateway clients

Allowlist from --cors-origins; echoes the origin with
Allow-Credentials:true and answers OPTIONS preflight. No-op when the
allowlist is empty (dev/same-origin path)."
```

---

## Task 3: Cross-site cookie mode

When CORS is enabled the auth cookie must be `SameSite=None; Secure` to ride cross-site `fetch(credentials:'include')`. Add a pure helper and apply it in the facade.

**Files:**
- Modify: `pkg/services/auth/cookie.go` (add `CrossSiteHTTPOpts`)
- Create: `pkg/services/auth/cookie_test.go`
- Modify: `pkg/mmokit/auth.go` (apply when `CORSOrigins` set)

- [ ] **Step 1: Write the failing test**

Create `pkg/services/auth/cookie_test.go`:

```go
package auth

import (
	"net/http"
	"testing"
)

func TestCrossSiteHTTPOpts(t *testing.T) {
	in := DefaultHTTPOpts() // SameSite=Strict, Secure=true
	out := CrossSiteHTTPOpts(in)

	if out.SameSite != http.SameSiteNoneMode {
		t.Fatalf("SameSite = %v, want None", out.SameSite)
	}
	if !out.CookieSecure {
		t.Fatalf("CookieSecure = false, want true (SameSite=None requires Secure)")
	}
	// Unrelated fields preserved.
	if out.CookieName != in.CookieName {
		t.Fatalf("CookieName changed: %q != %q", out.CookieName, in.CookieName)
	}
}

func TestCrossSiteHTTPOpts_ForcesSecureOverDevInsecure(t *testing.T) {
	in := DefaultHTTPOpts()
	in.CookieSecure = false // as --dev-insecure-cookie would set it
	out := CrossSiteHTTPOpts(in)
	if !out.CookieSecure {
		t.Fatalf("cross-site mode must force Secure=true even over a dev-insecure base")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/services/auth/ -run TestCrossSiteHTTPOpts -v`
Expected: FAIL — `undefined: CrossSiteHTTPOpts`.

- [ ] **Step 3: Implement the helper**

In `pkg/services/auth/cookie.go`, after `DefaultHTTPOpts()`, add:

```go
// CrossSiteHTTPOpts returns o adjusted for cross-site delivery: the
// browser only sends a cookie on a cross-origin credentialed request when
// it is SameSite=None, and SameSite=None is rejected without Secure. This
// forces both, overriding any dev-insecure Secure=false base — callers
// running cross-origin must use HTTPS (localhost is a secure context, so
// local plain-http testing still works). Applied by the mmokit facade
// when Config.CORSOrigins is set.
func CrossSiteHTTPOpts(o HTTPOpts) HTTPOpts {
	o.SameSite = http.SameSiteNoneMode
	o.CookieSecure = true
	return o
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./pkg/services/auth/ -run TestCrossSiteHTTPOpts -v`
Expected: PASS (both).

- [ ] **Step 5: Apply it in the facade**

In `pkg/mmokit/auth.go`, find the dev-insecure block (~lines 71-74):

```go
	// --dev-insecure-cookie overrides Secure regardless of caller intent.
	if p.Config().DevInsecureCookie {
		opts.HTTPOpts.CookieSecure = false
	}
```

Immediately after it, add:

```go
	// Cross-origin serving (Config.CORSOrigins set) requires SameSite=None;
	// Secure so the session cookie rides cross-site fetch(credentials:
	// 'include'). This wins over --dev-insecure-cookie (Secure forced true);
	// localhost is a secure context so local cross-origin testing still works.
	if strings.TrimSpace(p.Config().CORSOrigins) != "" {
		opts.HTTPOpts = auth.CrossSiteHTTPOpts(opts.HTTPOpts)
	}
```

Ensure `"strings"` is imported in `pkg/mmokit/auth.go` (add to the import block if absent).

- [ ] **Step 6: Verify compile + tests**

Run: `go vet ./pkg/mmokit/ ./pkg/services/auth/ && go test ./pkg/services/auth/ -run TestCrossSiteHTTPOpts`
Expected: no vet output; `ok`.

- [ ] **Step 7: Commit**

```bash
git add pkg/services/auth/cookie.go pkg/services/auth/cookie_test.go pkg/mmokit/auth.go
git commit -m "auth: SameSite=None;Secure cookies when CORS cross-origin is enabled

CrossSiteHTTPOpts forces None+Secure; the facade applies it when
Config.CORSOrigins is non-empty, overriding --dev-insecure-cookie."
```

---

## Task 4: Web client runtime config (web-pixi)

The client learns its backend URL from `/config.json` at boot; dev uses same-origin relative URLs (vite proxy).

**Files:**
- Create: `web-pixi/src/config.ts`
- Create: `web-pixi/public/config.json`
- Modify: `web-pixi/src/network.ts` (~line 165-171)
- Modify: `web-pixi/src/auth.ts` (lines 16-21, 46-65)
- Modify: `web-pixi/src/main.ts` (~line 45-46)

- [ ] **Step 1: Create the config module**

Create `web-pixi/src/config.ts`:

```ts
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
    }
  } catch {
    /* fall back to same-origin */
  }
}

// backendBase returns the absolute backend origin (no trailing slash), or
// "" for same-origin. Prefix HTTP paths with this: `${backendBase()}/auth/me`.
export function backendBase(): string {
  return cfg.backendUrl;
}

// backendWsUrl returns the WebSocket URL for /ws, derived from the backend
// origin (http→ws, https→wss). Falls back to the current page origin when
// backendUrl is empty (dev / same-origin via proxy).
export function backendWsUrl(): string {
  const base = cfg.backendUrl;
  if (base) {
    const wsBase = base.replace(/^http/, "ws"); // http→ws, https→wss
    return `${wsBase}/ws`;
  }
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/ws`;
}
```

- [ ] **Step 2: Create the deployable config template**

Create `web-pixi/public/config.json`:

```json
{ "backendUrl": "" }
```

- [ ] **Step 3: Use the backend ws URL in `network.ts`**

In `web-pixi/src/network.ts`, add to the imports at the top of the file:

```ts
import { backendWsUrl } from "./config";
```

Then in `connect()` (~lines 165-171), replace:

```ts
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  const statusEl = document.getElementById("status")!;
```

with:

```ts
  const statusEl = document.getElementById("status")!;
```

and replace the client URL line:

```ts
    url: `${proto}//${window.location.host}/ws`,
```

with:

```ts
    url: backendWsUrl(),
```

- [ ] **Step 4: Prefix auth fetches with the backend base in `auth.ts`**

At the top of `web-pixi/src/auth.ts`, add:

```ts
import { backendBase } from "./config";
```

In `postJSON`, change the fetch (lines 16-21) to prefix the path and use `include`:

```ts
async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(`${backendBase()}${path}`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
```

In `authMe` (line 58), change:

```ts
  const res = await fetch("/auth/me", { credentials: "same-origin" });
```

to:

```ts
  const res = await fetch(`${backendBase()}/auth/me`, { credentials: "include" });
```

- [ ] **Step 5: Load config first in `main.ts`**

In `web-pixi/src/main.ts`, add to the imports:

```ts
import { loadRuntimeConfig } from "./config";
```

Make it the very first line inside `async function main()` (before `createInitialState()`, ~line 46):

```ts
async function main() {
  await loadRuntimeConfig();
  const state = createInitialState();
```

- [ ] **Step 6: Typecheck the client**

Run: `cd web-pixi && bun run typecheck`
Expected: no errors (tsc exits 0).

- [ ] **Step 7: Commit**

```bash
git add web-pixi/src/config.ts web-pixi/public/config.json \
        web-pixi/src/network.ts web-pixi/src/auth.ts web-pixi/src/main.ts
git commit -m "web-pixi: learn backend URL from runtime /config.json

Client fetches /config.json at boot (dev falls back to same-origin via
vite proxy); ws + auth derive their host from backendUrl and use
credentials:'include' for cross-origin auth."
```

---

## Task 5: Root build + serve tooling

Decouple the web build from the Go build, add a `web-serve` process, and fix stale comments.

**Files:**
- Modify: `justfile` (root)

- [ ] **Step 1: Decouple `build-web` from `build-go` in the top-level `build`**

The current `build` (line 12) is:

```
build: space-sdk build-web admin-build build-go
```

Leave it as-is — it still builds the client into `web-pixi/dist` so a built bundle is ready to serve. But update the comment above `build-go` (lines 6-7) from:

```
# build the Go binary only (assumes web-pixi/dist already exists)
build-go:
```

to:

```
# build the Go binary only (the web client is no longer embedded — it is
# served separately via `just web-serve`)
build-go:
```

And update the comment above `build-web` (lines 1-2) from:

```
# build the web-pixi client (vite → web-pixi/dist) — required before go
# build so the webpixi package's //go:embed has real content to include.
```

to:

```
# build the web-pixi client (vite → web-pixi/dist). Served standalone via
# `just web-serve`; no longer embedded into the Go binary.
```

- [ ] **Step 2: Add a `web-serve` target**

After the `dev` recipe (~line 51), add:

```
# serve the built web client (web-pixi/dist) as a standalone static host.
# Run alongside `just run` (server on :8080) for a no-vite build+run setup.
# Pass the matching origin to the server: --cors-origins=http://localhost:5174
web-serve port="5174":
    cd web-pixi && bunx serve dist --single --listen {{port}}
```

- [ ] **Step 3: Fix the `distributed-space` comment**

The comment block above `distributed-space` (~lines 53-56) currently says the browser is served by the gateway at `:8080`. Replace the line:

```
# at http://localhost:8080 — gateway serves the embedded web-pixi/dist.
```

with:

```
# Gateway serves only /ws + /auth on :8080; serve the web client separately
# with `just web-serve` (or `just dev` for vite) and point it at :8080.
```

- [ ] **Step 4: Verify just recipes parse**

Run: `just --list | grep -E 'web-serve|build-go|build-web'`
Expected: `web-serve`, `build-go`, `build-web` all listed (no parse error).

- [ ] **Step 5: Commit**

```bash
git add justfile
git commit -m "just: add web-serve (bunx serve) target; decouple web build from go build"
```

---

## Task 6: Convert examples (simple + 4node-basic)

Remove the last embedded-client assumptions from the example justfiles/clients so they run with the separate-host model.

**Files:**
- Modify: `examples/4node-basic/justfile` (dev recipe, ~line 56)
- Modify: `examples/simple/justfile` (run recipe)
- Create: `examples/4node-basic/web/public/config.json`
- Modify: `examples/simple/index.html` (ws URL)

- [ ] **Step 1: Drop `--web-dir=disabled` from 4node's `dev`**

In `examples/4node-basic/justfile`, the `dev` recipe's last line (~line 56) is:

```
    cd {{root}} && ./bin/4node-basic --web-dir=disabled '--postgres-url={{postgres_url}}' --dev-insecure-cookie {{ARGS}}
```

Remove the now-removed flag:

```
    cd {{root}} && ./bin/4node-basic '--postgres-url={{postgres_url}}' --dev-insecure-cookie {{ARGS}}
```

(The `echo-dev` recipe at ~line 61 — check for and remove any `--web-dir` there too.)

- [ ] **Step 2: Add a config.json for 4node's web client (parity)**

Create `examples/4node-basic/web/public/config.json`:

```json
{ "backendUrl": "" }
```

(4node's dev path uses the vite proxy, so `backendUrl` stays empty; the file exists so a standalone `bunx serve` of its build has a config to overwrite per environment.)

- [ ] **Step 3: Point simple's client ws at the backend port**

`examples/simple/index.html` builds its WebSocket URL from `window.location.host`. Since the page is now served from a different port (e.g. `:5174`) than the backend (`:8080`), find the ws-URL construction (search the file for `location.host` / `new WebSocket` / `/ws`) and change it to target the backend port explicitly. Replace the host expression with:

```js
const backend = location.hostname + ":8080";
const proto = location.protocol === "https:" ? "wss:" : "ws:";
const ws = new WebSocket(`${proto}//${backend}/ws`);
```

(Adapt variable names to the existing code; the key change is `location.host` → `location.hostname + ":8080"`. `simple` uses `AnonymousAuth`, so there are no `/auth` fetches to adjust.)

- [ ] **Step 4: Serve simple's client from its `run` recipe**

In `examples/simple/justfile`, the `run` recipe (~lines 38-40) is:

```
run: db-up build
    {{bin}} '--postgres-url={{postgres_url}}' --dev-insecure-cookie
```

Replace it with a tmux-wrapped version that serves the client alongside the server:

```
# build + run: client on :5174 (bunx serve), server /ws+/auth on :8080,
# admin on :9101/admin/ (admin / admin)
run: db-up build
    #!/usr/bin/env bash
    set -euo pipefail
    tmux kill-session -t simple-web 2>/dev/null || true
    tmux new-session -d -s simple-web -c "{{root}}/examples/simple" 'bunx serve . --single --listen 5174'
    trap 'tmux kill-session -t simple-web 2>/dev/null' INT TERM EXIT
    {{bin}} '--postgres-url={{postgres_url}}' --cors-origins=http://localhost:5174 --dev-insecure-cookie
```

- [ ] **Step 5: Verify recipes parse**

Run: `cd examples/simple && just --list >/dev/null && cd ../4node-basic && just --list >/dev/null && echo OK`
Expected: `OK` (both justfiles parse).

- [ ] **Step 6: Verify the whole tree still compiles**

Run: `cd . && go vet ./...`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add examples/4node-basic/justfile examples/4node-basic/web/public/config.json \
        examples/simple/justfile examples/simple/index.html
git commit -m "examples: serve clients standalone; drop --web-dir from justfiles"
```

---

## Task 7: Full-build verification + manual smoke

Confirms the engine, SDK, and web build are all green end-to-end, then provides the manual cross-origin smoke steps.

**Files:** none (verification only)

- [ ] **Step 1: Full Go test of the touched packages**

Run: `go test ./pkg/universe/ ./pkg/services/auth/ ./pkg/mmokit/`
Expected: `ok` for each (no FAIL).

- [ ] **Step 2: Web client typecheck + production build**

Run: `cd web-pixi && bun run build`
Expected: tsc passes and vite writes `dist/` (including `dist/config.json` copied from `public/`). Confirm:
Run: `test -f web-pixi/dist/config.json && echo present`
Expected: `present`.

- [ ] **Step 3: Full server build**

Run: `cd . && just build`
Expected: completes; `bin/server` exists. Confirm the binary no longer embeds the client:
Run: `ls -la bin/server`
Expected: a binary noticeably smaller than before (web bundle no longer linked in) — informational only.

- [ ] **Step 4: Manual smoke — dev path (no regression)**

These are MANUAL steps for the user (do not run servers unattended in the agent session — see the "no leftover processes" convention). Report them to the user:

```
# Dev path (vite proxy, same-origin) — must still work unchanged:
just dev
# → open http://localhost:5173 ; register/login; fly around.
```

- [ ] **Step 5: Manual smoke — standalone cross-origin path (the new capability)**

```
# Terminal A — backend only, CORS allowlisting the static host origin:
./bin/server --cors-origins=http://localhost:5174 --dev-insecure-cookie

# Terminal B — serve the built client standalone:
just web-serve            # bunx serve web-pixi/dist --single --listen 5174

# Edit web-pixi/dist/config.json → { "backendUrl": "http://localhost:8080" }
# Open http://localhost:5174 ; register/login; confirm:
#   - /auth/login succeeds cross-origin (Network tab: request to :8080,
#     response has Access-Control-Allow-Origin: http://localhost:5174)
#   - the session cookie is set with SameSite=None; Secure
#   - the WebSocket connects to ws://localhost:8080/ws and gameplay works
```

- [ ] **Step 6: Final commit (if any verification fixes were needed)**

If steps 1-3 surfaced fixes, commit them with a descriptive message. Otherwise nothing to commit — verification only.

---

## Self-Review Notes

- **Spec coverage:** Engine static removal (Task 1), CORS (Task 2), cross-site cookies (Task 3), web-pixi runtime config (Task 4), tooling/`bunx serve` (Task 5), all-examples conversion (Task 6), testing (Tasks 2/3 unit + Task 7 build/smoke). All spec sections mapped.
- **Type consistency:** `corsMiddleware(string, http.Handler) http.Handler`, `parseOrigins`, `CrossSiteHTTPOpts(HTTPOpts) HTTPOpts`, and the TS `loadRuntimeConfig()`/`backendBase()`/`backendWsUrl()` names are used identically across the tasks that define and call them.
- **Known caveat carried from spec:** cross-site cookies need HTTPS in real prod; localhost is a secure context so the local smoke works. The `--dev-insecure-cookie` flag is intentionally overridden by cross-site mode.
- **Line numbers** are approximate anchors from the 2026-05-30 tree; match on the surrounding code shown, not the line number.

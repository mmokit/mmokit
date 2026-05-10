# Admin Dashboard — Frontend Foundation + Cluster Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a usable login + Cluster page for the admin dashboard — the Svelte 5 + Vite SPA scaffold, lib/ modules (api/stream/auth/stores/types/format/icons), app shell (sidebar + topbar + routing), the login flow, and the hero Cluster page with canvas-rendered CellMap + drilldown drawer.

**Architecture:** A new top-level `web-admin/` directory holds the SPA. `bun run build` produces output directly into `pkg/admin/static/dist/` — the Go binary's `//go:embed dist` (already wired in the foundation plan) picks up the bundle on its next build. Live updates flow through one multiplexed SSE connection at `/admin/api/stream`; commands ride POST `/admin/api/commands/<verb>`. State is Svelte-5 runes (`$state`, `$derived`) over per-topic stores. The Cluster page renders a canvas-based quadtree-aware CellMap and a slide-in drawer for cell details + ops.

**Tech Stack:** Bun, Svelte 5, Vite, TypeScript, Tailwind CSS, lucide-svelte (icons), Vitest (lib unit tests), Playwright (optional e2e — deferred to a follow-up plan if it complicates v1).

**Spec:** [`docs/superpowers/specs/2026-05-10-admin-dashboard-design.md`](../specs/2026-05-10-admin-dashboard-design.md) §5 (Frontend), §6 (Wire API), §8 panels #1, 2, 4, 10, 21, 25 (Cluster + drawer + ownership view + cluster ops + alert banner + ⌘K palette stub).

**Backend:** Already shipped in [`docs/superpowers/plans/2026-05-10-admin-dashboard-backend-foundation.md`](2026-05-10-admin-dashboard-backend-foundation.md). Curl-testable at `:9101/admin/api/*` with `--admin-listen=:9101 --admin-enabled` set on a 4node-basic instance.

---

## Quick orientation

Files to skim before starting:

- `pkg/admin/admin.go` — `Mount()` routes; the SPA's HTTP contract
- `pkg/admin/view.go` — Go DTO struct shapes; the TS types in `web-admin/src/lib/types.ts` mirror these
- `pkg/admin/api_auth.go` — login response shape (`{user, grants, expiresAt}`)
- `pkg/admin/api_read.go` — cluster/cells endpoint shapes
- `pkg/admin/sse.go` — SSE wire format: `event: <topic>\ndata: <json>\n\n`
- `examples/4node-basic/web/` — reference TypeScript+Vite+Bun project layout (no Svelte, no Tailwind — different stack but same toolchain)
- Existing `justfile` `build-web` recipe pattern

The dashboard runs at `http://<admin-listen>/admin/` once the binary is built. For local dev, `just admin-dev` proxies API calls to a separately-running `:9101` backend.

The SPA is **game-agnostic** — it lives in `web-admin/` (top-level), not under `examples/4node-basic/`. Every mmokit deployment that enables `Config.Admin.Enabled` gets it embedded.

---

## File structure

```text
web-admin/
├── package.json
├── bun.lock                         # produced by `bun install`
├── vite.config.ts                   # outDir → ../pkg/admin/static/dist
├── tailwind.config.ts
├── postcss.config.js
├── tsconfig.json
├── tsconfig.node.json
├── svelte.config.js
├── vitest.config.ts
├── index.html
├── src/
│   ├── main.ts                      # mount, router init
│   ├── app.svelte                   # shell: <Sidebar /> + <TopBar /> + route outlet
│   ├── style.css                    # tailwind directives + theme tokens
│   ├── lib/
│   │   ├── api.ts                   # fetch wrappers, error normalization
│   │   ├── api.test.ts
│   │   ├── stream.ts                # SSE client w/ topic demux + reconnect
│   │   ├── stream.test.ts
│   │   ├── auth.ts                  # login / logout / session check
│   │   ├── auth.test.ts
│   │   ├── stores.ts                # Svelte stores per topic
│   │   ├── types.ts                 # TS mirror of admin DTOs
│   │   ├── format.ts                # bytes/duration/load formatters
│   │   ├── format.test.ts
│   │   ├── icons.ts                 # lucide subset re-exports
│   │   └── router.ts                # tiny hash router (svelte 5 friendly)
│   ├── routes/
│   │   ├── login.svelte
│   │   └── cluster.svelte
│   └── components/
│       ├── Sidebar.svelte
│       ├── TopBar.svelte
│       ├── AlertBanner.svelte
│       ├── CellMap.svelte           # canvas-rendered hero
│       ├── CellMap.test.ts          # layout math
│       └── CellDrawer.svelte
└── (no dist/ committed — build artifacts go to pkg/admin/static/dist)
```

`pkg/admin/static/dist/` is gitignored except for the placeholder `index.html` and `.gitkeep` already committed by the foundation plan.

Build/dev commands (added in Task 2):

- `just admin-dev` — `cd web-admin && bun install && bun run dev` (Vite dev server on `:5173` proxying to `:9101`)
- `just admin-build` — `cd web-admin && bun install --frozen-lockfile && bun run build` (writes to `pkg/admin/static/dist/`)
- `just admin-test` — `cd web-admin && bun run test` (Vitest)

---

### Task 1: Web project scaffold

**Files:**
- Create: `web-admin/package.json`
- Create: `web-admin/tsconfig.json`
- Create: `web-admin/tsconfig.node.json`
- Create: `web-admin/svelte.config.js`
- Create: `web-admin/vite.config.ts`
- Create: `web-admin/index.html`
- Create: `web-admin/src/main.ts`
- Create: `web-admin/src/app.svelte`
- Modify: `.gitignore` (add `web-admin/node_modules/`, `pkg/admin/static/dist/*` exceptions)

- [ ] **Step 1: package.json**

```json
{
  "name": "mmokit-admin",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "svelte-check --tsconfig ./tsconfig.json && vite build",
    "preview": "vite preview",
    "typecheck": "svelte-check --tsconfig ./tsconfig.json",
    "test": "vitest run",
    "test:watch": "vitest"
  },
  "dependencies": {
    "lucide-svelte": "^0.460.0",
    "svelte": "^5.0.0"
  },
  "devDependencies": {
    "@sveltejs/vite-plugin-svelte": "^5.0.0",
    "@tailwindcss/postcss": "^4.0.0",
    "autoprefixer": "^10.4.20",
    "jsdom": "^25.0.1",
    "postcss": "^8.4.49",
    "svelte-check": "^4.0.0",
    "tailwindcss": "^4.0.0",
    "typescript": "^5.7.0",
    "vite": "^6.0.0",
    "vitest": "^2.1.0"
  }
}
```

- [ ] **Step 2: tsconfig.json**

```json
{
  "extends": "@tsconfig/svelte/tsconfig.json",
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "verbatimModuleSyntax": true,
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "types": ["vitest/globals"]
  },
  "include": ["src/**/*.ts", "src/**/*.svelte"],
  "exclude": ["node_modules"]
}
```

If `@tsconfig/svelte` isn't installed, create the file standalone (drop the `extends`) — that's the common shape for a Svelte+Vite project. Pure TS strictness is what matters.

- [ ] **Step 3: tsconfig.node.json**

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "allowSyntheticDefaultImports": true,
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "noEmit": true
  },
  "include": ["vite.config.ts", "vitest.config.ts", "tailwind.config.ts"]
}
```

- [ ] **Step 4: svelte.config.js**

```js
import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";

export default {
  preprocess: vitePreprocess(),
  compilerOptions: {
    runes: true,
  },
};
```

- [ ] **Step 5: vite.config.ts**

```ts
import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { resolve } from "node:path";

export default defineConfig({
  plugins: [svelte()],
  // Build directly into the embed location so `go build` picks it up without
  // a copy step. Foundation plan committed a placeholder index.html that this
  // overwrites on first build.
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
```

- [ ] **Step 6: index.html**

```html
<!doctype html>
<html lang="en" class="dark">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>mmokit admin</title>
    <link rel="icon" href="data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'><circle cx='8' cy='8' r='6' fill='%237dd3fc'/></svg>" />
  </head>
  <body class="bg-slate-950 text-slate-300">
    <div id="app"></div>
    <script type="module" src="/src/main.ts"></script>
  </body>
</html>
```

- [ ] **Step 7: src/main.ts**

```ts
import "./style.css";
import App from "./app.svelte";
import { mount } from "svelte";

const target = document.getElementById("app");
if (!target) throw new Error("missing #app root");

mount(App, { target });
```

- [ ] **Step 8: src/app.svelte (placeholder; Tasks 11+ flesh it out)**

```svelte
<main class="p-8">
  <h1 class="text-2xl font-semibold text-cyan-300">mmokit admin</h1>
  <p class="mt-2 text-slate-400">Scaffold up. Login + Cluster page land in later tasks.</p>
</main>
```

- [ ] **Step 9: Update .gitignore**

Add at the bottom:

```gitignore
# admin frontend build artifacts
web-admin/node_modules/
web-admin/.vite/

# admin embed: build output is derived; only the placeholder + .gitkeep are tracked
pkg/admin/static/dist/*
!pkg/admin/static/dist/.gitkeep
!pkg/admin/static/dist/index.html
```

- [ ] **Step 10: Smoke test the install**

```bash
cd web-admin
bun install
```

Expected: dependencies install cleanly; a `bun.lock` and `node_modules/` appear.

If `@sveltejs/vite-plugin-svelte@5` isn't compatible with Svelte 5 yet (check at install time), pin to a compatible major. If Bun complains about `@tsconfig/svelte`, drop the `extends` line in `tsconfig.json` — the standalone config from Step 2 works without it.

- [ ] **Step 11: Commit**

```bash
git add web-admin/package.json web-admin/bun.lock web-admin/tsconfig.json \
  web-admin/tsconfig.node.json web-admin/svelte.config.js web-admin/vite.config.ts \
  web-admin/index.html web-admin/src/main.ts web-admin/src/app.svelte .gitignore
git commit -m "web-admin: project scaffold (Svelte 5 + Vite + Bun + Tailwind v4)"
```

---

### Task 2: Tailwind setup + theme tokens

**Files:**
- Create: `web-admin/postcss.config.js`
- Create: `web-admin/tailwind.config.ts`
- Create: `web-admin/src/style.css`
- Modify: `justfile` (add `admin-dev`, `admin-build`, `admin-test`, wire `admin-build` into `build`)

- [ ] **Step 1: postcss.config.js (Tailwind v4 uses a single PostCSS plugin)**

```js
export default {
  plugins: {
    "@tailwindcss/postcss": {},
    autoprefixer: {},
  },
};
```

- [ ] **Step 2: tailwind.config.ts**

Tailwind v4 prefers a CSS-first config but a `tailwind.config.ts` is still supported for the explicit content scan + theme extension. If Tailwind v4 in your install rejects this file, move the same config into `@theme` blocks inside `style.css`.

```ts
import type { Config } from "tailwindcss";

export default {
  content: ["./index.html", "./src/**/*.{ts,svelte}"],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        // Mockup palette from the design spec — cyan accent on dark slate.
        accent: {
          50: "#ecfeff",
          200: "#a5f3fc",
          300: "#7dd3fc",
          400: "#38bdf8",
          500: "#0ea5e9",
        },
      },
      fontFamily: {
        sans: ["ui-sans-serif", "system-ui", "-apple-system", "Inter", "sans-serif"],
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
    },
  },
} satisfies Config;
```

- [ ] **Step 3: src/style.css**

```css
@import "tailwindcss";

/* Design tokens — referenced by Svelte components via Tailwind theme()
   or directly via CSS custom properties. */
:root {
  --color-bg: #0a0e14;
  --color-surface: #0d1117;
  --color-border: rgba(255, 255, 255, 0.08);
  --color-text: #cbd5e1;
  --color-muted: #94a3b8;
  --color-accent: #7dd3fc;

  /* Cell-load color scale (green → yellow → red), used by CellMap. */
  --load-0: #22c55e;
  --load-1: #84cc16;
  --load-2: #facc15;
  --load-3: #f97316;
  --load-4: #ef4444;
}

html,
body,
#app {
  height: 100%;
  margin: 0;
}

body {
  font-feature-settings: "cv11", "ss01";
}

/* Subtle scrollbar styling for dark theme. */
*::-webkit-scrollbar {
  width: 10px;
  height: 10px;
}
*::-webkit-scrollbar-track {
  background: transparent;
}
*::-webkit-scrollbar-thumb {
  background: rgba(255, 255, 255, 0.1);
  border-radius: 5px;
}
*::-webkit-scrollbar-thumb:hover {
  background: rgba(255, 255, 255, 0.18);
}
```

- [ ] **Step 4: Update the justfile**

Add at the bottom of `justfile`:

```makefile
# build the admin SPA into pkg/admin/static/dist (consumed by //go:embed)
admin-build:
    cd web-admin && bun install --frozen-lockfile && bun run build

# vite dev server with proxy to a running coordinator's --admin-listen=:9101
admin-dev:
    cd web-admin && bun install && bun run dev

# vitest unit tests for lib/*
admin-test:
    cd web-admin && bun run test
```

Find the existing `build:` target (it has `space-sdk build-web build-go`) and add `admin-build` so the binary embeds the SPA:

```makefile
build: space-sdk build-web admin-build build-go
```

- [ ] **Step 5: Verify dev server boots**

```bash
cd web-admin
bun run dev
```

Open `http://localhost:5173/admin/` — the placeholder app.svelte should render with cyan title on dark bg. Kill the server with Ctrl-C.

- [ ] **Step 6: Verify production build**

```bash
cd web-admin
bun run build
```

Expected: `pkg/admin/static/dist/` is regenerated with `index.html`, `assets/*-<hash>.js`, `assets/*-<hash>.css`. The placeholder index.html is overwritten — that's intentional.

- [ ] **Step 7: Commit**

```bash
git add web-admin/postcss.config.js web-admin/tailwind.config.ts \
  web-admin/src/style.css justfile
git commit -m "web-admin: Tailwind v4 + theme tokens + just admin-{dev,build,test} recipes"
```

---

### Task 3: Vitest setup

**Files:**
- Create: `web-admin/vitest.config.ts`
- Create: `web-admin/src/setup-tests.ts`

- [ ] **Step 1: vitest.config.ts**

```ts
import { defineConfig } from "vitest/config";
import { svelte } from "@sveltejs/vite-plugin-svelte";

export default defineConfig({
  plugins: [svelte({ hot: false })],
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: ["./src/setup-tests.ts"],
    include: ["src/**/*.test.ts"],
  },
});
```

- [ ] **Step 2: src/setup-tests.ts (fetch + EventSource shims)**

```ts
// Vitest jsdom doesn't ship EventSource. Provide a minimal mock so SSE tests
// can exercise the demuxer without spinning up a real server.
class MockEventSource extends EventTarget {
  static CONNECTING = 0 as const;
  static OPEN = 1 as const;
  static CLOSED = 2 as const;

  readyState = MockEventSource.CONNECTING;
  url: string;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;

  constructor(url: string) {
    super();
    this.url = url;
    queueMicrotask(() => {
      this.readyState = MockEventSource.OPEN;
      this.onopen?.(new Event("open"));
    });
  }

  // Test-only: fire a server-sent event of the given type with JSON payload.
  emit(type: string, data: unknown) {
    const evt = new MessageEvent(type, { data: JSON.stringify(data) });
    this.dispatchEvent(evt);
  }

  close() {
    this.readyState = MockEventSource.CLOSED;
  }
}

(globalThis as unknown as { EventSource: typeof MockEventSource }).EventSource =
  MockEventSource;

// Tests can grab the most recent constructed mock via this helper.
export function lastEventSource(): MockEventSource | undefined {
  return (globalThis as unknown as { __lastES?: MockEventSource }).__lastES;
}

// Patch the constructor to record the last instance.
const Real = (globalThis as unknown as { EventSource: typeof MockEventSource }).EventSource;
(globalThis as unknown as { EventSource: typeof MockEventSource }).EventSource =
  class extends Real {
    constructor(url: string) {
      super(url);
      (globalThis as unknown as { __lastES?: MockEventSource }).__lastES = this;
    }
  } as typeof MockEventSource;
```

- [ ] **Step 3: Smoke test (no real test yet — this just confirms vitest runs)**

Add a trivial test file `web-admin/src/lib/__smoke.test.ts`:

```ts
import { describe, it, expect } from "vitest";

describe("smoke", () => {
  it("runs", () => {
    expect(1 + 1).toBe(2);
  });
});
```

Run: `cd web-admin && bun run test`
Expected: 1 passing test.

- [ ] **Step 4: Delete the smoke test (real tests come in Tasks 4-9)**

```bash
rm web-admin/src/lib/__smoke.test.ts
```

- [ ] **Step 5: Commit**

```bash
git add web-admin/vitest.config.ts web-admin/src/setup-tests.ts
git commit -m "web-admin: Vitest setup with jsdom + EventSource mock"
```

---

### Task 4: lib/types.ts — TS mirror of Go DTOs

**Files:**
- Create: `web-admin/src/lib/types.ts`

- [ ] **Step 1: Mirror the camelCase JSON shapes from `pkg/admin/view.go`**

```ts
// Type definitions mirror pkg/admin/view.go DTOs (camelCase wire format).
// Hand-maintained for v1; if drift becomes a problem, codegen via cmd/sdkgen.

export type CellInfo = {
  id: string;
  depth: number;
  parent?: string;
  hostId: string;
  load: number;
  tickP99Us: number;
  tickP95Us: number;
  entities: EntityCounts;
  bytes: BytesTotal;
  neighbors: string[] | null;
};

export type EntityCounts = {
  real: number;
  replica: number;
  ghost: number;
  connected: number;
};

export type BytesTotal = {
  sent: number;
  recv: number;
};

export type HostInfo = {
  id: string;
  roles: string[] | null;
  state: "live" | "draining" | "dead";
  isLocal: boolean;
  heartbeatAgeMs: number;
  cells: string[] | null;
  load: number;
  totalEntities: number;
};

export type GatewayInfo = {
  id: string;
  sessions: number;
  bytesSent: number;
  bytesRecv: number;
  mode: string;
};

export type PlayerInfo = {
  username: string;
  status: "online" | "offline";
  hostId?: string;
  cellId?: string;
  worldX?: number;
  worldY?: number;
  lastLogin?: string;
};

export type CommitEvent = {
  commitId: string;
  scenario: string;
  step: string;
  stepIndex: number;
  success: boolean;
  durationMs: number;
  affected: string[] | null;
  hostIds: string[] | null;
  error?: string;
  seqNo: number;
  kind: string;
  timestamp: string;
};

export type ClusterInfo = {
  now: string;
  hostCount: number;
  gatewayCount: number;
  cellCount: number;
  sessionCount: number;
  totalEntities: number;
  recentEvents: CommitEvent[] | null;
};

export type PerfSnapshot = {
  cellId: string;
  systemNames: string[] | null;
  systems: TimingStats[] | null;
  total: TimingStats;
  sampleCount: number;
};

export type TimingStats = {
  latestUs: number;
  avgUs: number;
  p50Us: number;
  p95Us: number;
  p99Us: number;
  maxUs: number;
};

export type PanelDef = {
  id: string;
  label: string;
  icon: string;
  group: string;
  topics: string[] | null;
  commands: string[] | null;
  initialFetch?: string;
  component?: string;
  visualization?: string;
};

export type AuthSession = {
  user: string;
  grants: string[];
  expiresAt: string;
};
```

- [ ] **Step 2: Verify typecheck**

```bash
cd web-admin
bun run typecheck
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add web-admin/src/lib/types.ts
git commit -m "web-admin: TS mirror of pkg/admin DTOs (camelCase wire format)"
```

---

### Task 5: lib/api.ts — fetch wrappers + error normalization

**Files:**
- Create: `web-admin/src/lib/api.ts`
- Create: `web-admin/src/lib/api.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, it, expect, beforeEach, vi } from "vitest";
import { apiGet, apiPost, ApiError } from "./api";

describe("api", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("apiGet parses JSON response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      ),
    );
    const res = await apiGet<{ ok: boolean }>("/admin/api/cluster");
    expect(res.ok).toBe(true);
  });

  it("apiGet throws ApiError on 401", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ error: "no session" }), {
          status: 401,
          headers: { "content-type": "application/json" },
        }),
      ),
    );
    await expect(apiGet("/admin/api/cluster")).rejects.toMatchObject({
      kind: "rbac",
      status: 401,
    });
  });

  it("apiPost includes body and content-type", async () => {
    const fetchMock = vi.fn(async () =>
      new Response(JSON.stringify({ ok: true }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);
    await apiPost("/admin/api/commands/cell.split", { CellID: "0_0" });
    const [, init] = fetchMock.mock.calls[0];
    expect(init?.method).toBe("POST");
    expect(init?.headers).toMatchObject({ "Content-Type": "application/json" });
    expect(JSON.parse(init?.body as string)).toEqual({ CellID: "0_0" });
  });

  it("network failure surfaces as kind=network", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    await expect(apiGet("/admin/api/cluster")).rejects.toMatchObject({
      kind: "network",
    });
  });
});
```

Run: `cd web-admin && bun run test src/lib/api.test.ts`
Expected: build error / undefined.

- [ ] **Step 2: Implement**

```ts
// Centralized fetch wrappers. Every API call goes through here so error
// shapes are uniform and panels can render consistent toasts/banners.

export type ApiErrorKind =
  | "http"        // generic non-2xx that doesn't fit a more specific kind
  | "network"     // DNS / TLS / dropped — fetch threw
  | "rbac"        // 401 / 403
  | "validation"  // 400 with field errors
  | "cmdsys";     // 409 — cmdsys returned a domain error

export class ApiError extends Error {
  kind: ApiErrorKind;
  status: number;
  fieldErrors?: Record<string, string>;
  raw?: unknown;

  constructor(kind: ApiErrorKind, status: number, message: string, raw?: unknown) {
    super(message);
    this.kind = kind;
    this.status = status;
    this.raw = raw;
  }
}

const COMMON_HEADERS: HeadersInit = {
  "Content-Type": "application/json",
};

async function call<T>(method: string, url: string, body?: unknown): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(url, {
      method,
      credentials: "same-origin",
      headers: body !== undefined ? COMMON_HEADERS : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (e) {
    throw new ApiError("network", 0, (e as Error).message ?? "network error");
  }

  // Try to read JSON body for error detail.
  let payload: unknown;
  const ct = resp.headers.get("content-type") ?? "";
  if (ct.includes("application/json")) {
    try {
      payload = await resp.json();
    } catch {
      payload = undefined;
    }
  }

  if (!resp.ok) {
    const msg =
      (payload as { error?: string } | undefined)?.error ?? `HTTP ${resp.status}`;
    const kind: ApiErrorKind =
      resp.status === 401 || resp.status === 403
        ? "rbac"
        : resp.status === 400
          ? "validation"
          : resp.status === 409
            ? "cmdsys"
            : "http";
    throw new ApiError(kind, resp.status, msg, payload);
  }

  return (payload as T) ?? (undefined as T);
}

export function apiGet<T>(url: string): Promise<T> {
  return call<T>("GET", url);
}

export function apiPost<T = unknown>(url: string, body?: unknown): Promise<T> {
  return call<T>("POST", url, body ?? {});
}

export function apiDelete<T = unknown>(url: string): Promise<T> {
  return call<T>("DELETE", url);
}
```

- [ ] **Step 3: Run + commit**

```bash
cd web-admin
bun run test src/lib/api.test.ts
```

Expected: 4 passing.

```bash
git add web-admin/src/lib/api.ts web-admin/src/lib/api.test.ts
git commit -m "web-admin: lib/api.ts — fetch wrappers with normalized ApiError"
```

---

### Task 6: lib/stream.ts — SSE multiplexer

**Files:**
- Create: `web-admin/src/lib/stream.ts`
- Create: `web-admin/src/lib/stream.test.ts`

- [ ] **Step 1: Test**

```ts
import { describe, it, expect, beforeEach } from "vitest";
import { stream } from "./stream";

describe("stream", () => {
  beforeEach(() => {
    stream.reset();
  });

  it("subscribes to a topic and receives parsed events", async () => {
    const received: unknown[] = [];
    stream.subscribe("cells", (msg) => received.push(msg));
    await Promise.resolve(); // allow EventSource open

    const es = (globalThis as { __lastES?: { emit: (t: string, d: unknown) => void } }).__lastES!;
    es.emit("cells", [{ id: "0_0", load: 0.5 }]);

    await Promise.resolve();
    expect(received).toEqual([[{ id: "0_0", load: 0.5 }]]);
  });

  it("multiplexes multiple topics over one EventSource", async () => {
    const seen: string[] = [];
    stream.subscribe("cells", () => seen.push("cells"));
    stream.subscribe("hosts", () => seen.push("hosts"));
    await Promise.resolve();

    const es = (globalThis as { __lastES?: { emit: (t: string, d: unknown) => void; url: string } }).__lastES!;
    expect(es.url).toContain("topics=");
    expect(es.url).toMatch(/cells/);
    expect(es.url).toMatch(/hosts/);

    es.emit("cells", []);
    es.emit("hosts", []);
    await Promise.resolve();
    expect(seen).toEqual(["cells", "hosts"]);
  });

  it("unsubscribe stops delivery and closes ES when last sub removed", async () => {
    let count = 0;
    const off = stream.subscribe("cells", () => count++);
    await Promise.resolve();
    const es = (globalThis as { __lastES?: { emit: (t: string, d: unknown) => void; readyState: number } }).__lastES!;
    es.emit("cells", []);
    await Promise.resolve();
    expect(count).toBe(1);

    off();
    es.emit("cells", []);
    await Promise.resolve();
    expect(count).toBe(1);
    expect(es.readyState).toBe(2 /* CLOSED */);
  });
});
```

Run: `bun run test src/lib/stream.test.ts` → fail (undefined).

- [ ] **Step 2: Implement**

```ts
// One global multiplexed EventSource. Every panel that wants live data calls
// stream.subscribe(topic, handler); the lib shares one HTTP connection and
// reopens it (with the merged topic set) on subscribe / unsubscribe.

type Handler = (msg: unknown) => void;

const subs = new Map<string, Set<Handler>>();
let es: EventSource | null = null;
let reopenTimer: ReturnType<typeof setTimeout> | null = null;
let backoffMs = 200;

function topicsList(): string[] {
  return [...subs.keys()].filter((t) => (subs.get(t)?.size ?? 0) > 0);
}

function reopen(): void {
  if (es) {
    es.close();
    es = null;
  }
  const topics = topicsList();
  if (topics.length === 0) {
    return;
  }
  const url = `/admin/api/stream?topics=${encodeURIComponent(topics.join(","))}`;
  const next = new EventSource(url);
  es = next;

  // Generic message handler for all named events on this connection.
  // Server sends `event: <topic>\ndata: <json>\n\n` so we attach one
  // listener per known topic; new topics added later trigger another reopen.
  for (const t of topics) {
    next.addEventListener(t, (evt) => {
      let data: unknown;
      try {
        data = JSON.parse((evt as MessageEvent).data);
      } catch {
        return;
      }
      const set = subs.get(t);
      if (!set) return;
      for (const h of set) h(data);
    });
  }

  next.onopen = () => {
    backoffMs = 200; // reset on success
  };
  next.onerror = () => {
    next.close();
    es = null;
    if (topicsList().length === 0) return;
    if (reopenTimer) return;
    const delay = Math.min(backoffMs, 5000) + Math.floor(Math.random() * 100);
    reopenTimer = setTimeout(() => {
      reopenTimer = null;
      backoffMs = Math.min(backoffMs * 2, 5000);
      reopen();
    }, delay);
  };
}

function ensureOpen(): void {
  if (!es && topicsList().length > 0) reopen();
}

export const stream = {
  subscribe(topic: string, handler: Handler): () => void {
    const beforeTopics = topicsList().sort().join(",");
    let set = subs.get(topic);
    if (!set) {
      set = new Set();
      subs.set(topic, set);
    }
    set.add(handler);
    const afterTopics = topicsList().sort().join(",");
    if (beforeTopics !== afterTopics) reopen();
    else ensureOpen();
    return () => {
      const s = subs.get(topic);
      if (!s) return;
      s.delete(handler);
      if (s.size === 0) {
        subs.delete(topic);
        const remaining = topicsList();
        if (remaining.length === 0) {
          es?.close();
          es = null;
        } else {
          reopen();
        }
      }
    };
  },

  // Test-only: close the stream and clear subscriptions.
  reset(): void {
    es?.close();
    es = null;
    subs.clear();
    if (reopenTimer) {
      clearTimeout(reopenTimer);
      reopenTimer = null;
    }
    backoffMs = 200;
  },
};
```

- [ ] **Step 3: Run + commit**

```bash
bun run test src/lib/stream.test.ts
```

Expected: 3 passing.

```bash
git add web-admin/src/lib/stream.ts web-admin/src/lib/stream.test.ts
git commit -m "web-admin: lib/stream.ts — multiplexed SSE with auto-reconnect"
```

---

### Task 7: lib/auth.ts

**Files:**
- Create: `web-admin/src/lib/auth.ts`
- Create: `web-admin/src/lib/auth.test.ts`

- [ ] **Step 1: Test**

```ts
import { describe, it, expect, beforeEach, vi } from "vitest";
import { auth } from "./auth";

describe("auth", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("login posts credentials and returns session", async () => {
    const fetchMock = vi.fn(async () =>
      new Response(
        JSON.stringify({ user: "josh", grants: ["*.*"], expiresAt: "2099-01-01T00:00:00Z" }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const sess = await auth.login("josh", "secret");
    expect(sess.user).toBe("josh");
    const [, init] = fetchMock.mock.calls[0];
    expect(JSON.parse(init?.body as string)).toEqual({ username: "josh", password: "secret" });
  });

  it("session() returns null on 401", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ error: "no session" }), {
          status: 401,
          headers: { "content-type": "application/json" },
        }),
      ),
    );
    expect(await auth.session()).toBeNull();
  });
});
```

- [ ] **Step 2: Implement**

```ts
import { apiPost, ApiError } from "./api";
import type { AuthSession } from "./types";

export const auth = {
  async login(username: string, password: string): Promise<AuthSession> {
    return apiPost<AuthSession>("/admin/api/auth/login", { username, password });
  },

  async logout(): Promise<void> {
    try {
      await apiPost<{ ok: boolean }>("/admin/api/auth/logout");
    } catch (e) {
      if (e instanceof ApiError && e.kind === "rbac") return;
      throw e;
    }
  },

  // session() returns null when no valid session cookie is present, the
  // session itself when one exists. Used at app boot to decide login vs cluster.
  async session(): Promise<AuthSession | null> {
    try {
      return await apiPost<AuthSession>("/admin/api/auth/session", {});
    } catch (e) {
      if (e instanceof ApiError && e.kind === "rbac") return null;
      throw e;
    }
  },
};
```

Note: the backend's `/admin/api/auth/session` is a GET in `pkg/admin/admin.go` (`mux.Handle("/admin/api/auth/session", s.requireSession(http.HandlerFunc(s.handleSession)))`). Use GET, not POST. Adjust:

```ts
async session(): Promise<AuthSession | null> {
  try {
    return await apiGet<AuthSession>("/admin/api/auth/session");
  } catch (e) {
    if (e instanceof ApiError && e.kind === "rbac") return null;
    throw e;
  }
},
```

Add `import { apiGet, apiPost, ApiError } from "./api";` at the top.

- [ ] **Step 3: Run + commit**

```bash
bun run test src/lib/auth.test.ts
```

Expected: 2 passing.

```bash
git add web-admin/src/lib/auth.ts web-admin/src/lib/auth.test.ts
git commit -m "web-admin: lib/auth.ts — login/logout/session"
```

---

### Task 8: lib/format.ts — formatters

**Files:**
- Create: `web-admin/src/lib/format.ts`
- Create: `web-admin/src/lib/format.test.ts`

- [ ] **Step 1: Test**

```ts
import { describe, it, expect } from "vitest";
import { fmtBytes, fmtDuration, fmtLoad, fmtUsAsMs } from "./format";

describe("format", () => {
  it("fmtBytes scales", () => {
    expect(fmtBytes(0)).toBe("0 B");
    expect(fmtBytes(1023)).toBe("1023 B");
    expect(fmtBytes(1024)).toBe("1.00 KB");
    expect(fmtBytes(1024 * 1024 * 5)).toBe("5.00 MB");
  });

  it("fmtDuration", () => {
    expect(fmtDuration(0)).toBe("0ms");
    expect(fmtDuration(800)).toBe("800ms");
    expect(fmtDuration(2_500)).toBe("2.5s");
    expect(fmtDuration(120_000)).toBe("2m");
  });

  it("fmtLoad clamps and percentizes", () => {
    expect(fmtLoad(0)).toBe("0%");
    expect(fmtLoad(0.42)).toBe("42%");
    expect(fmtLoad(1.5)).toBe("150%");
  });

  it("fmtUsAsMs", () => {
    expect(fmtUsAsMs(0)).toBe("0.0ms");
    expect(fmtUsAsMs(1500)).toBe("1.5ms");
    expect(fmtUsAsMs(50_000)).toBe("50.0ms");
  });
});
```

- [ ] **Step 2: Implement**

```ts
export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(2)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(2)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

export function fmtDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1).replace(/\.0$/, "")}s`;
  return `${Math.round(ms / 60_000)}m`;
}

export function fmtLoad(load: number): string {
  return `${Math.round(load * 100)}%`;
}

export function fmtUsAsMs(us: number): string {
  return `${(us / 1000).toFixed(1)}ms`;
}
```

- [ ] **Step 3: Run + commit**

```bash
bun run test src/lib/format.test.ts
```

Expected: 4 passing.

```bash
git add web-admin/src/lib/format.ts web-admin/src/lib/format.test.ts
git commit -m "web-admin: lib/format.ts — bytes/duration/load/µs formatters"
```

---

### Task 9: lib/icons.ts + lib/stores.ts

**Files:**
- Create: `web-admin/src/lib/icons.ts`
- Create: `web-admin/src/lib/stores.ts`

- [ ] **Step 1: icons.ts (lucide subset re-export)**

```ts
// Curated subset of lucide-svelte icons used across the dashboard.
// Importing only what we use keeps the bundle lean.
export {
  Globe,
  Server,
  GitBranch,
  Users,
  Activity,
  List,
  ScrollText as Scroll,
  Settings,
  Search,
  Command,
  Bell,
  ChevronDown,
  ChevronRight,
  X as Close,
  AlertTriangle,
  CheckCircle2,
  Circle,
  Loader2,
} from "lucide-svelte";
```

- [ ] **Step 2: stores.ts — Svelte 5 runes-based stores**

```ts
import type {
  CellInfo,
  HostInfo,
  GatewayInfo,
  ClusterInfo,
  CommitEvent,
  PlayerInfo,
  AuthSession,
  PanelDef,
} from "./types";

// A reactive holder backed by Svelte 5 $state. Use directly: cellsStore.value.
//
// Pattern: each topic owns one store. The Cluster page subscribes via
// stream.subscribe(topic, (data) => cellsStore.set(data)) at mount, and
// unsubscribes via the returned destructor in onDestroy.
class Store<T> {
  #value = $state<T | null>(null);
  get value(): T | null {
    return this.#value;
  }
  set(v: T): void {
    this.#value = v;
  }
  clear(): void {
    this.#value = null;
  }
}

export const sessionStore = new Store<AuthSession>();
export const cellsStore = new Store<CellInfo[]>();
export const hostsStore = new Store<HostInfo[]>();
export const gatewaysStore = new Store<GatewayInfo[]>();
export const playersStore = new Store<PlayerInfo[]>();
export const eventsStore = new Store<CommitEvent[]>();
export const alertsStore = new Store<CommitEvent[]>();
export const clusterStore = new Store<ClusterInfo>();
export const panelsStore = new Store<PanelDef[]>();

// alerts: push-only ring of recent invariant violations / important commits.
export function pushAlert(e: CommitEvent): void {
  const cur = alertsStore.value ?? [];
  const next = [e, ...cur].slice(0, 50);
  alertsStore.set(next);
}
```

- [ ] **Step 3: Verify typecheck**

```bash
cd web-admin && bun run typecheck
```

- [ ] **Step 4: Commit**

```bash
git add web-admin/src/lib/icons.ts web-admin/src/lib/stores.ts
git commit -m "web-admin: lib/icons.ts (lucide subset) + lib/stores.ts (Svelte 5 runes)"
```

---

### Task 10: lib/router.ts — tiny hash router

**Files:**
- Create: `web-admin/src/lib/router.ts`

- [ ] **Step 1: Implement**

```ts
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
```

- [ ] **Step 2: Tiny writable shim (Svelte 5 stores work but this keeps lib agnostic)**

Create `web-admin/src/lib/writable.ts`:

```ts
type Subscriber<T> = (v: T) => void;

export type Readable<T> = {
  subscribe(run: Subscriber<T>): () => void;
};

export type Writable<T> = Readable<T> & {
  set(v: T): void;
  get(): T;
};

export function writable<T>(initial: T): Writable<T> {
  let value = initial;
  const subs = new Set<Subscriber<T>>();
  return {
    subscribe(run) {
      subs.add(run);
      run(value);
      return () => subs.delete(run);
    },
    set(v) {
      if (Object.is(v, value)) return;
      value = v;
      for (const s of subs) s(value);
    },
    get() {
      return value;
    },
  };
}
```

- [ ] **Step 3: Commit**

```bash
git add web-admin/src/lib/router.ts web-admin/src/lib/writable.ts
git commit -m "web-admin: hash router + writable shim"
```

---

### Task 11: components/Sidebar.svelte

**Files:**
- Create: `web-admin/src/components/Sidebar.svelte`

- [ ] **Step 1: Implement**

```svelte
<script lang="ts">
  import { Globe, Server, GitBranch, Users, Activity, List, Scroll, Settings } from "$lib/icons";
  import { navigate, route } from "$lib/router";

  type Item = {
    id: string;
    label: string;
    icon: typeof Globe;
    group: string;
    path: string;
  };

  const items: Item[] = [
    { id: "cluster", label: "Cells", icon: Globe, group: "CLUSTER", path: "/cluster" },
    { id: "hosts", label: "Hosts", icon: Server, group: "CLUSTER", path: "/hosts" },
    { id: "gateways", label: "Gateways", icon: GitBranch, group: "CLUSTER", path: "/gateways" },
    { id: "players", label: "Players", icon: Users, group: "PEOPLE", path: "/players" },
    { id: "performance", label: "Performance", icon: Activity, group: "DIAGNOSE", path: "/performance" },
    { id: "events", label: "Events", icon: List, group: "DIAGNOSE", path: "/events" },
    { id: "logs", label: "Logs", icon: Scroll, group: "DIAGNOSE", path: "/logs" },
    { id: "settings", label: "Settings", icon: Settings, group: "CONFIG", path: "/settings" },
  ];

  let currentPath = $state("/cluster");
  $effect(() => {
    const off = route.subscribe((p) => (currentPath = p));
    return off;
  });

  // Group items by group, preserving order.
  const groups: { name: string; items: Item[] }[] = [];
  for (const it of items) {
    const last = groups[groups.length - 1];
    if (last && last.name === it.group) last.items.push(it);
    else groups.push({ name: it.group, items: [it] });
  }
</script>

<aside
  class="w-44 shrink-0 bg-[#0a0e14] border-r border-white/5 py-3 text-[12px] flex flex-col"
>
  <div class="px-4 pb-3 font-semibold text-accent-300 tracking-wide text-[13px]">
    mmokit ops
  </div>
  {#each groups as g (g.name)}
    <div class="px-4 pt-2 pb-1 text-[10px] text-slate-500 tracking-[0.08em]">
      {g.name}
    </div>
    {#each g.items as it (it.id)}
      {@const active = currentPath === it.path || currentPath.startsWith(it.path + "/")}
      <button
        type="button"
        class="flex items-center gap-2 px-4 py-1.5 text-left {active
          ? 'bg-accent-300/10 text-accent-300 border-l-2 border-accent-300 pl-[14px]'
          : 'text-slate-300 hover:bg-white/5'}"
        onclick={() => navigate(it.path)}
      >
        <it.icon class="w-3.5 h-3.5" />
        <span>{it.label}</span>
      </button>
    {/each}
  {/each}
  <div class="grow"></div>
</aside>
```

- [ ] **Step 2: Commit**

```bash
git add web-admin/src/components/Sidebar.svelte
git commit -m "web-admin: Sidebar component with grouped sections + active highlight"
```

---

### Task 12: components/TopBar.svelte + AlertBanner.svelte

**Files:**
- Create: `web-admin/src/components/TopBar.svelte`
- Create: `web-admin/src/components/AlertBanner.svelte`

- [ ] **Step 1: AlertBanner.svelte**

```svelte
<script lang="ts">
  import { alertsStore } from "$lib/stores";
  import { AlertTriangle, Close } from "$lib/icons";

  let dismissed = $state(new Set<string>());

  function key(seqNo: number, commitId: string): string {
    return `${commitId}#${seqNo}`;
  }

  let visible = $derived(
    (alertsStore.value ?? [])
      .filter((a) => !dismissed.has(key(a.seqNo, a.commitId)))
      .slice(0, 1),
  );
</script>

{#each visible as a (key(a.seqNo, a.commitId))}
  <div class="flex items-center gap-2 bg-rose-900/30 border border-rose-700/40 text-rose-200 px-3 py-1.5 rounded text-[11.5px]">
    <AlertTriangle class="w-3.5 h-3.5 shrink-0" />
    <span class="grow">
      {a.scenario}/{a.step} — {a.error || "invariant violation"}
    </span>
    <button
      type="button"
      class="opacity-60 hover:opacity-100"
      aria-label="Dismiss"
      onclick={() => {
        dismissed = new Set([...dismissed, key(a.seqNo, a.commitId)]);
      }}
    >
      <Close class="w-3 h-3" />
    </button>
  </div>
{/each}
```

- [ ] **Step 2: TopBar.svelte**

```svelte
<script lang="ts">
  import { Circle, Command, Bell } from "$lib/icons";
  import { clusterStore, alertsStore, sessionStore } from "$lib/stores";
  import AlertBanner from "./AlertBanner.svelte";

  let cluster = $derived(clusterStore.value);
  let alertCount = $derived((alertsStore.value ?? []).length);
  let user = $derived(sessionStore.value?.user ?? "");

  // Health: green if cluster snapshot recent and no alerts; yellow if stale; red if alerts.
  let health = $derived(alertCount > 0 ? "warn" : cluster ? "ok" : "stale");
</script>

<header class="border-b border-white/5 bg-[#0a0e14] flex items-center gap-4 px-4 py-2 text-[12px]">
  <div class="text-slate-400">
    Cluster &rsaquo; <span class="text-slate-100 font-semibold">Cells</span>
  </div>
  <div class="grow flex items-center justify-center gap-3">
    <AlertBanner />
  </div>
  <div class="flex items-center gap-3 text-slate-400">
    <span class="flex items-center gap-1.5">
      <Circle
        class="w-2 h-2 fill-current {health === 'ok' ? 'text-emerald-500' : health === 'warn' ? 'text-amber-400' : 'text-slate-600'}"
      />
      {cluster
        ? `${cluster.cellCount} cells · ${cluster.totalEntities} ent · ${cluster.sessionCount} sess`
        : "loading…"}
    </span>
    <button
      type="button"
      class="px-2 py-0.5 rounded border border-white/10 bg-white/5 text-slate-300 hover:bg-white/10 flex items-center gap-1"
      title="Command palette (⌘K)"
    >
      <Command class="w-3 h-3" /> ⌘K
    </button>
    <span class="relative inline-flex items-center text-rose-300" title="Active alerts">
      <Bell class="w-3.5 h-3.5" />
      {#if alertCount > 0}
        <span class="ml-1 text-[10px] font-semibold">{alertCount}</span>
      {/if}
    </span>
    {#if user}
      <span class="text-slate-300">{user}</span>
    {/if}
  </div>
</header>
```

- [ ] **Step 3: Commit**

```bash
git add web-admin/src/components/TopBar.svelte web-admin/src/components/AlertBanner.svelte
git commit -m "web-admin: TopBar + AlertBanner components"
```

---

### Task 13: app.svelte shell + auth gate + svelte $lib alias

**Files:**
- Modify: `web-admin/vite.config.ts` (add `$lib` alias)
- Modify: `web-admin/tsconfig.json` (path mapping)
- Modify: `web-admin/src/app.svelte`

The auth gate (login redirect) is embedded directly in `app.svelte` — no separate route file. Login renders when `sessionStore.value === null`.

- [ ] **Step 1: Add `$lib` alias to vite.config.ts**

Update `web-admin/vite.config.ts`:

```ts
import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { resolve } from "node:path";

export default defineConfig({
  plugins: [svelte()],
  resolve: {
    alias: {
      $lib: resolve(__dirname, "src/lib"),
    },
  },
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
```

- [ ] **Step 2: tsconfig.json paths**

Add to `compilerOptions`:

```json
{
  "compilerOptions": {
    "baseUrl": ".",
    "paths": {
      "$lib/*": ["src/lib/*"]
    }
  }
}
```

- [ ] **Step 3: Replace src/app.svelte**

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { auth } from "$lib/auth";
  import { sessionStore, clusterStore } from "$lib/stores";
  import { route, navigate } from "$lib/router";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import type { ClusterInfo } from "$lib/types";
  import Login from "./routes/login.svelte";
  import Cluster from "./routes/cluster.svelte";
  import Sidebar from "./components/Sidebar.svelte";
  import TopBar from "./components/TopBar.svelte";

  let path = $state("/cluster");
  let booting = $state(true);
  let loggedIn = $derived(sessionStore.value !== null);

  $effect(() => {
    const off = route.subscribe((p) => (path = p));
    return off;
  });

  onMount(async () => {
    const s = await auth.session();
    sessionStore.set(s as any); // null falls through; the Store accepts assignment
    if (s) {
      hydrateCluster();
    }
    booting = false;
  });

  async function hydrateCluster() {
    try {
      const c = await apiGet<ClusterInfo>("/admin/api/cluster");
      clusterStore.set(c);
    } catch {
      // 401 etc. — auth gate will redirect.
    }
    // Subscribe to topics: cells (per-cell snapshots), alerts (invariant violations).
    stream.subscribe("cells", () => {
      // CellMap reads cellsStore directly — see Task 16.
    });
  }

  async function onLogin() {
    const s = await auth.session();
    sessionStore.set(s as any);
    if (s) hydrateCluster();
  }
</script>

{#if booting}
  <div class="h-full flex items-center justify-center text-slate-400">loading…</div>
{:else if !loggedIn}
  <Login onLoggedIn={onLogin} />
{:else}
  <div class="h-full flex">
    <Sidebar />
    <div class="grow flex flex-col min-w-0">
      <TopBar />
      <div class="grow overflow-auto">
        {#if path === "/cluster"}
          <Cluster />
        {:else}
          <div class="p-8 text-slate-500">Panel <code>{path}</code> — not yet implemented.</div>
        {/if}
      </div>
    </div>
  </div>
{/if}
```

NOTE: `Store.set(null)` isn't allowed by the typed Store API in `lib/stores.ts`. Cast workaround. A cleaner alternative — make `Store.set` accept `T | null`:

Update `lib/stores.ts` `Store.set` to:

```ts
set(v: T | null): void {
  this.#value = v;
}
```

(Drop the `as any` casts above once fixed.)

- [ ] **Step 4: Smoke**

```bash
cd web-admin && bun run typecheck
```

Errors expected: `Login` and `Cluster` components don't exist yet (Tasks 14, 15+). Continue — those land in the next two tasks. To unblock the typecheck temporarily, add stub files:

```bash
echo '<script>let onLoggedIn: () => void; export { onLoggedIn };</script><div></div>' > web-admin/src/routes/login.svelte
echo '<div></div>' > web-admin/src/routes/cluster.svelte
```

(Tasks 14, 16 replace these.)

- [ ] **Step 5: Commit**

```bash
git add web-admin/vite.config.ts web-admin/tsconfig.json web-admin/src/app.svelte \
  web-admin/src/routes/login.svelte web-admin/src/routes/cluster.svelte \
  web-admin/src/lib/stores.ts
git commit -m "web-admin: app shell with auth gate + sidebar + topbar + route stubs"
```

---

### Task 14: routes/login.svelte

**Files:**
- Modify: `web-admin/src/routes/login.svelte`

- [ ] **Step 1: Implement**

```svelte
<script lang="ts">
  import { auth } from "$lib/auth";
  import { ApiError } from "$lib/api";
  import { Loader2 } from "$lib/icons";

  type Props = { onLoggedIn: () => void };
  let { onLoggedIn }: Props = $props();

  let username = $state("");
  let password = $state("");
  let submitting = $state(false);
  let error = $state("");

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (submitting) return;
    submitting = true;
    error = "";
    try {
      await auth.login(username.trim(), password);
      onLoggedIn();
    } catch (e) {
      if (e instanceof ApiError) {
        error =
          e.kind === "rbac"
            ? "Invalid credentials."
            : e.kind === "network"
              ? "Could not reach the server."
              : e.message;
      } else {
        error = (e as Error).message;
      }
    } finally {
      submitting = false;
    }
  }
</script>

<main class="h-full flex items-center justify-center bg-[#0a0e14]">
  <form
    class="w-[320px] bg-[#0d1117] border border-white/8 rounded-lg p-6 shadow-xl"
    onsubmit={submit}
  >
    <h1 class="text-accent-300 text-lg font-semibold mb-1">mmokit admin</h1>
    <p class="text-slate-500 text-[12px] mb-4">Sign in with operator credentials.</p>

    <label class="block text-[11px] text-slate-400 uppercase tracking-wide mb-1" for="username">
      Username
    </label>
    <input
      id="username"
      type="text"
      autocomplete="username"
      class="w-full bg-black/40 border border-white/10 rounded px-2 py-1.5 mb-3 text-slate-200 focus:outline-none focus:border-accent-300/50"
      bind:value={username}
      required
    />

    <label class="block text-[11px] text-slate-400 uppercase tracking-wide mb-1" for="password">
      Password
    </label>
    <input
      id="password"
      type="password"
      autocomplete="current-password"
      class="w-full bg-black/40 border border-white/10 rounded px-2 py-1.5 mb-4 text-slate-200 focus:outline-none focus:border-accent-300/50"
      bind:value={password}
      required
    />

    {#if error}
      <div class="text-rose-300 text-[12px] mb-3">{error}</div>
    {/if}

    <button
      type="submit"
      class="w-full bg-accent-400 hover:bg-accent-500 text-slate-950 font-semibold py-1.5 rounded flex items-center justify-center gap-2 disabled:opacity-50"
      disabled={submitting}
    >
      {#if submitting}<Loader2 class="w-3.5 h-3.5 animate-spin" />{/if}
      Sign in
    </button>
  </form>
</main>
```

- [ ] **Step 2: Commit**

```bash
git add web-admin/src/routes/login.svelte
git commit -m "web-admin: login route"
```

---

### Task 15: components/CellMap.svelte — canvas hero

**Files:**
- Create: `web-admin/src/components/CellMap.svelte`
- Create: `web-admin/src/components/CellMap.test.ts`

- [ ] **Step 1: Test the layout math (the renderer is canvas; we test the geometry helper)**

```ts
import { describe, it, expect } from "vitest";
import { layoutCells, type Layout } from "./cellmap-layout";

describe("layoutCells", () => {
  it("places depth-0 cells in a grid", () => {
    const cells = [
      { id: "0_0", depth: 0 },
      { id: "1_0", depth: 0 },
      { id: "0_1", depth: 0 },
      { id: "1_1", depth: 0 },
    ];
    const out = layoutCells(cells, { width: 400, height: 400, padding: 0 });
    expect(out.length).toBe(4);
    // The 4 cells should evenly tile a 2x2 grid.
    const sizes = new Set(out.map((c) => Math.round(c.w)));
    expect(sizes.size).toBe(1);
  });

  it("nests split children inside their parent rect", () => {
    const cells = [
      { id: "0_0", depth: 0 },
      { id: "0_0:1", depth: 1, parent: "0_0" },
      { id: "0_0:2", depth: 1, parent: "0_0" },
      { id: "0_0:3", depth: 1, parent: "0_0" },
      { id: "0_0:4", depth: 1, parent: "0_0" },
    ];
    const out = layoutCells(cells, { width: 200, height: 200, padding: 0 });
    const parent = out.find((c) => c.id === "0_0")!;
    const children = out.filter((c) => c.id.startsWith("0_0:"));
    for (const ch of children) {
      expect(ch.x).toBeGreaterThanOrEqual(parent.x);
      expect(ch.y).toBeGreaterThanOrEqual(parent.y);
      expect(ch.x + ch.w).toBeLessThanOrEqual(parent.x + parent.w + 0.01);
      expect(ch.y + ch.h).toBeLessThanOrEqual(parent.y + parent.h + 0.01);
    }
  });
});
```

- [ ] **Step 2: Implement layout helper**

Create `web-admin/src/components/cellmap-layout.ts`:

```ts
export type CellLayoutInput = {
  id: string;
  depth: number;
  parent?: string;
};

export type Layout = {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  depth: number;
};

export type LayoutOpts = {
  width: number;
  height: number;
  padding: number; // outer padding in pixels
};

// layoutCells lays cells out as a quadtree-aware 2D grid. Depth-0 cells form
// an N×M grid derived from their X_Y IDs; split children render as nested
// 2×2 squares inside their parent rect.
//
// Cell IDs follow `<X>_<Y>` for depth 0 and `<X>_<Y>:1..4` for splits, where
// split index 1=NW, 2=NE, 3=SW, 4=SE. The same scheme nests recursively for
// deeper splits (`0_0:1:2` is the NE quadrant of the NW quadrant of 0_0).
//
// Layout returns one entry per input cell (parents AND children) so callers
// can choose to render only leaves, only parents, or both with translucency.
export function layoutCells(input: CellLayoutInput[], opts: LayoutOpts): Layout[] {
  // 1. Compute base grid extent from depth-0 IDs.
  const baseCells = input.filter((c) => c.depth === 0);
  let maxX = 0;
  let maxY = 0;
  for (const c of baseCells) {
    const [xs, ys] = c.id.split("_");
    maxX = Math.max(maxX, Number.parseInt(xs, 10));
    maxY = Math.max(maxY, Number.parseInt(ys, 10));
  }
  const cols = maxX + 1;
  const rows = maxY + 1;
  const cellW = (opts.width - 2 * opts.padding) / cols;
  const cellH = (opts.height - 2 * opts.padding) / rows;

  const out: Layout[] = [];

  // 2. Place depth-0 cells.
  const baseRect = new Map<string, Layout>();
  for (const c of baseCells) {
    const [xs, ys] = c.id.split("_");
    const xi = Number.parseInt(xs, 10);
    const yi = Number.parseInt(ys, 10);
    const rect: Layout = {
      id: c.id,
      x: opts.padding + xi * cellW,
      y: opts.padding + yi * cellH,
      w: cellW,
      h: cellH,
      depth: 0,
    };
    out.push(rect);
    baseRect.set(c.id, rect);
  }

  // 3. Place split children. Order by depth ascending so each split's parent
  //    is already placed by the time we descend.
  const splits = input.filter((c) => c.depth > 0).sort((a, b) => a.depth - b.depth);
  const placed = new Map<string, Layout>(baseRect);
  for (const c of splits) {
    const colonIdx = c.id.lastIndexOf(":");
    if (colonIdx < 0) continue;
    const parentId = c.id.slice(0, colonIdx);
    const slot = Number.parseInt(c.id.slice(colonIdx + 1), 10);
    const parent = placed.get(parentId);
    if (!parent) continue;
    const halfW = parent.w / 2;
    const halfH = parent.h / 2;
    let dx = 0;
    let dy = 0;
    switch (slot) {
      case 1: dx = 0; dy = 0; break;        // NW
      case 2: dx = halfW; dy = 0; break;     // NE
      case 3: dx = 0; dy = halfH; break;     // SW
      case 4: dx = halfW; dy = halfH; break; // SE
    }
    const rect: Layout = {
      id: c.id,
      x: parent.x + dx,
      y: parent.y + dy,
      w: halfW,
      h: halfH,
      depth: c.depth,
    };
    out.push(rect);
    placed.set(c.id, rect);
  }

  return out;
}
```

- [ ] **Step 3: Implement CellMap.svelte (canvas renderer)**

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { cellsStore } from "$lib/stores";
  import { stream } from "$lib/stream";
  import { layoutCells, type Layout } from "./cellmap-layout";
  import type { CellInfo } from "$lib/types";

  type Props = {
    onSelect?: (id: string | null) => void;
    selected?: string | null;
    colorMode?: "load" | "host" | "entities";
  };
  let { onSelect, selected, colorMode = "load" }: Props = $props();

  let canvas: HTMLCanvasElement;
  let container: HTMLDivElement;
  let layouts = $state<Layout[]>([]);
  let cells = $state<CellInfo[]>([]);
  let hover = $state<string | null>(null);
  let tooltip = $state<{ x: number; y: number; cell: CellInfo } | null>(null);

  $effect(() => {
    const off = stream.subscribe("cells", (data) => {
      cells = data as CellInfo[];
      cellsStore.set(cells);
      requestAnimationFrame(redraw);
    });
    return off;
  });

  onMount(() => {
    const ro = new ResizeObserver(redraw);
    ro.observe(container);
    redraw();
    return () => ro.disconnect();
  });

  function loadColor(load: number): string {
    // 0 → green, 1 → red, with yellow mid.
    const stops = [
      [0.0, "#22c55e"],
      [0.5, "#84cc16"],
      [0.75, "#facc15"],
      [1.0, "#ef4444"],
    ] as const;
    for (let i = 1; i < stops.length; i++) {
      if (load <= (stops[i][0] as number)) return stops[i][1];
    }
    return stops[stops.length - 1][1];
  }

  function hostColor(hostId: string): string {
    // Stable categorical hue from a tiny hash.
    let h = 0;
    for (let i = 0; i < hostId.length; i++) {
      h = (h * 31 + hostId.charCodeAt(i)) | 0;
    }
    const hue = ((h % 360) + 360) % 360;
    return `hsl(${hue} 65% 50%)`;
  }

  function entityColor(real: number): string {
    const t = Math.min(1, real / 200);
    const g = Math.round(255 * t);
    return `rgb(${100 + g / 2}, ${130 + g / 2}, 255)`;
  }

  function colorOf(c: CellInfo): string {
    switch (colorMode) {
      case "host": return hostColor(c.hostId || "?");
      case "entities": return entityColor(c.entities.real);
      default: return loadColor(c.load);
    }
  }

  function redraw() {
    if (!canvas || !container) return;
    const dpr = window.devicePixelRatio || 1;
    const rect = container.getBoundingClientRect();
    const w = Math.max(100, Math.floor(rect.width));
    const h = Math.max(100, Math.floor(rect.height));
    canvas.width = w * dpr;
    canvas.height = h * dpr;
    canvas.style.width = `${w}px`;
    canvas.style.height = `${h}px`;
    const ctx = canvas.getContext("2d")!;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);

    const layoutInput = cells.map((c) => ({ id: c.id, depth: c.depth, parent: c.parent }));
    layouts = layoutCells(layoutInput, { width: w, height: h, padding: 12 });

    const byId = new Map(cells.map((c) => [c.id, c]));
    // Sort so deeper cells render last (on top).
    const ordered = [...layouts].sort((a, b) => a.depth - b.depth);

    for (const L of ordered) {
      const cell = byId.get(L.id);
      if (!cell) continue;

      // Don't fill parents that are split — their children cover the area.
      const hasChildren = layouts.some((c) => c.id.startsWith(L.id + ":"));
      if (!hasChildren) {
        ctx.fillStyle = colorOf(cell);
        ctx.fillRect(L.x + 1, L.y + 1, L.w - 2, L.h - 2);
      }

      // Border.
      ctx.strokeStyle = L.id === selected
        ? "rgba(125,211,252,0.95)"
        : L.id === hover
          ? "rgba(255,255,255,0.4)"
          : "rgba(0,0,0,0.3)";
      ctx.lineWidth = L.id === selected ? 2 : 1;
      ctx.strokeRect(L.x + 0.5, L.y + 0.5, L.w - 1, L.h - 1);
    }
  }

  function pickAt(px: number, py: number): Layout | null {
    // Pick the deepest cell whose rect contains the point.
    let best: Layout | null = null;
    for (const L of layouts) {
      if (px >= L.x && px <= L.x + L.w && py >= L.y && py <= L.y + L.h) {
        if (!best || L.depth > best.depth) best = L;
      }
    }
    return best;
  }

  function onMouseMove(e: MouseEvent) {
    const rect = canvas.getBoundingClientRect();
    const L = pickAt(e.clientX - rect.left, e.clientY - rect.top);
    const id = L?.id ?? null;
    if (id !== hover) {
      hover = id;
      requestAnimationFrame(redraw);
    }
    if (L) {
      const cell = cells.find((c) => c.id === L.id);
      tooltip = cell ? { x: e.clientX - rect.left + 10, y: e.clientY - rect.top + 10, cell } : null;
    } else {
      tooltip = null;
    }
  }

  function onClick(e: MouseEvent) {
    const rect = canvas.getBoundingClientRect();
    const L = pickAt(e.clientX - rect.left, e.clientY - rect.top);
    onSelect?.(L?.id ?? null);
  }

  function onLeave() {
    hover = null;
    tooltip = null;
    requestAnimationFrame(redraw);
  }
</script>

<div bind:this={container} class="relative w-full h-full">
  <canvas
    bind:this={canvas}
    onmousemove={onMouseMove}
    onmouseleave={onLeave}
    onclick={onClick}
  ></canvas>
  {#if tooltip}
    <div
      class="absolute pointer-events-none bg-[#0d1117]/95 border border-white/10 rounded px-2 py-1 text-[10.5px] text-slate-200 shadow-xl"
      style="left: {tooltip.x}px; top: {tooltip.y}px"
    >
      <div class="font-semibold text-accent-300">{tooltip.cell.id}</div>
      <div>load {Math.round(tooltip.cell.load * 100)}% · {tooltip.cell.entities.real} ent</div>
      <div class="text-slate-400">host {tooltip.cell.hostId}</div>
    </div>
  {/if}
</div>
```

- [ ] **Step 4: Run + commit**

```bash
cd web-admin && bun run test src/components/CellMap.test.ts
```

Expected: 2 passing.

```bash
git add web-admin/src/components/cellmap-layout.ts web-admin/src/components/CellMap.svelte \
  web-admin/src/components/CellMap.test.ts
git commit -m "web-admin: CellMap canvas renderer + quadtree layout helper"
```

---

### Task 16: components/CellDrawer.svelte

**Files:**
- Create: `web-admin/src/components/CellDrawer.svelte`

- [ ] **Step 1: Implement**

```svelte
<script lang="ts">
  import type { CellInfo } from "$lib/types";
  import { fmtBytes, fmtUsAsMs, fmtLoad } from "$lib/format";
  import { apiPost } from "$lib/api";
  import { ApiError } from "$lib/api";
  import { Close } from "$lib/icons";

  type Props = {
    cell: CellInfo | null;
    onClose: () => void;
    onAction?: (action: string, ok: boolean, message: string) => void;
  };
  let { cell, onClose, onAction }: Props = $props();

  let busy = $state(false);

  async function invoke(verb: string, args: Record<string, unknown>) {
    if (!cell || busy) return;
    busy = true;
    try {
      await apiPost(`/admin/api/commands/${verb}`, args);
      onAction?.(verb, true, `${verb} ok`);
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      onAction?.(verb, false, msg);
    } finally {
      busy = false;
    }
  }
</script>

{#if cell}
  <aside class="w-72 shrink-0 bg-[#0a0e14] border-l border-white/8 p-4 overflow-y-auto">
    <div class="flex items-center justify-between mb-3">
      <div class="text-accent-300 font-mono">{cell.id}</div>
      <button class="text-slate-500 hover:text-slate-200" aria-label="Close" onclick={onClose}>
        <Close class="w-4 h-4" />
      </button>
    </div>

    <dl class="text-[12px] space-y-1.5 mb-4">
      <div class="flex justify-between"><dt class="text-slate-500">Host</dt><dd>{cell.hostId || "—"}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Depth</dt><dd>{cell.depth}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Load</dt><dd>{fmtLoad(cell.load)}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Tick p99</dt><dd>{fmtUsAsMs(cell.tickP99Us)}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Tick p95</dt><dd>{fmtUsAsMs(cell.tickP95Us)}</dd></div>
    </dl>

    <div class="text-[10.5px] uppercase text-slate-500 tracking-wide mb-1.5">Entities</div>
    <dl class="text-[12px] space-y-1.5 mb-4">
      <div class="flex justify-between"><dt class="text-slate-500">Real</dt><dd>{cell.entities.real}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Replica</dt><dd>{cell.entities.replica}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Ghost</dt><dd>{cell.entities.ghost}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Sessions</dt><dd>{cell.entities.connected}</dd></div>
    </dl>

    <div class="text-[10.5px] uppercase text-slate-500 tracking-wide mb-1.5">Bytes (cumulative)</div>
    <dl class="text-[12px] space-y-1.5 mb-4">
      <div class="flex justify-between"><dt class="text-slate-500">Sent</dt><dd>{fmtBytes(cell.bytes.sent)}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Recv</dt><dd>{fmtBytes(cell.bytes.recv)}</dd></div>
    </dl>

    {#if cell.neighbors && cell.neighbors.length > 0}
      <div class="text-[10.5px] uppercase text-slate-500 tracking-wide mb-1.5">Neighbors</div>
      <div class="text-[12px] text-slate-400 mb-4 font-mono">{cell.neighbors.join(", ")}</div>
    {/if}

    <div class="text-[10.5px] uppercase text-slate-500 tracking-wide mb-1.5">Actions</div>
    <div class="flex gap-2 flex-wrap">
      <button
        class="px-2 py-1 text-[11.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
        disabled={busy}
        onclick={() => invoke("cell.split", { CellID: cell.id })}
      >
        split
      </button>
      <button
        class="px-2 py-1 text-[11.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
        disabled={busy}
        onclick={() => invoke("cell.merge", { CellID: cell.id })}
      >
        merge
      </button>
      <button
        class="px-2 py-1 text-[11.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
        disabled={busy}
        onclick={() => {
          const target = window.prompt("Target hostID for migrate:");
          if (target) invoke("cell.migrate", { CellID: cell.id, HostID: target });
        }}
      >
        migrate
      </button>
    </div>
  </aside>
{/if}
```

- [ ] **Step 2: Commit**

```bash
git add web-admin/src/components/CellDrawer.svelte
git commit -m "web-admin: CellDrawer with cell details + split/merge/migrate actions"
```

---

### Task 17: routes/cluster.svelte

**Files:**
- Modify: `web-admin/src/routes/cluster.svelte`

- [ ] **Step 1: Implement**

```svelte
<script lang="ts">
  import { cellsStore } from "$lib/stores";
  import CellMap from "../components/CellMap.svelte";
  import CellDrawer from "../components/CellDrawer.svelte";
  import type { CellInfo } from "$lib/types";

  let selected = $state<string | null>(null);
  let colorMode = $state<"load" | "host" | "entities">("load");
  let toast = $state<{ msg: string; ok: boolean } | null>(null);

  let cells = $derived<CellInfo[]>(cellsStore.value ?? []);
  let selectedCell = $derived<CellInfo | null>(
    selected ? (cells.find((c) => c.id === selected) ?? null) : null,
  );
</script>

<div class="h-full flex">
  <main class="grow flex flex-col p-4 gap-3 min-w-0">
    <div class="flex items-center justify-between">
      <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Live cell map</h2>
      <div class="flex items-center gap-2 text-[11px] text-slate-400">
        <span>color:</span>
        <div class="flex bg-white/5 border border-white/10 rounded overflow-hidden">
          {#each ["load", "host", "entities"] as m (m)}
            <button
              class="px-2 py-0.5 {colorMode === m ? 'bg-accent-300/20 text-accent-300' : 'hover:bg-white/5'}"
              onclick={() => (colorMode = m as typeof colorMode)}
            >{m}</button>
          {/each}
        </div>
      </div>
    </div>

    <div class="grow rounded-lg border border-accent-300/15 bg-gradient-to-br from-[#0f1a2e] to-[#0a1119] overflow-hidden min-h-[300px]">
      <CellMap
        {selected}
        {colorMode}
        onSelect={(id) => (selected = id)}
      />
    </div>

    {#if toast}
      <div
        class="text-[12px] px-3 py-1.5 rounded {toast.ok ? 'bg-emerald-900/30 text-emerald-200 border border-emerald-700/40' : 'bg-rose-900/30 text-rose-200 border border-rose-700/40'}"
      >
        {toast.msg}
      </div>
    {/if}
  </main>

  <CellDrawer
    cell={selectedCell}
    onClose={() => (selected = null)}
    onAction={(_verb, ok, msg) => {
      toast = { ok, msg };
      setTimeout(() => (toast = null), 4000);
    }}
  />
</div>
```

- [ ] **Step 2: Commit**

```bash
git add web-admin/src/routes/cluster.svelte
git commit -m "web-admin: Cluster route with CellMap hero + CellDrawer + color-mode toggle"
```

---

### Task 18: End-to-end smoke

**Files:**
- (No new files; this is a manual verification step)

- [ ] **Step 1: Build everything**

```bash
cd .
just admin-build
just build
```

Expected: `web-admin/` builds clean; `bin/server` is produced; `pkg/admin/static/dist/` contains the built SPA.

- [ ] **Step 2: Generate operator credentials**

```bash
echo 'localdev' | ./bin/server --admin-hash-password
```

Expected: a single-line output starting with `$argon2id$v=19$…`. Copy this hash.

- [ ] **Step 3: Configure 4node-basic**

In `examples/4node-basic/main.go`, find the `mmokit.Config{...}` construction and ADD:

```go
Admin: universe.AdminConfig{
    Enabled:            true,
    SessionTTL:         8 * time.Hour,
    LockoutMaxAttempts: 5,
    LockoutWindow:      15 * time.Minute,
    Operators: []universe.AdminOperatorConfig{
        {
            Username:     "josh",
            PasswordHash: "<paste hash from Step 2>",
            Grants:       []string{"*.*"},
        },
    },
    ServerFactory: mmokit.DefaultAdminServerFactory(),
},
```

If `mmokit.Config{}` is the existing wrapper type, locate the equivalent universe.Config field. The `ServerFactory` line is the load-bearing part — without it the admin section is a no-op even when `Enabled=true`.

- [ ] **Step 4: Run with admin enabled**

```bash
cd examples/4node-basic
just build
./bin/server --admin-listen=:9101 --admin-enabled
```

- [ ] **Step 5: Open the dashboard**

Visit `http://localhost:9101/admin/`. Expect:
1. Login page renders.
2. Login with `josh` / `localdev` succeeds and redirects to the Cluster page.
3. Sidebar shows the 8 sections (Cluster, Hosts, Gateways, Players, Performance, Events, Logs, Settings).
4. The 2x2 grid of cells appears in the hero, color-coded by load (mostly green at idle).
5. Hover a cell → tooltip shows ID + load + entity count.
6. Click a cell → drawer slides in with cell details + split/merge/migrate buttons.
7. Click "split" on cell 0_0 → toast appears confirming success; the map re-renders with 4 nested sub-cells inside 0_0 within ~1 second.
8. Click "color: host" → cells recolor by owning host.

If any step fails, debug and fix before declaring this task done.

- [ ] **Step 6: Commit any 4node-basic config changes**

```bash
git add examples/4node-basic/main.go
git commit -m "4node-basic: enable admin dashboard for local dev"
```

---

### Task 19: CLAUDE.md update

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add a section under Admin dashboard**

Find the existing **Admin dashboard** paragraph (added by the foundation plan) and APPEND:

```markdown

The SPA lives in `web-admin/` (Svelte 5 + Vite + Bun + Tailwind v4). `bun run build` outputs directly into `pkg/admin/static/dist/` so the binary's `//go:embed` picks it up. Local dev: `just admin-dev` runs Vite on `:5173` proxying API calls to a backend at `:9101`. CI/release: `just admin-build` regenerates the bundle; wired into the top-level `just build`. The Cluster page is canvas-rendered (CellMap.svelte) with quadtree-aware nesting; click a cell → CellDrawer with split/merge/migrate actions. Live updates flow through one multiplexed SSE connection at `/admin/api/stream`.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "CLAUDE.md: document web-admin/ frontend wiring"
```

---

## Self-review checklist

- [ ] **Spec coverage:**
  - §5.1 Build pipeline → Tasks 1, 2 (Vite outDir → embed location)
  - §5.2 Reactivity (Svelte 5 runes) → Task 9 (stores), enabled in Task 1's svelte.config.js
  - §5.3 Cell map (canvas, quadtree-aware, color-mode toggle) → Tasks 15, 17
  - §5.4 PanelHost (generic renderer for game panels) → DEFERRED to a follow-up plan along with the other panel routes
  - §6 Wire API → Tasks 5 (api), 6 (stream), 7 (auth) cover login, session, stream, cluster, commands, events
  - §8 Phase 1 panels — ONLY #1 (Cell map), #2 (Cell drawer), #4 (host coloring), #10 (cluster ops actions in drawer), #21 (alert banner stub), #25 (⌘K palette button — non-functional placeholder). Tasks 11, 12, 15-17 cover them. Panels #6, #8, #11, #12, #16, #17, #20, #28 are deferred to subsequent plans.
- [ ] **Placeholder scan:** No "TODO" / "implement later" / "fill in details" in any step. Every code block is complete.
- [ ] **Type consistency:** `Layout` from `cellmap-layout.ts` is used in `CellMap.svelte` (Task 15); `CellInfo` matches the Go DTO from `pkg/admin/view.go` per Task 4.

---

## Execution

Plan complete. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session using `executing-plans`, batch with checkpoints.

Which approach?

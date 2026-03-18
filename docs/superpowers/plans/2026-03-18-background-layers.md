# Background Layers Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add nebula, planet, and enhanced star layers to the web client background, all procedurally generated with parallax scrolling and subtle animation.

**Architecture:** Three separate classes (`Nebula`, `Planets`, enhanced `Starfield`) each own their own `Container` added to `starfieldContainer` in back-to-front order. All graphics are pre-drawn at construction; `update()` only touches `position`, `visible`, and `alpha`. Two new star layers extend the existing `layerConfigs` array.

**Tech Stack:** PixiJS 8.12, TypeScript, Vite dev server (`make dev`)

**Spec:** `docs/superpowers/specs/2026-03-18-background-layers-design.md`

---

## File Map

| Action | File | Purpose |
|--------|------|---------|
| Create | `web-pixi/src/world/nebula.ts` | Nebula class — 2 clouds/tile with color blobs + subtle wisps, breathing animation |
| Create | `web-pixi/src/world/planets.ts` | Planets class — 4–6 planets/tile with atmosphere glow, cloud bands, optional rings, pulse animation |
| Modify | `web-pixi/src/world/starfield.ts` | Prepend ultra-distant layer + append foreground bright layer |
| Modify | `web-pixi/src/main.ts` | Instantiate Nebula and Planets before Starfield; call their update() in game loop |

---

## Task 1: Create `nebula.ts`

**Files:**
- Create: `web-pixi/src/world/nebula.ts`

### How it works

Two nebula clouds per 8000-unit tile. Each cloud is a `Container` holding:
- 3–4 overlapping `Graphics` ellipses (concentric, decreasing alpha) to simulate a radial gradient color blob
- One `Graphics` with 4–6 bezier wisp strokes at very low alpha (0.03–0.06)

The outer `Nebula` container holds all cloud containers. In `update()`, each cloud container is tiled/culled the same way stars are, and `cloudContainer.alpha` is modulated by a sine breath formula.

Mulberry32 seeded with `99999` (different from starfield's `12345`).

- [ ] **Step 1: Create the file with interfaces and RNG**

`web-pixi/src/world/nebula.ts`:
```typescript
import { Container, Graphics } from "pixi.js";

function mulberry32(a: number): () => number {
  return function () {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

interface NebulaCloud {
  x: number;
  y: number;
  container: Container;
  baseAlpha: number;
  breatheSpeed: number;
  breatheOffset: number;
}

const NEBULA_PALETTE = [0xff2266, 0x8822ff, 0x2266ff, 0x00ccff];

export class Nebula {
  private clouds: NebulaCloud[] = [];
  private outerContainer: Container;
  private readonly tileSize = 8000;
  private readonly parallax = 0.006;

  constructor(parent: Container) {
    this.outerContainer = new Container();
    parent.addChild(this.outerContainer);

    const rng = mulberry32(99999);
    const count = 2;

    for (let i = 0; i < count; i++) {
      const container = new Container();
      this.outerContainer.addChild(container);

      // --- Color blobs: 3–4 overlapping ellipses ---
      const blobGfx = new Graphics();
      const numBlobs = 3 + Math.floor(rng() * 2); // 3 or 4
      const color = NEBULA_PALETTE[Math.floor(rng() * NEBULA_PALETTE.length)];
      const rx = 280 + rng() * 320; // 280–600
      const ry = rx * (0.55 + rng() * 0.35); // aspect ratio

      const blobAlphas = [0.17, 0.12, 0.08, 0.05];
      const blobScales = [0.35, 0.55, 0.75, 1.0];
      for (let b = 0; b < numBlobs; b++) {
        blobGfx
          .ellipse(0, 0, rx * blobScales[b], ry * blobScales[b])
          .fill({ color, alpha: blobAlphas[b] });
      }
      container.addChild(blobGfx);

      // --- Wisps: 4–6 bezier strokes at very low alpha ---
      const wispGfx = new Graphics();
      const numWisps = 4 + Math.floor(rng() * 3); // 4–6
      for (let w = 0; w < numWisps; w++) {
        const wispAlpha = 0.03 + rng() * 0.03; // 0.03–0.06
        const wispWidth = 12 + rng() * 13;     // 12–25
        const spread = rx * 0.9;
        const x0 = (rng() - 0.5) * spread * 2;
        const y0 = (rng() - 0.5) * ry * 1.5;
        const x3 = (rng() - 0.5) * spread * 2;
        const y3 = (rng() - 0.5) * ry * 1.5;
        const cx1 = (rng() - 0.5) * spread * 1.5;
        const cy1 = (rng() - 0.5) * ry;
        const cx2 = (rng() - 0.5) * spread * 1.5;
        const cy2 = (rng() - 0.5) * ry;
        wispGfx
          .moveTo(x0, y0)
          .bezierCurveTo(cx1, cy1, cx2, cy2, x3, y3)
          .stroke({ color, alpha: wispAlpha, width: wispWidth, cap: "round" });
      }
      container.addChild(wispGfx);

      const baseAlpha = 0.75 + rng() * 0.25;
      container.alpha = baseAlpha;

      this.clouds.push({
        x: rng() * this.tileSize,
        y: rng() * this.tileSize,
        container,
        baseAlpha,
        breatheSpeed: 0.04 + rng() * 0.04,
        breatheOffset: rng() * Math.PI * 2,
      });
    }
  }

  update(cameraX: number, cameraY: number, screenW: number, screenH: number, now: number): void {
    const offX = (cameraX * this.parallax) % this.tileSize;
    const offY = (cameraY * this.parallax) % this.tileSize;
    const cullMargin = 600;

    this.outerContainer.position.set(cameraX - screenW / 2, cameraY - screenH / 2);

    for (const cloud of this.clouds) {
      const sx = ((cloud.x - offX) % this.tileSize + this.tileSize) % this.tileSize;
      const sy = ((cloud.y - offY) % this.tileSize + this.tileSize) % this.tileSize;

      if (sx > screenW + cullMargin || sy > screenH + cullMargin) {
        cloud.container.visible = false;
        continue;
      }

      cloud.container.visible = true;
      cloud.container.position.set(sx, sy);
      const breath = 0.85 + 0.15 * Math.sin(now * 0.001 * cloud.breatheSpeed + cloud.breatheOffset);
      cloud.container.alpha = cloud.baseAlpha * breath;
    }
  }

  destroy(): void {
    this.outerContainer.destroy({ children: true });
  }
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd web-pixi && npx tsc --noEmit
```
Expected: no errors related to `nebula.ts`.

- [ ] **Step 3: Commit**

```bash
cd .
git add web-pixi/src/world/nebula.ts
git commit -m "feat(web): add Nebula class with parallax color blobs and subtle wisps"
```

---

## Task 2: Create `planets.ts`

**Files:**
- Create: `web-pixi/src/world/planets.ts`

### How it works

4–6 planets per 6000-unit tile. Each planet is a `Container` with layered `Graphics` children:
1. Atmosphere `Graphics` (concentric circles for glow halo) — animated
2. Body `Graphics` (filled circle + highlight + cloud bands + outline)
3. Ring `Graphics` (optional, 40% chance — two ellipse strokes)

Only the atmosphere `Graphics` alpha is updated per frame (pulse). Position + visible updated via standard tiling/culling.

Mulberry32 seeded with `77777`.

- [ ] **Step 1: Create the file**

`web-pixi/src/world/planets.ts`:
```typescript
import { Container, Graphics } from "pixi.js";

function mulberry32(a: number): () => number {
  return function () {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

interface Planet {
  x: number;
  y: number;
  container: Container;
  atmoGfx: Graphics;
  pulseSpeed: number;
  pulseOffset: number;
}

const BODY_PALETTE  = [0x0d1f3c, 0x1a0a30, 0x081a10, 0x251408, 0x101028];
const ACCENT_PALETTE = [0x3388ff, 0xaa66ff, 0x44cc88, 0xff8844, 0x88aaff];

export class Planets {
  private planets: Planet[] = [];
  private outerContainer: Container;
  private readonly tileSize = 6000;
  private readonly parallax = 0.015;

  constructor(parent: Container) {
    this.outerContainer = new Container();
    parent.addChild(this.outerContainer);

    const rng = mulberry32(77777);
    const count = 4 + Math.floor(rng() * 3); // 4–6

    for (let i = 0; i < count; i++) {
      const container = new Container();
      this.outerContainer.addChild(container);

      const r = 35 + rng() * 50; // 35–85
      const bodyColor  = BODY_PALETTE[Math.floor(rng() * BODY_PALETTE.length)];
      const accentColor = ACCENT_PALETTE[Math.floor(rng() * ACCENT_PALETTE.length)];
      const hasRings = rng() < 0.4;

      // 1. Atmosphere glow (4 concentric circles, outermost first)
      const atmoGfx = new Graphics();
      const atmoRadii  = [r * 1.35, r * 1.2, r * 1.1, r * 1.05];
      const atmoAlphas = [0.04,     0.07,    0.10,    0.12];
      for (let a = 0; a < 4; a++) {
        atmoGfx.circle(0, 0, atmoRadii[a]).fill({ color: accentColor, alpha: atmoAlphas[a] });
      }
      container.addChild(atmoGfx);

      // 2. Body (fill + highlight + cloud bands + outline) — one Graphics object
      const bodyGfx = new Graphics();

      // Body fill
      bodyGfx.circle(0, 0, r).fill({ color: bodyColor });

      // Lit highlight (upper-left offset circle)
      const hlR = r * 0.55;
      const hlOffset = r * 0.28;
      // Slightly lighter tint: add 0x111111 to bodyColor channels approximately
      const hlColor = Math.min(bodyColor + 0x151515, 0xffffff);
      bodyGfx.circle(-hlOffset, -hlOffset, hlR).fill({ color: hlColor, alpha: 0.12 });

      // Cloud bands (2–3 thin ellipses across the disk)
      const numBands = 2 + Math.floor(rng() * 2);
      for (let b = 0; b < numBands; b++) {
        const bandY = -r * 0.6 + (b + 1) * (r * 1.2 / (numBands + 1));
        const bandH = r * (0.12 + rng() * 0.1);
        // Clip to disk bounds via alpha — no mask needed for this subtle effect
        bodyGfx.ellipse(0, bandY, r, bandH).fill({ color: accentColor, alpha: 0.07 + rng() * 0.04 });
      }

      // Body outline
      bodyGfx.circle(0, 0, r).stroke({ color: accentColor, alpha: 0.4, width: 1 });
      container.addChild(bodyGfx);

      // 3. Rings (optional)
      if (hasRings) {
        const ringGfx = new Graphics();
        ringGfx
          .ellipse(0, 0, r * 1.35, r * 0.22)
          .stroke({ color: accentColor, alpha: 0.35, width: 1.5 });
        ringGfx
          .ellipse(0, 0, r * 1.55, r * 0.26)
          .stroke({ color: accentColor, alpha: 0.2, width: 1 });
        container.addChild(ringGfx);
      }

      this.planets.push({
        x: rng() * this.tileSize,
        y: rng() * this.tileSize,
        container,
        atmoGfx,
        pulseSpeed: 0.06 + rng() * 0.06,
        pulseOffset: rng() * Math.PI * 2,
      });
    }
  }

  update(cameraX: number, cameraY: number, screenW: number, screenH: number, now: number): void {
    const offX = (cameraX * this.parallax) % this.tileSize;
    const offY = (cameraY * this.parallax) % this.tileSize;
    const cullMargin = 185; // max planet radius (85) + 100

    this.outerContainer.position.set(cameraX - screenW / 2, cameraY - screenH / 2);

    for (const planet of this.planets) {
      const sx = ((planet.x - offX) % this.tileSize + this.tileSize) % this.tileSize;
      const sy = ((planet.y - offY) % this.tileSize + this.tileSize) % this.tileSize;

      if (sx > screenW + cullMargin || sy > screenH + cullMargin) {
        planet.container.visible = false;
        continue;
      }

      planet.container.visible = true;
      planet.container.position.set(sx, sy);
      const pulse = 0.8 + 0.2 * Math.sin(now * 0.001 * planet.pulseSpeed + planet.pulseOffset);
      planet.atmoGfx.alpha = pulse;
    }
  }

  destroy(): void {
    this.outerContainer.destroy({ children: true });
  }
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd web-pixi && npx tsc --noEmit
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
cd .
git add web-pixi/src/world/planets.ts
git commit -m "feat(web): add Planets class with atmosphere glow, cloud bands, and optional rings"
```

---

## Task 3: Enhance `starfield.ts` with 2 new star layers

**Files:**
- Modify: `web-pixi/src/world/starfield.ts`

Add one layer config at the start of the array (ultra-distant) and one at the end (very-close foreground). The single shared RNG naturally produces diverging positions for these layers since they consume different RNG values.

- [ ] **Step 1: Update `layerConfigs` in the constructor**

In `web-pixi/src/world/starfield.ts`, replace the `layerConfigs` array:

```typescript
// Before:
const layerConfigs = [
  { count: 200, parallax: 0.05, sizeMin: 0.5, sizeMax: 1.0, alphaMin: 0.15, alphaMax: 0.35 },
  { count: 120, parallax: 0.15, sizeMin: 0.8, sizeMax: 1.5, alphaMin: 0.25, alphaMax: 0.5 },
  { count: 60,  parallax: 0.3,  sizeMin: 1.0, sizeMax: 2.0, alphaMin: 0.4,  alphaMax: 0.7 },
];

// After:
const layerConfigs = [
  { count: 300, parallax: 0.02, sizeMin: 0.3, sizeMax: 0.7, alphaMin: 0.08, alphaMax: 0.22 },
  { count: 200, parallax: 0.05, sizeMin: 0.5, sizeMax: 1.0, alphaMin: 0.15, alphaMax: 0.35 },
  { count: 120, parallax: 0.15, sizeMin: 0.8, sizeMax: 1.5, alphaMin: 0.25, alphaMax: 0.5 },
  { count: 60,  parallax: 0.3,  sizeMin: 1.0, sizeMax: 2.0, alphaMin: 0.4,  alphaMax: 0.7 },
  { count: 25,  parallax: 0.5,  sizeMin: 1.5, sizeMax: 3.0, alphaMin: 0.5,  alphaMax: 0.9 },
];
```

No other changes to `starfield.ts` — the constructor loop and `update()` are already generic.

- [ ] **Step 2: Verify it compiles**

```bash
cd web-pixi && npx tsc --noEmit
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
cd .
git add web-pixi/src/world/starfield.ts
git commit -m "feat(web): add ultra-distant and foreground star layers to starfield"
```

---

## Task 4: Wire everything into `main.ts`

**Files:**
- Modify: `web-pixi/src/main.ts`

Three small changes:
1. Import `Nebula` and `Planets`
2. Instantiate them **before** `new Starfield(...)` so their containers sit below the star layers in z-order
3. Call their `update()` in the game loop alongside `starfield.update()`

- [ ] **Step 1: Add imports**

At the top of `web-pixi/src/main.ts`, after the existing `Starfield` import:

```typescript
// existing:
import { Starfield } from "./world/starfield";
// add:
import { Nebula } from "./world/nebula";
import { Planets } from "./world/planets";
```

- [ ] **Step 2: Instantiate before Starfield**

Find the `// Starfield` comment block in `main.ts`:

```typescript
// Before:
// Starfield
const starfield = new Starfield(starfieldContainer);

// After:
// Background layers (order = z-order: nebula furthest back, then planets, then stars)
const nebula = new Nebula(starfieldContainer);
const planets = new Planets(starfieldContainer);
const starfield = new Starfield(starfieldContainer);
```

- [ ] **Step 3: Add update calls in the game loop**

Find where `starfield.update(...)` is called in the game loop and add the two new calls immediately before it:

```typescript
// add before existing starfield.update:
nebula.update(camX, camY, app.screen.width, app.screen.height, now);
planets.update(camX, camY, app.screen.width, app.screen.height, now);
// existing:
starfield.update(camX, camY, app.screen.width, app.screen.height, now);
```

> Note: Check the exact variable names used in the existing `starfield.update(...)` call and match them exactly. They may be `camera.x / camera.y` or extracted locals — use whatever the existing call uses.

- [ ] **Step 4: Verify it compiles**

```bash
cd web-pixi && npx tsc --noEmit
```
Expected: no errors.

- [ ] **Step 5: Run and visually verify**

```bash
cd . && make dev
```

Open `http://localhost:8080`, log in, and confirm:

- [ ] Nebula color blobs visible as faint background clouds (magenta/violet/blue/cyan hues)
- [ ] Nebula wisps are barely visible — very subtle streaks at near-zero alpha
- [ ] Nebula clouds slowly breathe (opacity oscillates gently over ~15s)
- [ ] 2–5 planets visible with atmospheric glow halos
- [ ] Some planets have ellipse rings
- [ ] Planet atmosphere glow slowly pulses
- [ ] Star field is visibly richer — more micro-stars in the distance, occasional bright foreground star
- [ ] All layers scroll at different speeds as ship moves (parallax confirmed)
- [ ] Planets appear behind all stars (no stars visible "beneath" a planet)
- [ ] No visual pop-in as ship flies in any direction

- [ ] **Step 6: Commit**

```bash
cd .
git add web-pixi/src/main.ts
git commit -m "feat(web): wire nebula and planets into main scene, add to game loop"
```

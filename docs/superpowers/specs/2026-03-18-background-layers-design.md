# Background Layers Design

**Date:** 2026-03-18
**Status:** Approved

## Context

The web client's background currently has 3 parallax star layers (200/120/60 stars at parallax 0.05/0.15/0.3) on a plain black background. The goal is to add visual atmosphere: more star depth, background planets, and nebula cloud layers — all procedurally generated (no sprites) in the existing neon-on-black vector art style.

## Approach

Separate files matching the existing `Starfield` pattern. Each new class accepts a parent `Container`, adds its own sub-container(s), and exposes an `update(cameraX, cameraY, screenW, screenH, now)` method. All three are instantiated and updated in `main.ts`.

Z-order is controlled by add-order to `starfieldContainer`: Nebula first (furthest back), Planets second, then the enhanced Starfield on top.

---

## Layer Stack (back → front in `starfieldContainer`)

| # | Layer | File | Parallax | Tile | Description |
|---|-------|------|----------|------|-------------|
| 1 | Nebula | `nebula.ts` | 0.006 | 8000 | 2 clouds/tile, blobs + subtle wisps |
| 2 | Planets | `planets.ts` | 0.015 | 6000 | 4–6 planets/tile, glow + optional rings |
| 3 | Stars — ultra-distant **[new]** | `starfield.ts` | 0.02 | 4000 | 300 micro-stars, 0.3–0.7px, very faint |
| 4 | Stars — far | `starfield.ts` | 0.05 | 4000 | 200 stars (existing) |
| 5 | Stars — mid | `starfield.ts` | 0.15 | 4000 | 120 stars (existing) |
| 6 | Stars — close | `starfield.ts` | 0.3 | 4000 | 60 stars (existing) |
| 7 | Stars — foreground **[new]** | `starfield.ts` | 0.5 | 4000 | 25 bright stars, 1.5–3px, fast twinkle |

---

## Components

### 1. `web-pixi/src/world/nebula.ts` — new file

**Structure:** `Nebula` class mirrors `Starfield`. One container added to parent.

**Per cloud (2 clouds per tile):**
- **Blobs:** 3–4 overlapping `Graphics` ellipses with decreasing alpha (simulated radial gradient). Colors seeded from palette `[#ff2266, #8822ff, #2266ff, #00ccff]`. Radii 250–600 units. Alpha 0.06–0.18 on outermost, 0.18–0.35 on inner.
- **Wisps:** 4–6 bezier-curve strokes per cloud drawn into a single `Graphics` object. Width 12–25 units, alpha 0.03–0.06 (very subtle). Use `gfx.moveTo(...).bezierCurveTo(...).stroke({color, alpha, width, cap:'round'})`.
- **Animation:** Each cloud has a `breatheSpeed` (0.04–0.08 rad/s) and `breatheOffset`. Each cloud's blobs and wisps are children of a single `Container` (`cloudContainer`). In `update()`: `cloudContainer.alpha = baseAlpha * (0.85 + 0.15 * sin(now * 0.001 * breatheSpeed + offset))` — so blobs and wisps breathe together.

**RNG:** Mulberry32 seeded with `99999` (different from star seed `12345`).

**Tiling/culling:** Same pattern as `Starfield` — `offX/offY = (cameraX * parallax) % tileSize`, position each cloud, cull if outside viewport + 600px margin (clouds are large).

---

### 2. `web-pixi/src/world/planets.ts` — new file

**Structure:** `Planets` class mirrors `Starfield`. One container added to parent.

**Per planet (4–6 per tile):**

Drawn once at construction into a `Container` with layered `Graphics` children:

1. **Atmosphere glow:** 4 concentric `circle` fills at radii `r*1.35`, `r*1.2`, `r*1.1`, `r*1.05` with alpha `0.04`, `0.07`, `0.1`, `0.12`. Color = planet accent color.
2. **Body:** Filled `circle` at radius `r`. Dark tinted fill (seeded from palette: `[#0d1f3c, #1a0a30, #081a10, #251408, #101028]`).
3. **Lit highlight:** Small offset filled `circle` at `r*0.55`, positioned upper-left, alpha 0.1. Color = slightly lighter tint of body.
4. **Cloud bands:** 2–3 `ellipse` fills, width `r*2`, height `r*0.15–0.25`, y-offset spread across the disk, alpha 0.06–0.1. Same accent color.
5. **Body outline:** Thin `circle` stroke (1px), accent color, alpha 0.4.
6. **Rings (40% chance):** Two `ellipse` strokes centered at planet: inner `rx=r*1.35, ry=r*0.22`, outer `rx=r*1.55, ry=r*0.26`. Stroke width 1.5/1px, alpha 0.35/0.2.

Planet radii: 35–85 units. Accent color palette: `[#3388ff, #aa66ff, #44cc88, #ff8844, #88aaff]`.

**Animation:** Each planet stores `pulseSpeed` (0.06–0.12) and `pulseOffset`. In `update()`, the atmosphere `Graphics` object (child 0) has its alpha modulated: `atmoGfx.alpha = 1.0 * (0.8 + 0.2 * sin(now * 0.001 * pulseSpeed + offset))`.

**RNG:** Mulberry32 seeded with `77777`.

**Tiling/culling:** Same pattern. Cull margin = 185px (max planet radius 85 + 100) applied uniformly to all planets regardless of their individual radius.

---

### 3. `web-pixi/src/world/starfield.ts` — enhanced

Add 2 new layer configs to the `layerConfigs` array (prepend ultra-distant, append foreground):

```ts
// Prepend:
{ count: 300, parallax: 0.02, sizeMin: 0.3, sizeMax: 0.7, alphaMin: 0.08, alphaMax: 0.22 },

// Existing 3 layers unchanged

// Append:
{ count: 25, parallax: 0.5, sizeMin: 1.5, sizeMax: 3.0, alphaMin: 0.5, alphaMax: 0.9 },
```

The existing code uses a single shared Mulberry32 RNG (`seed=12345`) advanced across all layers. Keep that approach — simply prepending the new ultra-distant layer and appending the foreground layer to `layerConfigs` will naturally advance the RNG state enough that star positions diverge from the existing layers.

---

### 4. `web-pixi/src/main.ts` — small update

Instantiation order (before existing `new Starfield(...)`):
```ts
import { Nebula } from "./world/nebula";
import { Planets } from "./world/planets";

const nebula = new Nebula(starfieldContainer);
const planets = new Planets(starfieldContainer);
const starfield = new Starfield(starfieldContainer); // existing
```

In the game loop update, alongside the existing `starfield.update(...)` call:
```ts
nebula.update(camX, camY, screenW, screenH, now);
planets.update(camX, camY, screenW, screenH, now);
starfield.update(camX, camY, screenW, screenH, now);
```

Both classes expose a `destroy()` method (same pattern as `Starfield`).

---

## Data Flow

```
main.ts game loop
  → nebula.update(camX, camY, w, h, now)
      → per cloud: compute tiled position, cull, set alpha via breathe formula
  → planets.update(camX, camY, w, h, now)
      → per planet: compute tiled position, cull, set atmoGfx.alpha via pulse formula
  → starfield.update(camX, camY, w, h, now)  [existing, unchanged behavior]
      → per star: compute tiled position, cull, set alpha via twinkle formula
```

All drawing happens once at construction. Update loop only touches `position`, `visible`, and `alpha` — no Graphics redraws.

---

## Error Handling / Edge Cases

- Culling margin must be larger than the object's visual radius to prevent pop-in. Nebula: +600px. Planets: +planet radius + 100px.
- RNG seeds must differ between classes (stars=12345, nebula=99999, planets=77777) to prevent tile patterns aligning.
- `destroy()` must call `container.destroy({ children: true })` to free GPU memory.

---

## Verification

1. Run `make dev` — server and Vite dev client start at `localhost:8080`
2. Log in and move the ship around the map
3. Confirm:
   - Nebula clouds visible in the background with very faint colour, no distracting motion
   - 2–4 planets visible at various distances/sizes with atmospheric glow; rings visible on some
   - Star field visibly richer (more depth layers, occasional bright foreground star)
   - All layers scroll at different parallax rates as ship moves
   - Planets appear behind all stars (due to add-order)
   - No visual pop-in as ship moves
4. Confirm destroy works: disconnect and reconnect without memory leak (no duplicate containers)

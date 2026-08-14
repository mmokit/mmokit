import { Container, Graphics, Text } from "pixi.js";
import type { DungeonEntity } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// ──────────────────────────────────────────────────────────────────────────────
// Dungeon silhouette — "Sublime Threat" aesthetic.
//
// A wound torn open in the void: jagged basalt shell with molten amber
// cracks bleeding light from deep within. Atmospheric haze, drifting
// mote particles inside, and pulsing portal-vortex entrance markers.
// The interior is translucent so the player ship + walls + NPCs remain
// readable after flying inside.
// ──────────────────────────────────────────────────────────────────────────────

const RING_SEGMENTS = 16;

// Color palette — committed once here so future tuning happens in one place.
const COL_AURA = 0xff5523; // outer heat haze
const COL_SHELL_DARK = 0x120819; // deep basalt shadow
const COL_SHELL_RIM = 0x4a2240; // weathered violet edge
const COL_CRACK_HOT = 0xffaa55; // bright molten core
const COL_CRACK_DIM = 0xb04a18; // older crack, cooler
const COL_CORE_GLOW = 0xff5533; // central deep-energy bloom
const COL_MOTE = 0xd9a070; // drifting interior dust
const COL_VORTEX_HOT = 0xffeeaa;
const COL_VORTEX_RING = 0xff8833;
const COL_VORTEX_BLEED = 0xff5522;

// Deterministic pseudo-random in [0,1) seeded by i so all clients see
// the same crack/crater pattern.
function seedRand(i: number): number {
  const s = Math.sin(i * 12.9898) * 43758.5453;
  return s - Math.floor(s);
}

function entrancePositionsFromMask(
  mask: number,
  radius: number,
): Array<{ x: number; y: number; angle: number }> {
  const arcLen = (2 * Math.PI) / RING_SEGMENTS;
  const out: Array<{ x: number; y: number; angle: number }> = [];
  for (let s = 0; s < RING_SEGMENTS; s++) {
    if ((mask & (1 << s)) === 0) {
      const angle = arcLen * s + arcLen / 2;
      out.push({
        x: radius * Math.cos(angle),
        y: radius * Math.sin(angle),
        angle,
      });
    }
  }
  return out;
}

export function createDungeonDisplay(): EntityDisplayObject {
  const container = new Container();
  // Underneath every other entity so the silhouette frames the scene
  // instead of obscuring ships/walls/NPCs (set in conjunction with
  // entityContainer.sortableChildren = true in main.ts).
  container.zIndex = -10;

  // Layer order: aura → shell → cracks → ambient core → motes →
  // entrance vortexes → labels. Each layer is its own Graphics so we
  // can mutate one without re-clearing the others.
  const aura = new Graphics();
  const shell = new Graphics();
  const cracks = new Graphics();
  const coreGlow = new Graphics();
  const motesGfx = new Graphics();
  const entrances = new Graphics();
  container.addChild(aura, shell, cracks, coreGlow, motesGfx, entrances);

  // Floating interior dust motes. Positions + drift vectors are stable
  // for the life of the entity; alpha and offset animate from the
  // update() ticker. Pre-allocated array keeps the hot path
  // alloc-free.
  type Mote = { ox: number; oy: number; vx: number; vy: number; phase: number };
  const motes: Mote[] = [];

  const label = new Text({
    text: "DUNGEON",
    style: {
      fontFamily: '"Cinzel", "Times New Roman", serif',
      fontSize: 22,
      fontWeight: "700",
      fill: 0xffd9a0,
      letterSpacing: 6,
      dropShadow: {
        color: 0xff5522,
        alpha: 0.85,
        blur: 8,
        distance: 0,
      },
    },
  });
  label.anchor.set(0.5, 1);
  label.scale.set(px(1), px(1));
  container.addChild(label);

  const sublabel = new Text({
    text: "▸ BREACH POINT ◂",
    style: {
      fontFamily: '"Major Mono Display", "Courier New", monospace',
      fontSize: 11,
      fill: 0xc88060,
      letterSpacing: 4,
    },
  });
  sublabel.anchor.set(0.5, 1);
  sublabel.scale.set(px(1), px(1));
  sublabel.alpha = 0.6;
  container.addChild(sublabel);

  let drawnRadius = 0;
  let drawnMask = -1;
  let drawnName = "";

  // ───────── drawAura: large diffuse warm halo around the asteroid ─────────
  // Suggests heat / energy radiating from the structure. Multiple
  // concentric rings of low-alpha fill approximate a radial gradient.
  function drawAura(radius: number) {
    aura.clear();
    const layers = 8;
    for (let i = 0; i < layers; i++) {
      const t = 1 - i / layers; // 1 at innermost, 0 at outer
      const r = radius * (1 + 0.5 * (i / (layers - 1)));
      const alpha = 0.06 * t * t; // quadratic falloff
      aura.circle(0, 0, r).fill({ color: COL_AURA, alpha });
    }
  }

  // ───────── drawShell: jagged basalt outline with rim highlight ─────────
  function drawShell(radius: number) {
    shell.clear();
    const sides = 48;
    const points: number[] = [];
    for (let i = 0; i < sides; i++) {
      const angle = (i / sides) * Math.PI * 2;
      // Sharper, more angular jitter than the smooth-sin version —
      // pseudo-random gives a real rocky outline rather than a wavy
      // blob. Range ~ [0.85, 1.05] of the outer radius.
      const j = 0.85 + seedRand(i * 7 + 13) * 0.2;
      const r = radius * j;
      points.push(Math.cos(angle) * r, Math.sin(angle) * r);
    }
    // Translucent fill so interior content stays readable; rim stroke
    // is opaque + warm so the silhouette is still clearly outlined.
    shell
      .poly(points)
      .fill({ color: COL_SHELL_DARK, alpha: 0.55 })
      .stroke({ color: COL_SHELL_RIM, width: px(2.5), alpha: 0.95 });

    // Inner shadow ring — gives the silhouette some depth at the rim.
    shell
      .circle(0, 0, radius * 0.92)
      .stroke({ color: 0x2a1228, width: px(1), alpha: 0.4 });
  }

  // ───────── drawCracks: molten-amber veins arcing across the shell ─────────
  // Generates a fixed number of deterministic crack paths radiating from
  // the asteroid center toward the rim. Each path is a chain of short
  // segments with branching nubs — gives the rock a "glowing wound"
  // feel without needing per-vertex shading.
  function drawCracks(radius: number) {
    cracks.clear();
    const nCracks = 7;
    for (let i = 0; i < nCracks; i++) {
      const baseAngle = (i / nCracks) * Math.PI * 2 + seedRand(i * 3) * 0.4;
      const startR = radius * (0.12 + seedRand(i * 5 + 1) * 0.08);
      const endR = radius * (0.78 + seedRand(i * 5 + 2) * 0.12);
      const segments = 6;
      let px0 = Math.cos(baseAngle) * startR;
      let py0 = Math.sin(baseAngle) * startR;
      for (let s = 0; s < segments; s++) {
        const t1 = (s + 1) / segments;
        const r = startR + (endR - startR) * t1;
        const wob = (seedRand(i * 11 + s) - 0.5) * 0.35;
        const ang = baseAngle + wob;
        const px1 = Math.cos(ang) * r;
        const py1 = Math.sin(ang) * r;
        // Bright hot core line
        cracks
          .moveTo(px0, py0)
          .lineTo(px1, py1)
          .stroke({ color: COL_CRACK_HOT, width: px(1.6), alpha: 0.95 });
        // Wider, dimmer glow halo around the hot core for the bleed effect
        cracks
          .moveTo(px0, py0)
          .lineTo(px1, py1)
          .stroke({ color: COL_CRACK_DIM, width: px(4), alpha: 0.25 });
        px0 = px1;
        py0 = py1;
      }
      // Small terminal nub at the rim where the crack "vents" outward
      cracks
        .circle(px0, py0, radius * 0.025)
        .fill({ color: COL_CRACK_HOT, alpha: 0.7 });
    }
  }

  // ───────── drawCoreGlow: central bloom suggesting deep energy ─────────
  function drawCoreGlow(radius: number) {
    coreGlow.clear();
    const layers = 6;
    for (let i = 0; i < layers; i++) {
      const t = 1 - i / layers;
      const r = radius * (0.05 + (i / (layers - 1)) * 0.35);
      const alpha = 0.18 * t * t;
      coreGlow.circle(0, 0, r).fill({ color: COL_CORE_GLOW, alpha });
    }
  }

  // ───────── initMotes: drifting dust positions, animated in update() ─────────
  function initMotes(radius: number) {
    motes.length = 0;
    const count = 22;
    for (let i = 0; i < count; i++) {
      // Random angle + radius inside the shell, biased toward middle
      const angle = seedRand(i * 17 + 3) * Math.PI * 2;
      const rr = Math.sqrt(seedRand(i * 17 + 4)) * radius * 0.78;
      motes.push({
        ox: Math.cos(angle) * rr,
        oy: Math.sin(angle) * rr,
        vx: (seedRand(i * 17 + 5) - 0.5) * 0.04,
        vy: (seedRand(i * 17 + 6) - 0.5) * 0.04,
        phase: seedRand(i * 17 + 7) * Math.PI * 2,
      });
    }
  }

  // ───────── drawEntrances: portal-vortex markers at each cave-mouth slot ─
  // Static layers (halos + dim ring); the rotating arcs + bright core are
  // re-drawn each tick from the update() closure for animation.
  function drawEntranceStatic(radius: number, mask: number) {
    entrances.clear();
    const ents = entrancePositionsFromMask(mask, radius);
    for (const e of ents) {
      const eR = radius * 0.13;
      // Soft outer halo — large translucent radial bloom
      entrances
        .circle(e.x, e.y, eR * 1.4)
        .fill({ color: COL_VORTEX_BLEED, alpha: 0.18 });
      entrances
        .circle(e.x, e.y, eR)
        .fill({ color: COL_VORTEX_RING, alpha: 0.28 });
    }
  }

  // ───────── animation: pulsing entrances + drifting motes ─────────
  // Called every frame from update(). Re-renders only the dynamic
  // sub-graphics (motes + entrance vortex animation), keeping the
  // static shell/aura/cracks untouched.
  function drawAnimated(radius: number, mask: number, now: number) {
    // Motes
    motesGfx.clear();
    const t = now * 0.0008;
    for (let i = 0; i < motes.length; i++) {
      const m = motes[i];
      const drift = Math.sin(t + m.phase) * 1.2;
      const x = m.ox + m.vx * now * 0.04 + drift;
      const y = m.oy + m.vy * now * 0.04 + Math.cos(t + m.phase) * 0.6;
      // Wrap motes inside the shell — keep them in [-0.85R, 0.85R] both axes
      const wx = ((((x + radius * 0.85) % (radius * 1.7)) + radius * 1.7) % (radius * 1.7)) - radius * 0.85;
      const wy = ((((y + radius * 0.85) % (radius * 1.7)) + radius * 1.7) % (radius * 1.7)) - radius * 0.85;
      const aa = 0.35 + 0.4 * Math.sin(t * 2 + m.phase);
      motesGfx
        .circle(wx, wy, radius * 0.008)
        .fill({ color: COL_MOTE, alpha: Math.max(0.05, aa) });
    }

    // Entrance dynamic layer — rotating arcs + bright core pulse.
    // We re-clear the entrances Graphics each frame and re-draw static
    // halos + animated arcs so it stays in lock-step. Slightly more
    // expensive but each entrance has <10 ops.
    entrances.clear();
    const ents = entrancePositionsFromMask(mask, radius);
    const pulse = 0.7 + 0.3 * Math.sin(now * 0.0028);
    const rot = now * 0.0012;
    for (let idx = 0; idx < ents.length; idx++) {
      const e = ents[idx];
      const eR = radius * 0.13;
      // Outer bloom halo
      entrances
        .circle(e.x, e.y, eR * 1.5)
        .fill({ color: COL_VORTEX_BLEED, alpha: 0.14 * pulse });
      // Inner halo
      entrances
        .circle(e.x, e.y, eR)
        .fill({ color: COL_VORTEX_RING, alpha: 0.32 * pulse });
      // Three rotating arc sweeps — each offset by 2π/3 + per-entrance offset
      const arcR = eR * 0.7;
      for (let k = 0; k < 3; k++) {
        const start = rot + idx * 1.1 + (k * Math.PI * 2) / 3;
        const end = start + 0.6;
        entrances
          .arc(e.x, e.y, arcR, start, end)
          .stroke({ color: COL_VORTEX_HOT, width: px(1.5), alpha: 0.85 * pulse });
      }
      // Bright vacuum-gate core
      entrances
        .circle(e.x, e.y, eR * 0.18)
        .fill({ color: COL_VORTEX_HOT, alpha: 0.95 });
      // Crisp small ring around the core
      entrances
        .circle(e.x, e.y, eR * 0.28)
        .stroke({ color: COL_VORTEX_HOT, width: px(1), alpha: 0.7 * pulse });
    }
  }

  function drawAll(radius: number, mask: number) {
    drawAura(radius);
    drawShell(radius);
    drawCracks(radius);
    drawCoreGlow(radius);
    initMotes(radius);
    drawEntranceStatic(radius, mask);
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, now: number) {
      const e = ent.current as DungeonEntity;
      const radius = e.radius || 80;
      const mask = e.entranceMask || 0;

      if (radius !== drawnRadius || mask !== drawnMask) {
        drawnRadius = radius;
        drawnMask = mask;
        drawAll(radius, mask);
        // Position labels relative to the asteroid's outer radius.
        // Slight extra clearance for the larger Cinzel + glow shadow.
        label.position.set(0, -radius - px(28));
        sublabel.position.set(0, -radius - px(6));
      }
      if (e.name && e.name !== drawnName) {
        drawnName = e.name;
        label.text = e.name.toUpperCase();
      }

      // Per-frame animation pass.
      drawAnimated(radius, mask, now);
      // Subtle aura breathing — overall container alpha stays 1.0 but
      // the aura layer ebbs to suggest a living energy field.
      aura.alpha = 0.75 + 0.25 * Math.sin(now * 0.0011);
      // Core glow slow pulse — separate phase from aura so they don't
      // beat in sync.
      coreGlow.alpha = 0.85 + 0.15 * Math.sin(now * 0.0007 + 1.3);
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}

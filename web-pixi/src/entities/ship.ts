import { Container, Graphics, Text } from "pixi.js";
import type { ShipEntity } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { getCombat, getShip } from "../entity-accessors";
import { px } from "../view";

type WakeRing = {
  worldX: number;
  worldY: number;
  spawnTime: number;
  angle: number; // velocity direction at spawn (radians)
};
type FieldParticle = {
  worldX: number;
  worldY: number;
  vx: number;
  vy: number;
  spawnTime: number;
};
type Streak = {
  worldX: number;
  worldY: number;
  vx: number;
  vy: number;
  spawnTime: number;
};

const RING_LIFETIME_MS = 1500;
const RING_SPAWN_PERIOD_MS = 150;
const PARTICLE_SPAWN_PERIOD_MS = 30;
const PARTICLE_LIFETIME_MS = 700;
const STREAK_SPAWN_PERIOD_MS = 50;
const STREAK_LIFETIME_MS = 500;

export function createShipDisplay(): EntityDisplayObject {
  const container = new Container();

  // Exhaust layer (behind hull)
  const exhaust = new Graphics();
  container.addChild(exhaust);

  // Hull layer
  const hull = new Graphics();
  container.addChild(hull);

  // Nav lights
  const navLights = new Graphics();
  container.addChild(navLights);

  // Screen-space container (not rotated) for bars and name
  const uiContainer = new Container();
  // We'll position this separately each frame

  // Supercruise VFX layers — line-art, refined-physics visualization. Each
  // effect lives in uiContainer (non-rotating, world-aligned) so the geometry
  // is oriented by the velocity vector explicitly rather than inheriting the
  // hull's rotation. Paint order: world-anchored ripples + drifting field
  // particles first (deepest), then ship-following wake cone + bow wave on
  // top. Bars/name/lockout overlays are added later and sit above all VFX.
  const ripples = new Graphics(); // anisotropic world-anchored ellipses
  const fieldParticles = new Graphics(); // drifting field-particles
  const streaksLayer = new Graphics(); // motion streaks oriented along velocity
  const bowWave = new Graphics(); // compressed arcs in front
  uiContainer.addChild(ripples);
  uiContainer.addChild(fieldParticles);
  uiContainer.addChild(streaksLayer);
  uiContainer.addChild(bowWave);

  // Name tag
  const nameTag = new Text({ text: "", style: { fontFamily: "monospace", fontSize: 10, fill: 0xccddff } });
  nameTag.anchor.set(0.5, 1);
  nameTag.scale.set(px(1), px(1));
  uiContainer.addChild(nameTag);

  // Shield bar background + fill
  const shieldBarBg = new Graphics();
  const shieldBarFill = new Graphics();
  uiContainer.addChild(shieldBarBg);
  uiContainer.addChild(shieldBarFill);

  // HP bar background + fill
  const hpBarBg = new Graphics();
  const hpBarFill = new Graphics();
  uiContainer.addChild(hpBarBg);
  uiContainer.addChild(hpBarFill);

  // Supercruise channel radial — drawn for ALL ships while phase===1 so
  // AoI viewers can telegraph the interdiction window.
  const channelRadial = new Graphics();
  uiContainer.addChild(channelRadial);

  // Supercruise VFX state — per-ship closure so each ship has its own
  // ring + particle histories. Both are world-anchored: spawned at the
  // ship's renderX/renderY at the time of spawn, and rendered offset from
  // the ship's CURRENT renderX/renderY so they stay put in world space.
  const wakeRings: WakeRing[] = [];
  const particles: FieldParticle[] = [];
  const streaks: Streak[] = [];
  let lastRingSpawn = 0;
  let lastParticleSpawn = 0;
  let lastStreakSpawn = 0;

  let currentColor = 0x44aaff;
  let lastW = 0;
  let lastH = 0;

  function drawHull(hw: number, hh: number, color: number) {
    hull.clear();

    // Hull fill
    hull.poly([
      hw, 0,
      hw * 0.55, -hh * 0.42,
      hw * 0.1, -hh * 0.52,
      -hw * 0.1, -hh * 0.6,
      -hw * 0.25, -hh * 1.25,
      -hw * 0.55, -hh * 1.1,
      -hw * 0.7, -hh * 0.5,
      -hw * 0.7, hh * 0.5,
      -hw * 0.55, hh * 1.1,
      -hw * 0.25, hh * 1.25,
      -hw * 0.1, hh * 0.6,
      hw * 0.1, hh * 0.52,
      hw * 0.55, hh * 0.42,
    ]);
    hull.fill({ color, alpha: 0.1 });

    // Hull stroke
    hull.poly([
      hw, 0,
      hw * 0.55, -hh * 0.42,
      hw * 0.1, -hh * 0.52,
      -hw * 0.1, -hh * 0.6,
      -hw * 0.25, -hh * 1.25,
      -hw * 0.55, -hh * 1.1,
      -hw * 0.7, -hh * 0.5,
      -hw * 0.7, hh * 0.5,
      -hw * 0.55, hh * 1.1,
      -hw * 0.25, hh * 1.25,
      -hw * 0.1, hh * 0.6,
      hw * 0.1, hh * 0.52,
      hw * 0.55, hh * 0.42,
    ]);
    hull.stroke({ color, width: px(2), alpha: 1 });

    // Panel lines
    hull.moveTo(hw * 0.6, 0).lineTo(-hw * 0.65, 0).stroke({ color, width: px(1), alpha: 0.2 });
    hull.moveTo(hw * 0.3, -hh * 0.35).lineTo(hw * 0.3, hh * 0.35).stroke({ color, width: px(1), alpha: 0.2 });
    hull.moveTo(-hw * 0.1, -hh * 0.55).lineTo(-hw * 0.1, hh * 0.55).stroke({ color, width: px(1), alpha: 0.2 });
    hull.moveTo(-hw * 0.5, -hh * 0.7).lineTo(-hw * 0.5, hh * 0.7).stroke({ color, width: px(1), alpha: 0.2 });

    // Wing spars
    hull.moveTo(0, -hh * 0.55).lineTo(-hw * 0.45, -hh * 1.05).stroke({ color, width: px(1), alpha: 0.3 });
    hull.moveTo(0, hh * 0.55).lineTo(-hw * 0.45, hh * 1.05).stroke({ color, width: px(1), alpha: 0.3 });

    // Wing panel fills
    hull.poly([
      -hw * 0.1, -hh * 0.6,
      -hw * 0.25, -hh * 1.25,
      -hw * 0.55, -hh * 1.1,
      -hw * 0.35, -hh * 0.6,
    ]).fill({ color, alpha: 0.15 });
    hull.poly([
      -hw * 0.1, hh * 0.6,
      -hw * 0.25, hh * 1.25,
      -hw * 0.55, hh * 1.1,
      -hw * 0.35, hh * 0.6,
    ]).fill({ color, alpha: 0.15 });

    // Cockpit canopy
    hull.poly([
      hw * 0.8, 0,
      hw * 0.35, -hh * 0.22,
      hw * 0.15, -hh * 0.18,
      hw * 0.15, hh * 0.18,
      hw * 0.35, hh * 0.22,
    ]).fill({ color, alpha: 0.35 });
    hull.poly([
      hw * 0.8, 0,
      hw * 0.35, -hh * 0.22,
      hw * 0.15, -hh * 0.18,
      hw * 0.15, hh * 0.18,
      hw * 0.35, hh * 0.22,
    ]).stroke({ color, width: px(1), alpha: 0.5 });

    // Engine housings
    hull.rect(-hw * 0.72, -hh * 0.48, hw * 0.12, hh * 0.35)
      .fill({ color, alpha: 0.15 })
      .stroke({ color, width: px(1), alpha: 0.4 });
    hull.rect(-hw * 0.72, hh * 0.13, hw * 0.12, hh * 0.35)
      .fill({ color, alpha: 0.15 })
      .stroke({ color, width: px(1), alpha: 0.4 });

    // Nose accent
    hull.moveTo(hw * 0.85, 0).lineTo(hw * 0.55, -hh * 0.4).stroke({ color, width: px(2), alpha: 0.15 });
    hull.moveTo(hw * 0.85, 0).lineTo(hw * 0.55, hh * 0.4).stroke({ color, width: px(2), alpha: 0.15 });
  }

  return {
    container,
    update(ent: ClientEntity, isMe: boolean, now: number) {
      const e = ent.current as ShipEntity;
      const w = e.width || 2;
      const h = e.height || 1;
      const hw = w / 2;
      const hh = h / 2;
      const color = isMe ? 0x00ff00 : 0x44aaff;
      const pulse = 0.7 + 0.3 * Math.sin(now * 0.004);

      // Rebuild hull if color or size changed
      if (color !== currentColor || w !== lastW || h !== lastH) {
        currentColor = color;
        lastW = w;
        lastH = h;
        drawHull(hw, hh, color);
      }

      // Thrusting detection
      let thrusting = false;
      if (isMe) {
        // Will be set from outside via thruster particle system
        const spd = Math.sqrt(e.velX * e.velX + e.velY * e.velY);
        thrusting = spd > 1;
      } else {
        const spd = Math.sqrt(e.velX * e.velX + e.velY * e.velY);
        thrusting = spd > 1;
      }

      // Exhaust
      exhaust.clear();
      if (thrusting) {
        const flicker = 0.7 + 0.3 * Math.sin(now * 0.015);

        // Outer plume
        exhaust.poly([
          -hw * 0.7, -hh * 0.55,
          -hw * (1.3 + flicker * 0.3), 0,
          -hw * 0.7, hh * 0.55,
        ]).fill({ color: 0x3c78ff, alpha: 0.1 });

        // Main glow
        exhaust.poly([
          -hw * 0.7, -hh * 0.4,
          -hw * (1.1 + flicker * 0.25), 0,
          -hw * 0.7, hh * 0.4,
        ]).fill({ color: 0x5096ff, alpha: 0.2 + flicker * 0.25 });

        // Hot core
        exhaust.poly([
          -hw * 0.72, -hh * 0.12,
          -hw * (0.9 + flicker * 0.15), 0,
          -hw * 0.72, hh * 0.12,
        ]).fill({ color: 0xc8e6ff, alpha: 0.3 + flicker * 0.4 });
      }

      // Engine nozzle glow
      const nozzleAlpha = thrusting ? 0.6 + pulse * 0.4 : 0.15;
      exhaust.rect(-hw * 0.73, -hh * 0.38, hw * 0.04, hh * 0.22).fill({ color: 0x5096ff, alpha: nozzleAlpha });
      exhaust.rect(-hw * 0.73, hh * 0.16, hw * 0.04, hh * 0.22).fill({ color: 0x5096ff, alpha: nozzleAlpha });

      // Nav lights
      navLights.clear();
      const navAlpha = 0.5 + pulse * 0.3;

      // Wing tips
      for (const wy of [-hh * 1.23, hh * 1.23]) {
        navLights.circle(-hw * 0.25, wy, px(1.5)).fill({ color: 0xffffff, alpha: navAlpha });
      }
      // Nose
      navLights.circle(hw * 0.95, 0, px(1)).fill({ color: 0xffffff, alpha: navAlpha });
      // Tail
      navLights.circle(-hw * 0.68, 0, px(1.5)).fill({ color: 0xffffff, alpha: navAlpha * 0.7 });

      // Update UI elements (screen-space — we position uiContainer relative to entity)
      const barW = Math.max(w, px(40));
      const barH = px(3);
      const barGap = px(2);
      const shipTopOffset = -Math.sqrt(hw * hw + hh * hh) - px(24);

      // Shield bar
      const combat = getCombat(ent);
      const shY = shipTopOffset - barH * 2 - barGap;
      shieldBarBg.clear().rect(-barW / 2, shY, barW, barH).fill({ color: 0x5082ff, alpha: 0.15 });
      const shFrac = combat && combat.maxShield > 0 ? combat.shield / combat.maxShield : 0;
      shieldBarFill.clear();
      if (shFrac > 0) {
        shieldBarFill.rect(-barW / 2, shY, barW * shFrac, barH).fill({ color: 0x5082ff, alpha: 0.9 });
      }

      // HP bar
      const hpY = shipTopOffset - barH;
      hpBarBg.clear().rect(-barW / 2, hpY, barW, barH).fill({ color: 0xff3c3c, alpha: 0.15 });
      const hpFrac = combat && combat.maxHealth > 0 ? combat.health / combat.maxHealth : 0;
      hpBarFill.clear().rect(-barW / 2, hpY, barW * hpFrac, barH).fill({ color: hpFrac > 0.3 ? 0xff3c3c : 0xff1e1e, alpha: 0.9 });

      // Name
      const shipData = getShip(ent);
      if (shipData?.name) {
        nameTag.text = shipData.name;
        nameTag.style.fill = isMe ? 0x00ff00 : 0xccddff;
        nameTag.position.set(0, shY - px(4));
        nameTag.visible = true;
      } else {
        nameTag.visible = false;
      }

      // Supercruise Active-phase visuals — physics-grounded line-art VFX
      // for ALL ships in phase===2. Four effects compose the look:
      //  (1) Compressed bow-wave arcs in front of the ship (ship-following).
      //  (2) Motion-streak particles — short line segments oriented along
      //      their velocity, emergent wake-cone shape (world-anchored).
      //  (3) Anisotropic ripple ellipses left in world space, stretched
      //      along velocity direction at moment of spawn (world-anchored).
      //  (4) Drifting field particles caught in the gravitational wake
      //      (world-anchored, brief lifetime).
      // All effects are anisotropic — oriented by the velocity vector —
      // and continue to play out their lifetimes after the player exits
      // Active.
      const speed = Math.sqrt(e.velX * e.velX + e.velY * e.velY);
      const hullRadius = Math.sqrt(hw * hw + hh * hh);

      if (e.phase === 2 && speed > 0.5 && now - lastRingSpawn > RING_SPAWN_PERIOD_MS) {
        wakeRings.push({
          worldX: ent.renderX,
          worldY: ent.renderY,
          spawnTime: now,
          angle: Math.atan2(e.velY, e.velX),
        });
        lastRingSpawn = now;
      }
      if (e.phase === 2 && speed > 1 && now - lastParticleSpawn > PARTICLE_SPAWN_PERIOD_MS) {
        const count = 2 + Math.floor(Math.random() * 3);
        for (let i = 0; i < count; i++) {
          const offset = (Math.random() - 0.5) * h * 2.5;
          const back = hullRadius * (0.5 + Math.random() * 4);
          const dirX = e.velX / speed;
          const dirY = e.velY / speed;
          const perpX = -dirY;
          const perpY = dirX;
          const sx = ent.renderX + dirX * -back + perpX * offset;
          const sy = ent.renderY + dirY * -back + perpY * offset;
          const driftSpeed = 0.5 + Math.random() * 1.0;
          const drift = (Math.random() - 0.5) * 2; // -1..1
          particles.push({
            worldX: sx,
            worldY: sy,
            vx: -dirX * 0.2 + perpX * drift * driftSpeed,
            vy: -dirY * 0.2 + perpY * drift * driftSpeed,
            spawnTime: now,
          });
        }
        lastParticleSpawn = now;
      }

      // Motion-streak spawn — short line segments in the wake cone area
      // oriented along their velocity. Together with the denser field
      // particles, the wake cone shape emerges from particle distribution
      // rather than being drawn as geometry.
      if (e.phase === 2 && speed > 1 && now - lastStreakSpawn > STREAK_SPAWN_PERIOD_MS) {
        const count = 2 + Math.floor(Math.random() * 2);
        const dirX = e.velX / speed;
        const dirY = e.velY / speed;
        const perpX = -dirY;
        const perpY = dirX;
        for (let i = 0; i < count; i++) {
          // Position: somewhere in the wake cone behind the ship.
          const back = hullRadius * (1.0 + Math.random() * 3.0);
          const offset = (Math.random() - 0.5) * h * 2.0 * (back / (hullRadius * 4)); // widens with distance
          const sx = ent.renderX - dirX * back + perpX * offset;
          const sy = ent.renderY - dirY * back + perpY * offset;
          // Velocity: predominantly opposite the ship's motion (so the streak orients
          // along the ship's direction), with small perpendicular jitter.
          const baseStreakSpeed = 3 + Math.random() * 5;
          const jitter = (Math.random() - 0.5) * 1.5;
          streaks.push({
            worldX: sx,
            worldY: sy,
            vx: -dirX * baseStreakSpeed + perpX * jitter,
            vy: -dirY * baseStreakSpeed + perpY * jitter,
            spawnTime: now,
          });
        }
        lastStreakSpawn = now;
      }

      // Always age + prune so lifetimes drain after Active ends.
      for (let i = wakeRings.length - 1; i >= 0; i--) {
        if (now - wakeRings[i].spawnTime > RING_LIFETIME_MS) wakeRings.splice(i, 1);
      }
      for (let i = particles.length - 1; i >= 0; i--) {
        if (now - particles[i].spawnTime > PARTICLE_LIFETIME_MS) particles.splice(i, 1);
      }

      // (3) Anisotropic ripple ellipses, world-anchored. Each ring's
      // local-space center is offset from its world spawn position by the
      // ship's CURRENT renderX/renderY so it stays put in world space.
      // The ellipse is stretched along the velocity direction at spawn.
      ripples.clear();
      for (const r of wakeRings) {
        const age = (now - r.spawnTime) / RING_LIFETIME_MS;
        if (age > 1) continue;
        const lx = r.worldX - ent.renderX;
        const ly = r.worldY - ent.renderY;
        const baseRadius = px(4) + age * px(40);
        const radX = baseRadius * 1.5; // stretched along velocity
        const radY = baseRadius * 0.7; // compressed perpendicular
        const alpha = (1 - age) * 0.5;
        // Parametric polyline for a rotated ellipse — PixiJS 8 Graphics
        // can't transform individual shapes mid-batch, so we precompute.
        const segs = 32;
        const cosA = Math.cos(r.angle);
        const sinA = Math.sin(r.angle);
        ripples.moveTo(lx + cosA * radX, ly + sinA * radX);
        for (let s = 1; s <= segs; s++) {
          const t = (s / segs) * Math.PI * 2;
          const ex = Math.cos(t) * radX;
          const ey = Math.sin(t) * radY;
          const rx = ex * cosA - ey * sinA;
          const ry = ex * sinA + ey * cosA;
          ripples.lineTo(lx + rx, ly + ry);
        }
        ripples.stroke({ color: 0x66ddff, width: px(1), alpha });
      }

      // (4) Drifting field particles. World-anchored — accumulate drift
      // per-frame and render relative to ship's current position.
      fieldParticles.clear();
      for (const p of particles) {
        const age = (now - p.spawnTime) / PARTICLE_LIFETIME_MS;
        if (age > 1) continue;
        p.worldX += p.vx * 0.05;
        p.worldY += p.vy * 0.05;
        const lx = p.worldX - ent.renderX;
        const ly = p.worldY - ent.renderY;
        const alpha = (1 - age) * 0.7;
        fieldParticles.circle(lx, ly, px(0.8)).fill({ color: 0xbbeeff, alpha });
      }

      // (1) Compressed bow-wave arcs — 4 thin arcs in front of the ship,
      // each at increasing radius and decreasing alpha. Static in shape,
      // rotates with the velocity vector. Drawn in ship-local frame.
      bowWave.clear();
      if (e.phase === 2 && speed > 0.5) {
        const dirX = e.velX / speed;
        const dirY = e.velY / speed;
        const angleToForward = Math.atan2(dirY, dirX);
        for (let k = 0; k < 4; k++) {
          const arcDist = hullRadius * (1.2 + k * 0.5);
          const arcCx = dirX * arcDist;
          const arcCy = dirY * arcDist;
          const arcRadius = hullRadius * (0.4 + k * 0.2);
          const arcAlpha = 0.45 - k * 0.09;
          // Arc's center-of-curvature is at (arcCx, arcCy) ahead of the
          // ship; the arc itself bulges back toward the ship.
          const arcCenterAngle = angleToForward + Math.PI;
          const arcHalf = 0.44; // ~25° per side = 50° total
          bowWave
            .arc(arcCx, arcCy, arcRadius, arcCenterAngle - arcHalf, arcCenterAngle + arcHalf, false)
            .stroke({ color: 0x66ddff, width: px(1.5), alpha: arcAlpha });
        }
      }

      // (2) Motion streaks — short line segments oriented along their
      // velocity. World-anchored (each streak holds its own worldX/worldY
      // and is rendered offset by the ship's CURRENT renderX/renderY).
      // Updated + pruned every frame regardless of phase so lifetimes
      // finish playing out after Active ends.
      streaksLayer.clear();
      for (let i = streaks.length - 1; i >= 0; i--) {
        const s = streaks[i];
        const age = (now - s.spawnTime) / STREAK_LIFETIME_MS;
        if (age > 1) {
          streaks.splice(i, 1);
          continue;
        }
        // Advance position by velocity per frame (approximate dt).
        s.worldX += s.vx * 0.02;
        s.worldY += s.vy * 0.02;
        const sSpeed = Math.sqrt(s.vx * s.vx + s.vy * s.vy);
        const streakLen = Math.min(px(8), sSpeed * px(1.2));
        const ux = s.vx / sSpeed;
        const uy = s.vy / sSpeed;
        const lx = s.worldX - ent.renderX;
        const ly = s.worldY - ent.renderY;
        const tailX = lx - ux * streakLen;
        const tailY = ly - uy * streakLen;
        // Alpha curve: ramps in over the first 15% of life, then fades out.
        let alpha: number;
        if (age < 0.15) {
          alpha = (age / 0.15) * 0.7;
        } else {
          alpha = (1 - (age - 0.15) / 0.85) * 0.7;
        }
        streaksLayer
          .moveTo(lx, ly)
          .lineTo(tailX, tailY)
          .stroke({ color: 0xbbeeff, width: px(1.2), alpha });
      }

      // Supercruise channel radial — drawn for ALL ships while phase===1
      // so AoI viewers see the interdiction window telegraphed on nearby
      // players. Fills clockwise from 0 to 1 over (1 - channelRemaining/CHANNEL_TIME).
      // SUPERCRUISE_CHANNEL_TIME default = 3.0s; hard-coded here per spec.
      channelRadial.clear();
      if (e.phase === 1) {
        const CHANNEL_TIME = 3.0;
        const remaining = e.channelRemaining;
        const progress = Math.max(0, Math.min(1, 1 - remaining / CHANNEL_TIME));
        const radius = Math.sqrt(hw * hw + hh * hh) + px(8);
        // Background ring (faint)
        channelRadial.circle(0, 0, radius).stroke({ color: 0x66aaff, width: px(2), alpha: 0.25 });
        // Progress arc, starting at top (-PI/2), sweeping clockwise.
        if (progress > 0) {
          const startAngle = -Math.PI / 2;
          const endAngle = startAngle + progress * Math.PI * 2;
          channelRadial.arc(0, 0, radius, startAngle, endAngle, false)
            .stroke({ color: 0x88ccff, width: px(3), alpha: 0.9 });
        }
      }

      // Position UI container at entity position but without rotation
      uiContainer.position.set(ent.renderX, ent.renderY);
    },
    destroy() {
      container.destroy({ children: true });
      uiContainer.destroy({ children: true });
    },
    // Expose uiContainer for adding to a non-rotating layer
    get uiContainer() { return uiContainer; },
  } as EntityDisplayObject & { uiContainer: Container };
}

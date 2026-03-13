import { Container, Graphics } from "pixi.js";
import { StatusEffectType } from "@gen/game_pb.js";
import { getCombat } from "../entity-accessors";
import { spawnExplosion } from "./explosion";
import type { GameState } from "../state";
import type {
  AbilityCastEvent,
  AbilityEffect,
  BeamEffect,
  ImpactEffect,
  MissileEffect,
  ProjectileEffect,
  RangeRingEffect,
} from "../types";
import { audio } from "../audio/audio-manager";
import { ABILITY_TYPE_SOUNDS, SoundId } from "../audio/sounds";

// Ability colors
const COLOR_Q = 0x44aaff; // cyan
const COLOR_W = 0xffaa44; // orange
const COLOR_E = 0xaa44ff; // magenta
const COLOR_R = 0xff4444; // red
const COLOR_D = 0x44ff44; // green
const COLOR_F = 0xffff44; // yellow

const SLOT_COLORS = [COLOR_Q, COLOR_W, COLOR_E, COLOR_R, COLOR_D, COLOR_F];

/**
 * Compute weapon mount offset in world space for a given ability slot.
 * Slots 0,1 (Weapon1) fire from port (left) side; slots 2,3 (Weapon2) from starboard (right).
 */
export function weaponMountOffset(
  rot: number,
  height: number,
  slot: number,
): { x: number; y: number } {
  const hh = (height || 30) / 2;
  const mountDist = hh * 0.6;
  // port = positive perpendicular, starboard = negative
  const side = slot <= 1 ? 1 : -1;
  return {
    x: Math.sin(rot) * mountDist * side,
    y: -Math.cos(rot) * mountDist * side,
  };
}

export class AbilityEffectRenderer {
  private gfx: Graphics;
  private effects: AbilityEffect[] = [];
  private pendingExplosions: { time: number; targetId: number; x: number; y: number }[] = [];
  private pendingImpacts: { time: number; targetId: number; x: number; y: number; color: number; radius: number; duration: number }[] = [];

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState, now: number): void {
    this.gfx.clear();
    this.drainQueue(state, now);
    this.processPendingExplosions(state, now);
    this.processPendingImpacts(state, now);
    this.drawInstantEffects(state, now);
    this.drawStatusEffects(state, now);
  }

  private drainQueue(state: GameState, now: number): void {
    while (state.abilityEffectQueue.length > 0) {
      const event = state.abilityEffectQueue.shift()!;
      this.spawnEffect(event, state, now);
    }

    // Drain range ring requests (out-of-range ability attempts)
    while (state.rangeRingQueue.length > 0) {
      const event = state.rangeRingQueue.shift()!;
      this.effects.push({
        type: "rangeRing",
        entityId: state.myEntityId,
        startTime: now,
        duration: 800,
        radius: event.range,
        color: SLOT_COLORS[event.slot] ?? 0xffffff,
      } satisfies RangeRingEffect);
    }
  }

  private processPendingExplosions(state: GameState, now: number): void {
    for (let i = this.pendingExplosions.length - 1; i >= 0; i--) {
      const pe = this.pendingExplosions[i];
      if (now >= pe.time) {
        // Resolve live position of target
        const target = state.entities.get(pe.targetId);
        const x = target ? target.renderX : pe.x;
        const y = target ? target.renderY : pe.y;

        this.effects.push({
          type: "impact",
          entityId: pe.targetId,
          x, y,
          startTime: now,
          duration: 500,
          color: COLOR_R,
          radius: 40,
        } satisfies ImpactEffect);
        spawnExplosion(state.explosions, x, y, 30, 30, false);
        audio.play(SoundId.Explosion);
        state.screenShake = { intensity: 6, startTime: now, duration: 300 };
        this.pendingExplosions.splice(i, 1);
      }
    }
  }

  private processPendingImpacts(state: GameState, now: number): void {
    for (let i = this.pendingImpacts.length - 1; i >= 0; i--) {
      const pi = this.pendingImpacts[i];
      if (now >= pi.time) {
        const target = state.entities.get(pi.targetId);
        const x = target ? target.renderX : pi.x;
        const y = target ? target.renderY : pi.y;

        this.effects.push({
          type: "impact",
          entityId: pi.targetId,
          x, y,
          startTime: now,
          duration: pi.duration,
          color: pi.color,
          radius: pi.radius,
        } satisfies ImpactEffect);
        this.pendingImpacts.splice(i, 1);
      }
    }
  }

  private spawnEffect(
    event: AbilityCastEvent,
    state: GameState,
    now: number,
  ): void {
    const caster = state.entities.get(event.casterId);
    const target = state.entities.get(event.targetId);
    if (!caster) return;

    const myX = caster.renderX;
    const myY = caster.renderY;
    const tX = target ? target.renderX : myX;
    const tY = target ? target.renderY : myY;

    // Compute weapon mount offset based on slot
    const mount = weaponMountOffset(caster.renderRot, caster.curr.height, event.slot);
    const mX = myX + mount.x;
    const mY = myY + mount.y;

    // Play ability sound effect (keyed by ability type, not slot)
    const soundId = ABILITY_TYPE_SOUNDS[event.abilityType];
    if (soundId) {
      // Thruster abilities play for their boost duration
      if (event.abilityType === 30 || event.abilityType === 31) {
        audio.playFor(soundId, event.abilityType === 31 ? 800 : 1500);
      } else {
        audio.play(soundId);
      }
    }

    const slotColor = SLOT_COLORS[event.slot] ?? 0xffffff;

    switch (event.abilityType) {
      // --- Pulse Laser / Pulse Barrage — missile salvo ---
      case 1:   // PulseLaser
      case 2: { // PulseBarrage
        const qdx = tX - mX;
        const qdy = tY - mY;
        const qdist = Math.sqrt(qdx * qdx + qdy * qdy) || 1;
        const perpX = -qdy / qdist;
        const perpY = qdx / qdist;
        const midX = (mX + tX) / 2;
        const midY = (mY + tY) / 2;

        const missileCount = 6;
        const speed = 600;
        const travelTime = Math.max(qdist / speed, 0.15) * 1000;

        for (let m = 0; m < missileCount; m++) {
          const side = m % 2 === 0 ? 1 : -1;
          const arcSpread = 80 + Math.random() * 60;
          const stagger = m * 40;

          this.effects.push({
            type: "missile",
            fromId: event.casterId,
            toId: event.targetId,
            fromX: mX + perpX * side * 12,
            fromY: mY + perpY * side * 12,
            toX: tX,
            toY: tY,
            cpX: midX + perpX * side * arcSpread,
            cpY: midY + perpY * side * arcSpread,
            startTime: now + stagger,
            duration: travelTime,
            color: slotColor,
            trailColor: 0x2288cc,
            size: 2.5,
            slot: event.slot,
          } satisfies MissileEffect);
        }

        if (target) {
          const lastArrival = now + travelTime + (missileCount - 1) * 40;
          this.pendingImpacts.push({
            time: lastArrival,
            targetId: event.targetId,
            x: tX,
            y: tY,
            color: slotColor,
            radius: 12,
            duration: 250,
          });
        }
        break;
      }

      // --- Rail Shot / Piercing Round — instant beam + impact ---
      case 3:  // RailShot
      case 4:  // PiercingRound
        this.effects.push({
          type: "beam",
          fromId: event.casterId,
          toId: event.targetId,
          fromX: mX,
          fromY: mY,
          toX: tX,
          toY: tY,
          startTime: now,
          duration: 250,
          color: slotColor,
          width: 5,
          slot: event.slot,
        } satisfies BeamEffect);
        if (target) {
          this.effects.push({
            type: "impact",
            entityId: event.targetId,
            x: tX,
            y: tY,
            startTime: now,
            duration: 350,
            color: slotColor,
            radius: 20,
          } satisfies ImpactEffect);
        }
        break;

      // --- Ion Burn / Ion Overload — beam + DoT zap ---
      case 5:  // IonBurn
      case 6:  // IonOverload
        this.effects.push({
          type: "beam",
          fromId: event.casterId,
          toId: event.targetId,
          fromX: mX,
          fromY: mY,
          toX: tX,
          toY: tY,
          startTime: now,
          duration: 200,
          color: slotColor,
          width: 3,
          slot: event.slot,
        } satisfies BeamEffect);
        break;

      // --- Plasma Bolt / Plasma Torpedo — projectile + explosion ---
      case 7:    // PlasmaBolt
      case 8: {  // PlasmaTorpedo
        const dx = tX - mX;
        const dy = tY - mY;
        const dist = Math.sqrt(dx * dx + dy * dy);
        const speed = 800;
        const travelTime = Math.max(dist / speed, 0.1) * 1000;

        this.effects.push({
          type: "projectile",
          fromId: event.casterId,
          toId: event.targetId,
          fromX: mX,
          fromY: mY,
          toX: tX,
          toY: tY,
          startTime: now,
          duration: travelTime,
          color: slotColor,
          trailColor: 0xff8844,
          size: 5,
          slot: event.slot,
        } satisfies ProjectileEffect);

        if (target) {
          const arrivalTime = now + travelTime;
          this.pendingExplosions.push({
            time: arrivalTime,
            targetId: event.targetId,
            x: tX,
            y: tY,
          });
        }
        break;
      }

      // --- Shield abilities — shield bubble ---
      case 20: // EmergencyShield
      case 21: // HardenedShield
        this.effects.push({
          type: "shieldBubble",
          entityId: event.casterId,
          startTime: now,
          duration: 400,
        });
        break;

      // --- Thruster abilities — brief flash ---
      case 30: // Afterburner
      case 31: // MicroWarp
        this.effects.push({
          type: "impact",
          entityId: event.casterId,
          x: myX,
          y: myY,
          startTime: now,
          duration: 200,
          color: COLOR_F,
          radius: 15,
        } satisfies ImpactEffect);
        break;

      // --- Mining beam toggle — no instant VFX (continuous beam handled by mining-laser.ts) ---
      case 40: // MiningBeam
        break;

      // --- Extract pulse — burst flash on target ---
      case 41: // ExtractPulse
        if (target) {
          this.effects.push({
            type: "impact",
            entityId: event.targetId,
            x: tX,
            y: tY,
            startTime: now,
            duration: 300,
            color: 0x00ff80,
            radius: 25,
          } satisfies ImpactEffect);
        }
        break;
    }
  }

  private drawInstantEffects(state: GameState, now: number): void {
    for (let i = this.effects.length - 1; i >= 0; i--) {
      const eff = this.effects[i];
      const elapsed = now - eff.startTime;
      if (elapsed >= eff.duration) {
        this.effects.splice(i, 1);
        continue;
      }
      const t = elapsed / eff.duration;

      switch (eff.type) {
        case "beam":
          this.drawBeam(eff, state, t);
          break;
        case "impact":
          this.drawImpact(eff, state, t);
          break;
        case "shieldBubble":
          this.drawShieldBubble(eff, state, t);
          break;
        case "projectile":
          this.drawProjectile(eff, state, t);
          break;
        case "missile":
          this.drawMissile(eff, state, t, now);
          break;
        case "rangeRing":
          this.drawRangeRing(eff, state, t, now);
          break;
      }
    }
  }

  private drawBeam(eff: BeamEffect, state: GameState, t: number): void {
    // Resolve live positions if entities still exist
    const from = state.entities.get(eff.fromId);
    const to = state.entities.get(eff.toId);
    let x1: number, y1: number;
    if (from && eff.slot != null) {
      const m = weaponMountOffset(from.renderRot, from.curr.height, eff.slot);
      x1 = from.renderX + m.x;
      y1 = from.renderY + m.y;
    } else {
      x1 = from ? from.renderX : eff.fromX;
      y1 = from ? from.renderY : eff.fromY;
    }
    const x2 = to ? to.renderX : eff.toX;
    const y2 = to ? to.renderY : eff.toY;

    const alpha = 1 - t * t;
    const w = eff.width * (1 - t * 0.5);

    // Glow
    this.gfx
      .moveTo(x1, y1)
      .lineTo(x2, y2)
      .stroke({ color: 0xffffff, width: w * 3, alpha: alpha * 0.15 });

    // Core beam
    this.gfx
      .moveTo(x1, y1)
      .lineTo(x2, y2)
      .stroke({ color: eff.color, width: w, alpha });
  }

  private drawProjectile(eff: ProjectileEffect, state: GameState, t: number): void {
    // Resolve live positions
    const from = state.entities.get(eff.fromId);
    const to = state.entities.get(eff.toId);
    let x1: number, y1: number;
    if (from && eff.slot != null) {
      const m = weaponMountOffset(from.renderRot, from.curr.height, eff.slot);
      x1 = from.renderX + m.x;
      y1 = from.renderY + m.y;
    } else {
      x1 = from ? from.renderX : eff.fromX;
      y1 = from ? from.renderY : eff.fromY;
    }
    const x2 = to ? to.renderX : eff.toX;
    const y2 = to ? to.renderY : eff.toY;

    // Current projectile position (lerp)
    const px = x1 + (x2 - x1) * t;
    const py = y1 + (y2 - y1) * t;

    // Trail — draw a few segments behind the projectile
    const trailLen = 0.15; // fraction of path
    for (let i = 0; i < 4; i++) {
      const tt = Math.max(t - trailLen * (i / 4), 0);
      const tx = x1 + (x2 - x1) * tt;
      const ty = y1 + (y2 - y1) * tt;
      const alpha = (1 - i / 4) * 0.4;
      const w = eff.size * (1 - i / 4) * 0.6;
      this.gfx
        .moveTo(px, py)
        .lineTo(tx, ty)
        .stroke({ color: eff.trailColor, width: w, alpha });
    }

    // Outer glow
    this.gfx
      .circle(px, py, eff.size * 2)
      .fill({ color: eff.color, alpha: 0.15 });

    // Core
    this.gfx
      .circle(px, py, eff.size)
      .fill({ color: 0xffffff, alpha: 0.9 });

    // Colored ring
    this.gfx
      .circle(px, py, eff.size * 1.3)
      .stroke({ color: eff.color, width: 1.5, alpha: 0.8 });
  }

  private drawMissile(eff: MissileEffect, state: GameState, _t: number, now: number): void {
    // Don't draw before staggered launch
    if (now < eff.startTime) return;

    // Resolve live positions
    const from = state.entities.get(eff.fromId);
    const to = state.entities.get(eff.toId);
    let x1: number, y1: number;
    if (from && eff.slot != null) {
      const m = weaponMountOffset(from.renderRot, from.curr.height, eff.slot);
      x1 = from.renderX + m.x;
      y1 = from.renderY + m.y;
    } else {
      x1 = from ? from.renderX : eff.fromX;
      y1 = from ? from.renderY : eff.fromY;
    }
    const x2 = to ? to.renderX : eff.toX;
    const y2 = to ? to.renderY : eff.toY;

    // Recalculate control point relative to live positions
    const midX = (x1 + x2) / 2;
    const midY = (y1 + y2) / 2;
    const dx = x2 - x1;
    const dy = y2 - y1;
    const dist = Math.sqrt(dx * dx + dy * dy) || 1;
    const perpX = -dy / dist;
    const perpY = dx / dist;
    // Preserve the offset magnitude from the original control point
    const origMidX = (eff.fromX + eff.toX) / 2;
    const origMidY = (eff.fromY + eff.toY) / 2;
    const cpOffX = eff.cpX - origMidX;
    const cpOffY = eff.cpY - origMidY;
    const origDx = eff.toX - eff.fromX;
    const origDy = eff.toY - eff.fromY;
    const origDist = Math.sqrt(origDx * origDx + origDy * origDy) || 1;
    const origPerpX = -origDy / origDist;
    const origPerpY = origDx / origDist;
    const offsetMag = cpOffX * origPerpX + cpOffY * origPerpY;
    const cpX = midX + perpX * offsetMag;
    const cpY = midY + perpY * offsetMag;

    // Quadratic bezier: p = (1-t)²·start + 2(1-t)t·cp + t²·end
    const mt = Math.min((now - eff.startTime) / eff.duration, 1);
    const mt1 = 1 - mt;
    const px = mt1 * mt1 * x1 + 2 * mt1 * mt * cpX + mt * mt * x2;
    const py = mt1 * mt1 * y1 + 2 * mt1 * mt * cpY + mt * mt * y2;

    // Trail — sample a few points behind on the bezier
    const trailSegments = 5;
    const trailSpan = 0.12;
    let prevTx = px;
    let prevTy = py;
    for (let i = 1; i <= trailSegments; i++) {
      const tt = Math.max(mt - trailSpan * (i / trailSegments), 0);
      const tt1 = 1 - tt;
      const tx = tt1 * tt1 * x1 + 2 * tt1 * tt * cpX + tt * tt * x2;
      const ty = tt1 * tt1 * y1 + 2 * tt1 * tt * cpY + tt * tt * y2;
      const alpha = (1 - i / trailSegments) * 0.5;
      const w = eff.size * (1 - i / trailSegments) * 0.8;
      this.gfx
        .moveTo(prevTx, prevTy)
        .lineTo(tx, ty)
        .stroke({ color: eff.trailColor, width: w, alpha });
      prevTx = tx;
      prevTy = ty;
    }

    // Glow
    this.gfx
      .circle(px, py, eff.size * 2.5)
      .fill({ color: eff.color, alpha: 0.12 });

    // Core
    this.gfx
      .circle(px, py, eff.size)
      .fill({ color: 0xffffff, alpha: 0.9 });

    // Colored ring
    this.gfx
      .circle(px, py, eff.size * 1.5)
      .stroke({ color: eff.color, width: 1, alpha: 0.7 });
  }

  private drawImpact(eff: ImpactEffect, state: GameState, t: number): void {
    // Track entity position if available
    const ent = eff.entityId ? state.entities.get(eff.entityId) : null;
    const x = ent ? ent.renderX : eff.x;
    const y = ent ? ent.renderY : eff.y;

    // Expanding ring
    const ringRadius = eff.radius * (0.3 + t * 0.7);
    const ringAlpha = (1 - t) * 0.8;
    const ringWidth = 2 + (1 - t) * 3;
    this.gfx
      .circle(x, y, ringRadius)
      .stroke({ color: eff.color, width: ringWidth, alpha: ringAlpha });

    // Center flash (first 30%)
    if (t < 0.3) {
      const flashAlpha = (1 - t / 0.3) * 0.6;
      this.gfx
        .circle(x, y, eff.radius * 0.4)
        .fill({ color: 0xffffff, alpha: flashAlpha });
    }
  }

  private drawShieldBubble(
    eff: { entityId: number; startTime: number; duration: number },
    state: GameState,
    t: number,
  ): void {
    const ent = state.entities.get(eff.entityId);
    if (!ent) return;

    const baseRadius = Math.max(ent.curr.width, ent.curr.height, 20) * 0.5 + 15;
    const r = baseRadius * (1 + t * 0.3);
    const alpha = (1 - t) * 0.7;
    this.gfx
      .circle(ent.renderX, ent.renderY, r)
      .stroke({ color: COLOR_D, width: 3 * (1 - t), alpha });
  }

  private drawRangeRing(
    eff: RangeRingEffect,
    state: GameState,
    t: number,
    now: number,
  ): void {
    const ent = state.entities.get(eff.entityId);
    if (!ent) return;

    const x = ent.renderX;
    const y = ent.renderY;
    const radius = eff.radius;

    // Quick scale-up in first 15%, then hold
    const scale = t < 0.15 ? t / 0.15 : 1;
    const r = radius * scale;

    // Fade out over the effect lifetime
    const alpha = (1 - t) * 0.7;

    // Draw dashed circle
    const dashCount = 32;
    const gapFraction = 0.35; // fraction of each segment that is gap
    const segmentAngle = (Math.PI * 2) / dashCount;
    const dashAngle = segmentAngle * (1 - gapFraction);

    // Rotating animation
    const rotation = now * 0.001;

    for (let i = 0; i < dashCount; i++) {
      const startAngle = rotation + i * segmentAngle;
      const endAngle = startAngle + dashAngle;

      // Draw arc segment as a series of short lines
      const steps = 4;
      const x0 = x + Math.cos(startAngle) * r;
      const y0 = y + Math.sin(startAngle) * r;
      this.gfx.moveTo(x0, y0);

      for (let s = 1; s <= steps; s++) {
        const a = startAngle + (endAngle - startAngle) * (s / steps);
        this.gfx.lineTo(x + Math.cos(a) * r, y + Math.sin(a) * r);
      }

      this.gfx.stroke({ color: eff.color, width: 1.5, alpha });
    }
  }

  // --- Persistent status effect visuals (visible on all entities) ---

  private drawStatusEffects(state: GameState, now: number): void {
    for (const [, ent] of state.entities) {
      const combat = getCombat(ent.curr);
      if (!combat || !combat.statusEffects || combat.statusEffects.length === 0)
        continue;

      for (const se of combat.statusEffects) {
        switch (se.type) {
          case StatusEffectType.STATUS_EFFECT_ION_BURN:
            this.drawIonBurn(ent.renderX, ent.renderY, ent.curr.width, ent.curr.height, now);
            break;
          case StatusEffectType.STATUS_EFFECT_FORTIFIED:
            this.drawFortified(ent.renderX, ent.renderY, ent.curr.width, ent.curr.height, now);
            break;
          case StatusEffectType.STATUS_EFFECT_AFTERBURNER:
            this.drawAfterburner(
              ent.renderX,
              ent.renderY,
              ent.renderRot,
              now,
            );
            break;
        }
      }
    }
  }

  private drawIonBurn(
    x: number,
    y: number,
    w: number,
    h: number,
    now: number,
  ): void {
    const baseR = Math.max(w, h, 20) * 0.5 + 5;
    // Deterministic pseudo-random via time quantization
    const seed = Math.floor(now / 80);

    for (let i = 0; i < 6; i++) {
      const angle =
        now * 0.003 + (i * Math.PI) / 3 + Math.sin(seed * 0.7 + i) * 0.3;
      const innerR = baseR * 0.7;
      const outerR = baseR;

      const x1 = x + Math.cos(angle) * innerR;
      const y1 = y + Math.sin(angle) * innerR;
      const x2 = x + Math.cos(angle + 0.2) * outerR;
      const y2 = y + Math.sin(angle + 0.2) * outerR;

      // Jitter midpoint for crackle effect
      const jitterSeed = (seed * 13 + i * 7) % 100;
      const mx = (x1 + x2) / 2 + (jitterSeed / 100 - 0.5) * 8;
      const my = (y1 + y2) / 2 + ((jitterSeed * 3) % 100 / 100 - 0.5) * 8;

      const alpha = 0.5 + 0.3 * Math.sin(now * 0.02 + i);
      this.gfx
        .moveTo(x1, y1)
        .lineTo(mx, my)
        .lineTo(x2, y2)
        .stroke({ color: COLOR_E, width: 1.5, alpha });
    }
  }

  private drawFortified(
    x: number,
    y: number,
    w: number,
    h: number,
    now: number,
  ): void {
    const r = Math.max(w, h, 20) * 0.5 + 12;
    const pulse = 0.7 + 0.3 * Math.sin(now * 0.004);

    // Hexagonal shield outline
    this.gfx.moveTo(x + r, y);
    for (let i = 1; i <= 6; i++) {
      const a = (i * Math.PI * 2) / 6;
      this.gfx.lineTo(x + Math.cos(a) * r, y + Math.sin(a) * r);
    }
    this.gfx.stroke({ color: COLOR_D, width: 2, alpha: pulse * 0.5 });
  }

  private drawAfterburner(
    x: number,
    y: number,
    rot: number,
    now: number,
  ): void {
    const pulse = 0.8 + 0.2 * Math.sin(now * 0.01);

    for (let i = 1; i <= 3; i++) {
      const trailX = x - Math.cos(rot) * (20 + i * 15);
      const trailY = y - Math.sin(rot) * (20 + i * 15);
      const alpha = (0.4 - i * 0.12) * pulse;
      this.gfx
        .moveTo(x, y)
        .lineTo(trailX, trailY)
        .stroke({ color: COLOR_F, width: 2, alpha });
    }
  }
}

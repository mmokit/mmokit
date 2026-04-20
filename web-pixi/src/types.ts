import type { Container } from "pixi.js";
import type { AnyEntity } from "../sdk/index.js";

/**
 * A single authoritative server snapshot of an entity, carrying the
 * wall-clock server time at which the frame was stamped. Each
 * ClientEntity keeps a small ring of these to feed snapshot
 * interpolation (Source/Gaffer canonical pattern).
 */
export interface EntitySample {
  worldX: number;
  worldY: number;
  velX: number;
  velY: number;
  rotation: number;
  serverTimeMs: number;
}

export interface ClientEntity {
  current: AnyEntity;            // latest decoded state (for HUD / game logic)
  samples: EntitySample[];       // ring, samples[0] = oldest, capped at RING_SIZE
  // Interpolated render values (set each frame by interpolateEntities).
  renderX: number;
  renderY: number;
  renderRot: number;
}

export interface EntityDisplayObject {
  container: Container;
  update(ent: ClientEntity, isMe: boolean, now: number): void;
  destroy(): void;
}

export interface ThrusterParticle {
  x: number;
  y: number;
  vx: number;
  vy: number;
  life: number;
  maxLife: number;
  size: number;
}

export interface ExplosionParticle {
  type: "debris" | "spark" | "flame";
  x: number;
  y: number;
  vx: number;
  vy: number;
  life: number;
  maxLife: number;
  rot?: number;
  rotSpeed?: number;
  w?: number;
  h?: number;
  color?: number[];
  size?: number;
  radius?: number;
}

export interface Explosion {
  x: number;
  y: number;
  startTime: number;
  particles: ExplosionParticle[];
  shockRadius: number;
  shockMaxRadius: number;
  shockDuration: number;
  flashDuration: number;
  flashRadius: number;
  duration: number;
}

export interface Toast {
  text: string;
  time: number;
}

export interface AbilityCastEvent {
  slot: number;
  abilityType: number;
  targetId: number;
  damageDealt: number;
  casterId: number;
  time: number;
}

export interface RangeRingEvent {
  slot: number;
  range: number;
}

export interface BeamEffect {
  type: "beam";
  fromId: number;
  toId: number;
  fromX: number;
  fromY: number;
  toX: number;
  toY: number;
  startTime: number;
  duration: number;
  color: number;
  width: number;
  slot?: number; // ability slot (0-5) for weapon mount offset
}

export interface ImpactEffect {
  type: "impact";
  entityId: number;
  x: number;
  y: number;
  startTime: number;
  duration: number;
  color: number;
  radius: number;
}

export interface ShieldBubbleEffect {
  type: "shieldBubble";
  entityId: number;
  startTime: number;
  duration: number;
}

export interface ProjectileEffect {
  type: "projectile";
  fromId: number;
  toId: number;
  fromX: number;
  fromY: number;
  toX: number;
  toY: number;
  startTime: number;
  duration: number;
  color: number;
  trailColor: number;
  size: number;
  slot?: number; // ability slot (0-5) for weapon mount offset
}

export interface MissileEffect {
  type: "missile";
  fromId: number;
  toId: number;
  fromX: number;
  fromY: number;
  toX: number;
  toY: number;
  // Bezier control point for the arc
  cpX: number;
  cpY: number;
  startTime: number;
  duration: number;
  color: number;
  trailColor: number;
  size: number;
  slot?: number; // ability slot (0-5) for weapon mount offset
}

export interface RangeRingEffect {
  type: "rangeRing";
  entityId: number;
  startTime: number;
  duration: number;
  radius: number;
  color: number;
}

export type AbilityEffect = BeamEffect | ImpactEffect | ShieldBubbleEffect | ProjectileEffect | MissileEffect | RangeRingEffect;

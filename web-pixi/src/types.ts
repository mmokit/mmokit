import type { Container } from "pixi.js";
import type { EntityState } from "@gen/game_pb.js";

export interface ClientEntity {
  prev: EntityState;
  curr: EntityState;
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
  targetId: number;
  damageDealt: number;
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

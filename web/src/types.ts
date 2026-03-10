import type { EntityState } from "@gen/game_pb.js";

export interface ClientEntity {
  prev: EntityState;
  curr: EntityState;
  renderX: number;
  renderY: number;
  renderRot: number;
  thrusterParticles: ThrusterParticle[];
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

export interface StarLayer {
  stars: Star[];
  parallax: number;
  tileSize: number;
}

export interface Star {
  x: number;
  y: number;
  size: number;
  alpha: number;
  twinkleSpeed: number;
  twinkleOffset: number;
}

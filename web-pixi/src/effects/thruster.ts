import { Container, Graphics } from "pixi.js";
import { MAX_THRUSTER_PARTICLES } from "../constants";
import { px } from "../view";
import type { ThrusterParticle } from "../types";
import type { GameState } from "../state";
import { audio } from "../audio/audio-manager";
import { SoundId } from "../audio/sounds";

// Per-entity thruster particle arrays
const particleMap = new Map<number, ThrusterParticle[]>();

export class ThrusterRenderer {
  private gfx: Graphics;
  private wasThrusting = false;

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState, dt: number): void {
    this.gfx.clear();

    // Clean up particles for removed entities
    for (const id of particleMap.keys()) {
      if (!state.entities.has(id)) {
        particleMap.delete(id);
      }
    }

    // Track local player thruster sound
    let myThrusting = false;
    const myEnt = state.entities.get(state.myEntityId);
    if (myEnt) {
      const spd = Math.sqrt(myEnt.current.velX * myEnt.current.velX + myEnt.current.velY * myEnt.current.velY);
      myThrusting = spd > 30;
    }
    if (myThrusting && !this.wasThrusting) {
      audio.loop(SoundId.Thruster);
    } else if (!myThrusting && this.wasThrusting) {
      audio.stopLoop(SoundId.Thruster);
    }
    this.wasThrusting = myThrusting;

    for (const [id, ent] of state.entities) {
      const e = ent.current;
      if (e.entityType !== 0) continue; // SHIP = 0

      const spd = Math.sqrt(e.velX * e.velX + e.velY * e.velY);
      const isThrusting = spd > 30;

      let particles = particleMap.get(id);
      if (!particles) {
        particles = [];
        particleMap.set(id, particles);
      }

      const hw = (e.width || 2) / 2;
      const hh = (e.height || 1) / 2;
      const rot = ent.renderRot;

      // Spawn particles
      if (isThrusting && particles.length < MAX_THRUSTER_PARTICLES) {
        for (const nozzleOff of [-hh * 0.3, hh * 0.3]) {
          const localX = -hw * 0.72;
          const localY = nozzleOff;
          const wx = ent.renderX + Math.cos(rot) * localX - Math.sin(rot) * localY;
          const wy = ent.renderY + Math.sin(rot) * localX + Math.cos(rot) * localY;

          const spread = (Math.random() - 0.5) * 0.6;
          const emitAngle = rot + Math.PI + spread;
          const speed = 60 + Math.random() * 120;
          const life = 0.2 + Math.random() * 0.3;

          particles.push({
            x: wx + (Math.random() - 0.5) * px(3),
            y: wy + (Math.random() - 0.5) * px(3),
            vx: Math.cos(emitAngle) * px(speed),
            vy: Math.sin(emitAngle) * px(speed),
            life,
            maxLife: life,
            size: px(1.5 + Math.random() * 2.5),
          });
        }
      }

      // Update and draw particles
      for (let i = particles.length - 1; i >= 0; i--) {
        const p = particles[i];
        p.x += p.vx * dt;
        p.y += p.vy * dt;
        p.vx *= 0.94;
        p.vy *= 0.94;
        p.life -= dt;

        if (p.life <= 0) {
          particles.splice(i, 1);
          continue;
        }

        const t = 1 - p.life / p.maxLife;
        const alpha = (1 - t * t) * 0.8;
        const size = p.size * (1 - t * 0.5);

        const r = Math.floor(200 * (1 - t * 0.8));
        const g = Math.floor(220 * (1 - t * 0.5));
        const b = 255;
        const color = (r << 16) | (g << 8) | b;

        this.gfx.circle(p.x, p.y, size).fill({ color, alpha });
      }
    }
  }
}

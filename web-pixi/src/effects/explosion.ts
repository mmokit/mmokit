import { Container, Graphics } from "pixi.js";
import type { Explosion, ExplosionParticle } from "../types";

export function spawnExplosion(
  explosions: Explosion[],
  worldX: number,
  worldY: number,
  shipW: number,
  shipH: number,
  isMe: boolean,
): void {
  const now = performance.now();
  const size = Math.max(shipW || 60, shipH || 30);
  const particles: ExplosionParticle[] = [];

  // Debris chunks
  for (let i = 0; i < 18; i++) {
    const angle = Math.random() * Math.PI * 2;
    const speed = 40 + Math.random() * 160;
    const life = 0.6 + Math.random() * 0.9;
    particles.push({
      type: "debris",
      x: (Math.random() - 0.5) * size * 0.3,
      y: (Math.random() - 0.5) * size * 0.3,
      vx: Math.cos(angle) * speed,
      vy: Math.sin(angle) * speed,
      rot: Math.random() * Math.PI * 2,
      rotSpeed: (Math.random() - 0.5) * 12,
      w: 3 + Math.random() * 8,
      h: 2 + Math.random() * 4,
      life,
      maxLife: life,
      color: isMe
        ? [0, 255, 0]
        : [68 + Math.random() * 60, 170 + Math.random() * 50, 255],
    });
  }

  // Hot sparks
  for (let i = 0; i < 30; i++) {
    const angle = Math.random() * Math.PI * 2;
    const speed = 80 + Math.random() * 250;
    const life = 0.3 + Math.random() * 0.5;
    particles.push({
      type: "spark",
      x: (Math.random() - 0.5) * size * 0.15,
      y: (Math.random() - 0.5) * size * 0.15,
      vx: Math.cos(angle) * speed,
      vy: Math.sin(angle) * speed,
      life,
      maxLife: life,
      size: 1 + Math.random() * 2,
    });
  }

  // Flame puffs
  for (let i = 0; i < 8; i++) {
    const angle = Math.random() * Math.PI * 2;
    const speed = 15 + Math.random() * 60;
    const life = 0.4 + Math.random() * 0.6;
    particles.push({
      type: "flame",
      x: (Math.random() - 0.5) * size * 0.2,
      y: (Math.random() - 0.5) * size * 0.2,
      vx: Math.cos(angle) * speed,
      vy: Math.sin(angle) * speed,
      life,
      maxLife: life,
      radius: 4 + Math.random() * 10,
    });
  }

  explosions.push({
    x: worldX,
    y: worldY,
    startTime: now,
    particles,
    shockRadius: size * 0.2,
    shockMaxRadius: size * 1.8,
    shockDuration: 0.5,
    flashDuration: 0.15,
    flashRadius: size * 0.8,
    duration: 1.8,
  });
}

export class ExplosionRenderer {
  private gfx: Graphics;
  private container: Container;

  constructor(parent: Container) {
    this.container = parent;
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(explosions: Explosion[], now: number): void {
    this.gfx.clear();
    const dt = 1 / 60;

    for (let i = explosions.length - 1; i >= 0; i--) {
      const ex = explosions[i];
      const elapsed = (now - ex.startTime) / 1000;

      if (elapsed > ex.duration) {
        explosions.splice(i, 1);
        continue;
      }

      // Flash
      if (elapsed < ex.flashDuration) {
        const flashT = elapsed / ex.flashDuration;
        const flashAlpha = (1 - flashT) * 0.7;
        const r = ex.flashRadius * (0.5 + flashT * 0.5);
        this.gfx.circle(ex.x, ex.y, r).fill({ color: 0xffdca0, alpha: flashAlpha });
      }

      // Shockwave ring
      if (elapsed < ex.shockDuration) {
        const shockT = elapsed / ex.shockDuration;
        const r = ex.shockRadius + (ex.shockMaxRadius - ex.shockRadius) * shockT;
        const alpha = (1 - shockT) * 0.6;
        this.gfx.circle(ex.x, ex.y, r).stroke({ color: 0xffb450, width: 2 + (1 - shockT) * 3, alpha });
      }

      // Particles
      for (const p of ex.particles) {
        if (p.life <= 0) continue;

        p.x += p.vx * dt;
        p.y += p.vy * dt;
        p.vx *= 0.97;
        p.vy *= 0.97;
        p.life -= dt;

        const t = 1 - p.life / p.maxLife;
        const alpha = Math.max(0, 1 - t * t);
        const px = ex.x + p.x;
        const py = ex.y + p.y;

        if (p.type === "debris") {
          p.rot! += p.rotSpeed! * dt;
          const r = Math.floor(p.color![0] + (200 - p.color![0]) * t * 0.5);
          const g = Math.floor(p.color![1] * (1 - t * 0.7));
          const b = Math.floor(p.color![2] * (1 - t * 0.9));
          const color = (r << 16) | (g << 8) | b;
          this.gfx.rect(px - p.w! / 2, py - p.h! / 2, p.w!, p.h!).fill({ color, alpha });
        } else if (p.type === "spark") {
          const r2 = 255;
          const g2 = Math.floor(255 * (1 - t * 0.8));
          const b2 = Math.floor(200 * (1 - t));
          const color = (r2 << 16) | (g2 << 8) | b2;
          this.gfx.circle(px, py, p.size! * (1 - t * 0.5)).fill({ color, alpha });
        } else if (p.type === "flame") {
          const r3 = p.radius! * (1 + t * 2);
          const flameAlpha = alpha * 0.4;
          this.gfx.circle(px, py, r3).fill({ color: 0xff5014, alpha: flameAlpha });
        }
      }
    }
  }
}

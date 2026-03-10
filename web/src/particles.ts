import { MAX_THRUSTER_PARTICLES } from "./constants";
import type { ClientEntity, Explosion, ExplosionParticle } from "./types";

export function updateThrusterParticles(
  ent: ClientEntity,
  isThrusting: boolean,
  dt: number,
): void {
  if (!ent.thrusterParticles) ent.thrusterParticles = [];
  const particles = ent.thrusterParticles;
  const e = ent.curr;
  const hw = (e.width || 60) / 2;
  const hh = (e.height || 30) / 2;
  const rot = ent.renderRot;

  // Spawn new particles when thrusting
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
        x: wx + (Math.random() - 0.5) * 3,
        y: wy + (Math.random() - 0.5) * 3,
        vx: Math.cos(emitAngle) * speed,
        vy: Math.sin(emitAngle) * speed,
        life,
        maxLife: life,
        size: 1.5 + Math.random() * 2.5,
      });
    }
  }

  // Update existing particles
  for (let i = particles.length - 1; i >= 0; i--) {
    const p = particles[i];
    p.x += p.vx * dt;
    p.y += p.vy * dt;
    p.vx *= 0.94;
    p.vy *= 0.94;
    p.life -= dt;
    if (p.life <= 0) {
      particles.splice(i, 1);
    }
  }
}

export function drawThrusterParticles(
  ctx: CanvasRenderingContext2D,
  ent: ClientEntity,
  cameraX: number,
  cameraY: number,
): void {
  if (!ent.thrusterParticles) return;
  for (const p of ent.thrusterParticles) {
    const t = 1 - p.life / p.maxLife;
    const alpha = (1 - t * t) * 0.8;
    const sx = p.x - cameraX;
    const sy = p.y - cameraY;
    const size = p.size * (1 - t * 0.5);

    const r = Math.floor(200 * (1 - t * 0.8));
    const g = Math.floor(220 * (1 - t * 0.5));
    const b = 255;

    ctx.beginPath();
    ctx.arc(sx, sy, size, 0, Math.PI * 2);
    ctx.fillStyle = `rgba(${r}, ${g}, ${b}, ${alpha})`;
    ctx.shadowColor = `rgba(100, 160, 255, ${alpha * 0.5})`;
    ctx.shadowBlur = 3;
    ctx.fill();
    ctx.shadowBlur = 0;
  }
}

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

export function updateAndDrawExplosions(
  ctx: CanvasRenderingContext2D,
  canvas: HTMLCanvasElement,
  explosions: Explosion[],
  cameraX: number,
  cameraY: number,
  now: number,
): void {
  const dt = 1 / 60;

  for (let i = explosions.length - 1; i >= 0; i--) {
    const ex = explosions[i];
    const elapsed = (now - ex.startTime) / 1000;

    if (elapsed > ex.duration) {
      explosions.splice(i, 1);
      continue;
    }

    const sx = ex.x - cameraX;
    const sy = ex.y - cameraY;

    if (sx < -300 || sx > canvas.width + 300 || sy < -300 || sy > canvas.height + 300)
      continue;

    // Flash
    if (elapsed < ex.flashDuration) {
      const flashT = elapsed / ex.flashDuration;
      const flashAlpha = (1 - flashT) * 0.9;
      const r = ex.flashRadius * (0.5 + flashT * 0.5);
      const grad = ctx.createRadialGradient(sx, sy, 0, sx, sy, r);
      grad.addColorStop(0, `rgba(255, 220, 160, ${flashAlpha})`);
      grad.addColorStop(0.3, `rgba(255, 140, 40, ${flashAlpha * 0.7})`);
      grad.addColorStop(1, `rgba(255, 60, 10, 0)`);
      ctx.fillStyle = grad;
      ctx.beginPath();
      ctx.arc(sx, sy, r, 0, Math.PI * 2);
      ctx.fill();
    }

    // Shockwave ring
    if (elapsed < ex.shockDuration) {
      const shockT = elapsed / ex.shockDuration;
      const r = ex.shockRadius + (ex.shockMaxRadius - ex.shockRadius) * shockT;
      const alpha = (1 - shockT) * 0.6;
      ctx.beginPath();
      ctx.arc(sx, sy, r, 0, Math.PI * 2);
      ctx.strokeStyle = `rgba(255, 180, 80, ${alpha})`;
      ctx.lineWidth = 2 + (1 - shockT) * 3;
      ctx.shadowColor = `rgba(255, 120, 20, ${alpha * 0.8})`;
      ctx.shadowBlur = 10;
      ctx.stroke();
      ctx.shadowBlur = 0;
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
      const px = sx + p.x;
      const py = sy + p.y;

      if (p.type === "debris") {
        p.rot! += p.rotSpeed! * dt;
        ctx.save();
        ctx.translate(px, py);
        ctx.rotate(p.rot!);
        const r = Math.floor(p.color![0] + (200 - p.color![0]) * t * 0.5);
        const g = Math.floor(p.color![1] * (1 - t * 0.7));
        const b = Math.floor(p.color![2] * (1 - t * 0.9));
        ctx.fillStyle = `rgba(${r}, ${g}, ${b}, ${alpha})`;
        ctx.fillRect(-p.w! / 2, -p.h! / 2, p.w!, p.h!);
        if (t < 0.4) {
          ctx.shadowColor = `rgba(255, 160, 40, ${(0.4 - t) * 2})`;
          ctx.shadowBlur = 6;
          ctx.strokeStyle = `rgba(255, 200, 100, ${(0.4 - t) * 1.5})`;
          ctx.lineWidth = 1;
          ctx.strokeRect(-p.w! / 2, -p.h! / 2, p.w!, p.h!);
          ctx.shadowBlur = 0;
        }
        ctx.restore();
      } else if (p.type === "spark") {
        const r2 = 255;
        const g2 = Math.floor(255 * (1 - t * 0.8));
        const b2 = Math.floor(200 * (1 - t));
        ctx.fillStyle = `rgba(${r2}, ${g2}, ${b2}, ${alpha})`;
        ctx.shadowColor = `rgba(${r2}, ${g2}, 50, ${alpha * 0.6})`;
        ctx.shadowBlur = 4;
        ctx.beginPath();
        ctx.arc(px, py, p.size! * (1 - t * 0.5), 0, Math.PI * 2);
        ctx.fill();
        ctx.shadowBlur = 0;
      } else if (p.type === "flame") {
        const r3 = p.radius! * (1 + t * 2);
        const flameAlpha = alpha * 0.5;
        const grad = ctx.createRadialGradient(px, py, 0, px, py, r3);
        grad.addColorStop(0, `rgba(255, 200, 60, ${flameAlpha})`);
        grad.addColorStop(0.5, `rgba(255, 80, 10, ${flameAlpha * 0.5})`);
        grad.addColorStop(1, `rgba(100, 20, 0, 0)`);
        ctx.fillStyle = grad;
        ctx.beginPath();
        ctx.arc(px, py, r3, 0, Math.PI * 2);
        ctx.fill();
      }
    }
  }
}

import { ResourceType } from "@gen/game_pb.js";

export function drawMinable(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  radius: number,
  resourceType: ResourceType,
  resourceRemaining: number,
  color: string,
  rotation: number,
): void {
  switch (resourceType) {
    case ResourceType.ORE:
      drawOreAsteroid(ctx, x, y, radius, rotation);
      break;
    case ResourceType.CRYSTAL:
      drawCrystalAsteroid(ctx, x, y, radius, rotation);
      break;
    case ResourceType.GAS:
      drawGasCloud(ctx, x, y, radius);
      break;
    case ResourceType.METAL:
      drawMetalAsteroid(ctx, x, y, radius, rotation);
      break;
    default:
      drawOreAsteroid(ctx, x, y, radius, rotation);
      break;
  }
}

function drawOreAsteroid(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  radius: number,
  rotation: number,
): void {
  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(rotation);

  // Rocky body
  const sides = 10;
  ctx.beginPath();
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    const jag = 0.72 + Math.sin(i * 5.1 + 2.3) * 0.18 + Math.cos(i * 3.7) * 0.1;
    const r = radius * jag;
    const px = Math.cos(angle) * r;
    const py = Math.sin(angle) * r;
    if (i === 0) ctx.moveTo(px, py);
    else ctx.lineTo(px, py);
  }
  ctx.closePath();
  ctx.fillStyle = "#3a2a15";
  ctx.fill();
  ctx.strokeStyle = "#c90";
  ctx.lineWidth = 1.5;
  ctx.stroke();

  // Surface cracks / veins
  for (let i = 0; i < 3; i++) {
    const a1 = i * 2.2 + 0.5;
    const a2 = a1 + 0.6 + Math.sin(i * 4.1) * 0.3;
    ctx.beginPath();
    ctx.moveTo(Math.cos(a1) * radius * 0.2, Math.sin(a1) * radius * 0.2);
    ctx.lineTo(Math.cos(a2) * radius * 0.6, Math.sin(a2) * radius * 0.6);
    ctx.strokeStyle = "rgba(255, 180, 40, 0.5)";
    ctx.lineWidth = 1.5;
    ctx.stroke();
  }

  // Ore glint highlights
  for (let i = 0; i < 4; i++) {
    const ga = i * 1.7 + 0.8;
    const gr = radius * (0.25 + Math.sin(i * 3.3) * 0.2);
    const gx = Math.cos(ga) * gr;
    const gy = Math.sin(ga) * gr;
    ctx.beginPath();
    ctx.arc(gx, gy, radius * 0.06, 0, Math.PI * 2);
    ctx.fillStyle = "rgba(255, 200, 60, 0.7)";
    ctx.fill();
  }

  ctx.restore();
}

function drawCrystalAsteroid(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  radius: number,
  rotation: number,
): void {
  const now = performance.now();
  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(rotation);

  // Core glow
  const glow = ctx.createRadialGradient(0, 0, 0, 0, 0, radius * 0.6);
  glow.addColorStop(0, "rgba(170, 68, 255, 0.25)");
  glow.addColorStop(1, "rgba(170, 68, 255, 0)");
  ctx.fillStyle = glow;
  ctx.beginPath();
  ctx.arc(0, 0, radius * 0.6, 0, Math.PI * 2);
  ctx.fill();

  // Crystal spikes
  const spikes = 6;
  for (let i = 0; i < spikes; i++) {
    const angle = (i / spikes) * Math.PI * 2;
    const len = radius * (0.7 + Math.sin(i * 2.9 + 1.1) * 0.3);
    const width = radius * (0.12 + Math.sin(i * 4.3) * 0.05);
    const shimmer = 0.5 + 0.3 * Math.sin(now * 0.003 + i * 1.5);

    ctx.save();
    ctx.rotate(angle);

    ctx.beginPath();
    ctx.moveTo(len, 0);
    ctx.lineTo(len * 0.3, -width);
    ctx.lineTo(0, 0);
    ctx.lineTo(len * 0.3, width);
    ctx.closePath();

    ctx.fillStyle = `rgba(180, 100, 255, ${0.25 + shimmer * 0.2})`;
    ctx.fill();
    ctx.strokeStyle = `rgba(200, 140, 255, ${0.6 + shimmer * 0.3})`;
    ctx.lineWidth = 1;
    ctx.stroke();

    ctx.restore();
  }

  // Center facet
  ctx.beginPath();
  for (let i = 0; i < spikes; i++) {
    const angle = (i / spikes) * Math.PI * 2;
    const r = radius * 0.22;
    const px = Math.cos(angle) * r;
    const py = Math.sin(angle) * r;
    if (i === 0) ctx.moveTo(px, py);
    else ctx.lineTo(px, py);
  }
  ctx.closePath();
  ctx.fillStyle = "rgba(200, 150, 255, 0.4)";
  ctx.fill();
  ctx.strokeStyle = "rgba(220, 180, 255, 0.7)";
  ctx.lineWidth = 1;
  ctx.stroke();

  ctx.restore();
}

function drawGasCloud(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  radius: number,
): void {
  const now = performance.now();
  ctx.save();

  // Multiple layered translucent blobs
  const blobs = 5;
  for (let i = 0; i < blobs; i++) {
    const phase = now * 0.0008 + i * 1.3;
    const drift = Math.sin(phase) * radius * 0.15;
    const driftY = Math.cos(phase * 0.7 + i) * radius * 0.12;
    const blobR = radius * (0.5 + Math.sin(i * 2.1 + 0.5) * 0.25);
    const bx = x + Math.cos(i * 1.26) * radius * 0.3 + drift;
    const by = y + Math.sin(i * 1.26) * radius * 0.3 + driftY;

    const grad = ctx.createRadialGradient(bx, by, 0, bx, by, blobR);
    grad.addColorStop(0, `rgba(60, 200, 240, ${0.15 + Math.sin(phase) * 0.05})`);
    grad.addColorStop(0.5, `rgba(40, 160, 220, ${0.08})`);
    grad.addColorStop(1, "rgba(40, 160, 220, 0)");
    ctx.fillStyle = grad;
    ctx.beginPath();
    ctx.arc(bx, by, blobR, 0, Math.PI * 2);
    ctx.fill();
  }

  // Bright core
  const coreGrad = ctx.createRadialGradient(x, y, 0, x, y, radius * 0.35);
  coreGrad.addColorStop(0, "rgba(100, 230, 255, 0.3)");
  coreGrad.addColorStop(1, "rgba(60, 200, 240, 0)");
  ctx.fillStyle = coreGrad;
  ctx.beginPath();
  ctx.arc(x, y, radius * 0.35, 0, Math.PI * 2);
  ctx.fill();

  // Wisp particles
  for (let i = 0; i < 6; i++) {
    const wPhase = now * 0.001 + i * 1.05;
    const wa = (i / 6) * Math.PI * 2 + Math.sin(wPhase) * 0.5;
    const wr = radius * (0.3 + Math.sin(wPhase * 0.8) * 0.3);
    const wx = x + Math.cos(wa) * wr;
    const wy = y + Math.sin(wa) * wr;
    const walpha = 0.3 + 0.2 * Math.sin(wPhase * 1.3);
    ctx.beginPath();
    ctx.arc(wx, wy, radius * 0.04, 0, Math.PI * 2);
    ctx.fillStyle = `rgba(150, 240, 255, ${walpha})`;
    ctx.fill();
  }

  // Outer boundary ring
  ctx.beginPath();
  ctx.arc(x, y, radius, 0, Math.PI * 2);
  ctx.strokeStyle = "rgba(60, 200, 240, 0.12)";
  ctx.lineWidth = 1;
  ctx.setLineDash([4, 6]);
  ctx.stroke();
  ctx.setLineDash([]);

  ctx.restore();
}

function drawMetalAsteroid(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  radius: number,
  rotation: number,
): void {
  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(rotation);

  // Main body
  const sides = 7;
  const points: { x: number; y: number }[] = [];
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    const r = radius * (0.85 + Math.sin(i * 2.6 + 1.0) * 0.12);
    points.push({ x: Math.cos(angle) * r, y: Math.sin(angle) * r });
  }

  ctx.beginPath();
  for (let i = 0; i < points.length; i++) {
    if (i === 0) ctx.moveTo(points[i].x, points[i].y);
    else ctx.lineTo(points[i].x, points[i].y);
  }
  ctx.closePath();
  ctx.fillStyle = "#2a2a2e";
  ctx.fill();
  ctx.strokeStyle = "#999";
  ctx.lineWidth = 1.5;
  ctx.stroke();

  // Facet lines
  for (let i = 0; i < points.length; i++) {
    ctx.beginPath();
    ctx.moveTo(0, 0);
    ctx.lineTo(points[i].x * 0.95, points[i].y * 0.95);
    ctx.strokeStyle = "rgba(180, 180, 190, 0.2)";
    ctx.lineWidth = 1;
    ctx.stroke();
  }

  // Reflective highlight panels
  for (let i = 0; i < points.length; i++) {
    const next = (i + 1) % points.length;
    if (i % 2 === 0) {
      ctx.beginPath();
      ctx.moveTo(0, 0);
      ctx.lineTo(points[i].x, points[i].y);
      ctx.lineTo(points[next].x, points[next].y);
      ctx.closePath();
      ctx.fillStyle = "rgba(200, 200, 210, 0.08)";
      ctx.fill();
    }
  }

  // Specular highlight
  ctx.beginPath();
  ctx.arc(-radius * 0.2, -radius * 0.2, radius * 0.15, 0, Math.PI * 2);
  ctx.fillStyle = "rgba(255, 255, 255, 0.15)";
  ctx.fill();

  // Small rivets/bolts
  for (let i = 0; i < points.length; i++) {
    ctx.beginPath();
    ctx.arc(points[i].x * 0.7, points[i].y * 0.7, radius * 0.03, 0, Math.PI * 2);
    ctx.fillStyle = "rgba(160, 160, 170, 0.6)";
    ctx.fill();
  }

  ctx.restore();
}

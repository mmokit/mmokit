export function drawLootCrate(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  radius: number,
): void {
  const now = performance.now();
  const spin = now * 0.002;
  const pulse = 0.7 + 0.3 * Math.sin(now * 0.004);
  const r = radius * 2;

  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(spin);

  // Diamond / rhombus shape
  ctx.beginPath();
  ctx.moveTo(0, -r);
  ctx.lineTo(r * 0.6, 0);
  ctx.lineTo(0, r);
  ctx.lineTo(-r * 0.6, 0);
  ctx.closePath();

  ctx.fillStyle = `rgba(255, 221, 0, ${0.15 * pulse})`;
  ctx.fill();
  ctx.strokeStyle = `rgba(255, 221, 0, ${0.8 * pulse})`;
  ctx.lineWidth = 2;
  ctx.shadowColor = "#fd0";
  ctx.shadowBlur = 8 * pulse;
  ctx.stroke();
  ctx.shadowBlur = 0;

  ctx.restore();
}

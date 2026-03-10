export function drawProjectile(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  rotation: number,
  color: string,
): void {
  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(rotation);

  ctx.beginPath();
  ctx.moveTo(6, 0);
  ctx.lineTo(-3, -2);
  ctx.lineTo(-3, 2);
  ctx.closePath();
  ctx.fillStyle = color;
  ctx.fill();

  ctx.shadowColor = color;
  ctx.shadowBlur = 8;
  ctx.fill();
  ctx.shadowBlur = 0;

  ctx.restore();
}

export function drawStation(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  radius: number,
): void {
  const now = performance.now();
  const slowSpin = now * 0.0002;
  const pulse = 0.7 + 0.3 * Math.sin(now * 0.002);
  const sides = 8;
  const r = radius * 1.6;

  ctx.save();

  // Ambient glow
  const ambientGrad = ctx.createRadialGradient(x, y, 0, x, y, r * 1.8);
  ambientGrad.addColorStop(0, `rgba(100, 255, 140, 0.06)`);
  ambientGrad.addColorStop(0.5, `rgba(80, 200, 120, 0.03)`);
  ambientGrad.addColorStop(1, "rgba(0, 0, 0, 0)");
  ctx.fillStyle = ambientGrad;
  ctx.beginPath();
  ctx.arc(x, y, r * 1.8, 0, Math.PI * 2);
  ctx.fill();

  // Outer ring (octagon)
  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(slowSpin);

  // Outer octagon fill
  ctx.beginPath();
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    const px = Math.cos(angle) * r;
    const py = Math.sin(angle) * r;
    if (i === 0) ctx.moveTo(px, py);
    else ctx.lineTo(px, py);
  }
  ctx.closePath();
  ctx.fillStyle = "rgba(100, 255, 140, 0.04)";
  ctx.fill();
  ctx.strokeStyle = "rgba(136, 255, 136, 0.7)";
  ctx.lineWidth = 2;
  ctx.stroke();

  // Outer ring double-line detail
  ctx.beginPath();
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    const px = Math.cos(angle) * r * 0.92;
    const py = Math.sin(angle) * r * 0.92;
    if (i === 0) ctx.moveTo(px, py);
    else ctx.lineTo(px, py);
  }
  ctx.closePath();
  ctx.strokeStyle = "rgba(136, 255, 136, 0.25)";
  ctx.lineWidth = 1;
  ctx.stroke();

  // Struts from outer ring to inner hub
  for (let i = 0; i < sides; i += 2) {
    const angle = (i / sides) * Math.PI * 2;
    const ox = Math.cos(angle) * r * 0.92;
    const oy = Math.sin(angle) * r * 0.92;
    const ix = Math.cos(angle) * r * 0.38;
    const iy = Math.sin(angle) * r * 0.38;
    ctx.beginPath();
    ctx.moveTo(ox, oy);
    ctx.lineTo(ix, iy);
    ctx.strokeStyle = "rgba(136, 255, 136, 0.4)";
    ctx.lineWidth = 2;
    ctx.stroke();

    // Strut panel fill
    const perpAngle = angle + Math.PI / 2;
    const pw = r * 0.025;
    ctx.beginPath();
    ctx.moveTo(ox + Math.cos(perpAngle) * pw, oy + Math.sin(perpAngle) * pw);
    ctx.lineTo(ix + Math.cos(perpAngle) * pw * 0.5, iy + Math.sin(perpAngle) * pw * 0.5);
    ctx.lineTo(ix - Math.cos(perpAngle) * pw * 0.5, iy - Math.sin(perpAngle) * pw * 0.5);
    ctx.lineTo(ox - Math.cos(perpAngle) * pw, oy - Math.sin(perpAngle) * pw);
    ctx.closePath();
    ctx.fillStyle = "rgba(136, 255, 136, 0.08)";
    ctx.fill();
  }

  // Solar panel arrays
  for (let i = 1; i < sides; i += 2) {
    const angle = (i / sides) * Math.PI * 2;
    const bx = Math.cos(angle) * r;
    const by = Math.sin(angle) * r;
    const panelLen = r * 0.35;
    const panelW = r * 0.06;

    ctx.save();
    ctx.translate(bx, by);
    ctx.rotate(angle);

    // Panel arm
    ctx.beginPath();
    ctx.moveTo(0, 0);
    ctx.lineTo(panelLen, 0);
    ctx.strokeStyle = "rgba(136, 255, 136, 0.3)";
    ctx.lineWidth = 1;
    ctx.stroke();

    // Panel rectangles
    for (let s = 0; s < 2; s++) {
      const px = panelLen * 0.35 + s * panelLen * 0.35;
      ctx.fillStyle = `rgba(80, 180, 120, ${0.12 + pulse * 0.06})`;
      ctx.fillRect(px - panelLen * 0.14, -panelW, panelLen * 0.28, panelW * 2);
      ctx.strokeStyle = "rgba(136, 255, 136, 0.4)";
      ctx.lineWidth = 1;
      ctx.strokeRect(px - panelLen * 0.14, -panelW, panelLen * 0.28, panelW * 2);
      // Panel grid lines
      ctx.beginPath();
      ctx.moveTo(px, -panelW);
      ctx.lineTo(px, panelW);
      ctx.strokeStyle = "rgba(136, 255, 136, 0.2)";
      ctx.stroke();
    }

    ctx.restore();
  }

  // Inner hub — hexagon
  const hubSides = 6;
  const hubR = r * 0.35;
  ctx.beginPath();
  for (let i = 0; i < hubSides; i++) {
    const angle = (i / hubSides) * Math.PI * 2;
    const px = Math.cos(angle) * hubR;
    const py = Math.sin(angle) * hubR;
    if (i === 0) ctx.moveTo(px, py);
    else ctx.lineTo(px, py);
  }
  ctx.closePath();
  ctx.fillStyle = "rgba(100, 255, 140, 0.08)";
  ctx.fill();
  ctx.strokeStyle = "rgba(136, 255, 136, 0.6)";
  ctx.lineWidth = 1.5;
  ctx.stroke();

  // Inner detail ring
  ctx.beginPath();
  ctx.arc(0, 0, hubR * 0.55, 0, Math.PI * 2);
  ctx.strokeStyle = "rgba(136, 255, 136, 0.3)";
  ctx.lineWidth = 1;
  ctx.stroke();

  // Center core — pulsing dot
  ctx.beginPath();
  ctx.arc(0, 0, r * 0.06, 0, Math.PI * 2);
  ctx.fillStyle = `rgba(150, 255, 180, ${0.5 + pulse * 0.5})`;
  ctx.shadowColor = "rgba(100, 255, 140, 0.8)";
  ctx.shadowBlur = 8;
  ctx.fill();
  ctx.shadowBlur = 0;

  // Docking port markers
  for (let i = 0; i < sides; i++) {
    const midAngle = ((i + 0.5) / sides) * Math.PI * 2;
    const dx = Math.cos(midAngle) * r;
    const dy = Math.sin(midAngle) * r;

    ctx.save();
    ctx.translate(dx, dy);
    ctx.rotate(midAngle);
    ctx.fillStyle = `rgba(136, 255, 136, ${0.15 + pulse * 0.1})`;
    ctx.fillRect(-3, -1.5, 6, 3);
    ctx.restore();
  }

  // Blinking nav lights
  const blinkOn = Math.sin(now * 0.005) > 0;
  const blinkOn2 = Math.sin(now * 0.005 + Math.PI) > 0;
  for (let i = 0; i < sides; i += 2) {
    const angle = (i / sides) * Math.PI * 2;
    const lx = Math.cos(angle) * r * 0.98;
    const ly = Math.sin(angle) * r * 0.98;
    const on = i % 4 === 0 ? blinkOn : blinkOn2;
    if (on) {
      ctx.beginPath();
      ctx.arc(lx, ly, 2, 0, Math.PI * 2);
      ctx.fillStyle = i % 4 === 0 ? "rgba(255, 80, 80, 0.9)" : "rgba(80, 130, 255, 0.9)";
      ctx.shadowColor = ctx.fillStyle;
      ctx.shadowBlur = 6;
      ctx.fill();
      ctx.shadowBlur = 0;
    }
  }

  ctx.restore(); // undo translate+rotate

  // Label (unrotated)
  const labelY = y - r * 1.4;
  ctx.font = "bold 14px monospace";
  ctx.textAlign = "center";
  ctx.fillStyle = `rgba(136, 255, 136, ${0.6 + pulse * 0.3})`;
  ctx.fillText("TRADE STATION", x, labelY - 14);
  ctx.font = "10px monospace";
  ctx.fillStyle = "rgba(136, 255, 136, 0.4)";
  ctx.fillText("[ PRESS E TO SELL ]", x, labelY);
  ctx.textAlign = "left";

  ctx.restore();
}

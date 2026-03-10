export function drawShip(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  rotation: number,
  w: number,
  h: number,
  color: string,
  isMe: boolean,
  health: number,
  shield: number,
  pilotName: string,
  thrusting: boolean,
): void {
  const hw = (w || 60) / 2;
  const hh = (h || 30) / 2;
  const now = performance.now();
  const baseColor = isMe ? "#0f0" : color;
  const pulse = 0.7 + 0.3 * Math.sin(now * 0.004);

  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(rotation);

  // === Ion thruster exhaust (only when thrusting) ===
  if (thrusting) {
    const flicker = 0.7 + 0.3 * Math.sin(now * 0.015 + Math.random() * 0.5);

    // Outer exhaust plume
    ctx.beginPath();
    ctx.moveTo(-hw * 0.7, -hh * 0.55);
    ctx.lineTo(-hw * (1.3 + flicker * 0.3), 0);
    ctx.lineTo(-hw * 0.7, hh * 0.55);
    ctx.closePath();
    ctx.fillStyle = `rgba(60, 120, 255, 0.1)`;
    ctx.fill();

    // Main exhaust glow
    ctx.beginPath();
    ctx.moveTo(-hw * 0.7, -hh * 0.4);
    ctx.lineTo(-hw * (1.1 + flicker * 0.25), 0);
    ctx.lineTo(-hw * 0.7, hh * 0.4);
    ctx.closePath();
    ctx.fillStyle = `rgba(80, 150, 255, ${0.2 + flicker * 0.25})`;
    ctx.shadowColor = "rgba(80, 150, 255, 0.7)";
    ctx.shadowBlur = 10;
    ctx.fill();
    ctx.shadowBlur = 0;

    // Hot core
    ctx.beginPath();
    ctx.moveTo(-hw * 0.72, -hh * 0.12);
    ctx.lineTo(-hw * (0.9 + flicker * 0.15), 0);
    ctx.lineTo(-hw * 0.72, hh * 0.12);
    ctx.closePath();
    ctx.fillStyle = `rgba(200, 230, 255, ${0.3 + flicker * 0.4})`;
    ctx.fill();
  }

  // === Main hull ===
  function hullPath() {
    ctx.beginPath();
    ctx.moveTo(hw, 0);
    ctx.lineTo(hw * 0.55, -hh * 0.42);
    ctx.lineTo(hw * 0.1, -hh * 0.52);
    ctx.lineTo(-hw * 0.1, -hh * 0.6);
    ctx.lineTo(-hw * 0.25, -hh * 1.25);
    ctx.lineTo(-hw * 0.55, -hh * 1.1);
    ctx.lineTo(-hw * 0.7, -hh * 0.5);
    ctx.lineTo(-hw * 0.7, hh * 0.5);
    ctx.lineTo(-hw * 0.55, hh * 1.1);
    ctx.lineTo(-hw * 0.25, hh * 1.25);
    ctx.lineTo(-hw * 0.1, hh * 0.6);
    ctx.lineTo(hw * 0.1, hh * 0.52);
    ctx.lineTo(hw * 0.55, hh * 0.42);
    ctx.closePath();
  }

  // Hull fill
  hullPath();
  ctx.fillStyle = baseColor;
  ctx.globalAlpha = 0.1;
  ctx.fill();
  ctx.globalAlpha = 1;

  // Hull stroke
  hullPath();
  ctx.strokeStyle = baseColor;
  ctx.lineWidth = 2;
  ctx.stroke();

  // === Hull panel lines ===
  ctx.strokeStyle = baseColor;
  ctx.lineWidth = 1;
  ctx.globalAlpha = 0.2;

  // Center spine
  ctx.beginPath();
  ctx.moveTo(hw * 0.6, 0);
  ctx.lineTo(-hw * 0.65, 0);
  ctx.stroke();

  // Forward cross-brace
  ctx.beginPath();
  ctx.moveTo(hw * 0.3, -hh * 0.35);
  ctx.lineTo(hw * 0.3, hh * 0.35);
  ctx.stroke();

  // Mid cross-brace
  ctx.beginPath();
  ctx.moveTo(-hw * 0.1, -hh * 0.55);
  ctx.lineTo(-hw * 0.1, hh * 0.55);
  ctx.stroke();

  // Aft cross-brace
  ctx.beginPath();
  ctx.moveTo(-hw * 0.5, -hh * 0.7);
  ctx.lineTo(-hw * 0.5, hh * 0.7);
  ctx.stroke();

  ctx.globalAlpha = 1;

  // === Wing detail ===
  ctx.globalAlpha = 0.3;
  ctx.strokeStyle = baseColor;
  ctx.lineWidth = 1;

  // Top wing spar
  ctx.beginPath();
  ctx.moveTo(hw * 0.0, -hh * 0.55);
  ctx.lineTo(-hw * 0.45, -hh * 1.05);
  ctx.stroke();

  // Top wing rib
  ctx.beginPath();
  ctx.moveTo(-hw * 0.18, -hh * 0.9);
  ctx.lineTo(-hw * 0.38, -hh * 0.7);
  ctx.stroke();

  // Bottom wing spar
  ctx.beginPath();
  ctx.moveTo(hw * 0.0, hh * 0.55);
  ctx.lineTo(-hw * 0.45, hh * 1.05);
  ctx.stroke();

  // Bottom wing rib
  ctx.beginPath();
  ctx.moveTo(-hw * 0.18, hh * 0.9);
  ctx.lineTo(-hw * 0.38, hh * 0.7);
  ctx.stroke();

  // Wing tip accents
  ctx.fillStyle = baseColor;
  ctx.globalAlpha = 0.15;
  // Top wing panel fill
  ctx.beginPath();
  ctx.moveTo(-hw * 0.1, -hh * 0.6);
  ctx.lineTo(-hw * 0.25, -hh * 1.25);
  ctx.lineTo(-hw * 0.55, -hh * 1.1);
  ctx.lineTo(-hw * 0.35, -hh * 0.6);
  ctx.closePath();
  ctx.fill();
  // Bottom wing panel fill
  ctx.beginPath();
  ctx.moveTo(-hw * 0.1, hh * 0.6);
  ctx.lineTo(-hw * 0.25, hh * 1.25);
  ctx.lineTo(-hw * 0.55, hh * 1.1);
  ctx.lineTo(-hw * 0.35, hh * 0.6);
  ctx.closePath();
  ctx.fill();

  ctx.globalAlpha = 1;

  // === Cockpit canopy ===
  ctx.beginPath();
  ctx.moveTo(hw * 0.8, 0);
  ctx.lineTo(hw * 0.35, -hh * 0.22);
  ctx.lineTo(hw * 0.15, -hh * 0.18);
  ctx.lineTo(hw * 0.15, hh * 0.18);
  ctx.lineTo(hw * 0.35, hh * 0.22);
  ctx.closePath();
  ctx.fillStyle = baseColor;
  ctx.globalAlpha = 0.35;
  ctx.fill();
  ctx.globalAlpha = 1;
  ctx.strokeStyle = baseColor;
  ctx.lineWidth = 1;
  ctx.globalAlpha = 0.5;
  ctx.stroke();
  ctx.globalAlpha = 1;

  // Cockpit frame line
  ctx.beginPath();
  ctx.moveTo(hw * 0.5, -hh * 0.12);
  ctx.lineTo(hw * 0.5, hh * 0.12);
  ctx.strokeStyle = baseColor;
  ctx.globalAlpha = 0.3;
  ctx.lineWidth = 1;
  ctx.stroke();
  ctx.globalAlpha = 1;

  // === Engine section ===

  // Engine housing top
  ctx.fillStyle = baseColor;
  ctx.globalAlpha = 0.15;
  ctx.fillRect(-hw * 0.72, -hh * 0.48, hw * 0.12, hh * 0.35);
  ctx.globalAlpha = 1;
  ctx.strokeStyle = baseColor;
  ctx.globalAlpha = 0.4;
  ctx.lineWidth = 1;
  ctx.strokeRect(-hw * 0.72, -hh * 0.48, hw * 0.12, hh * 0.35);
  ctx.globalAlpha = 1;

  // Engine housing bottom
  ctx.fillStyle = baseColor;
  ctx.globalAlpha = 0.15;
  ctx.fillRect(-hw * 0.72, hh * 0.13, hw * 0.12, hh * 0.35);
  ctx.globalAlpha = 1;
  ctx.strokeStyle = baseColor;
  ctx.globalAlpha = 0.4;
  ctx.lineWidth = 1;
  ctx.strokeRect(-hw * 0.72, hh * 0.13, hw * 0.12, hh * 0.35);
  ctx.globalAlpha = 1;

  // Engine nozzle glow
  const nozzleAlpha = thrusting ? 0.6 + pulse * 0.4 : 0.15;
  ctx.fillStyle = `rgba(80, 150, 255, ${nozzleAlpha})`;
  ctx.fillRect(-hw * 0.73, -hh * 0.38, hw * 0.04, hh * 0.22);
  ctx.fillRect(-hw * 0.73, hh * 0.16, hw * 0.04, hh * 0.22);
  if (thrusting) {
    ctx.shadowColor = "rgba(80, 150, 255, 0.6)";
    ctx.shadowBlur = 5;
    ctx.fillRect(-hw * 0.73, -hh * 0.38, hw * 0.04, hh * 0.22);
    ctx.fillRect(-hw * 0.73, hh * 0.16, hw * 0.04, hh * 0.22);
    ctx.shadowBlur = 0;
  }

  // === Nav lights ===
  const navAlpha = 0.5 + pulse * 0.3;

  // Wing tip lights
  for (const wy of [-hh * 1.23, hh * 1.23]) {
    ctx.beginPath();
    ctx.arc(-hw * 0.25, wy, 1.5, 0, Math.PI * 2);
    ctx.fillStyle = `rgba(255, 255, 255, ${navAlpha})`;
    ctx.shadowColor = "rgba(255, 255, 255, 0.6)";
    ctx.shadowBlur = 5;
    ctx.fill();
    ctx.shadowBlur = 0;
  }

  // Nose light
  ctx.beginPath();
  ctx.arc(hw * 0.95, 0, 1, 0, Math.PI * 2);
  ctx.fillStyle = `rgba(255, 255, 255, ${navAlpha})`;
  ctx.shadowColor = "rgba(255, 255, 255, 0.6)";
  ctx.shadowBlur = 4;
  ctx.fill();
  ctx.shadowBlur = 0;

  // Tail light
  ctx.beginPath();
  ctx.arc(-hw * 0.68, 0, 1.5, 0, Math.PI * 2);
  ctx.fillStyle = `rgba(255, 255, 255, ${navAlpha * 0.7})`;
  ctx.shadowColor = "rgba(255, 255, 255, 0.4)";
  ctx.shadowBlur = 4;
  ctx.fill();
  ctx.shadowBlur = 0;

  // === Hull accent — nose stripe ===
  ctx.beginPath();
  ctx.moveTo(hw * 0.85, 0);
  ctx.lineTo(hw * 0.55, -hh * 0.4);
  ctx.strokeStyle = baseColor;
  ctx.globalAlpha = 0.15;
  ctx.lineWidth = 2;
  ctx.stroke();
  ctx.beginPath();
  ctx.moveTo(hw * 0.85, 0);
  ctx.lineTo(hw * 0.55, hh * 0.4);
  ctx.stroke();
  ctx.globalAlpha = 1;

  ctx.restore();

  // === Screen-space indicators (unrotated) ===
  const barW = Math.max(hw * 2, 40);
  const barH = 3;
  const barGap = 2;
  const barX = x - barW / 2;
  const shipTop = y - Math.sqrt(hw * hw + hh * hh) - 8;

  // HP bar
  const hpY = shipTop - barH;
  ctx.fillStyle = "rgba(255,60,60,0.15)";
  ctx.fillRect(barX, hpY, barW, barH);
  const hpColor = health > 0.3 ? "rgba(255,60,60,0.9)" : "rgba(255,30,30,1)";
  ctx.fillStyle = hpColor;
  ctx.fillRect(barX, hpY, barW * health, barH);
  if (health <= 0.3) {
    ctx.shadowColor = "rgba(255,30,30,0.6)";
    ctx.shadowBlur = 6;
    ctx.fillRect(barX, hpY, barW * health, barH);
    ctx.shadowBlur = 0;
  }

  // Shield bar
  const shY = hpY - barH - barGap;
  ctx.fillStyle = "rgba(80,130,255,0.15)";
  ctx.fillRect(barX, shY, barW, barH);
  if (shield > 0) {
    ctx.fillStyle = "rgba(80,130,255,0.9)";
    ctx.shadowColor = "rgba(80,130,255,0.5)";
    ctx.shadowBlur = 4;
    ctx.fillRect(barX, shY, barW * shield, barH);
    ctx.shadowBlur = 0;
  }

  // Pilot name
  if (pilotName) {
    const nameY = shY - 4;
    ctx.font = "10px monospace";
    ctx.textAlign = "center";
    ctx.textBaseline = "bottom";
    ctx.fillStyle = isMe ? "rgba(0,255,0,0.8)" : "rgba(200,220,255,0.7)";
    ctx.fillText(pilotName, x, nameY);
  }
}

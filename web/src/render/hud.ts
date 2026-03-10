import { EntityType } from "@gen/game_pb.js";
import { MAX_CARGO, RESOURCE_COLORS, RESOURCE_NAMES, TOAST_DURATION } from "../constants";
import type { GameState } from "../state";
import type { ClientEntity } from "../types";

export function drawStatusBars(
  ctx: CanvasRenderingContext2D,
  canvas: HTMLCanvasElement,
  myEntity: ClientEntity,
): void {
  const barW = 200;
  const barH = 14;
  const bx = canvas.width / 2 - barW / 2;
  const by = canvas.height - 60;
  const hp = myEntity.curr.health;
  const sh = myEntity.curr.shield;

  // Shield bar
  ctx.fillStyle = "rgba(80,130,255,0.2)";
  ctx.fillRect(bx, by, barW, barH);
  ctx.fillStyle = "rgba(80,130,255,0.8)";
  ctx.fillRect(bx, by, barW * sh, barH);
  ctx.strokeStyle = "rgba(80,130,255,0.5)";
  ctx.lineWidth = 1;
  ctx.strokeRect(bx, by, barW, barH);
  ctx.fillStyle = "#fff";
  ctx.font = "11px monospace";
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillText(`SHIELD ${(sh * 100).toFixed(0)}%`, bx + barW / 2, by + barH / 2);

  // Health bar
  const hy = by + barH + 4;
  ctx.fillStyle = "rgba(255,60,60,0.2)";
  ctx.fillRect(bx, hy, barW, barH);
  ctx.fillStyle = hp > 0.3 ? "rgba(255,60,60,0.8)" : "rgba(255,30,30,1)";
  ctx.fillRect(bx, hy, barW * hp, barH);
  ctx.strokeStyle = "rgba(255,60,60,0.5)";
  ctx.lineWidth = 1;
  ctx.strokeRect(bx, hy, barW, barH);
  ctx.fillStyle = "#fff";
  ctx.fillText(`HP ${(hp * 100).toFixed(0)}%`, bx + barW / 2, hy + barH / 2);
  ctx.textAlign = "left";
  ctx.textBaseline = "alphabetic";

  // Cargo bar
  const res = myEntity.curr.resources;
  if (res && res.length >= 4) {
    const totalCargo = res.reduce((a: number, b: number) => a + b, 0);
    const cargoFrac = Math.min(totalCargo / MAX_CARGO, 1);
    const cargoFull = cargoFrac >= 1;

    const cy2 = hy + barH + 4;
    ctx.fillStyle = "rgba(200,150,50,0.2)";
    ctx.fillRect(bx, cy2, barW, barH);
    const cargoColor = cargoFull
      ? `rgba(255,60,60,${0.7 + 0.3 * Math.sin(performance.now() * 0.006)})`
      : "rgba(200,150,50,0.8)";
    ctx.fillStyle = cargoColor;
    ctx.fillRect(bx, cy2, barW * cargoFrac, barH);
    ctx.strokeStyle = cargoFull ? "rgba(255,60,60,0.7)" : "rgba(200,150,50,0.5)";
    ctx.lineWidth = 1;
    ctx.strokeRect(bx, cy2, barW, barH);
    ctx.fillStyle = "#fff";
    ctx.font = "11px monospace";
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    const cargoLabel = cargoFull
      ? "CARGO FULL"
      : `CARGO ${Math.floor(totalCargo)}/${MAX_CARGO}`;
    ctx.fillText(cargoLabel, bx + barW / 2, cy2 + barH / 2);
    ctx.textAlign = "left";
    ctx.textBaseline = "alphabetic";
  }
}

export function drawCargoPanel(
  ctx: CanvasRenderingContext2D,
  canvas: HTMLCanvasElement,
  myEntity: ClientEntity,
): void {
  const res = myEntity.curr.resources;
  if (!res || res.length < 4) return;

  const pw = 260;
  const rowH = 32;
  const ph = 30 + 4 * rowH + 40;
  const px = canvas.width / 2 - pw / 2;
  const py = canvas.height / 2 - ph / 2;

  // Panel background
  ctx.fillStyle = "rgba(10, 12, 20, 0.92)";
  ctx.fillRect(px, py, pw, ph);
  ctx.strokeStyle = "rgba(200, 150, 50, 0.6)";
  ctx.lineWidth = 1;
  ctx.strokeRect(px, py, pw, ph);

  // Title
  ctx.fillStyle = "#dda";
  ctx.font = "bold 14px monospace";
  ctx.textAlign = "center";
  ctx.fillText("CARGO HOLD", px + pw / 2, py + 22);

  const totalCargo = res.reduce((a: number, b: number) => a + b, 0);

  // Resource rows
  for (let i = 0; i < 4; i++) {
    const ry = py + 30 + i * rowH;
    const amount = res[i];
    const frac = amount / MAX_CARGO;

    // Bar background
    ctx.fillStyle = "rgba(255,255,255,0.05)";
    ctx.fillRect(px + 10, ry + 4, pw - 20, rowH - 8);

    // Bar fill
    ctx.fillStyle = RESOURCE_COLORS[i];
    ctx.globalAlpha = 0.3;
    ctx.fillRect(px + 10, ry + 4, (pw - 20) * frac, rowH - 8);
    ctx.globalAlpha = 1;

    // Label
    ctx.fillStyle = RESOURCE_COLORS[i];
    ctx.font = "13px monospace";
    ctx.textAlign = "left";
    ctx.fillText(`[${i + 1}] ${RESOURCE_NAMES[i]}`, px + 16, ry + 20);

    // Amount
    ctx.textAlign = "right";
    ctx.fillStyle = "#eee";
    ctx.fillText(Math.floor(amount).toString(), px + pw - 16, ry + 20);
  }

  // Footer
  const fy = py + 30 + 4 * rowH + 8;
  ctx.textAlign = "center";
  ctx.font = "11px monospace";
  ctx.fillStyle =
    totalCargo >= MAX_CARGO
      ? `rgba(255,80,80,${0.7 + 0.3 * Math.sin(performance.now() * 0.006)})`
      : "#888";
  ctx.fillText(
    `${Math.floor(totalCargo)} / ${MAX_CARGO}  |  Press 1-4 to jettison`,
    px + pw / 2,
    fy + 10,
  );
  ctx.textAlign = "left";
}

export function drawTargetHighlight(
  ctx: CanvasRenderingContext2D,
  state: GameState,
): void {
  if (!state.targetId || !state.entities.has(state.targetId)) return;

  const tgt = state.entities.get(state.targetId)!;
  const tx = tgt.renderX - state.cameraX;
  const ty = tgt.renderY - state.cameraY;
  const tr = (tgt.curr.radius || 20) + 8;

  ctx.save();

  if (tgt.curr.entityType === EntityType.LOOT_CRATE) {
    ctx.strokeStyle = "#fd0";
    ctx.lineWidth = 2;
    ctx.setLineDash([6, 4]);
    ctx.beginPath();
    ctx.arc(tx, ty, tr, 0, Math.PI * 2);
    ctx.stroke();
    ctx.setLineDash([]);

    ctx.font = "12px monospace";
    ctx.textAlign = "center";
    ctx.fillStyle = "#fd0";
    ctx.fillText("LOOT CRATE", tx, ty - tr - 14);
    const res = tgt.curr.resources;
    if (res && res.length >= 4) {
      let infoY = ty - tr - 2;
      for (let i = 0; i < 4; i++) {
        if (res[i] > 0) {
          ctx.fillStyle = RESOURCE_COLORS[i];
          ctx.fillText(`${RESOURCE_NAMES[i]}: ${Math.floor(res[i])}`, tx, infoY);
          infoY += 14;
        }
      }
    }
    ctx.textAlign = "left";
  } else {
    // Asteroid target
    const resType = tgt.curr.resourceType || 0;
    const resColor = RESOURCE_COLORS[resType] || "#a86";

    ctx.strokeStyle = resColor;
    ctx.lineWidth = 2;
    ctx.setLineDash([6, 4]);
    ctx.beginPath();
    ctx.arc(tx, ty, tr, 0, Math.PI * 2);
    ctx.stroke();
    ctx.setLineDash([]);

    const resName = RESOURCE_NAMES[resType] || "???";
    const remaining = Math.floor(tgt.curr.resourceRemaining || 0);
    ctx.font = "12px monospace";
    ctx.textAlign = "center";
    ctx.fillStyle = resColor;
    ctx.fillText(resName, tx, ty - tr - 14);
    ctx.fillStyle = "#ccc";
    ctx.fillText(`${remaining} remaining`, tx, ty - tr - 2);
    ctx.textAlign = "left";
  }

  ctx.restore();
}

export function drawMiningLasers(
  ctx: CanvasRenderingContext2D,
  state: GameState,
): void {
  for (const [, ent] of state.entities) {
    const e = ent.curr;
    if (e.miningActive && e.miningTargetId && state.entities.has(e.miningTargetId)) {
      const tgt = state.entities.get(e.miningTargetId)!;
      const sx = ent.renderX - state.cameraX;
      const sy = ent.renderY - state.cameraY;
      const tx = tgt.renderX - state.cameraX;
      const ty = tgt.renderY - state.cameraY;

      const pulse = 0.5 + 0.5 * Math.sin(performance.now() * 0.01);
      ctx.save();
      ctx.strokeStyle = `rgba(0, 255, 128, ${0.4 + pulse * 0.4})`;
      ctx.lineWidth = 2 + pulse;
      ctx.shadowColor = "rgba(0, 255, 128, 0.6)";
      ctx.shadowBlur = 8;
      ctx.beginPath();
      ctx.moveTo(sx, sy);
      ctx.lineTo(tx, ty);
      ctx.stroke();
      ctx.shadowBlur = 0;
      ctx.restore();
    }
  }
}

export function drawDeathScreen(
  ctx: CanvasRenderingContext2D,
  canvas: HTMLCanvasElement,
): void {
  ctx.fillStyle = "rgba(0, 0, 0, 0.6)";
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  ctx.fillStyle = "#f44";
  ctx.font = "bold 48px monospace";
  ctx.textAlign = "center";
  ctx.fillText("YOU DIED", canvas.width / 2, canvas.height / 2 - 30);

  ctx.fillStyle = "#aaa";
  ctx.font = "20px monospace";
  ctx.fillText("Press SPACE to respawn", canvas.width / 2, canvas.height / 2 + 20);
  ctx.textAlign = "left";
}

export function drawToasts(
  ctx: CanvasRenderingContext2D,
  canvas: HTMLCanvasElement,
  state: GameState,
): void {
  const toastNow = performance.now();
  state.toasts = state.toasts.filter((t) => toastNow - t.time < TOAST_DURATION);
  for (let i = 0; i < state.toasts.length; i++) {
    const t = state.toasts[i];
    const age = toastNow - t.time;
    let alpha = 1;
    if (age > 2000) alpha = 1 - (age - 2000) / 1000;
    ctx.save();
    ctx.font = "bold 18px monospace";
    ctx.textAlign = "center";
    ctx.fillStyle = `rgba(100, 255, 100, ${alpha})`;
    ctx.fillText(t.text, canvas.width / 2, canvas.height / 2 - 80 - i * 24);
    ctx.restore();
  }
}

export function drawStationPrompt(
  ctx: CanvasRenderingContext2D,
  canvas: HTMLCanvasElement,
  state: GameState,
  myEntity: ClientEntity,
): void {
  for (const [, ent] of state.entities) {
    if (ent.curr.entityType !== EntityType.STATION) continue;
    const dx = myEntity.renderX - ent.renderX;
    const dy = myEntity.renderY - ent.renderY;
    const dist = Math.sqrt(dx * dx + dy * dy);
    if (dist < 250) {
      ctx.save();
      ctx.font = "bold 14px monospace";
      ctx.textAlign = "center";
      ctx.fillStyle = "#8f8";
      const barY = canvas.height - 60;
      ctx.fillText("Press E to sell cargo", canvas.width / 2, barY - 20);
      ctx.restore();
      break;
    }
  }
}

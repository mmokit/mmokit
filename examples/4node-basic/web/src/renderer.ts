import { state } from "./state.js";
import { interpPos, getInterp, updatePrediction } from "./interpolation.js";

const NODE_COLORS = [
  { bg: "rgba(100,150,255,0.07)", fill: "#5588cc", stroke: "#6496FF", label: "node_0_0" },
  { bg: "rgba(255,150,100,0.07)", fill: "#cc8855", stroke: "#FF9664", label: "node_1_0" },
  { bg: "rgba(100,255,150,0.07)", fill: "#55cc88", stroke: "#64FF96", label: "node_0_1" },
  { bg: "rgba(255,100,255,0.07)", fill: "#cc55cc", stroke: "#FF64FF", label: "node_1_1" },
];

export function startRenderLoop(): void {
  requestAnimationFrame(renderLoop);
}

function renderLoop(now: number): void {
  requestAnimationFrame(renderLoop);

  // FPS counter.
  state.frameCount++;
  if (now - state.lastFpsTime >= 1000) {
    state.fps = Math.round((state.frameCount * 1000) / (now - state.lastFpsTime));
    state.frameCount = 0;
    state.lastFpsTime = now;
  }

  const canvas = document.getElementById("canvas") as HTMLCanvasElement;
  const ctx = canvas.getContext("2d")!;
  const W = canvas.width;
  const H = canvas.height;

  ctx.fillStyle = "#0a0a14";
  ctx.fillRect(0, 0, W, H);

  if (!state.playerNetID) return;

  const interp = getInterp();

  // Update client prediction.
  updatePrediction(now);
  state.lastFrameTime = now;

  // Camera position.
  const player = state.entities.get(state.playerNetID);
  let camX: number, camY: number;
  if (player) {
    if (state.predictionActive) {
      camX = state.predictedX;
      camY = state.predictedY;
    } else {
      camX = interpPos(player.prevX, player.worldX, player.velX, interp);
      camY = interpPos(player.prevY, player.worldY, player.velY, interp);
    }
  } else {
    camX = state.viewerX;
    camY = state.viewerY;
  }
  state.camX = camX;
  state.camY = camY;

  const scale = Math.min(W, H) / 3500;

  function worldToScreen(wx: number, wy: number): [number, number] {
    return [(wx - camX) * scale + W / 2, (wy - camY) * scale + H / 2];
  }

  // -- 1. Cell background tints & boundaries --
  const cs = state.cellSize;
  const gw = state.gridW;
  const gh = state.gridH;

  for (let cy = 0; cy < gh; cy++) {
    for (let cx = 0; cx < gw; cx++) {
      const nodeIdx = cy * gw + cx;
      const nc = NODE_COLORS[nodeIdx % NODE_COLORS.length];
      const [sx0, sy0] = worldToScreen(cx * cs, cy * cs);
      const [sx1, sy1] = worldToScreen(cx * cs + cs, cy * cs + cs);
      ctx.fillStyle = nc.bg;
      ctx.fillRect(sx0, sy0, sx1 - sx0, sy1 - sy0);
    }
  }

  ctx.save();
  ctx.setLineDash([6, 4]);
  ctx.strokeStyle = "rgba(180,180,255,0.25)";
  ctx.lineWidth = 1;
  for (let cx = 0; cx <= gw; cx++) {
    const [sx] = worldToScreen(cx * cs, 0);
    const [, sy0] = worldToScreen(0, 0);
    const [, sy1] = worldToScreen(0, gh * cs);
    ctx.beginPath(); ctx.moveTo(sx, sy0); ctx.lineTo(sx, sy1); ctx.stroke();
  }
  for (let cy = 0; cy <= gh; cy++) {
    const [sx0] = worldToScreen(0, 0);
    const [sx1] = worldToScreen(gw * cs, 0);
    const [, sy] = worldToScreen(0, cy * cs);
    ctx.beginPath(); ctx.moveTo(sx0, sy); ctx.lineTo(sx1, sy); ctx.stroke();
  }
  ctx.restore();

  // -- 2. Cell labels --
  for (let cy = 0; cy < gh; cy++) {
    for (let cx = 0; cx < gw; cx++) {
      const nodeIdx = cy * gw + cx;
      const nc = NODE_COLORS[nodeIdx % NODE_COLORS.length];
      const [sx0] = worldToScreen(cx * cs, cy * cs);
      const [sx1] = worldToScreen(cx * cs + cs, cy * cs);
      const [, sy0] = worldToScreen(cx * cs, cy * cs);
      const [, sy1] = worldToScreen(cx * cs, cy * cs + cs);
      ctx.save();
      ctx.font = `${Math.max(11, cs * scale * 0.04)}px 'Courier New', monospace`;
      ctx.fillStyle = "rgba(200,200,255,0.15)";
      ctx.textAlign = "center";
      ctx.textBaseline = "middle";
      ctx.fillText(nc.label, (sx0 + sx1) / 2, (sy0 + sy1) / 2);
      ctx.restore();
    }
  }

  // -- 3. AoI radius ring --
  if (player) {
    const aoiX = interpPos(player.prevX, player.worldX, player.velX, interp);
    const aoiY = interpPos(player.prevY, player.worldY, player.velY, interp);
    const [px, py] = worldToScreen(aoiX, aoiY);
    ctx.save();
    ctx.setLineDash([8, 5]);
    ctx.strokeStyle = "rgba(255,255,0,0.35)";
    ctx.lineWidth = 1.5;
    ctx.beginPath(); ctx.arc(px, py, state.aoiRadius * scale, 0, Math.PI * 2); ctx.stroke();
    ctx.restore();
  }

  // -- 3b. Move target crosshair --
  if (state.moveTargetActive) {
    const [tx, ty] = worldToScreen(state.moveTargetX, state.moveTargetY);
    const sz = 8;
    ctx.save();
    ctx.strokeStyle = "rgba(0,255,180,0.7)";
    ctx.lineWidth = 2;
    ctx.beginPath();
    ctx.moveTo(tx - sz, ty - sz); ctx.lineTo(tx + sz, ty + sz);
    ctx.moveTo(tx + sz, ty - sz); ctx.lineTo(tx - sz, ty + sz);
    ctx.stroke();
    ctx.strokeStyle = "rgba(0,255,180,0.35)";
    ctx.lineWidth = 1;
    ctx.beginPath(); ctx.arc(tx, ty, sz + 4, 0, Math.PI * 2); ctx.stroke();
    ctx.restore();
  }

  // -- 4. Entities --
  for (const [netID, ent] of state.entities) {
    let rx = interpPos(ent.prevX, ent.worldX, ent.velX, interp);
    let ry = interpPos(ent.prevY, ent.worldY, ent.velY, interp);

    if (netID === state.playerNetID && state.predictionActive) {
      rx = state.predictedX;
      ry = state.predictedY;
    }

    const [sx, sy] = worldToScreen(rx, ry);
    const r = Math.max(4, Math.abs(ent.radius) * scale);
    const isPlayer = netID === state.playerNetID;
    const nc = NODE_COLORS[(ent.ownerNode || 0) % NODE_COLORS.length];

    // Main circle.
    ctx.save();
    ctx.beginPath(); ctx.arc(sx, sy, r, 0, Math.PI * 2);
    ctx.fillStyle = nc.fill; ctx.fill();

    ctx.strokeStyle = isPlayer ? "#ffffff" : nc.stroke;
    ctx.lineWidth = isPlayer ? 2.5 : 1;
    if (ent.isReplica) {
      ctx.save(); ctx.setLineDash([4, 3]); ctx.lineWidth = 1.5; ctx.stroke(); ctx.restore();
    } else if (ent.isGhost) {
      ctx.save(); ctx.setLineDash([2, 2]); ctx.globalAlpha = 0.5; ctx.stroke(); ctx.restore();
    } else {
      ctx.stroke();
    }
    ctx.restore();

    // Velocity arrow.
    if (ent.velX !== 0 || ent.velY !== 0) {
      const speed = Math.sqrt(ent.velX * ent.velX + ent.velY * ent.velY);
      const arrowLen = Math.min(speed * scale * 0.08, 40);
      const nx = ent.velX / speed;
      const ny = ent.velY / speed;
      const ex = sx + nx * arrowLen;
      const ey = sy + ny * arrowLen;
      const angle = Math.atan2(ny, nx);
      const hl = 5;
      ctx.save();
      ctx.globalAlpha = 0.6; ctx.strokeStyle = nc.stroke; ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(sx, sy); ctx.lineTo(ex, ey);
      ctx.lineTo(ex - hl * Math.cos(angle - 0.4), ey - hl * Math.sin(angle - 0.4));
      ctx.moveTo(ex, ey);
      ctx.lineTo(ex - hl * Math.cos(angle + 0.4), ey - hl * Math.sin(angle + 0.4));
      ctx.stroke();
      ctx.restore();
    }

    // NetID label.
    ctx.save();
    ctx.font = "9px Courier New, monospace"; ctx.fillStyle = "#aaa";
    ctx.textAlign = "center"; ctx.textBaseline = "bottom";
    ctx.fillText(`#${netID}`, sx, sy - r - 2);
    ctx.restore();

    // Replica/Ghost badge.
    if (ent.isReplica || ent.isGhost) {
      const badge = ent.isGhost ? "G" : "R";
      const badgeColor = ent.isGhost ? "#ff8800" : "#00ccff";
      ctx.save();
      ctx.font = "bold 8px Courier New, monospace"; ctx.fillStyle = badgeColor;
      ctx.textAlign = "left"; ctx.textBaseline = "middle";
      ctx.fillText(badge, sx + r + 3, sy);
      ctx.restore();
    }

    // Player name.
    if (ent.name) {
      ctx.save();
      ctx.font = isPlayer ? "bold 11px Courier New, monospace" : "10px Courier New, monospace";
      ctx.fillStyle = isPlayer ? "#7af" : "#999";
      ctx.textAlign = "center"; ctx.textBaseline = "top";
      ctx.fillText(ent.name, sx, sy + r + 2);
      ctx.restore();
    }
  }

  // -- 5. HUD: Tick + FPS --
  ctx.save();
  ctx.fillStyle = "rgba(0,0,0,0.55)"; ctx.fillRect(8, 8, 160, 61);
  ctx.font = "11px Courier New, monospace"; ctx.fillStyle = "#7af";
  ctx.textAlign = "left"; ctx.textBaseline = "top";
  ctx.fillText(`TICK  ${state.tick}`, 14, 13);
  ctx.fillText(`FPS   ${state.fps}`, 14, 28);
  ctx.fillText(`NET   #${state.playerNetID || "?"}`, 14, 43);
  ctx.restore();

  // -- 6. Grid info --
  const panelW = 200;
  ctx.save();
  ctx.fillStyle = "rgba(0,0,0,0.55)"; ctx.fillRect(W - panelW - 8, 8, panelW, 38);
  ctx.font = "11px Courier New, monospace"; ctx.textAlign = "left"; ctx.textBaseline = "top";
  ctx.fillStyle = "#aaa";
  ctx.fillText(`GRID      ${state.gridW}x${state.gridH}`, W - panelW - 2, 13);
  ctx.fillText(`ENTITIES  ${state.entities.size}`, W - panelW - 2, 28);
  ctx.restore();

  // -- 7. Legend --
  const rows = [
    ...NODE_COLORS.slice(0, gw * gh).map((nc) => ({ color: nc.fill, label: nc.label, dash: false })),
    { color: "#ffdd00", label: "AoI radius", dash: true },
    { color: "#00ccff", label: "R = replica", dash: true },
    { color: "#ff8800", label: "G = ghost", dash: true },
    { color: "#00ffb4", label: "move target (X)", dash: false },
  ];
  const rowH = 16;
  const legendH = rows.length * rowH + 12;
  const legendW = 170;
  const legendY = H - legendH - 8;

  ctx.save();
  ctx.fillStyle = "rgba(0,0,0,0.55)"; ctx.fillRect(8, legendY, legendW, legendH);
  ctx.font = "10px Courier New, monospace"; ctx.textBaseline = "middle";
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i];
    const ry = legendY + 6 + i * rowH + rowH / 2;
    ctx.save();
    if (row.dash) {
      ctx.setLineDash([3, 2]); ctx.strokeStyle = row.color; ctx.lineWidth = 1.5;
      ctx.beginPath(); ctx.arc(20, ry, 5, 0, Math.PI * 2); ctx.stroke();
    } else {
      ctx.fillStyle = row.color;
      ctx.beginPath(); ctx.arc(20, ry, 5, 0, Math.PI * 2); ctx.fill();
    }
    ctx.restore();
    ctx.fillStyle = "#bbb"; ctx.textAlign = "left";
    ctx.fillText(row.label, 32, ry);
  }
  ctx.restore();
}

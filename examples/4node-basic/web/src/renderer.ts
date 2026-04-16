import { state, type CellInfo, type ClientEntity } from "./state.js";
import { interpPos, getInterp, updatePrediction } from "./interpolation.js";
import { VIEWPORT_SCALE } from "./constants.js";

// 9 pre-selected colors — enough to avoid repeats for adjacent cells.
const CELL_COLORS = [
  { bg: "rgba(100,150,255,0.07)", fill: "#5588cc", stroke: "#6496FF" },
  { bg: "rgba(255,150,100,0.07)", fill: "#cc8855", stroke: "#FF9664" },
  { bg: "rgba(100,255,150,0.07)", fill: "#55cc88", stroke: "#64FF96" },
  { bg: "rgba(255,100,255,0.07)", fill: "#cc55cc", stroke: "#FF64FF" },
  { bg: "rgba(255,255,100,0.07)", fill: "#cccc55", stroke: "#FFFF64" },
  { bg: "rgba(100,255,255,0.07)", fill: "#55cccc", stroke: "#64FFFF" },
  { bg: "rgba(200,100,255,0.07)", fill: "#aa55cc", stroke: "#CC64FF" },
  { bg: "rgba(255,200,100,0.07)", fill: "#ccaa55", stroke: "#FFCC64" },
  { bg: "rgba(150,255,200,0.07)", fill: "#88ccaa", stroke: "#96FFCC" },
];

function cellColorIndex(c: CellInfo): number {
  // Hash that ensures adjacent cells get different colors
  const hash = ((c.cellX * 7 + c.cellY * 13 + c.depth * 31) & 0x7fffffff) % CELL_COLORS.length;
  const parity = ((c.cellX + c.cellY) % 2 + 2) % 2;
  return (hash + parity * 3) % CELL_COLORS.length;
}

let renderLoopRunning = false;

export function startRenderLoop(): void {
  if (renderLoopRunning) return;
  renderLoopRunning = true;
  requestAnimationFrame(renderLoop);
}

function renderLoop(now: number): void {
  requestAnimationFrame(renderLoop);

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
  updatePrediction(now);
  state.lastFrameTime = now;

  const player = state.entities.get(state.playerNetID);
  // No player entity yet (transient between spawn and first world update) —
  // skip rendering until it arrives rather than drawing at (0,0).
  if (!player) return;

  let camX: number, camY: number;
  if (state.predictionActive) {
    camX = state.predictedX;
    camY = state.predictedY;
  } else {
    camX = interpPos(player.prevX, player.worldX, player.velX, interp);
    camY = interpPos(player.prevY, player.worldY, player.velY, interp);
  }
  state.camX = camX;
  state.camY = camY;

  const scale = Math.min(W, H) / VIEWPORT_SCALE;

  function worldToScreen(wx: number, wy: number): [number, number] {
    return [(wx - camX) * scale + W / 2, (wy - camY) * scale + H / 2];
  }

  // -- 1. Cell backgrounds, boundaries, and labels (debug only) --
  for (const c of state.debugVisible ? state.cells : []) {
    const nc = CELL_COLORS[cellColorIndex(c)];
    const [sx0, sy0] = worldToScreen(c.originX, c.originY);
    const [sx1, sy1] = worldToScreen(c.originX + c.size, c.originY + c.size);

    // Background tint
    ctx.fillStyle = nc.bg;
    ctx.fillRect(sx0, sy0, sx1 - sx0, sy1 - sy0);

    // Border
    ctx.save();
    if (c.depth > 0) {
      ctx.setLineDash([3, 3]);
      ctx.lineWidth = 1.5;
    } else {
      ctx.setLineDash([6, 4]);
      ctx.lineWidth = 1;
    }
    ctx.strokeStyle = c.depth > 0 ? nc.stroke : "rgba(180,180,255,0.25)";
    ctx.strokeRect(sx0, sy0, sx1 - sx0, sy1 - sy0);
    ctx.restore();

    // Label: show BOTH the cell coordinates AND the owning host/node ID
    // so debugging a multi-process setup makes it obvious which cells
    // live on which node. Two separate fillText calls (canvas doesn't
    // handle embedded newlines).
    const cellName = c.depth > 0
      ? `d${c.depth}:${c.cellX},${c.cellY}`
      : `${c.cellX},${c.cellY}`;
    ctx.save();
    const fontPx = Math.max(9, c.size * scale * 0.04);
    ctx.font = `${fontPx}px 'Courier New', monospace`;
    ctx.fillStyle = c.depth > 0 ? "rgba(200,200,255,0.25)" : "rgba(200,200,255,0.15)";
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    const cx = (sx0 + sx1) / 2;
    const cy = (sy0 + sy1) / 2;
    if (c.nodeId) {
      ctx.fillText(cellName, cx, cy - fontPx * 0.6);
      ctx.fillText(c.nodeId, cx, cy + fontPx * 0.6);
    } else {
      ctx.fillText(cellName, cx, cy);
    }
    ctx.restore();
  }

  // -- 2. AoI radius ring (debug only) --
  if (state.debugVisible && player) {
    const aoiX = interpPos(player.prevX, player.worldX, player.velX, interp);
    const aoiY = interpPos(player.prevY, player.worldY, player.velY, interp);
    const [px, py] = worldToScreen(aoiX, aoiY);
    ctx.save();
    ctx.setLineDash([8, 5]);
    ctx.strokeStyle = "rgba(255,255,0,0.35)";
    ctx.lineWidth = 1.5;
    ctx.beginPath(); ctx.arc(px, py, player.aoIRadius * scale, 0, Math.PI * 2); ctx.stroke();
    ctx.restore();
  }

  // -- 3. Move target crosshair --
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
  // Two-pass render so the local player always draws on top of bots.
  function drawEntity(netID: number, ent: ClientEntity): void {
    let rx = interpPos(ent.prevX, ent.worldX, ent.velX, interp);
    let ry = interpPos(ent.prevY, ent.worldY, ent.velY, interp);

    const isPlayer = netID === state.playerNetID;
    if (isPlayer && state.predictionActive) {
      rx = state.predictedX;
      ry = state.predictedY;
    }

    const [sx, sy] = worldToScreen(rx, ry);
    const baseR = Math.max(4, Math.abs(ent.radius) * scale);
    const r = isPlayer ? baseR * 1.4 : baseR;

    // Color by the cell the entity is currently in (debug only). Matches the
    // cell background color so entities visually belong to their cell. Node
    // identity is shown via the label under the cell coords, not color —
    // that way single-process mode (all cells share one host) still gets 4
    // distinct colors instead of collapsing to one.
    const cellAt = state.debugVisible ? findCellAtPos(rx, ry) : null;
    const nc = cellAt
      ? CELL_COLORS[cellColorIndex(cellAt)]
      : { fill: "#5588cc", stroke: "#6496FF", bg: "" };

    // Player-only glow halo, drawn beneath the filled circle so cell color +
    // white stroke render unchanged on top.
    if (isPlayer) {
      const glowR = r * 2.0;
      const grad = ctx.createRadialGradient(sx, sy, r, sx, sy, glowR);
      grad.addColorStop(0, "rgba(255,255,255,0.35)");
      grad.addColorStop(1, "rgba(255,255,255,0)");
      ctx.save();
      ctx.fillStyle = grad;
      ctx.beginPath(); ctx.arc(sx, sy, glowR, 0, Math.PI * 2); ctx.fill();
      ctx.restore();
    }

    ctx.save();
    ctx.beginPath(); ctx.arc(sx, sy, r, 0, Math.PI * 2);
    ctx.fillStyle = nc.fill; ctx.fill();

    ctx.strokeStyle = isPlayer ? "#ffffff" : nc.stroke;
    ctx.lineWidth = isPlayer ? 2.5 : 1;
    if (state.debugVisible && ent.isReplica) {
      ctx.save(); ctx.setLineDash([4, 3]); ctx.lineWidth = 1.5; ctx.stroke(); ctx.restore();
    } else if (state.debugVisible && ent.isGhost) {
      ctx.save(); ctx.setLineDash([2, 2]); ctx.globalAlpha = 0.5; ctx.stroke(); ctx.restore();
    } else {
      ctx.stroke();
    }
    ctx.restore();

    // Velocity arrow
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

    // NetID label
    ctx.save();
    ctx.font = "9px Courier New, monospace"; ctx.fillStyle = "#aaa";
    ctx.textAlign = "center"; ctx.textBaseline = "bottom";
    ctx.fillText(`#${netID}`, sx, sy - r - 2);
    ctx.restore();

    // Replica/Ghost badge (debug only)
    if (state.debugVisible && (ent.isReplica || ent.isGhost)) {
      const badge = ent.isGhost ? "G" : "R";
      const badgeColor = ent.isGhost ? "#ff8800" : "#00ccff";
      ctx.save();
      ctx.font = "bold 8px Courier New, monospace"; ctx.fillStyle = badgeColor;
      ctx.textAlign = "left"; ctx.textBaseline = "middle";
      ctx.fillText(badge, sx + r + 3, sy);
      ctx.restore();
    }

    // Player name
    if (ent.name) {
      ctx.save();
      ctx.font = isPlayer ? "bold 11px Courier New, monospace" : "10px Courier New, monospace";
      ctx.fillStyle = isPlayer ? "#7af" : "#999";
      ctx.textAlign = "center"; ctx.textBaseline = "top";
      ctx.fillText(ent.name, sx, sy + r + 2);
      ctx.restore();
    }
  }

  // Pass A: everything except the local player.
  for (const [netID, ent] of state.entities) {
    if (netID === state.playerNetID) continue;
    drawEntity(netID, ent);
  }
  // Pass B: local player last, guaranteed on top.
  if (state.playerNetID) {
    const playerEnt = state.entities.get(state.playerNetID);
    if (playerEnt) drawEntity(state.playerNetID, playerEnt);
  }

  // -- 5. HUD --
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
  ctx.fillText(`CELLS     ${state.cells.length}`, W - panelW - 2, 13);
  ctx.fillText(`ENTITIES  ${state.entities.size}`, W - panelW - 2, 28);
  ctx.restore();

  // -- 7. Legend --
  // Sort cells deterministically so the legend order doesn't flicker when
  // the topology push re-emits cells in a different order. Always show
  // cell coords; append node/host ID when available so multi-process mode
  // still distinguishes owners.
  const sortedCells = state.cells.slice().sort((a, b) => {
    if (a.depth !== b.depth) return a.depth - b.depth;
    if (a.cellY !== b.cellY) return a.cellY - b.cellY;
    return a.cellX - b.cellX;
  });
  const legendCells = sortedCells.slice(0, 8).map((c) => {
    const coords = c.depth > 0
      ? `d${c.depth}:${c.cellX},${c.cellY}`
      : `${c.cellX},${c.cellY}`;
    return {
      color: CELL_COLORS[cellColorIndex(c)].fill,
      label: c.nodeId ? `${coords} (${c.nodeId})` : coords,
    };
  });
  const legendItems = [
    ...legendCells.map((lc) => ({ color: lc.color, label: lc.label, dash: false })),
    { color: "#ffdd00", label: "AoI radius", dash: true },
    { color: "#00ccff", label: "R = replica", dash: true },
    { color: "#ff8800", label: "G = ghost", dash: true },
    { color: "#00ffb4", label: "move target (X)", dash: false },
  ];
  const rowH = 16;
  const legendH = legendItems.length * rowH + 12;
  const legendW = 170;
  const legendY = H - legendH - 8;

  ctx.save();
  ctx.fillStyle = "rgba(0,0,0,0.55)"; ctx.fillRect(8, legendY, legendW, legendH);
  ctx.font = "10px Courier New, monospace"; ctx.textBaseline = "middle";
  for (let i = 0; i < legendItems.length; i++) {
    const row = legendItems[i];
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

// Find the smallest cell containing a world position.
function findCellAtPos(wx: number, wy: number): CellInfo | null {
  let best: CellInfo | null = null;
  for (const c of state.cells) {
    if (wx >= c.originX && wx < c.originX + c.size &&
        wy >= c.originY && wy < c.originY + c.size) {
      if (!best || c.depth > best.depth) {
        best = c;
      }
    }
  }
  return best;
}

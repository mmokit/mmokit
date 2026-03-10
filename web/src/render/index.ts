import { EntityType } from "@gen/game_pb.js";
import { ENTITY_COLORS, RESOURCE_COLORS, TICK_INTERVAL } from "../constants";
import { interpolateEntities } from "../interpolation";
import { drawThrusterParticles, updateAndDrawExplosions, updateThrusterParticles } from "../particles";
import type { GameState } from "../state";
import type { StarLayer } from "../types";
import { drawMinable } from "./asteroids";
import {
  drawCargoPanel,
  drawDeathScreen,
  drawMiningLasers,
  drawStatusBars,
  drawStationPrompt,
  drawTargetHighlight,
  drawToasts,
} from "./hud";
import { drawLootCrate } from "./loot-crate";
import { drawProjectile } from "./projectile";
import { drawShip } from "./ship";
import { drawStarfield } from "./starfield";
import { drawStation } from "./station";

export function startRenderLoop(
  ctx: CanvasRenderingContext2D,
  canvas: HTMLCanvasElement,
  state: GameState,
): void {
  const hud = document.getElementById("hud")!;

  function render() {
    requestAnimationFrame(render);

    const now = performance.now();

    state.frameCount++;
    if (now - state.lastFpsTime >= 1000) {
      state.fps = state.frameCount;
      state.frameCount = 0;
      state.lastFpsTime = now;
    }

    let t = 0;
    if (state.lastTickTime > 0) {
      t = (now - state.lastTickTime) / TICK_INTERVAL;
      t = Math.max(0, Math.min(t, 2.0));
    }

    interpolateEntities(state.entities, t);

    ctx.fillStyle = "#000";
    ctx.fillRect(0, 0, canvas.width, canvas.height);

    const myEntity = state.entities.get(state.myEntityId);
    if (myEntity) {
      state.cameraX = myEntity.renderX - canvas.width / 2;
      state.cameraY = myEntity.renderY - canvas.height / 2;
    }

    // Draw starfield
    drawStarfield(ctx, state.starLayers, state.cameraX, state.cameraY, canvas.width, canvas.height, now);

    // Draw grid
    ctx.strokeStyle = "rgba(255, 255, 255, 0.04)";
    ctx.lineWidth = 1;
    const gridSize = 200;
    const startX = Math.floor(state.cameraX / gridSize) * gridSize;
    const startY = Math.floor(state.cameraY / gridSize) * gridSize;
    for (let x = startX; x < state.cameraX + canvas.width; x += gridSize) {
      ctx.beginPath();
      ctx.moveTo(x - state.cameraX, 0);
      ctx.lineTo(x - state.cameraX, canvas.height);
      ctx.stroke();
    }
    for (let y = startY; y < state.cameraY + canvas.height; y += gridSize) {
      ctx.beginPath();
      ctx.moveTo(0, y - state.cameraY);
      ctx.lineTo(canvas.width, y - state.cameraY);
      ctx.stroke();
    }

    // Draw entities
    for (const [id, ent] of state.entities) {
      const e = ent.curr;
      const sx = ent.renderX - state.cameraX;
      const sy = ent.renderY - state.cameraY;

      if (sx < -100 || sx > canvas.width + 100 || sy < -100 || sy > canvas.height + 100) continue;

      let color = ENTITY_COLORS[e.entityType] || "#fff";
      if (e.entityType === EntityType.ASTEROID) {
        color = RESOURCE_COLORS[e.resourceType] || "#a86";
      }

      switch (e.entityType) {
        case EntityType.SHIP: {
          let thrusting = false;
          if (id === state.myEntityId) {
            thrusting = !!(state.keys["KeyW"] || state.keys["ArrowUp"]);
          } else {
            const spd = Math.sqrt(e.vx * e.vx + e.vy * e.vy);
            thrusting = spd > 30;
          }
          updateThrusterParticles(ent, thrusting, 1 / 60);
          drawThrusterParticles(ctx, ent, state.cameraX, state.cameraY);
          drawShip(ctx, sx, sy, ent.renderRot, e.width, e.height, color, id === state.myEntityId, e.health, e.shield, e.pilotName, thrusting);
          break;
        }
        case EntityType.ASTEROID:
          drawMinable(ctx, sx, sy, e.radius, e.resourceType, e.resourceRemaining, color, ent.renderRot);
          break;
        case EntityType.PROJECTILE:
          drawProjectile(ctx, sx, sy, ent.renderRot, color);
          break;
        case EntityType.STATION:
          drawStation(ctx, sx, sy, e.radius || 80);
          break;
        case EntityType.LOOT_CRATE:
          drawLootCrate(ctx, sx, sy, e.radius || 12);
          break;
        default:
          ctx.beginPath();
          ctx.arc(sx, sy, e.radius || 5, 0, Math.PI * 2);
          ctx.fillStyle = color;
          ctx.fill();
      }
    }

    // Draw target highlight + info
    drawTargetHighlight(ctx, state);

    // Draw mining lasers
    drawMiningLasers(ctx, state);

    // Draw explosions
    updateAndDrawExplosions(ctx, canvas, state.explosions, state.cameraX, state.cameraY, now);

    // Draw death screen
    if (state.isDead) {
      drawDeathScreen(ctx, canvas);
    }

    // Track FLUX from own entity state
    if (myEntity && myEntity.curr.flux !== undefined) {
      state.playerFlux = myEntity.curr.flux;
    }

    // Draw HUD
    let hudText = `${state.playerUsername} | FPS: ${state.fps} | Tick: ${state.tickCount} | Entities: ${state.entities.size}`;
    if (myEntity) {
      hudText += ` | FLUX: ${Math.floor(state.playerFlux)}`;
      hudText += `\nPos: (${myEntity.renderX.toFixed(0)}, ${myEntity.renderY.toFixed(0)})`;
    }
    hud.textContent = hudText;

    // Draw player status bars at bottom center
    if (myEntity) {
      drawStatusBars(ctx, canvas, myEntity);
    }

    // Draw cargo panel
    if (state.cargoPanelOpen && myEntity) {
      drawCargoPanel(ctx, canvas, myEntity);
    }

    // Station proximity prompt
    if (myEntity && !state.isDead) {
      drawStationPrompt(ctx, canvas, state, myEntity);
    }

    // Draw toasts
    drawToasts(ctx, canvas, state);
  }

  render();
}

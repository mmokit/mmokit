import { EntityType } from "@gen/game_pb.js";
import { RESOURCE_COLORS_CSS } from "../constants";
import type { GameState } from "../state";

// How many world units the minimap shows in each direction from the player
const VIEW_RANGE = 3000;

export class Minimap {
  private canvas: HTMLCanvasElement;
  private ctx: CanvasRenderingContext2D;
  private size: number;

  constructor() {
    this.canvas = document.getElementById("minimap") as HTMLCanvasElement;
    this.ctx = this.canvas.getContext("2d")!;
    this.size = this.canvas.width;
  }

  update(state: GameState, screenW: number, screenH: number): void {
    const ctx = this.ctx;
    const s = this.size;
    const half = s / 2;

    const myEntity = state.entities.get(state.myEntityId);
    const cx = myEntity ? myEntity.renderX : 0;
    const cy = myEntity ? myEntity.renderY : 0;

    // Scale: map VIEW_RANGE world units to half the minimap size
    const scale = half / VIEW_RANGE;

    ctx.fillStyle = "rgba(0, 0, 0, 0.85)";
    ctx.fillRect(0, 0, s, s);

    // World boundary
    const ww = state.worldWidth || 10000;
    const wh = state.worldHeight || 10000;
    const bx = (-ww / 2 - cx) * scale + half;
    const by = (-wh / 2 - cy) * scale + half;
    const bw = ww * scale;
    const bh = wh * scale;
    ctx.strokeStyle = "rgba(255, 255, 255, 0.15)";
    ctx.lineWidth = 1;
    ctx.strokeRect(bx, by, bw, bh);

    // Crosshair at center (player position)
    ctx.strokeStyle = "rgba(255, 255, 255, 0.1)";
    ctx.beginPath();
    ctx.moveTo(half, 0);
    ctx.lineTo(half, s);
    ctx.moveTo(0, half);
    ctx.lineTo(s, half);
    ctx.stroke();

    // Entities
    for (const [id, ent] of state.entities) {
      const ex = (ent.renderX - cx) * scale + half;
      const ey = (ent.renderY - cy) * scale + half;

      // Skip if off minimap
      if (ex < -4 || ex > s + 4 || ey < -4 || ey > s + 4) continue;

      const isMe = id === state.myEntityId;

      if (isMe) {
        // Player — bright green diamond
        ctx.fillStyle = "#0f0";
        ctx.beginPath();
        ctx.moveTo(ex, ey - 4);
        ctx.lineTo(ex + 3, ey);
        ctx.lineTo(ex, ey + 4);
        ctx.lineTo(ex - 3, ey);
        ctx.closePath();
        ctx.fill();
        // Glow
        ctx.shadowColor = "#0f0";
        ctx.shadowBlur = 6;
        ctx.fill();
        ctx.shadowBlur = 0;
      } else {
        switch (ent.curr.entityType) {
          case EntityType.SHIP:
            ctx.fillStyle = "#4af";
            ctx.fillRect(ex - 2, ey - 2, 4, 4);
            break;
          case EntityType.ASTEROID: {
            // Color by resource type
            const resColor = RESOURCE_COLORS_CSS[ent.curr.resourceType] || "#a86";
            ctx.fillStyle = resColor;
            // Larger dot, scaled slightly by asteroid radius
            const dotSize = Math.max(2, Math.min((ent.curr.radius || 20) * scale * 0.5, 4));
            ctx.beginPath();
            ctx.arc(ex, ey, dotSize, 0, Math.PI * 2);
            ctx.fill();
            break;
          }
          case EntityType.STATION:
            ctx.fillStyle = "#8f8";
            ctx.strokeStyle = "#8f8";
            ctx.lineWidth = 1;
            ctx.beginPath();
            ctx.arc(ex, ey, 5, 0, Math.PI * 2);
            ctx.stroke();
            ctx.beginPath();
            ctx.arc(ex, ey, 2, 0, Math.PI * 2);
            ctx.fill();
            break;
          case EntityType.LOOT_CRATE:
            ctx.fillStyle = "#fd0";
            ctx.fillRect(ex - 2, ey - 2, 4, 4);
            break;
          case EntityType.NPC:
            ctx.fillStyle = "#f44";
            ctx.fillRect(ex - 2, ey - 2, 4, 4);
            break;
        }
      }
    }

    // Camera viewport rectangle
    if (myEntity) {
      const vw = screenW * scale;
      const vh = screenH * scale;
      ctx.strokeStyle = "rgba(255, 255, 255, 0.25)";
      ctx.lineWidth = 1;
      ctx.strokeRect(half - vw / 2, half - vh / 2, vw, vh);
    }

    // Border
    ctx.strokeStyle = "rgba(255, 255, 255, 0.2)";
    ctx.lineWidth = 1;
    ctx.strokeRect(0, 0, s, s);
  }
}

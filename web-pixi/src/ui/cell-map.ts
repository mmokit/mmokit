import { Container, Graphics, Text } from "pixi.js";
import type { GameState, CellInfo } from "../state";
import { CELL_SIZE } from "../constants";

export class CellMap {
  private container: Container;
  private background: Graphics;
  private gridLayer: Container;
  private markerLayer: Container;
  private scanlineOverlay: Graphics;
  private cornerAccents: Graphics;
  private titleText: Text;
  private cellLabel: Text;
  private posLabel: Text;

  // Grid + marker graphics (redrawn each frame when visible)
  private gridGfx: Graphics;
  private markerGfx: Graphics;

  private readonly MIN_ZOOM = 0.15;
  private readonly MAX_ZOOM = 4.0;
  private mapZoom = this.MIN_ZOOM;

  // Cached panel dimensions
  private panelW = 0;
  private panelH = 0;
  private lastScreenW = 0;
  private lastScreenH = 0;
  private scanlinesDirty = true;

  constructor(stage: Container) {
    this.container = new Container();
    this.container.visible = false;
    this.container.zIndex = 1000;
    stage.addChild(this.container);

    // Background
    this.background = new Graphics();
    this.container.addChild(this.background);

    // Grid layer
    this.gridLayer = new Container();
    this.container.addChild(this.gridLayer);
    this.gridGfx = new Graphics();
    this.gridLayer.addChild(this.gridGfx);

    // Marker layer
    this.markerLayer = new Container();
    this.container.addChild(this.markerLayer);
    this.markerGfx = new Graphics();
    this.markerLayer.addChild(this.markerGfx);

    // Scanline overlay
    this.scanlineOverlay = new Graphics();
    this.container.addChild(this.scanlineOverlay);

    // Corner accents
    this.cornerAccents = new Graphics();
    this.container.addChild(this.cornerAccents);

    // Title
    this.titleText = new Text({
      text: "CELL MAP",
      style: {
        fontFamily: "monospace",
        fontSize: 14,
        fill: 0x00ccff,
        fontWeight: "bold",
        letterSpacing: 3,
      },
    });
    this.titleText.anchor.set(0.5, 0);
    this.container.addChild(this.titleText);

    // Cell label
    this.cellLabel = new Text({
      text: "",
      style: {
        fontFamily: "monospace",
        fontSize: 10,
        fill: 0x4488cc,
      },
    });
    this.cellLabel.anchor.set(0.5, 0);
    this.container.addChild(this.cellLabel);

    // Position label (bottom bar)
    this.posLabel = new Text({
      text: "",
      style: {
        fontFamily: "monospace",
        fontSize: 9,
        fill: 0x4488cc,
      },
    });
    this.posLabel.anchor.set(0.5, 1);
    this.container.addChild(this.posLabel);
  }

  handleWheel(deltaY: number): void {
    const factor = deltaY > 0 ? 1 / 1.15 : 1.15;
    this.mapZoom = Math.max(this.MIN_ZOOM, Math.min(this.MAX_ZOOM, this.mapZoom * factor));
  }

  update(state: GameState, screenW: number, screenH: number): void {
    this.container.visible = state.cellMapOpen;
    document.body.classList.toggle("cell-map-open", state.cellMapOpen);
    if (!state.cellMapOpen) return;

    const now = performance.now();

    // Recompute panel dimensions on resize or open
    if (screenW !== this.lastScreenW || screenH !== this.lastScreenH) {
      this.lastScreenW = screenW;
      this.lastScreenH = screenH;
      this.panelW = Math.floor(screenW * 0.85);
      this.panelH = Math.floor(screenH * 0.85);
      const panelX = Math.floor((screenW - this.panelW) / 2);
      const panelY = Math.floor((screenH - this.panelH) / 2);
      this.container.position.set(panelX, panelY);
      this.scanlinesDirty = true;
    }

    const pw = this.panelW;
    const ph = this.panelH;

    // Player absolute world position. renderX/renderY are already in
    // world-absolute coordinates after the topology-transparent wire
    // refactor (SpawnedMsg carries world_x/world_y with zero knowledge
    // of cells). Earlier this code treated renderX as cell-local and
    // added `originCellX * CELL_SIZE`, which double-counted the cell
    // offset and placed the map marker one cell away from the player's
    // real position.
    const myEntity = state.entities.get(state.myEntityId);
    const playerAbsX = myEntity ? myEntity.renderX : CELL_SIZE / 2;
    const playerAbsY = myEntity ? myEntity.renderY : CELL_SIZE / 2;

    // Derive cell from world position — the client receives no cell-change
    // events (topology-transparent protocol). Falls back to (0, 0) before
    // the player entity exists.
    const currentCellX = myEntity ? Math.floor(playerAbsX / CELL_SIZE) : 0;
    const currentCellY = myEntity ? Math.floor(playerAbsY / CELL_SIZE) : 0;

    const pixelsPerUnit = (pw * this.mapZoom) / CELL_SIZE;

    // --- Background ---
    this.background.clear();
    this.background
      .rect(0, 0, pw, ph)
      .fill({ color: 0x000a14, alpha: 0.92 });
    this.background
      .rect(0, 0, pw, ph)
      .stroke({ color: 0x00ccff, width: 1, alpha: 0.3 });

    // --- Grid ---
    this.drawGrid(playerAbsX, playerAbsY, pixelsPerUnit, pw, ph, state.gridCellsX, state.gridCellsY, state);

    // --- Markers ---
    this.drawMarkers(state, playerAbsX, playerAbsY, pixelsPerUnit, pw, ph, now);

    // --- Scanlines (cached) ---
    if (this.scanlinesDirty) {
      this.drawScanlines(pw, ph);
      this.scanlinesDirty = false;
    }

    // --- Corner accents ---
    this.drawCornerAccents(pw, ph);

    // --- Title ---
    this.titleText.position.set(pw / 2, 12);
    this.cellLabel.text = `CELL (${currentCellX}, ${currentCellY})`;
    this.cellLabel.position.set(pw / 2, 30);

    // --- Position label ---
    // Show cell-local coords, matching the HUD convention so both
    // readouts agree. playerAbsX/Y are world-absolute; subtract the
    // origin cell to get the familiar "0..CELL_SIZE" range.
    const labelLocalX = playerAbsX - currentCellX * CELL_SIZE;
    const labelLocalY = playerAbsY - currentCellY * CELL_SIZE;
    this.posLabel.text = `POS: ${Math.floor(labelLocalX)}, ${Math.floor(labelLocalY)}  |  ZOOM: ${this.mapZoom.toFixed(2)}x`;
    this.posLabel.position.set(pw / 2, ph - 8);
  }

  private drawGrid(
    playerAbsX: number, playerAbsY: number,
    pixelsPerUnit: number, pw: number, ph: number,
    gridCellsX: number, gridCellsY: number,
    state?: GameState,
  ): void {
    this.gridGfx.clear();

    // Use topology data when available
    if (state?.cellTopology) {
      this.drawTopologyGrid(state.cellTopology, playerAbsX, playerAbsY, pixelsPerUnit, pw, ph);
      return;
    }

    // Visible world range
    const halfWorldW = (pw / 2) / pixelsPerUnit;
    const halfWorldH = (ph / 2) / pixelsPerUnit;

    // Clamp to actual grid bounds (cells 0..gridCellsN-1, boundaries 0..gridCellsN)
    const minSX = gridCellsX > 0 ? Math.max(0, Math.floor((playerAbsX - halfWorldW) / CELL_SIZE)) : Math.floor((playerAbsX - halfWorldW) / CELL_SIZE);
    const maxSX = gridCellsX > 0 ? Math.min(gridCellsX, Math.ceil((playerAbsX + halfWorldW) / CELL_SIZE)) : Math.ceil((playerAbsX + halfWorldW) / CELL_SIZE);
    const minSY = gridCellsY > 0 ? Math.max(0, Math.floor((playerAbsY - halfWorldH) / CELL_SIZE)) : Math.floor((playerAbsY - halfWorldH) / CELL_SIZE);
    const maxSY = gridCellsY > 0 ? Math.min(gridCellsY, Math.ceil((playerAbsY + halfWorldH) / CELL_SIZE)) : Math.ceil((playerAbsY + halfWorldH) / CELL_SIZE);

    // Cell boundary lines
    for (let sx = minSX; sx <= maxSX; sx++) {
      const worldX = sx * CELL_SIZE;
      const screenX = pw / 2 + (worldX - playerAbsX) * pixelsPerUnit;
      if (screenX < -1 || screenX > pw + 1) continue;
      this.gridGfx
        .moveTo(screenX, 0)
        .lineTo(screenX, ph)
        .stroke({ color: 0x0066aa, width: 1, alpha: 0.25 });
    }
    for (let sy = minSY; sy <= maxSY; sy++) {
      const worldY = sy * CELL_SIZE;
      const screenY = ph / 2 + (worldY - playerAbsY) * pixelsPerUnit;
      if (screenY < -1 || screenY > ph + 1) continue;
      this.gridGfx
        .moveTo(0, screenY)
        .lineTo(pw, screenY)
        .stroke({ color: 0x0066aa, width: 1, alpha: 0.25 });
    }

    // Sub-grid: quarter-cell lines when zoomed in enough
    if (this.mapZoom >= 1.0) {
      const quarterSize = CELL_SIZE / 4;
      const subMinX = Math.floor((playerAbsX - halfWorldW) / quarterSize);
      const subMaxX = Math.ceil((playerAbsX + halfWorldW) / quarterSize);
      const subMinY = Math.floor((playerAbsY - halfWorldH) / quarterSize);
      const subMaxY = Math.ceil((playerAbsY + halfWorldH) / quarterSize);

      for (let qx = subMinX; qx <= subMaxX; qx++) {
        if (qx % 4 === 0) continue; // skip cell boundaries (already drawn)
        const worldX = qx * quarterSize;
        const screenX = pw / 2 + (worldX - playerAbsX) * pixelsPerUnit;
        if (screenX < -1 || screenX > pw + 1) continue;
        this.gridGfx
          .moveTo(screenX, 0)
          .lineTo(screenX, ph)
          .stroke({ color: 0x0066aa, width: 1, alpha: 0.08 });
      }
      for (let qy = subMinY; qy <= subMaxY; qy++) {
        if (qy % 4 === 0) continue;
        const worldY = qy * quarterSize;
        const screenY = ph / 2 + (worldY - playerAbsY) * pixelsPerUnit;
        if (screenY < -1 || screenY > ph + 1) continue;
        this.gridGfx
          .moveTo(0, screenY)
          .lineTo(pw, screenY)
          .stroke({ color: 0x0066aa, width: 1, alpha: 0.08 });
      }
    }

    // Cell labels
    // Remove old cell label texts from gridLayer (keep gridGfx)
    while (this.gridLayer.children.length > 1) {
      this.gridLayer.removeChildAt(this.gridLayer.children.length - 1);
    }
    const labelMaxSX = gridCellsX > 0 ? Math.min(maxSX, gridCellsX - 1) : maxSX;
    const labelMaxSY = gridCellsY > 0 ? Math.min(maxSY, gridCellsY - 1) : maxSY;
    for (let sx = minSX; sx <= labelMaxSX; sx++) {
      for (let sy = minSY; sy <= labelMaxSY; sy++) {
        // Place label at center of each visible cell
        const centerX = pw / 2 + ((sx + 0.5) * CELL_SIZE - playerAbsX) * pixelsPerUnit;
        const centerY = ph / 2 + ((sy + 0.5) * CELL_SIZE - playerAbsY) * pixelsPerUnit;
        if (centerX < -50 || centerX > pw + 50 || centerY < -20 || centerY > ph + 20) continue;

        const label = new Text({
          text: `(${sx}, ${sy})`,
          style: {
            fontFamily: "monospace",
            fontSize: 9,
            fill: 0x334466,
          },
        });
        label.anchor.set(0.5, 0.5);
        label.position.set(centerX, centerY);
        this.gridLayer.addChild(label);
      }
    }
  }

  private drawTopologyGrid(
    cells: CellInfo[],
    playerAbsX: number, playerAbsY: number,
    pixelsPerUnit: number, pw: number, ph: number,
  ): void {
    // Remove old cell label texts from gridLayer (keep gridGfx)
    while (this.gridLayer.children.length > 1) {
      this.gridLayer.removeChildAt(this.gridLayer.children.length - 1);
    }

    for (const cell of cells) {
      const cellScreenX = pw / 2 + (cell.originX - playerAbsX) * pixelsPerUnit;
      const cellScreenY = ph / 2 + (cell.originY - playerAbsY) * pixelsPerUnit;
      const cellScreenW = cell.size * pixelsPerUnit;
      const cellScreenH = cell.size * pixelsPerUnit;

      // Cull off-panel cells
      if (cellScreenX + cellScreenW < -10 || cellScreenX > pw + 10 ||
          cellScreenY + cellScreenH < -10 || cellScreenY > ph + 10) {
        continue;
      }

      const isSubcell = cell.depth > 0;
      const color = isSubcell ? 0x0088cc : 0x0066aa;
      const alpha = isSubcell ? 0.35 : 0.25;

      if (isSubcell) {
        // Dashed rectangle for subcells
        const dashLen = 6;
        const gapLen = 4;
        this.drawDashedMapRect(
          cellScreenX, cellScreenY, cellScreenW, cellScreenH,
          dashLen, gapLen, color, alpha,
        );
      } else {
        this.gridGfx
          .rect(cellScreenX, cellScreenY, cellScreenW, cellScreenH)
          .stroke({ color, width: 1, alpha });
      }

      // Cell coordinate label at center
      const centerX = cellScreenX + cellScreenW / 2;
      const centerY = cellScreenY + cellScreenH / 2;
      if (centerX > -50 && centerX < pw + 50 && centerY > -20 && centerY < ph + 20) {
        const labelText = `(${cell.cellX},${cell.cellY}${cell.depth > 0 ? `:${cell.depth}` : ""})`;
        const label = new Text({
          text: labelText,
          style: {
            fontFamily: "monospace",
            fontSize: 9,
            fill: isSubcell ? 0x4488cc : 0x334466,
          },
        });
        label.anchor.set(0.5, 0.5);
        label.position.set(centerX, centerY);
        this.gridLayer.addChild(label);
      }
    }
  }

  /** Draw a dashed rectangle on the minimap grid graphics. */
  private drawDashedMapRect(
    x: number, y: number, w: number, h: number,
    dashLen: number, gapLen: number,
    color: number, alpha: number,
  ): void {
    const edges: [number, number, number, number][] = [
      [x, y, x + w, y],
      [x + w, y, x + w, y + h],
      [x + w, y + h, x, y + h],
      [x, y + h, x, y],
    ];
    for (const [x0, y0, x1, y1] of edges) {
      const dx = x1 - x0;
      const dy = y1 - y0;
      const len = Math.sqrt(dx * dx + dy * dy);
      if (len < 1) continue;
      const nx = dx / len;
      const ny = dy / len;
      let d = 0;
      while (d < len) {
        const segEnd = Math.min(d + dashLen, len);
        this.gridGfx
          .moveTo(x0 + nx * d, y0 + ny * d)
          .lineTo(x0 + nx * segEnd, y0 + ny * segEnd)
          .stroke({ color, width: 1, alpha });
        d = segEnd + gapLen;
      }
    }
  }

  private drawMarkers(
    state: GameState,
    playerAbsX: number, playerAbsY: number,
    pixelsPerUnit: number, pw: number, ph: number,
    now: number,
  ): void {
    this.markerGfx.clear();

    // Remove old station label texts from markerLayer (keep markerGfx)
    while (this.markerLayer.children.length > 1) {
      this.markerLayer.removeChildAt(this.markerLayer.children.length - 1);
    }

    // Station markers
    for (const station of state.mapStations) {
      const stationAbsX = station.cellX * CELL_SIZE + station.localX;
      const stationAbsY = station.cellY * CELL_SIZE + station.localY;
      const mx = pw / 2 + (stationAbsX - playerAbsX) * pixelsPerUnit;
      const my = ph / 2 + (stationAbsY - playerAbsY) * pixelsPerUnit;

      // Cull off-panel
      if (mx < -30 || mx > pw + 30 || my < -30 || my > ph + 30) continue;

      // Outer ring
      this.markerGfx
        .circle(mx, my, 8)
        .stroke({ color: 0x88ffaa, width: 1.5, alpha: 0.7 });
      // Center dot
      this.markerGfx
        .circle(mx, my, 2.5)
        .fill({ color: 0x88ffaa, alpha: 0.9 });

      // Station name label
      const stationLabel = new Text({
        text: station.name,
        style: {
          fontFamily: "monospace",
          fontSize: 8,
          fill: 0x88ffaa,
          letterSpacing: 1,
        },
      });
      stationLabel.anchor.set(0.5, 0);
      stationLabel.position.set(mx, my + 12);
      this.markerLayer.addChild(stationLabel);
    }

    // Player marker (always at center)
    const cx = pw / 2;
    const cy = ph / 2;
    const d = 6;

    // Glow background
    this.markerGfx
      .poly([cx, cy - d * 1.3, cx + d * 0.9, cy, cx, cy + d * 1.3, cx - d * 0.9, cy])
      .fill({ color: 0x00ff66, alpha: 0.15 });

    // Diamond
    this.markerGfx
      .poly([cx, cy - d, cx + d * 0.7, cy, cx, cy + d, cx - d * 0.7, cy])
      .fill({ color: 0x00ff66, alpha: 0.9 });

    // Pulse ring
    const pulseT = (now % 2000) / 2000;
    const pulseR = 6 + pulseT * 24;
    const pulseAlpha = 0.4 * (1 - pulseT);
    this.markerGfx
      .circle(cx, cy, pulseR)
      .stroke({ color: 0x00ff66, width: 1, alpha: pulseAlpha });

    // Second offset pulse
    const pulseT2 = ((now + 1000) % 2000) / 2000;
    const pulseR2 = 6 + pulseT2 * 24;
    const pulseAlpha2 = 0.3 * (1 - pulseT2);
    this.markerGfx
      .circle(cx, cy, pulseR2)
      .stroke({ color: 0x00ff66, width: 1, alpha: pulseAlpha2 });
  }

  private drawScanlines(pw: number, ph: number): void {
    this.scanlineOverlay.clear();
    for (let y = 0; y < ph; y += 3) {
      this.scanlineOverlay
        .rect(0, y, pw, 1)
        .fill({ color: 0x000000, alpha: 0.06 });
    }
  }

  private drawCornerAccents(pw: number, ph: number): void {
    this.cornerAccents.clear();
    const L = 30;
    const w = 2;
    const color = 0x00ccff;
    const alpha = 0.6;

    // Top-left
    this.cornerAccents
      .moveTo(0, L).lineTo(0, 0).lineTo(L, 0)
      .stroke({ color, width: w, alpha });
    // Top-right
    this.cornerAccents
      .moveTo(pw - L, 0).lineTo(pw, 0).lineTo(pw, L)
      .stroke({ color, width: w, alpha });
    // Bottom-left
    this.cornerAccents
      .moveTo(0, ph - L).lineTo(0, ph).lineTo(L, ph)
      .stroke({ color, width: w, alpha });
    // Bottom-right
    this.cornerAccents
      .moveTo(pw - L, ph).lineTo(pw, ph).lineTo(pw, ph - L)
      .stroke({ color, width: w, alpha });
  }
}

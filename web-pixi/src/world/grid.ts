import { Container, Graphics, Text, TextStyle } from "pixi.js";
import { px, zoom } from "../view";
import { CELL_SIZE } from "../constants";
import type { CellInfo } from "../state";

const LINE_COLOR = 0x00cccc;
const LINE_ALPHA = 0.3;
const SUBCELL_LINE_COLOR = 0x00cccc;
const SUBCELL_LINE_ALPHA = 0.2;
const LABEL_STYLE = new TextStyle({
  fontFamily: "monospace",
  fontSize: 12,
  fill: 0x00cccc,
});

/** Container holding cell boundary lines and coordinate labels. */
export class CellGrid {
  readonly container: Container;
  private gfx: Graphics;
  private labels: Text[] = [];
  private originSX = 0;
  private originSY = 0;
  private gridCellsX = 0;
  private gridCellsY = 0;
  private topology: CellInfo[] | null = null;

  constructor() {
    this.container = new Container();
    this.gfx = new Graphics();
    this.container.addChild(this.gfx);
  }

  /** Update the player's cell origin (from spawn or cell change). */
  setOrigin(sx: number, sy: number) {
    this.originSX = sx;
    this.originSY = sy;
  }

  /** Set the grid dimensions so lines/labels are clamped to valid cells. */
  setGridSize(cellsX: number, cellsY: number) {
    this.gridCellsX = cellsX;
    this.gridCellsY = cellsY;
  }

  /** Store dynamic cell topology from the server. */
  setTopology(cells: CellInfo[]) {
    this.topology = cells;
    // Derive grid bounds from topology
    if (cells.length > 0) {
      let maxX = 0;
      let maxY = 0;
      for (const c of cells) {
        if (c.depth === 0) {
          maxX = Math.max(maxX, c.cellX + 1);
          maxY = Math.max(maxY, c.cellY + 1);
        }
      }
      if (maxX > 0) this.gridCellsX = maxX;
      if (maxY > 0) this.gridCellsY = maxY;
    }
  }

  /** Redraw cell lines for the current viewport. */
  update(cameraX: number, cameraY: number, screenW: number, screenH: number) {
    this.gfx.clear();

    // Recycle labels
    for (const label of this.labels) {
      label.visible = false;
    }
    let labelIdx = 0;

    if (this.topology) {
      labelIdx = this.drawTopology(cameraX, cameraY, screenW, screenH, labelIdx);
    } else {
      labelIdx = this.drawUniformGrid(cameraX, cameraY, screenW, screenH, labelIdx);
    }
  }

  /** Draw topology-aware cell boundaries from server data. */
  private drawTopology(
    cameraX: number, cameraY: number,
    screenW: number, screenH: number,
    labelIdx: number,
  ): number {
    const z = zoom();
    const viewW = screenW / z;
    const viewH = screenH / z;
    const halfW = viewW / 2;
    const halfH = viewH / 2;

    const left = cameraX - halfW;
    const right = cameraX + halfW;
    const top = cameraY - halfH;
    const bottom = cameraY + halfH;

    const pad = px(4);

    for (const cell of this.topology!) {
      // Cell world origin relative to our coordinate frame
      const localX = cell.originX - this.originSX * CELL_SIZE;
      const localY = cell.originY - this.originSY * CELL_SIZE;
      const size = cell.size;

      // Cull cells outside viewport
      if (localX + size < left || localX > right || localY + size < top || localY > bottom) {
        continue;
      }

      const isSubcell = cell.depth > 0;
      const color = isSubcell ? SUBCELL_LINE_COLOR : LINE_COLOR;
      const alpha = isSubcell ? SUBCELL_LINE_ALPHA : LINE_ALPHA;
      const lineWidth = px(isSubcell ? 1 : 2);

      // Draw cell rectangle
      if (isSubcell) {
        // Dashed lines for subcells
        this.drawDashedRect(localX, localY, size, size, px(8), px(4), color, alpha, lineWidth);
      } else {
        this.gfx
          .rect(localX, localY, size, size)
          .stroke({ color, alpha, width: lineWidth });
      }

      // Labels in all 4 corners
      const text = `${cell.cellX},${cell.cellY}${cell.depth > 0 ? `:${cell.depth}` : ""}`;
      const x0 = localX;
      const y0 = localY;
      const x1 = localX + size;
      const y1 = localY + size;

      // Top-left
      const tl = this.getLabel(labelIdx++);
      tl.text = text;
      tl.anchor.set(0, 0);
      tl.position.set(x0 + pad, y0 + pad);
      tl.scale.set(1 / z);
      tl.visible = true;

      // Top-right
      const tr = this.getLabel(labelIdx++);
      tr.text = text;
      tr.anchor.set(1, 0);
      tr.position.set(x1 - pad, y0 + pad);
      tr.scale.set(1 / z);
      tr.visible = true;

      // Bottom-left
      const bl = this.getLabel(labelIdx++);
      bl.text = text;
      bl.anchor.set(0, 1);
      bl.position.set(x0 + pad, y1 - pad);
      bl.scale.set(1 / z);
      bl.visible = true;

      // Bottom-right
      const br = this.getLabel(labelIdx++);
      br.text = text;
      br.anchor.set(1, 1);
      br.position.set(x1 - pad, y1 - pad);
      br.scale.set(1 / z);
      br.visible = true;
    }

    return labelIdx;
  }

  /** Draw a dashed rectangle. */
  private drawDashedRect(
    x: number, y: number, w: number, h: number,
    dashLen: number, gapLen: number,
    color: number, alpha: number, lineWidth: number,
  ): void {
    // Draw each edge as a dashed line
    this.drawDashedLine(x, y, x + w, y, dashLen, gapLen, color, alpha, lineWidth);
    this.drawDashedLine(x + w, y, x + w, y + h, dashLen, gapLen, color, alpha, lineWidth);
    this.drawDashedLine(x + w, y + h, x, y + h, dashLen, gapLen, color, alpha, lineWidth);
    this.drawDashedLine(x, y + h, x, y, dashLen, gapLen, color, alpha, lineWidth);
  }

  /** Draw a single dashed line segment. */
  private drawDashedLine(
    x0: number, y0: number, x1: number, y1: number,
    dashLen: number, gapLen: number,
    color: number, alpha: number, lineWidth: number,
  ): void {
    const dx = x1 - x0;
    const dy = y1 - y0;
    const len = Math.sqrt(dx * dx + dy * dy);
    if (len < 1) return;
    const nx = dx / len;
    const ny = dy / len;
    let d = 0;
    while (d < len) {
      const segEnd = Math.min(d + dashLen, len);
      this.gfx
        .moveTo(x0 + nx * d, y0 + ny * d)
        .lineTo(x0 + nx * segEnd, y0 + ny * segEnd)
        .stroke({ color, alpha, width: lineWidth });
      d = segEnd + gapLen;
    }
  }

  /** Draw uniform grid (fallback when no topology). */
  private drawUniformGrid(
    cameraX: number, cameraY: number,
    screenW: number, screenH: number,
    labelIdx: number,
  ): number {
    const z = zoom();
    const viewW = screenW / z;
    const viewH = screenH / z;
    const halfW = viewW / 2;
    const halfH = viewH / 2;

    const left = cameraX - halfW;
    const right = cameraX + halfW;
    const top = cameraY - halfH;
    const bottom = cameraY + halfH;

    // Cell boundaries in local coords: n * CELL_SIZE relative to origin
    let firstSX = Math.floor(left / CELL_SIZE);
    let lastSX = Math.ceil(right / CELL_SIZE);
    let firstSY = Math.floor(top / CELL_SIZE);
    let lastSY = Math.ceil(bottom / CELL_SIZE);
    if (this.gridCellsX > 0) {
      firstSX = Math.max(firstSX, -this.originSX);
      lastSX = Math.min(lastSX, this.gridCellsX - this.originSX);
    }
    if (this.gridCellsY > 0) {
      firstSY = Math.max(firstSY, -this.originSY);
      lastSY = Math.min(lastSY, this.gridCellsY - this.originSY);
    }

    // Draw vertical cell boundary lines
    for (let sx = firstSX; sx <= lastSX; sx++) {
      const x = sx * CELL_SIZE;
      this.gfx.moveTo(x, top).lineTo(x, bottom);
    }

    // Draw horizontal cell boundary lines
    for (let sy = firstSY; sy <= lastSY; sy++) {
      const y = sy * CELL_SIZE;
      this.gfx.moveTo(left, y).lineTo(right, y);
    }

    // Place cell coordinate labels in all 4 corners of each visible cell
    const pad = px(4);
    let firstSecX = Math.floor(left / CELL_SIZE);
    let lastSecX = Math.floor(right / CELL_SIZE);
    let firstSecY = Math.floor(top / CELL_SIZE);
    let lastSecY = Math.floor(bottom / CELL_SIZE);
    if (this.gridCellsX > 0) {
      firstSecX = Math.max(firstSecX, -this.originSX);
      lastSecX = Math.min(lastSecX, this.gridCellsX - 1 - this.originSX);
    }
    if (this.gridCellsY > 0) {
      firstSecY = Math.max(firstSecY, -this.originSY);
      lastSecY = Math.min(lastSecY, this.gridCellsY - 1 - this.originSY);
    }

    for (let sx = firstSecX; sx <= lastSecX; sx++) {
      for (let sy = firstSecY; sy <= lastSecY; sy++) {
        const worldSX = sx + this.originSX;
        const worldSY = sy + this.originSY;
        const text = `${worldSX},${worldSY}`;
        const x0 = sx * CELL_SIZE;
        const y0 = sy * CELL_SIZE;
        const x1 = (sx + 1) * CELL_SIZE;
        const y1 = (sy + 1) * CELL_SIZE;

        // Top-left
        const tl = this.getLabel(labelIdx++);
        tl.text = text;
        tl.anchor.set(0, 0);
        tl.position.set(x0 + pad, y0 + pad);
        tl.scale.set(1 / z);
        tl.visible = true;

        // Top-right
        const tr = this.getLabel(labelIdx++);
        tr.text = text;
        tr.anchor.set(1, 0);
        tr.position.set(x1 - pad, y0 + pad);
        tr.scale.set(1 / z);
        tr.visible = true;

        // Bottom-left
        const bl = this.getLabel(labelIdx++);
        bl.text = text;
        bl.anchor.set(0, 1);
        bl.position.set(x0 + pad, y1 - pad);
        bl.scale.set(1 / z);
        bl.visible = true;

        // Bottom-right
        const br = this.getLabel(labelIdx++);
        br.text = text;
        br.anchor.set(1, 1);
        br.position.set(x1 - pad, y1 - pad);
        br.scale.set(1 / z);
        br.visible = true;
      }
    }

    this.gfx.stroke({ color: LINE_COLOR, alpha: LINE_ALPHA, width: px(2) });
    return labelIdx;
  }

  private getLabel(idx: number): Text {
    if (idx < this.labels.length) return this.labels[idx];
    const label = new Text({ text: "", style: LABEL_STYLE });
    label.alpha = 0.5;
    this.labels.push(label);
    this.container.addChild(label);
    return label;
  }
}

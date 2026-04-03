import { Container, Graphics, Text, TextStyle } from "pixi.js";
import { px, zoom } from "../view";
import { CELL_SIZE } from "../constants";

const LINE_COLOR = 0x00cccc;
const LINE_ALPHA = 0.3;
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

  /** Redraw cell lines for the current viewport. */
  update(cameraX: number, cameraY: number, screenW: number, screenH: number) {
    this.gfx.clear();

    // Recycle labels
    for (const label of this.labels) {
      label.visible = false;
    }
    let labelIdx = 0;

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
    // The origin cell's local (0,0) maps to world cell (originSX, originSY)
    // So cell boundary at world cell N is at local x = (N - originSX) * CELL_SIZE

    // Find range of cell boundaries visible, clamped to grid bounds (in local space)
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

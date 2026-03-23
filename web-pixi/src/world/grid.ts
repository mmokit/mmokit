import { Container, Graphics, Text, TextStyle } from "pixi.js";
import { px, zoom } from "../view";
import { SECTOR_SIZE } from "../constants";

const LINE_COLOR = 0x00cccc;
const LINE_ALPHA = 0.3;
const LABEL_STYLE = new TextStyle({
  fontFamily: "monospace",
  fontSize: 12,
  fill: 0x00cccc,
});

/** Container holding sector boundary lines and coordinate labels. */
export class SectorGrid {
  readonly container: Container;
  private gfx: Graphics;
  private labels: Text[] = [];
  private originSX = 0;
  private originSY = 0;

  constructor() {
    this.container = new Container();
    this.gfx = new Graphics();
    this.container.addChild(this.gfx);
  }

  /** Update the player's sector origin (from spawn or sector change). */
  setOrigin(sx: number, sy: number) {
    this.originSX = sx;
    this.originSY = sy;
  }

  /** Redraw sector lines for the current viewport. */
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

    // Sector boundaries in local coords: n * SECTOR_SIZE relative to origin
    // The origin sector's local (0,0) maps to world sector (originSX, originSY)
    // So sector boundary at world sector N is at local x = (N - originSX) * SECTOR_SIZE

    // Find range of sector boundaries visible
    const firstSX = Math.floor(left / SECTOR_SIZE);
    const lastSX = Math.ceil(right / SECTOR_SIZE);
    const firstSY = Math.floor(top / SECTOR_SIZE);
    const lastSY = Math.ceil(bottom / SECTOR_SIZE);

    // Draw vertical lines
    for (let sx = firstSX; sx <= lastSX; sx++) {
      const x = sx * SECTOR_SIZE;
      this.gfx.moveTo(x, top).lineTo(x, bottom);
    }

    // Draw horizontal lines
    for (let sy = firstSY; sy <= lastSY; sy++) {
      const y = sy * SECTOR_SIZE;
      this.gfx.moveTo(left, y).lineTo(right, y);
    }

    // Place sector coordinate labels in all 4 corners of each visible sector
    const pad = px(4);
    const firstSecX = Math.floor(left / SECTOR_SIZE);
    const lastSecX = Math.floor(right / SECTOR_SIZE);
    const firstSecY = Math.floor(top / SECTOR_SIZE);
    const lastSecY = Math.floor(bottom / SECTOR_SIZE);

    for (let sx = firstSecX; sx <= lastSecX; sx++) {
      for (let sy = firstSecY; sy <= lastSecY; sy++) {
        const worldSX = sx + this.originSX;
        const worldSY = sy + this.originSY;
        const text = `${worldSX},${worldSY}`;
        const x0 = sx * SECTOR_SIZE;
        const y0 = sy * SECTOR_SIZE;
        const x1 = (sx + 1) * SECTOR_SIZE;
        const y1 = (sy + 1) * SECTOR_SIZE;

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

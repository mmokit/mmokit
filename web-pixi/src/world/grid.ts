import { Graphics } from "pixi.js";
import { px, zoom } from "../view";

const gridSize = 6.7;

export function createGrid(screenW: number, screenH: number): Graphics {
  const grid = new Graphics();
  drawGrid(grid, screenW, screenH);
  return grid;
}

export function drawGrid(grid: Graphics, screenW: number, screenH: number): void {
  grid.clear();
  const viewW = screenW / zoom();
  const viewH = screenH / zoom();
  const cols = Math.ceil(viewW / gridSize) + 2;
  const rows = Math.ceil(viewH / gridSize) + 2;

  for (let i = 0; i < cols; i++) {
    grid.moveTo(i * gridSize, 0).lineTo(i * gridSize, rows * gridSize);
  }
  for (let j = 0; j < rows; j++) {
    grid.moveTo(0, j * gridSize).lineTo(cols * gridSize, j * gridSize);
  }
  grid.stroke({ color: 0xffffff, alpha: 0.04, width: px(1) });
}

export function updateGridPosition(grid: Graphics, cameraX: number, cameraY: number, screenW: number, screenH: number): void {
  const viewW = screenW / zoom();
  const viewH = screenH / zoom();
  const startX = Math.floor((cameraX - viewW / 2) / gridSize) * gridSize;
  const startY = Math.floor((cameraY - viewH / 2) / gridSize) * gridSize;
  grid.position.set(startX, startY);
}

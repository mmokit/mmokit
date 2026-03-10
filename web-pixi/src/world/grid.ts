import { Graphics } from "pixi.js";

export function createGrid(screenW: number, screenH: number): Graphics {
  const grid = new Graphics();
  drawGrid(grid, screenW, screenH);
  return grid;
}

export function drawGrid(grid: Graphics, screenW: number, screenH: number): void {
  grid.clear();
  const gridSize = 200;
  const cols = Math.ceil(screenW / gridSize) + 2;
  const rows = Math.ceil(screenH / gridSize) + 2;

  for (let i = 0; i < cols; i++) {
    grid.moveTo(i * gridSize, 0).lineTo(i * gridSize, rows * gridSize);
  }
  for (let j = 0; j < rows; j++) {
    grid.moveTo(0, j * gridSize).lineTo(cols * gridSize, j * gridSize);
  }
  grid.stroke({ color: 0xffffff, alpha: 0.04, width: 1 });
}

export function updateGridPosition(grid: Graphics, cameraX: number, cameraY: number, screenW: number, screenH: number): void {
  const gridSize = 200;
  const startX = Math.floor((cameraX - screenW / 2) / gridSize) * gridSize;
  const startY = Math.floor((cameraY - screenH / 2) / gridSize) * gridSize;
  grid.position.set(startX, startY);
}

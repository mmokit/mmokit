import { state } from "./state.js";
import { sendMoveTarget } from "./network.js";
import { VIEWPORT_SCALE } from "./constants.js";
import { isSnapMode } from "../sdk/entities.js";

export function setupInput(canvas: HTMLCanvasElement): void {
  let mouseHeld = false;

  function worldCoords(e: MouseEvent): [number, number] {
    const rect = canvas.getBoundingClientRect();
    const scaleX = canvas.width / rect.width;
    const scaleY = canvas.height / rect.height;
    const canvasX = (e.clientX - rect.left) * scaleX;
    const canvasY = (e.clientY - rect.top) * scaleY;
    const scale = Math.min(canvas.width, canvas.height) / VIEWPORT_SCALE;
    const wx = (canvasX - canvas.width / 2) / scale + state.camX;
    const wy = (canvasY - canvas.height / 2) / scale + state.camY;
    return [wx, wy];
  }

  function setMoveTarget(e: MouseEvent): void {
    const [wx, wy] = worldCoords(e);
    state.moveTargetX = wx;
    state.moveTargetY = wy;
    state.moveTargetActive = true;
    if (!isSnapMode()) {
      const player = state.entities.get(state.playerNetID);
      if (player && !state.predictionActive) {
        state.predictedX = player.worldX;
        state.predictedY = player.worldY;
        state.predictionActive = true;
        state.predictionStartTime = performance.now();
      }
    }
    sendMoveTarget();
  }

  canvas.addEventListener("mousedown", (e) => { mouseHeld = true; setMoveTarget(e); });
  canvas.addEventListener("mousemove", (e) => { if (mouseHeld) setMoveTarget(e); });
  canvas.addEventListener("mouseup", () => { mouseHeld = false; });
  canvas.addEventListener("mouseleave", () => { mouseHeld = false; });
  canvas.addEventListener("contextmenu", (e) => e.preventDefault());
}

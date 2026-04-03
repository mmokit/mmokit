import { connect, onShowGame } from "./network.js";
import { setupInput } from "./input.js";
import { startRenderLoop } from "./renderer.js";

function resizeCanvas(): void {
  const canvas = document.getElementById("canvas") as HTMLCanvasElement;
  canvas.width = window.innerWidth;
  canvas.height = window.innerHeight;
}

function showGame(): void {
  document.getElementById("login")!.style.display = "none";
  document.getElementById("game")!.style.display = "block";
  resizeCanvas();
  setupInput(document.getElementById("canvas") as HTMLCanvasElement);
  startRenderLoop();
}

onShowGame(showGame);
window.addEventListener("resize", resizeCanvas);

const connectBtn = document.getElementById("connectBtn")!;
const nameInput = document.getElementById("nameInput") as HTMLInputElement;

connectBtn.addEventListener("click", () => {
  const name = nameInput.value.trim();
  if (!name) {
    document.getElementById("status")!.textContent = "enter a name";
    return;
  }
  document.getElementById("status")!.textContent = "connecting...";
  connect(name);
});

nameInput.addEventListener("keydown", (e) => {
  if (e.key === "Enter") connectBtn.click();
});

nameInput.focus();

import { connect, onShowGame } from "./network.js";
import { loginAsName } from "./auth.js";
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

connectBtn.addEventListener("click", async () => {
  const name = nameInput.value.trim();
  const status = document.getElementById("status")!;
  if (!name) {
    status.textContent = "enter a name";
    return;
  }
  status.textContent = "authenticating...";
  try {
    const username = await loginAsName(name);
    status.textContent = "connecting...";
    connect(username);
  } catch (e) {
    status.textContent = e instanceof Error ? e.message : String(e);
  }
});

nameInput.addEventListener("keydown", (e) => {
  if (e.key === "Enter") connectBtn.click();
});

nameInput.focus();

import { setupInput, sendInput } from "./input";
import { handleLoginKeydown, renderLoginScreen } from "./login";
import { connect } from "./network";
import { startRenderLoop } from "./render";
import { createStarLayers } from "./render/starfield";
import { createInitialState } from "./state";

const canvas = document.getElementById("game") as HTMLCanvasElement;
const ctx = canvas.getContext("2d")!;

// Resize canvas to fill window
function resize() {
  canvas.width = window.innerWidth;
  canvas.height = window.innerHeight;
}
window.addEventListener("resize", resize);
resize();

const state = createInitialState();
state.starLayers = createStarLayers();

// Set up game input handlers (keyboard, mouse)
setupInput(state, canvas);

// Start input sending loop (20Hz)
setInterval(() => sendInput(state), 50);

// Login keydown handler — needs to be added/removed during login flow
let loginKeydownHandler: ((e: KeyboardEvent) => void) | null = null;

function startLoginScreen() {
  state.loginActive = true;
  loginKeydownHandler = (e: KeyboardEvent) => handleLoginKeydown(state, e, onLogin);
  window.addEventListener("keydown", loginKeydownHandler);
  requestAnimationFrame(() => renderLoginScreen(ctx, canvas, state));
}

function onLogin() {
  // Remove login keydown handler
  if (loginKeydownHandler) {
    window.removeEventListener("keydown", loginKeydownHandler);
    loginKeydownHandler = null;
  }
  // Connect and start game render loop
  connect(state, () => {}, startLoginScreen);
  startRenderLoop(ctx, canvas, state);
}

// Start with login screen
startLoginScreen();

import type { GameState } from "./state";

export function handleLoginKeydown(
  state: GameState,
  e: KeyboardEvent,
  onLogin: () => void,
): void {
  if (!state.loginActive) return;
  if (e.key === "Enter" && state.loginInput.trim().length > 0) {
    e.preventDefault();
    state.playerUsername = state.loginInput.trim();
    state.spawnedOnce = false;
    localStorage.setItem("username", state.playerUsername);
    state.loggedIn = true;
    state.loginActive = false;
    onLogin();
    return;
  }
  if (e.key === "Backspace") {
    e.preventDefault();
    state.loginInput = state.loginInput.slice(0, -1);
    state.loginError = "";
    return;
  }
  if (e.key.length === 1 && state.loginInput.length < 20) {
    e.preventDefault();
    state.loginInput += e.key.toLowerCase();
    state.loginError = "";
  }
}

export function renderLoginScreen(
  ctx: CanvasRenderingContext2D,
  canvas: HTMLCanvasElement,
  state: GameState,
): void {
  if (!state.loginActive) return;
  requestAnimationFrame(() => renderLoginScreen(ctx, canvas, state));

  const now = performance.now();
  state.loginCursorTimer += 16;
  if (state.loginCursorTimer > 530) {
    state.loginCursorVisible = !state.loginCursorVisible;
    state.loginCursorTimer = 0;
  }

  ctx.fillStyle = "#000";
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  // Starfield background
  const starSeed = 42;
  for (let i = 0; i < 120; i++) {
    const sx = (starSeed * (i + 1) * 7919) % canvas.width;
    const sy = (starSeed * (i + 1) * 6271) % canvas.height;
    const brightness = 0.2 + 0.4 * Math.sin(now * 0.001 + i);
    ctx.fillStyle = `rgba(255, 255, 255, ${brightness})`;
    ctx.fillRect(sx, sy, 1.5, 1.5);
  }

  const cx = canvas.width / 2;
  const cy = canvas.height / 2;

  // Panel
  const pw = 360;
  const ph = 200;
  const px = cx - pw / 2;
  const py = cy - ph / 2;

  ctx.fillStyle = "rgba(10, 12, 20, 0.94)";
  ctx.fillRect(px, py, pw, ph);
  ctx.strokeStyle = "rgba(68, 170, 255, 0.5)";
  ctx.lineWidth = 1;
  ctx.strokeRect(px, py, pw, ph);

  // Title
  ctx.fillStyle = "#4af";
  ctx.font = "bold 28px monospace";
  ctx.textAlign = "center";
  ctx.fillText("SPACE MMO", cx, py + 45);

  // Subtitle
  ctx.fillStyle = "#667";
  ctx.font = "12px monospace";
  ctx.fillText("Enter your callsign, pilot", cx, py + 68);

  // Input field
  const fieldW = pw - 60;
  const fieldH = 36;
  const fieldX = cx - fieldW / 2;
  const fieldY = py + 85;

  ctx.fillStyle = "rgba(255, 255, 255, 0.06)";
  ctx.fillRect(fieldX, fieldY, fieldW, fieldH);
  ctx.strokeStyle = "rgba(68, 170, 255, 0.6)";
  ctx.lineWidth = 1;
  ctx.strokeRect(fieldX, fieldY, fieldW, fieldH);

  // Input text
  ctx.fillStyle = "#eee";
  ctx.font = "16px monospace";
  ctx.textAlign = "left";
  const displayText = state.loginInput + (state.loginCursorVisible ? "_" : "");
  ctx.fillText(displayText, fieldX + 12, fieldY + 24);

  // Error/instruction message
  ctx.textAlign = "center";
  if (state.loginError) {
    ctx.fillStyle = "#f44";
    ctx.font = "13px monospace";
    ctx.fillText(state.loginError, cx, fieldY + fieldH + 30);
  } else if (state.loginInput.trim().length > 0) {
    ctx.fillStyle = "#4af";
    ctx.font = "13px monospace";
    ctx.fillText("Press ENTER to launch", cx, fieldY + fieldH + 30);
  } else {
    ctx.fillStyle = "#445";
    ctx.font = "13px monospace";
    ctx.fillText("Type a username to begin", cx, fieldY + fieldH + 30);
  }

  ctx.textAlign = "left";
}

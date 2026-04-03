import { Application } from "pixi.js";
import { GameState, CELL_SIZE } from "./state";
import { Network } from "./network";
import { Input } from "./input";
import { Camera } from "./camera";
import { Background } from "./background";
import { SnakeRenderer } from "./snake-renderer";
import { FoodRenderer } from "./food-renderer";
import { Minimap } from "./minimap";
import { Leaderboard } from "./leaderboard";
import { KillFeed } from "./killfeed";
import { LoginScreen } from "./login";
import { getInterpolationT, interpolateSnake } from "./interpolation";
import type { InterpolatedSnake } from "./interpolation";

async function main() {
  const app = new Application();
  await app.init({
    background: 0x111111,
    resizeTo: window,
    antialias: true,
    autoDensity: true,
    resolution: window.devicePixelRatio || 1,
  });
  document.getElementById("app")!.appendChild(app.canvas);

  const state = new GameState();
  const camera = new Camera();
  const minimap = new Minimap();
  const leaderboard = new Leaderboard();
  const killFeed = new KillFeed();
  const loginScreen = new LoginScreen();

  // Score HUD
  const scoreDisplay = document.createElement("div");
  scoreDisplay.style.cssText = `
    position: fixed;
    bottom: 20px;
    left: 50%;
    transform: translateX(-50%);
    color: white;
    font-family: Arial, sans-serif;
    font-size: 24px;
    font-weight: bold;
    text-shadow: 0 2px 4px rgba(0,0,0,0.5);
    z-index: 50;
    pointer-events: none;
    display: none;
  `;
  document.body.appendChild(scoreDisplay);

  let lastMass = 0;
  let isDead = false;

  // Renderers
  const background = new Background(app.stage, CELL_SIZE, 2, 2);
  const foodRenderer = new FoodRenderer(app.stage);
  const snakeRenderer = new SnakeRenderer(app.stage);

  // Network
  let connected = false;

  const network = new Network({
    onOpen: () => {
      connected = true;
      loginScreen.setStatus("Connected. Enter your name to play.");
    },
    onClose: () => {
      connected = false;
      isDead = false;
      loginScreen.setStatus("Disconnected. Refresh to reconnect.");
      input.stopSending();
      state.spawned = false;
      loginScreen.show();
      leaderboard.hide();
      killFeed.hide();
      minimap.hide();
    },
    onWorldUpdate: (data) => {
      state.applyWorldUpdate(data.tick, data.snakes, data.foods, data.removed);

      // Check for death
      if (state.dead) {
        state.dead = false;
        state.spawned = false;
        state.myEntityID = 0;
        isDead = true;
        input.stopSending();
        loginScreen.showDeath(`You died! Score: ${Math.floor(lastMass)}`);
        loginScreen.show();
        scoreDisplay.style.display = "none";
        leaderboard.hide();
        killFeed.hide();
        minimap.hide();
      }
    },
    onLeaderboard: (data) => {
      state.leaderboard = data.entries;
      leaderboard.update(data.entries);
    },
    onKillFeed: (data) => {
      const now = performance.now();
      for (const entry of data.entries) {
        state.killFeed.push({ ...entry, time: now });
      }
      state.killFeed = state.killFeed.filter((e) => now - e.time < 6000);
    },
    onSpawned: (data) => {
      state.myEntityID = data.entityID;
      state.handleCellChange(data.cellX, data.cellY);
      state.spawned = true;
      state.dead = false;
      loginScreen.hide();
      loginScreen.hideDeath();
      leaderboard.show();
      killFeed.show();
      minimap.show();
      input.startSending();
    },
  });

  const input = new Input(network);

  // Login screen
  loginScreen.setOnPlay((name, skinID) => {
    if (!connected) {
      loginScreen.setStatus("Not connected to server.");
      return;
    }
    leaderboard.setMyName(name);
    if (isDead) {
      network.sendRespawn();
      isDead = false;
    } else {
      network.sendSkinSelect(skinID, name);
    }
  });

  // Start
  loginScreen.show();
  loginScreen.setStatus("Connecting...");
  network.connect();

  // Attach input after canvas is ready
  input.attach(app.canvas as HTMLCanvasElement);

  // Resize handler
  const handleResize = () => {
    camera.setScreenSize(app.screen.width, app.screen.height);
  };
  handleResize();
  window.addEventListener("resize", handleResize);

  // Game loop
  app.ticker.add((ticker) => {
    const dt = ticker.deltaMS / 1000; // seconds since last frame
    const t = getInterpolationT(state);

    // Cell offset: server sends local coords, we add offset for world space
    const sox = state.cellOffsetX;
    const soy = state.cellOffsetY;

    // Interpolate all snakes with frame-rate independent smoothing
    const interpolatedSnakes: InterpolatedSnake[] = [];
    for (const [, snake] of state.snakes) {
      const interp = interpolateSnake(snake, t, dt);
      // Convert to world space for rendering
      interp.headX += sox;
      interp.headY += soy;
      for (const seg of interp.segments) {
        seg.x += sox;
        seg.y += soy;
      }
      interpolatedSnakes.push(interp);
    }

    // Update camera (in world space)
    const mySnake = interpolatedSnakes.find(
      (s) => s.id === state.myEntityID
    );
    if (mySnake) {
      camera.update(mySnake.headX, mySnake.headY, mySnake.mass, dt);
      lastMass = mySnake.mass;
      scoreDisplay.style.display = "block";
      scoreDisplay.textContent = `Score: ${Math.floor(mySnake.mass)}`;
    } else if (!state.spawned) {
      scoreDisplay.style.display = "none";
    }

    // Convert food to world space for rendering
    const worldFood = new Map<number, { id: number; x: number; y: number; value: number; color: number }>();
    for (const [id, f] of state.food) {
      worldFood.set(id, { ...f, x: f.x + sox, y: f.y + soy });
    }

    // Convert eaten food animations to world space
    const worldEaten = state.eatenFood.map((e) => ({
      ...e,
      x: e.x + sox,
      y: e.y + soy,
    }));

    // Render (camera is in world space, all positions are world space)
    background.render(camera);
    foodRenderer.render(worldFood, camera, interpolatedSnakes, worldEaten);
    snakeRenderer.render(interpolatedSnakes, camera, state.myEntityID);

    // UI overlays
    minimap.update(state, interpolatedSnakes, camera);
    killFeed.update(state.killFeed);
  });
}

main().catch(console.error);

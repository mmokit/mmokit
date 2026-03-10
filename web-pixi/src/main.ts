import { Application, Container } from "pixi.js";
import { TICK_INTERVAL } from "./constants";
import { interpolateEntities } from "./interpolation";
import { createInitialState } from "./state";
import { setupInput, sendInput } from "./input";
import { connect } from "./network";
import { setupLogin, showLogin } from "./ui/login";
import { Camera } from "./world/camera";
import { Starfield } from "./world/starfield";
import { createGrid, drawGrid, updateGridPosition } from "./world/grid";
import { EntityManager } from "./entities/entity-manager";
import { ThrusterRenderer } from "./effects/thruster";
import { ExplosionRenderer } from "./effects/explosion";
import { MiningLaserRenderer } from "./effects/mining-laser";
import { TargetHighlight } from "./effects/target-highlight";
import { Minimap } from "./world/minimap";
import {
  updateHUD,
  updateStatusBars,
  updateStationPrompt,
  updateDeathScreen,
  updateCargoPanel,
  updateToasts,
} from "./ui/hud";

async function main() {
  const state = createInitialState();

  // Create PixiJS application
  const app = new Application();
  await app.init({
    background: 0x000000,
    resizeTo: window,
    antialias: true,
  });

  // Insert canvas into DOM (before UI overlays)
  document.body.insertBefore(app.canvas, document.body.firstChild);

  // Scene graph hierarchy
  const worldContainer = new Container();
  app.stage.addChild(worldContainer);

  const starfieldContainer = new Container();
  worldContainer.addChild(starfieldContainer);

  const gridContainer = new Container();
  worldContainer.addChild(gridContainer);

  const entityContainer = new Container();
  worldContainer.addChild(entityContainer);

  const uiEntityContainer = new Container(); // Non-rotating ship bars/names
  worldContainer.addChild(uiEntityContainer);

  const effectsContainer = new Container();
  worldContainer.addChild(effectsContainer);

  const particleContainer = new Container();
  worldContainer.addChild(particleContainer);

  // Camera
  const camera = new Camera(worldContainer);
  camera.resize(app.screen.width, app.screen.height);

  // Starfield
  const starfield = new Starfield(starfieldContainer);

  // Grid
  const grid = createGrid(app.screen.width, app.screen.height);
  gridContainer.addChild(grid);

  // Entity manager
  const entityManager = new EntityManager(entityContainer, uiEntityContainer);

  // Effects
  const thrusterRenderer = new ThrusterRenderer(particleContainer);
  const explosionRenderer = new ExplosionRenderer(particleContainer);
  const miningLaserRenderer = new MiningLaserRenderer(effectsContainer);
  const targetHighlight = new TargetHighlight(effectsContainer);

  // Minimap
  const minimap = new Minimap();

  // Handle resize
  window.addEventListener("resize", () => {
    camera.resize(app.screen.width, app.screen.height);
    drawGrid(grid, app.screen.width, app.screen.height);
  });

  // Input setup
  setupInput(
    state,
    (wx, wy) => camera.worldToScreen(wx, wy),
    (sx, sy) => camera.screenToWorld(sx, sy),
  );

  // Input sending loop (20Hz)
  setInterval(() => sendInput(state), TICK_INTERVAL);

  // Login flow
  setupLogin((username) => {
    state.playerUsername = username;
    state.loggedIn = true;

    connect(state, {
      onSpawned() {
        // Game is running — nothing extra needed, ticker handles rendering
      },
      onDisconnected() {
        entityManager.clear();
      },
      onLoginRejected(reason) {
        state.loggedIn = false;
        showLogin(reason || "Login rejected");
      },
    });
  });

  // Main game loop via PixiJS ticker
  app.ticker.add(() => {
    if (!state.loggedIn) return;

    const now = performance.now();

    // FPS counter
    state.frameCount++;
    if (now - state.lastFpsTime >= 1000) {
      state.fps = state.frameCount;
      state.frameCount = 0;
      state.lastFpsTime = now;
    }

    // Interpolation
    let t = 0;
    if (state.lastTickTime > 0) {
      t = (now - state.lastTickTime) / TICK_INTERVAL;
      t = Math.max(0, Math.min(t, 2.0));
    }
    interpolateEntities(state.entities, t);

    // Camera follows player
    const myEntity = state.entities.get(state.myEntityId);
    if (myEntity) {
      camera.update(myEntity.renderX, myEntity.renderY);
    }

    // Update starfield
    starfield.update(camera.x, camera.y, app.screen.width, app.screen.height, now);

    // Update grid position
    updateGridPosition(grid, camera.x, camera.y, app.screen.width, app.screen.height);

    // Sync entity display objects
    entityManager.sync(state.entities, state.myEntityId, now);

    // Particles and effects
    const dt = app.ticker.deltaMS / 1000;
    thrusterRenderer.update(state, dt);
    explosionRenderer.update(state.explosions, now);
    miningLaserRenderer.update(state, now);
    targetHighlight.update(state, now);

    // HTML UI updates
    updateHUD(state);
    updateStatusBars(state);
    updateStationPrompt(state);
    updateDeathScreen(state);
    updateCargoPanel(state);
    updateToasts(state);

    // Minimap
    minimap.update(state, app.screen.width, app.screen.height);
  });
}

main().catch(console.error);

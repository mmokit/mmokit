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
import { LockOnRing } from "./effects/lock-on-ring";
import { MoveIndicator } from "./effects/move-indicator";
import { AbilityEffectRenderer } from "./effects/ability-effects";
import { Minimap } from "./world/minimap";
import { createAbilityBar, updateAbilityBar } from "./ui/ability-bar";
import { createLockOverlay, updateLockOverlay } from "./ui/lock-overlay";
import {
  updateHUD,
  updateStatusBars,
  updateStationPrompt,
  updateDeathScreen,
  updateCargoPanel,
  updateToasts,
} from "./ui/hud";
import { audio } from "./audio/audio-manager";
import { initEscMenu, updateEscMenu } from "./ui/esc-menu";

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
  camera.resize(window.innerWidth, window.innerHeight);

  // Starfield
  const starfield = new Starfield(starfieldContainer);

  // Grid
  const grid = createGrid(window.innerWidth, window.innerHeight);
  gridContainer.addChild(grid);

  // Entity manager
  const entityManager = new EntityManager(entityContainer, uiEntityContainer);

  // Effects
  const thrusterRenderer = new ThrusterRenderer(particleContainer);
  const explosionRenderer = new ExplosionRenderer(particleContainer);
  const miningLaserRenderer = new MiningLaserRenderer(effectsContainer);
  const targetHighlight = new TargetHighlight(effectsContainer);
  const lockOnRing = new LockOnRing(effectsContainer);
  const moveIndicator = new MoveIndicator(effectsContainer);
  const abilityEffectRenderer = new AbilityEffectRenderer(effectsContainer);

  // Ability bar (HTML overlay)
  createAbilityBar();

  // Lock target overlay (HTML)
  createLockOverlay(() => {
    state.lockTargetId = 0;
    state.lockProgress = 0;
  });

  // Minimap
  const minimap = new Minimap();

  // Handle resize — read window dimensions directly since PixiJS
  // resizeTo updates asynchronously and app.screen may be stale.
  window.addEventListener("resize", () => {
    const w = window.innerWidth;
    const h = window.innerHeight;
    camera.resize(w, h);
    drawGrid(grid, w, h);
  });

  // Input setup
  setupInput(
    state,
    (wx, wy) => camera.worldToScreen(wx, wy),
    (sx, sy) => camera.screenToWorld(sx, sy),
    (wx, wy) => moveIndicator.show(wx, wy),
  );

  // Input sending loop (20Hz)
  setInterval(() => sendInput(state), TICK_INTERVAL);

  // Initialize audio (preloads all sounds) and ESC menu
  audio.init();
  initEscMenu();

  // Login flow
  setupLogin((username) => {
    state.playerUsername = username;
    state.loggedIn = true;

    connect(state, {
      onSpawned() {
        audio.playMusic();
      },
      onDisconnected() {
        audio.stopAllLoops();
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
      camera.update(myEntity.renderX, myEntity.renderY, state.screenShake);
    }

    // Clear expired screen shake
    if (state.screenShake && now - state.screenShake.startTime >= state.screenShake.duration) {
      state.screenShake = null;
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
    lockOnRing.update(state, now);
    moveIndicator.update(state, now);
    abilityEffectRenderer.update(state, now);

    // HTML UI updates
    updateHUD(state);
    updateStatusBars(state);
    updateStationPrompt(state);
    updateDeathScreen(state);
    updateCargoPanel(state);
    updateToasts(state);
    updateAbilityBar(state);
    updateLockOverlay(state);
    updateEscMenu(state);

    // Minimap
    minimap.update(state, app.screen.width, app.screen.height);
  });
}

main().catch(console.error);

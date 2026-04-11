import { Application, Container } from "pixi.js";
import { CELL_SIZE, TICK_INTERVAL } from "./constants";
import { interpolateEntities } from "./interpolation";
import { createInitialState } from "./state";
import { setupInput, sendInput } from "./input";
import { connect } from "./network";
import { setupLogin, showLogin } from "./ui/login";
import { initZoom, scrollZoom } from "./view";
import { Camera } from "./world/camera";
import { Starfield } from "./world/starfield";
import { Nebula } from "./world/nebula";
import { Planets } from "./world/planets";
import { CellGrid } from "./world/grid";
import { EntityManager } from "./entities/entity-manager";
import { ThrusterRenderer } from "./effects/thruster";
import { ExplosionRenderer } from "./effects/explosion";
import { MiningLaserRenderer } from "./effects/mining-laser";
import { TargetHighlight } from "./effects/target-highlight";
import { LockOnRing } from "./effects/lock-on-ring";
import { BeingLockedRing } from "./effects/being-locked-ring";
import { MoveIndicator } from "./effects/move-indicator";
import { AbilityEffectRenderer } from "./effects/ability-effects";
import { TractorBeamRenderer } from "./effects/tractor-beam";
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
import { updateBankPanel } from "./ui/bank";
import { createLootPopup, updateLootPopup } from "./ui/loot-popup";
import { createMarketPanel, updateMarketPanel } from "./ui/market";
import { CellMap } from "./ui/cell-map";

async function main() {
  const state = createInitialState();
  if (import.meta.env.DEV) {
    (window as unknown as { __state: unknown }).__state = state;
  }

  // Create PixiJS application. We intentionally do NOT set `resizeTo:
  // window` — we want manual control so the canvas can shrink to leave
  // room for the cargo/loadout sidebar when it's open. See applyViewport().
  const app = new Application();
  await app.init({
    background: 0x000000,
    width: window.innerWidth,
    height: window.innerHeight,
    antialias: true,
  });

  // Insert canvas into DOM (before UI overlays)
  document.body.insertBefore(app.canvas, document.body.firstChild);

  // Scene graph hierarchy
  app.stage.sortableChildren = true;
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

  // Zoom baseline must be established BEFORE the Camera constructor
  // reads zoom() and applies it to the world container scale. This is
  // the only place zoom is computed from window width; subsequent
  // resizes leave the baseline alone.
  initZoom(window.innerWidth);

  // Camera
  const camera = new Camera(worldContainer);

  // Width reserved on the right side for the cargo/loadout sidebar. Must
  // match `#cargo-panel` width in index.html.
  const SIDEBAR_WIDTH = 300;
  const sidebarWidth = (): number => (state.cargoPanelOpen ? SIDEBAR_WIDTH : 0);

  // Resizes the renderer and camera so the playable canvas occupies
  // only the area to the left of the sidebar. Called on init, on window
  // resize, and whenever the sidebar toggles (see the ticker loop).
  const applyViewport = (): void => {
    const w = Math.max(1, window.innerWidth - sidebarWidth());
    const h = window.innerHeight;
    app.renderer.resize(w, h);
    camera.resize(w, h);
    // Drives CSS rules that shift centered HUD elements (status bars,
    // ability bar, toasts, station prompt) left by half the sidebar width
    // so they stay visually centered over the shrunken canvas.
    document.body.classList.toggle("cargo-open", state.cargoPanelOpen);
  };
  applyViewport();

  // Background layers (order = z-order: nebula furthest back, then planets, then stars)
  const nebula = new Nebula(starfieldContainer);
  const planets = new Planets(starfieldContainer);
  const starfield = new Starfield(starfieldContainer);

  // Grid
  const cellGrid = new CellGrid();
  gridContainer.addChild(cellGrid.container);

  // Entity manager
  const entityManager = new EntityManager(entityContainer, uiEntityContainer);

  // Effects
  const thrusterRenderer = new ThrusterRenderer(particleContainer);
  const explosionRenderer = new ExplosionRenderer(particleContainer);
  const miningLaserRenderer = new MiningLaserRenderer(effectsContainer);
  const targetHighlight = new TargetHighlight(effectsContainer);
  const lockOnRing = new LockOnRing(effectsContainer);
  const beingLockedRing = new BeingLockedRing(effectsContainer);
  const moveIndicator = new MoveIndicator(effectsContainer);
  const abilityEffectRenderer = new AbilityEffectRenderer(effectsContainer);
  const tractorBeamRenderer = new TractorBeamRenderer(effectsContainer);

  // Ability bar (HTML overlay)
  createAbilityBar();

  // Lock target overlay (HTML)
  createLockOverlay(() => {
    state.lockTargetId = 0;
    state.lockProgress = 0;
  });

  // Loot popup overlay (HTML)
  createLootPopup();

  // Marketplace panel overlay (HTML)
  createMarketPanel();

  // Minimap (PixiJS overlay on app.stage, zIndex 100)
  const minimap = new Minimap(app.stage);

  // Cell map (full-screen overlay on app.stage)
  const cellMap = new CellMap(app.stage);

  // Handle window resize — applyViewport() subtracts the sidebar width
  // from the renderer/camera dimensions so the canvas never overlaps
  // the sidebar.
  window.addEventListener("resize", applyViewport);

  // Scroll-wheel zoom
  window.addEventListener("wheel", (e) => {
    if (state.cellMapOpen) {
      cellMap.handleWheel(e.deltaY);
      return;
    }
    const z = scrollZoom(e.deltaY);
    if (z != null) {
      worldContainer.scale.set(z, z);
    }
  }, { passive: true });

  // Input setup
  setupInput(
    state,
    (wx, wy) => camera.worldToScreen(wx, wy),
    (sx, sy) => camera.screenToWorld(sx, sy),
    (wx, wy) => moveIndicator.show(wx, wy),
  );

  // Input sending loop (20Hz)
  setInterval(() => {
    // Continuously re-project cursor to world coords while right mouse is held,
    // so the player keeps moving toward the cursor even when the mouse is still
    // (the camera moves with the player, so the world target shifts each tick).
    if (state.rightMouseDown && state.loggedIn && !state.isDead && !state.cellMapOpen) {
      const world = camera.screenToWorld(state.mouseX, state.mouseY);
      state.moveTarget = { x: world.x, y: world.y, active: true };
    }
    sendInput(state);
  }, TICK_INTERVAL);

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
      onOriginChanged: () => {
        // Grid no longer needs an origin — all drawing is world-absolute.
        // Still refresh topology/grid-size here because this callback fires
        // after cell change, when the mesh layout may have been updated.
        if (state.cellTopology) {
          cellGrid.setTopology(state.cellTopology);
        } else if (state.gridCellsX > 0) {
          cellGrid.setGridSize(state.gridCellsX, state.gridCellsY);
        }
      },
      onTopologyChanged: () => {
        if (state.cellTopology) {
          cellGrid.setTopology(state.cellTopology);
        }
      },
    });
  });

  // Tracks the last applied sidebar width so the ticker can detect
  // toggles of state.cargoPanelOpen and re-run applyViewport() without
  // needing to plumb a callback through every UI event.
  let lastSidebarWidth = sidebarWidth();

  // Main game loop via PixiJS ticker
  app.ticker.add(() => {
    if (!state.loggedIn) return;

    // Re-apply viewport if the sidebar toggled this frame.
    const sb = sidebarWidth();
    if (sb !== lastSidebarWidth) {
      lastSidebarWidth = sb;
      applyViewport();
    }

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

    // Background layers need absolute world coordinates for parallax offset
    // so the pattern is continuous across cell transfers. The cell-relative
    // camera.x/y is still needed for container positioning in world space.
    const cellOffX = state.originCellX * CELL_SIZE;
    const cellOffY = state.originCellY * CELL_SIZE;
    nebula.update(camera.x, camera.y, cellOffX, cellOffY, app.screen.width, app.screen.height, now);
    planets.update(camera.x, camera.y, cellOffX, cellOffY, app.screen.width, app.screen.height, now);
    starfield.update(camera.x, camera.y, cellOffX, cellOffY, app.screen.width, app.screen.height, now);

    // Update grid position
    gridContainer.visible = state.cellTopology !== null;
    if (state.cellTopology !== null) {
      cellGrid.update(camera.x, camera.y, app.screen.width, app.screen.height);
    }

    // Sync entity display objects
    entityManager.sync(state.entities, state.myEntityId, now);

    // Particles and effects
    const dt = app.ticker.deltaMS / 1000;
    thrusterRenderer.update(state, dt);
    explosionRenderer.update(state.explosions, now);
    miningLaserRenderer.update(state, now);
    targetHighlight.update(state, now);
    lockOnRing.update(state, now);
    beingLockedRing.update(state, now);
    if (state.rightMouseDown && state.loggedIn && !state.isDead) {
      const world = camera.screenToWorld(state.mouseX, state.mouseY);
      moveIndicator.pin(world.x, world.y);
    }
    moveIndicator.update(state, now);
    abilityEffectRenderer.update(state, now);
    tractorBeamRenderer.update(state, now);

    // HTML UI updates
    updateHUD(state);
    updateStatusBars(state);
    updateStationPrompt(state);
    updateDeathScreen(state);
    updateCargoPanel(state);
    updateBankPanel(state);
    updateMarketPanel(state);
    updateToasts(state);
    updateAbilityBar(state);
    updateLockOverlay(state);
    updateLootPopup(state);
    updateEscMenu(state);

    // Minimap
    minimap.update(state, app.screen.width, app.screen.height);

    // Cell map overlay
    cellMap.update(state, window.innerWidth, window.innerHeight);
  });
}

main().catch(console.error);

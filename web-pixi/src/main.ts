import { Application, Container } from "pixi.js";
import { TICK_INTERVAL } from "./constants";
import { interpolateEntities } from "./interpolation";
import { createInitialState } from "./state";
import { setupInput, sendInput, tickChannelAim } from "./input";
import { connect } from "./network";
import { authLogout } from "./auth";
import { setupLogin, showLogin, type LoginResult } from "./ui/login";
import { scrollZoom, zoom } from "./view";
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
import { AimIndicator } from "./effects/aim-indicator";
import { Minimap } from "./world/minimap";
import { createAbilityBar, updateAbilityBar } from "./ui/ability-bar";
import { createLockOverlay, updateLockOverlay } from "./ui/lock-overlay";
import { createLockHud, updateLockHud } from "./ui/lock-hud";
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

  // Parallax backgrounds (starfield/nebula/planets) live OUTSIDE
  // worldContainer so they're not subject to its zoom-scale transform.
  // Without this, scroll-wheel zoom shifts every star at sx>0 by
  // sx*Δzoom screen pixels — distant objects shouldn't translate when
  // the viewport zooms, only when the camera moves.
  // zIndex=-1 keeps it behind worldContainer regardless of insertion order.
  const starfieldContainer = new Container();
  starfieldContainer.zIndex = -1;
  app.stage.addChild(starfieldContainer);

  const worldContainer = new Container();
  app.stage.addChild(worldContainer);

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

  // Camera. Scale is seeded by the first applyViewport() call below;
  // every subsequent resize / sidebar toggle re-derives zoom from the
  // canvas width, so visible world width stays roughly constant.
  const camera = new Camera(worldContainer);

  // Width reserved on the right side for the cargo/loadout sidebar. Must
  // match `#cargo-panel` width in index.html.
  const SIDEBAR_WIDTH = 300;
  const sidebarWidth = (): number => (state.cargoPanelOpen ? SIDEBAR_WIDTH : 0);

  // Single source of truth for the playable canvas width in CSS pixels.
  // Both applyViewport() and the wheel-zoom handler read from here so
  // recomputeZoom() always sees the same value the renderer was sized
  // with. (Don't use app.renderer.width — that's physical pixels and
  // would diverge under HiDPI / non-default `resolution`.)
  const canvasWidth = (): number => Math.max(1, window.innerWidth - sidebarWidth());

  // Resizes the renderer and camera so the playable canvas occupies
  // only the area to the left of the sidebar. Called on init, on window
  // resize, and whenever the sidebar toggles (see the ticker loop).
  const applyViewport = (): void => {
    const w = canvasWidth();
    const h = window.innerHeight;
    app.renderer.resize(w, h);
    camera.resize(w, h);
    // Drives CSS rules that shift centered HUD elements (status bars,
    // ability bar, toasts, station prompt) left by half the sidebar width
    // so they stay visually centered over the shrunken canvas.
    document.body.classList.toggle("cargo-open", state.cargoPanelOpen);
  };
  applyViewport();

  // Anchor zoom for parallax — captured ONCE so subsequent scroll-zooms
  // don't rescale the starfield/nebula/planets. Stars at infinite
  // distance stay put when you zoom; only camera translation moves
  // them. starfieldContainer.scale carries world-unit constructor data
  // (positions, sizes) into screen pixels at this fixed ratio. The
  // ticker re-centers it on the camera each frame.
  const anchorZoom = zoom();
  starfieldContainer.scale.set(anchorZoom);

  // Background layers (order = z-order: nebula furthest back, then planets, then stars)
  const nebula = new Nebula(starfieldContainer);
  const planets = new Planets(starfieldContainer);
  const starfield = new Starfield(starfieldContainer, anchorZoom);

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
  const aimIndicator = new AimIndicator(effectsContainer);

  // Ability bar (HTML overlay)
  createAbilityBar();

  // Lock target overlay (HTML) — legacy single-target visual. The UNLOCK
  // button in the overlay header sends an UnlockTarget input for the
  // currently active slot; the server's LockSlotsMsg broadcast then
  // updates state.lockTargetId via the network handler.
  createLockOverlay(() => {
    if (state.connected && state.client && state.lockTargetId !== 0) {
      // Inline import to dodge the input.ts barrel-import cycle.
      void import("../sdk/index.js").then(({ UnlockTarget }) => {
        if (!state.client || state.lockTargetId === 0) return;
        state.inputSeq++;
        state.client.send(new UnlockTarget({
          sequence: state.inputSeq,
          netID: state.lockTargetId,
        }));
      });
    }
  });

  // Multi-lock HUD strip (HTML overlay, bottom-left). Slot icons accept
  // left-click=set active and right-click=unlock; both dispatch through
  // the same SDK message types the keyboard path uses.
  createLockHud({
    onActivate: (netID) => {
      if (!state.connected || !state.client) return;
      void import("../sdk/index.js").then(({ SetActiveTarget }) => {
        if (!state.client) return;
        state.inputSeq++;
        state.client.send(new SetActiveTarget({ sequence: state.inputSeq, netID }));
      });
    },
    onUnlock: (netID) => {
      if (!state.connected || !state.client) return;
      void import("../sdk/index.js").then(({ UnlockTarget }) => {
        if (!state.client) return;
        state.inputSeq++;
        state.client.send(new UnlockTarget({ sequence: state.inputSeq, netID }));
      });
    },
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

  // Scroll-wheel zoom. scrollZoom updates viewportUnits and recomputes
  // currentZoom against the live canvas width; camera.applyZoom() then
  // pushes the new scale onto the world container.
  window.addEventListener("wheel", (e) => {
    if (state.cellMapOpen) {
      cellMap.handleWheel(e.deltaY);
      return;
    }
    if (scrollZoom(e.deltaY, canvasWidth()) != null) {
      camera.applyZoom();
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

  // Login flow — authenticate FIRST (cookie set), THEN open WS so the
  // upgrade carries the cookie and the gateway can validate + bind the
  // session at upgrade time. Reversing the order leaves the existing
  // WS connection unauthenticated post-login (the cookie is set but
  // the upgrade already happened without it), and PlayerAssignment is
  // never dispatched until the user manually reloads the page.
  let loginResult: LoginResult | null = null;
  try {
    loginResult = await setupLogin();
    state.playerUsername = loginResult.username;
    state.loggedIn = true;
  } catch (e) {
    console.error("auth failed:", e);
    showLogin(e instanceof Error ? e.message : "Auth failed");
    return;
  }

  // Logout button — visible only while authenticated. Sends AUTH_LOGOUT,
  // clears the saved session token, and reloads the page so the overlay
  // comes back to the login form (with no stored token to auto-resume).
  const logoutBtn = document.getElementById(
    "logout-btn",
  ) as HTMLButtonElement | null;
  if (logoutBtn) logoutBtn.style.display = "block";
  logoutBtn?.addEventListener("click", async () => {
    if (!state.loggedIn) return;
    logoutBtn.disabled = true;
    try {
      await authLogout();
    } catch (e) {
      console.warn("logout failed:", e);
    }
    window.location.reload();
  });

  connect(state, {
    onWSOpen: () => {
      // Auth already completed before connect(); WS upgrade carries
      // the cookie and the gateway dispatches PlayerAssignment on
      // the upgrade-time validate. Nothing to do here.
    },
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

  // Tracks the last applied sidebar width so the ticker can detect
  // toggles of state.cargoPanelOpen and re-run applyViewport() without
  // needing to plumb a callback through every UI event.
  let lastSidebarWidth = sidebarWidth();

  // ChannelAim streaming throttle. Per-frame in the render loop would
  // flood the server at 60+ Hz; 50ms gives ~20 Hz aim updates, matching
  // the server tick rate. tickChannelAim is a no-op when no channel is
  // active, so this cost is essentially free off-channel.
  let lastChannelAimTime = 0;

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

    // ChannelAim stream (50ms throttle ≈ 20 Hz). No-op when no
    // SkillshotChannel ability is active.
    if (now - lastChannelAimTime > 50) {
      tickChannelAim(state);
      lastChannelAimTime = now;
    }

    // Interpolation
    interpolateEntities(state.entities, state.clockSync, now);

    // Camera follows player
    const myEntity = state.entities.get(state.myEntityId);
    if (myEntity) {
      camera.update(myEntity.renderX, myEntity.renderY, state.screenShake);
    }

    // Clear expired screen shake
    if (state.screenShake && now - state.screenShake.startTime >= state.screenShake.duration) {
      state.screenShake = null;
    }

    // Re-center the parallax layer on the camera each frame. Inside
    // starfieldContainer, modules position children in world units;
    // this transform converts those into screen pixels (via scale =
    // anchorZoom) and pivots so a child at world (camera.x, camera.y)
    // lands at the screen center. Replicates worldContainer's
    // pivot+position math without inheriting its scroll-coupled scale.
    starfieldContainer.position.set(
      -camera.x * anchorZoom + app.screen.width / 2,
      -camera.y * anchorZoom + app.screen.height / 2,
    );

    // Background parallax is driven by the camera's absolute world coordinates
    // (camera.x/y follow renderX/worldY, which the topology-transparent
    // protocol delivers in world space).
    nebula.update(camera.x, camera.y, app.screen.width, app.screen.height, now);
    planets.update(camera.x, camera.y, app.screen.width, app.screen.height, now);
    starfield.update(camera.x, camera.y, app.screen.width, app.screen.height, now);

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
    aimIndicator.update(state);

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
    updateLockHud(state);
    updateLootPopup(state);
    updateEscMenu(state);

    // Minimap
    minimap.update(state, app.screen.width, app.screen.height);

    // Cell map overlay
    cellMap.update(state, window.innerWidth, window.innerHeight);
  });
}

main().catch(console.error);

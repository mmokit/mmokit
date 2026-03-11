import { EntityType } from "@gen/game_pb.js";
import { ITEM_COLORS_CSS, DEFAULT_ITEM_COLOR, TOAST_DURATION } from "../constants";
import type { GameState } from "../state";

const hudEl = () => document.getElementById("hud")!;
const statusBarsEl = () => document.getElementById("status-bars")!;
const stationPromptEl = () => document.getElementById("station-prompt")!;
const deathScreenEl = () => document.getElementById("death-screen")!;
const cargoPanelEl = () => document.getElementById("cargo-panel")!;
const cargoRowsEl = () => document.getElementById("cargo-rows")!;
const cargoFooterEl = () => document.getElementById("cargo-footer")!;
const toastsEl = () => document.getElementById("toasts")!;

let cargoJettisonSetup = false;
let cargoState: GameState | null = null;

function setupCargoJettison(): void {
  if (cargoJettisonSetup) return;
  cargoJettisonSetup = true;
  const rows = cargoRowsEl();
  rows.addEventListener("mousedown", (e) => {
    if (!e.altKey || !cargoState) return;
    const row = (e.target as HTMLElement).closest(".cargo-row") as HTMLElement | null;
    if (!row || !row.dataset.itemId) return;
    e.stopPropagation();
    cargoState.jettisonRequest = Number(row.dataset.itemId);
  });
}

export function updateHUD(state: GameState): void {
  const myEntity = state.entities.get(state.myEntityId);

  const bankFlux = state.bankItems.get(1) || 0;
  let hudText = `${state.playerUsername} | FPS: ${state.fps} | Tick: ${state.tickCount} | Entities: ${state.entities.size}`;
  if (myEntity) {
    hudText += ` | FLUX: ${Math.floor(bankFlux)}`;
    const spd = Math.sqrt(myEntity.curr.vx * myEntity.curr.vx + myEntity.curr.vy * myEntity.curr.vy);
    hudText += ` | Speed: ${Math.floor(spd)}`;
    hudText += `\nPos: (${myEntity.renderX.toFixed(0)}, ${myEntity.renderY.toFixed(0)})`;
  }
  hudEl().textContent = hudText;
}

export function updateStatusBars(state: GameState): void {
  const myEntity = state.entities.get(state.myEntityId);
  const el = statusBarsEl();

  if (!myEntity) {
    el.style.display = "none";
    return;
  }
  el.style.display = "block";

  const sh = myEntity.curr.shield;
  const hp = myEntity.curr.health;

  // Shield
  const shieldFill = document.querySelector("#shield-bar .bar-fill") as HTMLElement;
  const shieldLabel = document.querySelector("#shield-bar .bar-label") as HTMLElement;
  shieldFill.style.width = `${sh * 100}%`;
  shieldLabel.textContent = `SHIELD ${(sh * 100).toFixed(0)}%`;

  // Health
  const hpFill = document.querySelector("#health-bar .bar-fill") as HTMLElement;
  const hpLabel = document.querySelector("#health-bar .bar-label") as HTMLElement;
  hpFill.style.width = `${hp * 100}%`;
  hpLabel.textContent = `HP ${(hp * 100).toFixed(0)}%`;
  if (hp <= 0.3) {
    hpFill.style.background = "rgba(255,30,30,1)";
  } else {
    hpFill.style.background = "rgba(255,60,60,0.8)";
  }

  // Cargo - use mass-based values from server
  const cargoMass = myEntity.curr.cargoMass || 0;
  const maxCargoMass = myEntity.curr.maxCargoMass || 100;
  const cargoFrac = maxCargoMass > 0 ? cargoMass / maxCargoMass : 0;
  const cargoFill = document.querySelector("#cargo-bar .bar-fill") as HTMLElement;
  const cargoLabel = document.querySelector("#cargo-bar .bar-label") as HTMLElement;
  cargoFill.style.width = `${cargoFrac * 100}%`;
  cargoLabel.textContent = `CARGO ${Math.floor(cargoMass)} / ${Math.floor(maxCargoMass)}`;
  if (cargoFrac >= 1) {
    cargoFill.style.background = "rgba(255,60,60,0.9)";
  } else {
    cargoFill.style.background = "rgba(200,180,60,0.8)";
  }
}

export function updateStationPrompt(state: GameState): void {
  const myEntity = state.entities.get(state.myEntityId);
  const el = stationPromptEl();

  if (!myEntity || state.isDead) {
    el.style.display = "none";
    return;
  }

  let nearStation = false;
  for (const [, ent] of state.entities) {
    if (ent.curr.entityType !== EntityType.STATION) continue;
    const dx = myEntity.renderX - ent.renderX;
    const dy = myEntity.renderY - ent.renderY;
    if (Math.sqrt(dx * dx + dy * dy) < 250) {
      nearStation = true;
      break;
    }
  }

  if (nearStation) {
    el.style.display = "block";
    el.textContent = "Press X to open station";
  } else {
    el.style.display = "none";
    // Close bank panel if we moved away from station
    if (state.bankPanelOpen) {
      state.bankPanelOpen = false;
    }
  }
}

export function updateDeathScreen(state: GameState): void {
  deathScreenEl().style.display = state.isDead ? "flex" : "none";
}

export function updateCargoPanel(state: GameState): void {
  const myEntity = state.entities.get(state.myEntityId);
  const el = cargoPanelEl();

  cargoState = state;
  setupCargoJettison();

  if (!state.cargoPanelOpen || !myEntity) {
    el.style.display = "none";
    return;
  }
  el.style.display = "block";

  const cargoItems = myEntity.curr.cargoItems;
  const rows = cargoRowsEl();
  const cargoMass = myEntity.curr.cargoMass || 0;
  const maxCargoMass = myEntity.curr.maxCargoMass || 100;

  // Rebuild rows
  rows.innerHTML = "";

  if (!cargoItems || cargoItems.length === 0) {
    const emptyRow = document.createElement("div");
    emptyRow.className = "cargo-row";
    emptyRow.style.justifyContent = "center";
    const label = document.createElement("span");
    label.className = "cargo-label";
    label.style.color = "#666";
    label.textContent = "Empty";
    emptyRow.appendChild(label);
    rows.appendChild(emptyRow);
  } else {
    // Sort by item ID for consistent display
    const sorted = [...cargoItems].sort((a, b) => a.itemId - b.itemId);
    for (const item of sorted) {
      const def = state.itemDefs.get(item.itemId);
      const name = def ? def.name : `Item #${item.itemId}`;
      const color = ITEM_COLORS_CSS[item.itemId] || DEFAULT_ITEM_COLOR;
      const itemMass = def ? item.quantity * def.massPerUnit : item.quantity;
      const frac = maxCargoMass > 0 ? itemMass / maxCargoMass : 0;

      const row = document.createElement("div");
      row.className = "cargo-row";

      const fill = document.createElement("div");
      fill.className = "cargo-fill";
      fill.style.width = `${frac * 100}%`;
      fill.style.background = color;
      row.appendChild(fill);

      const label = document.createElement("span");
      label.className = "cargo-label";
      label.style.color = color;
      label.textContent = name;
      row.appendChild(label);

      const amt = document.createElement("span");
      amt.className = "cargo-amount";
      amt.textContent = Math.floor(item.quantity).toString();
      row.appendChild(amt);

      // Alt+click to jettison
      row.dataset.itemId = item.itemId.toString();
      row.style.cursor = "pointer";

      rows.appendChild(row);
    }
  }

  const bankFlux = state.bankItems.get(1) || 0;
  cargoFooterEl().textContent = `${Math.floor(cargoMass)} / ${Math.floor(maxCargoMass)} mass  |  FLUX: ${Math.floor(bankFlux)}  |  Alt+Click: Jettison`;
  if (cargoMass >= maxCargoMass) {
    cargoFooterEl().style.color = "#f55";
  } else {
    cargoFooterEl().style.color = "#888";
  }
}

export function updateToasts(state: GameState): void {
  const now = performance.now();
  state.toasts = state.toasts.filter((t) => now - t.time < TOAST_DURATION);

  const el = toastsEl();
  el.innerHTML = "";
  for (const t of state.toasts) {
    const age = now - t.time;
    let alpha = 1;
    if (age > 2000) alpha = 1 - (age - 2000) / 1000;

    const div = document.createElement("div");
    div.className = "toast";
    div.style.opacity = alpha.toString();
    div.textContent = t.text;
    el.appendChild(div);
  }
}

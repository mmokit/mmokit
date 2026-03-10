import { EntityType } from "@gen/game_pb.js";
import { MAX_CARGO, RESOURCE_COLORS_CSS, RESOURCE_NAMES, TOAST_DURATION } from "../constants";
import type { GameState } from "../state";

const hudEl = () => document.getElementById("hud")!;
const statusBarsEl = () => document.getElementById("status-bars")!;
const stationPromptEl = () => document.getElementById("station-prompt")!;
const deathScreenEl = () => document.getElementById("death-screen")!;
const cargoPanelEl = () => document.getElementById("cargo-panel")!;
const cargoRowsEl = () => document.getElementById("cargo-rows")!;
const cargoFooterEl = () => document.getElementById("cargo-footer")!;
const toastsEl = () => document.getElementById("toasts")!;

export function updateHUD(state: GameState): void {
  const myEntity = state.entities.get(state.myEntityId);

  let hudText = `${state.playerUsername} | FPS: ${state.fps} | Tick: ${state.tickCount} | Entities: ${state.entities.size}`;
  if (myEntity) {
    hudText += ` | FLUX: ${Math.floor(state.playerFlux)}`;
    hudText += `\nPos: (${myEntity.renderX.toFixed(0)}, ${myEntity.renderY.toFixed(0)})`;
  }
  hudEl().textContent = hudText;

  // Track flux from entity state
  if (myEntity && myEntity.curr.flux !== undefined) {
    state.playerFlux = myEntity.curr.flux;
  }
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

  // Cargo
  const res = myEntity.curr.resources;
  const totalCargo = res && res.length >= 4 ? res.reduce((a: number, b: number) => a + b, 0) : 0;
  const cargoFrac = totalCargo / MAX_CARGO;
  const cargoFill = document.querySelector("#cargo-bar .bar-fill") as HTMLElement;
  const cargoLabel = document.querySelector("#cargo-bar .bar-label") as HTMLElement;
  cargoFill.style.width = `${cargoFrac * 100}%`;
  cargoLabel.textContent = `CARGO ${Math.floor(totalCargo)} / ${MAX_CARGO}`;
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

  el.style.display = nearStation ? "block" : "none";
}

export function updateDeathScreen(state: GameState): void {
  deathScreenEl().style.display = state.isDead ? "flex" : "none";
}

export function updateCargoPanel(state: GameState): void {
  const myEntity = state.entities.get(state.myEntityId);
  const el = cargoPanelEl();

  if (!state.cargoPanelOpen || !myEntity) {
    el.style.display = "none";
    return;
  }
  el.style.display = "block";

  const res = myEntity.curr.resources;
  if (!res || res.length < 4) return;

  const rows = cargoRowsEl();
  const totalCargo = res.reduce((a: number, b: number) => a + b, 0);

  // Rebuild rows
  rows.innerHTML = "";
  for (let i = 0; i < 4; i++) {
    const amount = res[i];
    const frac = amount / MAX_CARGO;

    const row = document.createElement("div");
    row.className = "cargo-row";

    const fill = document.createElement("div");
    fill.className = "cargo-fill";
    fill.style.width = `${frac * 100}%`;
    fill.style.background = RESOURCE_COLORS_CSS[i];
    row.appendChild(fill);

    const label = document.createElement("span");
    label.className = "cargo-label";
    label.style.color = RESOURCE_COLORS_CSS[i];
    label.textContent = `[${i + 1}] ${RESOURCE_NAMES[i]}`;
    row.appendChild(label);

    const amt = document.createElement("span");
    amt.className = "cargo-amount";
    amt.textContent = Math.floor(amount).toString();
    row.appendChild(amt);

    rows.appendChild(row);
  }

  cargoFooterEl().textContent = `${Math.floor(totalCargo)} / ${MAX_CARGO}  |  FLUX: ${Math.floor(state.playerFlux)}  |  1-4: Jettison`;
  if (totalCargo >= MAX_CARGO) {
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

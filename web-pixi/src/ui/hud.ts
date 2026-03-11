import { EntityType } from "@gen/game_pb.js";
import { ITEM_COLORS_CSS, DEFAULT_ITEM_COLOR, TOAST_DURATION } from "../constants";
import { encodeEquipRequest } from "../protocol";
import type { GameState } from "../state";
import { ITEM_ABILITIES, type AbilityInfo } from "./ability-bar";

// Equipment slot constants (matches server EquipSlot enum for physical slots)
const EQUIP_SLOT_WEAPON1 = 1;
const EQUIP_SLOT_WEAPON2 = 2;
const EQUIP_SLOT_SHIELD = 3;
const EQUIP_SLOT_THRUSTER = 4;
// Item category: fits either weapon slot
const EQUIP_SLOT_WEAPON = 5;

const hudEl = () => document.getElementById("hud")!;
const statusBarsEl = () => document.getElementById("status-bars")!;
const stationPromptEl = () => document.getElementById("station-prompt")!;
const deathScreenEl = () => document.getElementById("death-screen")!;
const cargoPanelEl = () => document.getElementById("cargo-panel")!;
const equipSlotsEl = () => document.getElementById("equip-slots")!;
const cargoRowsEl = () => document.getElementById("cargo-rows")!;
const cargoFooterEl = () => document.getElementById("cargo-footer")!;
const toastsEl = () => document.getElementById("toasts")!;

let cargoEventsSetup = false;
let cargoState: GameState | null = null;
let tooltipEl: HTMLElement | null = null;
let hoveredItemId = 0;

const CATEGORY_NAMES = ["Currency", "Resource", "Equipment", "Consumable", "Module"];
const CATEGORY_EQUIPMENT = 2;

function ensureTooltip(): HTMLElement {
  if (!tooltipEl) {
    tooltipEl = document.createElement("div");
    tooltipEl.className = "cargo-tooltip";
    document.body.appendChild(tooltipEl);
  }
  return tooltipEl;
}

function appendAbilitySection(parent: HTMLElement, info: AbilityInfo, label: string, color: string): void {
  const section = document.createElement("div");
  section.className = "tt-ability-section";

  const nameEl = document.createElement("div");
  nameEl.className = "tt-ability-name";
  nameEl.style.color = info.color || color;
  nameEl.textContent = `${label}: ${info.title}`;
  section.appendChild(nameEl);

  const descEl = document.createElement("div");
  descEl.className = "tt-ability-desc";
  descEl.textContent = info.desc;
  section.appendChild(descEl);

  for (const stat of info.stats) {
    const statEl = document.createElement("div");
    statEl.className = "tt-stat";
    statEl.textContent = stat;
    section.appendChild(statEl);
  }

  parent.appendChild(section);
}

function showCargoTooltip(itemId: number, state: GameState, anchorEl: HTMLElement): void {
  const tt = ensureTooltip();
  const def = state.itemDefs.get(itemId);
  if (!def) { tt.style.display = "none"; return; }

  hoveredItemId = itemId;
  tt.innerHTML = "";

  const color = ITEM_COLORS_CSS[itemId] || DEFAULT_ITEM_COLOR;

  // Item name
  const nameEl = document.createElement("div");
  nameEl.className = "tt-name";
  nameEl.style.color = color;
  nameEl.textContent = def.name;
  tt.appendChild(nameEl);

  // Category label
  const catEl = document.createElement("div");
  catEl.className = "tt-category";
  catEl.textContent = CATEGORY_NAMES[def.category] || "Unknown";
  tt.appendChild(catEl);

  // Basic stats
  const lines: string[] = [];
  if (def.massPerUnit > 0) lines.push(`Mass: ${def.massPerUnit}/unit`);
  if (def.sellPrice > 0) lines.push(`Sell: ${Math.floor(def.sellPrice)} FLUX`);
  if (def.buyPrice > 0) lines.push(`Buy: ${Math.floor(def.buyPrice)} FLUX`);
  if (lines.length > 0) {
    const statsEl = document.createElement("div");
    statsEl.className = "tt-stat";
    statsEl.innerHTML = lines.join("<br>");
    tt.appendChild(statsEl);
  }

  // Equipment abilities
  if (def.category === CATEGORY_EQUIPMENT) {
    const abilities = ITEM_ABILITIES[itemId];
    if (abilities) {
      appendAbilitySection(tt, abilities.primary, "Primary", color);
      if (abilities.secondary) {
        appendAbilitySection(tt, abilities.secondary, "Secondary", color);
      }
    }
  }

  // Position to the left of the anchor
  const rect = anchorEl.getBoundingClientRect();
  tt.style.display = "block";
  tt.style.top = `${rect.top}px`;
  tt.style.left = `${rect.left - 210}px`;

  // Clamp to viewport
  const ttRect = tt.getBoundingClientRect();
  if (ttRect.top + ttRect.height > window.innerHeight) {
    tt.style.top = `${window.innerHeight - ttRect.height - 10}px`;
  }
  if (ttRect.left < 0) {
    tt.style.left = "10px";
  }
}

function hideCargoTooltip(): void {
  if (tooltipEl) tooltipEl.style.display = "none";
  hoveredItemId = 0;
}

function setupCargoEvents(): void {
  if (cargoEventsSetup) return;
  cargoEventsSetup = true;

  // Use mousedown instead of click — the DOM is rebuilt every frame,
  // so elements are destroyed before mouseup/click can fire.

  const rows = cargoRowsEl();
  rows.addEventListener("mousedown", (e) => {
    if (!cargoState) return;
    const row = (e.target as HTMLElement).closest(".cargo-row") as HTMLElement | null;
    if (!row || !row.dataset.itemId) return;
    e.stopPropagation();

    const itemId = Number(row.dataset.itemId);

    if (e.altKey) {
      // Alt+Click: jettison (non-equipment items only)
      if (!row.dataset.equipSlot) {
        cargoState.jettisonRequest = itemId;
      }
      return;
    }

    // Click: equip equipment items
    if (row.dataset.equipSlot && cargoState.ws) {
      let slot = Number(row.dataset.equipSlot);
      // Weapon category (5) can go in either weapon slot — pick first empty, or weapon2
      if (slot === EQUIP_SLOT_WEAPON) {
        if (!cargoState.equipment.weapon1) {
          slot = EQUIP_SLOT_WEAPON1;
        } else {
          slot = EQUIP_SLOT_WEAPON2;
        }
      }
      if (itemId && slot) {
        cargoState.ws.sendReliable(encodeEquipRequest(itemId, slot));
      }
    }
  });

  // Click on equipment slots to unequip
  const equipSlots = equipSlotsEl();
  equipSlots.addEventListener("mousedown", (e) => {
    if (!cargoState || !cargoState.ws) return;
    const slotEl = (e.target as HTMLElement).closest(".equip-slot[data-slot]") as HTMLElement | null;
    if (!slotEl) return;
    e.stopPropagation();
    const slot = Number(slotEl.dataset.slot);
    if (slot) {
      cargoState.ws.sendReliable(encodeEquipRequest(0, slot));
    }
  });

  // Tooltip: mouseover on cargo rows and equip slots
  const panel = cargoPanelEl();
  panel.addEventListener("mouseover", (e) => {
    if (!cargoState) return;
    const row = (e.target as HTMLElement).closest(".cargo-row[data-item-id], .equip-slot[data-item-id]") as HTMLElement | null;
    if (!row) return;

    const itemId = Number(row.dataset.itemId);
    if (itemId) showCargoTooltip(itemId, cargoState, row);
  });

  // Hide tooltip when mouse leaves the panel entirely
  panel.addEventListener("mouseleave", () => {
    hideCargoTooltip();
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

  const shFrac = myEntity.curr.maxShield > 0 ? myEntity.curr.shield / myEntity.curr.maxShield : 0;
  const hpFrac = myEntity.curr.maxHealth > 0 ? myEntity.curr.health / myEntity.curr.maxHealth : 0;

  // Shield
  const shieldFill = document.querySelector("#shield-bar .bar-fill") as HTMLElement;
  const shieldLabel = document.querySelector("#shield-bar .bar-label") as HTMLElement;
  shieldFill.style.width = `${shFrac * 100}%`;
  shieldLabel.textContent = `SHIELD ${Math.floor(myEntity.curr.shield)} / ${Math.floor(myEntity.curr.maxShield)}`;

  // Health
  const hpFill = document.querySelector("#health-bar .bar-fill") as HTMLElement;
  const hpLabel = document.querySelector("#health-bar .bar-label") as HTMLElement;
  hpFill.style.width = `${hpFrac * 100}%`;
  hpLabel.textContent = `HP ${Math.floor(myEntity.curr.health)} / ${Math.floor(myEntity.curr.maxHealth)}`;
  if (hpFrac <= 0.3) {
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
  setupCargoEvents();

  // Shift minimap and status when panel is open/closed
  const minimap = document.getElementById("minimap");
  const statusEl = document.getElementById("status");
  if (!state.cargoPanelOpen || !myEntity) {
    el.style.display = "none";
    hideCargoTooltip();
    if (minimap) minimap.style.right = "10px";
    if (statusEl) statusEl.style.right = "10px";
    return;
  }
  el.style.display = "block";
  if (minimap) minimap.style.right = "320px";
  if (statusEl) statusEl.style.right = "320px";

  // --- Equipment slots ---
  const equipEl = equipSlotsEl();
  equipEl.innerHTML = "";

  const slots: Array<{ keys: string; label: string; slot: number; itemId: number; color: string }> = [
    { keys: "Q W", label: "Weapon 1", slot: EQUIP_SLOT_WEAPON1, itemId: state.equipment.weapon1, color: "#4af" },
    { keys: "E R", label: "Weapon 2", slot: EQUIP_SLOT_WEAPON2, itemId: state.equipment.weapon2, color: "#a4f" },
    { keys: "D",   label: "Shield",   slot: EQUIP_SLOT_SHIELD,  itemId: state.equipment.shield,  color: "#4f4" },
    { keys: "F",   label: "Thruster", slot: EQUIP_SLOT_THRUSTER, itemId: state.equipment.thruster, color: "#ff4" },
  ];

  for (const s of slots) {
    const box = document.createElement("div");
    box.className = s.itemId ? "equip-slot" : "equip-slot equip-empty";
    box.style.borderColor = s.itemId ? s.color : "rgba(255,255,255,0.1)";
    if (s.itemId) {
      box.dataset.slot = s.slot.toString();
      box.dataset.itemId = s.itemId.toString();
    }

    const keysEl = document.createElement("div");
    keysEl.className = "equip-keys";
    keysEl.style.color = s.color;
    keysEl.textContent = s.keys;
    box.appendChild(keysEl);

    const nameEl = document.createElement("div");
    nameEl.className = "equip-name";
    if (s.itemId) {
      const def = state.itemDefs.get(s.itemId);
      nameEl.textContent = def ? def.name : `Item #${s.itemId}`;
      nameEl.style.color = "#ddd";
    } else {
      nameEl.textContent = "—";
      nameEl.style.color = "#444";
    }
    box.appendChild(nameEl);

    const labelEl = document.createElement("div");
    labelEl.className = "equip-label";
    labelEl.textContent = s.label;
    box.appendChild(labelEl);

    equipEl.appendChild(box);
  }

  // --- Cargo rows ---
  const cargoItems = myEntity.curr.cargoItems;
  const rows2 = cargoRowsEl();
  const cargoMass = myEntity.curr.cargoMass || 0;
  const maxCargoMass = myEntity.curr.maxCargoMass || 100;

  rows2.innerHTML = "";

  if (!cargoItems || cargoItems.length === 0) {
    const emptyRow = document.createElement("div");
    emptyRow.className = "cargo-row";
    emptyRow.style.justifyContent = "center";
    const label = document.createElement("span");
    label.className = "cargo-label";
    label.style.color = "#666";
    label.textContent = "Empty";
    emptyRow.appendChild(label);
    rows2.appendChild(emptyRow);
  } else {
    const sorted = [...cargoItems].sort((a, b) => a.itemId - b.itemId);
    for (const item of sorted) {
      const def = state.itemDefs.get(item.itemId);
      const name = def ? def.name : `Item #${item.itemId}`;
      const color = ITEM_COLORS_CSS[item.itemId] || DEFAULT_ITEM_COLOR;
      const itemMass = def ? item.quantity * def.massPerUnit : item.quantity;
      const frac = maxCargoMass > 0 ? itemMass / maxCargoMass : 0;

      const row = document.createElement("div");
      row.className = def && def.category === CATEGORY_EQUIPMENT ? "cargo-row cargo-equip" : "cargo-row";

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

      row.dataset.itemId = item.itemId.toString();
      row.style.cursor = "pointer";

      // If this is an equippable item, add equip data attr
      if (def && def.equipSlot > 0) {
        row.dataset.equipSlot = def.equipSlot.toString();
      }

      rows2.appendChild(row);
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

import { EntityType } from "@gen/game_pb.js";
import { ITEM_COLORS_CSS, DEFAULT_ITEM_COLOR, TOAST_DURATION } from "../constants";
import { encodeEquipRequest } from "../protocol";
import { SETTLEMENT_CURRENCY_ID, type GameState } from "../state";
import { getCombat } from "../entity-accessors";
import { ITEM_ABILITIES, type AbilityInfo } from "./ability-bar";
import { needsRebuild } from "./memo";

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
const moveModeEl = () => document.getElementById("move-mode")!;
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

// Drag-and-drop state
let dragGhostEl: HTMLElement | null = null;
let dragSource: { type: "cargo" | "equip"; itemId: number; equipSlot: number } | null = null;
let dragStarted = false;
let dragStartX = 0;
let dragStartY = 0;
const DRAG_THRESHOLD = 5; // pixels before drag activates

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
  const cn = state.itemDefs.get(SETTLEMENT_CURRENCY_ID)?.name ?? "Currency";
  if (def.sellPrice > 0) lines.push(`Sell: ${Math.floor(def.sellPrice)} ${cn}`);
  if (def.buyPrice > 0) lines.push(`Buy: ${Math.floor(def.buyPrice)} ${cn}`);
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
      if (abilities.passive) {
        const passiveSection = document.createElement("div");
        passiveSection.className = "tt-ability-section";
        const passiveLabel = document.createElement("div");
        passiveLabel.className = "tt-ability-name";
        passiveLabel.style.color = color;
        passiveLabel.textContent = "Passive";
        passiveSection.appendChild(passiveLabel);
        for (const stat of abilities.passive) {
          const statEl = document.createElement("div");
          statEl.className = "tt-stat";
          statEl.textContent = stat;
          passiveSection.appendChild(statEl);
        }
        tt.appendChild(passiveSection);
      }
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

function resolveWeaponSlot(state: GameState, preferredSlot?: number): number {
  // For weapon-category items, pick the best slot
  if (!state.equipment.weapon1) return EQUIP_SLOT_WEAPON1;
  if (!state.equipment.weapon2) return EQUIP_SLOT_WEAPON2;
  // Both full — default to weapon2 unless a specific slot was given
  return preferredSlot || EQUIP_SLOT_WEAPON2;
}

function createDragGhost(text: string, color: string): HTMLElement {
  if (dragGhostEl) dragGhostEl.remove();
  const el = document.createElement("div");
  el.className = "drag-ghost";
  el.textContent = text;
  el.style.color = color;
  document.body.appendChild(el);
  dragGhostEl = el;
  return el;
}

function isValidSlotForItem(itemEquipSlot: number, targetSlot: number): boolean {
  if (itemEquipSlot === EQUIP_SLOT_WEAPON && (targetSlot === EQUIP_SLOT_WEAPON1 || targetSlot === EQUIP_SLOT_WEAPON2)) {
    return true;
  }
  return itemEquipSlot === targetSlot;
}

function cleanupDrag(): void {
  if (dragGhostEl) { dragGhostEl.remove(); dragGhostEl = null; }
  dragSource = null;
  dragStarted = false;
  document.querySelectorAll(".drop-target").forEach(el => el.classList.remove("drop-target"));
}

function handleDrop(e: MouseEvent): void {
  if (!dragSource || !cargoState?.ws) { cleanupDrag(); return; }

  const target = document.elementFromPoint(e.clientX, e.clientY) as HTMLElement | null;

  if (dragSource.type === "cargo") {
    // Dragging from cargo → look for equip slot drop target
    const slotEl = target?.closest(".equip-slot[data-slot]") as HTMLElement | null;
    if (slotEl) {
      const targetSlot = Number(slotEl.dataset.slot);
      // Validate: item's equipSlot must match target slot (or be weapon category for weapon slots)
      const itemEquipSlot = dragSource.equipSlot;
      if (isValidSlotForItem(itemEquipSlot, targetSlot) && targetSlot) {
        const p = encodeEquipRequest(dragSource.itemId, targetSlot); cargoState.ws.sendEvent(p.code, p.data);
      }
    }
  } else if (dragSource.type === "equip") {
    // Dragging from equip slot → drop on cargo area to unequip
    const cargoArea = target?.closest("#cargo-rows, #cargo-panel") as HTMLElement | null;
    const equipArea = target?.closest(".equip-slot") as HTMLElement | null;
    // Unequip if dropped on cargo area (not on another equip slot)
    if (cargoArea && !equipArea) {
      { const p = encodeEquipRequest(0, dragSource.equipSlot); cargoState.ws.sendEvent(p.code, p.data); }
    }
  }

  cleanupDrag();
}

function setupCargoEvents(): void {
  if (cargoEventsSetup) return;
  cargoEventsSetup = true;

  const rows = cargoRowsEl();
  const equipSlots = equipSlotsEl();
  const panel = cargoPanelEl();

  // Prevent context menu on cargo panel
  panel.addEventListener("contextmenu", (e) => e.preventDefault());

  // --- Cargo row: mousedown ---
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

    if (e.button === 2) {
      // Right-click: quick equip
      if (row.dataset.equipSlot && cargoState.ws) {
        let slot = Number(row.dataset.equipSlot);
        if (slot === EQUIP_SLOT_WEAPON) {
          slot = resolveWeaponSlot(cargoState);
        }
        if (itemId && slot) {
          { const p = encodeEquipRequest(itemId, slot); cargoState.ws.sendEvent(p.code, p.data); }
        }
      }
      return;
    }

    if (e.button === 0 && row.dataset.equipSlot) {
      // Left-click: begin drag for equippable items
      e.preventDefault(); // prevent text selection
      dragSource = { type: "cargo", itemId, equipSlot: Number(row.dataset.equipSlot) };
      dragStarted = false;
      dragStartX = e.clientX;
      dragStartY = e.clientY;

      const def = cargoState.itemDefs.get(itemId);
      const name = def ? def.name : `Item #${itemId}`;
      const color = ITEM_COLORS_CSS[itemId] || DEFAULT_ITEM_COLOR;
      createDragGhost(name, color);
      dragGhostEl!.style.display = "none"; // hidden until threshold met
    }
  });

  // --- Equipment slot: mousedown ---
  equipSlots.addEventListener("mousedown", (e) => {
    if (!cargoState || !cargoState.ws) return;
    const slotEl = (e.target as HTMLElement).closest(".equip-slot[data-slot]") as HTMLElement | null;
    if (!slotEl) return;
    e.stopPropagation();

    const slot = Number(slotEl.dataset.slot);
    if (!slot) return;

    if (e.button === 2) {
      // Right-click: quick unequip
      const itemId = Number(slotEl.dataset.itemId);
      if (itemId) {
        { const p = encodeEquipRequest(0, slot); cargoState.ws.sendEvent(p.code, p.data); }
      }
      return;
    }

    if (e.button === 0) {
      const itemId = Number(slotEl.dataset.itemId);
      if (!itemId) return; // can't drag from empty slot
      e.preventDefault(); // prevent text selection
      // Left-click: begin drag to unequip
      dragSource = { type: "equip", itemId, equipSlot: slot };
      dragStarted = false;
      dragStartX = e.clientX;
      dragStartY = e.clientY;

      const def = cargoState.itemDefs.get(itemId);
      const name = def ? def.name : `Item #${itemId}`;
      const color = ITEM_COLORS_CSS[itemId] || DEFAULT_ITEM_COLOR;
      createDragGhost(name, color);
      dragGhostEl!.style.display = "none";
    }
  });

  // --- Document-level drag tracking ---
  document.addEventListener("mousemove", (e) => {
    if (!dragSource || !dragGhostEl) return;

    if (!dragStarted) {
      const dx = e.clientX - dragStartX;
      const dy = e.clientY - dragStartY;
      if (Math.sqrt(dx * dx + dy * dy) < DRAG_THRESHOLD) return;
      dragStarted = true;
      dragGhostEl.style.display = "block";
      hideCargoTooltip();
    }

    dragGhostEl.style.left = `${e.clientX + 12}px`;
    dragGhostEl.style.top = `${e.clientY - 10}px`;

    // Hover highlight on cargo rows area (for equip→cargo drags)
    document.querySelectorAll("#cargo-rows.drop-target").forEach(el => el.classList.remove("drop-target"));
    if (dragSource.type === "equip") {
      const target = document.elementFromPoint(e.clientX, e.clientY) as HTMLElement | null;
      const cargoArea = target?.closest("#cargo-rows") as HTMLElement | null;
      if (cargoArea) cargoArea.classList.add("drop-target");
    }
  });

  document.addEventListener("mouseup", (e) => {
    if (!dragSource) return;
    if (dragStarted) {
      handleDrop(e);
    } else {
      cleanupDrag();
    }
  });

  // Tooltip + hover tracking on cargo rows and equip slots
  panel.addEventListener("mouseover", (e) => {
    if (!cargoState || dragStarted) return;
    const row = (e.target as HTMLElement).closest(".cargo-row[data-item-id], .equip-slot[data-item-id]") as HTMLElement | null;
    if (!row) return;

    const itemId = Number(row.dataset.itemId);
    if (itemId) {
      hoveredItemId = itemId;
      // Apply .hovered class (survives DOM rebuilds via reapplyHover)
      rows.querySelectorAll(".cargo-row.hovered").forEach(el => el.classList.remove("hovered"));
      if (row.classList.contains("cargo-row")) row.classList.add("hovered");
      showCargoTooltip(itemId, cargoState, row);
    }
  });

  panel.addEventListener("mouseout", (e) => {
    const row = (e.target as HTMLElement).closest(".cargo-row[data-item-id], .equip-slot[data-item-id]") as HTMLElement | null;
    if (row && row.classList.contains("cargo-row")) {
      row.classList.remove("hovered");
    }
  });

  // Hide tooltip when mouse leaves the panel entirely
  panel.addEventListener("mouseleave", () => {
    hoveredItemId = 0;
    rows.querySelectorAll(".cargo-row.hovered").forEach(el => el.classList.remove("hovered"));
    hideCargoTooltip();
  });
}

export function updateHUD(state: GameState): void {
  const myEntity = state.entities.get(state.myEntityId);

  let hudText = `${state.playerUsername} | FPS: ${state.fps} | Ping: ${state.pingMs}ms | Tick: ${state.tickCount} | Entities: ${state.entities.size}`;
  if (myEntity) {
    const curName = state.itemDefs.get(SETTLEMENT_CURRENCY_ID)?.name ?? "Currency";
    hudText += ` | ${curName}: ${Math.floor(state.currencyBalances[SETTLEMENT_CURRENCY_ID] ?? 0)}`;
    const spd = Math.sqrt(myEntity.curr.vx * myEntity.curr.vx + myEntity.curr.vy * myEntity.curr.vy);
    hudText += ` | Speed: ${Math.floor(spd)}`;
    hudText += `\nCell: (${state.originCellX}, ${state.originCellY}) | Pos: (${myEntity.renderX.toFixed(0)}, ${myEntity.renderY.toFixed(0)})`;
  }
  hudEl().textContent = hudText;
}

export function updateMoveMode(state: GameState): void {
  const el = moveModeEl();
  if (!state.loggedIn || state.isDead || state.isDocked) {
    el.style.display = "none";
    return;
  }
  el.style.display = "block";
  const isDest = state.moveMode === 'destination';
  el.className = isDest ? "mode-destination" : "mode-direction";
  el.textContent = isDest ? "Destination [V]" : "Direction [V]";
}

export function updateStatusBars(state: GameState): void {
  const myEntity = state.entities.get(state.myEntityId);
  const el = statusBarsEl();

  if (!myEntity) {
    el.style.display = "none";
    return;
  }
  el.style.display = "block";

  const combat = getCombat(myEntity.curr);
  const shFrac = combat && combat.maxShield > 0 ? combat.shield / combat.maxShield : 0;
  const hpFrac = combat && combat.maxHealth > 0 ? combat.health / combat.maxHealth : 0;

  // Shield
  const shieldFill = document.querySelector("#shield-bar .bar-fill") as HTMLElement;
  const shieldLabel = document.querySelector("#shield-bar .bar-label") as HTMLElement;
  shieldFill.style.width = `${shFrac * 100}%`;
  shieldLabel.textContent = `SHIELD ${Math.floor(combat?.shield ?? 0)} / ${Math.floor(combat?.maxShield ?? 0)}`;

  // Health
  const hpFill = document.querySelector("#health-bar .bar-fill") as HTMLElement;
  const hpLabel = document.querySelector("#health-bar .bar-label") as HTMLElement;
  hpFill.style.width = `${hpFrac * 100}%`;
  hpLabel.textContent = `HP ${Math.floor(combat?.health ?? 0)} / ${Math.floor(combat?.maxHealth ?? 0)}`;
  if (hpFrac <= 0.3) {
    hpFill.style.background = "rgba(255,30,30,1)";
  } else {
    hpFill.style.background = "rgba(255,60,60,0.8)";
  }

  // Cargo - use mass-based values from state (populated by PlayerOwnStateMsg)
  const cargoMass = state.cargoMass;
  const maxCargoMass = state.maxCargoMass || 100;
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
  const el = stationPromptEl();

  if (state.isDead) {
    el.style.display = "none";
    return;
  }

  // Docked state
  if (state.isDocked) {
    el.style.display = "block";
    el.textContent = "DOCKED AT STATION — Press X to undock";
    return;
  }

  // Docking in progress
  if (state.isDockingInProgress) {
    const pct = Math.floor(state.dockingProgress * 100);
    el.style.display = "block";
    el.textContent = `DOCKING... ${pct}%`;
    return;
  }

  const myEntity = state.entities.get(state.myEntityId);
  if (!myEntity) {
    el.style.display = "none";
    return;
  }

  let nearStation = false;
  for (const [, ent] of state.entities) {
    if (ent.curr.entityType !== EntityType.STATION) continue;
    const dx = myEntity.renderX - ent.renderX;
    const dy = myEntity.renderY - ent.renderY;
    if (Math.sqrt(dx * dx + dy * dy) < 400) {
      nearStation = true;
      break;
    }
  }

  if (nearStation) {
    el.style.display = "block";
    el.textContent = "Press X to dock";
  } else {
    el.style.display = "none";
    // Close bank panel if we moved away from station
    if (state.bankPanelOpen) {
      state.bankPanelOpen = false;
    }
  }
}

export function updateDeathScreen(state: GameState): void {
  deathScreenEl().style.display = state.isDead && !state.isDocked ? "flex" : "none";
}

export function updateCargoPanel(state: GameState): void {
  const myEntity = state.entities.get(state.myEntityId);
  const el = cargoPanelEl();

  cargoState = state;
  setupCargoEvents();

  // Shift status bar when panel is open/closed
  const statusEl = document.getElementById("status");
  if (!state.cargoPanelOpen || !myEntity) {
    el.style.display = "none";
    hideCargoTooltip();
    if (statusEl) statusEl.style.right = "10px";
    return;
  }
  el.style.display = "block";
  if (statusEl) statusEl.style.right = "320px";

  // --- Equipment slots (memoized) ---
  // Include drag state in hash so highlights update when drag starts/stops
  const dragHighlightKey = dragStarted && dragSource?.type === "cargo" ? `drag-${dragSource.equipSlot}` : "none";
  const equipEl = equipSlotsEl();
  if (needsRebuild("cargo-equip", state.equipment.weapon1, state.equipment.weapon2, state.equipment.shield, state.equipment.thruster, dragHighlightKey)) {
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
      box.dataset.slot = s.slot.toString();
      if (s.itemId) {
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

      // Highlight valid drop targets during cargo drag
      if (dragStarted && dragSource?.type === "cargo" && isValidSlotForItem(dragSource.equipSlot, s.slot)) {
        box.classList.add("drop-target");
      }

      equipEl.appendChild(box);
    }
  }

  // --- Cargo rows (memoized) ---
  const cargoItemsMap = state.cargoItems;
  const cargoPanelMass = state.cargoMass;
  const cargoPanelMaxMass = state.maxCargoMass || 100;

  if (needsRebuild("cargo-rows", cargoItemsMap)) {
    const rows2 = cargoRowsEl();
    rows2.innerHTML = "";

    if (cargoItemsMap.size === 0) {
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
      const sorted = [...cargoItemsMap.entries()].sort((a, b) => a[0] - b[0]);
      for (const [itemId, quantity] of sorted) {
        const item = { itemId, quantity };
        const def = state.itemDefs.get(item.itemId);
        const name = def ? def.name : `Item #${item.itemId}`;
        const color = ITEM_COLORS_CSS[item.itemId] || DEFAULT_ITEM_COLOR;
        const itemMass = def ? item.quantity * def.massPerUnit : item.quantity;
        const frac = cargoPanelMaxMass > 0 ? itemMass / cargoPanelMaxMass : 0;

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

    // Re-apply hover class after rebuild
    if (hoveredItemId) {
      const hovered = rows2.querySelector(`.cargo-row[data-item-id="${hoveredItemId}"]`);
      if (hovered) hovered.classList.add("hovered");
    }
  }

  const curName2 = state.itemDefs.get(SETTLEMENT_CURRENCY_ID)?.name ?? "Currency";
  cargoFooterEl().textContent = `${Math.floor(cargoPanelMass)} / ${Math.floor(cargoPanelMaxMass)} mass  |  ${curName2}: ${Math.floor(state.currencyBalances[SETTLEMENT_CURRENCY_ID] ?? 0)}  |  RClick: Equip  Drag: Move  Alt: Jettison`;
  if (cargoPanelMass >= cargoPanelMaxMass) {
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

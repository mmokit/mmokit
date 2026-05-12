import type { GameState } from "../state";
import { RESOURCE_NAMES } from "../constants";
import { LootAll, LootItem } from "../../sdk/index.js";

let popupEl: HTMLElement | null = null;
let headerEl: HTMLElement | null = null;
let closeBtn: HTMLElement | null = null;
let itemsContainer: HTMLElement | null = null;
let lootAllBtn: HTMLElement | null = null;

// Drag state
let dragging = false;
let dragOffX = 0;
let dragOffY = 0;

// Stashed state ref for event handlers
let stateRef: GameState | null = null;
let lastRenderedCrateId = 0;
let lastRenderedItemsSig = "";

const LOOT_RANGE_OPEN = 90;
const LOOT_RANGE_CLOSE = 120; // hysteresis to prevent flicker

export function createLootPopup(): void {
  popupEl = document.createElement("div");
  popupEl.id = "loot-popup";
  popupEl.style.cssText = `
    position: fixed;
    bottom: 320px;
    left: 50%;
    transform: translateX(-50%);
    width: 220px;
    background: rgba(0, 0, 0, 0.85);
    border: 1px solid #e8a020;
    border-radius: 4px;
    padding: 0;
    font-family: monospace;
    color: #ccc;
    z-index: 110;
    display: none;
  `;

  // Header (draggable)
  headerEl = document.createElement("div");
  headerEl.style.cssText = `
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 6px 8px;
    background: rgba(232, 160, 32, 0.15);
    border-bottom: 1px solid #e8a020;
    cursor: grab;
    user-select: none;
  `;

  const titleEl = document.createElement("div");
  titleEl.style.cssText = "font-size: 11px; font-weight: bold; color: #e8a020; letter-spacing: 1px;";
  titleEl.textContent = "LOOT CRATE";
  headerEl.appendChild(titleEl);

  closeBtn = document.createElement("div");
  closeBtn.style.cssText = `
    font-size: 11px;
    color: #888;
    cursor: pointer;
    padding: 0 4px;
    user-select: none;
  `;
  closeBtn.textContent = "X";
  closeBtn.addEventListener("click", (e) => {
    e.stopPropagation();
    if (stateRef) stateRef.lootCrateId = 0;
  });
  closeBtn.addEventListener("mouseenter", () => {
    closeBtn!.style.color = "#fff";
  });
  closeBtn.addEventListener("mouseleave", () => {
    closeBtn!.style.color = "#888";
  });
  headerEl.appendChild(closeBtn);

  // Drag-to-move
  headerEl.addEventListener("mousedown", (e) => {
    if (e.target === closeBtn) return;
    dragging = true;
    headerEl!.style.cursor = "grabbing";
    const rect = popupEl!.getBoundingClientRect();
    dragOffX = e.clientX - rect.left;
    dragOffY = e.clientY - rect.top;
    e.preventDefault();
  });

  window.addEventListener("mousemove", (e) => {
    if (!dragging) return;
    const x = Math.max(0, Math.min(e.clientX - dragOffX, window.innerWidth - popupEl!.offsetWidth));
    const y = Math.max(0, Math.min(e.clientY - dragOffY, window.innerHeight - popupEl!.offsetHeight));
    popupEl!.style.left = `${x}px`;
    popupEl!.style.top = `${y}px`;
    popupEl!.style.bottom = "auto";
    popupEl!.style.transform = "none";
  });

  window.addEventListener("mouseup", () => {
    if (dragging) {
      dragging = false;
      headerEl!.style.cursor = "grab";
    }
  });

  popupEl.appendChild(headerEl);

  // Items container
  itemsContainer = document.createElement("div");
  itemsContainer.style.cssText = `
    max-height: 200px;
    overflow-y: auto;
    padding: 4px 0;
  `;

  // Per-item loot buttons are populated in updateLootPopup from the replicated
  // Inventory var-tail on the targeted crate entity.

  popupEl.appendChild(itemsContainer);

  // Loot All button
  lootAllBtn = document.createElement("div");
  lootAllBtn.style.cssText = `
    padding: 6px 8px;
    text-align: center;
    font-size: 11px;
    font-weight: bold;
    color: #e8a020;
    cursor: pointer;
    border-top: 1px solid #333;
    user-select: none;
  `;
  lootAllBtn.textContent = "LOOT ALL";
  lootAllBtn.addEventListener("mouseenter", () => {
    lootAllBtn!.style.background = "rgba(232, 160, 32, 0.15)";
  });
  lootAllBtn.addEventListener("mouseleave", () => {
    lootAllBtn!.style.background = "transparent";
  });
  lootAllBtn.addEventListener("mousedown", () => {
    if (stateRef?.client && stateRef.lootCrateId) {
      stateRef.inputSeq++;
      stateRef.client.send(new LootAll({
        sequence: stateRef.inputSeq,
        crateNetID: stateRef.lootCrateId,
      }));
      stateRef.toasts.push({ text: "Looting all...", time: performance.now() });
    }
  });
  popupEl.appendChild(lootAllBtn);

  document.body.appendChild(popupEl);
}

export function updateLootPopup(state: GameState): void {
  if (!popupEl) return;
  stateRef = state;

  // Auto-open when player arrives within range of a pending loot crate
  if (state.pendingLootCrateId && !state.lootCrateId) {
    const pendingEnt = state.entities.get(state.pendingLootCrateId);
    const myEnt = state.entities.get(state.myEntityId);
    if (pendingEnt && myEnt) {
      const dx = myEnt.renderX - pendingEnt.renderX;
      const dy = myEnt.renderY - pendingEnt.renderY;
      if (Math.sqrt(dx * dx + dy * dy) <= LOOT_RANGE_OPEN) {
        state.lootCrateId = state.pendingLootCrateId;
        state.pendingLootCrateId = 0;
      }
    } else if (!pendingEnt) {
      // Crate despawned while approaching
      state.pendingLootCrateId = 0;
    }
  }

  if (!state.lootCrateId) {
    popupEl.style.display = "none";
    return;
  }

  const ent = state.entities.get(state.lootCrateId);
  if (!ent) {
    state.lootCrateId = 0;
    popupEl.style.display = "none";
    return;
  }

  // Auto-close if player moved out of range (with hysteresis)
  const myEnt = state.entities.get(state.myEntityId);
  if (myEnt) {
    const dx = myEnt.renderX - ent.renderX;
    const dy = myEnt.renderY - ent.renderY;
    if (Math.sqrt(dx * dx + dy * dy) > LOOT_RANGE_CLOSE) {
      state.lootCrateId = 0;
      popupEl.style.display = "none";
      return;
    }
  }

  popupEl.style.display = "block";

  // Rebuild item buttons when the rendered crate OR its inventory changes.
  // The signature captures length + every (itemId,quantity) pair so partial
  // loots (e.g. another player looting one item) refresh the display.
  const items = (ent.current as { items?: Array<{ itemId: number; quantity: number }> }).items ?? [];
  const itemsSig = items.map((i) => `${i.itemId}:${i.quantity}`).join(",");
  if (state.lootCrateId !== lastRenderedCrateId || itemsSig !== lastRenderedItemsSig) {
    lastRenderedCrateId = state.lootCrateId;
    lastRenderedItemsSig = itemsSig;
    itemsContainer!.innerHTML = "";

    if (items.length === 0) {
      const emptyEl = document.createElement("div");
      emptyEl.style.cssText = "padding: 8px; text-align: center; color: #888; font-size: 10px;";
      emptyEl.textContent = "Empty";
      itemsContainer!.appendChild(emptyEl);
    } else {
      for (const item of items) {
        const itemName =
          stateRef?.itemDefs.get(item.itemId)?.name ||
          RESOURCE_NAMES[item.itemId] ||
          `item${item.itemId}`;
        const btn = document.createElement("div");
        btn.style.cssText = `
          padding: 6px 10px;
          font-size: 11px;
          color: #ddd;
          cursor: pointer;
          border-bottom: 1px solid #222;
          user-select: none;
          display: flex;
          justify-content: space-between;
        `;
        btn.innerHTML = `<span>${itemName}</span><span style="color: #e8a020;">x${item.quantity}</span>`;
        btn.addEventListener("mouseenter", () => {
          btn.style.background = "rgba(232, 160, 32, 0.15)";
        });
        btn.addEventListener("mouseleave", () => {
          btn.style.background = "transparent";
        });
        btn.addEventListener("mousedown", () => {
          if (stateRef?.client && stateRef.lootCrateId) {
            stateRef.inputSeq++;
            stateRef.client.send(new LootItem({
              sequence: stateRef.inputSeq,
              crateNetID: stateRef.lootCrateId,
              itemID: item.itemId,
            }));
          }
        });
        itemsContainer!.appendChild(btn);
      }
    }
  }
  lootAllBtn!.style.display = "block";
}

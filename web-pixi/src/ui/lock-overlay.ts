import { EntityType } from "@gen/game_pb.js";
import type { GameState } from "../state";

let overlayEl: HTMLElement | null = null;
let nameEl: HTMLElement | null = null;
let hpBarEl: HTMLElement | null = null;
let shieldBarEl: HTMLElement | null = null;
let hpLabelEl: HTMLElement | null = null;
let shieldLabelEl: HTMLElement | null = null;
let statusEl: HTMLElement | null = null;
let unlockBtn: HTMLElement | null = null;

export function createLockOverlay(onUnlock: () => void): void {
  overlayEl = document.createElement("div");
  overlayEl.id = "lock-overlay";
  overlayEl.style.cssText = `
    position: fixed;
    bottom: 160px;
    left: 50%;
    transform: translateX(-50%);
    width: 180px;
    background: rgba(0, 0, 0, 0.75);
    border: 1px solid #444;
    border-radius: 4px;
    padding: 8px;
    font-family: monospace;
    color: #ccc;
    z-index: 100;
    display: none;
  `;

  // Header row: name + unlock button (draggable)
  const header = document.createElement("div");
  header.style.cssText = "display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px; cursor: grab; user-select: none;";

  // Drag-to-move
  let dragging = false;
  let dragOffX = 0;
  let dragOffY = 0;

  header.addEventListener("mousedown", (e) => {
    if (e.target === unlockBtn) return;
    dragging = true;
    header.style.cursor = "grabbing";
    const rect = overlayEl!.getBoundingClientRect();
    dragOffX = e.clientX - rect.left;
    dragOffY = e.clientY - rect.top;
    e.preventDefault();
  });

  window.addEventListener("mousemove", (e) => {
    if (!dragging) return;
    const x = Math.max(0, Math.min(e.clientX - dragOffX, window.innerWidth - overlayEl!.offsetWidth));
    const y = Math.max(0, Math.min(e.clientY - dragOffY, window.innerHeight - overlayEl!.offsetHeight));
    overlayEl!.style.left = `${x}px`;
    overlayEl!.style.top = `${y}px`;
    overlayEl!.style.bottom = "auto";
    overlayEl!.style.transform = "none";
  });

  window.addEventListener("mouseup", () => {
    if (dragging) {
      dragging = false;
      header.style.cursor = "grab";
    }
  });

  nameEl = document.createElement("div");
  nameEl.style.cssText = "font-size: 12px; font-weight: bold; color: #ff6666; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;";
  header.appendChild(nameEl);

  unlockBtn = document.createElement("div");
  unlockBtn.style.cssText = `
    font-size: 9px;
    color: #ff4444;
    cursor: pointer;
    padding: 2px 6px;
    border: 1px solid #ff4444;
    border-radius: 2px;
    user-select: none;
    white-space: nowrap;
    flex-shrink: 0;
  `;
  unlockBtn.textContent = "UNLOCK";
  unlockBtn.addEventListener("click", (e) => {
    e.stopPropagation();
    onUnlock();
  });
  unlockBtn.addEventListener("mouseenter", () => {
    unlockBtn!.style.background = "rgba(255, 68, 68, 0.2)";
  });
  unlockBtn.addEventListener("mouseleave", () => {
    unlockBtn!.style.background = "transparent";
  });
  header.appendChild(unlockBtn);

  overlayEl.appendChild(header);

  // Lock status
  statusEl = document.createElement("div");
  statusEl.style.cssText = "font-size: 10px; margin-bottom: 6px;";
  overlayEl.appendChild(statusEl);

  // Shield bar
  const shieldRow = createBarRow("SHIELD", "#5082ff");
  shieldBarEl = shieldRow.fill;
  shieldLabelEl = shieldRow.label;
  overlayEl.appendChild(shieldRow.container);

  // HP bar
  const hpRow = createBarRow("HP", "#ff3c3c");
  hpBarEl = hpRow.fill;
  hpLabelEl = hpRow.label;
  overlayEl.appendChild(hpRow.container);

  document.body.appendChild(overlayEl);
}

function createBarRow(name: string, color: string) {
  const container = document.createElement("div");
  container.style.cssText = "margin-bottom: 4px;";

  const label = document.createElement("div");
  label.style.cssText = `font-size: 9px; color: ${color}; margin-bottom: 1px;`;
  label.textContent = name;
  container.appendChild(label);

  const barBg = document.createElement("div");
  barBg.style.cssText = `
    width: 100%;
    height: 6px;
    background: rgba(255,255,255,0.08);
    border-radius: 2px;
    overflow: hidden;
  `;

  const fill = document.createElement("div");
  fill.style.cssText = `
    height: 100%;
    width: 0%;
    background: ${color};
    border-radius: 2px;
    transition: width 0.1s;
  `;
  barBg.appendChild(fill);
  container.appendChild(barBg);

  return { container, fill, label };
}

export function updateLockOverlay(state: GameState): void {
  if (!overlayEl) return;

  if (!state.lockTargetId || !state.entities.has(state.lockTargetId)) {
    overlayEl.style.display = "none";
    return;
  }

  overlayEl.style.display = "block";
  const tgt = state.entities.get(state.lockTargetId)!;

  // Name
  const isNpc = tgt.curr.entityType === EntityType.NPC;
  const name = tgt.curr.pilotName || (isNpc ? "NPC" : "Ship");
  nameEl!.textContent = name;
  nameEl!.style.color = isNpc ? "#ff6666" : "#44aaff";

  // Border color matches target type
  overlayEl.style.borderColor = isNpc ? "#ff4444" : "#44aaff";

  // Lock status
  const progress = state.lockProgress;
  if (progress >= 1.0) {
    statusEl!.textContent = "● LOCKED";
    statusEl!.style.color = "#00ff00";
  } else {
    statusEl!.textContent = `◐ LOCKING ${Math.floor(progress * 100)}%`;
    statusEl!.style.color = progress > 0.5 ? "#ffaa00" : "#ff4444";
  }

  // Bars
  const hp = tgt.curr.health;
  const sh = tgt.curr.shield;
  hpBarEl!.style.width = `${hp * 100}%`;
  hpLabelEl!.textContent = `HP ${Math.floor(hp * 100)}%`;
  shieldBarEl!.style.width = `${sh * 100}%`;
  shieldLabelEl!.textContent = `SHIELD ${Math.floor(sh * 100)}%`;
}

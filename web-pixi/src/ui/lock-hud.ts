import type { GameState } from "../state";

// Horizontal strip in the bottom-left corner showing one icon per locked
// (or in-progress) slot from LockSlotsMsg. Active slot gets a highlighted
// border; in-progress slots draw a partial radial fill. Cap at 4 visible
// to match server-side MaxLockSlots.

const MAX_SLOTS = 4;

let stripEl: HTMLElement | null = null;
let slotEls: HTMLElement[] = [];
let rejectFlashEl: HTMLElement | null = null;

export function createLockHud(): void {
  stripEl = document.createElement("div");
  stripEl.id = "lock-hud-strip";
  stripEl.style.cssText = `
    position: fixed;
    bottom: 24px;
    left: 24px;
    display: flex;
    gap: 8px;
    z-index: 90;
    pointer-events: none;
    font-family: monospace;
  `;
  document.body.appendChild(stripEl);

  for (let i = 0; i < MAX_SLOTS; i++) {
    const slot = document.createElement("div");
    slot.style.cssText = `
      position: relative;
      width: 44px;
      height: 44px;
      border: 1px solid #444;
      border-radius: 4px;
      background: rgba(0, 0, 0, 0.65);
      display: none;
      box-sizing: border-box;
      color: #ccc;
      font-size: 9px;
      overflow: hidden;
    `;

    // Progress bar along the bottom edge — fills 0..100% during locking.
    const progress = document.createElement("div");
    progress.className = "lock-hud-progress";
    progress.style.cssText = `
      position: absolute;
      left: 0;
      bottom: 0;
      height: 3px;
      width: 0%;
      background: #ffaa00;
      transition: width 0.05s linear;
    `;
    slot.appendChild(progress);

    // Center reticle glyph
    const icon = document.createElement("div");
    icon.className = "lock-hud-icon";
    icon.style.cssText = `
      position: absolute;
      inset: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 18px;
      font-weight: bold;
    `;
    icon.textContent = "+";
    slot.appendChild(icon);

    // Slot index in the bottom-right corner.
    const idx = document.createElement("div");
    idx.className = "lock-hud-idx";
    idx.style.cssText = `
      position: absolute;
      bottom: 2px;
      right: 4px;
      font-size: 8px;
      color: #888;
    `;
    idx.textContent = `${i + 1}`;
    slot.appendChild(idx);

    slotEls.push(slot);
    stripEl.appendChild(slot);
  }

  rejectFlashEl = document.createElement("div");
  rejectFlashEl.id = "lock-reject-flash";
  rejectFlashEl.style.cssText = `
    position: fixed;
    bottom: 80px;
    left: 24px;
    padding: 4px 10px;
    background: rgba(255, 68, 68, 0.85);
    color: #fff;
    font-family: monospace;
    font-size: 11px;
    border-radius: 3px;
    z-index: 91;
    display: none;
    pointer-events: none;
  `;
  document.body.appendChild(rejectFlashEl);
}

const REJECT_REASONS: Record<number, string> = {
  0: "Lock rejected",
  1: "Out of range",
  2: "No line of sight",
  3: "Already locked",
  4: "Locks full",
  5: "Target invalid",
};

export function updateLockHud(state: GameState): void {
  if (!stripEl || !rejectFlashEl) return;

  const slots = state.lockSlots;
  const active = state.lockTargetId;

  for (let i = 0; i < MAX_SLOTS; i++) {
    const el = slotEls[i];
    const s = slots[i];
    if (!s) {
      el.style.display = "none";
      continue;
    }
    el.style.display = "block";

    const isActive = s.targetNetID === active && active !== 0;
    const locked = s.locked;
    const color = locked ? (isActive ? "#00ff00" : "#88aa88") : "#ffaa00";
    el.style.borderColor = isActive ? color : (locked ? "#226622" : "#553300");
    el.style.boxShadow = isActive ? `0 0 6px ${color}` : "none";

    const progress = el.querySelector(".lock-hud-progress") as HTMLElement | null;
    if (progress) {
      progress.style.width = `${Math.min(1, Math.max(0, s.progress)) * 100}%`;
      progress.style.background = color;
    }
    const icon = el.querySelector(".lock-hud-icon") as HTMLElement | null;
    if (icon) {
      icon.style.color = color;
      icon.textContent = locked ? "◉" : "◐";
    }
  }

  const flash = state.lockRejectFlash;
  if (flash && performance.now() < flash.until) {
    rejectFlashEl.style.display = "block";
    rejectFlashEl.textContent = REJECT_REASONS[flash.reason] || `Lock rejected (${flash.reason})`;
  } else {
    rejectFlashEl.style.display = "none";
    if (flash) state.lockRejectFlash = null;
  }
}

import type { GameState } from "../state";

const ABILITY_SLOTS = [
  { key: "Q", name: "Barrage", color: "#4af", range: 500 },
  { key: "W", name: "Rail", color: "#fa4", range: 1000 },
  { key: "E", name: "Ion", color: "#a4f", range: 500 },
  { key: "R", name: "Torpedo", color: "#f44", range: 900 },
  { key: "D", name: "Shield", color: "#4f4", range: 0 },
  { key: "F", name: "Boost", color: "#ff4", range: 0 },
];

let barEl: HTMLElement | null = null;
let slotEls: HTMLElement[] = [];

export function createAbilityBar(): void {
  barEl = document.createElement("div");
  barEl.id = "ability-bar";
  barEl.style.cssText = `
    position: fixed;
    bottom: 16px;
    left: 50%;
    transform: translateX(-50%);
    display: flex;
    gap: 6px;
    z-index: 100;
    pointer-events: none;
  `;

  slotEls = ABILITY_SLOTS.map((slot) => {
    const el = document.createElement("div");
    el.style.cssText = `
      width: 50px;
      height: 50px;
      border: 2px solid ${slot.color};
      border-radius: 4px;
      background: rgba(0, 0, 0, 0.7);
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      position: relative;
      font-family: monospace;
      color: #fff;
    `;

    const keyLabel = document.createElement("div");
    keyLabel.style.cssText = `font-size: 16px; font-weight: bold; color: ${slot.color};`;
    keyLabel.textContent = slot.key;
    el.appendChild(keyLabel);

    const nameLabel = document.createElement("div");
    nameLabel.style.cssText = "font-size: 8px; color: #999; white-space: nowrap;";
    nameLabel.textContent = slot.name;
    el.appendChild(nameLabel);

    // Out-of-range overlay
    const rangeOverlay = document.createElement("div");
    rangeOverlay.className = "range-overlay";
    rangeOverlay.style.cssText = `
      position: absolute;
      top: 0; left: 0; right: 0; bottom: 0;
      background: rgba(180, 0, 0, 0.35);
      border-radius: 2px;
      pointer-events: none;
    `;
    rangeOverlay.style.display = "none";
    el.appendChild(rangeOverlay);

    // Cooldown overlay
    const cdOverlay = document.createElement("div");
    cdOverlay.className = "cd-overlay";
    cdOverlay.style.cssText = `
      position: absolute;
      top: 0; left: 0; right: 0; bottom: 0;
      background: rgba(0, 0, 0, 0.7);
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 14px;
      font-weight: bold;
      color: #fff;
      border-radius: 2px;
    `;
    cdOverlay.style.display = "none";
    el.appendChild(cdOverlay);

    barEl!.appendChild(el);
    return el;
  });

  document.body.appendChild(barEl);
}

export function updateAbilityBar(state: GameState): void {
  if (!barEl) return;
  barEl.style.display = state.isDead || !state.loggedIn ? "none" : "flex";

  // Calculate distance to lock target for range checks
  let distToTarget = Infinity;
  const myEnt = state.entities.get(state.myEntityId);
  const lockEnt = state.lockTargetId ? state.entities.get(state.lockTargetId) : null;
  if (myEnt && lockEnt) {
    const dx = lockEnt.renderX - myEnt.renderX;
    const dy = lockEnt.renderY - myEnt.renderY;
    distToTarget = Math.sqrt(dx * dx + dy * dy);
  }

  for (let i = 0; i < ABILITY_SLOTS.length; i++) {
    const slot = ABILITY_SLOTS[i];
    const el = slotEls[i];
    const cdOverlay = el.querySelector(".cd-overlay") as HTMLElement;
    const rangeOverlay = el.querySelector(".range-overlay") as HTMLElement;
    const cd = state.abilityCooldowns.get(i);

    if (cd && cd.remaining > 0) {
      cdOverlay.style.display = "flex";
      cdOverlay.textContent = cd.remaining.toFixed(1);
    } else {
      cdOverlay.style.display = "none";
    }

    // Show red overlay if ability has a range and target is out of range (or no lock target)
    if (slot.range > 0) {
      const outOfRange = lockEnt && distToTarget > slot.range;
      rangeOverlay.style.display = outOfRange ? "block" : "none";
    } else {
      rangeOverlay.style.display = "none";
    }
  }
}

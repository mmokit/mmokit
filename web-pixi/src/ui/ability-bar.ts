import type { GameState } from "../state";

const ABILITY_SLOTS = [
  {
    key: "Q", name: "Barrage", color: "#4af", range: 500,
    title: "Missile Barrage",
    desc: "Fires a salvo of 6 guided missiles at the locked target.",
    stats: ["Damage: 15", "Range: 500", "Cooldown: 2s"],
  },
  {
    key: "W", name: "Rail", color: "#fa4", range: 1000,
    title: "Railgun",
    desc: "High-damage single shot. Long range, long cooldown.",
    stats: ["Damage: 35", "Range: 1000", "Cooldown: 6s"],
  },
  {
    key: "E", name: "Ion", color: "#a4f", range: 500,
    title: "Ion Disruptor",
    desc: "Applies a damage-over-time burn to the target.",
    stats: ["DPS: 6", "Duration: 4s", "Range: 500", "Cooldown: 8s"],
  },
  {
    key: "R", name: "Torpedo", color: "#f44", range: 900,
    title: "Plasma Torpedo",
    desc: "Slow but devastating. Bonus damage against unshielded targets.",
    stats: ["Damage: 60 (+30 no shield)", "Range: 900", "Cooldown: 20s"],
  },
  {
    key: "D", name: "Shield", color: "#4f4", range: 0,
    title: "Emergency Shields",
    desc: "Restores shield and reduces incoming damage briefly.",
    stats: ["Restore: 25", "Dmg Reduction: 30%", "Duration: 3s", "Cooldown: 15s"],
  },
  {
    key: "F", name: "Boost", color: "#ff4", range: 0,
    title: "Afterburner",
    desc: "Temporary speed boost for escape or pursuit.",
    stats: ["Speed: 2.5x", "Duration: 1.5s", "Cooldown: 10s"],
  },
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
    pointer-events: auto;
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

    // Tooltip
    const tooltip = document.createElement("div");
    tooltip.className = "ability-tooltip";
    tooltip.style.cssText = `
      position: absolute;
      bottom: 58px;
      left: 50%;
      transform: translateX(-50%);
      width: 160px;
      background: rgba(0, 0, 0, 0.9);
      border: 1px solid ${slot.color};
      border-radius: 4px;
      padding: 8px;
      font-family: monospace;
      pointer-events: none;
      display: none;
      z-index: 200;
    `;

    const ttTitle = document.createElement("div");
    ttTitle.style.cssText = `font-size: 11px; font-weight: bold; color: ${slot.color}; margin-bottom: 4px;`;
    ttTitle.textContent = `[${slot.key}] ${slot.title}`;
    tooltip.appendChild(ttTitle);

    const ttDesc = document.createElement("div");
    ttDesc.style.cssText = "font-size: 9px; color: #bbb; margin-bottom: 6px; line-height: 1.3;";
    ttDesc.textContent = slot.desc;
    tooltip.appendChild(ttDesc);

    for (const stat of slot.stats) {
      const line = document.createElement("div");
      line.style.cssText = "font-size: 9px; color: #ddd;";
      line.textContent = stat;
      tooltip.appendChild(line);
    }

    el.appendChild(tooltip);

    el.addEventListener("mouseenter", () => {
      tooltip.style.display = "block";
    });
    el.addEventListener("mouseleave", () => {
      tooltip.style.display = "none";
    });

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

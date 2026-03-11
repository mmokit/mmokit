import type { GameState } from "../state";

// Equipment slot constants (matches server EquipSlot enum)
const EQUIP_SLOT_WEAPON1 = 1;
const EQUIP_SLOT_WEAPON2 = 2;
const EQUIP_SLOT_SHIELD = 3;
const EQUIP_SLOT_THRUSTER = 4;

// Ability slot index -> key mapping
const SLOT_KEYS = ["Q", "W", "E", "R", "D", "F"];

// Default colors per equipment slot type
const SLOT_COLORS: Record<number, string> = {
  0: "#4af",  // Q (weapon1 primary)
  1: "#4af",  // W (weapon1 secondary)
  2: "#a4f",  // E (weapon2 primary)
  3: "#a4f",  // R (weapon2 secondary)
  4: "#4f4",  // D (shield)
  5: "#ff4",  // F (thruster)
};

// Client-side ability data per equipment item ID
// Matches server item registry in internal/item/item.go
interface AbilityInfo {
  name: string;
  title: string;
  desc: string;
  stats: string[];
  range: number;
  color?: string;
}

const ITEM_ABILITIES: Record<number, { primary: AbilityInfo; secondary?: AbilityInfo }> = {
  // Weapon 1 items (Q + W)
  100: { // Pulse Laser Array
    primary: {
      name: "Pulse", title: "Pulse Shot", range: 500,
      desc: "Rapid-fire energy pulse at the locked target.",
      stats: ["Damage: 15", "Range: 500", "Cooldown: 2s"],
    },
    secondary: {
      name: "Barrage", title: "Pulse Barrage", range: 400,
      desc: "Fires a concentrated burst of pulse energy.",
      stats: ["Damage: 25", "Range: 400", "Cooldown: 5s"],
    },
  },
  101: { // Railgun System
    primary: {
      name: "Rail", title: "Rail Shot", range: 1000,
      desc: "High-damage single shot. Long range, long cooldown.",
      stats: ["Damage: 35", "Range: 1000", "Cooldown: 6s"],
    },
    secondary: {
      name: "Pierce", title: "Piercing Round", range: 800,
      desc: "Armor-piercing round. Bonus damage vs unshielded.",
      stats: ["Damage: 50 (+20 no shield)", "Range: 800", "Cooldown: 10s"],
    },
  },
  // Weapon 2 items (E + R)
  105: { // Ion Array
    primary: {
      name: "Ion", title: "Ion Burn", range: 500,
      desc: "Applies a damage-over-time burn to the target.",
      stats: ["DPS: 6", "Duration: 4s", "Range: 500", "Cooldown: 8s"],
    },
    secondary: {
      name: "Overld", title: "Ion Overload", range: 600,
      desc: "Releases a concentrated ion discharge.",
      stats: ["Damage: 40", "Range: 600", "Cooldown: 12s"],
    },
  },
  106: { // Plasma System
    primary: {
      name: "Plasma", title: "Plasma Bolt", range: 700,
      desc: "Versatile plasma projectile.",
      stats: ["Damage: 20", "Range: 700", "Cooldown: 4s"],
    },
    secondary: {
      name: "Torp", title: "Plasma Torpedo", range: 900,
      desc: "Slow but devastating. Bonus damage vs unshielded.",
      stats: ["Damage: 60 (+30 no shield)", "Range: 900", "Cooldown: 20s"],
    },
  },
  // Shield items (D)
  110: { // Standard Shield Gen
    primary: {
      name: "Shield", title: "Emergency Shield", range: 0,
      desc: "Restores shield and reduces incoming damage briefly.",
      stats: ["Restore: 25", "Dmg Reduction: 30%", "Duration: 3s", "Cooldown: 15s"],
    },
  },
  111: { // Hardened Shield Gen
    primary: {
      name: "Harden", title: "Hardened Shield", range: 0,
      desc: "Heavy shield restore with strong damage reduction.",
      stats: ["Restore: 40", "Dmg Reduction: 50%", "Duration: 2s", "Cooldown: 20s"],
    },
  },
  // Thruster items (F)
  120: { // Standard Thruster
    primary: {
      name: "Boost", title: "Afterburner", range: 0,
      desc: "Temporary speed boost for escape or pursuit.",
      stats: ["Speed: 2.5x", "Duration: 1.5s", "Cooldown: 10s"],
    },
  },
  121: { // Micro Warp Drive
    primary: {
      name: "Warp", title: "Micro Warp", range: 0,
      desc: "Extreme speed burst for a very short time.",
      stats: ["Speed: 4.0x", "Duration: 0.8s", "Cooldown: 18s"],
    },
  },
};

// Get ability info for a given ability slot from current equipment
function getAbilityForSlot(state: GameState, slot: number): AbilityInfo | null {
  let itemId: number;
  let isPrimary: boolean;

  switch (slot) {
    case 0: itemId = state.equipment.weapon1; isPrimary = true; break;  // Q
    case 1: itemId = state.equipment.weapon1; isPrimary = false; break; // W
    case 2: itemId = state.equipment.weapon2; isPrimary = true; break;  // E
    case 3: itemId = state.equipment.weapon2; isPrimary = false; break; // R
    case 4: itemId = state.equipment.shield; isPrimary = true; break;   // D
    case 5: itemId = state.equipment.thruster; isPrimary = true; break;  // F
    default: return null;
  }

  if (!itemId) return null;

  const itemAbilities = ITEM_ABILITIES[itemId];
  if (!itemAbilities) return null;

  if (isPrimary) return itemAbilities.primary;
  return itemAbilities.secondary || null;
}

// Get the range for a given ability slot from equipment
export function getAbilityRange(state: GameState, slot: number): number {
  const info = getAbilityForSlot(state, slot);
  return info ? info.range : 0;
}

let barEl: HTMLElement | null = null;
let slotEls: HTMLElement[] = [];
let lastEquipHash = "";

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

  for (let i = 0; i < 6; i++) {
    const color = SLOT_COLORS[i];
    const el = document.createElement("div");
    el.style.cssText = `
      width: 50px;
      height: 50px;
      border: 2px solid ${color};
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
    keyLabel.className = "key-label";
    keyLabel.style.cssText = `font-size: 16px; font-weight: bold; color: ${color};`;
    keyLabel.textContent = SLOT_KEYS[i];
    el.appendChild(keyLabel);

    const nameLabel = document.createElement("div");
    nameLabel.className = "name-label";
    nameLabel.style.cssText = "font-size: 8px; color: #999; white-space: nowrap;";
    nameLabel.textContent = "";
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

    // Tooltip (rebuilt dynamically)
    const tooltip = document.createElement("div");
    tooltip.className = "ability-tooltip";
    tooltip.style.cssText = `
      position: absolute;
      bottom: 58px;
      left: 50%;
      transform: translateX(-50%);
      width: 160px;
      background: rgba(0, 0, 0, 0.9);
      border: 1px solid ${color};
      border-radius: 4px;
      padding: 8px;
      font-family: monospace;
      pointer-events: none;
      display: none;
      z-index: 200;
    `;
    el.appendChild(tooltip);

    el.addEventListener("mouseenter", () => {
      tooltip.style.display = "block";
    });
    el.addEventListener("mouseleave", () => {
      tooltip.style.display = "none";
    });

    barEl!.appendChild(el);
    slotEls.push(el);
  }

  document.body.appendChild(barEl);
}

function rebuildSlotContent(state: GameState): void {
  const equipHash = `${state.equipment.weapon1}-${state.equipment.weapon2}-${state.equipment.shield}-${state.equipment.thruster}`;
  if (equipHash === lastEquipHash) return;
  lastEquipHash = equipHash;

  for (let i = 0; i < 6; i++) {
    const el = slotEls[i];
    const color = SLOT_COLORS[i];
    const info = getAbilityForSlot(state, i);
    const nameLabel = el.querySelector(".name-label") as HTMLElement;
    const tooltip = el.querySelector(".ability-tooltip") as HTMLElement;

    if (info) {
      nameLabel.textContent = info.name;
      nameLabel.style.color = "#999";
      el.style.borderColor = info.color || color;
      el.style.opacity = "1";

      // Rebuild tooltip
      tooltip.innerHTML = "";
      const ttTitle = document.createElement("div");
      ttTitle.style.cssText = `font-size: 11px; font-weight: bold; color: ${info.color || color}; margin-bottom: 4px;`;
      ttTitle.textContent = `[${SLOT_KEYS[i]}] ${info.title}`;
      tooltip.appendChild(ttTitle);

      const ttDesc = document.createElement("div");
      ttDesc.style.cssText = "font-size: 9px; color: #bbb; margin-bottom: 6px; line-height: 1.3;";
      ttDesc.textContent = info.desc;
      tooltip.appendChild(ttDesc);

      for (const stat of info.stats) {
        const line = document.createElement("div");
        line.style.cssText = "font-size: 9px; color: #ddd;";
        line.textContent = stat;
        tooltip.appendChild(line);
      }
    } else {
      nameLabel.textContent = "Empty";
      nameLabel.style.color = "#555";
      el.style.borderColor = "#444";
      el.style.opacity = "0.5";

      tooltip.innerHTML = "";
      const ttEmpty = document.createElement("div");
      ttEmpty.style.cssText = "font-size: 9px; color: #666;";
      ttEmpty.textContent = "No equipment in this slot";
      tooltip.appendChild(ttEmpty);
    }
  }
}

export function updateAbilityBar(state: GameState): void {
  if (!barEl) return;
  barEl.style.display = state.isDead || !state.loggedIn ? "none" : "flex";

  rebuildSlotContent(state);

  // Calculate distance to lock target for range checks
  let distToTarget = Infinity;
  const myEnt = state.entities.get(state.myEntityId);
  const lockEnt = state.lockTargetId ? state.entities.get(state.lockTargetId) : null;
  if (myEnt && lockEnt) {
    const dx = lockEnt.renderX - myEnt.renderX;
    const dy = lockEnt.renderY - myEnt.renderY;
    distToTarget = Math.sqrt(dx * dx + dy * dy);
  }

  for (let i = 0; i < 6; i++) {
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

    // Show red overlay if ability has a range and target is out of range
    const range = getAbilityRange(state, i);
    if (range > 0) {
      const outOfRange = lockEnt && distToTarget > range;
      rangeOverlay.style.display = outOfRange ? "block" : "none";
    } else {
      rangeOverlay.style.display = "none";
    }
  }
}

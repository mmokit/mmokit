import type { GameState } from "../state";
import { needsRebuild } from "./memo";
import { getShip } from "../entity-accessors";

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

// Targeting mode mirrors the server-side AbilityType.Mode enum.
// Duplicated on client because the SDK only emits the item registry,
// not the underlying TargetingMode enum. Values MUST match the Go-side
// declaration exactly (Self=0, LockOn=1, SkillshotLine=2,
// SkillshotGround=3, SkillshotChannel=4) — out-of-sync values silently
// break the aim-state machine dispatch.
export const enum TargetingMode {
  Self = 0,
  LockOn = 1,
  SkillshotLine = 2,
  SkillshotGround = 3,
  SkillshotChannel = 4,
}

// Client-side ability data per equipment item ID
// Matches server item registry in internal/item/item.go
export interface AbilityInfo {
  name: string;
  title: string;
  desc: string;
  stats: string[];
  range: number;
  color?: string;
  mode: TargetingMode;
  splashRadius?: number;
  /**
   * Total channel duration in ms for SkillshotChannel abilities. Used by
   * input.ts to time out client-side ChannelAim streaming; server has its
   * own ChannelDuration that's authoritative — this is just a hint so the
   * client stops sending when its channel runs out.
   */
  channelDuration?: number;
}

export const ITEM_ABILITIES: Record<number, { primary: AbilityInfo; secondary?: AbilityInfo; passive?: string[] }> = {
  // Weapon 1 items (Q + W)
  100: { // Pulse Laser Array
    primary: {
      name: "Pulse", title: "Pulse Shot", range: 500,
      desc: "Rapid-fire energy pulse at the locked target.",
      stats: ["Damage: 15", "Range: 500", "Cooldown: 2s"],
      mode: TargetingMode.SkillshotLine,
    },
    secondary: {
      name: "Barrage", title: "Pulse Barrage", range: 400,
      desc: "Fires a concentrated burst of pulse energy.",
      stats: ["Damage: 25", "Range: 400", "Cooldown: 5s"],
      mode: TargetingMode.SkillshotLine,
    },
  },
  101: { // Railgun System
    primary: {
      name: "Rail", title: "Rail Shot", range: 1000,
      desc: "High-damage single shot. Long range, long cooldown.",
      stats: ["Damage: 35", "Range: 1000", "Cooldown: 6s"],
      mode: TargetingMode.SkillshotLine,
    },
    secondary: {
      name: "Pierce", title: "Piercing Round", range: 800,
      desc: "Armor-piercing round. Bonus damage vs unshielded.",
      stats: ["Damage: 50 (+20 no shield)", "Range: 800", "Cooldown: 10s"],
      mode: TargetingMode.SkillshotLine,
    },
  },
  // Weapon 2 items (E + R)
  105: { // Ion Array
    primary: {
      name: "Ion", title: "Ion Burn", range: 500,
      desc: "Applies a damage-over-time burn to the target.",
      stats: ["DPS: 6", "Duration: 4s", "Range: 500", "Cooldown: 8s"],
      mode: TargetingMode.LockOn,
    },
    secondary: {
      name: "Overld", title: "Ion Overload", range: 600,
      desc: "Releases a concentrated ion discharge.",
      stats: ["Damage: 40", "Range: 600", "Cooldown: 12s"],
      mode: TargetingMode.SkillshotLine,
    },
  },
  106: { // Plasma System
    primary: {
      name: "Plasma", title: "Plasma Bolt", range: 700,
      desc: "Versatile plasma projectile.",
      stats: ["Damage: 20", "Range: 700", "Cooldown: 4s"],
      mode: TargetingMode.SkillshotLine,
    },
    secondary: {
      name: "Torp", title: "Plasma Torpedo", range: 900,
      desc: "Slow but devastating. Bonus damage vs unshielded.",
      stats: ["Damage: 60 (+30 no shield)", "Range: 900", "Cooldown: 20s"],
      mode: TargetingMode.LockOn,
    },
  },
  // PVE v2 weapons (W2 slot)
  107: { // Plasma Cannon
    primary: {
      name: "Plasma", title: "Plasma Shot", range: 40,
      desc: "Fast travel-time projectile. Lead your target.",
      stats: ["Damage: 25", "Speed: 50/s", "Range: 40", "Cooldown: 1.5s"],
      mode: TargetingMode.SkillshotLine,
    },
    secondary: {
      name: "Missile", title: "Homing Missile", range: 50,
      desc: "Locks on and chases. Splashes on impact. Requires a target lock.",
      stats: ["Damage: 60", "Splash: 20 in 3u", "Speed: 30/s", "Range: 50", "Cooldown: 8s"],
      mode: TargetingMode.LockOn,
      splashRadius: 3,
    },
  },
  108: { // Beam-Mortar Battery
    primary: {
      name: "Beam", title: "Sustained Beam", range: 30,
      desc: "Channeled beam. Keep target inside the firing arc to keep dealing damage.",
      stats: ["Damage: 4/tick × 10/s", "Channel: 3s", "Range: 30", "Cooldown: 4s post-channel"],
      mode: TargetingMode.SkillshotChannel,
      channelDuration: 3000,
    },
    secondary: {
      name: "Mortar", title: "Mortar Shell", range: 40,
      desc: "Lobbed projectile with splash on impact. Great vs clustered enemies.",
      stats: ["Direct: 60", "Splash: 40 in 6u", "Speed: 40/s", "Range: 40", "Cooldown: 6s"],
      mode: TargetingMode.SkillshotGround,
      splashRadius: 6,
    },
  },
  // Shield items (D)
  110: { // Standard Shield Gen
    primary: {
      name: "Shield", title: "Emergency Shield", range: 0,
      desc: "Regenerates shield over time and reduces incoming damage.",
      stats: ["Heal: 7/s for 5s (35 total)", "Dmg Reduction: 30%", "Duration: 5s", "Cooldown: 15s"],
      mode: TargetingMode.Self,
    },
    passive: ["Shield: +50", "Regen: 1.7/s"],
  },
  111: { // Hardened Shield Gen
    primary: {
      name: "Harden", title: "Hardened Shield", range: 0,
      desc: "Strong shield regeneration with heavy damage reduction.",
      stats: ["Heal: 9.2/s for 6s (55 total)", "Dmg Reduction: 50%", "Duration: 6s", "Cooldown: 20s"],
      mode: TargetingMode.Self,
    },
    passive: ["Shield: +75", "Regen: 1.0/s"],
  },
  // Thruster items (F)
  120: { // Standard Thruster
    primary: {
      name: "Boost", title: "Afterburner", range: 0,
      desc: "Temporary speed boost for escape or pursuit.",
      stats: ["Speed: 2.5x", "Duration: 1.5s", "Cooldown: 10s"],
      mode: TargetingMode.Self,
    },
  },
  121: { // Micro Warp Drive
    primary: {
      name: "Warp", title: "Micro Warp", range: 0,
      desc: "Extreme speed burst for a very short time.",
      stats: ["Speed: 4.0x", "Duration: 0.8s", "Cooldown: 18s"],
      mode: TargetingMode.Self,
    },
    passive: ["Thrust: +100", "Max Speed: +200"],
  },
  // Mining Lasers (weapon slot)
  130: { // Mining Laser
    primary: {
      name: "Mine", title: "Mining Beam", range: 300,
      desc: "Toggle continuous mining beam on a locked asteroid.",
      stats: ["Rate: 5.0/s", "Range: 300", "No cooldown"],
      mode: TargetingMode.LockOn,
    },
    secondary: {
      name: "Pulse", title: "Extract Pulse", range: 300,
      desc: "Extract a bonus chunk of resources while mining.",
      stats: ["Yield: 15.0", "Range: 300", "Cooldown: 3s"],
      mode: TargetingMode.LockOn,
    },
  },
  131: { // Deep Core Mining Laser
    primary: {
      name: "Mine", title: "Mining Beam", range: 400,
      desc: "Toggle continuous mining beam on a locked asteroid.",
      stats: ["Rate: 8.0/s", "Range: 400", "No cooldown"],
      mode: TargetingMode.LockOn,
    },
    secondary: {
      name: "Pulse", title: "Extract Pulse", range: 400,
      desc: "Extract a large bonus chunk of resources while mining.",
      stats: ["Yield: 20.0", "Range: 400", "Cooldown: 2.5s"],
      mode: TargetingMode.LockOn,
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

// abilityParamsForSlot is the public accessor used by input.ts to drive
// the aim-state machine. Mirrors the private getAbilityForSlot but is
// exported so input dispatch logic can branch on .mode and .splashRadius
// without reaching into the internal slot lookup.
export function abilityParamsForSlot(state: GameState, slot: number): AbilityInfo | null {
  return getAbilityForSlot(state, slot);
}

// Mining laser item IDs that have Extract Pulse as secondary
const MINING_LASER_IDS = new Set([130, 131]);

// Check if an ability slot is an Extract Pulse (secondary of a mining laser)
function isExtractPulseSlot(state: GameState, slot: number): boolean {
  if (slot === 1) return MINING_LASER_IDS.has(state.equipment.weapon1); // W = weapon1 secondary
  if (slot === 3) return MINING_LASER_IDS.has(state.equipment.weapon2); // R = weapon2 secondary
  return false;
}

function isMiningBeamActive(state: GameState, slot: number): boolean {
  const myEnt = state.myEntityId ? state.entities.get(state.myEntityId) : undefined;
  if (!myEnt) return false;
  const e = myEnt.current as { beam0Active?: boolean; beam1Active?: boolean };
  // Slot 1 = W key (weapon1 secondary) → beam0, slot 3 = R key (weapon2 secondary) → beam1.
  if (slot === 1) return !!e.beam0Active;
  if (slot === 3) return !!e.beam1Active;
  return false;
}

let barEl: HTMLElement | null = null;
let slotEls: HTMLElement[] = [];

export function createAbilityBar(): void {
  barEl = document.createElement("div");
  barEl.id = "ability-bar";
  // Starts hidden: the main ticker early-returns when !state.loggedIn,
  // so updateAbilityBar never runs before login and can't flip this on
  // its own. Defaulting to display:none keeps the bar off the login
  // screen; updateAbilityBar switches it to flex once logged in.
  barEl.style.cssText = `
    position: fixed;
    bottom: 16px;
    left: 50%;
    transform: translateX(-50%);
    display: none;
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
  if (!needsRebuild("ability-equip", state.equipment.weapon1, state.equipment.weapon2, state.equipment.shield, state.equipment.thruster)) return;

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

  // Calculate distance to selected target for range checks
  let distToTarget = Infinity;
  const myEnt = state.entities.get(state.myEntityId);
  const lockEnt = state.selectedNetID ? state.entities.get(state.selectedNetID) : null;
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

    // Show red overlay if ability has a range and target is out of range,
    // or if this is an Extract Pulse and the mining beam isn't active
    const range = getAbilityRange(state, i);
    const outOfRange = range > 0 && lockEnt && distToTarget > range;
    const extractBlocked = isExtractPulseSlot(state, i) && !isMiningBeamActive(state, i);
    if (outOfRange || extractBlocked) {
      rangeOverlay.style.display = "block";
    } else {
      rangeOverlay.style.display = "none";
    }
  }
}

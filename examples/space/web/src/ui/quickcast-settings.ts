// Quickcast settings — per-slot toggle for "smart cast" behavior.
//
// When a slot's bit is set in state.quickcastMask, pressing the key fires
// the ability immediately at the current cursor (skipping the aim-confirm
// state). This is a UX preference for muscle-memory on commonly-used spells.
//
// Mask is persisted to localStorage under "skillshot.quickcast" as a
// stringified integer; loaded by createInitialState in state.ts.

import type { GameState } from "../state";

const SLOT_KEYS = ["Q", "W", "E", "R", "D", "F"];
const STORAGE_KEY = "skillshot.quickcast";

export function buildQuickcastSettings(state: GameState): HTMLElement {
  const root = document.createElement("div");
  root.className = "quickcast-row";

  const label = document.createElement("label");
  label.className = "quickcast-label";
  label.textContent = "Quickcast";
  root.appendChild(label);

  const slots = document.createElement("div");
  slots.className = "quickcast-slots";

  for (let i = 0; i < 6; i++) {
    const slotLabel = document.createElement("label");
    slotLabel.className = "quickcast-slot";

    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.checked = (state.quickcastMask & (1 << i)) !== 0;
    checkbox.addEventListener("change", () => {
      if (checkbox.checked) {
        state.quickcastMask |= 1 << i;
      } else {
        state.quickcastMask &= ~(1 << i);
      }
      localStorage.setItem(STORAGE_KEY, String(state.quickcastMask));
    });

    slotLabel.appendChild(checkbox);
    const keyText = document.createElement("span");
    keyText.textContent = SLOT_KEYS[i];
    slotLabel.appendChild(keyText);
    slots.appendChild(slotLabel);
  }

  root.appendChild(slots);
  return root;
}

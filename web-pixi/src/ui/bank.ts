import { ITEM_COLORS_CSS, DEFAULT_ITEM_COLOR } from "../constants";

import { SETTLEMENT_CURRENCY_ID, type GameState } from "../state";
import { needsRebuild, invalidate } from "./memo";

let delegationSetup = false;
let currentState: GameState | null = null;
let wasBankOpen = false;

function setupDelegation(): void {
  if (delegationSetup) return;
  delegationSetup = true;

  const rowsEl = document.getElementById("bank-rows")!;

  rowsEl.addEventListener("mousedown", (e) => {
    const btn = (e.target as HTMLElement).closest(".bank-btn") as HTMLElement | null;
    if (!btn || !currentState?.connected || !currentState.client) return;
    e.stopPropagation();
    const itemId = Number(btn.dataset.itemId);
    if (!itemId) return;

    if (btn.classList.contains("bank-sell-btn")) {
      const bankQty = currentState.bankItems.get(itemId) ?? 0;
      if (bankQty <= 0) return;
      const sellQty = e.shiftKey ? Math.floor(bankQty / 2) : 0; // 0 = all
      currentState.client.sendSellBankItem({ itemId, quantity: sellQty });
    } else {
      // Smart transfer: cargo>0 → deposit; else withdraw from bank.
      const cargoQty = currentState.dockedCargoItems.get(itemId) ?? 0;
      const bankQty = currentState.bankItems.get(itemId) ?? 0;
      let deposit: boolean;
      let qty: number;
      if (cargoQty > 0) {
        deposit = true;
        qty = e.shiftKey ? Math.floor(cargoQty / 2) : 0;
      } else if (bankQty > 0) {
        deposit = false;
        qty = e.shiftKey ? Math.floor(bankQty / 2) : 0;
      } else {
        return;
      }
      currentState.client.sendInventoryTransfer({ itemId, quantity: qty, deposit });
    }
    setTimeout(() => {
      if (currentState?.connected && currentState.client && currentState.bankPanelOpen) {
        currentState.client.sendBankRequest({});
      }
    }, 100);
  });

  const closeBtn = document.getElementById("bank-close-btn");
  if (closeBtn) {
    closeBtn.addEventListener("mousedown", (e) => {
      e.stopPropagation();
      if (currentState) currentState.bankPanelOpen = false;
    });
  }
}

/**
 * Reparent #equip-slots so it lives in the bank panel while docked and the
 * cargo-panel sidebar otherwise. Same DOM node, same drag-listeners — just
 * a different container.
 */
export function syncEquipSlotsParent(isDocked: boolean): void {
  const slots = document.getElementById("equip-slots");
  if (!slots) return;
  const targetId = isDocked ? "bank-equip-host" : "cargo-equip-host";
  const target = document.getElementById(targetId);
  if (!target || slots.parentElement === target) return;
  target.appendChild(slots);
}

export function updateBankPanel(state: GameState): void {
  const panelEl = document.getElementById("bank-panel")!;
  if (!panelEl) return;

  currentState = state;
  setupDelegation();

  if (!state.bankPanelOpen) {
    panelEl.style.display = "none";
    wasBankOpen = false;
    return;
  }
  panelEl.style.display = "flex";

  if (!wasBankOpen) {
    wasBankOpen = true;
    invalidate("bank-rows");
  }

  const rowsEl = document.getElementById("bank-rows")!;
  if (needsRebuild("bank-rows", state.bankItems, state.dockedCargoItems)) {
    rowsEl.innerHTML = "";

    // Union of itemIds across bank + cargo, dropping any zero-on-both rows.
    const ids = new Set<number>();
    for (const [id, qty] of state.bankItems) if (qty > 0) ids.add(id);
    for (const [id, qty] of state.dockedCargoItems) if (qty > 0) ids.add(id);
    const sorted = [...ids].sort((a, b) => a - b);

    if (sorted.length === 0) {
      const empty = document.createElement("tr");
      empty.className = "bank-empty-row";
      const td = document.createElement("td");
      td.colSpan = 4;
      td.textContent = "No items in storage or cargo";
      empty.appendChild(td);
      rowsEl.appendChild(empty);
    } else {
      for (const itemId of sorted) {
        const def = state.itemDefs.get(itemId);
        const name = def ? def.name : `Item #${itemId}`;
        const color = ITEM_COLORS_CSS[itemId] || DEFAULT_ITEM_COLOR;
        const bankQty = Math.floor(state.bankItems.get(itemId) ?? 0);
        const cargoQty = Math.floor(state.dockedCargoItems.get(itemId) ?? 0);

        const row = document.createElement("tr");
        row.className = "bank-row";
        row.dataset.itemId = String(itemId);
        // Tag rows whose item is equippable AND present in cargo so the
        // hud.ts drag-init listener can start an equip-by-drag from this
        // row. Equipping while docked pulls from cargo (the bank ↔ button
        // is the path for moving stock between bank and cargo).
        if (def && def.equipSlot > 0 && cargoQty > 0) {
          row.dataset.equipSlot = String(def.equipSlot);
        }

        const nameTd = document.createElement("td");
        nameTd.style.color = color;
        nameTd.textContent = name;
        row.appendChild(nameTd);

        const bankTd = document.createElement("td");
        bankTd.className = "num" + (bankQty === 0 ? " empty" : "");
        bankTd.textContent = bankQty === 0 ? "—" : String(bankQty);
        row.appendChild(bankTd);

        const cargoTd = document.createElement("td");
        cargoTd.className = "num" + (cargoQty === 0 ? " empty" : "");
        cargoTd.textContent = cargoQty === 0 ? "—" : String(cargoQty);
        row.appendChild(cargoTd);

        const actTd = document.createElement("td");
        actTd.className = "actions";

        const xferBtn = document.createElement("button");
        xferBtn.className = "bank-btn";
        xferBtn.textContent = "↔";
        xferBtn.title = cargoQty > 0
          ? "Deposit to bank (Shift = half)"
          : "Withdraw to cargo (Shift = half)";
        xferBtn.dataset.itemId = String(itemId);
        actTd.appendChild(xferBtn);

        if (def && def.sellPrice > 0 && bankQty > 0) {
          const sellBtn = document.createElement("button");
          sellBtn.className = "bank-btn bank-sell-btn";
          sellBtn.textContent = "$";
          sellBtn.title = `Sell from bank @ ${Math.floor(def.sellPrice)} (Shift = half)`;
          sellBtn.dataset.itemId = String(itemId);
          actTd.appendChild(sellBtn);
        }

        row.appendChild(actTd);
        rowsEl.appendChild(row);
      }
    }
  }

  const bankFlux = state.currencyBalances[SETTLEMENT_CURRENCY_ID] ?? 0;
  const footerEl = document.getElementById("bank-footer")!;
  const massText = state.bankMaxMass > 0
    ? `${Math.floor(state.bankTotalMass)} / ${Math.floor(state.bankMaxMass)} mass`
    : `${Math.floor(state.bankTotalMass)} mass`;
  const curName = state.itemDefs.get(SETTLEMENT_CURRENCY_ID)?.name ?? "Currency";
  footerEl.textContent = `${massText} | ${curName}: ${Math.floor(bankFlux)} | Click: All • Shift+Click: Half`;
}

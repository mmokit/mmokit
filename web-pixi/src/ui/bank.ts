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

    // The bank table lists items actually IN THE BANK. The CARGO column is
    // a context hint ("how much of this thing is also on your ship"); it
    // does not add rows. Items that are only in cargo show up in the
    // cargo-panel (left), not here.
    const sorted = [...state.bankItems.entries()]
      .filter(([, qty]) => qty > 0)
      .map(([id]) => id)
      .sort((a, b) => a - b);

    if (sorted.length === 0) {
      const empty = document.createElement("tr");
      empty.className = "bank-empty-row";
      const td = document.createElement("td");
      td.colSpan = 4;
      td.textContent = "Bank is empty";
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

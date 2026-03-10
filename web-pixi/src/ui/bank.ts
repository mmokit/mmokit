import { ITEM_COLORS_CSS, DEFAULT_ITEM_COLOR } from "../constants";
import { encodeTransferRequest, encodeBankRequest, encodeSellBankItem } from "../protocol";
import type { GameState } from "../state";

let delegationSetup = false;
let currentState: GameState | null = null;

function setupDelegation(): void {
  if (delegationSetup) return;
  delegationSetup = true;

  const bankRowsEl = document.getElementById("bank-rows")!;
  const depositRowsEl = document.getElementById("deposit-rows")!;

  // Use mousedown instead of click — the DOM is rebuilt every frame,
  // so buttons are destroyed before mouseup/click can fire.
  bankRowsEl.addEventListener("mousedown", (e) => {
    const btn = (e.target as HTMLElement).closest(".bank-btn") as HTMLElement | null;
    if (!btn || !currentState?.connected || !currentState.ws) return;
    e.stopPropagation();
    const itemId = Number(btn.dataset.itemId);
    const qty = Number(btn.dataset.qty);

    if (btn.classList.contains("bank-sell-btn")) {
      // Sell: shift = half, otherwise all
      const sellQty = e.shiftKey ? Math.floor(qty / 2) : 0;
      currentState.ws.sendReliable(encodeSellBankItem(itemId, sellQty));
    } else {
      // Withdraw
      const transferQty = e.shiftKey ? Math.floor(qty / 2) : 0;
      currentState.ws.sendReliable(encodeTransferRequest(itemId, transferQty, false));
    }
    setTimeout(() => {
      if (currentState?.connected && currentState.ws && currentState.bankPanelOpen) {
        currentState.ws.sendReliable(encodeBankRequest());
      }
    }, 100);
  });

  depositRowsEl.addEventListener("mousedown", (e) => {
    const btn = (e.target as HTMLElement).closest(".bank-btn") as HTMLElement | null;
    if (!btn || !currentState?.connected || !currentState.ws) return;
    e.stopPropagation();
    const itemId = Number(btn.dataset.itemId);
    const qty = Number(btn.dataset.qty);
    const transferQty = e.shiftKey ? Math.floor(qty / 2) : 0;
    currentState.ws.sendReliable(encodeTransferRequest(itemId, transferQty, true));
    setTimeout(() => {
      if (currentState?.connected && currentState.ws && currentState.bankPanelOpen) {
        currentState.ws.sendReliable(encodeBankRequest());
      }
    }, 100);
  });
}

export function updateBankPanel(state: GameState): void {
  const el = document.getElementById("bank-panel")!;
  if (!el) return;

  currentState = state;
  setupDelegation();

  if (!state.bankPanelOpen) {
    el.style.display = "none";
    return;
  }
  el.style.display = "block";

  const myEntity = state.entities.get(state.myEntityId);
  if (!myEntity) {
    el.style.display = "none";
    return;
  }

  // Build bank section
  const bankRowsEl = document.getElementById("bank-rows")!;
  bankRowsEl.innerHTML = "";

  if (state.bankItems.size === 0) {
    const emptyRow = document.createElement("div");
    emptyRow.className = "bank-row";
    emptyRow.style.justifyContent = "center";
    const label = document.createElement("span");
    label.style.color = "#666";
    label.style.fontSize = "13px";
    label.textContent = "Bank is empty";
    emptyRow.appendChild(label);
    bankRowsEl.appendChild(emptyRow);
  } else {
    const sorted = [...state.bankItems.entries()].sort((a, b) => a[0] - b[0]);
    for (const [itemId, qty] of sorted) {
      const def = state.itemDefs.get(itemId);
      const name = def ? def.name : `Item #${itemId}`;
      const color = ITEM_COLORS_CSS[itemId] || DEFAULT_ITEM_COLOR;

      const row = document.createElement("div");
      row.className = "bank-row";

      const label = document.createElement("span");
      label.className = "bank-item-name";
      label.style.color = color;
      label.textContent = name;
      row.appendChild(label);

      const amount = document.createElement("span");
      amount.className = "bank-item-qty";
      amount.textContent = Math.floor(qty).toString();
      row.appendChild(amount);

      const withdrawBtn = document.createElement("button");
      withdrawBtn.className = "bank-btn";
      withdrawBtn.textContent = "Withdraw";
      withdrawBtn.dataset.itemId = itemId.toString();
      withdrawBtn.dataset.qty = qty.toString();
      row.appendChild(withdrawBtn);

      // Add sell button for sellable items (not FLUX itself)
      if (def && def.sellPrice > 0) {
        const sellBtn = document.createElement("button");
        sellBtn.className = "bank-btn bank-sell-btn";
        sellBtn.textContent = "Sell";
        sellBtn.dataset.itemId = itemId.toString();
        sellBtn.dataset.qty = qty.toString();
        sellBtn.style.borderColor = "rgba(200,180,50,0.5)";
        sellBtn.style.color = "#dd4";
        sellBtn.style.background = "rgba(200,180,50,0.2)";
        row.appendChild(sellBtn);
      }

      bankRowsEl.appendChild(row);
    }
  }

  // Build cargo section (for depositing)
  const depositRowsEl = document.getElementById("deposit-rows")!;
  depositRowsEl.innerHTML = "";

  const cargoItems = myEntity.curr.cargoItems;
  if (!cargoItems || cargoItems.length === 0) {
    const emptyRow = document.createElement("div");
    emptyRow.className = "bank-row";
    emptyRow.style.justifyContent = "center";
    const label = document.createElement("span");
    label.style.color = "#666";
    label.style.fontSize = "13px";
    label.textContent = "Cargo is empty";
    emptyRow.appendChild(label);
    depositRowsEl.appendChild(emptyRow);
  } else {
    const sorted = [...cargoItems].sort((a, b) => a.itemId - b.itemId);
    for (const item of sorted) {
      const def = state.itemDefs.get(item.itemId);
      const name = def ? def.name : `Item #${item.itemId}`;
      const color = ITEM_COLORS_CSS[item.itemId] || DEFAULT_ITEM_COLOR;

      const row = document.createElement("div");
      row.className = "bank-row";

      const label = document.createElement("span");
      label.className = "bank-item-name";
      label.style.color = color;
      label.textContent = name;
      row.appendChild(label);

      const amount = document.createElement("span");
      amount.className = "bank-item-qty";
      amount.textContent = Math.floor(item.quantity).toString();
      row.appendChild(amount);

      const depositBtn = document.createElement("button");
      depositBtn.className = "bank-btn";
      depositBtn.textContent = "Deposit";
      depositBtn.dataset.itemId = item.itemId.toString();
      depositBtn.dataset.qty = item.quantity.toString();
      row.appendChild(depositBtn);

      depositRowsEl.appendChild(row);
    }
  }

  // Update bank footer
  const bankFooterEl = document.getElementById("bank-footer")!;
  const massText = state.bankMaxMass > 0
    ? `${Math.floor(state.bankTotalMass)} / ${Math.floor(state.bankMaxMass)} mass`
    : `${Math.floor(state.bankTotalMass)} mass`;
  bankFooterEl.textContent = `${massText}  |  Click: Transfer All  |  Shift+Click: Half`;
}

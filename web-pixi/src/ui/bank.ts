import { ITEM_COLORS_CSS, DEFAULT_ITEM_COLOR } from "../constants";
import { encodeTransferRequest, encodeBankRequest, encodeSellBankItem, encodeShopBuy } from "../protocol";
import type { GameState } from "../state";
import { needsRebuild, invalidate } from "./memo";

let delegationSetup = false;
let currentState: GameState | null = null;
let wasBankOpen = false;

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
      { const p = encodeSellBankItem(itemId, sellQty); currentState.ws.sendEvent(p.code, p.data); }
    } else {
      // Withdraw
      const transferQty = e.shiftKey ? Math.floor(qty / 2) : 0;
      { const p = encodeTransferRequest(itemId, transferQty, false); currentState.ws.sendEvent(p.code, p.data); }
    }
    setTimeout(() => {
      if (currentState?.connected && currentState.ws && currentState.bankPanelOpen) {
        { const p = encodeBankRequest(); currentState.ws.sendEvent(p.code, p.data); }
      }
    }, 100);
  });

  const shopRowsEl = document.getElementById("shop-rows")!;
  shopRowsEl.addEventListener("mousedown", (e) => {
    const btn = (e.target as HTMLElement).closest(".bank-btn") as HTMLElement | null;
    if (!btn || !currentState?.connected || !currentState.ws) return;
    e.stopPropagation();
    const itemId = Number(btn.dataset.itemId);
    { const p = encodeShopBuy(itemId, 1); currentState.ws.sendEvent(p.code, p.data); }
    setTimeout(() => {
      if (currentState?.connected && currentState.ws && currentState.bankPanelOpen) {
        { const p = encodeBankRequest(); currentState.ws.sendEvent(p.code, p.data); }
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
    { const p = encodeTransferRequest(itemId, transferQty, true); currentState.ws.sendEvent(p.code, p.data); }
    setTimeout(() => {
      if (currentState?.connected && currentState.ws && currentState.bankPanelOpen) {
        { const p = encodeBankRequest(); currentState.ws.sendEvent(p.code, p.data); }
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
    wasBankOpen = false;
    return;
  }
  el.style.display = "block";

  // Invalidate all bank hashes when panel first opens so we get fresh content
  if (!wasBankOpen) {
    wasBankOpen = true;
    invalidate("bank-rows");
    invalidate("bank-deposit");
    invalidate("bank-shop");
  }

  const myEntity = state.entities.get(state.myEntityId);
  // When docked, there's no entity — that's fine, we use PlayerDB data
  if (!myEntity && !state.isDocked) {
    el.style.display = "none";
    return;
  }

  // Build bank section (memoized)
  const bankRowsEl = document.getElementById("bank-rows")!;
  if (needsRebuild("bank-rows", state.bankItems)) {
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
  } // end bank-rows memoize

  // Build cargo section (for depositing, memoized)
  // When docked, use cargo data from BankContentsMsg; otherwise from entity
  let cargoItems: Array<{itemId: number; quantity: number}>;
  if (state.isDocked) {
    cargoItems = [...state.dockedCargoItems.entries()].map(([itemId, quantity]) => ({ itemId, quantity }));
  } else {
    cargoItems = myEntity ? myEntity.curr.cargoItems : [];
  }
  const depositRowsEl = document.getElementById("deposit-rows")!;
  if (needsRebuild("bank-deposit", cargoItems, state.dockedCargoItems)) {
  depositRowsEl.innerHTML = "";

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
  } // end bank-deposit memoize

  // Build shop section (buyable equipment items, memoized)
  const bankFlux = state.fluxBalance;
  const shopRowsEl = document.getElementById("shop-rows")!;
  if (needsRebuild("bank-shop", bankFlux)) {
  shopRowsEl.innerHTML = "";
  const shopItems = [...state.itemDefs.values()].filter(d => d.buyPrice > 0).sort((a, b) => a.id - b.id);

  if (shopItems.length === 0) {
    const emptyRow = document.createElement("div");
    emptyRow.className = "bank-row";
    emptyRow.style.justifyContent = "center";
    const label = document.createElement("span");
    label.style.color = "#666";
    label.style.fontSize = "13px";
    label.textContent = "No items for sale";
    emptyRow.appendChild(label);
    shopRowsEl.appendChild(emptyRow);
  } else {
    for (const def of shopItems) {
      const color = ITEM_COLORS_CSS[def.id] || DEFAULT_ITEM_COLOR;

      const row = document.createElement("div");
      row.className = "bank-row";

      const label = document.createElement("span");
      label.className = "bank-item-name";
      label.style.color = color;
      label.textContent = def.name;
      row.appendChild(label);

      const price = document.createElement("span");
      price.className = "bank-item-qty";
      price.style.color = bankFlux >= def.buyPrice ? "#4f8" : "#f44";
      price.textContent = `${Math.floor(def.buyPrice)} FLUX`;
      row.appendChild(price);

      const buyBtn = document.createElement("button");
      buyBtn.className = "bank-btn";
      buyBtn.textContent = "Buy";
      buyBtn.dataset.itemId = def.id.toString();
      buyBtn.style.borderColor = "rgba(68,170,255,0.5)";
      buyBtn.style.color = "#4af";
      buyBtn.style.background = "rgba(68,170,255,0.2)";
      row.appendChild(buyBtn);

      shopRowsEl.appendChild(row);
    }
  }
  } // end bank-shop memoize

  // Update bank footer
  const bankFooterEl = document.getElementById("bank-footer")!;
  const massText = state.bankMaxMass > 0
    ? `${Math.floor(state.bankTotalMass)} / ${Math.floor(state.bankMaxMass)} mass`
    : `${Math.floor(state.bankTotalMass)} mass`;
  bankFooterEl.textContent = `${massText}  |  FLUX: ${Math.floor(bankFlux)}  |  Click: Transfer All  |  Shift+Click: Half`;
}

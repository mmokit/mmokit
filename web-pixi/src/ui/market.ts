import {
  encodeMarketBrowse,
  encodeMarketCreateOrder,
  encodeMarketCancelOrder,
  encodeMarketMyOrders,
  encodeMarketInstantTrade,
  encodeBankRequest,
} from "../protocol";
import { ITEM_COLORS_CSS, DEFAULT_ITEM_COLOR } from "../constants";
import type { GameState } from "../state";

const FLUX_ITEM_ID = 1;

let panelEl: HTMLElement | null = null;
let currentState: GameState | null = null;
let pollInterval: ReturnType<typeof setInterval> | null = null;
let built = false;

// Persistent DOM refs
let headerEl: HTMLElement;
let bodyEl: HTMLElement;
let browseTabBtn: HTMLElement;
let sellTabBtn: HTMLElement;
let myOrdersTabBtn: HTMLElement;

// Browse tab elements
let browseItemColEl: HTMLElement;
let browseSearchInputEl: HTMLInputElement;
let browseItemListEl: HTMLElement;
let browseBookColEl: HTMLElement;
let browseFormColEl: HTMLElement;
let buyPriceInputEl: HTMLInputElement;
let buyQtyInputEl: HTMLInputElement;
let buyTotalLabelEl: HTMLElement;
let buyAvailLabelEl: HTMLElement;

// Sell tab elements
let sellBankColEl: HTMLElement;
let sellBankListEl: HTMLElement;
let sellBookColEl: HTMLElement;
let sellFormColEl: HTMLElement;
let sellPriceInputEl: HTMLInputElement;
let sellQtyInputEl: HTMLInputElement;
let sellTotalLabelEl: HTMLElement;
let sellAvailLabelEl: HTMLElement;

// My orders elements
let myOrdersColEl: HTMLElement;

// Change tracking — browse tab
let lastTab = "";
let lastBrowseListSelectedId = 0;
let lastBrowseBookSelectedId = 0;
let lastBrowseFormSelectedId = 0;
let lastBrowseOrderBookJson = "";
let lastBrowseItemDefsSize = 0;
let lastBrowseSearchQuery = "";
let lastBrowseBankJson = "";

// Change tracking — sell tab
let lastSellBankJson = "";
let lastSellSelectedId = 0;
let lastSellBookSelectedId = 0;
let lastSellOrderBookJson = "";
let lastSellFormSelectedId = 0;
let lastSellFormBankJson = "";
let lastSellAutoSuggestItemId = 0;

// Change tracking — my orders tab
let lastMyOrdersJson = "";

function nextRequestId(state: GameState): number {
  state.marketRequestCounter++;
  state.marketPendingRequestId = state.marketRequestCounter;
  return state.marketPendingRequestId;
}

function sendOp(state: GameState, encoded: { code: number; data: Uint8Array }): void {
  if (!state.ws || !state.connected) return;
  state.ws.sendOperation(encoded.code, nextRequestId(state), encoded.data);
}

export function createMarketPanel(): void {
  panelEl = document.getElementById("marketplace-panel");
  if (!panelEl) return;
  panelEl.style.display = "none";
}

function buildStructure(): void {
  if (built || !panelEl) return;
  built = true;

  // Header
  headerEl = el("div", "market-header");
  const title = el("span", "market-title");
  title.textContent = "MARKETPLACE";
  headerEl.appendChild(title);

  const tabs = el("div", "market-tabs");
  browseTabBtn = el("span", "market-tab active");
  browseTabBtn.textContent = "Browse";
  tabs.appendChild(browseTabBtn);
  sellTabBtn = el("span", "market-tab");
  sellTabBtn.textContent = "Sell";
  tabs.appendChild(sellTabBtn);
  myOrdersTabBtn = el("span", "market-tab");
  myOrdersTabBtn.textContent = "My Orders";
  tabs.appendChild(myOrdersTabBtn);
  headerEl.appendChild(tabs);

  const closeBtn = el("span", "market-close-btn");
  closeBtn.textContent = "X";
  headerEl.appendChild(closeBtn);
  panelEl.appendChild(headerEl);

  // Body
  bodyEl = el("div", "market-body");

  // === Browse tab columns ===
  browseItemColEl = el("div", "market-col market-items");
  browseSearchInputEl = document.createElement("input");
  browseSearchInputEl.type = "text";
  browseSearchInputEl.className = "market-search-input";
  browseSearchInputEl.placeholder = "Search items...";
  browseItemColEl.appendChild(browseSearchInputEl);
  browseItemListEl = el("div", "market-item-list");
  browseItemColEl.appendChild(browseItemListEl);

  browseBookColEl = el("div", "market-col market-book");
  browseFormColEl = el("div", "market-col market-form");

  buyPriceInputEl = document.createElement("input");
  buyPriceInputEl.type = "number";
  buyPriceInputEl.className = "market-input";
  buyPriceInputEl.placeholder = "0";
  buyPriceInputEl.step = "1";
  buyPriceInputEl.min = "1";

  buyQtyInputEl = document.createElement("input");
  buyQtyInputEl.type = "number";
  buyQtyInputEl.className = "market-input";
  buyQtyInputEl.placeholder = "0";
  buyQtyInputEl.step = "1";
  buyQtyInputEl.min = "1";

  // === Sell tab columns ===
  sellBankColEl = el("div", "market-col market-items");
  const sellColHeader = el("div", "market-col-title");
  sellColHeader.textContent = "YOUR BANK";
  sellBankColEl.appendChild(sellColHeader);
  sellBankListEl = el("div", "market-item-list");
  sellBankColEl.appendChild(sellBankListEl);

  sellBookColEl = el("div", "market-col market-book");
  sellFormColEl = el("div", "market-col market-form");

  sellPriceInputEl = document.createElement("input");
  sellPriceInputEl.type = "number";
  sellPriceInputEl.className = "market-input";
  sellPriceInputEl.placeholder = "0";
  sellPriceInputEl.step = "1";
  sellPriceInputEl.min = "1";

  sellQtyInputEl = document.createElement("input");
  sellQtyInputEl.type = "number";
  sellQtyInputEl.className = "market-input";
  sellQtyInputEl.placeholder = "0";
  sellQtyInputEl.step = "1";
  sellQtyInputEl.min = "1";

  // === My orders column ===
  myOrdersColEl = el("div", "market-col market-my-orders-col");

  // Start with browse tab visible
  bodyEl.appendChild(browseItemColEl);
  bodyEl.appendChild(browseBookColEl);
  bodyEl.appendChild(browseFormColEl);
  panelEl.appendChild(bodyEl);

  // === Event delegation ===
  panelEl.addEventListener("mousedown", (e) => {
    const target = e.target as HTMLElement;
    if (!currentState?.ws || !currentState.connected) return;
    if (target instanceof HTMLInputElement) return;

    if (target.classList.contains("market-close-btn")) {
      currentState.marketPanelOpen = false;
      return;
    }

    // Tab switching
    if (target === browseTabBtn) {
      currentState.marketTab = "browse";
      return;
    }
    if (target === sellTabBtn) {
      currentState.marketTab = "sell";
      return;
    }
    if (target === myOrdersTabBtn) {
      currentState.marketTab = "myorders";
      sendOp(currentState, encodeMarketMyOrders());
      return;
    }

    // Browse tab: item selection
    if (target.dataset.browseItemId) {
      const id = Number(target.dataset.browseItemId);
      if (id === currentState.marketSelectedItemId) return;
      currentState.marketSelectedItemId = id;
      currentState.marketOrderBook = null;
      sendOp(currentState, encodeMarketBrowse(id));
      return;
    }

    // Sell tab: bank item selection
    if (target.dataset.sellItemId) {
      const id = Number(target.dataset.sellItemId);
      if (id === currentState.marketSellSelectedItemId) return;
      currentState.marketSellSelectedItemId = id;
      currentState.marketOrderBook = null;
      lastSellAutoSuggestItemId = 0;
      sellPriceInputEl.value = "";
      sellQtyInputEl.value = "1";
      sendOp(currentState, encodeMarketBrowse(id));
      return;
    }

    // Sell tab: MAX qty button
    if (target.classList.contains("market-sell-max-btn")) {
      const bankQty = currentState.bankItems.get(currentState.marketSellSelectedItemId) || 0;
      sellQtyInputEl.value = Math.floor(bankQty).toString();
      return;
    }

    // Browse: create buy order
    if (target.classList.contains("market-buy-create-btn")) {
      const price = Math.floor(parseFloat(buyPriceInputEl.value));
      const qty = Math.floor(parseFloat(buyQtyInputEl.value));
      if (isNaN(price) || isNaN(qty) || price <= 0 || qty <= 0) return;
      buyPriceInputEl.value = "";
      buyQtyInputEl.value = "";
      currentState.marketOrderFormPrice = "";
      currentState.marketOrderFormQty = "";
      sendOp(currentState, encodeMarketCreateOrder(
        currentState.marketSelectedItemId, true, price, qty,
      ));
      refreshAfterAction(currentState, currentState.marketSelectedItemId);
      return;
    }

    // Browse: instant buy
    if (target.classList.contains("market-buy-instant-btn")) {
      const qty = Math.floor(parseFloat(buyQtyInputEl.value));
      if (isNaN(qty) || qty <= 0) return;
      buyQtyInputEl.value = "";
      currentState.marketOrderFormQty = "";
      sendOp(currentState, encodeMarketInstantTrade(
        currentState.marketSelectedItemId, true, qty,
      ));
      refreshAfterAction(currentState, currentState.marketSelectedItemId);
      return;
    }

    // Sell: create sell order
    if (target.classList.contains("market-sell-create-btn")) {
      const price = Math.floor(parseFloat(sellPriceInputEl.value));
      const qty = Math.floor(parseFloat(sellQtyInputEl.value));
      if (isNaN(price) || isNaN(qty) || price <= 0 || qty <= 0) return;
      sellPriceInputEl.value = "";
      sellQtyInputEl.value = "1";
      sendOp(currentState, encodeMarketCreateOrder(
        currentState.marketSellSelectedItemId, false, price, qty,
      ));
      refreshAfterAction(currentState, currentState.marketSellSelectedItemId);
      return;
    }

    // Sell: instant sell
    if (target.classList.contains("market-sell-instant-btn")) {
      const qty = Math.floor(parseFloat(sellQtyInputEl.value));
      if (isNaN(qty) || qty <= 0) return;
      sellQtyInputEl.value = "1";
      sendOp(currentState, encodeMarketInstantTrade(
        currentState.marketSellSelectedItemId, false, qty,
      ));
      refreshAfterAction(currentState, currentState.marketSellSelectedItemId);
      return;
    }

    // My orders: cancel
    if (target.dataset.cancelOrderId) {
      sendOp(currentState, encodeMarketCancelOrder(BigInt(target.dataset.cancelOrderId)));
      setTimeout(() => {
        if (currentState) {
          sendOp(currentState, encodeMarketMyOrders());
          const bankReq = encodeBankRequest();
          currentState.ws?.sendEvent(bankReq.code, bankReq.data);
        }
      }, 200);
      return;
    }
  });

  // Input syncing
  browseSearchInputEl.addEventListener("input", () => {
    if (currentState) currentState.marketSearchQuery = browseSearchInputEl.value;
  });
  buyPriceInputEl.addEventListener("input", () => {
    if (currentState) currentState.marketOrderFormPrice = buyPriceInputEl.value;
  });
  buyQtyInputEl.addEventListener("input", () => {
    if (currentState) currentState.marketOrderFormQty = buyQtyInputEl.value;
  });

  // Prevent game input while typing
  panelEl.addEventListener("keydown", (e) => {
    if (e.target instanceof HTMLInputElement) {
      e.stopPropagation();
    }
  });
}

function refreshAfterAction(state: GameState, itemId: number): void {
  setTimeout(() => {
    if (state.marketPanelOpen && itemId) {
      sendOp(state, encodeMarketBrowse(itemId));
    }
  }, 200);
}

export function updateMarketPanel(state: GameState): void {
  if (!panelEl) return;
  currentState = state;
  buildStructure();

  if (!state.marketPanelOpen || !state.isDocked) {
    panelEl.style.display = "none";
    if (pollInterval) {
      clearInterval(pollInterval);
      pollInterval = null;
    }
    return;
  }

  panelEl.style.display = "flex";

  if (!pollInterval) {
    pollInterval = setInterval(() => {
      if (!state.marketPanelOpen) return;
      if (state.marketTab === "browse" && state.marketSelectedItemId) {
        sendOp(state, encodeMarketBrowse(state.marketSelectedItemId));
      } else if (state.marketTab === "sell" && state.marketSellSelectedItemId) {
        sendOp(state, encodeMarketBrowse(state.marketSellSelectedItemId));
      }
    }, 3000);
  }

  // Tab switching
  const tabChanged = lastTab !== state.marketTab;
  if (tabChanged) {
    lastTab = state.marketTab;
    browseTabBtn.className = `market-tab ${state.marketTab === "browse" ? "active" : ""}`;
    sellTabBtn.className = `market-tab ${state.marketTab === "sell" ? "active" : ""}`;
    myOrdersTabBtn.className = `market-tab ${state.marketTab === "myorders" ? "active" : ""}`;

    bodyEl.innerHTML = "";
    if (state.marketTab === "browse") {
      bodyEl.appendChild(browseItemColEl);
      bodyEl.appendChild(browseBookColEl);
      bodyEl.appendChild(browseFormColEl);
    } else if (state.marketTab === "sell") {
      bodyEl.appendChild(sellBankColEl);
      bodyEl.appendChild(sellBookColEl);
      bodyEl.appendChild(sellFormColEl);
    } else {
      bodyEl.appendChild(myOrdersColEl);
    }

    // Force rebuild all sections
    lastBrowseListSelectedId = -1;
    lastBrowseBookSelectedId = -1;
    lastBrowseFormSelectedId = -1;
    lastBrowseOrderBookJson = "";
    lastBrowseItemDefsSize = -1;
    lastBrowseSearchQuery = "\0";
    lastBrowseBankJson = "";
    lastSellBankJson = "";
    lastSellSelectedId = -1;
    lastSellBookSelectedId = -1;
    lastSellOrderBookJson = "";
    lastSellFormSelectedId = -1;
    lastSellFormBankJson = "";
    lastMyOrdersJson = "";
  }

  if (state.marketTab === "browse") {
    updateBrowseItemList(state);
    updateBrowseOrderBook(state);
    updateBuyForm(state);
  } else if (state.marketTab === "sell") {
    updateSellBankList(state);
    updateSellOrderBook(state);
    updateSellForm(state);
  } else {
    updateMyOrders(state);
  }
}

// ============ BROWSE TAB ============

function updateBrowseItemList(state: GameState): void {
  const queryChanged = lastBrowseSearchQuery !== state.marketSearchQuery;
  const defsChanged = lastBrowseItemDefsSize !== state.itemDefs.size;
  const selChanged = lastBrowseListSelectedId !== state.marketSelectedItemId;

  if (!queryChanged && !defsChanged && !selChanged) return;
  lastBrowseSearchQuery = state.marketSearchQuery;
  lastBrowseItemDefsSize = state.itemDefs.size;
  lastBrowseListSelectedId = state.marketSelectedItemId;

  if (document.activeElement !== browseSearchInputEl) {
    browseSearchInputEl.value = state.marketSearchQuery;
  }

  browseItemListEl.innerHTML = "";
  const query = state.marketSearchQuery.toLowerCase();

  const categories = new Map<number, { id: number; name: string }[]>();
  for (const [, def] of state.itemDefs) {
    if (def.id === FLUX_ITEM_ID) continue;
    if (query && !def.name.toLowerCase().includes(query)) continue;
    const cat = def.category || 0;
    if (!categories.has(cat)) categories.set(cat, []);
    categories.get(cat)!.push(def);
  }

  const catNames: Record<number, string> = { 0: "Currency", 1: "Resources", 2: "Equipment", 3: "Consumables", 4: "Modules" };

  for (const [cat, items] of [...categories.entries()].sort((a, b) => a[0] - b[0])) {
    const catHeader = el("div", "market-cat-header");
    catHeader.textContent = catNames[cat] || `Category ${cat}`;
    browseItemListEl.appendChild(catHeader);

    for (const item of items.sort((a, b) => a.name.localeCompare(b.name))) {
      const row = el("div", `market-item-row ${item.id === state.marketSelectedItemId ? "selected" : ""}`);
      row.dataset.browseItemId = item.id.toString();
      row.style.color = ITEM_COLORS_CSS[item.id] || DEFAULT_ITEM_COLOR;
      row.textContent = item.name;
      browseItemListEl.appendChild(row);
    }
  }
}

function updateBrowseOrderBook(state: GameState): void {
  const obJson = state.marketOrderBook ? JSON.stringify(state.marketOrderBook) : "";
  if (obJson === lastBrowseOrderBookJson && lastBrowseBookSelectedId === state.marketSelectedItemId) return;
  lastBrowseOrderBookJson = obJson;
  lastBrowseBookSelectedId = state.marketSelectedItemId;

  renderOrderBook(browseBookColEl, state, state.marketSelectedItemId);
}

function updateBuyForm(state: GameState): void {
  const bankJson = JSON.stringify([...state.bankItems.entries()]);
  const needsRebuild =
    lastBrowseFormSelectedId !== state.marketSelectedItemId ||
    lastBrowseBankJson !== bankJson;

  if (needsRebuild) {
    lastBrowseFormSelectedId = state.marketSelectedItemId;
    lastBrowseBankJson = bankJson;

    browseFormColEl.innerHTML = "";

    if (!state.marketSelectedItemId) {
      const hint = el("div", "market-form-hint");
      hint.textContent = "Select an item to buy";
      browseFormColEl.appendChild(hint);
      return;
    }

    const def = state.itemDefs.get(state.marketSelectedItemId);
    const itemName = def ? def.name : `Item #${state.marketSelectedItemId}`;

    const itemLabel = el("div", "market-form-item");
    itemLabel.textContent = itemName;
    itemLabel.style.color = ITEM_COLORS_CSS[state.marketSelectedItemId] || DEFAULT_ITEM_COLOR;
    browseFormColEl.appendChild(itemLabel);

    const priceLabel = el("div", "market-form-label");
    priceLabel.textContent = "Price per unit:";
    browseFormColEl.appendChild(priceLabel);
    if (document.activeElement !== buyPriceInputEl) {
      buyPriceInputEl.value = state.marketOrderFormPrice;
    }
    browseFormColEl.appendChild(buyPriceInputEl);

    const qtyLabel = el("div", "market-form-label");
    qtyLabel.textContent = "Quantity:";
    browseFormColEl.appendChild(qtyLabel);
    if (document.activeElement !== buyQtyInputEl) {
      buyQtyInputEl.value = state.marketOrderFormQty;
    }
    browseFormColEl.appendChild(buyQtyInputEl);

    buyTotalLabelEl = el("div", "market-form-total");
    browseFormColEl.appendChild(buyTotalLabelEl);
    buyAvailLabelEl = el("div", "market-form-avail");
    browseFormColEl.appendChild(buyAvailLabelEl);

    browseFormColEl.appendChild(el("div", "market-form-sep"));

    const createBtn = el("div", "market-btn market-buy-create-btn");
    createBtn.textContent = "CREATE BUY ORDER";
    browseFormColEl.appendChild(createBtn);

    const instantBtn = el("div", "market-btn market-buy-instant-btn");
    instantBtn.textContent = "INSTANT BUY";
    browseFormColEl.appendChild(instantBtn);
  }

  // Update labels every frame
  if (!state.marketSelectedItemId || !buyTotalLabelEl) return;

  const price = Math.floor(parseFloat(buyPriceInputEl.value) || 0);
  const qty = Math.floor(parseFloat(buyQtyInputEl.value) || 0);
  buyTotalLabelEl.textContent = `Total: ${price * qty} FLUX`;

  const fluxBal = state.fluxBalance;
  buyAvailLabelEl.textContent = `Available: ${Math.floor(fluxBal)} FLUX`;
}

// ============ SELL TAB ============

function updateSellBankList(state: GameState): void {
  const bankJson = JSON.stringify([...state.bankItems.entries()]);
  const selChanged = lastSellSelectedId !== state.marketSellSelectedItemId;

  if (bankJson === lastSellBankJson && !selChanged) return;
  lastSellBankJson = bankJson;
  lastSellSelectedId = state.marketSellSelectedItemId;

  sellBankListEl.innerHTML = "";

  const items: { id: number; name: string; qty: number }[] = [];
  for (const [itemId, qty] of state.bankItems) {
    if (itemId === FLUX_ITEM_ID || qty <= 0) continue;
    const def = state.itemDefs.get(itemId);
    items.push({ id: itemId, name: def ? def.name : `Item #${itemId}`, qty });
  }
  items.sort((a, b) => a.name.localeCompare(b.name));

  if (items.length === 0) {
    const empty = el("div", "market-book-empty");
    empty.textContent = "No items in bank to sell";
    sellBankListEl.appendChild(empty);
    return;
  }

  for (const item of items) {
    const row = el("div", `market-bank-row ${item.id === state.marketSellSelectedItemId ? "selected" : ""}`);
    row.dataset.sellItemId = item.id.toString();
    row.style.color = ITEM_COLORS_CSS[item.id] || DEFAULT_ITEM_COLOR;

    const nameSpan = el("span", "market-bank-name");
    nameSpan.textContent = item.name;
    nameSpan.dataset.sellItemId = item.id.toString();
    row.appendChild(nameSpan);

    const qtySpan = el("span", "market-bank-qty");
    qtySpan.textContent = `x${Math.floor(item.qty)}`;
    qtySpan.dataset.sellItemId = item.id.toString();
    row.appendChild(qtySpan);

    sellBankListEl.appendChild(row);
  }
}

function updateSellOrderBook(state: GameState): void {
  const obJson = state.marketOrderBook ? JSON.stringify(state.marketOrderBook) : "";
  if (obJson === lastSellOrderBookJson && lastSellBookSelectedId === state.marketSellSelectedItemId) return;
  lastSellOrderBookJson = obJson;
  lastSellBookSelectedId = state.marketSellSelectedItemId;

  renderOrderBook(sellBookColEl, state, state.marketSellSelectedItemId);
}

function updateSellForm(state: GameState): void {
  const bankJson = JSON.stringify([...state.bankItems.entries()]);
  const needsRebuild =
    lastSellFormSelectedId !== state.marketSellSelectedItemId ||
    lastSellFormBankJson !== bankJson;

  if (needsRebuild) {
    lastSellFormSelectedId = state.marketSellSelectedItemId;
    lastSellFormBankJson = bankJson;

    sellFormColEl.innerHTML = "";

    if (!state.marketSellSelectedItemId) {
      const hint = el("div", "market-form-hint");
      hint.textContent = "Select a bank item to sell";
      sellFormColEl.appendChild(hint);
      return;
    }

    const def = state.itemDefs.get(state.marketSellSelectedItemId);
    const itemName = def ? def.name : `Item #${state.marketSellSelectedItemId}`;

    const itemLabel = el("div", "market-form-item");
    itemLabel.textContent = itemName;
    itemLabel.style.color = ITEM_COLORS_CSS[state.marketSellSelectedItemId] || DEFAULT_ITEM_COLOR;
    sellFormColEl.appendChild(itemLabel);

    const priceLabel = el("div", "market-form-label");
    priceLabel.textContent = "Price per unit:";
    sellFormColEl.appendChild(priceLabel);
    if (document.activeElement !== sellPriceInputEl) {
      sellPriceInputEl.value = sellPriceInputEl.value;
    }
    sellFormColEl.appendChild(sellPriceInputEl);

    const qtyLabel = el("div", "market-form-label");
    qtyLabel.textContent = "Quantity:";
    sellFormColEl.appendChild(qtyLabel);
    const qtyRow = el("div", "market-qty-row");
    if (document.activeElement !== sellQtyInputEl) {
      sellQtyInputEl.value = sellQtyInputEl.value || "1";
    }
    qtyRow.appendChild(sellQtyInputEl);
    const maxBtn = el("span", "market-btn market-sell-max-btn");
    maxBtn.textContent = "MAX";
    qtyRow.appendChild(maxBtn);
    sellFormColEl.appendChild(qtyRow);

    sellTotalLabelEl = el("div", "market-form-total");
    sellFormColEl.appendChild(sellTotalLabelEl);
    sellAvailLabelEl = el("div", "market-form-avail");
    sellFormColEl.appendChild(sellAvailLabelEl);

    sellFormColEl.appendChild(el("div", "market-form-sep"));

    const createBtn = el("div", "market-btn market-sell-create-btn");
    createBtn.textContent = "CREATE SELL ORDER";
    sellFormColEl.appendChild(createBtn);

    const instantBtn = el("div", "market-btn market-sell-instant-btn");
    instantBtn.textContent = "INSTANT SELL";
    sellFormColEl.appendChild(instantBtn);
  }

  if (!state.marketSellSelectedItemId || !sellTotalLabelEl) return;

  // Auto-suggest price when order book loads for a new item
  if (state.marketOrderBook &&
      state.marketOrderBook.itemId === state.marketSellSelectedItemId &&
      lastSellAutoSuggestItemId !== state.marketSellSelectedItemId &&
      document.activeElement !== sellPriceInputEl) {
    lastSellAutoSuggestItemId = state.marketSellSelectedItemId;
    if (state.marketOrderBook.sellLevels.length > 0) {
      const lowestSell = Math.floor(state.marketOrderBook.sellLevels[0].price);
      const suggested = Math.max(1, lowestSell - 1);
      sellPriceInputEl.value = suggested.toString();
    } else if (state.marketOrderBook.buyLevels.length > 0) {
      const highestBuy = Math.floor(state.marketOrderBook.buyLevels[0].price);
      sellPriceInputEl.value = (highestBuy + 1).toString();
    }
  }

  // Update labels every frame
  const price = Math.floor(parseFloat(sellPriceInputEl.value) || 0);
  const qty = Math.floor(parseFloat(sellQtyInputEl.value) || 0);
  sellTotalLabelEl.textContent = `Total: ${price * qty} FLUX`;

  const bankQty = state.bankItems.get(state.marketSellSelectedItemId) || 0;
  sellAvailLabelEl.textContent = `In bank: ${Math.floor(bankQty)}`;
}

// ============ MY ORDERS TAB ============

function updateMyOrders(state: GameState): void {
  const json = JSON.stringify(state.marketMyOrders);
  if (json === lastMyOrdersJson) return;
  lastMyOrdersJson = json;

  myOrdersColEl.innerHTML = "";

  if (state.marketMyOrders.length === 0) {
    const empty = el("div", "market-book-empty");
    empty.textContent = "No active orders";
    myOrdersColEl.appendChild(empty);
    return;
  }

  const headerRow = el("div", "market-order-row header");
  headerRow.innerHTML = "<span>Type</span><span>Item</span><span>Price</span><span>Qty</span><span>Filled</span><span></span>";
  myOrdersColEl.appendChild(headerRow);

  for (const order of state.marketMyOrders) {
    const def = state.itemDefs.get(order.itemId);
    const name = def ? def.name : `#${order.itemId}`;
    const color = ITEM_COLORS_CSS[order.itemId] || DEFAULT_ITEM_COLOR;
    const row = el("div", `market-order-row ${order.isBuy ? "buy" : "sell"}`);
    const filled = order.origQuantity > 0 ? Math.floor(order.origQuantity - order.quantity) : 0;
    row.innerHTML = `
      <span class="${order.isBuy ? "buy-text" : "sell-text"}">${order.isBuy ? "BUY" : "SELL"}</span>
      <span style="color:${color}">${name}</span>
      <span>${Math.floor(order.pricePerUnit)}</span>
      <span>${Math.floor(order.quantity)}</span>
      <span>${filled}/${Math.floor(order.origQuantity)}</span>
    `;
    const cancelBtn = el("span", "market-cancel-btn");
    cancelBtn.textContent = "CANCEL";
    cancelBtn.dataset.cancelOrderId = order.orderId.toString();
    row.appendChild(cancelBtn);
    myOrdersColEl.appendChild(row);
  }
}

// ============ SHARED ============

function renderOrderBook(container: HTMLElement, state: GameState, selectedItemId: number): void {
  container.innerHTML = "";

  if (selectedItemId && state.marketOrderBook && state.marketOrderBook.itemId === selectedItemId) {
    const ob = state.marketOrderBook;

    const sellHeader = el("div", "market-book-header sell");
    sellHeader.textContent = "SELL ORDERS";
    container.appendChild(sellHeader);

    const sellTable = el("div", "market-book-table");
    const sellHdr = el("div", "market-book-row header");
    sellHdr.innerHTML = "<span>Price</span><span>Qty</span><span>Total</span>";
    sellTable.appendChild(sellHdr);

    for (const level of ob.sellLevels) {
      const row = el("div", "market-book-row sell");
      row.innerHTML = `<span>${Math.floor(level.price)}</span><span>${Math.floor(level.quantity)}</span><span>${Math.floor(level.price * level.quantity)}</span>`;
      sellTable.appendChild(row);
    }
    if (ob.sellLevels.length === 0) {
      const empty = el("div", "market-book-empty");
      empty.textContent = "No sell orders";
      sellTable.appendChild(empty);
    }
    container.appendChild(sellTable);

    const spread = el("div", "market-spread");
    if (ob.sellLevels.length > 0 && ob.buyLevels.length > 0) {
      spread.textContent = `SPREAD: ${Math.floor(ob.sellLevels[0].price - ob.buyLevels[0].price)} FLUX`;
    } else {
      spread.textContent = "---";
    }
    container.appendChild(spread);

    const buyHeader = el("div", "market-book-header buy");
    buyHeader.textContent = "BUY ORDERS";
    container.appendChild(buyHeader);

    const buyTable = el("div", "market-book-table");
    const buyHdr = el("div", "market-book-row header");
    buyHdr.innerHTML = "<span>Price</span><span>Qty</span><span>Total</span>";
    buyTable.appendChild(buyHdr);

    for (const level of ob.buyLevels) {
      const row = el("div", "market-book-row buy");
      row.innerHTML = `<span>${Math.floor(level.price)}</span><span>${Math.floor(level.quantity)}</span><span>${Math.floor(level.price * level.quantity)}</span>`;
      buyTable.appendChild(row);
    }
    if (ob.buyLevels.length === 0) {
      const empty = el("div", "market-book-empty");
      empty.textContent = "No buy orders";
      buyTable.appendChild(empty);
    }
    container.appendChild(buyTable);

  } else if (selectedItemId) {
    const loading = el("div", "market-book-empty");
    loading.textContent = "Loading...";
    container.appendChild(loading);
  } else {
    const hint = el("div", "market-book-empty");
    hint.textContent = "Select an item to view orders";
    container.appendChild(hint);
  }
}

function el(tag: string, className?: string): HTMLElement {
  const e = document.createElement(tag);
  if (className) e.className = className;
  return e;
}

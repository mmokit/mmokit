# Station UI Redesign + Equipment-Shop Removal

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make bank + market the auto-open default screen on dock; rebuild the bank panel around a unified Bank|Cargo table; remove the vendor "buy at station" feature in full.

**Architecture:** The two panels (bank, market) are wrapped in a flex container `#station-panels` so they sit side-by-side. Bank panel becomes a single sortable table with `BANK` and `CARGO` qty columns, plus an absorbed loadout grid (the existing `#equip-slots` element is reparented between cargo-panel and bank-panel based on dock state — same DOM node, same drag listeners, just different parent). Server side, every reference to `BuyPrice` / `ShopBuyMsg` / `GCE_SHOP_BUY` / `processShopBuys` / `CatEconomyShop` is deleted; per the project's no-backward-compat convention, proto fields are renumbered rather than reserved.

**Tech Stack:** Go 1.22+, protobuf via buf, TypeScript/PixiJS web client, Vite. Server build via `just build`; client SDK regen via `just space-sdk`.

**Spec:** [2026-04-30-station-ui-redesign-design.md](../specs/2026-04-30-station-ui-redesign-design.md)

---

## Task 1: Server — strip equipment-shop from proto

**Files:**

- Modify: `proto/gamepb/game.proto`

- [ ] **Step 1: Delete `GCE_SHOP_BUY` from `GameClientEventCode` and renumber later values to fill the gap.**

In `proto/gamepb/game.proto`, replace the enum block:

```proto
enum GameClientEventCode {
    GCE_UNKNOWN = 0;
    GCE_INVENTORY_TRANSFER = 5;
    GCE_BANK_REQUEST = 6;
    GCE_SELL_BANK_ITEM = 7;
    GCE_EQUIP = 8;
    GCE_DOCK = 9;
    GCE_UNDOCK = 10;
    GCE_LOOT_ITEM = 11;
    GCE_LOOT_ALL = 12;
    GCE_RESPAWN = 13;
}
```

(Was: `GCE_SHOP_BUY = 9; GCE_DOCK = 10; ... GCE_RESPAWN = 14;`)

- [ ] **Step 2: Delete `buy_price` field from `ItemDefMsg`.**

Replace the message:

```proto
message ItemDefMsg {
    uint32 id = 1;
    string name = 2;
    float mass_per_unit = 3;
    float sell_price = 4;
    uint32 category = 5;        // ItemCategory enum value
    uint32 equip_slot = 6;      // EquipSlot (0 = not equippable)
}
```

(Was: had `float buy_price = 7;` as the last field.)

- [ ] **Step 3: Delete the `ShopBuyMsg` message entirely.**

Remove these four lines from the file:

```proto
message ShopBuyMsg {
    uint32 item_id = 1;
    uint32 quantity = 2;   // number to buy (usually 1 for equipment)
}
```

- [ ] **Step 4: Regenerate proto bindings.**

Run: `just proto`
Expected: completes without errors. Generated `gen/go/gamepb/game.pb.go` no longer contains `ShopBuyMsg` or `BuyPrice`.

---

## Task 2: Server — strip equipment-shop from Go code

**Files:**

- Modify: `internal/item/item.go`
- Modify: `internal/game/entity_ship.go`
- Modify: `internal/game/world.go`
- Modify: `internal/game/system_economy.go`
- Modify: `internal/game/input_handlers.go`
- Modify: `internal/game/logcat.go`

- [ ] **Step 1: Delete `BuyPrice` field from `ItemDef` struct.**

In `internal/item/item.go` line 130, delete:

```go
BuyPrice    float64    // settlement currency cost at station shop (0 = not purchasable)
```

- [ ] **Step 2: Delete every `BuyPrice:` literal in the seeded item table.**

In `internal/item/item.go`, in each `ItemDef{...}` literal in the seed table, delete the `BuyPrice: NNN,` term. Affected lines (per `grep -n BuyPrice internal/item/item.go`): 164, 178, 192, 206, 222, 236, 252, 263, 276, 286.

After this, run `grep -n BuyPrice internal/item/item.go` and confirm no matches.

- [ ] **Step 3: Delete `BuyPrice:` from the two `ItemDefMsg` builder loops in `entity_ship.go`.**

In `internal/game/entity_ship.go` at line 158 and line 232, delete the line:

```go
BuyPrice:    float32(def.BuyPrice),
```

The surrounding struct literal becomes:

```go
itemDefs = append(itemDefs, &gamepb.ItemDefMsg{
    Id:          def.ID,
    Name:        def.Name,
    MassPerUnit: def.MassPerUnit,
    SellPrice:   float32(def.SellPrice),
    Category:    uint32(def.Category),
    EquipSlot:   uint32(def.EquipSlot),
})
```

- [ ] **Step 4: Delete `PendingShopBuy` struct from `world.go`.**

In `internal/game/world.go`, delete:

```go
// PendingShopBuy records a request to buy an item from the station shop.
type PendingShopBuy struct {
    ConnID uint32
    ItemID uint32
    Qty    uint32
}
```

- [ ] **Step 5: Delete `processShopBuys` method and its call site in `system_economy.go`.**

In `internal/game/system_economy.go`, delete the call site in `EconomySystem.Update`:

```go
// Process shop buy requests
s.processShopBuys(stationPositions, sellRange2)
```

Delete the method itself (entire function body, currently lines 313–399 — `func (s *EconomySystem) processShopBuys(stationPositions []mmokit.Position, sellRange2 float64) { ... }`).

- [ ] **Step 6: Delete the `GCE_SHOP_BUY` input handler in `input_handlers.go`.**

In `internal/game/input_handlers.go`, delete the entire block (currently lines 179–191):

```go
mmokit.OnInput[gamepb.ShopBuyMsg](mmo, gamepb.GameClientEventCode_GCE_SHOP_BUY).
    States(mmokit.StateActive, StateDocked).
    Do(func(p *mmokit.Player, msg *gamepb.ShopBuyMsg) {
        gw := gameWorldFromPlayer(p)
        if gw == nil {
            return
        }
        mmokit.Enqueue(gw.Queue, PendingShopBuy{
            ConnID: p.ConnID(),
            ItemID: msg.ItemId,
            Qty:    msg.Quantity,
        })
    })
```

- [ ] **Step 7: Delete `CatEconomyShop` from `logcat.go` and remove from the registration slice.**

In `internal/game/logcat.go`:

Delete the constant declaration (line 17):

```go
CatEconomyShop   = "economy:shop"   // buy/sell at stations
```

In the `GameCategories` slice, remove the `CatEconomyShop,` token from the line. The line should become:

```go
CatEconomyBank, CatEconomyLoot, CatEconomyMarket, CatEconomyMining,
```

- [ ] **Step 8: Verify the server builds clean.**

Run: `go vet ./...`
Expected: no output (clean). If any reference to `BuyPrice`, `ShopBuyMsg`, `PendingShopBuy`, `processShopBuys`, `GCE_SHOP_BUY`, or `CatEconomyShop` remains anywhere, the vet will surface it as undefined identifier — fix at the reported location.

Run: `just build-go`
Expected: completes; produces `bin/server`.

- [ ] **Step 9: Commit.**

```bash
git add proto/gamepb/game.proto gen/go/gamepb/ gen/csharp/Game.cs gen/es/gamepb/ \
    internal/item/item.go internal/game/entity_ship.go internal/game/world.go \
    internal/game/system_economy.go internal/game/input_handlers.go \
    internal/game/logcat.go
git commit -m "$(cat <<'EOF'
feat: remove equipment shop feature

Equipment will eventually be crafted from refined materials; for now,
admin commands grant gear. Removes ShopBuyMsg / GCE_SHOP_BUY / BuyPrice
field / processShopBuys handler / CatEconomyShop log category.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Client — strip `buyPrice` references and regenerate SDK

**Files:**

- Modify: `web-pixi/src/state.ts`
- Modify: `web-pixi/src/network.ts`
- Modify: `web-pixi/src/ui/hud.ts`

- [ ] **Step 1: Delete `buyPrice` field from `ItemDef` in `state.ts`.**

In `web-pixi/src/state.ts` around line 15, delete:

```ts
buyPrice: number;
```

- [ ] **Step 2: Delete `buyPrice` from the `ItemDef` builder in `network.ts`.**

In `web-pixi/src/network.ts` around line 196, delete:

```ts
buyPrice: def.buyPrice,
```

- [ ] **Step 3: Delete the `Buy:` tooltip line in `hud.ts`.**

In `web-pixi/src/ui/hud.ts` around line 133, delete:

```ts
if (def.buyPrice > 0) lines.push(`Buy: ${Math.floor(def.buyPrice)} ${cn}`);
```

- [ ] **Step 4: Regenerate the typed client SDK.**

Run: `just space-sdk`
Expected: completes; `web-pixi/sdk/client.ts` no longer contains `sendShopBuy` or any `ShopBuyMsg` reference. `web-pixi/sdk/_generated/` is rewritten in place.

- [ ] **Step 5: Verify the client typechecks.**

Run: `cd web-pixi && bun run tsc --noEmit`
Expected: no errors. If `bank.ts` flags errors about `sendShopBuy` not existing on `client`, that is expected — the next task rewrites `bank.ts` and removes those calls.

If the only TypeScript error is in `bank.ts` referencing `sendShopBuy` or shop-related state, that's fine — proceed. Any other error must be fixed before continuing.

- [ ] **Step 6: Commit.**

```bash
git add web-pixi/src/state.ts web-pixi/src/network.ts web-pixi/src/ui/hud.ts \
    web-pixi/sdk/
git commit -m "$(cat <<'EOF'
feat(web): strip buyPrice references and regenerate SDK

Removes ItemDef.buyPrice from client state, the network builder, and
the hud tooltip. SDK regenerated; sendShopBuy is gone.

bank.ts still references shop-buy code paths and is rewritten in the
next commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Client — restructure index.html for the docked station view

**Files:**

- Modify: `web-pixi/index.html`

- [ ] **Step 1: Replace the bank-panel HTML body.**

In `web-pixi/index.html`, find the `<div id="bank-panel">…</div>` block (around lines 561–570). Replace the entire block with:

```html
<div id="bank-panel">
    <div class="bank-header">
        <span class="bank-title">STATION</span>
        <span class="bank-close-btn" id="bank-close-btn">✕</span>
    </div>
    <h3>Loadout</h3>
    <div id="bank-equip-host"></div>
    <h3>Bank &amp; Cargo</h3>
    <table id="bank-table">
        <thead><tr><th>ITEM</th><th class="num">BANK</th><th class="num">CARGO</th><th></th></tr></thead>
        <tbody id="bank-rows"></tbody>
    </table>
    <div id="bank-footer"></div>
</div>
```

(`#bank-equip-host` is the anchor where the existing `#equip-slots` node is moved by `bank.ts` while docked.)

- [ ] **Step 2: Add a host element inside `#cargo-panel` for `#equip-slots` to live in while undocked.**

Find the `<div id="cargo-panel">` block (around line 554). Wrap the existing `<div id="equip-slots"></div>` in a host:

```html
<div id="cargo-panel">
    <h2>LOADOUT</h2>
    <div id="cargo-equip-host">
        <div id="equip-slots"></div>
    </div>
    <h3>CARGO</h3>
    <div id="cargo-rows"></div>
    <div id="cargo-footer"></div>
</div>
```

(Only the `<div id="cargo-equip-host">…</div>` wrapper around `#equip-slots` is new.)

- [ ] **Step 3: Wrap the bank and marketplace panels in a `#station-panels` flex container.**

Find the consecutive blocks `<div id="bank-panel">…</div>` and `<div id="marketplace-panel"></div>`. Wrap both in:

```html
<div id="station-panels">
    <div id="bank-panel"> … </div>
    <div id="marketplace-panel"></div>
</div>
```

- [ ] **Step 4: Add CSS for `#station-panels`, the bank header / table, and the docked-state cargo-panel hide.**

In the `<style>` block, replace the existing `#bank-panel` rules (and add new rules for the table and station-panels) by inserting this CSS — replace the existing `#bank-panel` block (currently lines 252–282 starting with `#bank-panel {`) with:

```css
/* Docked station view: bank + market wrapped together */
#station-panels {
    position: absolute; top: 50%; left: 50%;
    transform: translate(-50%, -50%);
    display: none;
    gap: 12px;
    z-index: 25;
    align-items: stretch;
}
#station-panels.open { display: flex; }

/* Bank panel */
#bank-panel {
    position: static;
    background: rgba(10, 12, 20, 0.94);
    border: 1px solid rgba(80, 180, 80, 0.6);
    padding: 12px 14px; width: 360px;
    display: none;
    flex-direction: column;
}
#station-panels.open #bank-panel { display: flex; }
.bank-header {
    display: flex; align-items: center; justify-content: space-between;
    padding-bottom: 6px; margin-bottom: 6px;
    border-bottom: 1px solid rgba(255,255,255,0.1);
}
.bank-title { color: #8f8; font-size: 14px; font-weight: bold; letter-spacing: 1px; }
.bank-close-btn {
    color: #888; cursor: pointer; font-size: 14px; font-weight: bold;
    padding: 0 6px;
}
.bank-close-btn:hover { color: #fff; }
#bank-panel h3 {
    color: #aab; font-size: 11px; margin: 8px 0 4px;
    text-transform: uppercase; letter-spacing: 1px;
    border-bottom: 1px solid rgba(255,255,255,0.08); padding-bottom: 4px;
}
#bank-equip-host #equip-slots {
    /* slots inherit their existing 2x2 grid styling */
    padding: 12px 0;
}
#bank-table {
    width: 100%; border-collapse: collapse; margin-top: 4px;
}
#bank-table thead th {
    color: #888; font-size: 10px; font-weight: bold;
    text-align: left; padding: 4px 8px; letter-spacing: 1px;
    border-bottom: 1px solid rgba(255,255,255,0.08);
}
#bank-table thead th.num { text-align: right; }
.bank-row {
    display: table-row;
    background: rgba(255,255,255,0.04);
}
.bank-row:hover { background: rgba(255,255,255,0.08); }
.bank-row td {
    padding: 5px 8px; font-size: 12px;
    border-bottom: 1px solid rgba(0,0,0,0.2);
    vertical-align: middle;
}
.bank-row td.num { text-align: right; color: #eee; }
.bank-row td.num.empty { color: #555; }
.bank-row td.actions { text-align: right; white-space: nowrap; }
.bank-empty-row td { text-align: center; color: #666; padding: 12px; font-size: 12px; }
.bank-btn {
    background: rgba(80,180,80,0.2); border: 1px solid rgba(80,180,80,0.5);
    color: #8f8; font-family: monospace; font-size: 11px;
    padding: 2px 6px; cursor: pointer; margin-left: 4px;
}
.bank-btn:hover { background: rgba(80,180,80,0.4); }
.bank-btn.bank-sell-btn {
    color: #dd4; border-color: rgba(200,180,50,0.5);
    background: rgba(200,180,50,0.2);
}
.bank-btn.bank-sell-btn:hover { background: rgba(200,180,50,0.4); }
#bank-footer {
    text-align: center; font-size: 11px; color: #888; margin-top: 10px;
    border-top: 1px solid rgba(255,255,255,0.08); padding-top: 6px;
}

/* Hide the cargo sidebar while the station view is up */
body.docked #cargo-panel { display: none !important; }
```

Also locate the existing `#marketplace-panel` rule (line ~382 starting `#marketplace-panel {`) and change `position: absolute; top: 50%; left: 50%; transform: translate(-50%, -50%);` to `position: static;` so the panel sits inside the flex container instead of doing its own centering. The `width: 900px; height: 600px; … display: none; flex-direction: column;` lines stay; add a sibling rule:

```css
#station-panels.open #marketplace-panel { display: flex; }
```

The existing `#marketplace-panel` rule should end up looking like:

```css
#marketplace-panel {
    position: static;
    background: rgba(10, 12, 20, 0.96);
    border: 1px solid rgba(68, 170, 255, 0.5);
    width: 900px; height: 600px;
    z-index: 35; display: none;
    flex-direction: column; font-size: 13px;
    color: #ccc;
}
#station-panels.open #marketplace-panel { display: flex; }
```

- [ ] **Step 5: Verify the page still loads.**

Run: `just dev` (in a separate terminal)
Open: http://localhost:8080
Expected: page loads, login overlay visible. No JS console errors specific to missing element IDs (the bank-panel is hidden until docked, so its empty state is fine).

Stop the dev server.

- [ ] **Step 6: Commit.**

```bash
git add web-pixi/index.html
git commit -m "$(cat <<'EOF'
feat(web): restructure docked station view layout

Wraps bank and market panels in #station-panels flex container so they
appear side-by-side on dock. Bank panel rebuilt with a single table for
the unified Bank|Cargo display, plus a #bank-equip-host anchor that
bank.ts uses to host the equip-slots grid while docked. Cargo-panel
sidebar gains a #cargo-equip-host so the same node can be reparented
back when undocked. Equipment-shop section deleted.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Client — rewrite `bank.ts` for the unified table

**Files:**

- Modify: `web-pixi/src/ui/bank.ts`

- [ ] **Step 1: Replace the bank.ts module body.**

Overwrite `web-pixi/src/ui/bank.ts` with the following:

```ts
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

  // Use mousedown — the DOM is rebuilt on data changes and click can race.
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
  const stationEl = document.getElementById("station-panels")!;
  if (!panelEl || !stationEl) return;

  currentState = state;
  setupDelegation();

  // station-panels container is "open" iff bank is open OR market is open;
  // each child uses its own display rule under that container.
  const stationOpen = state.bankPanelOpen || state.marketPanelOpen;
  stationEl.classList.toggle("open", stationOpen);

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

  // Build the unified table (memoized on bankItems + dockedCargoItems).
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
```

- [ ] **Step 2: Verify the client typechecks and the SDK call is correct.**

Run: `cd web-pixi && bun run tsc --noEmit`
Expected: no errors. (If the typechecker complains about `state.dockedCargoItems` not existing, see step 3 — it should exist from the existing bank panel; otherwise add it through state.ts. The existing bank.ts already used `state.dockedCargoItems`, so it's already defined.)

- [ ] **Step 3: Commit.**

```bash
git add web-pixi/src/ui/bank.ts
git commit -m "$(cat <<'EOF'
feat(web): rewrite bank panel as unified table

One sortable table over the union of bankItems + dockedCargoItems.
Smart ↔ button: deposit when cargo>0, withdraw when cargo=0 and bank>0.
$ button only when sellPrice>0 AND bank qty>0. Same wire ops as before
(InventoryTransferMsg, SellBankItemMsg) — the transfer.deposit flag is
chosen by the table's per-row state instead of by separate buttons.

Adds syncEquipSlotsParent() helper that the dock-state side-effect site
calls to reparent #equip-slots between cargo-panel and bank-panel.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Client — wire dock side effects (auto-open market, body class, equip-slots reparent)

**Files:**

- Modify: `web-pixi/src/network.ts`
- Modify: `web-pixi/src/input.ts`

- [ ] **Step 1: Add a `syncEquipSlotsParent` import and `body.docked` toggling on the dock event.**

In `web-pixi/src/network.ts`, add at the top (with the other UI imports):

```ts
import { syncEquipSlotsParent } from "./ui/bank";
```

Then update the `client.onDocked(...)` handler (around line 426). Replace:

```ts
client.onDocked(() => {
    state.isDocked = true;
    state.isDockingInProgress = false;
    state.dockingProgress = 0;
    state.cellMapOpen = false;
    state.bankPanelOpen = true;
    // …
    client.sendBankRequest({});
});
```

with:

```ts
client.onDocked(() => {
    state.isDocked = true;
    state.isDockingInProgress = false;
    state.dockingProgress = 0;
    state.cellMapOpen = false;
    state.bankPanelOpen = true;
    state.marketPanelOpen = true;
    document.body.classList.add("docked");
    syncEquipSlotsParent(true);
    // The server parks the ship at station center and marks it Dormant — other
    // pilots' AoI broadcasts skip it (we vanish from the system view), but the
    // docked player's own AoI still includes it so the HUD can continue to
    // read position/cell/equipment from state.entities.get(myEntityId).
    client.sendBankRequest({});
});
```

- [ ] **Step 2: Reset the dock side effects on respawn.**

Replace the block at network.ts ~line 209 (inside the `onPlayerSpawned` handler):

```ts
state.isDead = false;
state.isDocked = false;
state.isDockingInProgress = false;
state.dockingProgress = 0;
state.bankPanelOpen = false;
state.marketPanelOpen = false;
```

with:

```ts
state.isDead = false;
state.isDocked = false;
state.isDockingInProgress = false;
state.dockingProgress = 0;
state.bankPanelOpen = false;
state.marketPanelOpen = false;
document.body.classList.remove("docked");
syncEquipSlotsParent(false);
```

Also update the `onPlayerDied` handler (around line 220 — find the block that sets `state.bankPanelOpen = false; state.marketPanelOpen = false;`) to add the same two lines after the existing state writes:

```ts
document.body.classList.remove("docked");
syncEquipSlotsParent(false);
```

- [ ] **Step 3: Drop the `!state.isDocked` gate on Escape closing the bank panel.**

In `web-pixi/src/input.ts` around line 107, the current Escape priority chain has:

```ts
} else if (state.bankPanelOpen && !state.isDocked) {
    state.bankPanelOpen = false;
```

Change to:

```ts
} else if (state.bankPanelOpen) {
    state.bankPanelOpen = false;
```

Now Escape can dismiss the bank panel even while docked (matching the `×` close button on the panel header). Re-docking via X→undock→X re-opens it.

- [ ] **Step 4: Verify typecheck.**

Run: `cd web-pixi && bun run tsc --noEmit`
Expected: no errors.

- [ ] **Step 5: Commit.**

```bash
git add web-pixi/src/network.ts web-pixi/src/input.ts
git commit -m "$(cat <<'EOF'
feat(web): auto-open market on dock; reparent equip-slots; body.docked

Dock now opens both bank and market. Adds body.docked class so the CSS
rule hides #cargo-panel while docked. syncEquipSlotsParent() moves the
existing #equip-slots node between cargo-panel and bank-panel based on
dock state — same DOM node, same drag-listeners. Escape can now dismiss
the bank panel while docked.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Manual smoke test

**Files:** none — runtime verification only.

- [ ] **Step 1: Start the dev server.**

Run: `just dev`
Wait until both Go server logs ("listening on :8080") and Vite logs ("ready in") are visible.

- [ ] **Step 2: Login and approach the station.**

Open http://localhost:8080. Enter a callsign and login. The player ship spawns near the station (per `Config.DefaultSpawn`).

Expected:
- HUD visible (top-left), status bars visible (bottom).
- Cargo-panel sidebar opens with `C` and shows the equip slots + cargo rows. Closes with `C` again.
- Press `M`. Expected: nothing happens (market is gated on `isDocked`).

- [ ] **Step 3: Dock at the station.**

Drive close to the station, press `X`. Wait for docking to complete.

Expected:
- `#station-panels` becomes visible: bank panel (green border, ~360px wide) on left, market panel (blue border, 900px) on right, centered as a pair.
- Bank panel shows: `STATION` header with × close button; "Loadout" with the 4 equip slots; "Bank & Cargo" with the unified table; footer with mass + currency.
- Cargo-panel sidebar is hidden (its space is empty).
- Market panel is fully functional — market tabs, item list, order book, form all visible.

If bank table is empty: that's expected for a fresh account. Mine some asteroid first (undock, mine, redock) and verify the row appears with `BANK —`, `CARGO 60` (or whatever you mined).

- [ ] **Step 4: Test the unified table actions.**

With at least one cargo item visible:

- Click `↔` on a cargo-only row → item moves to bank column. Bank qty grows by the cargo qty; cargo column becomes `—`.
- Shift+click `↔` on a row with both bank and cargo qty → half of cargo moves to bank.
- Click `↔` on a bank-only row → item moves to cargo (withdraw). Bank shrinks; cargo grows.
- Shift+click `↔` on a bank-only row → half of bank moves to cargo.
- Click `$` on a row with bank qty and `sellPrice > 0` → bank qty goes to 0 (or `—`); flux balance increases by `sellPrice * qty`.

If any action fails, check the dev console — server-side `transfer_result.success=false` paths (no transfer, cargo full, etc.) bubble up as toasts.

- [ ] **Step 5: Test the market panel still works.**

Place a small buy order (`Buy 1 Iron Ore @ 10`). Expected: order shows in "My Orders". Cancel it. Expected: cancellation success toast; row disappears.

This confirms the market panel was not regressed by the layout change.

- [ ] **Step 6: Test close + reopen flow.**

- Click `×` on the bank panel → bank panel closes; market panel stays open.
- Press Escape → market panel closes (next in the Escape priority chain).
- Now both panels are closed but you're still docked. Press `M` → market reopens.
- Press `X` → undock. Both panels reset; cargo-panel sidebar can be opened again with `C`; equip-slots are visible inside the cargo-panel sidebar.
- Re-dock → both panels appear again automatically.

- [ ] **Step 7: Test equip-slots drag-and-drop survives reparenting.**

Undock. Open `#cargo-panel` with `C`. Verify drag-and-drop of an equippable cargo item onto an equip slot still works (this is the existing behavior; we just want to confirm reparenting hasn't broken the listeners).

Re-dock. The equip slots are now in the bank panel. Verify:
- Equip slot shows the currently-equipped item correctly.
- Right-click an equip slot to unequip → item moves into ship cargo (visible in the bank table's CARGO column).

(Drag-from-bank-table-to-equip-slot is **not** wired in this round — the current drag sources are `.cargo-row` elements, and the bank panel uses `<tr>` rows. Re-equipping while docked should be done by undocking first; this is acceptable for a first cut. Fix forward if it bites.)

- [ ] **Step 8: Verify the equipment-shop is gone.**

- No "Equipment Shop" section in any panel.
- No "Buy: …" line in any item tooltip.
- Server logs (look at the console where `just dev` runs): no `economy:shop` category.

- [ ] **Step 9: Run a full server-side build.**

Stop `just dev`. Run: `just build`
Expected: completes; produces `bin/server`.

- [ ] **Step 10: Final commit (only if smoke turned up tweaks; else skip).**

If steps 1–8 found issues you fixed inline, commit the fixes here:

```bash
git add -A
git commit -m "$(cat <<'EOF'
fix(web): smoke-test polish for station-ui redesign

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

If no issues → no commit, plan complete.

---

## Self-review notes

**Spec coverage:** every section of the spec is covered:

- §"Equipment-shop removal (server)" → Tasks 1, 2.
- §"Equipment-shop removal (client)" → Task 3 (state/network/hud) + Task 4 (HTML deletion of `#shop-rows`) + Task 5 (bank.ts deletion of shop section).
- §"Layout container" → Task 4 step 3 + step 4.
- §"Bank panel internal layout" + "Unified bank/cargo table" → Task 4 step 1 + Task 5 (rows).
- §"Cargo-panel sidebar (C key) behavior" → Task 4 step 4 (CSS rule) + Task 6 step 1/2 (`body.docked` class).
- §"Open / close flow" → Task 6 (auto-open market, body class, equip-slots reparent) + Task 5 (close button) + Task 6 step 3 (Escape gate).
- §"State plumbing" → Task 6.
- §"Test plan" → Task 7.

**Type/method consistency:** `syncEquipSlotsParent`, `updateBankPanel`, `state.bankItems`, `state.dockedCargoItems`, `state.marketPanelOpen`, `state.bankPanelOpen` all match between definition (Task 5) and call sites (Task 6). `bank-equip-host` and `cargo-equip-host` element IDs match between HTML (Task 4) and bank.ts (Task 5).

**Risks per spec, surfaced:** the drag-from-bank-table-to-equip-slot path is intentionally unwired in this first cut (Task 7 step 7). If re-equipping while docked turns out to be a common workflow, follow up with a small extension that adds drag from `.bank-row` rows.

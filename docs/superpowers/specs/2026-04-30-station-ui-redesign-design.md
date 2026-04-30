# Station UI Redesign + Equipment-Shop Removal

**Date:** 2026-04-30
**Status:** Draft

## Summary

Two coupled changes to the docked-at-station experience:

1. Remove the "equipment shop" feature in full. Items will eventually be crafted from refined materials; for now, equipment is granted via admin commands.
2. Redesign the docked-station UI so the bank and the marketplace are visible side-by-side as the default screen on dock. The bank panel itself is rebuilt around a unified table that shows bank and ship-cargo quantities for each item in a single row, and absorbs the equip-slot loadout grid from the right-rail cargo sidebar.

## Goals

- Eliminate every reference to a vendor "buy at station" mechanic from server, proto, and client.
- Make `Bank + Market` the on-dock default. The current flow (`X` to dock, then separately press for bank, then `M` for market) becomes one action.
- Replace the bank's three-section layout (Bank Storage / Your Cargo / Equipment Shop) with a single unified table that surfaces "do I have this on my ship or in storage" at a glance.
- Merge ship-loadout management into the bank panel while docked so the right-rail cargo sidebar stops competing for screen space.

## Non-goals

- No changes to the marketplace (orderbook, settlement, order form, my-orders panel, etc.). The market panel is repositioned and auto-opened on dock; its internals are untouched.
- No new persistence, no new server-authoritative behavior. The transfer/sell ops in use today (`InventoryTransferMsg`, `SellBankItemMsg`) cover all bank-panel actions.
- No new visual polish pass on the marketplace panel. Its styling stays.
- No replacement for the equipment shop. Equipment acquisition is out of scope; admin commands suffice for now.

## Equipment-shop removal (server)

Full clean cut. No aliases, no deprecation stubs, no proto-field reservations (per the project's standing convention).

Files and their changes:

- `proto/gamepb/game.proto`
  - Delete `GCE_SHOP_BUY` from `GameClientEventCode` and renumber any later values from 1.
  - Delete `ShopBuyMsg`.
  - Delete `buy_price` field from `ItemDef` and renumber.
- `internal/item/item.go`
  - Delete `BuyPrice float64` field on `ItemDef`.
  - Delete every `BuyPrice:` literal in the seeded item table.
- `internal/game/entity_ship.go`
  - Delete the two `BuyPrice: float32(def.BuyPrice)` lines (currently at lines 158 and 232).
- `internal/game/world.go`
  - Delete `PendingShopBuy` struct.
- `internal/game/system_economy.go`
  - Delete the `processShopBuys` method and its call site in `EconomySystem.Update` (currently lines 42–43, 313+).
- `internal/game/input_handlers.go`
  - Delete the `mmokit.OnInput[gamepb.ShopBuyMsg]` registration block.
- `internal/game/logcat.go`
  - Delete `CatEconomyShop` constant and remove it from the log-category registration list.

After regenerating proto + SDK (`just proto` + `just client-sdk`), the auto-regenerated `web-pixi/sdk/client.ts` will lose `sendShopBuy`. No hand edits to SDK files.

## Equipment-shop removal (client)

- `web-pixi/index.html` — delete `<h3>Equipment Shop</h3>` + `<div id="shop-rows"></div>` from the bank panel block (lines ~567–568). The full bank-panel block is rewritten in this same change set; see UI section.
- `web-pixi/src/ui/bank.ts` — delete the `shopRowsEl` delegation handler and the `bank-shop` memoized rebuild block. (The full file is rewritten; see UI section.)
- `web-pixi/src/ui/hud.ts` — delete the `if (def.buyPrice > 0) lines.push(\`Buy: ${...}\`)` line at line 133.
- `web-pixi/src/state.ts` — delete `buyPrice: number;` field from `ItemDef`.
- `web-pixi/src/network.ts` — delete `buyPrice: def.buyPrice,` line at line 196.

`internal/marketplace/settlement_test.go` has `TestSellOrder_NoMatchWhenBuyPriceTooLow` — that "buy_price" is the marketplace-order bid price, not the shop, so it stays untouched.

## Docked-station UI

### Layout container

A new `#station-panels` element wraps the bank and market panels:

```html
<div id="station-panels">
  <div id="bank-panel">…</div>
  <div id="marketplace-panel">…</div>
</div>
```

CSS:

```css
#station-panels {
  position: absolute; top: 50%; left: 50%;
  transform: translate(-50%, -50%);
  display: flex; gap: 12px;
  z-index: 25;  /* above HUD, below modals like esc-menu */
}
#station-panels > #bank-panel,
#station-panels > #marketplace-panel {
  position: static;     /* override the existing absolute centering */
  transform: none;
}
```

The existing `#bank-panel` and `#marketplace-panel` rules keep their borders and padding; only their positioning rules collapse when they live inside `#station-panels`.

### Bank panel internal layout

```text
┌── STATION ──────────────────────┐
│                                 │
│   LOADOUT                       │
│   ┌─────┐ ┌─────┐               │
│   │ L1  │ │ R1  │               │
│   ├─────┤ ├─────┤               │
│   │ L2  │ │ R2  │               │
│   └─────┘ └─────┘               │
│                                 │
│   ITEM        BANK  CARGO   …   │
│   Iron Ore     80    20    ↔ $  │
│   Copper       40     —    ↔ $  │
│   Asteroid     —     60    ↔    │
│   Flux        500     0    ↔    │
│                                 │
│   120/1000 mass | Flux: 500     │
└─────────────────────────────────┘
```

Width: ~360px (was ~320 with the shop section). Height: vertically anchored, scrolls internally if the table overflows.

#### Loadout subsection

The 4-slot equip grid is pulled out of `#cargo-panel` (today's right-rail sidebar) and added to the bank panel as the first section under the header. Slot rendering, drag-and-drop semantics, and equip/unequip messages are unchanged. Drag sources expand: dragging starts from the unified table's bank or cargo qty cells (in addition to today's source, which was cargo-panel rows).

#### Unified bank/cargo table

Rows are the union of the keys in `state.bankItems` and `state.dockedCargoItems`, sorted by item id. Columns:

| Column | Source | Render rule |
| --- | --- | --- |
| ITEM | `state.itemDefs.get(itemId).name` | colored by `ITEM_COLORS_CSS[itemId]` (existing convention) |
| BANK | `state.bankItems.get(itemId)` | integer floor; `—` when 0 or absent |
| CARGO | `state.dockedCargoItems.get(itemId)` | integer floor; `—` when 0 or absent |
| ↔ | smart transfer | always rendered when row has any qty on either side |
| $ | sell-from-bank | rendered iff `def.sellPrice > 0` and bank qty > 0 |

`—` and `0` render the same way (`—`); a row never appears with both sides at 0.

`↔` smart-transfer rule:

- If cargo qty > 0 → deposit (cargo → bank). All by default; half on shift+click.
- Else if bank qty > 0 → withdraw (bank → cargo). All by default; half on shift+click.

This collapses today's two separate buttons (Withdraw / Deposit) into one whose direction is implied by where the item currently is. When an item is in both, "deposit" is the conservative default (shipping cargo to safe storage; deliberate withdraws are a less common workflow at a station).

`$` sell button: identical wire behavior to today (`SellBankItemMsg`), no quantity prompt, click = sell all, shift+click = sell half.

#### Footer

Unchanged: `<bank mass>/<max> mass | <currency name>: <floor(balance)>`.

### Cargo-panel sidebar (`C` key) behavior

While docked, `#cargo-panel` is force-hidden. The bank panel covers loadout + cargo viewing. `C` key bind is a no-op while docked (or trivially routes focus to the bank-panel loadout grid; not load-bearing).

While undocked, `C` continues to toggle `#cargo-panel` exactly as today.

### Open / close flow

The market is already station-bound today: `M` is gated on `state.isDocked` and the market panel renders nothing when undocked. The bank already auto-opens on dock (`network.ts` sets `bankPanelOpen = true` in the `DockedMsg` handler). The redesign mostly *adds* "market auto-opens too."

| Trigger | `#bank-panel` | `#marketplace-panel` | `#cargo-panel` |
| --- | --- | --- | --- |
| Dock (`DockedMsg` received) | open (existing) | **open (new — was: closed until M)** | force-hidden (new) |
| Undock | close (existing) | close (existing) | restored to user's prior toggle state |
| `M` while docked | — | toggle (existing) | — |
| `M` while undocked | — | no-op (existing — already gated) | — |
| `C` while undocked | — | — | toggle (existing) |
| `C` while docked | no-op (new) | — | no-op (new — sidebar is force-hidden) |
| Click `×` on bank panel | close | — | — |
| Click `×` on market panel | — | close (existing) | — |

Closing one panel via `×` while still docked leaves the other open. The bank panel grows a new `×` close button (it doesn't have one today). Re-opening the bank after manual-close while still docked is deferred — undock + redock resets both.

### State plumbing

`state.isDocked`, `state.bankPanelOpen`, and `state.marketPanelOpen` all already exist. The diff is one line — in the `DockedMsg` handler in `network.ts`, set `state.marketPanelOpen = true` alongside the existing `state.bankPanelOpen = true`. Undock teardown is already correct.

Cargo-panel hide-on-dock: one CSS rule plus a class toggle. Add `body.classList.toggle('docked', state.isDocked)` to the dock state-change site (immediately after the `state.isDocked = …` writes in `network.ts`, both branches), and the rule:

```css
body.docked #cargo-panel { display: none !important; }
```

## Test plan

- Manual smoke after change: `just dev`, log in, dock at the station, confirm bank+market both appear as a centered pair; cargo-panel hidden; bank table shows union of items; ↔ deposits/withdraws as described; `$` sells; undock closes both; `M` reopens market while undocked.
- `go vet ./...` after server changes.
- `just proto` + `just client-sdk examples/4node-basic` regenerate without errors.
- No new automated tests required — every server-side change is a deletion of dead-feature code; the bank panel rewrite is UI only.
- Manually verify the marketplace panel still works exactly as today (place buy, place sell, cancel, my-orders refresh).

## Risks / open questions

- The bank panel is currently centered horizontally; once it's inside `#station-panels` flex, it loses that. If users dock on a narrow viewport (<1280px), bank+market may overflow. Acceptable for now — the dev target is 1920×1080+ and the marketplace panel was already 900px wide. If it bites later, fix with a responsive media query collapsing to vertical stack.
- Drag-and-drop sources change. Today drag-sources are cargo-panel rows; in the new design they're bank-table cargo cells. The drag-and-drop code needs to be re-pointed at the new selectors. No new code, just an event-target rewire.
- `M` key wasn't previously gated on docking. Confirm pressing `M` while docked is harmless (today it would toggle market visibility, which while docked would mean closing the just-opened market). The proposed rule "M while docked = no-op" is a deliberate behavior change; minor, but worth flagging.

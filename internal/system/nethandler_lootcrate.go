package system

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// LootCrateNetHandler handles network serialization for loot crate entities.
type LootCrateNetHandler struct{}

func (h *LootCrateNetHandler) EntityType() uint8 { return component.TypeLootCrate }

func (h *LootCrateNetHandler) HashSnapshot(hasher *SnapshotHasher, ctx *NetworkContext, entry spatial.Entry) {
	gw := ctx.GW
	if gw.C.Inventory.HasAll(entry.Entity) {
		inv := gw.C.Inventory.Get(entry.Entity)
		var checksum int64
		for id, qty := range inv.Items {
			checksum += int64(id) * int64(qty)
		}
		hasher.Int64(checksum)
	}
}

func (h *LootCrateNetHandler) Serialize(state *gamepb.EntityState, ctx *NetworkContext, entry spatial.Entry) {
	gw := ctx.GW
	lc := &gamepb.LootCrateState{}
	if gw.C.Inventory.HasAll(entry.Entity) {
		inv := gw.C.Inventory.Get(entry.Entity)
		for itemID, qty := range inv.Items {
			if qty > 0 {
				lc.CargoItems = append(lc.CargoItems, &gamepb.InventoryItem{
					ItemId:   itemID,
					Quantity: qty,
				})
			}
		}
	}
	state.TypeData = &gamepb.EntityState_LootCrate{LootCrate: lc}
}

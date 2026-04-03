package system

import (
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/system"
)

// LootCrateNetHandler handles network serialization for loot crate entities.
type LootCrateNetHandler struct {
	gw  *game.GameWorld
	ctx *gameNetContext
}

func (h *LootCrateNetHandler) EntityType() uint8 { return component.TypeLootCrate }

func (h *LootCrateNetHandler) Hash(hasher *system.Hasher, viewer *system.ViewerInfo, entry spatial.Entry) {
	hashBaseFields(hasher, h.gw, h.ctx, viewer, entry)
	if h.gw.C.Inventory.HasAll(entry.Entity) {
		inv := h.gw.C.Inventory.Get(entry.Entity)
		var checksum int64
		for id, qty := range inv.Items {
			checksum += int64(id) * int64(qty)
		}
		hasher.Int64(checksum)
	}
}

func (h *LootCrateNetHandler) Snapshot(w *quantize.SnapshotWriter, viewer *system.ViewerInfo, entry spatial.Entry) {
	snapshotBaseFields(w, h.gw, h.ctx, viewer, entry)

	// Variable-length tail: uint16 byte-length prefix + [uint8 count, (uint32 itemID + uint32 qty) per item].
	// DeltaEncoder expects the tail to start with a uint16 big-endian byte length.
	if h.gw.C.Inventory.HasAll(entry.Entity) {
		inv := h.gw.C.Inventory.Get(entry.Entity)
		var count uint8
		for _, qty := range inv.Items {
			if qty > 0 {
				count++
			}
		}
		tailByteLen := uint16(1 + int(count)*8) // 1 byte count + 8 bytes per item
		w.Uint16(tailByteLen)
		w.Uint8(count)
		for itemID, qty := range inv.Items {
			if qty > 0 {
				w.Uint32(itemID)
				w.Uint32(uint32(qty))
			}
		}
	} else {
		w.Uint16(1) // 1 byte tail (just the count=0)
		w.Uint8(0)
	}
}

// SnapshotLayout returns field sizes for delta encoding.
// Base (10 fields) + variable-length tail (-1) for inventory items.
func (h *LootCrateNetHandler) SnapshotLayout() []int {
	return []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1, -1}
}

func (h *LootCrateNetHandler) InitialData(viewer *system.ViewerInfo, entry spatial.Entry) []byte {
	return nil
}



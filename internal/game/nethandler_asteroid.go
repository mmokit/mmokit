package game

import (
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/system"
)

// AsteroidNetHandler handles network serialization for asteroid entities.
type AsteroidNetHandler struct {
	gw  *GameWorld
	ctx *gameNetContext
}

func (h *AsteroidNetHandler) EntityType() uint8 { return component.TypeAsteroid }

func (h *AsteroidNetHandler) Hash(hasher *system.Hasher, viewer *system.ViewerInfo, entry spatial.Entry) {
	hashBaseFields(hasher, h.gw, h.ctx, viewer, entry)
	if h.gw.C.Minable.HasAll(entry.Entity) {
		minable := h.gw.C.Minable.Get(entry.Entity)
		hasher.Uint32(minable.ItemID)
		hasher.Float32(minable.Remaining)
	}
}

func (h *AsteroidNetHandler) Snapshot(w *quantize.SnapshotWriter, viewer *system.ViewerInfo, entry spatial.Entry) {
	snapshotBaseFields(w, h.gw, h.ctx, viewer, entry)

	var itemID uint32
	var remaining float32
	if h.gw.C.Minable.HasAll(entry.Entity) {
		minable := h.gw.C.Minable.Get(entry.Entity)
		itemID = minable.ItemID
		remaining = minable.Remaining
	}
	w.Uint32(itemID)
	w.Float32(remaining)
}

// SnapshotLayout returns field sizes for delta encoding.
// Base (10 fields) + itemID(4) + remaining(4)
func (h *AsteroidNetHandler) SnapshotLayout() []int {
	return []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1, 4, 4}
}

func (h *AsteroidNetHandler) InitialData(viewer *system.ViewerInfo, entry spatial.Entry) []byte {
	return nil
}

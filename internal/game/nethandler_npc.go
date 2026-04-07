package game

import (
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/system"
)

// NpcNetHandler handles network serialization for NPC entities.
type NpcNetHandler struct {
	gw  *GameWorld
	ctx *gameNetContext
}

func (h *NpcNetHandler) EntityType() uint8 { return component.TypeNPC }

func (h *NpcNetHandler) Hash(hasher *system.Hasher, viewer *system.ViewerInfo, entry spatial.Entry) {
	hashBaseFields(hasher, h.gw, h.ctx, viewer, entry)
	hashCombat(hasher, h.gw, entry.Entity)
}

func (h *NpcNetHandler) Snapshot(w *quantize.SnapshotWriter, viewer *system.ViewerInfo, entry spatial.Entry) {
	snapshotBaseFields(w, h.gw, h.ctx, viewer, entry)
	snapshotCombat(w, h.gw, entry.Entity)
}

// SnapshotLayout returns field sizes for delta encoding.
// Base (10 fields) + healthCur(2) + healthMax(2) + shieldCur(2) + shieldMax(2)
func (h *NpcNetHandler) SnapshotLayout() []int {
	return []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1, 2, 2, 2, 2}
}

func (h *NpcNetHandler) InitialData(viewer *system.ViewerInfo, entry spatial.Entry) []byte {
	return nil
}

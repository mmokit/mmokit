package system

import (
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/system"
)

// StationNetHandler handles network serialization for station entities.
// Stations have no mutable type-specific state.
type StationNetHandler struct {
	gw  *game.GameWorld
	ctx *gameNetContext
}

func (h *StationNetHandler) EntityType() uint8 { return component.TypeStation }

func (h *StationNetHandler) Hash(hasher *system.Hasher, viewer *system.ViewerInfo, entry spatial.Entry) {
	hashBaseFields(hasher, h.gw, h.ctx, viewer, entry)
}

func (h *StationNetHandler) Snapshot(w *quantize.SnapshotWriter, viewer *system.ViewerInfo, entry spatial.Entry) {
	snapshotBaseFields(w, h.gw, h.ctx, viewer, entry)
}

// SnapshotLayout returns field sizes for delta encoding.
// Base fields only (10 fields).
func (h *StationNetHandler) SnapshotLayout() []int {
	return []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1}
}

func (h *StationNetHandler) InitialData(viewer *system.ViewerInfo, entry spatial.Entry) []byte {
	return nil
}

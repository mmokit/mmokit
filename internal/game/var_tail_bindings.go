package game

import (
	"slices"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/system"
)

// StatusEffectDurationScale is the [0, StatusEffectDurationScale] range used
// to quantize effect duration into a qnorm byte (0-255). 25.5s with 0.1s
// resolution is a reasonable default: ion burn lasts a few seconds, afterburner
// up to ~10s, fortified up to ~15s. Effects with duration > 25.5s saturate.
const StatusEffectDurationScale = 25.5

// NewStatusEffectsBinding returns a ComponentBinding that emits every active
// status effect as a 2-byte record: [u8 type][qnorm duration/StatusEffectDurationScale].
// Inactive slots (i >= Count) are not emitted.
func NewStatusEffectsBinding(m *ecs.Map1[gamecomp.StatusEffects]) system.ComponentBinding {
	return system.VarTailComponent(m, system.VarTailAccessor[gamecomp.StatusEffects]{
		Name:     "statusEffects",
		ItemSize: 2,
		ItemFields: []system.BindingSchemaField{
			{Name: "type", Encoding: "u8", Size: 1},
			{Name: "duration", Encoding: "qnorm", Size: 1, Scale: StatusEffectDurationScale},
		},
		Count: func(se *gamecomp.StatusEffects) int { return int(se.Count) },
		WriteItems: func(se *gamecomp.StatusEffects, w *quantize.SnapshotWriter) {
			for i := uint8(0); i < se.Count; i++ {
				eff := se.Effects[i]
				w.Uint8(uint8(eff.Type))
				// QNorm clamps to [0,1]; scale Duration into that range.
				w.QNorm(eff.Duration / StatusEffectDurationScale)
			}
		},
		HashItems: func(se *gamecomp.StatusEffects, h *system.Hasher) {
			// Hash the same quantized byte that WriteItems emits. Hashing raw
			// Duration would cause false positives every tick as the float
			// drifts smoothly even though the qnorm byte is unchanged.
			for i := uint8(0); i < se.Count; i++ {
				eff := se.Effects[i]
				h.Uint8(uint8(eff.Type))
				h.Uint8(quantize.Norm(eff.Duration / StatusEffectDurationScale))
			}
		},
	})
}

// NewInventoryBinding returns a ComponentBinding that emits every inventory
// item as an 8-byte record: [u32 itemID][u32 quantity]. Map iteration order
// is sorted by item ID for deterministic hashing.
func NewInventoryBinding(m *ecs.Map1[gamecomp.Inventory]) system.ComponentBinding {
	return system.VarTailComponent(m, system.VarTailAccessor[gamecomp.Inventory]{
		Name:     "items",
		ItemSize: 8,
		ItemFields: []system.BindingSchemaField{
			{Name: "itemId", Encoding: "u32", Size: 4},
			{Name: "quantity", Encoding: "u32", Size: 4},
		},
		Count: func(inv *gamecomp.Inventory) int {
			count := 0
			for _, qty := range inv.Items {
				if qty > 0 {
					count++
				}
			}
			return count
		},
		WriteItems: func(inv *gamecomp.Inventory, w *quantize.SnapshotWriter) {
			keys := sortedInventoryKeys(inv)
			for _, id := range keys {
				qty := inv.Items[id]
				if qty <= 0 {
					continue
				}
				w.Uint32(id)
				w.Uint32(uint32(qty))
			}
		},
		HashItems: func(inv *gamecomp.Inventory, h *system.Hasher) {
			keys := sortedInventoryKeys(inv)
			for _, id := range keys {
				qty := inv.Items[id]
				if qty <= 0 {
					continue
				}
				h.Uint32(id)
				h.Uint32(uint32(qty))
			}
		},
	})
}

// sortedInventoryKeys returns the inventory's item IDs sorted ascending.
// Used for deterministic iteration when writing/hashing.
func sortedInventoryKeys(inv *gamecomp.Inventory) []uint32 {
	if len(inv.Items) == 0 {
		return nil
	}
	keys := make([]uint32, 0, len(inv.Items))
	for id := range inv.Items {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	return keys
}

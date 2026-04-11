package universe

import (
	"github.com/zenion/mmoserver/pkg/replication"
)

// CollectBaselinesForEntity builds a slice of ClientBaselineEntry for
// Case A handoff (non-player entity crosses a cell boundary). For each
// subscribed client that has an acknowledged baseline for this one
// entity, the helper produces a ClientBaselineEntry carrying that
// baseline's acked snapshot bytes.
//
// The returned slice is stable — LastAcked byte slices are deep-copied
// so the handover payload is not affected by subsequent mutations of
// the source stores.
//
// connStores is typically built by the handoff state machine iterating
// the client replication system's connection map. Pass an empty or nil
// map for an entity with no subscribers (the helper returns an empty
// slice in that case).
//
// LastTick is set to 0 in the produced entries. Callers that have
// per-entity tick metadata may overwrite it post-collection; Phase 7
// evaluates whether this is needed for the co-simulation handoff
// accounting.
func CollectBaselinesForEntity(
	connStores map[uint32]*replication.BaselineStore,
	entityNetID uint32,
) []ClientBaselineEntry {
	if len(connStores) == 0 {
		return nil
	}
	out := make([]ClientBaselineEntry, 0, len(connStores))
	for connID, store := range connStores {
		if store == nil {
			continue
		}
		bl := store.Baseline(entityNetID)
		if bl == nil {
			continue
		}
		out = append(out, ClientBaselineEntry{
			ConnID:      connID,
			EntityNetID: entityNetID,
			LastAcked:   cloneBytes(bl.Acked),
			LastTick:    0,
		})
	}
	return out
}

// CollectBaselinesForPlayer builds a slice of ClientBaselineEntry for
// Case B handoff (player crosses a cell boundary). For each entity in
// the player's own baseline store, the helper produces a
// ClientBaselineEntry carrying that entity's acked snapshot bytes.
//
// The resulting slice is larger than Case A (one entry per entity in
// the player's AoI view rather than one entry per subscribed client),
// but still bounded — typically 50-200 entries for a single player.
//
// Passing a nil store returns a nil slice. LastAcked is deep-copied
// per the same stability requirement as Case A.
func CollectBaselinesForPlayer(
	connID uint32,
	store *replication.BaselineStore,
) []ClientBaselineEntry {
	if store == nil {
		return nil
	}
	var out []ClientBaselineEntry
	store.ForEachBaseline(func(netID uint32, bl *replication.EntityBaseline) {
		out = append(out, ClientBaselineEntry{
			ConnID:      connID,
			EntityNetID: netID,
			LastAcked:   cloneBytes(bl.Acked),
			LastTick:    0,
		})
	})
	return out
}

// cloneBytes returns a fresh copy of src or nil if src is empty.
// Used by the handover helpers to ensure the payload owns stable
// bytes independent of the source baseline's mutable state.
func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	out := make([]byte, len(src))
	copy(out, src)
	return out
}

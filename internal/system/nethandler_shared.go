package system

import (
	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
)

// hashCombat hashes health, shield, and status effect types+count into the hasher.
func hashCombat(h *SnapshotHasher, gw *game.GameWorld, entity ecs.Entity) {
	if gw.C.Health.HasAll(entity) {
		hp := gw.C.Health.Get(entity)
		h.Float32(hp.Current)
		h.Float32(hp.Max)
	}
	if gw.C.Shield.HasAll(entity) {
		sh := gw.C.Shield.Get(entity)
		h.Float32(sh.Current)
		h.Float32(sh.Max)
	}
	if gw.C.StatusEffects.HasAll(entity) {
		se := gw.C.StatusEffects.Get(entity)
		h.Uint8(se.Count)
		for i := uint8(0); i < se.Count; i++ {
			h.Uint8(uint8(se.Effects[i].Type))
		}
	}
}

// serializeCombat builds a CombatState proto from the entity's health, shield,
// and status effect components.
func serializeCombat(gw *game.GameWorld, entity ecs.Entity) *gamepb.CombatState {
	cs := &gamepb.CombatState{}
	if gw.C.Health.HasAll(entity) {
		hp := gw.C.Health.Get(entity)
		cs.Health = hp.Current
		cs.MaxHealth = hp.Max
	}
	if gw.C.Shield.HasAll(entity) {
		sh := gw.C.Shield.Get(entity)
		cs.Shield = sh.Current
		cs.MaxShield = sh.Max
	}
	if gw.C.StatusEffects.HasAll(entity) {
		se := gw.C.StatusEffects.Get(entity)
		for i := uint8(0); i < se.Count; i++ {
			cs.StatusEffects = append(cs.StatusEffects, &gamepb.ActiveStatusEffect{
				Type:      gamepb.StatusEffectType(se.Effects[i].Type),
				Remaining: se.Effects[i].Duration,
			})
		}
	}
	return cs
}

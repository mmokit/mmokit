package system

import (
	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/game"
)

// hashCombat hashes health, shield, and status effect types+count into the hasher.
func hashCombat(h *SnapshotHasher, gw *game.GameWorld, entity ecs.Entity) {
	if gw.HealthMap.HasAll(entity) {
		hp := gw.HealthMap.Get(entity)
		h.Float32(hp.Current)
		h.Float32(hp.Max)
	}
	if gw.ShieldMap.HasAll(entity) {
		sh := gw.ShieldMap.Get(entity)
		h.Float32(sh.Current)
		h.Float32(sh.Max)
	}
	if gw.StatusEffectsMap.HasAll(entity) {
		se := gw.StatusEffectsMap.Get(entity)
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
	if gw.HealthMap.HasAll(entity) {
		hp := gw.HealthMap.Get(entity)
		cs.Health = hp.Current
		cs.MaxHealth = hp.Max
	}
	if gw.ShieldMap.HasAll(entity) {
		sh := gw.ShieldMap.Get(entity)
		cs.Shield = sh.Current
		cs.MaxShield = sh.Max
	}
	if gw.StatusEffectsMap.HasAll(entity) {
		se := gw.StatusEffectsMap.Get(entity)
		for i := uint8(0); i < se.Count; i++ {
			cs.StatusEffects = append(cs.StatusEffects, &gamepb.ActiveStatusEffect{
				Type:      gamepb.StatusEffectType(se.Effects[i].Type),
				Remaining: se.Effects[i].Duration,
			})
		}
	}
	return cs
}

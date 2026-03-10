package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
)

// StatusEffectSystem ticks down status effects and applies per-tick effects (e.g. Ion Burn DoT).
type StatusEffectSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter1[component.StatusEffects]
}

func NewStatusEffectSystem(gw *game.GameWorld) *StatusEffectSystem {
	return &StatusEffectSystem{gw: gw}
}

func (s *StatusEffectSystem) Update(dt float32) {
	gw := s.gw
	if s.filter == nil {
		s.filter = ecs.NewFilter1[component.StatusEffects](gw.ECS)
	}

	query := s.filter.Query()
	for query.Next() {
		se := query.Get()
		entity := query.Entity()

		// Apply per-tick effects before ticking down durations
		for i := uint8(0); i < se.Count; i++ {
			eff := &se.Effects[i]
			switch eff.Type {
			case component.StatusIonBurn:
				sourceNetID := uint32(0)
				if gw.ECS.Alive(eff.Source) && gw.NetworkIDMap.HasAll(eff.Source) {
					sourceNetID = gw.NetworkIDMap.Get(eff.Source).ID
				}
				gw.ApplyDamage(entity, eff.Value*dt, sourceNetID)
			}
		}

		se.TickDown(dt)
	}
}

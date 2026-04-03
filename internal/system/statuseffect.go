package system

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// StatusEffectSystem ticks down status effects and applies per-tick effects (e.g. Ion Burn DoT).
type StatusEffectSystem struct {
	mmokit.SystemBase
	gw     *game.GameWorld
	filter *ecs.Filter1[gamecomp.StatusEffects]
}

func (s *StatusEffectSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.filter = ecs.NewFilter1[gamecomp.StatusEffects](s.ECSWorld()).Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}

func (s *StatusEffectSystem) Update(dt float32) {
	gw := s.gw

	query := s.filter.Query()
	for query.Next() {
		se := query.Get()
		entity := query.Entity()

		// Apply per-tick effects before ticking down durations
		for i := uint8(0); i < se.Count; i++ {
			eff := &se.Effects[i]
			switch eff.Type {
			case gamecomp.StatusIonBurn:
				sourceNetID := uint32(0)
				if gw.ECS.Alive(eff.Source) && gw.C.NetworkID.HasAll(eff.Source) {
					sourceNetID = gw.C.NetworkID.Get(eff.Source).ID
				}
				gw.ApplyDamage(entity, eff.Value*dt, sourceNetID)
			case gamecomp.StatusShieldRegen:
				if gw.C.Shield.HasAll(entity) {
					shield := gw.C.Shield.Get(entity)
					shield.Current = min(shield.Current+eff.Value*dt, shield.Max)
				}
			}
		}

		se.TickDown(dt)
	}
}

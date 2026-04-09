package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ShipDynamicsSystem keeps per-ship movement parameters (MoveParams.MaxSpeed)
// in sync with the ship's configured MaxSpeed and any active speed-modifying
// status effects (afterburner). This replaces the speed/afterburner portion of
// the former ShipControlSystem now that click-to-move is handled by the
// generic pkg/system ClickToMoveSystem.
//
// Runs before ClickToMoveSystem so that each tick's movement uses the
// up-to-date effective max speed.
type ShipDynamicsSystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	entities mmokit.Query[struct {
		Ship   *gamecomp.ShipControl
		Params *mmokit.MoveParams
		Status *gamecomp.StatusEffects `ecs:"optional"`
	}]
}

func (s *ShipDynamicsSystem) Init() {
	s.gw = gwFromSystem(s.SystemBase)
	s.entities.Init(s)
}

func (s *ShipDynamicsSystem) Update(dt float32) {
	for _, b := range s.entities.All() {
		maxSpeed := b.Ship.MaxSpeed
		if b.Status != nil {
			if eff := b.Status.Get(gamecomp.StatusAfterburner); eff != nil {
				maxSpeed *= eff.Value
			}
		}
		b.Params.MaxSpeed = maxSpeed
	}
}

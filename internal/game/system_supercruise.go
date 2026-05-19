package game

import (
	"math"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// SupercruiseSystem ticks the Z-bound travel-mode state machine.
//
// Phase transitions:
//
//	Channeling → Active  (channel timer hits 0)
//	Idle/Channeling/Active → Idle  (handled by damage hook + auto-cancel sites)
//	LockoutRemaining decrements every tick regardless of phase
//
// Active-phase speed boost flows through the existing StatusEffects path:
// at Channeling→Active the system adds a StatusSupercruise effect; on
// cancel/knockout, callers remove it via cancelSupercruise.
type SupercruiseSystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	entities mmokit.Query[struct {
		SC *gamecomp.Supercruise
		H  *gamecomp.Health
		SE *gamecomp.StatusEffects
		MT *mmokit.MoveTarget `ecs:"optional"`
	}]
}

func (s *SupercruiseSystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *SupercruiseSystem) Update(dt float32) {
	gw := s.gw

	for e, b := range s.entities.Iter {
		sc, h, se, mt := b.SC, b.H, b.SE, b.MT
		entity := mmokit.EntityFromECS(gw.stage, e)

		// Tick lockout regardless of phase.
		if sc.LockoutRemaining > 0 {
			sc.LockoutRemaining -= dt
			if sc.LockoutRemaining < 0 {
				sc.LockoutRemaining = 0
			}
		}

		switch sc.Phase {
		case gamecomp.SupercruiseChanneling:
			sc.ChannelRemaining -= dt
			// Keep player rooted while channeling.
			if mt != nil {
				mt.Active = false
			}
			if sc.ChannelRemaining <= 0 {
				sc.ChannelRemaining = 0
				sc.Phase = gamecomp.SupercruiseActive
				sc.BufferMax = h.Max * gw.Config.SupercruiseBufferPct
				sc.BufferHP = sc.BufferMax
				se.Add(gamecomp.StatusEffect{
					Type:     gamecomp.StatusSupercruise,
					Duration: math.MaxFloat32,
					Value:    gw.Config.SupercruiseSpeedMul,
				})
				gw.eng.Log.Log(CatSupercruise, "active: netID=%d buffer=%.1f", entity.NetID(), sc.BufferMax)
			}
		case gamecomp.SupercruiseActive:
			// Transitions out happen in damage hook + cancel sites.
		case gamecomp.SupercruiseIdle:
			// Nothing to tick beyond lockout.
		}
	}
}

// cancelSupercruise transitions a ship out of supercruise (any phase) back
// to Idle. Removes the StatusSupercruise speed effect if present. Does NOT
// stamp lockout — combat lockout is handled exclusively in verb_damage.go.
// Safe to call when Phase is already Idle (no-op).
func cancelSupercruise(e mmokit.Entity) {
	sc := mmokit.Get[gamecomp.Supercruise](e)
	if sc == nil || sc.Phase == gamecomp.SupercruiseIdle {
		return
	}
	if sc.Phase == gamecomp.SupercruiseActive {
		if se := mmokit.Get[gamecomp.StatusEffects](e); se != nil {
			for i := uint8(0); i < se.Count; i++ {
				if se.Effects[i].Type == gamecomp.StatusSupercruise {
					se.Remove(i)
					break
				}
			}
		}
	}
	sc.Phase = gamecomp.SupercruiseIdle
	sc.ChannelRemaining = 0
	sc.BufferHP = 0
	sc.BufferMax = 0
}

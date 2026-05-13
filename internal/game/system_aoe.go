package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// FactionMask bits (game-defined; passed to AoESpec.FactionMask).
const (
	FactionMaskNPC    uint8 = 0x01
	FactionMaskPlayer uint8 = 0x02
	FactionMaskAll    uint8 = 0x03
)

// AoESystem resolves expired AoEMarker entities. Each tick:
//  1. For markers with Lifetime.Remaining <= 0:
//     a. Query the spatial grid in Spec.Radius around marker position.
//     b. For each non-owner hit matching Spec.FactionMask, call
//     gw.ApplyDamage.
//     c. Despawn the marker via Commands().
//
// Registered AFTER ProjectileSystem and NPCAISystem (see factory.go) so
// instant-lifetime markers (Kamikaze detonate, projectile splash)
// resolve same-tick as their spawn.
type AoESystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	nearby   []mmokit.SpatialEntry // reusable scratch buffer
	entities mmokit.Query[struct {
		Pos      *mmokit.Position
		Lifetime *mmokit.Lifetime
		Spec     *gamecomp.AoESpec
	}]
}

func (s *AoESystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
	s.nearby = make([]mmokit.SpatialEntry, 0, 32)
}

func (s *AoESystem) Update(dt float32) {
	gw := s.gw
	for entity, b := range s.entities.Iter {
		if b.Lifetime.Remaining > 0 {
			continue
		}
		marker := mmokit.EntityFromECS(gw.stage, entity)
		s.resolveMarker(marker, b.Pos, b.Spec)
		s.Commands().Despawn(marker.Handle())
	}
}

// resolveMarker queries the spatial grid and applies damage to all
// eligible victims in radius. Owner is always skipped.
func (s *AoESystem) resolveMarker(marker mmokit.Entity,
	pos *mmokit.Position, spec *gamecomp.AoESpec,
) {
	gw := s.gw
	r2 := spec.Radius * spec.Radius

	s.nearby = gw.Spatial.QueryRadius(pos.X, pos.Y, spec.Radius, s.nearby[:0])

	hits := 0
	for _, entry := range s.nearby {
		victim := mmokit.EntityFromECS(gw.stage, entry.Entity)
		if !victim.Alive() {
			continue
		}
		if victim.NetID() == spec.OwnerNetID {
			continue
		}
		vpos := mmokit.Get[mmokit.Position](victim)
		if vpos == nil {
			continue
		}
		dx, dy := vpos.X-pos.X, vpos.Y-pos.Y
		if dx*dx+dy*dy > r2 {
			continue
		}
		if !s.factionMatch(victim, spec.FactionMask) {
			continue
		}
		gw.ApplyDamage(victim, spec.Damage, spec.OwnerNetID)
		hits++
	}
	gw.eng.Log.Log(CatCombatHit, "aoe: resolved at (%.0f,%.0f) r=%.0f dmg=%.0f hits=%d",
		pos.X, pos.Y, spec.Radius, spec.Damage, hits)
}

// factionMatch returns true if the victim's faction matches the spec's
// FactionMask. NPCs match bit 0; players match bit 1; anything else
// (asteroids, stations, loot) is invulnerable.
func (s *AoESystem) factionMatch(victim mmokit.Entity, mask uint8) bool {
	if mmokit.Has[gamecomp.NPCAI](victim) {
		return mask&FactionMaskNPC != 0
	}
	if mmokit.Has[mmokit.PlayerConn](victim) {
		return mask&FactionMaskPlayer != 0
	}
	return false
}

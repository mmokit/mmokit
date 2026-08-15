package game

import (
	"time"

	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/examples/space/internal/item"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// DungeonChamberSystem drives per-chamber lifecycle:
// Active → Cleared (on roster death) → Cooldown → Active (after cooldown).
//
// Unlike POISystem (which fires bounty + leashes against a POI entity),
// chambers are pure server-side bookkeeping — they're tracked in
// GameWorld.dungeonChambers keyed by the dungeon entity's netID and have
// no entity of their own. The system iterates the map each tick.
type DungeonChamberSystem struct {
	mmokit.SystemBase
	gw *GameWorld
}

func (s *DungeonChamberSystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *DungeonChamberSystem) Update(dt float32) {
	now := time.Now().UnixNano()
	for dungeonNetID, chambers := range s.gw.dungeonChambers {
		for _, c := range chambers {
			s.tickChamber(dungeonNetID, c, now)
		}
	}
}

func (s *DungeonChamberSystem) tickChamber(dungeonNetID uint32, c *ChamberState, now int64) {
	gw := s.gw
	switch c.Status {
	case ChamberActive:
		// Prune dead from AliveNetIDs; remember the last live position so
		// the chest can land where the final mob fell (chamber center is
		// the fallback when no position is recoverable).
		alive := c.AliveNetIDs[:0]
		for _, id := range c.AliveNetIDs {
			e := mmokit.EntityByNetID(gw.stage, id)
			if !e.Alive() {
				continue
			}
			h := mmokit.Get[gamecomp.Health](e)
			if h == nil || h.Current <= 0 {
				// About to be despawned by the death observer — capture
				// position now while it's still readable.
				if p := mmokit.Get[mmokit.Position](e); p != nil {
					c.LastKillPos = spatial.Vec2{X: p.X, Y: p.Y}
				}
				continue
			}
			alive = append(alive, id)
		}
		c.AliveNetIDs = alive
		if len(alive) == 0 {
			c.Status = ChamberCleared
			c.ClearedAt = now
			gw.eng.Log.Log(CatDungeon, "chamber cleared: dungeon=%d chamber=%d role=%d",
				dungeonNetID, c.ID, c.Role)
		}

	case ChamberCleared:
		// Drop the chest, transition to cooldown. Flux amount depends on
		// chamber role; chest position uses the last-known kill position
		// (chamber center as fallback).
		var flux float32
		switch c.Role {
		case ChamberSideBoss:
			flux = gw.Config.ChamberSideBossFluxBase
		case ChamberTerminal:
			flux = gw.Config.ChamberTerminalBossFluxBase
		default:
			flux = gw.Config.ChamberMobPackFluxBase
		}
		pos := c.LastKillPos
		if pos.X == 0 && pos.Y == 0 {
			pos = c.Center
		}
		gw.SpawnLootCrate(pos.X, pos.Y, map[uint32]int32{
			item.CreditsItemID: int32(flux),
		})
		// SpawnLootCrate doesn't return the crate's netID; the chest
		// despawn-on-cooldown path will rely on its Lifetime to expire
		// (loot crates carry a Lifetime by default), so LastChestNetID
		// stays 0. If chest-recovery becomes important, expose a netID
		// from SpawnLootCrate in a later pass.
		c.LastChestNetID = 0
		c.Status = ChamberCooldown
		gw.eng.Log.Log(CatDungeon, "chamber chest spawned: dungeon=%d chamber=%d flux=%.0f pos=(%.0f,%.0f)",
			dungeonNetID, c.ID, flux, pos.X, pos.Y)

	case ChamberCooldown:
		cooldown := chamberCooldownFor(gw.Config, c.Role)
		elapsedSec := float32(now-c.ClearedAt) / 1e9
		if elapsedSec < cooldown {
			return
		}
		// Cooldown elapsed — repopulate the roster.
		c.RespawnCount++
		rngU64 := uint64(dungeonNetID) ^ uint64(c.ID+1)*0xbf58476d1ce4e5b9 ^ uint64(c.RespawnCount)
		c.RosterDefIdx = rosterIdxForRole(c.Role, rngU64)
		c.AliveNetIDs = nil
		c.ClearedAt = 0
		c.LastKillPos = spatial.Vec2{}
		gw.spawnChamberRoster(dungeonNetID, c)
		c.Status = ChamberActive
		gw.eng.Log.Log(CatDungeon, "chamber respawned: dungeon=%d chamber=%d respawn=%d",
			dungeonNetID, c.ID, c.RespawnCount)
	}
}

// chamberCooldownFor returns the configured cooldown (seconds) for a
// chamber role. Side-boss / terminal chambers cool down slower than
// regular mob packs.
func chamberCooldownFor(cfg *GameConfig, role ChamberRole) float32 {
	switch role {
	case ChamberSideBoss:
		return cfg.ChamberCooldownSideBoss
	case ChamberTerminal:
		return cfg.ChamberCooldownTerminal
	default:
		return cfg.ChamberCooldownMobPack
	}
}

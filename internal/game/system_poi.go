package game

import (
	"time"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// POISystem ticks POI lifecycle each frame: detects fully-cleared
// rosters, spawns the bounty crate, transitions to Cooldown, and
// repopulates the roster after the cooldown elapses.
type POISystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	entities mmokit.Query[struct {
		POI *gamecomp.POI
		Pos *mmokit.Position
	}]
}

func (s *POISystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *POISystem) Update(dt float32) {
	gw := s.gw
	now := time.Now().UnixNano()
	for e, b := range s.entities.Iter {
		poi, pos := b.POI, b.Pos
		ent := mmokit.EntityFromECS(gw.stage, e)
		poiNetID := ent.NetID()

		switch poi.Status {
		case gamecomp.POIStatusActive:
			s.tickActive(poi, pos, poiNetID)
		case gamecomp.POIStatusCooldown, gamecomp.POIStatusCleared:
			s.tickCooldown(poi, pos, poiNetID, now)
		}
	}
}

// tickActive scans the POI's tracked roster for dead/removed members.
// When the live set reaches zero the POI transitions to Cooldown via
// onClear (drops the bounty crate).
func (s *POISystem) tickActive(poi *gamecomp.POI, pos *mmokit.Position, poiNetID uint32) {
	gw := s.gw
	live := gw.poiRosters[poiNetID][:0]
	for _, nid := range gw.poiRosters[poiNetID] {
		e := mmokit.EntityByNetID(gw.stage, nid)
		if !e.Alive() {
			continue
		}
		if h := mmokit.Get[gamecomp.Health](e); h == nil || h.Current > 0 {
			live = append(live, nid)
		}
	}
	gw.poiRosters[poiNetID] = live

	if len(live) == 0 {
		s.onClear(poi, pos, poiNetID)
	}
}

// onClear drops the Flux bounty crate at the POI center and flips the
// component to Cooldown, stamping ClearedAt with server-local time.
func (s *POISystem) onClear(poi *gamecomp.POI, pos *mmokit.Position, poiNetID uint32) {
	gw := s.gw
	roster := rosterForIdx(poi.RosterDefIdx)
	rosterCount := 0
	for _, m := range roster.Members {
		rosterCount += m.Count
	}
	bounty := gw.Config.POIBaseClearFlux + gw.Config.POIPerKillFluxBonus*int32(rosterCount)
	gw.SpawnLootCrate(pos.X, pos.Y, map[uint32]int32{
		item.CreditsItemID: bounty,
	})

	poi.Status = gamecomp.POIStatusCooldown
	poi.ClearedAt = time.Now().UnixNano()
	gw.eng.Log.Log(CatPOI, "poi: cleared netID=%d bounty=%d roster=%s",
		poiNetID, bounty, roster.Name)
}

// tickCooldown advances cooldown timer; once the configured duration
// elapses the roster is respawned and Status flips back to Active.
func (s *POISystem) tickCooldown(poi *gamecomp.POI, pos *mmokit.Position, poiNetID uint32, now int64) {
	gw := s.gw
	cooldownSec := poiCooldownSec(gw)
	elapsed := now - poi.ClearedAt
	if elapsed < int64(cooldownSec)*int64(time.Second) {
		return
	}
	gw.spawnPOIRoster(pos.X, pos.Y, poiNetID, poi.RosterDefIdx)
	gw.poiRosters[poiNetID] = gw.collectRosterNetIDs(poiNetID)
	poi.Status = gamecomp.POIStatusActive
	gw.eng.Log.Log(CatPOI, "poi: repopulated netID=%d roster_size=%d",
		poiNetID, len(gw.poiRosters[poiNetID]))
}

// poiCooldownSec returns the cooldown duration (seconds) for POIs in
// this world's root cell. The station cell uses the shorter
// tutorial-friendly cooldown; all other cells use the standard one.
func poiCooldownSec(gw *GameWorld) int32 {
	if gw.RootCell == gw.Config.StationCell {
		return gw.Config.StationCellPOIClearCooldown
	}
	return gw.Config.NonStationCellPOIClearCooldown
}

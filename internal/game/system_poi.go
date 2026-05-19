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

// pendingPOIAction defers state-mutating work (loot-crate spawns,
// roster respawns, leash-component adds) until AFTER the
// entities.Iter query closes — modifying the ECS world inside the
// iteration trips ark's locked-world check.
type pendingPOIAction struct {
	poi      *gamecomp.POI
	pos      *mmokit.Position
	poiNetID uint32
	kind     uint8 // 0 = onClear, 1 = repopulate, 2 = leash roster
	leashIDs []uint32
}

func (s *POISystem) Update(dt float32) {
	gw := s.gw
	now := time.Now().UnixNano()
	var pending []pendingPOIAction
	for e, b := range s.entities.Iter {
		poi, pos := b.POI, b.Pos
		ent := mmokit.EntityFromECS(gw.stage, e)
		poiNetID := ent.NetID()

		switch poi.Status {
		case gamecomp.POIStatusActive:
			cleared, leashIDs := s.tickActive(poi, pos, poiNetID)
			if cleared {
				pending = append(pending, pendingPOIAction{poi: poi, pos: pos, poiNetID: poiNetID, kind: 0})
			} else if len(leashIDs) > 0 {
				pending = append(pending, pendingPOIAction{poi: poi, pos: pos, poiNetID: poiNetID, kind: 2, leashIDs: leashIDs})
			}
		case gamecomp.POIStatusCooldown, gamecomp.POIStatusCleared:
			if s.tickCooldown(poi, pos, poiNetID, now) {
				pending = append(pending, pendingPOIAction{poi: poi, pos: pos, poiNetID: poiNetID, kind: 1})
			}
		}
	}
	for _, a := range pending {
		switch a.kind {
		case 0:
			s.onClear(a.poi, a.pos, a.poiNetID)
		case 1:
			s.repopulate(a.poi, a.pos, a.poiNetID)
		case 2:
			s.applyLeash(a.poiNetID, a.leashIDs)
		}
	}
}

// tickActive scans the POI's tracked roster for dead/removed members
// and evaluates the roster-wide leash trigger. The leash policy is
// POI-wide, not per-NPC: if ANY surviving roster member crosses
// LeashRadius from the POI center, EVERY surviving roster member is
// flagged for leashing. NPCAISystem owns the actual return-to-anchor
// motion via tickLeash; POISystem is the authority on WHEN to add
// Leashing.
//
// Returns (cleared, leashIDs). The caller MUST defer both transitions
// until AFTER the entities.Iter query closes — loot-crate spawn
// (onClear) and mmokit.Set (applyLeash) both mutate the ECS world
// and would otherwise trip ark's locked-world check:
//   - cleared=true: roster is fully dead → run onClear
//   - leashIDs non-empty: any member out of range → run applyLeash
//
// Combat-activity is collected as a proxy for the spec's "6s no
// damage" de-escalation rule but is currently unused: NPCAI lacks a
// wall-clock timestamp, and the per-NPC tickEngage already drops to
// Idle on its own deescalation timer.
func (s *POISystem) tickActive(poi *gamecomp.POI, pos *mmokit.Position, poiNetID uint32) (bool, []uint32) {
	gw := s.gw
	live := gw.poiRosters[poiNetID][:0]
	rosterLeashNeeded := false
	rosterCombatActivity := false

	for _, nid := range gw.poiRosters[poiNetID] {
		e := mmokit.EntityByNetID(gw.stage, nid)
		if !e.Alive() {
			continue
		}
		if h := mmokit.Get[gamecomp.Health](e); h == nil || h.Current <= 0 {
			continue
		}
		live = append(live, nid)

		// Distance check from this member to the anchor.
		ppos := mmokit.Get[mmokit.Position](e)
		if ppos != nil {
			dx, dy := ppos.X-pos.X, ppos.Y-pos.Y
			if dx*dx+dy*dy > poi.LeashRadius*poi.LeashRadius {
				rosterLeashNeeded = true
			}
		}

		// Combat-activity proxy: pending damage hit OR active aggro.
		ai := mmokit.Get[gamecomp.NPCAI](e)
		if ai != nil && ai.LastDamageByNetID != 0 {
			rosterCombatActivity = true
		}
		if ai != nil && (ai.State == AIStateEngage || ai.State == AIStateAcquire || ai.State == AIStateApproach) {
			rosterCombatActivity = true
		}
	}
	gw.poiRosters[poiNetID] = live

	if len(live) == 0 {
		return true, nil
	}

	// rosterCombatActivity is computed but not currently used — kept
	// for the eventual full de-escalation rule (6s no damage = reset).
	_ = rosterCombatActivity

	if !rosterLeashNeeded {
		return false, nil
	}
	// Return a copy of the live roster — the caller will install
	// Leashing on each member after the query iteration closes.
	leashIDs := make([]uint32, len(live))
	copy(leashIDs, live)
	return false, leashIDs
}

// applyLeash installs the Leashing tag on every live roster member
// listed in ids, transitions each to AIStateLeash, and clears the
// target-lock slots. Must run OUTSIDE the entities.Iter query
// (mmokit.Set mutates the world).
func (s *POISystem) applyLeash(poiNetID uint32, ids []uint32) {
	gw := s.gw
	gw.eng.Log.Log(CatPOI, "poi: %d roster-wide leash triggered", poiNetID)
	for _, nid := range ids {
		e := mmokit.EntityByNetID(gw.stage, nid)
		if !e.Alive() {
			continue
		}
		if mmokit.Has[gamecomp.Leashing](e) {
			continue
		}
		ai := mmokit.Get[gamecomp.NPCAI](e)
		if ai != nil {
			mmokit.Set(e, gamecomp.Leashing{})
			ai.State = AIStateLeash
			ai.TargetNetID = 0
		}
	}
}

// onClear drops the Flux bounty crate at the POI center and flips the
// component to Cooldown, stamping ClearedAt with server-local time.
// Bounty is scaled by the POI's tier reward multiplier (TierDef.FluxRewardMul):
// T1=1.0x, T2=2.5x, T3=6.0x.
func (s *POISystem) onClear(poi *gamecomp.POI, pos *mmokit.Position, poiNetID uint32) {
	gw := s.gw
	roster := rosterForIdx(poi.RosterDefIdx)
	rosterCount := 0
	for _, m := range roster.Members {
		rosterCount += m.Count
	}
	rewardMul := tierDef(poi.Tier).FluxRewardMul
	if rewardMul <= 0 {
		rewardMul = 1.0
	}
	base := gw.Config.POIBaseClearFlux + gw.Config.POIPerKillFluxBonus*int32(rosterCount)
	bounty := int32(float32(base) * rewardMul)
	gw.SpawnLootCrate(pos.X, pos.Y, map[uint32]int32{
		item.CreditsItemID: bounty,
	})

	poi.Status = gamecomp.POIStatusCooldown
	poi.ClearedAt = time.Now().UnixNano()
	gw.eng.Log.Log(CatPOI, "poi: cleared netID=%d tier=%d bounty=%d roster=%s",
		poiNetID, poi.Tier, bounty, roster.Name)
}

// tickCooldown reports whether the cooldown has elapsed; the caller
// runs repopulate AFTER the entities.Iter query closes (spawning NPCs
// inside the iteration would trip ark's locked-world check).
func (s *POISystem) tickCooldown(poi *gamecomp.POI, pos *mmokit.Position, poiNetID uint32, now int64) bool {
	cooldownSec := poiCooldownSec(s.gw, poi.Tier)
	elapsed := now - poi.ClearedAt
	return elapsed >= int64(cooldownSec)*int64(time.Second)
}

// repopulate spawns a fresh roster around the POI and flips it back
// to Active. Must run outside the entities.Iter query.
func (s *POISystem) repopulate(poi *gamecomp.POI, pos *mmokit.Position, poiNetID uint32) {
	gw := s.gw
	gw.spawnPOIRoster(pos.X, pos.Y, poiNetID, poi.RosterDefIdx, poi.Tier)
	gw.poiRosters[poiNetID] = gw.collectRosterNetIDs(poiNetID)
	poi.Status = gamecomp.POIStatusActive
	gw.eng.Log.Log(CatPOI, "poi: repopulated netID=%d tier=%d roster_size=%d",
		poiNetID, poi.Tier, len(gw.poiRosters[poiNetID]))
}

// poiCooldownSec returns the cooldown duration (seconds) for POIs in
// this world's root cell. The station cell uses the shorter
// tutorial-friendly cooldown regardless of tier; outside the station
// cell the tier-table CooldownSec wins (T1=180s, T2=300s, T3=900s),
// falling back to the legacy NonStationCellPOIClearCooldown when the
// tier table doesn't pin a value.
func poiCooldownSec(gw *GameWorld, tier uint8) int32 {
	if gw.RootCell == gw.Config.StationCell {
		return gw.Config.StationCellPOIClearCooldown
	}
	td := tierDef(tier)
	if td.CooldownSec > 0 {
		return td.CooldownSec
	}
	return gw.Config.NonStationCellPOIClearCooldown
}

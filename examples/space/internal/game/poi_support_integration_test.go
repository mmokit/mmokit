package game

import (
	"testing"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// TestPOI_SupportIncreasesTTK is the crown integration test for the
// Support archetype: it validates the entire heal pipeline end-to-end
// (Support targets lowest-HP ally → verb_heal applies → Health.Current
// restored → roster takes longer to die).
//
// It compares two scenarios that differ ONLY in whether a Support NPC
// is attached to an otherwise-identical T1 Starter Arena roster:
//   - control:   spawn POI, apply DPS to roster until fully dead
//   - support:   spawn POI + Support, apply DPS to roster until fully dead
//
// The Support roster MUST take strictly more ticks to die than control.
//
// Two-world setup (rather than two POIs in the same world) avoids the
// shared-world confound where both rosters would share the same ECS
// tick budget and the same Support entity could heal nothing or both
// pools depending on iteration order.
func TestPOI_SupportIncreasesTTK(t *testing.T) {
	tickA := runRosterTTK(t /*withSupport=*/, false /*dps=*/, 30)
	tickB := runRosterTTK(t /*withSupport=*/, true /*dps=*/, 30)

	if tickB <= tickA {
		t.Fatalf("Support roster TTK %d not greater than control %d", tickB, tickA)
	}
	t.Logf("control TTK=%d ticks, support TTK=%d ticks (delta=%d)", tickA, tickB, tickB-tickA)
}

// runRosterTTK builds a fresh game world, spawns a T1 POI (optionally
// with a Support NPC attached), and applies dps damage spread across
// living roster members each tick until the roster is fully dead.
// Returns the tick count.
//
// gw.poiRosters[poiNetID] is captured at SpawnPOI time and therefore
// contains ONLY the Starter Arena members (Artillery/Brawler/Lancer) —
// the Support is spawned by a separate SpawnNPC AFTER SpawnPOI, so it
// never enters this slice in this test. That keeps DPS distributed
// across the rostered enemies only (not diluted into Support's HP
// pool), which is the correct measurement: "does Support heal Starter
// Arena members so they live longer?".
func runRosterTTK(t *testing.T, withSupport bool, dps float32) int {
	t.Helper()
	gw, _ := newTestGameWorld()
	defer gw.Shutdown()
	// Register the heal verb so Support actually heals — newTestGameWorld
	// doesn't go through GameSetup which wires RegisterHealVerb.
	mmokit.Handle(gw.stage, healHandler)
	sys, _ := newWiredNPCAISystem(t, gw)

	poiNetID := gw.SpawnPOIWithRoster(500, 500, 0, StarterArenaIdx, 1)
	if withSupport {
		gw.SpawnNPC(500, 500, ArchetypeSupport, poiNetID, NPCSpawnModifiers{})
	}

	const dt float32 = 0.05
	for i := 0; i < 10000; i++ {
		var aliveIDs []uint32
		for _, nid := range gw.poiRosters[poiNetID] {
			e := mmokit.EntityByNetID(gw.stage, nid)
			if !e.Alive() {
				continue
			}
			// Defensive filter: even though Support is spawned after
			// the roster slice is captured (and so shouldn't appear
			// here), be explicit — if anyone ever changes spawnPOIRoster
			// to include Support, the test should remain measuring
			// "Starter Arena dies slower" rather than silently shifting
			// to "Support absorbs DPS".
			if ai := mmokit.Get[gamecomp.NPCAI](e); ai != nil && ai.Archetype == ArchetypeSupport {
				continue
			}
			h := mmokit.Get[gamecomp.Health](e)
			if h == nil || h.Current <= 0 {
				continue
			}
			aliveIDs = append(aliveIDs, nid)
		}
		if len(aliveIDs) == 0 {
			return i
		}
		per := dps * dt / float32(len(aliveIDs))
		for _, nid := range aliveIDs {
			e := mmokit.EntityByNetID(gw.stage, nid)
			h := mmokit.Get[gamecomp.Health](e)
			h.Current -= per
		}
		gw.stage.TickOne(sys, dt)
	}
	t.Fatalf("runRosterTTK exceeded 10k ticks (withSupport=%v)", withSupport)
	return 0
}

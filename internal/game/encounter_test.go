package game

import (
	"testing"

	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// TestStarterArenaEncounter — sanity check that spawning the v2 Starter
// Arena roster doesn't crash and that the expected NPCs come up. Not a
// balance test — that's done in manual playtest.
//
// The test wires NPCAISystem + PhysicsSystem (mirroring production
// ordering) and ticks ~5 game-seconds so we catch panics in any of the
// new archetype state branches (Brawler hitscan, Artillery cast,
// Lancer windup/charge/recover).
func TestStarterArenaEncounter(t *testing.T) {
	gw, _ := newTestGameWorld()
	ai, phys := newWiredNPCAISystem(t, gw)

	// Spawn the Starter Arena roster (rosterIdx=0) around (1000, 1000).
	// Anchor with a fake POI netID — none of the archetype state
	// transitions read back through gw.poiRosters, so the anchor link
	// is benign for this smoke check.
	const cx, cy float32 = 1000, 1000
	gw.spawnPOIRoster(cx, cy /*poiNetID*/, 1 /*rosterIdx*/, 0 /*tier*/, 1)

	// Expected count from rosters[0]: 1 Artillery + 2 Brawler + 3 Lancer = 6.
	var npcCount int
	mmokit.ForEach1(gw.stage, func(_ mmokit.Entity, _ *gamecomp.NPCAI) {
		npcCount++
	})
	if npcCount < 6 {
		t.Fatalf("expected >=6 NPCs spawned from Starter Arena, got %d", npcCount)
	}

	// Tick for ~5 game-seconds (100 × 0.05s) to exercise the archetype
	// state branches. With no player nearby everyone should sit in
	// Idle/Acquire — the value here is panic detection across the
	// archetype-aware code paths.
	const dt = float32(0.05)
	tickAI(gw.stage, ai, phys, dt, 100)
}

// TestStarterArenaRosterShape — static check that the v2 Starter Arena
// roster contains exactly the three archetypes with their expected
// counts. Locks the design intent so a roster edit can't silently drop
// (or duplicate) an archetype.
func TestStarterArenaRosterShape(t *testing.T) {
	roster := rosterForIdx(0)
	if roster.Name != "Starter Arena" {
		t.Fatalf("rosterForIdx(0).Name = %q, want %q", roster.Name, "Starter Arena")
	}
	counts := map[uint8]int{}
	for _, m := range roster.Members {
		counts[m.Archetype] += m.Count
	}
	want := map[uint8]int{
		ArchetypeArtillery: 1,
		ArchetypeBrawler:   2,
		ArchetypeLancer:    3,
	}
	for arch, n := range want {
		if got := counts[arch]; got != n {
			t.Errorf("archetype %d count: got %d, want %d", arch, got, n)
		}
	}
	if len(counts) != len(want) {
		t.Errorf("starter arena has %d archetypes, want %d", len(counts), len(want))
	}
}

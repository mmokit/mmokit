package game

import (
	"testing"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// TestSpawnPOIWithRoster_AppliesTierStatMultiplier verifies that POIs
// spawned at a given tier scale their roster's HP per TierDef's
// StatMultiplier. This is the lone remaining test from the
// pre-world-editor poi_gen test file — the procgen path itself is
// gone (placement is hand-authored in world/pois.json), but the
// spawn-time tier scaling is still the contract callers rely on.
func TestSpawnPOIWithRoster_AppliesTierStatMultiplier(t *testing.T) {
	gw, _ := newTestGameWorld()
	baseHP := gw.Config.BrawlerHP

	// Spawn a tier-3 POI; tier label is what matters for the test.
	poiNetID := gw.SpawnPOIWithRoster(100, 100, 0, StarterArenaIdx, 3)

	// Walk roster NPCs anchored to THIS POI; verify Brawler members
	// have HP scaled by T3 mul. The test world spawns a starter
	// dungeon at (0,0) too, so we must filter by anchor.
	t3Mul := tierDef(3).StatMultiplier
	expected := baseHP * t3Mul

	var brawlerFound bool
	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, ai *gamecomp.NPCAI) {
		if ai.Archetype != ArchetypeBrawler {
			return
		}
		anchor := mmokit.Get[gamecomp.DungeonAnchor](e)
		if anchor == nil || anchor.DungeonNetID != poiNetID {
			return
		}
		h := mmokit.Get[gamecomp.Health](e)
		if h == nil {
			return
		}
		if absf(h.Max-expected) > 0.1 {
			t.Fatalf("T3 Brawler Health.Max = %v, want %v", h.Max, expected)
		}
		brawlerFound = true
	})
	if !brawlerFound {
		t.Fatal("no Brawler in spawned roster — fixture issue")
	}
}

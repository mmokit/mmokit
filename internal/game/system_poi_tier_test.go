package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/internal/item"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// TestPOIClear_T1RewardUnscaled verifies that clearing a tier-1 POI
// produces the base bounty (FluxRewardMul = 1.0).
func TestPOIClear_T1RewardUnscaled(t *testing.T) {
	gw, _ := newTestGameWorld()
	defer gw.Shutdown()

	sys := newWiredPOISystem(t, gw)

	poiNetID := gw.SpawnPOIWithRoster(2500, 2500, 0, StarterArenaIdx, 1)
	clearPOIRoster(t, gw, poiNetID)
	gw.stage.TickOne(sys, 0.05)

	bounty, found := lootCrateCreditsNear(gw, 2500, 2500, 5)
	if !found {
		t.Fatal("no loot crate after clear")
	}
	rosterCount := totalRosterMembers(StarterArenaIdx)
	expected := gw.Config.POIBaseClearFlux + gw.Config.POIPerKillFluxBonus*int32(rosterCount)
	if absInt32(bounty-expected) > 1 {
		t.Fatalf("T1 bounty = %d, want %d", bounty, expected)
	}
}

// TestPOIClear_T3RewardScaledBy6x verifies that clearing a tier-3 POI
// produces 6x the base bounty (FluxRewardMul = 6.0).
func TestPOIClear_T3RewardScaledBy6x(t *testing.T) {
	gw, _ := newTestGameWorld()
	defer gw.Shutdown()

	sys := newWiredPOISystem(t, gw)

	poiNetID := gw.SpawnPOIWithRoster(2500, 2500, 0, StarterArenaIdx, 3)
	clearPOIRoster(t, gw, poiNetID)
	gw.stage.TickOne(sys, 0.05)

	bounty, found := lootCrateCreditsNear(gw, 2500, 2500, 5)
	if !found {
		t.Fatal("no loot crate after clear")
	}
	rosterCount := totalRosterMembers(StarterArenaIdx)
	base := gw.Config.POIBaseClearFlux + gw.Config.POIPerKillFluxBonus*int32(rosterCount)
	expected := int32(float32(base) * 6.0)
	tolerance := int32(2) // float rounding
	if absInt32(bounty-expected) > tolerance {
		t.Fatalf("T3 bounty = %d, want ~%d", bounty, expected)
	}
}

// --- Test helpers ---

// clearPOIRoster zeroes the Health of every roster member tracked by
// poiRosters, simulating a full POI clear by the players.
func clearPOIRoster(t *testing.T, gw *GameWorld, poiNetID uint32) {
	t.Helper()
	ids := gw.poiRosters[poiNetID]
	if len(ids) == 0 {
		t.Fatal("POI has empty roster")
	}
	for _, nid := range ids {
		e := mmokit.EntityByNetID(gw.stage, nid)
		if !e.Alive() {
			continue
		}
		h := mmokit.Get[gamecomp.Health](e)
		if h != nil {
			h.Current = 0
		}
	}
}

// lootCrateCreditsNear scans the stage for a LootCrate within eps of
// (x, y) and returns the Credits quantity from its inventory. Items
// live on the Inventory component (LootCrate is a marker).
func lootCrateCreditsNear(gw *GameWorld, x, y, eps float32) (int32, bool) {
	filter := ecs.NewFilter3[mmokit.Position, mmokit.EntityKind, gamecomp.Inventory](gw.stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	for q.Next() {
		p, k, inv := q.Get()
		if k.Type != gamecomp.KindLootCrate {
			continue
		}
		dx, dy := p.X-x, p.Y-y
		if dx*dx+dy*dy > eps*eps {
			continue
		}
		return inv.Items[item.CreditsItemID], true
	}
	return 0, false
}

// totalRosterMembers sums RosterMember.Count across a roster def.
func totalRosterMembers(idx uint16) int {
	r := rosterForIdx(idx)
	n := 0
	for _, m := range r.Members {
		n += m.Count
	}
	return n
}

func absInt32(x int32) int32 {
	if x < 0 {
		return -x
	}
	return x
}

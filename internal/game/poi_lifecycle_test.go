package game

import (
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// newWiredPOISystem wires POISystem against gw so it can be ticked
// directly from tests without a running game loop. Pre-registers
// Leashing on the ECS world — POISystem calls mmokit.Set/Has on the
// Leashing tag and ark panics on the first query for an unregistered
// component type. Mirrors newWiredNPCAISystem in system_npc_ai_test.go.
func newWiredPOISystem(t *testing.T, gw *GameWorld) *POISystem {
	t.Helper()
	w := gw.stage.ECSWorld()
	_ = ecs.NewMap1[gamecomp.Leashing](w) // force component registration

	sys := &POISystem{}
	mmokit.WireSystem(sys, w, gw.eng, gw.stage)
	return sys
}

// TestPOI_ClearSpawnsLootCrate verifies that killing every roster
// member of a POI transitions it to Cooldown and spawns a single
// loot crate at the POI center.
func TestPOI_ClearSpawnsLootCrate(t *testing.T) {
	gw, _ := newTestGameWorld()
	defer gw.Shutdown()

	sys := newWiredPOISystem(t, gw)

	poiNetID := gw.SpawnPOIWithRoster(2000, 2000, gamecomp.POITypeCombat, 0, 1)

	// Kill every roster NPC.
	for _, nid := range gw.poiRosters[poiNetID] {
		e := mmokit.EntityByNetID(gw.stage, nid)
		if h := mmokit.Get[gamecomp.Health](e); h != nil {
			h.Current = 0
		}
	}

	// Tick once to run POISystem.
	gw.stage.TickOne(sys, 0.05)

	poiE := mmokit.EntityByNetID(gw.stage, poiNetID)
	poi := mmokit.Get[gamecomp.POI](poiE)
	if poi == nil {
		t.Fatal("POI component not found")
	}
	if poi.Status != gamecomp.POIStatusCooldown {
		t.Errorf("expected Cooldown, got status=%d", poi.Status)
	}
	if !crateExistsNear(gw, 2000, 2000, 5) {
		t.Errorf("expected loot crate at POI center")
	}
}

// TestPOI_RosterWideLeash verifies that dragging one NPC outside the
// leash radius causes ALL roster members to leash.
func TestPOI_RosterWideLeash(t *testing.T) {
	gw, _ := newTestGameWorld()
	defer gw.Shutdown()
	sys := newWiredPOISystem(t, gw)

	poiNetID := gw.SpawnPOIWithRoster(2000, 2000, gamecomp.POITypeCombat, 0, 1)
	roster := gw.poiRosters[poiNetID]
	if len(roster) < 2 {
		t.Fatalf("expected >=2 roster members, got %d", len(roster))
	}

	// Teleport one member outside the leash radius.
	firstE := mmokit.EntityByNetID(gw.stage, roster[0])
	pos := mmokit.Get[mmokit.Position](firstE)
	pos.X = 2000 + gw.Config.POILeashRadius + 100

	gw.stage.TickOne(sys, 0.05) // POISystem evaluates leash trigger

	// All roster members should now have Leashing.
	for _, nid := range roster {
		e := mmokit.EntityByNetID(gw.stage, nid)
		if !mmokit.Has[gamecomp.Leashing](e) {
			t.Errorf("roster member %d not leashing", nid)
		}
	}
}

// crateExistsNear scans the stage for a LootCrate within eps of (x, y).
func crateExistsNear(gw *GameWorld, x, y, eps float32) bool {
	filter := ecs.NewFilter2[mmokit.Position, mmokit.EntityKind](gw.stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	for q.Next() {
		p, k := q.Get()
		if k.Type != gamecomp.KindLootCrate {
			continue
		}
		dx, dy := p.X-x, p.Y-y
		if dx*dx+dy*dy <= eps*eps {
			return true
		}
	}
	return false
}

// Time-based cooldown-elapsed test omitted: the cooldown uses
// time.Now().UnixNano() which doesn't admit easy fast-forwarding;
// verify cooldown-elapsed path manually or extract a clock interface
// in a future task.
var _ = time.Now

package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// newWiredChamberSystem wires DungeonChamberSystem against gw so it can be
// ticked directly from tests without a running game loop. Pre-registers
// Leashing on the ECS world for the same reason as the POI scaffold —
// downstream component lookups inside the iteration would otherwise trip
// ark's first-touch registration check.
func newWiredChamberSystem(t *testing.T, gw *GameWorld) *DungeonChamberSystem {
	t.Helper()
	w := gw.stage.ECSWorld()
	_ = ecs.NewMap1[gamecomp.Leashing](w)

	sys := &DungeonChamberSystem{}
	mmokit.WireSystem(sys, w, gw.eng, gw.stage)
	return sys
}

// TestChamberSystem_ActiveToClearedOnRosterEmpty drives the full
// Active → Cleared → Cooldown transition by spawning a single mob-pack
// chamber, zeroing every roster member's Health, and ticking the system
// twice. Bypasses SpawnDungeonFromGraph to keep the test isolated from
// procgen layout choices.
func TestChamberSystem_ActiveToClearedOnRosterEmpty(t *testing.T) {
	gw, _ := newTestGameWorld()
	defer gw.Shutdown()

	sys := newWiredChamberSystem(t, gw)

	const dungeonNetID uint32 = 999
	// Build a minimal chamber backed by a real spawned roster — we need
	// the NPCs to exist so the system can prune them.
	state := &ChamberState{
		ID:           1,
		Center:       spatial.Vec2{X: 1000, Y: 1000},
		Radius:       100,
		Role:         ChamberMobPack,
		Status:       ChamberActive,
		RosterDefIdx: 0, // Brawler Pack
	}
	gw.dungeonChambers[dungeonNetID] = map[uint16]*ChamberState{state.ID: state}
	gw.spawnChamberRoster(dungeonNetID, state)
	if len(state.AliveNetIDs) == 0 {
		t.Fatal("expected roster spawn to populate AliveNetIDs")
	}

	// Kill every roster member.
	for _, nid := range state.AliveNetIDs {
		e := mmokit.EntityByNetID(gw.stage, nid)
		if h := mmokit.Get[gamecomp.Health](e); h != nil {
			h.Current = 0
		}
	}

	// Tick once: system should prune the dead and transition Active → Cleared.
	gw.stage.TickOne(sys, 0.05)
	if state.Status != ChamberCleared {
		t.Fatalf("expected Cleared after first tick, got status=%d", state.Status)
	}
	if state.ClearedAt == 0 {
		t.Errorf("expected ClearedAt to be stamped")
	}

	// Tick again: system should spawn the chest and transition Cleared → Cooldown.
	gw.stage.TickOne(sys, 0.05)
	if state.Status != ChamberCooldown {
		t.Fatalf("expected Cooldown after second tick, got status=%d", state.Status)
	}
	if !crateExistsNear(gw, state.LastKillPos.X, state.LastKillPos.Y, 200) &&
		!crateExistsNear(gw, state.Center.X, state.Center.Y, 200) {
		t.Errorf("expected a loot crate near the chamber after clear")
	}
}

// TestSpawnDungeonFromGraph_PopulatesChambers exercises the full
// procgen orchestration: walls, chamber rosters, and the chamber map.
func TestSpawnDungeonFromGraph_PopulatesChambers(t *testing.T) {
	gw, _ := newTestGameWorld()
	defer gw.Shutdown()

	dungeonNetID := gw.SpawnDungeonFromGraph(0, 0, 12345)
	chambers := gw.dungeonChambers[dungeonNetID]
	if len(chambers) == 0 {
		t.Fatalf("expected chambers populated, got %d", len(chambers))
	}
	for id, c := range chambers {
		if c.Status != ChamberActive {
			t.Errorf("chamber %d: expected Active, got %d", id, c.Status)
		}
		if len(c.AliveNetIDs) == 0 {
			t.Errorf("chamber %d: expected roster spawned (AliveNetIDs)", id)
		}
	}
}

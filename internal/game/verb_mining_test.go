package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestMineExtract_SameCell_ReducesMinable exercises the gw.MineExtract
// entry point against a same-cell asteroid. Same-cell dispatch is
// synchronous: by the time gw.MineExtract returns, mineExtractHandler
// has already mutated Minable.Remaining.
func TestMineExtract_SameCell_ReducesMinable(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.Stage.SetGameWorld(gw)

	// Register the handler manually (production goes through GameSetup
	// → RegisterMiningVerb → HandleAll, but this test bypasses Process
	// and works on a single Stage directly).
	mmokit.Handle(gw.Stage, mineExtractHandler)

	asteroid := newTestAsteroid(t, gw, 301, 100, 1)
	caster := newTestShip(t, gw, 401, 100, 0)

	casterE := mmokit.EntityByNetID(gw.Stage, caster)
	asteroidE := mmokit.EntityByNetID(gw.Stage, asteroid)

	gw.MineExtract(casterE, asteroidE, 0, 25)

	minable := mmokit.Get[gamecomp.Minable](asteroidE)
	if minable == nil {
		t.Fatal("Minable component missing on asteroid")
	}
	if minable.Remaining != 75 {
		t.Fatalf("Minable.Remaining = %v, want 75", minable.Remaining)
	}
}

// TestMineExtract_DepletesAndMarksForRemoval verifies that draining the
// last of an asteroid's reserves drops Remaining to zero, marks the
// entity for removal, and that FlushRemovals tears it down.
func TestMineExtract_DepletesAndMarksForRemoval(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.Stage.SetGameWorld(gw)
	mmokit.Handle(gw.Stage, mineExtractHandler)

	asteroid := newTestAsteroid(t, gw, 302, 10, 1)
	caster := newTestShip(t, gw, 402, 100, 0)

	casterE := mmokit.EntityByNetID(gw.Stage, caster)
	asteroidE := mmokit.EntityByNetID(gw.Stage, asteroid)

	gw.MineExtract(casterE, asteroidE, 0, 25)

	minable := mmokit.Get[gamecomp.Minable](asteroidE)
	if minable == nil {
		t.Fatal("Minable component missing before flush")
	}
	if minable.Remaining != 0 {
		t.Fatalf("Minable.Remaining = %v, want 0 (depleted)", minable.Remaining)
	}

	// The asteroid should be marked for removal. After FlushRemovals it
	// should be gone from the ECS world.
	gw.eng.FlushRemovals()
	if asteroidE.Alive() {
		t.Fatal("expected asteroid to be removed after depletion + flush")
	}
}

// newTestAsteroid spawns an asteroid-like entity directly via ECS for
// unit-test purposes: NetworkID, Position, Minable. The netID is
// registered as Live in the stage's NetID index so EntityByNetID
// resolves to the new entity. Returns the netID so callers can build
// an mmokit.Entity handle.
func newTestAsteroid(t *testing.T, gw *GameWorld, netID uint32, remaining float32, itemID uint32) uint32 {
	t.Helper()
	entity := gw.eng.ECS.NewEntity()
	gw.C.NetworkID.Add(entity, &mmokit.NetworkID{ID: netID})
	gw.C.Position.Add(entity, &mmokit.Position{X: 0, Y: 0})
	gw.C.Minable.Add(entity, &gamecomp.Minable{ItemID: itemID, Remaining: remaining})
	gw.Stage.RegisterLiveNetID(netID, entity)
	gw.NetIDToEntity[netID] = entity
	return netID
}

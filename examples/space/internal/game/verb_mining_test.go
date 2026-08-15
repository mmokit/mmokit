package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
)

// TestMineExtract_SameCell_ReducesMinable exercises the gw.MineExtract
// entry point against a same-cell asteroid. Same-cell dispatch is
// synchronous: by the time gw.MineExtract returns, mineExtractHandler
// has already mutated Minable.Remaining.
func TestMineExtract_SameCell_ReducesMinable(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)

	// Register the handler manually (production goes through GameSetup
	// → RegisterMiningVerb → HandleAll, but this test bypasses Process
	// and works on a single Stage directly).
	mmokit.Handle(gw.stage, mineExtractHandler)

	asteroid := newTestAsteroid(t, gw, 301, 100, 1)
	caster := newTestShip(t, gw, 401, 100, 0)

	casterE := mmokit.EntityByNetID(gw.stage, caster)
	asteroidE := mmokit.EntityByNetID(gw.stage, asteroid)

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
	gw.stage.SetStateByName("game.GameWorld", gw)
	mmokit.Handle(gw.stage, mineExtractHandler)

	asteroid := newTestAsteroid(t, gw, 302, 10, 1)
	caster := newTestShip(t, gw, 402, 100, 0)

	casterE := mmokit.EntityByNetID(gw.stage, caster)
	asteroidE := mmokit.EntityByNetID(gw.stage, asteroid)

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
	w := gw.stage.ECSWorld()
	netIDMapper := ecs.NewMap1[mmokit.NetworkID](w)
	handle := w.NewEntity()
	netIDMapper.Add(handle, &mmokit.NetworkID{ID: netID})
	gw.stage.RegisterLiveNetID(netID, handle)
	e := mmokit.EntityByNetID(gw.stage, netID)
	mmokit.Set(e, mmokit.Position{X: 0, Y: 0})
	mmokit.Set(e, gamecomp.Minable{ItemID: itemID, Remaining: remaining})
	return netID
}

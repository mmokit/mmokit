package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// wireSelectionLOSSystem constructs a freshly-wired SelectionLOSSystem
// against gw.stage so tests can tick it directly without spinning up the
// full game loop.
func wireSelectionLOSSystem(t *testing.T, gw *GameWorld) *SelectionLOSSystem {
	t.Helper()
	sys := &SelectionLOSSystem{}
	mmokit.WireSystem(sys, gw.stage.ECSWorld(), gw.eng, gw.stage)
	return sys
}

// TestSelectionLOS_ClearsOnSustainedLoss — a player whose Selection
// points at a live target must have that selection auto-cleared after
// LockLosLossBreakSec (1.0s default) of continuously blocked LOS. We
// place a LayerStatic wall directly between the player and the target,
// then tick the system past the cutoff and assert EntityNetID == 0.
func TestSelectionLOS_ClearsOnSustainedLoss(t *testing.T) {
	gw, _ := newTestGameWorld()
	sys := wireSelectionLOSSystem(t, gw)

	// Player at origin with a Selection. Use the same minimal scaffold
	// spawnAbilityTestPlayer / newTestPlayerAt use elsewhere; we only need
	// Position + Selection for SelectionLOSSystem.
	w := gw.stage.ECSWorld()
	playerMapper := ecs.NewMap3[mmokit.Position, mmokit.NetworkID, gamecomp.Selection](w)
	const targetNetID = uint32(9101)
	playerHandle := playerMapper.NewEntity(
		&mmokit.Position{X: 0, Y: 0},
		&mmokit.NetworkID{ID: 9100},
		&gamecomp.Selection{EntityNetID: targetNetID},
	)
	gw.stage.RegisterLiveNetID(9100, playerHandle)
	playerE := mmokit.EntityFromECS(gw.stage, playerHandle)

	// Target at (200, 0): far enough out that we can fit a wall between
	// it and the player.
	targetMapper := ecs.NewMap2[mmokit.Position, mmokit.NetworkID](w)
	targetHandle := targetMapper.NewEntity(
		&mmokit.Position{X: 200, Y: 0},
		&mmokit.NetworkID{ID: targetNetID},
	)
	gw.stage.RegisterLiveNetID(targetNetID, targetHandle)

	// Wall at (100, 0): rect 10 wide, 40 tall, LayerStatic — sits on the
	// segment player→target so Raycast against LayerStatic must hit it.
	wallE := w.NewEntity()
	gw.Spatial.Register(spatial.Entry{
		Entity: wallE,
		X:      100, Y: 0,
		Radius: 22, Width: 10, Height: 40,
		Shape: spatial.ShapeRect, Layer: spatial.LayerStatic,
	})

	const dt = float32(0.05)

	// Tick past LockLosLossBreakSec (1.0s default) — we want to be safely
	// past the cutoff. 1.5s = 30 ticks at 50ms each.
	for range 30 {
		gw.stage.TickOne(sys, dt)
	}

	sel := mmokit.Get[gamecomp.Selection](playerE)
	if sel == nil {
		t.Fatal("player lost its Selection component")
	}
	if sel.EntityNetID != 0 {
		t.Errorf("Selection must be cleared after sustained LOS loss; got EntityNetID=%d", sel.EntityNetID)
	}
	if sel.LOSLostAt != 0 {
		t.Errorf("LOSLostAt must reset to 0 after auto-clear; got %.3f", sel.LOSLostAt)
	}
}

// TestSelectionLOS_ResetsLatchOnClearLOS — if LOS clears before the
// cutoff fires, the latch must reset to 0 so a later brief blockage
// doesn't carry over the previous occlusion's time.
func TestSelectionLOS_ResetsLatchOnClearLOS(t *testing.T) {
	gw, _ := newTestGameWorld()
	sys := wireSelectionLOSSystem(t, gw)

	w := gw.stage.ECSWorld()
	playerMapper := ecs.NewMap3[mmokit.Position, mmokit.NetworkID, gamecomp.Selection](w)
	const targetNetID = uint32(9201)
	playerHandle := playerMapper.NewEntity(
		&mmokit.Position{X: 0, Y: 0},
		&mmokit.NetworkID{ID: 9200},
		&gamecomp.Selection{EntityNetID: targetNetID},
	)
	gw.stage.RegisterLiveNetID(9200, playerHandle)
	playerE := mmokit.EntityFromECS(gw.stage, playerHandle)

	targetMapper := ecs.NewMap2[mmokit.Position, mmokit.NetworkID](w)
	targetHandle := targetMapper.NewEntity(
		&mmokit.Position{X: 200, Y: 0},
		&mmokit.NetworkID{ID: targetNetID},
	)
	gw.stage.RegisterLiveNetID(targetNetID, targetHandle)

	// Drop the wall in.
	wallE := w.NewEntity()
	wallEntry := spatial.Entry{
		Entity: wallE,
		X:      100, Y: 0,
		Radius: 22, Width: 10, Height: 40,
		Shape: spatial.ShapeRect, Layer: spatial.LayerStatic,
	}
	gw.Spatial.Register(wallEntry)

	const dt = float32(0.05)

	// Tick 0.5s — half the cutoff — so the latch is engaged but not yet
	// past LockLosLossBreakSec.
	for range 10 {
		gw.stage.TickOne(sys, dt)
	}
	sel := mmokit.Get[gamecomp.Selection](playerE)
	if sel.LOSLostAt == 0 {
		t.Fatal("latch should be engaged mid-occlusion")
	}
	if sel.EntityNetID == 0 {
		t.Fatal("selection should not yet be cleared — only 0.5s of 1.0s elapsed")
	}

	// Remove the wall — LOS clears.
	gw.Spatial.Deregister(wallE)

	// Tick once with clear LOS; latch must reset to 0.
	gw.stage.TickOne(sys, dt)
	sel = mmokit.Get[gamecomp.Selection](playerE)
	if sel.LOSLostAt != 0 {
		t.Errorf("LOSLostAt must reset when LOS becomes clear; got %.3f", sel.LOSLostAt)
	}
	if sel.EntityNetID != targetNetID {
		t.Errorf("Selection must survive — LOS was restored in time; got EntityNetID=%d", sel.EntityNetID)
	}
}
